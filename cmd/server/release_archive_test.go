package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	runtimebaml "github.com/boundaryml/baml/engine/language_client_go/pkg"

	baml "github.com/Victoroide/axiom-engine/baml_client"
)

const (
	archivePromptMarker        = "ArchivePromptCanary"
	archiveCompletionMarker    = "ArchiveCompletionCanary"
	archiveTraceDrainPath      = "/archive-fixture-drained"
	archiveTraceBackendEnv     = "AXIOM_ARCHIVE_TRACE_BACKEND"
	legacyTraceAcknowledgement = "Legacy trace receiver acknowledged both fixture markers"
)

func TestReleaseArchiveLauncher(t *testing.T) {
	directory := os.Getenv("RELEASE_ARCHIVE_TEST_INSTALL")
	if directory == "" {
		t.Skip("run make release-archive-test to exercise an extracted archive")
	}
	library := os.Getenv("BAML_LIBRARY_PATH")
	dir := t.TempDir()
	graph := filepath.Join(dir, "archive curriculum.json")
	// A dependent appears in the rendered graph context without becoming a plan prerequisite.
	const curriculum = `[{"subject":"` + archivePromptMarker + `","relation":"depends_on","object":"Recursion"},` +
		`{"subject":"Recursion","relation":"depends_on","object":"Functions"}]`
	if err := os.WriteFile(graph, []byte(curriculum), 0600); err != nil { //nolint:gosec // The graph is a fixture in a fresh test directory.
		t.Fatal(err)
	}
	var calls, proxyCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			proxyCalls.Add(1)
			http.Error(w, "fixture proxy rejected connection", http.StatusBadGateway)
			return
		}
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		badReply := r.URL.Path == "/malformed/v1/chat/completions"
		if err != nil || r.Method != http.MethodPost || (!badReply && r.URL.Path != "/v1/chat/completions") || !bytes.Contains(body, []byte(archivePromptMarker)) {
			t.Errorf("provider request missing prompt marker: %s %s: %s (%v)", r.Method, r.URL.Path, body, err)
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		// The raw completion contains a field that the plan schema does not return.
		completion := `{"items":[{"topic":"Recursion","priority":1,"prerequisite_chain":[],` +
			`"explanation":"Practice recursion.","resources":[]}],"fixture_marker":"` + archiveCompletionMarker + `"}`
		if badReply {
			completion = "Invalid plan " + archiveCompletionMarker
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"completion","object":"chat.completion","created":1,"model":"local-planner",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}]}`, strconv.Quote(completion))
	}))
	t.Cleanup(backend.Close)
	env := []string{
		"PATH=/usr/bin:/bin", "HOME=" + dir, "GRAPH_FILE=" + graph,
		"BAML_LIBRARY_DISABLE_DOWNLOAD=true", "LOG_LEVEL=info", "RUST_LOG=trace",
		"LLM_PROVIDER=openai-generic", "LLM_MODEL=local-planner", "LLM_BASE_URL=" + backend.URL + "/v1",
		"TRACING_ENABLED=false", "LANGSMITH_TRACING=false", "TRACING_CAPTURE_CONTENT=false",
	}
	for _, tc := range []struct {
		name, executable string
		settings         []string
		wantPrompt       bool
		wantCompletion   bool
		badReply         bool
		proxyFailure     bool
		traceFiles       bool
		legacyTrace      bool
		signal           syscall.Signal
	}{
		{name: "launcher unset", executable: "axiom-server-launch", signal: syscall.SIGTERM},
		{name: "launcher off", executable: "axiom-server-launch", settings: []string{"BAML_LOG=off"}, signal: syscall.SIGTERM},
		{name: "launcher error", executable: "axiom-server-launch", settings: []string{"BAML_LOG=ErRoR"}, signal: syscall.SIGTERM},
		{name: "launcher warn", executable: "axiom-server-launch", settings: []string{"BAML_LOG=warn"}, signal: syscall.SIGTERM},
		{name: "launcher warning", executable: "axiom-server-launch", settings: []string{"BAML_LOG=warning"}, signal: syscall.SIGINT},
		{name: "launcher mixed warning", executable: "axiom-server-launch", settings: []string{"BAML_LOG=WaRnInG"}, signal: syscall.SIGTERM},
		{name: "raw binary unset", executable: "axiom-server", wantPrompt: true, wantCompletion: true, signal: syscall.SIGTERM},
		{name: "raw binary warning", executable: "axiom-server", settings: []string{"BAML_LOG=warning"}, wantPrompt: true, wantCompletion: true, signal: syscall.SIGTERM},
		{name: "raw rust log filter", executable: "axiom-server", settings: []string{"BAML_LOG=off"}, signal: syscall.SIGTERM},
		{name: "launcher warn malformed", executable: "axiom-server-launch", settings: []string{"BAML_LOG=warn"}, badReply: true, signal: syscall.SIGTERM},
		{name: "launcher warning malformed", executable: "axiom-server-launch", settings: []string{"BAML_LOG=WaRnInG"}, badReply: true, signal: syscall.SIGTERM},
		{name: "raw warn malformed", executable: "axiom-server", settings: []string{"BAML_LOG=warn"}, wantPrompt: true, wantCompletion: true, badReply: true, signal: syscall.SIGTERM},
		{name: "launcher internal debug", executable: "axiom-server-launch", settings: []string{"BAML_INTERNAL_LOG=debug"}, signal: syscall.SIGTERM},
		{name: "launcher internal debug malformed", executable: "axiom-server-launch", settings: []string{"BAML_INTERNAL_LOG=debug"}, badReply: true, signal: syscall.SIGTERM},
		{name: "raw internal debug", executable: "axiom-server", settings: []string{"BAML_LOG=off", "BAML_INTERNAL_LOG=debug"}, wantCompletion: true, signal: syscall.SIGTERM},
		{name: "raw internal debug malformed", executable: "axiom-server", settings: []string{"BAML_LOG=off", "BAML_INTERNAL_LOG=debug"}, wantCompletion: true, badReply: true, signal: syscall.SIGTERM},
		{name: "launcher internal trace transport failure", executable: "axiom-server-launch", settings: []string{"BAML_INTERNAL_LOG=trace"}, proxyFailure: true, signal: syscall.SIGTERM},
		{name: "raw internal trace transport failure", executable: "axiom-server", settings: []string{"BAML_LOG=off", "BAML_INTERNAL_LOG=trace"}, wantPrompt: true, proxyFailure: true, signal: syscall.SIGTERM},
		{name: "launcher native trace sinks", executable: "axiom-server-launch", traceFiles: true, signal: syscall.SIGTERM},
		{name: "raw native trace export disabled", executable: "axiom-server", legacyTrace: true, signal: syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			beforeProxy := proxyCalls.Load()
			cmd := exec.CommandContext(t.Context(), filepath.Join(directory, tc.executable), "mcp") //nolint:gosec // Exercise the executable from the extracted archive.
			cmd.Dir = dir
			cmd.Env = append(slices.Clone(env), "BAML_LIBRARY_PATH="+library)
			cmd.Env = append(cmd.Env, tc.settings...)
			if tc.badReply {
				cmd.Env = append(cmd.Env, "LLM_BASE_URL="+backend.URL+"/malformed/v1")
			}
			if tc.proxyFailure {
				cmd.Env = append(cmd.Env, "LLM_PROVIDER=anthropic", "LLM_BASE_URL=", "ANTHROPIC_API_KEY=fixture", "HTTPS_PROXY="+backend.URL)
			}
			tracePath := filepath.Join(dir, "native trace.jsonl")
			var traceSink *archiveTraceCollector
			if tc.traceFiles || tc.legacyTrace {
				traceSink = newArchiveTraceCollector(t, tc.legacyTrace)
			}
			if tc.traceFiles {
				cmd.Env = append(cmd.Env, "BAML_TRACE_FILE="+tracePath, "BAML_TRACE_BATCH_SIZE=1", "BOUNDARY_API_KEY=fixture", "BOUNDARY_API_URL="+traceSink.server.URL,
					"BOUNDARY_PROJECT_ID=fixture", "BOUNDARY_SECRET=fixture", "BOUNDARY_SeCrEt=fixture", "BOUNDARY_BASE_URL="+traceSink.server.URL)
			}
			if tc.legacyTrace {
				cmd.Env = append(cmd.Env, "BAML_LOG=off", "BAML_INTERNAL_LOG=off",
					"BOUNDARY_PROJECT_ID=fixture", "BOUNDARY_SeCrEt=fixture", "BOUNDARY_BASE_URL="+traceSink.server.URL)
			}
			p := startMCPProcess(t, cmd)
			p.initialize(t)
			result := p.exchange(t, mcpPlanCall, 2)
			wantError := tc.badReply || tc.proxyFailure
			if tc.proxyFailure {
				if calls.Load() != before || proxyCalls.Load() == beforeProxy {
					t.Fatalf("request bypassed the rejecting proxy: %v", result)
				}
			} else if calls.Load()-before != 1 {
				t.Fatalf("result=%v provider calls=%d", result, calls.Load()-before)
			}
			if (result["isError"] == true) != wantError {
				t.Fatalf("expected tool error=%v, result=%v", wantError, result)
			}
			if !wantError {
				plan, _ := result["structuredContent"].(map[string]any)
				items, _ := plan["items"].([]any)
				if len(items) != 1 {
					t.Fatalf("expected one plan item: %v", result)
				}
				item, _ := items[0].(map[string]any)
				if item["topic"] != "Recursion" || item["explanation"] != "Practice recursion." {
					t.Fatalf("unexpected plan item: %v", item)
				}
			}
			pid := strconv.Itoa(cmd.Process.Pid)
			var comm []byte
			var err error
			if runtime.GOOS == "linux" {
				comm, err = os.ReadFile("/proc/" + pid + "/comm") //nolint:gosec // PID was assigned to the child process.
			} else {
				comm, err = exec.CommandContext(t.Context(), "ps", "-p", pid, "-o", "comm=").Output() //nolint:gosec // PID was assigned to the child process.
			}
			if err != nil || filepath.Base(strings.TrimSpace(string(comm))) != "axiom-server" {
				t.Fatalf("PID %s did not become axiom-server: %q (%v)", pid, comm, err)
			}
			if tc.legacyTrace {
				// Give the legacy exporter time to send a batch before stopping the server.
				observation := time.NewTimer(min(3*time.Second, mcpWaitBudget(t)))
				defer observation.Stop()
				t.Setenv("BAML_LOG", "trace")
				t.Setenv("BAML_INTERNAL_LOG", "trace")
				checkDirectLegacyTraceUpload(t, backend.URL)
				<-observation.C
			}
			p.stop(t, tc.signal)
			if traceSink != nil {
				ctx, cancel := context.WithTimeout(t.Context(), mcpWaitBudget(t))
				defer cancel()
				if err := traceSink.drain(ctx); err != nil {
					t.Error(err)
				}
			}
			stdout, stderr := p.output.text(), p.logs.text()
			for _, marker := range []struct {
				value string
				want  bool
			}{{archivePromptMarker, tc.wantPrompt}, {archiveCompletionMarker, tc.wantCompletion}} {
				found := strings.Contains(stdout, marker.value) || strings.Contains(stderr, marker.value)
				if found != marker.want {
					t.Errorf("marker %q, expected in process output=%v\nstdout: %s\nstderr: %s", marker.value, marker.want, stdout, stderr)
				}
			}
			if tc.traceFiles {
				data, err := os.ReadFile(tracePath) //nolint:gosec // Read only the fixture file in the temporary directory.
				if !errors.Is(err, os.ErrNotExist) || traceSink.uploads.Load() != 0 {
					t.Errorf("native trace output: file error=%v, prompt markers=%d, completion markers=%d, export requests=%d",
						err, bytes.Count(data, []byte(archivePromptMarker)), bytes.Count(data, []byte(archiveCompletionMarker)), traceSink.uploads.Load())
				}
			}
			if tc.executable == "axiom-server-launch" && (p.output.invalid != nil || p.output.pending != "") {
				t.Errorf("invalid stdio framing: %v; trailing bytes %q", p.output.invalid, p.output.pending)
			}
			if !strings.Contains(stderr, `"signal":`+strconv.Quote(tc.signal.String())) || !strings.Contains(stderr, `"msg":"server.shutdown_completed"`) {
				t.Fatalf("signal did not trigger clean shutdown: %s", stderr)
			}
		})
	}
	t.Run("graph arguments", func(t *testing.T) {
		cmd := exec.CommandContext(t.Context(), filepath.Join(directory, "axiom-server-launch"), "validate-graph", graph) //nolint:gosec // Pass the two arguments to the extracted launcher.
		cmd.Dir = dir
		cmd.Env = append(slices.Clone(env), "BAML_LIBRARY_PATH="+library)
		output, err := cmd.Output()
		var summary struct {
			Concepts  int            `json:"concepts"`
			Relations map[string]int `json:"relations"`
		}
		if err != nil || json.Unmarshal(output, &summary) != nil || summary.Concepts != 3 || summary.Relations["depends_on"] != 2 {
			t.Fatalf("validate-graph with a path containing spaces: %v, output=%s", err, output)
		}
	})
	const invalidLevel = "axiom-server-launch: BAML_LOG must be off, error, warn or warning"
	for _, tc := range []struct {
		name, library, log, diagnostic string
		status                         int
	}{
		{"empty logging", library, "", invalidLevel, 1},
		{"invalid logging", library, "invalid", invalidLevel, 1},
		{"padded logging", library, " warn ", invalidLevel, 1},
		{"info logging", library, "info", invalidLevel, 1},
		{"debug logging", library, "DeBuG", invalidLevel, 1},
		{"trace logging", library, "trace", invalidLevel, 1},
		{"missing library", filepath.Join(dir, filepath.Base(library)), "off", "panic: baml: initialization failed: baml: failed loading shared library", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			cmd := exec.CommandContext(t.Context(), filepath.Join(directory, "axiom-server-launch"), "mcp") //nolint:gosec // Exercise the launcher from the extracted archive.
			cmd.Dir = dir
			cmd.Env = append(slices.Clone(env), "BAML_LIBRARY_PATH="+tc.library, "BAML_LOG="+tc.log)
			cmd.Stdin = strings.NewReader(mcpPlanCall + "\n")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			var status *exec.ExitError
			if err := cmd.Run(); !errors.As(err, &status) || status.ExitCode() != tc.status || stdout.Len() != 0 || !strings.Contains(stderr.String(), tc.diagnostic) {
				t.Fatalf("expected exit %d and %q, got %v\nstdout: %s\nstderr: %s", tc.status, tc.diagnostic, err, &stdout, &stderr)
			}
			if tc.status == 1 && strings.TrimSuffix(stderr.String(), "\n") != tc.diagnostic {
				t.Errorf("unexpected startup output: %s", &stderr)
			}
			for _, marker := range []string{archivePromptMarker, archiveCompletionMarker, "private-learner", "private-course"} {
				if strings.Contains(stderr.String(), marker) {
					t.Errorf("startup diagnostic contains marker %q: %s", marker, &stderr)
				}
			}
			if calls.Load() != before {
				t.Fatalf("failed startup called the provider")
			}
		})
	}
}

type archiveTraceCollector struct {
	server  *httptest.Server
	resume  func()
	uploads atomic.Int64
}

func newArchiveTraceCollector(t *testing.T, delayAccept bool) *archiveTraceCollector {
	t.Helper()
	ready := make(chan struct{})
	collector := &archiveTraceCollector{resume: sync.OnceFunc(func() { close(ready) })}
	collector.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == archiveTraceDrainPath {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		collector.uploads.Add(1)
		http.Error(w, "fixture trace sink", http.StatusBadRequest)
	}))
	server := collector.server
	server.Config.SetKeepAlivesEnabled(false)
	server.Config.ReadTimeout = mcpWaitBudget(t)
	closed := make(chan struct{})
	server.Listener = &archiveTraceListener{
		Listener: server.Listener, ready: ready, closed: closed,
		stop: sync.OnceFunc(func() { close(closed) }),
	}
	if !delayAccept {
		collector.resume()
	}
	server.Start()
	t.Cleanup(server.Close)
	return collector
}

func (c *archiveTraceCollector) drain(ctx context.Context) error {
	c.resume()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.server.URL+archiveTraceDrainPath, nil)
	if err != nil {
		return err
	}
	req.Close = true
	resp, err := c.server.Client().Do(req)
	if err != nil {
		return fmt.Errorf("trace collector drain: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("trace collector drain returned HTTP %d", resp.StatusCode)
	}
	if uploads := c.uploads.Load(); uploads != 0 {
		return fmt.Errorf("archive server exported %d native traces", uploads)
	}
	return nil
}

// Process one request per connection in accept order so a drain request
// waits for each connection queued before it.
type archiveTraceListener struct {
	net.Listener
	ready, closed <-chan struct{}
	previous      <-chan struct{}
	stop          func()
}

func (l *archiveTraceListener) Accept() (net.Conn, error) {
	select {
	case <-l.ready:
	case <-l.closed:
		return nil, net.ErrClosed
	}
	if l.previous != nil {
		select {
		case <-l.previous:
		case <-l.closed:
			return nil, net.ErrClosed
		}
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	l.previous = done
	return &archiveTraceConnection{Conn: conn, finish: sync.OnceFunc(func() { close(done) })}, nil
}

func (l *archiveTraceListener) Close() error {
	l.stop()
	return l.Listener.Close()
}

type archiveTraceConnection struct {
	net.Conn
	finish func()
}

func (c *archiveTraceConnection) Close() error {
	err := c.Conn.Close()
	c.finish()
	return err
}

func TestArchiveTraceCollectorDrainsQueuedExport(t *testing.T) {
	collector := newArchiveTraceCollector(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), mcpWaitBudget(t))
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", collector.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, collector.server.URL+"/log/v2",
		strings.NewReader(`{"prompt":"`+archivePromptMarker+`","completion":"`+archiveCompletionMarker+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	if uploads := collector.uploads.Load(); uploads != 0 {
		t.Fatalf("collector accepted a connection before the drain: %d uploads", uploads)
	}
	if err := collector.drain(ctx); err == nil || err.Error() != "archive server exported 1 native traces" {
		t.Fatalf("drain failed to report the queued export: %v", err)
	}
}

