package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Victoroide/axiom-engine/test/tracereceiver"
)

const (
	stdioTopic      = "Loops SYNTHETIC-TOPIC-MARKER"
	stdioCompletion = "SYNTHETIC-COMPLETION-MARKER"
	stdioCall       = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"generate_study_plan","arguments":{"failed_topics":["` + stdioTopic + `"],"vark_profile":"visual","student_id":"SYNTHETIC-LEARNER-MARKER","course_id":"SYNTHETIC-COURSE-MARKER"}}}`
)

var stdioIdentity = []string{"SYNTHETIC-LEARNER-MARKER", "SYNTHETIC-COURSE-MARKER", "SYNTHETIC-API-KEY-MARKER"}

func markerPlanner(t *testing.T) *httptest.Server {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		plan, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"topic": stdioTopic, "priority": 1, "prerequisite_chain": []string{"Variables"}, "explanation": stdioCompletion, "resources": []any{}}}})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "completion", "object": "chat.completion", "created": 1, "model": "local-planner", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": string(plan)}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 17, "completion_tokens": 23, "total_tokens": 40}})
	}))
	t.Cleanup(backend.Close)
	return backend
}

func TestMCPStdioTraceContentFollowsDestinationList(t *testing.T) {
	graphFile := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(graphFile, []byte(`[{"subject":"`+stdioTopic+`","relation":"depends_on","object":"Variables"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		env             map[string]string
		otlp, langsmith bool
	}{
		{"unset list sends no content", map[string]string{}, false, false},
		{"langsmith only", map[string]string{"TRACING_CONTENT_DESTINATIONS": "langsmith"}, false, true},
		{"otlp only", map[string]string{"TRACING_CONTENT_DESTINATIONS": "otlp"}, true, false},
		{"both", map[string]string{"TRACING_CONTENT_DESTINATIONS": "langsmith,otlp"}, true, true},
		{"global false vetoes", map[string]string{"TRACING_CAPTURE_CONTENT": "false", "TRACING_CONTENT_DESTINATIONS": "langsmith,otlp"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := markerPlanner(t)
			otlp, langsmith := tracereceiver.NewOTLP(t), tracereceiver.NewLangSmith(t, "SYNTHETIC-API-KEY-MARKER")
			settings := map[string]string{
				"LLM_PROVIDER": "openai-generic", "LLM_MODEL": "local-planner", "LLM_BASE_URL": backend.URL + "/v1", "LLM_API_KEY": "dummy-provider-key", "GRAPH_FILE": graphFile,
				"TRACING_ENABLED": "true", "LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "SYNTHETIC-API-KEY-MARKER", "LANGSMITH_ENDPOINT": langsmith.URL(),
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": otlp.TracesURL(), "OTEL_BSP_SCHEDULE_DELAY": "60000",
			}
			for key, value := range tc.env {
				settings[key] = value
			}
			p := startConfiguredMCP(t, settings)
			if result := p.exchange(t, stdioCall, 2); result["isError"] == true {
				t.Fatal(result)
			}
			p.stop(t, nil)
			p.output.mu.Lock()
			invalid := p.output.invalid
			p.output.mu.Unlock()
			if invalid != nil {
				t.Fatalf("stdout carried a non-protocol frame: %v", invalid)
			}
			for name, receiver := range map[string]*tracereceiver.Receiver{"OTLP": otlp, "LangSmith": langsmith} {
				allowed := tc.otlp
				if name == "LangSmith" {
					allowed = tc.langsmith
				}
				attrs := receiver.Attributes()
				if attrs["tools/call generate_study_plan"] == nil || attrs["chat local-planner"] == nil {
					t.Fatalf("%s tool trace=%v", name, attrs)
				}
				for _, marker := range []string{stdioTopic, stdioCompletion} {
					if n := receiver.Count(marker); (n > 0) != allowed {
						t.Errorf("%s received %d copies of %s with content allowed=%v", name, n, marker, allowed)
					}
				}
				for _, marker := range stdioIdentity {
					if n := receiver.Count(marker); n != 0 {
						t.Errorf("%s received identity marker %s", name, marker)
					}
				}
				keys := receiver.Keys("chat local-planner")
				for _, key := range []string{"gen_ai.prompt.0.content", "gen_ai.completion.0.content"} {
					if slices.Contains(keys, key) != allowed {
						t.Errorf("%s model span has %s=%v want %v", name, key, slices.Contains(keys, key), allowed)
					}
				}
				if got := receiver.Keys("tools/call generate_study_plan"); slices.Contains(got, "input.value") != allowed || slices.Contains(got, "output.value") != allowed {
					t.Errorf("%s tool span keys=%v with content allowed=%v", name, got, allowed)
				}
				if got := receiver.Keys("plan.topic"); slices.Contains(got, "axiom.topic") != allowed || !slices.Contains(got, "axiom.topic.index") {
					t.Errorf("%s topic span keys=%v with content allowed=%v", name, got, allowed)
				}
			}
		})
	}
}
