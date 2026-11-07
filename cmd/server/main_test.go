package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

func TestRunLogsConfigErrorAsJSON(t *testing.T) {
	testRunLogsErrorAsJSON(t, "PORT", "abc", "PORT must be")
}

func TestRunLogsRetryErrorAsJSON(t *testing.T) {
	testRunLogsErrorAsJSON(t, "AWS_MAX_ATTEMPTS", "0", "AWS_MAX_ATTEMPTS must be")
}

func testRunLogsErrorAsJSON(t *testing.T, key, value, want string) {
	t.Helper()
	if os.Getenv("AXIOM_ERROR_TEST") == "1" {
		main()
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$") //nolint:gosec // Reexecute this test binary to exercise process startup.
	cmd.Env = append(os.Environ(), "AXIOM_ERROR_TEST=1", "AXIOM_AUTH_DISABLED=true", "AXIOM_INTERNAL_TOKEN=", "PORT=8080", "AWS_MAX_ATTEMPTS=3", "AWS_RETRY_MODE=standard", key+"="+value, "BAML_LOG=off")
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1, got %v: %s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one JSON line, got %q", output)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("expected JSON record: %v: %s", err, output)
	}
	if record["level"] != "ERROR" || record["msg"] != "server.failed" || !strings.Contains(fmt.Sprint(record["error"]), want) {
		t.Fatalf("unexpected failure record: %v", record)
	}
}

func TestStartupRecordsPrecedeServing(t *testing.T) {
	testSecondSignalStopsServer(t, false)
}

func TestRunRejectsInvalidRetrySettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"AWS_MAX_ATTEMPTS", ""}, {"AWS_MAX_ATTEMPTS", "abc"}, {"AWS_MAX_ATTEMPTS", "0"},
		{"AWS_MAX_ATTEMPTS", "-1"}, {"AWS_MAX_ATTEMPTS", "1.5"}, {"AWS_MAX_ATTEMPTS", " 3"},
		{"AWS_MAX_ATTEMPTS", "4294967296"},
		{"AWS_RETRY_MODE", ""}, {"AWS_RETRY_MODE", "legacy"}, {"AWS_RETRY_MODE", "bogus"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv("AXIOM_AUTH_DISABLED", "true")
			t.Setenv("AXIOM_INTERNAL_TOKEN", "")
			t.Setenv("AWS_MAX_ATTEMPTS", "3")
			t.Setenv("AWS_RETRY_MODE", "standard")
			t.Setenv(tc.key, tc.value)
			if err := run([]string{"unexpected"}, io.Discard); err == nil || !strings.Contains(err.Error(), tc.key+" must be") {
				t.Fatalf("expected retry configuration error before startup, got %v", err)
			}
		})
	}
}

