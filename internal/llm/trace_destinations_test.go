package llm

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
	"github.com/Victoroide/axiom-engine/test/tracereceiver"
)

const (
	destTopic      = "Loops SYNTHETIC-TOPIC-MARKER"
	destCompletion = "SYNTHETIC-COMPLETION-MARKER"
)

// Identity markers must never reach either receiver, with or without content consent.
var destIdentity = []string{"SYNTHETIC-LEARNER-MARKER", "SYNTHETIC-COURSE-MARKER", "SYNTHETIC-TOKEN-MARKER", "SYNTHETIC-SUBJECT-MARKER", "SYNTHETIC-API-KEY-MARKER"}

const destPlanBody = `{"student_id":"SYNTHETIC-LEARNER-MARKER","course_id":"SYNTHETIC-COURSE-MARKER","failed_topics":["` + destTopic + `"],"vark_profile":"visual"}`

type destinationRun struct {
	otlp, langsmith *tracereceiver.Receiver
	cfg             config.Config
}

// expectedRoots names the request spans each enabled receiver must hold before shutdown begins.
// A streaming request ends its root span after the response body closes, so shutdown cannot
// be allowed to race it.
type expectedRoots struct{ otlp, langsmith []string }

func waitForRoots(t *testing.T, name string, receiver *tracereceiver.Receiver, roots []string) {
	t.Helper()
	waitForReceiver(t, func() bool {
		seen := map[string]bool{}
		for _, span := range receiver.Spans() {
			seen[span.Name] = true
		}
		for _, root := range roots {
			if !seen[root] {
				return false
			}
		}
		return true
	}, name+" never received its request spans")
}

func waitForReceiver(t *testing.T, ready func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		time.Sleep(time.Millisecond)
	}
}

