package llm

import (
	"context"
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
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/mcpserver"
	"github.com/Victoroide/axiom-engine/internal/server"
)

const (
	learnerID = "private-learner-fixture"
	courseID  = "private-course-fixture"
)

type providerFailure struct {
	mode, code, detail, retryAfter string
	status, repeat, attempts       int
}

func TestAdapterProviderPublicContracts(t *testing.T) {
	scenarios := []providerFailure{
		{mode: "empty", status: 502, code: "bad_gateway", detail: "model returned no usable item", repeat: 1, attempts: 1},
		{mode: "incomplete", status: 502, code: "bad_gateway", detail: "model returned no usable item", repeat: 1, attempts: 1},
		{mode: "no-content", status: 502, code: "bad_gateway", detail: "model returned no usable item", repeat: 3, attempts: 1},
		{mode: "malformed", status: 502, code: "bad_gateway", detail: "model returned no usable item", repeat: 3, attempts: 1},
		{mode: "max-tokens", status: 502, code: "bad_gateway", detail: "model returned no usable item", repeat: 1, attempts: 1},
		{mode: "unauthorized", status: 502, code: "bad_gateway", detail: "upstream reasoning failed", repeat: 3, attempts: 1},
		{mode: "denied", status: 502, code: "bad_gateway", detail: "upstream reasoning failed", repeat: 3, attempts: 1},
		{mode: "unknown", status: 502, code: "bad_gateway", detail: "upstream reasoning failed", repeat: 1, attempts: 1},
		{mode: "ok", status: 200, repeat: 1, attempts: 1}, // A successful call resets the failure from the unknown status.
		{mode: "throttle", status: 503, code: "service_unavailable", detail: "upstream throttled", retryAfter: "5", repeat: 1, attempts: 1},
		{mode: "internal", status: 502, code: "bad_gateway", detail: "upstream reasoning failed", repeat: 1, attempts: 1},
		{mode: "hang", status: 504, code: "gateway_timeout", detail: "reasoning exceeded deadline", repeat: 1, attempts: 1},
		{mode: "ok", status: 503, code: "service_unavailable", detail: "reasoning temporarily unavailable", retryAfter: "15", repeat: 1, attempts: 0},
	}
	for _, provider := range nativeProviders {
		t.Run(provider, func(t *testing.T) {
			for _, transport := range []string{"HTTP", "MCP"} {
				t.Run(transport, func(t *testing.T) {
					f := newProviderFixture(t, provider, "ok")
					recorder := recordProviderSpans(t)
					cfg, err := config.Load(func(key string) string {
						switch key {
						case "AXIOM_AUTH_DISABLED":
							return "true"
						case "LLM_TIMEOUT":
							return "500ms"
						default:
							return ""
						}
					})
					if err != nil {
						t.Fatal(err)
					}
					cfg.Tracing.Enabled, cfg.Tracing.CaptureContent = true, true
					g, planner := graph.NewInMemoryGraph(), New(f.settings)
					logger := slog.New(slog.NewTextHandler(io.Discard, nil))
					var call func(*testing.T, providerFailure) []byte
					if transport == "HTTP" {
						app := server.New(cfg, server.Dependencies{Graph: g, Planner: planner, Logger: logger})
						call = func(t *testing.T, tc providerFailure) []byte { return callProviderHTTP(t, app, tc) }
					} else {
						cs := connectProviderMCP(t, mcpserver.New(cfg, g, planner, logger, "test"))
						call = func(t *testing.T, tc providerFailure) []byte { return callProviderMCP(t, cs, tc) }
					}
					for _, tc := range scenarios {
						f.setMode(tc.mode)
						spanStart := len(recorder.Ended())
						for range tc.repeat {
							before := f.count()
							body := call(t, tc)
							if tc.status == http.StatusOK {
								assertPublicProviderPlan(t, body)
							} else {
								assertPublicProviderFailure(t, tc, body)
							}
							if attempts := f.count() - before; attempts != tc.attempts {
								t.Fatalf("%s dispatched %d attempts, want %d", tc.mode, attempts, tc.attempts)
							}
						}
						if tc.mode == "unknown" {
							assertUnknownProviderTrace(t, recorder.Ended()[spanStart:])
						}
					}
					for _, request := range f.capturedRequests() {
						if !strings.Contains(request.body, "Recursion") {
							t.Error("provider request missing topic")
						}
						for _, identifier := range []string{learnerID, courseID, "student_id", "course_id"} {
							if strings.Contains(request.body, identifier) || strings.Contains(request.headers, identifier) || strings.Contains(request.target, identifier) {
								t.Errorf("provider request contains %q", identifier)
							}
						}
					}
				})
			}
		})
	}
}

