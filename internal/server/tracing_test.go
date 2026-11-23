package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

func TestHTTPPanicPrivacy(t *testing.T) {
	const marker = "private-http-panic-value"
	if os.Getenv("AXIOM_TEST_HTTP_PANIC_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHTTPPanicPrivacy$") //nolint:gosec // Reexecute this test and capture direct process output.
		cmd.Env = append(cmd.Environ(), "AXIOM_TEST_HTTP_PANIC_CHILD=1")
		output, err := cmd.CombinedOutput()
		if bytes.Contains(output, []byte(marker)) {
			t.Error("recovered value appeared in HTTP process output")
		}
		if err != nil {
			t.Fatalf("HTTP panic subprocess: %v\n%s", err, output)
		}
		return
	}
	const planBody = `{"failed_topics":["Recursion"],"vark_profile":"visual"}`
	const streamPath = "/api/v1/adaptive-plan/stream"
	for _, tc := range []struct {
		name, method, path, body, event, details string
		value                                    error
	}{
		{"planner", http.MethodPost, "/api/v1/adaptive-plan", planBody, "planner.panic", "reasoning failed", errors.New(strings.Repeat(marker, 128))},
		{"planner returned sentinel", http.MethodPost, "/api/v1/adaptive-plan", planBody, "request.failed", "reasoning failed", service.ErrPlannerPanic},
		{"planner SSE", http.MethodPost, streamPath, planBody, "planner.panic", "reasoning failed", errors.New(marker)},
		{"route error", http.MethodGet, "/panic", "", "panic.recovered", "Internal Server Error", errors.New(marker)},
		{"Fiber error", http.MethodGet, "/panic", "", "panic.recovered", "Internal Server Error", fiber.NewError(http.StatusTeapot, marker)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(tp)
			defer otel.SetTracerProvider(previous)
			defer func() { _ = tp.Shutdown(t.Context()) }()

			cfg := defaultConfig(t)
			cfg.Tracing.Enabled = true
			cfg.Tracing.CaptureContent = true
			logs := make(chan []byte, 8)
			planner := fakePlanner(func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
				if errors.Is(tc.value, service.ErrPlannerPanic) {
					return nil, tc.value
				}
				panic(tc.value)
			})
			logger := slog.New(telemetry.LogHandler(slog.NewJSONHandler(streamLogWriter{records: logs}, nil)))
			app := New(cfg, Dependencies{Graph: graph.NewInMemoryGraph(), Planner: planner, Logger: logger})
			app.Get("/panic", func(fiber.Ctx) error { panic(tc.value) })
			id := strings.Repeat("r", 128)
			resp, body := call(t, app, tc.method, tc.path, tc.body, map[string]string{"X-Request-ID": id})
			want := domain.ErrorResponse{Error: "internal_error", Details: tc.details}
			if resp.Header.Get("X-Request-ID") != id {
				t.Errorf("response request ID=%q", resp.Header.Get("X-Request-ID"))
			}
			if tc.path == streamPath {
				events := parseEvents(t, body)
				if resp.StatusCode != http.StatusOK || len(events) != 2 || events[0].name != "topic" || events[1].name != "error" {
					t.Fatalf("stream response status=%d events=%v", resp.StatusCode, events)
				}
				var terminal domain.PlanStreamError
				if err := json.Unmarshal(events[1].data, &terminal); err != nil {
					t.Fatal(err)
				}
				if terminal.Status != http.StatusInternalServerError || terminal.ErrorResponse != want {
					t.Errorf("stream error=%+v", terminal)
				}
			} else {
				var envelope domain.ErrorResponse
				if err := json.Unmarshal([]byte(body), &envelope); err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusInternalServerError || envelope != want {
					t.Errorf("response status=%d body=%s", resp.StatusCode, body)
				}
			}
			spans := panicRequestSpans(t, exporter, tc.method+" "+tc.path)
			traceData, err := json.Marshal(spans)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(body, marker) || bytes.Contains(traceData, []byte(marker)) {
				t.Error("recovered error appeared in HTTP response or trace")
			}
			rootFound := false
			for _, span := range spans {
				if span.Name == tc.method+" "+tc.path {
					if tc.path == streamPath {
						rootFound = span.Status.Code == codes.Unset
						for _, attr := range span.Attributes {
							if attr.Key == "error.type" {
								rootFound = false
							}
						}
					} else {
						rootFound = span.Status.Code == codes.Error && span.Status.Description == "internal_error"
					}
				}
			}
			if !rootFound {
				t.Errorf("unexpected request span status: %s", traceData)
			}
			errorsLogged := 0
			for _, line := range requestLogRecords(t, logs, "http.access") {
				if bytes.Contains(line, []byte(marker)) {
					t.Error("recovered error appeared in HTTP logs")
				}
				var record map[string]any
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				if record["level"] != "ERROR" {
					continue
				}
				errorsLogged++
				traceID, _ := record["trace_id"].(string)
				spanID, _ := record["span_id"].(string)
				fields := 7
				if tc.event == "request.failed" {
					fields++
				}
				if record["msg"] != tc.event || record["request_id"] != id || record["error_class"] != "internal_error" || len(traceID) != 32 || len(spanID) != 16 || len(record) != fields || len(line) > 1024 {
					t.Errorf("unexpected panic log fields or size (%d bytes, event %q)", len(line), record["msg"])
				}
			}
			if errorsLogged != 1 {
				t.Errorf("got %d error records, want one", errorsLogged)
			}
		})
	}
}

