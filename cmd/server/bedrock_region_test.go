package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

func TestBedrockRegionRejectedBeforeStartup(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(endpoint.Close)
	dir := t.TempDir()
	awsConfig := filepath.Join(dir, "config")
	if err := os.WriteFile(awsConfig, []byte("[default]\nregion = eu-west-2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []struct {
		name string
		args []string
	}{{"http", nil}, {"mcp", []string{"mcp"}}} {
		t.Run(mode.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			env := append(graphProcessEnv(),
				"AXIOM_AUTH_DISABLED=true", "BEDROCK_REQUIRE_EXPLICIT_REGION=true",
				"GRAPH_FILE="+filepath.Join(dir, "missing-graph"),
				"AWS_CONFIG_FILE="+awsConfig, "AWS_SHARED_CREDENTIALS_FILE="+filepath.Join(dir, "missing-credentials"),
				"AWS_EC2_METADATA_SERVICE_ENDPOINT="+endpoint.URL, "AWS_ENDPOINT_URL_BEDROCK_RUNTIME="+endpoint.URL)
			cmd := serverCommand(ctx, env, mode.args...)
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("expected exit 1, got %v: %s", err, output)
			}
			var record struct {
				Message string `json:"msg"`
				Error   string `json:"error"`
			}
			if err := json.Unmarshal(output, &record); err != nil || record.Message != "server.failed" || !strings.HasPrefix(record.Error, "AWS_REGION ") {
				t.Fatalf("expected a region error before loading the graph, got %s", output)
			}
			if count := calls.Load(); count != 0 {
				t.Errorf("startup contacted the fixture %d times", count)
			}
		})
	}
}

func TestBedrockStartupAndGraphTrafficStayOffline(t *testing.T) {
	t.Setenv("BEDROCK_REQUIRE_EXPLICIT_REGION", "true")
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(endpoint.Close)
	settings := filepath.Join(t.TempDir(), "aws-settings")
	if err := os.WriteFile(settings, nil, 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("fixture-", 4)
	for _, tc := range []struct{ name, strict, region, disabled, source, metadata string }{
		{"strict", "true", "us-gov-west-1", "false", "AWS_REGION", ""},
		{"IMDS enabled", "false", "", "false", "", "enabled"},
		{"IMDS disabled", "false", "", "true", "", "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"http", "mcp"} {
				t.Run(mode, func(t *testing.T) {
					env := graphProcessEnv()
					if mode == "mcp" {
						env = mcpFixtureEnv(t)
					}
					env = append(env, "AXIOM_INTERNAL_TOKEN="+token, "BAML_LOG=off", "GRAPH_FILE=", "LOG_LEVEL=info",
						"AWS_CONFIG_FILE="+settings, "AWS_SHARED_CREDENTIALS_FILE="+settings,
						"AWS_EC2_METADATA_SERVICE_ENDPOINT="+endpoint.URL, "AWS_ENDPOINT_URL_BEDROCK_RUNTIME="+endpoint.URL,
						"AWS_EC2_METADATA_DISABLED="+tc.disabled)
					if tc.strict == "true" {
						env = append(env, "BEDROCK_REQUIRE_EXPLICIT_REGION=true", "AWS_REGION="+tc.region)
					}
					var logs string
					if mode == "http" {
						logs = checkHTTPGraphStartup(t, env, token)
					} else {
						mcp := startMCPProcess(t, serverCommand(t.Context(), env, "mcp"))
						mcp.initialize(t)
						result := mcp.exchange(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_topics","arguments":{"q":"recursion"}}}`, 2)
						if result["isError"] == true || !strings.Contains(fmt.Sprint(result["structuredContent"]), "Recursion") {
							t.Errorf("list_topics result = %v", result)
						}
						mcp.stop(t, nil)
						logs = mcp.logs.text()
					}
					if !strings.Contains(logs, `"msg":"server.started"`) || !strings.Contains(logs, `"aws_region_source":"`+tc.source+`"`) || strings.Contains(logs, token) {
						t.Errorf("startup log = %s", logs)
					}
					warning := strings.Contains(logs, `"msg":"llm.region.unresolved"`)
					if warning != (tc.metadata != "") || (warning && !strings.Contains(logs, `"instance_metadata":"`+tc.metadata+`"`)) {
						t.Errorf("region warning = %s", logs)
					}
					if count := calls.Load(); count != 0 {
						t.Errorf("startup or graph traffic contacted the AWS fixture %d times", count)
					}
				})
			}
		})
	}
}

func checkHTTPGraphStartup(t *testing.T, env []string, token string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_, port, err := net.SplitHostPort(addr)
	if closeErr := listener.Close(); closeErr != nil || err != nil {
		t.Fatalf("listener: split=%v close=%v", err, closeErr)
	}
	cmd := serverCommand(ctx, append(env, "PORT="+port))
	logs := &processRecords{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	get := func(path string) (int, string, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			return 0, "", err
		}
		request.Header.Set("X-Internal-Token", token)
		response, err := client.Do(request)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		return response.StatusCode, string(body), err
	}
	for {
		if status, _, err := get("/health"); err == nil && status == http.StatusOK {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("HTTP startup: %v logs=%s", ctx.Err(), logs.text())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if status, body, err := get("/readyz"); status != http.StatusOK || err != nil {
		t.Errorf("readiness: status=%d body=%s error=%v", status, body, err)
	}
	if status, body, err := get("/api/v1/topics?q=recursion"); status != http.StatusOK || err != nil || !strings.Contains(body, "Recursion") {
		t.Errorf("topics: status=%d body=%s error=%v", status, body, err)
	}
	return logs.text()
}

func TestStrictBedrockStartupReportsValidatedSource(t *testing.T) {
	env := map[string]string{
		"AXIOM_AUTH_DISABLED": "true", "BEDROCK_REQUIRE_EXPLICIT_REGION": "true", "AWS_REGION": "us-gov-west-1",
	}
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logStartup(slog.New(slog.NewJSONHandler(&output, nil)), cfg, graph.NewInMemoryGraph(), func(key string) string {
		t.Fatalf("unexpected startup diagnostic environment lookup: %s", key)
		return ""
	}, func(path string) ([]byte, error) {
		t.Fatalf("unexpected startup diagnostic file read: %s", path)
		return nil, os.ErrNotExist
	})
	var record struct {
		Message string `json:"msg"`
		Source  string `json:"aws_region_source"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil || record.Message != "server.started" || record.Source != "AWS_REGION" {
		t.Fatalf("unexpected startup diagnostic: %s", &output)
	}
}
