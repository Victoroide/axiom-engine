package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
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
