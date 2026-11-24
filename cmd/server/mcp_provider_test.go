package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	p := startMCPProcess(t, cmd)
	p.initialize(t)
	return p
}

func startMCPProcess(t *testing.T, cmd *exec.Cmd) *configuredMCP {
	t.Helper()
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
	return p
}

func (p *configuredMCP) initialize(t *testing.T) {
	t.Helper()
	p.exchange(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`, 1)
	if _, err := io.WriteString(p.input, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (p *configuredMCP) exchange(t *testing.T, message string, id float64) map[string]any {
	t.Helper()
	if _, err := io.WriteString(p.input, message+"\n"); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(mcpWaitBudget(t))
	defer timer.Stop()
	var response map[string]any
	for response == nil {
		select {
		case message := <-p.output.messages:
			if message["id"] == id {
				response = message
			}
		case <-p.exited:
			response = p.replyAtDeadline(id)
			if response == nil {
				t.Fatalf("process exited: %v logs=%s", p.waitErr, p.logs.text())
			}
		case <-timer.C:
			response = p.replyAtDeadline(id)
			if response == nil {
				t.Fatalf("response timed out: %s", p.logs.text())
			}
		}
	}
	if response["error"] != nil {
		t.Fatalf("protocol error=%v", response)
	}
	return response["result"].(map[string]any)
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
	timer := time.NewTimer(mcpWaitBudget(t))
	defer timer.Stop()
	var err error
	select {
	case <-p.exited:
		err = p.waitErr
	case <-timer.C:
		var exited bool
		exited, err = p.exitAtDeadline()
		if !exited {
			t.Fatalf("MCP did not stop: %s", p.logs.text())
		}
	}
	if err != nil {
		t.Fatalf("process=%v logs=%s", err, p.logs.text())
	}
}

func mcpWaitBudget(t *testing.T) time.Duration {
	const maxWait = 30 * time.Second
	if deadline, ok := t.Deadline(); ok {
		remaining := max(time.Until(deadline), 0)
		return min(maxWait, remaining-min(time.Second, remaining/2))
	}
	return maxWait
}

func (p *configuredMCP) replyAtDeadline(id float64) map[string]any {
	// The writer can still enqueue messages. Drain only those already queued.
	for range len(p.output.messages) {
		if response := <-p.output.messages; response["id"] == id {
			return response
		}
	}
	return nil
}

func (p *configuredMCP) exitAtDeadline() (bool, error) {
	select {
	case <-p.exited:
		return true, p.waitErr
	default:
		return false, nil
	}
}

func TestConfiguredMCPDeadline(t *testing.T) {
	t.Run("buffered reply", func(t *testing.T) {
		messages := make(chan map[string]any, 2)
		messages <- map[string]any{"method": "notifications/message"}
		want := map[string]any{"id": float64(2), "result": map[string]any{"ready": true}}
		messages <- want
		p := &configuredMCP{output: &processRecords{messages: messages}}
		if reply := p.replyAtDeadline(2); !reflect.DeepEqual(reply, want) {
			t.Fatalf("deadline discarded queued reply: %v", reply)
		}
		if reply := p.replyAtDeadline(3); reply != nil {
			t.Fatalf("unexpected reply after draining: %v", reply)
		}
	})
	t.Run("completed exit", func(t *testing.T) {
		p := &configuredMCP{exited: make(chan struct{})}
		if exited, _ := p.exitAtDeadline(); exited {
			t.Fatal("deadline reported exit for a running process")
		}
		close(p.exited)
		if exited, err := p.exitAtDeadline(); !exited || err != nil {
			t.Fatalf("deadline discarded completed exit: exited=%v, error=%v", exited, err)
		}
	})
	t.Run("failed exit", func(t *testing.T) {
		want := errors.New("child exited unsuccessfully")
		p := &configuredMCP{exited: make(chan struct{}), waitErr: want}
		close(p.exited)
		if exited, err := p.exitAtDeadline(); !exited || !errors.Is(err, want) {
			t.Fatalf("deadline discarded failed exit: exited=%v, error=%v", exited, err)
		}
	})
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
