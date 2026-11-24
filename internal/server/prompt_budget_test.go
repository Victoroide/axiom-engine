package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestPromptBudgetRejectsBeforeHTTPDispatch(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	env := map[string]string{"AXIOM_AUTH_DISABLED": "true", "METRICS_ENABLED": "true", "LLM_MAX_CONTEXT_EDGES": "1"}
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	addr, _, _ := startWireApp(t, cfg, func(_ context.Context, input service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return []domain.PlanItem{{Topic: input.Topic}}, nil
	}, io.Discard)
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	baseURL := "http://" + string(addr)
	for _, path := range []string{"/api/v1/adaptive-plan", "/api/v1/adaptive-plan/stream"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+path, strings.NewReader(validBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		const want = `{"error":"curriculum_context_too_large","details":"curriculum context exceeds the prompt budget"}`
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || string(body) != want {
			t.Errorf("%s: status=%d content-type=%q body=%s", path, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("planner calls = %d, want zero", got)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"axiom_plan_prompt_budget_rejections_total 2\n", "axiom_llm_calls_total{outcome=\"ok\"} 0\n"} {
		if !strings.Contains(string(data), line) {
			t.Errorf("metrics missing %q", line)
		}
	}
	for _, value := range []string{"Recursion", "Polymorphism", "student-42", "cs101"} {
		if strings.Contains(string(data), value) {
			t.Errorf("metrics contain %q", value)
		}
	}
}
