package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/Victoroide/axiom-engine/internal/config"
)

func TestExportersKeepHeadersAndEndpointsSeparate(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	receiver := func(name, path string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				t.Errorf("%s path=%s", name, r.URL.Path)
			}
			if r.Header.Get("Content-Type") != "application/x-protobuf" {
				t.Errorf("%s content type=%s", name, r.Header.Get("Content-Type"))
			}
			if name == "generic" {
				if r.Header.Get("X-Tenant") != "generic-only" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Langsmith-Project") != "" {
					t.Errorf("generic headers=%v", r.Header)
				}
			} else if r.Header.Get("X-Api-Key") != "langsmith-only" || r.Header.Get("Langsmith-Project") != "study" || r.Header.Get("X-Tenant") != "" {
				t.Errorf("LangSmith headers=%v", r.Header)
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
			counts[name]++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/x-protobuf")
		}))
	}
	generic := receiver("generic", "/generic/traces")
	defer generic.Close()
	langsmith := receiver("langsmith", "/otel/v1/traces")
	defer langsmith.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", generic.URL+"/ignored")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", generic.URL+"/generic/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-tenant=wrong-default")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "x-tenant=generic-only")
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	previousPropagation := otel.GetTextMapPropagator()
	defer otel.SetTextMapPropagator(previousPropagation)
	shutdown, err := Setup(t.Context(), config.Config{Tracing: config.Tracing{Enabled: true}, LangSmith: config.LangSmith{Enabled: true, Endpoint: langsmith.URL, APIKey: "langsmith-only", Project: "study"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(t.Context()) }()
	_, span := Start(t.Context(), "export", attribute.String("langsmith.span.kind", "llm"))
	span.End()
	if err := shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["generic"] != 1 || counts["langsmith"] != 1 {
		t.Fatalf("exports=%v", counts)
	}
}

func TestLangSmithWaitsForWholeTrace(t *testing.T) {
	var mu sync.Mutex
	var batches []*collector.ExportTraceServiceRequest
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1/generic")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "x-api-key=wrong-generic-key")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/json")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION", "gzip")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "1")
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	previousPropagation := otel.GetTextMapPropagator()
	defer otel.SetTextMapPropagator(previousPropagation)
	shutdown, err := Setup(t.Context(), config.Config{LangSmith: config.LangSmith{Enabled: true, Endpoint: receiver.URL, APIKey: "study-key"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(t.Context()) }()
	tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok {
		t.Fatal("no SDK provider")
	}
	ctx, parent := Start(t.Context(), "parent")
	_, child := Start(ctx, "child", attribute.String("langsmith.span.kind", "llm"))
	child.End()
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before := len(batches)
	mu.Unlock()
	if before != 0 {
		t.Fatal("child exported before its parent finished")
	}
	parent.End()
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx2, parent2 := Start(t.Context(), "second parent")
	_, child2 := Start(ctx2, "second child", attribute.String("langsmith.span.kind", "llm"))
	parent2.End()
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before = len(batches)
	mu.Unlock()
	if before != 1 {
		t.Fatal("parent exported while a child was still active")
	}
	child2.End()
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 2 {
		t.Fatalf("batches=%d", len(batches))
	}
	spans := batches[0].ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 2 {
		t.Fatalf("spans=%v", spans)
	}
}

func TestLangSmithShutdownRespectsDeadline(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	receiver := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer receiver.Close()
	defer close(release)
	previous := otel.GetTracerProvider()
	defer otel.SetTracerProvider(previous)
	previousPropagation := otel.GetTextMapPropagator()
	defer otel.SetTextMapPropagator(previousPropagation)
	shutdown, err := Setup(t.Context(), config.Config{LangSmith: config.LangSmith{Enabled: true, Endpoint: receiver.URL, APIKey: "test-key"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(t.Context()) }()
	_, span := Start(t.Context(), "deadline", attribute.String("langsmith.span.kind", "llm"))
	span.End()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("export never started")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("shutdown exceeded its deadline")
	}
}
