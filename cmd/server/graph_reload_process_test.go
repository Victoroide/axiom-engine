//go:build linux || darwin

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/Victoroide/axiom-engine/test/fakebedrock"
	"github.com/Victoroide/axiom-engine/test/mcpfixture"
)

const graphReloadTestProcess = "AXIOM_GRAPH_RELOAD_TEST_PROCESS"
const graphReloadTestIssuerCA = "AXIOM_GRAPH_RELOAD_TEST_ISSUER_CA"

func TestGraphReloadProcessEntry(t *testing.T) {
	switch mode := os.Getenv(graphReloadTestProcess); mode {
	case "http-default-hup", "http-ignore-hup", "mcp-http-default-hup":
		if mode == "http-ignore-hup" {
			signal.Ignore(syscall.SIGHUP)
		} else {
			// Exec resets a caught signal to the default even if this process inherited SIG_IGN.
			signal.Notify(make(chan os.Signal, 1), syscall.SIGHUP)
		}
		target := "http"
		if mode == "mcp-http-default-hup" {
			target = "mcp-http"
		}
		t.Setenv(graphReloadTestProcess, target)
		t.Fatal(syscall.Exec(os.Args[0], os.Args, os.Environ())) //nolint:gosec // Re-execute this test binary with a controlled signal disposition.
	case "http":
		if run(nil, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "mcp":
		if run([]string{"mcp"}, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "mcp-http":
		// Use the local issuer CA for the verifier in this test process.
		pool := graphReloadCertPool(t, os.Getenv(graphReloadTestIssuerCA))
		http.DefaultTransport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
		if run([]string{"mcp-http"}, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func reloadCommand(t *testing.T, mode, path, enabled string, env ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGraphReloadProcessEntry$") //nolint:gosec // Run the process entry in this test binary.
	cmd.Env = slices.DeleteFunc(mcpFixtureEnv(t), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return isHTTPAuthSetting(key)
	})
	cmd.Env = append(cmd.Env, graphReloadTestProcess+"="+mode, "GRAPH_RELOAD_ENABLED="+enabled, "GRAPH_FILE="+path,
		"GORACE=atexit_sleep_ms=0", "AXIOM_AUTH_DISABLED=true", "AXIOM_INTERNAL_TOKEN=", "AXIOM_INTERNAL_TOKEN_PREVIOUS=", "BAML_LOG=off",
		"LOG_LEVEL=info", "METRICS_ENABLED=true", "REDIS_URL=", "RESOURCE_LINK_CHECK=false", "RATE_LIMIT_MAX=50", "SHUTDOWN_DRAIN_DELAY=0s", "SHUTDOWN_TIMEOUT=11s",
		"AWS_REGION=us-east-1", "AWS_RETRY_MODE=standard", "AWS_MAX_ATTEMPTS=1")
	cmd.Env = append(cmd.Env, env...)
	return cmd
}

func assertGraphReloadLog(t *testing.T, logs *processRecords, outcome, revision string, published, failed int) {
	t.Helper()
	record := awaitGraphProcessLog(t, logs, "graph.reload")
	if record["outcome"] != outcome || record["graph_revision"] != revision || record["published_total"] != float64(published) || record["failed_total"] != float64(failed) {
		t.Fatalf("graph.reload=%v, want %s %s %d %d", record, outcome, revision, published, failed)
	}
}

func awaitGraphProcessLog(t *testing.T, logs *processRecords, message string) map[string]any {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case record := <-logs.messages:
			if record["msg"] == message {
				return record
			}
		case <-timer.C:
			t.Fatalf("waiting for %s: %s", message, logs.text())
		}
	}
}

type graphHTTPReplica struct {
	cmd     *exec.Cmd
	logs    *processRecords
	url     string
	exited  chan struct{}
	waitErr error
}

func startGraphHTTPReplica(t *testing.T, mode, path, enabled, listener string) *graphHTTPReplica {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := reloadCommand(t, mode, path, enabled, fmt.Sprintf("PORT=%d", addr.Port), "HTTP_LISTENER="+listener)
	return startGraphProcess(t, cmd, "http://"+addr.String())
}

func startGraphProcess(t *testing.T, cmd *exec.Cmd, url string) *graphHTTPReplica {
	t.Helper()
	logs := &processRecords{messages: make(chan map[string]any, 64), shutdown: make(chan struct{})}
	r := &graphHTTPReplica{cmd: cmd, logs: logs, url: url, exited: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { r.waitErr = cmd.Wait(); close(r.exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-r.exited })
	return r
}

func (r *graphHTTPReplica) signal(t *testing.T, sig os.Signal) {
	t.Helper()
	if err := r.cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
}

func (r *graphHTTPReplica) wait(t *testing.T) error {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case <-r.exited:
		return r.waitErr
	case <-timer.C:
		t.Fatalf("server did not exit: %s", r.logs.text())
		return nil
	}
}

func (r *graphHTTPReplica) get(t *testing.T, path string) string {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, r.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: response=%d body=%s err=%v", path, resp.StatusCode, body, err)
	}
	return string(body)
}

func TestGraphReloadDefaultStartupOnly(t *testing.T) {
	for _, mode := range []string{"http-default-hup", "http-ignore-hup"} {
		t.Run(mode, func(t *testing.T) { testGraphReloadDefaultStartupOnly(t, mode) })
	}
}

func testGraphReloadDefaultStartupOnly(t *testing.T, mode string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "graph.json")
	replaceCurriculum(t, path, curriculumJSON("Old Root"))
	r := startGraphHTTPReplica(t, mode, path, "", "")
	awaitGraphProcessLog(t, r.logs, "server.listening")
	const route = "/api/v1/topics/Recursion/path"
	before := r.get(t, route)
	if !strings.Contains(before, `"Old Root"`) {
		t.Fatal(before)
	}
	replaceCurriculum(t, path, curriculumJSON("New Root"))
	if after := r.get(t, route); after != before {
		t.Fatalf("startup-only graph changed: before=%s after=%s", before, after)
	}
	if metrics := r.get(t, "/metrics"); strings.Contains(metrics, "axiom_graph_reload_") {
		t.Fatal("reload counters present in default mode")
	}
	r.signal(t, syscall.SIGHUP)
	if mode == "http-ignore-hup" {
		if after := r.get(t, route); after != before {
			t.Fatalf("ignored SIGHUP changed the startup graph: before=%s after=%s", before, after)
		}
		r.signal(t, syscall.SIGTERM)
		if err := r.wait(t); err != nil {
			t.Fatalf("SIGTERM after ignored SIGHUP: %v", err)
		}
	} else {
		var exit *exec.ExitError
		if err := r.wait(t); !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGHUP {
			t.Fatalf("SIGHUP did not retain its default disposition: %v", err)
		}
	}
	if strings.Contains(r.logs.text(), "graph.reload") {
		t.Fatal("reload logged in default mode")
	}
}

