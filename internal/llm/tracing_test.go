package llm

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
)

func TestPlanTraceTree(t *testing.T) {
	for _, tc := range []struct {
		name, mode, body string
		status           int
	}{
		{"success", "ok", `{"student_id":"private-learner","course_id":"private-course","failed_topics":["Recursion"],"vark_profile":"visual"}`, 200},
		{"upstream failure", "internal", `{"failed_topics":["Recursion"],"vark_profile":"visual"}`, 502},
		{"validation failure", "ok", `{"failed_topics":["Unknown"],"vark_profile":"visual"}`, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bedrockEndpoint(t, tc.mode)
			recorder := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(tp)
			defer otel.SetTracerProvider(previous)
			defer func() {
				if err := tp.Shutdown(t.Context()); err != nil {
					t.Error(err)
				}
			}()
			cfg, err := config.Load(func(k string) string {
				if k == "AXIOM_AUTH_DISABLED" {
					return "true"
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			cfg.Tracing.Enabled = true
			app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Planner: New(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/adaptive-plan", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", "request-123")
			req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
			req.Header.Set("tracestate", "vendor=opaque")
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			spans := recorder.Ended()
			byName := map[string]sdktrace.ReadOnlySpan{}
			for _, span := range spans {
				byName[span.Name()] = span
				foundID := false
				for _, a := range span.Attributes() {
					if string(a.Key) == "request_id" && a.Value.AsString() == "request-123" {
						foundID = true
					}
				}
				if !foundID {
					t.Errorf("request ID missing from %s", span.Name())
				}
				for _, attr := range span.Attributes() {
					if strings.Contains(string(attr.Key), "student_id") || strings.Contains(string(attr.Key), "course_id") || strings.Contains(attr.Value.String(), "private-") {
						t.Errorf("identifier exported: %s", attr.Key)
					}
				}
				for _, event := range span.Events() {
					for _, attr := range event.Attributes {
						if strings.Contains(attr.Value.String(), "status code:") {
							t.Error("raw upstream error exported")
						}
					}
				}
			}
			root := byName["POST /api/v1/adaptive-plan"]
			if root == nil {
				t.Fatalf("missing server span: %v", byName)
			}
			if root.SpanKind() != trace.SpanKindServer || root.Parent().SpanID().String() != "0123456789abcdef" || root.SpanContext().TraceID().String() != "0123456789abcdef0123456789abcdef" || root.SpanContext().TraceState().String() != "vendor=opaque" {
				t.Fatalf("parent context=%v", root.Parent())
			}
			if resp.Header.Get("Traceparent") == "" || resp.Header.Get("Tracestate") != "vendor=opaque" {
				t.Fatal("response context missing")
			}
			validate := byName["plan.validate"]
			if validate == nil {
				t.Fatal("validation span missing")
			}
			if validate.Parent().SpanID() != root.SpanContext().SpanID() {
				t.Fatal("validation parent")
			}
			if tc.status == 422 {
				if validate.Status().Code != codes.Error {
					t.Fatal("validation status")
				}
				return
			}
			for _, name := range []string{"plan.subgraph", "plan.topic", "chat amazon.nova-micro-v1:0"} {
				if byName[name] == nil {
					t.Fatalf("missing %s", name)
				}
			}
			topic := byName["plan.topic"]
			call := byName["chat amazon.nova-micro-v1:0"]
			if topic.Parent().SpanID() != root.SpanContext().SpanID() || call.Parent().SpanID() != topic.SpanContext().SpanID() {
				t.Fatal("topic/call parent")
			}
			attrs := map[string]any{}
			for _, a := range call.Attributes() {
				attrs[string(a.Key)] = a.Value.AsInterface()
			}
			for k, want := range map[string]any{"gen_ai.operation.name": "chat", "gen_ai.provider.name": "aws.bedrock", "gen_ai.system": "aws.bedrock", "gen_ai.request.model": "amazon.nova-micro-v1:0", "gen_ai.request.max_tokens": int64(2048), "gen_ai.request.temperature": 0.1} {
				if attrs[k] != want {
					t.Errorf("%s=%v want %v", k, attrs[k], want)
				}
			}
			if tc.status == 502 {
				if root.Status().Code != codes.Error || call.Status().Code != codes.Error || attrs["error.type"] != "unavailable" {
					t.Fatalf("failure status=%v attrs=%v", call.Status(), attrs)
				}
				return
			}
			if attrs["gen_ai.usage.input_tokens"] != int64(100) || attrs["gen_ai.usage.output_tokens"] != int64(50) {
				t.Fatalf("missing usage: %v", attrs)
			}
			for _, name := range []string{"plan.merge", "plan.validate_output"} {
				if byName[name] == nil {
					t.Fatalf("missing %s", name)
				}
			}
			if root.EndTime().Before(call.EndTime()) || root.EndTime().Sub(root.StartTime()) > 10*time.Second {
				t.Fatal("invalid span timing")
			}
		})
	}
}
