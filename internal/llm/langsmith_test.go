package llm

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

func TestLangSmithExportsPlanContent(t *testing.T) {
	for _, capture := range []bool{true, false} {
		t.Run(strconv.FormatBool(capture), func(t *testing.T) {
			bedrockEndpoint(t, "ok")
			var mu sync.Mutex
			var batches []*collector.ExportTraceServiceRequest
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/otel/v1/traces" || r.Method != http.MethodPost || r.Header.Get("X-Api-Key") != "test-secret" || r.Header.Get("Langsmith-Project") != "study-plans" {
					t.Errorf("request=%s %s headers=%v", r.Method, r.URL.Path, r.Header)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				var batch collector.ExportTraceServiceRequest
				if err := proto.Unmarshal(body, &batch); err != nil {
					t.Error(err)
				}
				mu.Lock()
				batches = append(batches, &batch)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/x-protobuf")
			}))
			defer receiver.Close()
			env := map[string]string{"AXIOM_AUTH_DISABLED": "true", "LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "test-secret", "LANGSMITH_PROJECT": "study-plans", "LANGSMITH_ENDPOINT": receiver.URL + "/api/v1/", "TRACING_CAPTURE_CONTENT": strconv.FormatBool(capture)}
			cfg, err := config.Load(func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			previous := otel.GetTracerProvider()
			defer otel.SetTracerProvider(previous)
			previousPropagation := otel.GetTextMapPropagator()
			defer otel.SetTextMapPropagator(previousPropagation)
			shutdown, err := telemetry.Setup(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = shutdown(t.Context()) }()
			app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Planner: New(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/adaptive-plan", strings.NewReader(`{"student_id":"private-learner","course_id":"private-course","failed_topics":["Recursion"],"vark_profile":"visual"}`))
			req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", "capture-test")
			for range 1000 {
				probe := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil)
				response, err := app.Test(probe)
				if err != nil {
					t.Fatal(err)
				}
				if err := response.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatal(resp.StatusCode)
			}
			if err := shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(batches) != 1 {
				t.Fatalf("batches=%d", len(batches))
			}
			spans := flattenSpans(batches[0])
			byID := map[string]*tracepb.Span{}
			for _, span := range spans {
				byID[hex.EncodeToString(span.SpanId)] = span
			}
			found := map[string]bool{}
			for _, span := range spans {
				if len(span.ParentSpanId) > 0 && byID[hex.EncodeToString(span.ParentSpanId)] == nil {
					t.Errorf("missing parent for %s", span.Name)
				}
				attrs := spanAttrs(span.Attributes)
				if attrs["langsmith.metadata.request_id"] != "capture-test" || attrs["langsmith.trace.name"] != span.Name {
					t.Errorf("LangSmith attributes=%v", attrs)
				}
				for key, value := range attrs {
					if strings.Contains(value, "private-") || strings.Contains(value, "test-secret") {
						t.Errorf("private value exported in %s", key)
					}
					if !capture && (strings.HasPrefix(key, "gen_ai.prompt.") || strings.HasPrefix(key, "gen_ai.completion.") || key == "input.value" || key == "output.value") {
						t.Errorf("content exported: %s", key)
					}
				}
				switch span.Name {
				case "POST /api/v1/adaptive-plan":
					found["request"] = true
					if attrs["langsmith.span.kind"] != "chain" {
						t.Fatal("request kind")
					}
					if capture {
						var input map[string]any
						if err := json.Unmarshal([]byte(attrs["input.value"]), &input); err != nil {
							t.Fatal(err)
						}
						if len(input) != 2 || input["vark_profile"] != "visual" || !strings.Contains(attrs["output.value"], "Recursion") {
							t.Fatalf("request content=%v %s", input, attrs["output.value"])
						}
					}
				case "plan.topic":
					found["topic"] = true
					if attrs["langsmith.span.kind"] != "chain" {
						t.Fatal("topic kind")
					}
				case "chat amazon.nova-micro-v1:0":
					found["call"] = true
					if attrs["langsmith.span.kind"] != "llm" || attrs["gen_ai.operation.name"] != "chat" {
						t.Fatalf("call attrs=%v", attrs)
					}
					counts := map[string]int64{}
					for _, a := range span.Attributes {
						counts[a.Key] = a.Value.GetIntValue()
					}
					if counts["gen_ai.usage.input_tokens"] != 100 || counts["gen_ai.usage.output_tokens"] != 50 {
						t.Fatalf("usage=%v", counts)
					}
					if capture && (attrs["gen_ai.prompt.0.role"] == "" || !strings.Contains(attrs["gen_ai.prompt.0.content"]+attrs["gen_ai.prompt.1.content"], "Recursion") || attrs["gen_ai.completion.0.role"] != "assistant" || !strings.Contains(attrs["gen_ai.completion.0.content"], "Practice recursion.")) {
						t.Fatalf("call content=%v", attrs)
					}
				}
			}
			if len(found) != 3 {
				t.Fatalf("spans=%v", found)
			}
		})
	}
}

func flattenSpans(batch *collector.ExportTraceServiceRequest) []*tracepb.Span {
	spans := make([]*tracepb.Span, 0)
	for _, resource := range batch.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			spans = append(spans, scope.Spans...)
		}
	}
	return spans
}

func spanAttrs(attrs []*commonpb.KeyValue) map[string]string {
	values := make(map[string]string, len(attrs))
	for _, a := range attrs {
		values[a.Key] = a.Value.GetStringValue()
	}
	return values
}