func TestGraphReloadStaggeredHTTPReplicas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	old := replaceCurriculum(t, path, curriculumJSON("Old Root"))
	fiber := startGraphHTTPReplica(t, "http", path, "true", "fiber")
	netHTTP := startGraphHTTPReplica(t, "http", path, "true", "net-http")
	const route = "/api/v1/topics/Recursion/path"
	for _, replica := range []*graphHTTPReplica{fiber, netHTTP} {
		assertGraphReloadLog(t, replica.logs, "loaded", old, 0, 0)
		awaitGraphProcessLog(t, replica.logs, "server.listening")
		if body := replica.get(t, route); !strings.Contains(body, `"Old Root"`) {
			t.Fatalf("startup body=%s", body)
		}
	}
	newRevision := replaceCurriculum(t, path, curriculumJSON("New Root"))
	fiber.signal(t, syscall.SIGHUP)
	assertGraphReloadLog(t, fiber.logs, "published", newRevision, 1, 0)
	if a, b := fiber.get(t, route), netHTTP.get(t, route); !strings.Contains(a, `"New Root"`) || !strings.Contains(b, `"Old Root"`) {
		t.Fatalf("staggered update: fiber=%s net-http=%s", a, b)
	}
	netHTTP.signal(t, syscall.SIGHUP)
	assertGraphReloadLog(t, netHTTP.logs, "published", newRevision, 1, 0)
	replaceCurriculum(t, path, "private-invalid-curriculum")
	for _, replica := range []*graphHTTPReplica{fiber, netHTTP} {
		replica.signal(t, syscall.SIGHUP)
		assertGraphReloadLog(t, replica.logs, "failed", newRevision, 1, 1)
		if body := replica.get(t, route); !strings.Contains(body, `"New Root"`) {
			t.Fatalf("converged body=%s", body)
		}
		metrics := replica.get(t, "/metrics")
		if !strings.Contains(metrics, "\naxiom_graph_reload_success_total 1\n") || !strings.Contains(metrics, "\naxiom_graph_reload_failure_total 1\n") || strings.Contains(metrics, newRevision) || strings.Contains(metrics, "private-invalid-curriculum") {
			t.Fatal("reload counters have missing samples or contain private data")
		}
		replica.signal(t, syscall.SIGTERM)
		if err := replica.wait(t); err != nil {
			t.Fatalf("server exit=%v logs=%s", err, replica.logs.text())
		}
	}
}

func TestGraphReloadStdioTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	old := replaceCurriculum(t, path, curriculumJSON("Old Root"))
	backend := httptest.NewServer(fakebedrock.NewHandler("ok"))
	defer backend.Close()
	cmd := reloadCommand(t, "mcp", path, "true", "AWS_ACCESS_KEY_ID=dummy", "AWS_SECRET_ACCESS_KEY=dummy", "AWS_EC2_METADATA_DISABLED=true",
		"AWS_ENDPOINT_URL_BEDROCK_RUNTIME="+backend.URL, "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=false")
	logs := &processRecords{messages: make(chan map[string]any, 32), shutdown: make(chan struct{})}
	cmd.Stderr = logs
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "reload-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect MCP: %v logs=%s", err, logs.text())
	}
	defer func() {
		if err := cs.Close(); err != nil {
			t.Errorf("MCP close: %v", err)
		}
	}()
	assertGraphReloadLog(t, logs, "loaded", old, 0, 0)
	assertGraphReloadMCPTools(t, ctx, cs, "Old Root", "New Root")
	newRevision := replaceCurriculum(t, path, curriculumJSON("New Root"))
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	assertGraphReloadLog(t, logs, "published", newRevision, 1, 0)
	assertGraphReloadMCPTools(t, ctx, cs, "New Root", "Old Root")
}

func TestGraphReloadRemoteMCPTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	replaceCurriculum(t, path, curriculumJSON("Old Root"))
	issuer, err := mcpfixture.New(t.Context(), mcpfixture.Options{
		Dir: dir, IssuerHosts: []string{"127.0.0.1"}, MCPHost: "mcp.example.test", ListenHost: "127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issuer.Close() })
	backend := httptest.NewServer(fakebedrock.NewHandler("ok"))
	t.Cleanup(backend.Close)
	port := freePort(t)
	addr := "127.0.0.1:" + port
	resource := "https://mcp.example.test:" + port + "/mcp"
	ca := filepath.Join(dir, "ca.pem")
	files := mcpHTTPFiles{cert: filepath.Join(dir, "tls.crt"), key: filepath.Join(dir, "tls.key")}
	settings := mcpHTTPSettings("127.0.0.1:"+issuer.RedisPort, addr, files)
	settings["GRAPH_FILE"], settings["MCP_PUBLIC_URL"] = path, resource
	settings["AXIOM_AUTH_DISABLED"], settings["AXIOM_AUTH_ALGORITHMS"] = "false", "ES256"
	settings["AXIOM_AUTH_ISSUER"], settings["AXIOM_AUTH_JWKS_URL"] = issuer.Issuer(), issuer.KeysURL()
	env := make([]string, 0, len(settings)+6)
	env = append(env, graphReloadTestIssuerCA+"="+ca, "AWS_ACCESS_KEY_ID=dummy", "AWS_SECRET_ACCESS_KEY=dummy",
		"AWS_EC2_METADATA_DISABLED=true", "AWS_ENDPOINT_URL_BEDROCK_RUNTIME="+backend.URL, "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=false")
	for key, value := range settings {
		env = append(env, key+"="+value)
	}
	proc := startGraphProcess(t, reloadCommand(t, "mcp-http-default-hup", path, "true", env...), resource)
	awaitGraphProcessLog(t, proc.logs, "mcp.listening")

	token, err := issuer.Token(resource, "reload-tenant", "reload-client", "graph:read plan:generate")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: graphReloadCertPool(t, ca)},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := mcp.NewClient(&mcp.Implementation{Name: "reload-test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: proc.url, MaxRetries: -1,
		HTTPClient: &http.Client{Timeout: 5 * time.Second, Transport: &oauth2.Transport{
			Base: transport, Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token, TokenType: "Bearer"}),
		}},
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatalf("connect remote MCP: %v logs=%s", err, proc.logs.text())
	}
	defer func() { _ = cs.Close() }()

	assertGraphReloadMCPTools(t, t.Context(), cs, "Old Root", "New Root")
	revision := replaceCurriculum(t, path, curriculumJSON("New Root"))
	proc.signal(t, syscall.SIGHUP)
	assertGraphReloadLog(t, proc.logs, "published", revision, 1, 0)
	assertGraphReloadMCPTools(t, t.Context(), cs, "New Root", "Old Root")
	transport.CloseIdleConnections()
	proc.signal(t, syscall.SIGTERM)
	if err := proc.wait(t); err != nil {
		t.Fatalf("SIGTERM after publishing: %v", err)
	}
}

func graphReloadCertPool(t *testing.T, path string) *x509.CertPool {
	t.Helper()
	pem, err := os.ReadFile(path) //nolint:gosec // The parent test supplies the fixture CA.
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("fixture CA contained no certificate")
	}
	return pool
}

func assertGraphReloadMCPTools(t *testing.T, ctx context.Context, cs *mcp.ClientSession, root, other string) {
	t.Helper()
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"list_topics", map[string]any{"q": "Recursion"}},
		{"get_topic_path", map[string]any{"topic": "Recursion"}},
		{"get_cognitive_graph", map[string]any{"topics": []string{"Recursion"}}},
		{"generate_study_plan", map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "visual"}},
	} {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
		if err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil || result.IsError || !strings.Contains(string(data), root) || strings.Contains(string(data), other) {
			t.Fatalf("%s: data=%s error=%v result=%+v", tc.tool, data, err, result)
		}
	}
}