func TestRunAcceptsRetrySettings(t *testing.T) {
	for _, tc := range []struct{ attempts, mode string }{
		{"unset", "unset"}, {"1", "standard"}, {"5", "adaptive"}, {"4294967295", "standard"},
	} {
		t.Run(tc.attempts+"/"+tc.mode, func(t *testing.T) {
			t.Setenv("AXIOM_AUTH_DISABLED", "true")
			t.Setenv("AXIOM_INTERNAL_TOKEN", "")
			for key, value := range map[string]string{"AWS_MAX_ATTEMPTS": tc.attempts, "AWS_RETRY_MODE": tc.mode} {
				t.Setenv(key, value)
				if value == "unset" {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := run([]string{"unexpected"}, io.Discard); err == nil || err.Error() != "usage: axiom-server [healthcheck]" {
				t.Fatalf("expected argument validation after configuration, got %v", err)
			}
		})
	}
}

func TestSecondSignalStopsServer(t *testing.T) {
	testSecondSignalStopsServer(t, false)
}

func TestBackToBackSignalsStopServer(t *testing.T) {
	testSecondSignalStopsServer(t, true)
}

func testSecondSignalStopsServer(t *testing.T, backToBack bool) {
	t.Helper()
	if os.Getenv("AXIOM_SIGNAL_TEST") == "1" {
		if err := run(nil, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	const shutdownTimeout = 25 * time.Second
	// Finish before the incomplete body's read timeout or the shutdown budget.
	const waitTimeout = shutdownTimeout / 4
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$") //nolint:gosec // Reexecute this test binary to exercise process signals.
	cmd.Env = append(os.Environ(), "AXIOM_SIGNAL_TEST=1", "AXIOM_AUTH_DISABLED=true", "AXIOM_INTERNAL_TOKEN=", "PORT="+port, "SHUTDOWN_TIMEOUT="+shutdownTimeout.String(), "SHUTDOWN_DRAIN_DELAY=0s", "GRAPH_FILE=", "LOG_LEVEL=info", "AWS_MAX_ATTEMPTS=3", "AWS_RETRY_MODE=standard")
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	type shutdownRecord struct {
		Message string `json:"msg"`
		Address string `json:"address"`
		Level   string `json:"level"`
		Signal  string `json:"signal"`
		Timeout any    `json:"timeout"`
	}
	shutdownStarted := make(chan shutdownRecord, 1)
	startupRecords := make(chan shutdownRecord, 3)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var record shutdownRecord
			if json.Unmarshal(scanner.Bytes(), &record) != nil {
				continue
			}
			if t.Name() == "TestStartupRecordsPrecedeServing" && (record.Message == "server.started" || record.Message == "server.listening" || record.Message == "http.access") {
				select {
				case startupRecords <- record:
				default:
				}
			}
			if record.Message == "server.shutdown_started" {
				select {
				case shutdownStarted <- record:
				default:
				}
			}
		}
		close(shutdownStarted)
		done <- errors.Join(cmd.Wait(), scanner.Err())
	}()
	waited := false
	defer func() {
		_ = cmd.Process.Kill()
		if !waited {
			if err := <-done; err == nil {
				t.Error("child exited without a signal")
			}
		}
	}()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	defer client.CloseIdleConnections()
	probe := func() error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+"/health", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	deadline := time.Now().Add(waitTimeout)
	for probe() != nil {
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if t.Name() == "TestStartupRecordsPrecedeServing" {
		for _, want := range []string{"server.started", "server.listening"} {
			select {
			case got := <-startupRecords:
				if got.Message != want {
					t.Fatalf("startup message = %q, want %q", got.Message, want)
				}
				if got.Message == "server.listening" {
					host, boundPort, err := net.SplitHostPort(got.Address)
					if err != nil {
						t.Fatalf("invalid listener address %q: %v", got.Address, err)
					}
					if boundPort != port || net.ParseIP(host) == nil {
						t.Fatalf("listener address = %q, want IP host and port %s", got.Address, port)
					}
				}
			case <-time.After(waitTimeout):
				t.Fatalf("missing %s before serving", want)
			}
		}
	}
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "POST /api/v1/adaptive-plan HTTP/1.1\r\nHost: localhost\r\nContent-Length: 1\r\nExpect: 100-continue\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(waitTimeout)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "HTTP/1.1 100 Continue\r\n" {
		t.Fatalf("continue response=%q error=%v", line, err)
	}
	// Keep an incomplete body open while the listener closes.
	deadline = time.Now().Add(waitTimeout)
	shutdownTimer := time.NewTimer(waitTimeout)
	defer shutdownTimer.Stop()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if backToBack {
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
	} else {
		select {
		case record, ok := <-shutdownStarted:
			if !ok {
				t.Fatal("child output closed before shutdown started")
			}
			if record.Level != "INFO" || record.Signal != "terminated" || record.Timeout != "25s" {
				t.Fatalf("unexpected shutdown record: %+v", record)
			}
		case <-shutdownTimer.C:
			t.Fatal("server did not restore signal defaults before shutdown")
		}
		for probe() == nil {
			if time.Now().After(deadline) {
				t.Fatal("listener stayed open after termination signal")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		waited = true
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected signal exit, got %v", err)
		}
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok {
			t.Fatalf("expected signal status, got %v", err)
		}
		if backToBack {
			// Pending signals can be delivered in either order.
			if status.Signal() != syscall.SIGINT && status.Signal() != syscall.SIGTERM {
				t.Fatalf("expected interrupt or termination exit, got %v", err)
			}
		} else if status.Signal() != syscall.SIGINT {
			t.Fatalf("expected interrupt exit, got %v", err)
		}
	case <-shutdownTimer.C:
		t.Fatal("second signal did not stop the process")
	}
}
func TestStartupRecordOmitsSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, auth, graphSource string
		disabled, previous      bool
	}{
		{name: "token", auth: "token", graphSource: "embedded"},
		{name: "rotation", auth: "token+previous", graphSource: "file", previous: true},
		{name: "disabled", auth: "disabled", graphSource: "embedded", disabled: true, previous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				LLM:  config.LLM{Provider: "aws-bedrock", Model: "amazon.nova-micro-v1:0"},
				Port: "8081", AuthDisabled: tc.disabled,
				InternalToken:  "current-secret-0123456789abcdef-0123456789",
				SwaggerEnabled: true, MetricsEnabled: true,
				RateLimitMax: 70, RateLimitWindow: 30 * time.Second,
				RequestTimeout: 20 * time.Second, LLMTimeout: 15 * time.Second,
				LLMQueueTimeout: 2 * time.Second, ShutdownTimeout: 25 * time.Second,
				ShutdownDrainDelay: time.Second, LLMMaxConcurrency: 12, MaxFailedTopics: 8,
				CORSAllowedOrigins: []string{"https://private.example"}, TrustedProxies: []string{"10.0.0.1", "10.0.0.2"},
			}
			if tc.previous {
				cfg.InternalTokenPrevious = "previous-secret-0123456789abcdef-0123456789"
			}
			if tc.graphSource == "file" {
				cfg.GraphFile = "/private/graph.json"
			}
			g, err := graph.Decode(strings.NewReader(`[{"subject":"A","relation":"depends_on","object":"B"}]`))
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			logStartup(slog.New(slog.NewJSONHandler(&output, nil)), cfg, g, func(key string) string {
				if key == "AWS_REGION" {
					return "us-east-1"
				}
				return ""
			}, func(string) ([]byte, error) {
				t.Fatal("region environment should avoid reading configuration files")
				return nil, os.ErrNotExist
			})
			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]any{
				"level": "INFO", "msg": "server.started", "port": "8081", "auth": tc.auth,
				"swagger_enabled": true, "metrics_enabled": true,
				"rate_limit_max": float64(70), "rate_limit_window": "30s",
				"request_timeout": "20s", "llm_timeout": "15s", "llm_queue_timeout": "2s",
				"shutdown_timeout": "25s", "shutdown_drain_delay": "1s",
				"llm_max_concurrency": float64(12), "max_failed_topics": float64(8),
				"cors_origins_count": float64(1), "trusted_proxies_count": float64(2),
				"graph_source": tc.graphSource, "graph_tuples": float64(1), "aws_region_source": "AWS_REGION",
			} {
				if got := record[key]; got != want {
					t.Errorf("%s = %v, want %v", key, got, want)
				}
			}
			if version, ok := record["baml_version"].(string); !ok || version == "" {
				t.Errorf("missing BAML version: %v", record["baml_version"])
			}
			for _, secret := range []string{cfg.InternalToken, cfg.InternalTokenPrevious, "https://private.example", "10.0.0.1", "/private/graph.json"} {
				if secret != "" && strings.Contains(output.String(), secret) {
					t.Errorf("startup output contains %q", secret)
				}
			}
		})
	}
}