func callProviderHTTP(t *testing.T, app *fiber.App, tc providerFailure) []byte {
	t.Helper()
	body := `{"student_id":"` + learnerID + `","course_id":"` + courseID + `","failed_topics":["Recursion"],"vark_profile":"visual"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/adaptive-plan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if retry := resp.Header.Get("Retry-After"); resp.StatusCode != tc.status || retry != tc.retryAfter {
		t.Fatalf("%s: HTTP %d with Retry-After %q and body %s, want HTTP %d with Retry-After %q", tc.mode, resp.StatusCode, retry, data, tc.status, tc.retryAfter)
	}
	return data
}

func connectProviderMCP(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := c.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callProviderMCP(t *testing.T, cs *mcp.ClientSession, tc providerFailure) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_study_plan", Arguments: mcpserver.PlanInput{
		StudentID: learnerID, CourseID: courseID, FailedTopics: []string{"Recursion"}, VARKProfile: "visual",
	}})
	if err != nil {
		t.Fatalf("%s: MCP call failed: %v", tc.mode, err)
	}
	if result.IsError != (tc.status != http.StatusOK) {
		t.Fatalf("%s: MCP isError = %t", tc.mode, result.IsError)
	}
	if result.IsError {
		if len(result.Content) != 1 {
			t.Fatalf("%s: MCP error has %d content entries, want 1", tc.mode, len(result.Content))
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("%s: MCP error content has type %T, want text", tc.mode, result.Content[0])
		}
		assertPublicProviderFailure(t, tc, []byte(text.Text))
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertPublicProviderPlan(t *testing.T, body []byte) {
	t.Helper()
	var plan domain.PlanResponse
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.StudentID != learnerID || plan.CourseID != courseID || len(plan.Items) != 1 || plan.Items[0].Topic != "Recursion" || !slices.Equal(plan.Items[0].PrerequisiteChain, []string{"Functions"}) {
		t.Fatalf("unexpected validated plan: %+v", plan)
	}
}

func assertPublicProviderFailure(t *testing.T, tc providerFailure, body []byte) {
	t.Helper()
	var envelope map[string]string
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope) != 2 || envelope["error"] != tc.code || envelope["details"] != tc.detail {
		t.Fatalf("%s: error envelope = %s, want %q and %q", tc.mode, body, tc.code, tc.detail)
	}
}

func recordProviderSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		if err := tp.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return recorder
}

func assertUnknownProviderTrace(t *testing.T, spans []sdktrace.ReadOnlySpan) {
	t.Helper()
	var calls int
	for _, span := range spans {
		data, err := json.Marshal(tracetest.SpanStubFromReadOnlySpan(span))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), providerText) {
			t.Errorf("span %s contains provider diagnostics", span.Name())
		}
		if span.Name() == "chat "+fixtureModel {
			calls++
			if status := span.Status(); status.Code != codes.Error || status.Description != "error" {
				t.Errorf("unknown upstream error span status = %+v", status)
			}
		}
	}
	if calls != 1 {
		t.Fatalf("model call spans = %d, want 1", calls)
	}
}