// runDestinationStack serves HTTP requests through the real planner and exporters
// and returns both receivers after shutdown.
func runDestinationStack(t *testing.T, env map[string]string, requests func(t *testing.T, app *fiber.App) expectedRoots) *destinationRun {
	t.Helper()
	run := &destinationRun{otlp: tracereceiver.NewOTLP(t), langsmith: tracereceiver.NewLangSmith(t, "SYNTHETIC-API-KEY-MARKER")}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		plan, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"topic": destTopic, "priority": 1, "prerequisite_chain": []string{"Variables"}, "explanation": destCompletion, "resources": []any{}}}})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "completion", "object": "chat.completion", "created": 1, "model": "local-planner", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": string(plan)}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 17, "completion_tokens": 23, "total_tokens": 40}})
	}))
	t.Cleanup(provider.Close)
	values := map[string]string{
		"AXIOM_AUTH_DISABLED": "true", "LLM_PROVIDER": "openai-generic", "LLM_MODEL": "local-planner", "LLM_API_KEY": "dummy-provider-key", "LLM_BASE_URL": provider.URL + "/v1",
		"LANGSMITH_API_KEY": "SYNTHETIC-API-KEY-MARKER", "LANGSMITH_ENDPOINT": run.langsmith.URL(),
	}
	for key, value := range env {
		values[key] = value
	}
	cfg, err := config.Load(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	run.cfg = cfg
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", run.otlp.TracesURL())
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "20")
	previous, previousPropagation, previousHandler := otel.GetTracerProvider(), otel.GetTextMapPropagator(), otel.GetErrorHandler()
	defer func() {
		otel.SetTracerProvider(previous)
		otel.SetTextMapPropagator(previousPropagation)
		otel.SetErrorHandler(previousHandler)
	}()
	shutdown, err := telemetry.Setup(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if err := shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	}
	defer stop()
	g, err := graph.Decode(strings.NewReader(`[{"subject":"` + destTopic + `","relation":"depends_on","object":"Variables"}]`))
	if err != nil {
		t.Fatal(err)
	}
	app := server.New(cfg, server.Dependencies{Graph: g, Planner: New(cfg.LLM), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	want := requests(t, app)
	if cfg.Tracing.Enabled {
		waitForRoots(t, "OTLP", run.otlp, want.otlp)
	}
	if cfg.LangSmith.Enabled {
		waitForRoots(t, "LangSmith", run.langsmith, want.langsmith)
	}
	stop()
	return run
}

func destinationRequest(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer SYNTHETIC-TOKEN-MARKER")
	req.Header.Set("X-Subject", "SYNTHETIC-SUBJECT-MARKER")
	req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func serveJSONAndSSE(t *testing.T, app *fiber.App) expectedRoots {
	t.Helper()
	status, body := destinationRequest(t, app, http.MethodPost, "/api/v1/adaptive-plan", destPlanBody)
	if status != http.StatusOK || !strings.Contains(body, destCompletion) {
		t.Fatalf("JSON plan status=%d body=%s", status, body)
	}
	status, body = destinationRequest(t, app, http.MethodPost, "/api/v1/adaptive-plan/stream", destPlanBody)
	if status != http.StatusOK || !strings.Contains(body, destCompletion) {
		t.Fatalf("SSE plan status=%d body=%s", status, body)
	}
	roots := []string{"POST /api/v1/adaptive-plan", "POST /api/v1/adaptive-plan/stream"}
	return expectedRoots{otlp: roots, langsmith: roots}
}

func destinationRoots(r *tracereceiver.Receiver) map[string]int {
	roots := map[string]int{}
	for _, span := range r.Spans() {
		if strings.HasPrefix(span.Name, "POST /api/v1/adaptive-plan") {
			roots[span.Name]++
		}
	}
	return roots
}

func assertDestinationContent(t *testing.T, name string, r *tracereceiver.Receiver, allowed bool) {
	t.Helper()
	roots := destinationRoots(r)
	if roots["POST /api/v1/adaptive-plan"] != 1 || roots["POST /api/v1/adaptive-plan/stream"] != 1 {
		t.Fatalf("%s request roots=%v", name, roots)
	}
	for _, marker := range append([]string{destTopic, destCompletion}, destIdentity...) {
		n := r.Count(marker)
		switch {
		case slices.Contains(destIdentity, marker) && marker != "SYNTHETIC-API-KEY-MARKER":
			if n != 0 {
				t.Errorf("%s received identity marker %s %d times", name, marker, n)
			}
		case marker == "SYNTHETIC-API-KEY-MARKER":
			if n != 0 {
				t.Errorf("%s received the API key marker in a payload", name)
			}
		case allowed && n == 0:
			t.Errorf("%s did not receive approved content %s", name, marker)
		case !allowed && n != 0:
			t.Errorf("%s received %d copies of content %s without consent", name, n, marker)
		}
	}
	keys := map[string]bool{}
	for _, span := range r.Spans() {
		for _, a := range span.Attributes {
			keys[a.Key] = true
		}
	}
	for _, key := range []string{"gen_ai.prompt.0.content", "gen_ai.completion.0.content", "input.value", "output.value", "axiom.topic"} {
		if keys[key] != allowed {
			t.Errorf("%s attribute %s present=%v want %v", name, key, keys[key], allowed)
		}
	}
	for _, key := range []string{"gen_ai.request.model", "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "http.route", "axiom.topic.index", "request_id"} {
		if !keys[key] {
			t.Errorf("%s lost structural attribute %s", name, key)
		}
	}
	if allowed {
		for _, key := range []string{"input.value", "output.value"} {
			for _, span := range r.Spans() {
				for _, a := range span.Attributes {
					if a.Key == key && len(a.Value.GetStringValue()) > 32*1024 {
						t.Errorf("%s %s exceeds the content cap", name, key)
					}
				}
			}
		}
	}
}

func TestHTTPTraceContentFollowsDestinationList(t *testing.T) {
	bedrockEndpoint(t, "ok") // Keep native clients off the network if a provider default leaks in.
	both := map[string]string{"TRACING_ENABLED": "true", "LANGSMITH_TRACING": "true"}
	with := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for key, value := range both {
			env[key] = value
		}
		for key, value := range extra {
			env[key] = value
		}
		return env
	}
	for _, tc := range []struct {
		name            string
		env             map[string]string
		otlp, langsmith bool
	}{
		{"unset list sends no content", with(nil), false, false},
		{"global true alone grants nothing", with(map[string]string{"TRACING_CAPTURE_CONTENT": "true"}), false, false},
		{"langsmith only", with(map[string]string{"TRACING_CONTENT_DESTINATIONS": "langsmith"}), false, true},
		{"otlp only", with(map[string]string{"TRACING_CONTENT_DESTINATIONS": "otlp"}), true, false},
		{"both", with(map[string]string{"TRACING_CONTENT_DESTINATIONS": "langsmith,otlp"}), true, true},
		{"global false vetoes", with(map[string]string{"TRACING_CAPTURE_CONTENT": "false", "TRACING_CONTENT_DESTINATIONS": "langsmith,otlp"}), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runDestinationStack(t, tc.env, serveJSONAndSSE)
			assertDestinationContent(t, "OTLP", run.otlp, tc.otlp)
			assertDestinationContent(t, "LangSmith", run.langsmith, tc.langsmith)
			assertDestinationTrees(t, run)
		})
	}
	t.Run("single exporters", func(t *testing.T) {
		run := runDestinationStack(t, map[string]string{"TRACING_ENABLED": "true", "TRACING_CONTENT_DESTINATIONS": "otlp"}, serveJSONAndSSE)
		assertDestinationContent(t, "OTLP", run.otlp, true)
		if run.langsmith.Requests() != 0 {
			t.Error("disabled LangSmith received a trace")
		}
		run = runDestinationStack(t, map[string]string{"LANGSMITH_TRACING": "true", "TRACING_CONTENT_DESTINATIONS": "langsmith"}, serveJSONAndSSE)
		assertDestinationContent(t, "LangSmith", run.langsmith, true)
		if run.otlp.Requests() != 0 {
			t.Error("disabled OTLP received a trace")
		}
	})
}