func checkDirectLegacyTraceUpload(t *testing.T, backendURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), mcpWaitBudget(t))
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveDirectLegacyTraceProcess$", "-test.v") //nolint:gosec // Reexecute the test binary with isolated native logging.
	cmd.Env = []string{
		"HOME=" + t.TempDir(), archiveTraceBackendEnv + "=" + backendURL,
		"BAML_LIBRARY_PATH=" + os.Getenv("BAML_LIBRARY_PATH"), "BAML_LIBRARY_DISABLE_DOWNLOAD=true",
		"BAML_LOG=off", "BAML_INTERNAL_LOG=off",
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	for _, marker := range []string{archivePromptMarker, archiveCompletionMarker} {
		if bytes.Contains(stdout, []byte(marker)) || bytes.Contains(stderr.Bytes(), []byte(marker)) {
			t.Fatal("direct legacy trace helper printed fixture content")
		}
	}
	if err != nil {
		t.Fatalf("direct legacy trace helper failed: %v\n%s\n%s", err, stdout, &stderr)
	}
	if stderr.Len() != 0 {
		t.Fatal("direct legacy trace helper wrote to stderr")
	}
	if !bytes.Contains(stdout, []byte(legacyTraceAcknowledgement)) {
		t.Fatal("direct legacy trace helper did not verify its upload")
	}
	t.Log(legacyTraceAcknowledgement)
}