func panicRequestSpans(t *testing.T, exporter *tracetest.InMemoryExporter, root string) tracetest.SpanStubs {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		spans := exporter.GetSpans()
		for _, span := range spans {
			if span.Name == root {
				return spans
			}
		}
	}
	t.Fatalf("missing root span %q", root)
	return nil
}

func requestLogRecords(t *testing.T, records <-chan []byte, last string) [][]byte {
	t.Helper()
	var lines [][]byte
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case line := <-records:
			lines = append(lines, line)
			var header struct {
				Message string `json:"msg"`
			}
			if err := json.Unmarshal(line, &header); err != nil {
				t.Fatal(err)
			}
			if header.Message == last {
				return lines
			}
		case <-timer.C:
			t.Fatalf("missing log event %q", last)
		}
	}
}

func TestLatePlannerPanicRetainsHTTPRequestID(t *testing.T) {
	const id = "original-id"
	const replacement = "recycled-id"
	const marker = "private-late-panic-value"
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = tp.Shutdown(t.Context()) }()
	cfg := defaultConfig(t)
	cfg.Tracing.Enabled = true
	cfg.RequestTimeout, cfg.LLMTimeout = time.Minute, time.Minute
	started := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	logs := make(chan []byte, 8)
	app := newTestApp(t, cfg, func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
		close(started)
		<-release
		panic(errors.New(marker))
	}, streamLogWriter{records: logs})
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	app.State().Set(requestContextKey, base)
	var req fasthttp.Request
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("X-Request-ID", id)
	req.SetRequestURI("/api/v1/adaptive-plan")
	req.SetBodyString(`{"failed_topics":["Recursion"],"vark_profile":"visual"}`)
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	finished := make(chan struct{})
	go func() {
		app.Handler()(&ctx)
		close(finished)
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-started:
	case <-timer.C:
		t.Fatal("planner did not start")
	}
	cancel()
	select {
	case <-finished:
	case <-timer.C:
		t.Fatal("handler did not return after cancellation")
	}
	if ctx.Response.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("canceled response=%d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	// Reuse the header storage after the handler returned and before the planner exits.
	if n := copy(ctx.Request.Header.Peek("X-Request-ID"), replacement); n != len(replacement) {
		t.Fatalf("copied %d request ID bytes", n)
	}
	unblock()
	lines := requestLogRecords(t, logs, "planner.panic")
	line := lines[len(lines)-1]
	if bytes.Contains(line, []byte(marker)) || !bytes.Contains(line, []byte(`"request_id":"`+id+`"`)) {
		t.Errorf("late recovery lost its original request ID or exposed the recovered value: %s", line)
	}
	rootHasID := false
	for _, span := range panicRequestSpans(t, exporter, "POST /api/v1/adaptive-plan") {
		for _, attr := range span.Attributes {
			if attr.Key == "request_id" {
				if attr.Value.AsString() != id {
					t.Errorf("span %q has recycled request ID %q", span.Name, attr.Value.AsString())
				}
				if span.Name == "POST /api/v1/adaptive-plan" {
					rootHasID = true
				}
			}
		}
	}
	if !rootHasID {
		t.Error("root span is missing the request ID")
	}
}