// assertDestinationTrees checks remote-parent handling and that every group stays whole.
func assertDestinationTrees(t *testing.T, run *destinationRun) {
	t.Helper()
	ids := map[string]bool{}
	for _, span := range run.langsmith.Spans() {
		ids[hex.EncodeToString(span.SpanId)] = true
	}
	roots := 0
	for _, span := range run.langsmith.Spans() {
		if len(span.ParentSpanId) == 0 {
			roots++
		} else if !ids[hex.EncodeToString(span.ParentSpanId)] {
			t.Errorf("LangSmith span %s has a missing parent", span.Name)
		}
	}
	if run.cfg.LangSmith.Enabled && roots != 2 {
		t.Errorf("LangSmith roots=%d want one per request", roots)
	}
	for _, span := range run.otlp.Spans() {
		if strings.HasPrefix(span.Name, "POST /api/v1/adaptive-plan") && hex.EncodeToString(span.ParentSpanId) != "0123456789abcdef" {
			t.Errorf("OTLP root %s parent=%x, want the remote caller", span.Name, span.ParentSpanId)
		}
	}
}

func TestGraphOnlyHTTPTraceStaysStructural(t *testing.T) {
	bedrockEndpoint(t, "ok")
	run := runDestinationStack(t, map[string]string{"TRACING_ENABLED": "true", "LANGSMITH_TRACING": "true", "TRACING_CONTENT_DESTINATIONS": "langsmith,otlp"}, func(t *testing.T, app *fiber.App) expectedRoots {
		if status, _ := destinationRequest(t, app, http.MethodGet, "/api/v1/topics?q=Variables", ""); status != http.StatusOK {
			t.Fatalf("topics status=%d", status)
		}
		return expectedRoots{otlp: []string{"GET /api/v1/topics"}}
	})
	if run.langsmith.Requests() != 0 {
		t.Fatal("a trace without a model call reached LangSmith")
	}
	if names := run.otlp.Attributes(); len(names) == 0 || names["GET /api/v1/topics"] == nil {
		t.Fatalf("OTLP spans=%v", names)
	}
	for _, marker := range append([]string{destTopic, destCompletion}, destIdentity...) {
		if n := run.otlp.Count(marker); n != 0 {
			t.Errorf("graph-only OTLP trace contains %s", marker)
		}
	}
}