func TestArchiveDirectLegacyTraceProcess(t *testing.T) {
	backendURL := os.Getenv(archiveTraceBackendEnv)
	if backendURL == "" {
		t.Skip("run through the archive launcher trace fixture")
	}
	receipt := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read legacy upload: %v", err)
		}
		select {
		case receipt <- body:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)

	env := map[string]string{
		"BOUNDARY_PROJECT_ID": "fixture",
		"BOUNDARY_SeCrEt":     "fixture",
		"BOUNDARY_BASE_URL":   receiver.URL,
	}
	registry := runtimebaml.NewClientRegistry()
	registry.AddLlmClient("Fixture", "openai-generic", map[string]any{"model": "local-planner", "base_url": backendURL + "/v1"})
	registry.SetPrimaryClient("Fixture")
	ctx, cancel := context.WithTimeout(t.Context(), mcpWaitBudget(t))
	defer cancel()
	if _, err := baml.GenerateTopicPlan(ctx, nil, archivePromptMarker, nil, "visual", baml.WithClientRegistry(registry), baml.WithEnv(env)); err != nil {
		t.Fatalf("direct native call: %v", err)
	}
	checkLegacyTraceUpload(t, receipt)
}

func checkLegacyTraceUpload(t *testing.T, receipt <-chan []byte) {
	t.Helper()
	timer := time.NewTimer(mcpWaitBudget(t))
	defer timer.Stop()
	var body []byte
	select {
	case body = <-receipt:
	case <-timer.C:
		select {
		case body = <-receipt:
		default:
			t.Fatal("legacy trace receiver did not acknowledge an upload")
		}
	}
	if !bytes.Contains(body, []byte(archivePromptMarker)) || !bytes.Contains(body, []byte(archiveCompletionMarker)) {
		t.Fatal("legacy trace upload did not contain both fixture markers")
	}
	t.Log(legacyTraceAcknowledgement)
}