func TestHTTPTraceUsesRouteTemplate(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = tp.Shutdown(t.Context()) }()
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
	app := New(cfg, Dependencies{Graph: graph.NewInMemoryGraph(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	for _, method := range []string{http.MethodGet, "BREW"} {
		req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/tutor/student/private-learner/cognitive-graph?failed_topics=Recursion", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("spans=%d", len(spans))
	}
	if spans[0].Name() != "GET /api/v1/tutor/student/:student_id/cognitive-graph" {
		t.Fatal(spans[0].Name())
	}
	attrs := map[string]string{}
	for _, a := range spans[1].Attributes() {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if attrs["http.request.method"] != "_OTHER" || attrs["http.request.method_original"] != "BREW" {
		t.Fatalf("method attributes=%v", attrs)
	}
	if spans[1].Name() != "HTTP" || spans[1].Status().Code != codes.Error {
		t.Fatalf("unknown method span=%s %v", spans[1].Name(), spans[1].Status())
	}
}

func TestDisabledTracingIgnoresCallerContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = tp.Shutdown(t.Context()) }()
	cfg, err := config.Load(func(k string) string {
		if k == "AXIOM_AUTH_DISABLED" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	app := New(cfg, Dependencies{Graph: graph.NewInMemoryGraph(), Logger: slog.New(telemetry.LogHandler(slog.NewJSONHandler(&logs, nil)))})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil)
	req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	req.Header.Set("tracestate", "vendor=opaque")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("traceparent") != "" || resp.Header.Get("tracestate") != "" || strings.Contains(logs.String(), "trace_id") || len(recorder.Ended()) != 0 {
		t.Fatalf("disabled tracing handled caller context: headers=%v spans=%d logs=%s", resp.Header, len(recorder.Ended()), logs.String())
	}
}

func TestStreamTraceEndsAfterGeneration(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = tp.Shutdown(t.Context()) }()
	cfg := defaultConfig(t)
	cfg.Tracing.Enabled = true
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	app := newTestApp(t, cfg, func(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []domain.PlanItem{{Topic: r.Topic, Explanation: "Practice", Resources: []domain.Resource{}}}, nil
	}, nil)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/adaptive-plan/stream", strings.NewReader(`{"failed_topics":["Recursion"],"vark_profile":"visual"}`))
	req.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		res, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
		if err == nil {
			err = res.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("generation did not start")
	}
	endedEarly := false
	observeUntil := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(observeUntil) {
		for _, span := range recorder.Ended() {
			if span.Name() == "POST /api/v1/adaptive-plan/stream" {
				endedEarly = true
			}
		}
		if endedEarly {
			break
		}
		time.Sleep(time.Millisecond)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if endedEarly {
		t.Fatal("stream root ended while generation was active")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		spans := recorder.Ended()
		var root sdktrace.ReadOnlySpan
		for _, span := range spans {
			if span.Name() == "POST /api/v1/adaptive-plan/stream" {
				root = span
			}
		}
		if root != nil {
			for _, span := range spans {
				if span.EndTime().After(root.EndTime()) {
					t.Fatalf("%s ended after stream root", span.Name())
				}
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stream root never ended")
}
