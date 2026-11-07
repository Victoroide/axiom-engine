package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type configuredMCP struct {
	cmd          *exec.Cmd
	input        io.WriteCloser
	output, logs *processRecords
	exited       chan struct{}
	waitErr      error
}

func startConfiguredMCP(t *testing.T, settings map[string]string) *configuredMCP {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "axiom-server")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".") //nolint:gosec // Build the current server in a fresh test directory.
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, data)
	}
	cmd := exec.CommandContext(t.Context(), binary, "mcp") //nolint:gosec // Execute the server built by this test.
	defaults := map[string]string{"BAML_LOG": "off", "GRAPH_FILE": "", "AXIOM_AUTH_DISABLED": "invalid", "PORT": "invalid", "AWS_MAX_ATTEMPTS": "3", "AWS_RETRY_MODE": "standard", "TRACING_ENABLED": "false", "LANGSMITH_TRACING": "false", "RESOURCE_LINK_CHECK": "false"}
	for k, v := range settings {
		defaults[k] = v
	}
	cmd.Env = os.Environ()
	for k, v := range defaults {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	p := &configuredMCP{cmd: cmd, output: &processRecords{protocol: true, messages: make(chan map[string]any, 32)}, logs: &processRecords{shutdown: make(chan struct{})}, exited: make(chan struct{})}
	cmd.Stdout = p.output
	cmd.Stderr = p.logs
	var err error
	p.input, err = cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.waitErr = cmd.Wait(); close(p.exited) }()
	t.Cleanup(func() { _ = p.input.Close(); _ = cmd.Process.Kill(); <-p.exited })
	p.exchange(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`, 1)
	if _, err := io.WriteString(p.input, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *configuredMCP) exchange(t *testing.T, message string, id float64) map[string]any {
	t.Helper()
	if _, err := io.WriteString(p.input, message+"\n"); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case response := <-p.output.messages:
			if response["id"] == id {
				if response["error"] != nil {
					t.Fatalf("protocol error=%v", response)
				}
				return response["result"].(map[string]any)
			}
		case <-p.exited:
			t.Fatalf("process exited: %v logs=%s", p.waitErr, p.logs.text())
		case <-timer.C:
			t.Fatalf("response timed out: %s", p.logs.text())
		}
	}
}

func (p *configuredMCP) stop(t *testing.T, signal os.Signal) {
	t.Helper()
	if signal != nil {
		if err := p.cmd.Process.Signal(signal); err != nil {
			t.Fatal(err)
		}
	} else {
		_ = p.input.Close()
	}
	select {
	case <-p.exited:
		if p.waitErr != nil {
			t.Fatalf("process=%v logs=%s", p.waitErr, p.logs.text())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP did not stop")
	}
}

func localPlanner(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/chat/completions" || request["model"] != "local-planner" || request["max_tokens"] != float64(2048) || request["temperature"] != 0.1 {
			t.Errorf("request=%s %v", r.URL.Path, request)
		}
		content := `{"items":[{"topic":"Recursion","priority":1,"prerequisite_chain":[],"explanation":"Practice recursion.","resources":[]}]}`
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "completion", "object": "chat.completion", "created": 1, "model": "local-planner", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 17, "completion_tokens": 23, "total_tokens": 40}})
	}))
	t.Cleanup(backend.Close)
	return backend, calls
}

const mcpPlanCall = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"mastered_topics":["Functions"],"vark_profile":"visual","student_id":"private-learner","course_id":"private-course"}}}`

func TestMCPConfiguredProviderProcess(t *testing.T) {
	backend, calls := localPlanner(t)
	p := startConfiguredMCP(t, map[string]string{"LLM_PROVIDER": "openai-generic", "LLM_MODEL": "local-planner", "LLM_BASE_URL": backend.URL + "/v1", "AWS_MAX_ATTEMPTS": "invalid", "AWS_RETRY_MODE": "invalid"})
	result := p.exchange(t, mcpPlanCall, 2)
	if result["isError"] == true || calls.Load() != 1 {
		t.Fatalf("result=%v calls=%d", result, calls.Load())
	}
	encoded, err := json.Marshal(result["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"items_skipped_mastered":1`) {
		t.Fatalf("mastery lost: %s", encoded)
	}
	p.stop(t, nil)
	for _, field := range []string{`"llm_provider":"openai-generic"`, `"llm_model":"local-planner"`} {
		if !strings.Contains(p.logs.text(), field) {
			t.Fatalf("startup field missing: %s", p.logs.text())
		}
	}
	if strings.Contains(p.logs.text(), "region.unresolved") {
		t.Fatal("AWS warning for another provider")
	}
}
