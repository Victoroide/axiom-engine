package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/Victoroide/axiom-engine/internal/config"
)

func TestOTLPExport(t *testing.T) {
	var mu sync.Mutex
	var exports []*collector.ExportTraceServiceRequest
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" || r.Header.Get("x-tenant") != "test" || r.Header.Get("Content-Type") != "application/x-protobuf" {
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
		exports = append(exports, &batch)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer receiver.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", receiver.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-tenant=test")
	t.Setenv("OTEL_SERVICE_NAME", "custom-service")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=test")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	previousPropagation := otel.GetTextMapPropagator()
	defer otel.SetTextMapPropagator(previousPropagation)
	shutdown, err := Setup(t.Context(), config.Config{Tracing: config.Tracing{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, span := Start(t.Context(), "test.export")
	span.End()
	if err := shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(exports) != 1 || len(exports[0].ResourceSpans) != 1 {
		t.Fatalf("exports=%v", exports)
	}
	res := exports[0].ResourceSpans[0]
	attrs := map[string]string{}
	for _, a := range res.Resource.Attributes {
		attrs[a.Key] = a.Value.GetStringValue()
	}
	if attrs["service.name"] != "custom-service" || attrs["deployment.environment.name"] != "test" {
		t.Fatalf("resource=%v", attrs)
	}
	if len(res.ScopeSpans) != 1 || len(res.ScopeSpans[0].Spans) != 1 || res.ScopeSpans[0].Spans[0].Name != "test.export" {
		t.Fatalf("batch=%v", res)
	}
}

func TestDisabledTracingKeepsProvider(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", ":invalid")
	shutdown, err := Setup(t.Context(), config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if otel.GetTracerProvider() != previous {
		t.Fatal("disabled tracing installed a provider")
	}
	if err := shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLogCorrelation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer func() { _ = tp.Shutdown(t.Context()) }()
	ctx, span := tp.Tracer("test").Start(t.Context(), "log")
	defer span.End()
	var output bytes.Buffer
	logger := slog.New(LogHandler(slog.NewJSONHandler(&output, nil))).With("component", "test")
	logger.InfoContext(ctx, "correlated")
	for _, value := range []string{`"trace_id":"` + span.SpanContext().TraceID().String() + `"`, `"span_id":"` + span.SpanContext().SpanID().String() + `"`} {
		if !bytes.Contains(output.Bytes(), []byte(value)) {
			t.Fatalf("log=%s", output.String())
		}
	}
	output.Reset()
	logger.WithGroup("nested").InfoContext(context.Background(), "plain")
	if bytes.Contains(output.Bytes(), []byte("trace_id")) {
		t.Fatal("plain log has trace IDs")
	}
	Fail(trace.SpanFromContext(ctx), "unavailable")
	span.SetAttributes(attribute.String("safe", "value"))
}

func TestExporterErrorsAreSafeAndRateLimited(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)
	previousHandler := otel.GetErrorHandler()
	defer otel.SetErrorHandler(previousHandler)
	previousProvider := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previousProvider)
	previousPropagator := otel.GetTextMapPropagator()
	defer otel.SetTextMapPropagator(previousPropagator)
	shutdown, err := Setup(t.Context(), config.Config{Tracing: config.Tracing{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(t.Context()) }()
	for range 1000 {
		otel.Handle(errors.New("sensitive collector body"))
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"level":"WARN"`) || !strings.Contains(lines[0], `"msg":"tracing.export_failed"`) || !strings.Contains(lines[0], `"error_class":"trace_export_failed"`) || strings.Contains(logs.String(), "sensitive") {
		t.Fatalf("export log count=%d sample=%.256s", len(lines), logs.String())
	}
}

func TestDefaultOTLPEndpointUsesHTTP(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "100")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:4318")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	receiver := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/traces" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-protobuf")
	})}
	done := make(chan error, 1)
	go func() { done <- receiver.Serve(listener) }()
	defer func() {
		_ = receiver.Close()
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}()
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	previousPropagator := otel.GetTextMapPropagator()
	defer otel.SetTextMapPropagator(previousPropagator)
	shutdown, err := Setup(t.Context(), config.Config{Tracing: config.Tracing{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, span := Start(t.Context(), "default endpoint")
	span.End()
	err = shutdown(t.Context())
	if err != nil || calls.Load() != 1 {
		t.Fatalf("HTTP exports=%d shutdown=%v", calls.Load(), err)
	}
}