func TestStartupWarnsWithoutRegion(t *testing.T) {
	for _, metadata := range []string{"", "true", "false"} {
		t.Run("metadata="+metadata, func(t *testing.T) {
			var output bytes.Buffer
			logStartup(slog.New(slog.NewJSONHandler(&output, nil)), config.Config{LLM: config.LLM{Provider: "aws-bedrock", Model: "amazon.nova-micro-v1:0"}}, graph.NewInMemoryGraph(), func(key string) string {
				if key == "AWS_EC2_METADATA_DISABLED" {
					return metadata
				}
				return ""
			}, func(string) ([]byte, error) { return nil, os.ErrNotExist })
			decoder := json.NewDecoder(&output)
			var started, warning map[string]any
			if err := decoder.Decode(&started); err != nil {
				t.Fatal(err)
			}
			if started["aws_region_source"] != "" {
				t.Errorf("region source = %v, want empty", started["aws_region_source"])
			}
			if err := decoder.Decode(&warning); err != nil {
				t.Fatalf("missing region warning: %v", err)
			}
			wantMetadata := "enabled"
			if metadata == "true" {
				wantMetadata = "disabled"
			}
			if warning["msg"] != "llm.region.unresolved" || warning["level"] != "WARN" || warning["instance_metadata"] != wantMetadata {
				t.Errorf("unexpected warning: %v", warning)
			}
			if !strings.Contains(fmt.Sprint(warning["detail"]), "instance metadata may still supply a region") {
				t.Errorf("warning does not explain region fallback: %v", warning)
			}
			if err := decoder.Decode(&warning); err != io.EOF {
				t.Errorf("unexpected extra output: %v", err)
			}
		})
	}
}

func TestRunRejectsInvalidBAMLLog(t *testing.T) {
	for _, value := range []string{"", " off", "none", "bogus"} {
		t.Run("BAML_LOG="+value, func(t *testing.T) {
			t.Setenv("AXIOM_AUTH_DISABLED", "true")
			t.Setenv("AXIOM_INTERNAL_TOKEN", "")
			t.Setenv("AWS_MAX_ATTEMPTS", "3")
			t.Setenv("AWS_RETRY_MODE", "standard")
			t.Setenv("BAML_LOG", value)
			var output bytes.Buffer
			err := run([]string{"unexpected"}, &output)
			const want = "BAML_LOG must be off, error, warn, warning, info, debug or trace"
			if err == nil || err.Error() != want {
				t.Fatalf("expected logging configuration error before startup, got %v", err)
			}
			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
				t.Fatal(err)
			}
			if record["msg"] != "server.failed" || record["level"] != "ERROR" || record["error"] != want {
				t.Fatalf("unexpected failure record: %v", record)
			}
		})
	}
}

func TestOtherProvidersIgnoreAWSSettings(t *testing.T) {
	t.Setenv("AXIOM_AUTH_DISABLED", "true")
	t.Setenv("AXIOM_INTERNAL_TOKEN", "")
	t.Setenv("LLM_PROVIDER", "ollama")
	t.Setenv("LLM_MODEL", "local-planner")
	t.Setenv("AWS_MAX_ATTEMPTS", "invalid")
	if err := run([]string{"unexpected"}, io.Discard); err == nil || err.Error() != "usage: axiom-server [healthcheck]" {
		t.Fatalf("startup error=%v", err)
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logStartup(slog.New(slog.NewJSONHandler(&output, nil)), cfg, graph.NewInMemoryGraph(), os.Getenv, os.ReadFile)
	if strings.Contains(output.String(), "region.unresolved") || !strings.Contains(output.String(), `"llm_provider":"ollama"`) || !strings.Contains(output.String(), `"llm_model":"local-planner"`) {
		t.Fatalf("startup=%s", output.String())
	}
}
