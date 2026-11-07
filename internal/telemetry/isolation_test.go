package telemetry

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/internal/config"
)

func configureTraceReceiver(t *testing.T, receiver *traceReceiver, generic bool) *sdktrace.TracerProvider {
	t.Helper()
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	previousPropagation := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(previousPropagation) })
	previousHandler := otel.GetErrorHandler()
	t.Cleanup(func() { otel.SetErrorHandler(previousHandler) })
	shutdown, err := Setup(t.Context(), config.Config{Tracing: config.Tracing{Enabled: generic}, LangSmith: config.LangSmith{Enabled: true, Endpoint: receiver.server.URL, APIKey: "test-key"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := shutdown(cleanup); err != nil {
			t.Error(err)
		}
	})
	tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok {
		t.Fatal("SDK provider missing")
	}
	return tp
}

func callerSpanContext(t *testing.T) trace.SpanContext {
	t.Helper()
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	return trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true})
}

func TestLangSmithRerootsRemoteParentsOnly(t *testing.T) {
	langsmith := newTraceReceiver(t)
	generic := newTraceReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", generic.server.URL+"/v1/traces")
	tp := configureTraceReceiver(t, langsmith, true)
	parent := callerSpanContext(t)
	ctx, root := Start(trace.ContextWithRemoteSpanContext(t.Context(), parent), "request")
	_, call := Start(ctx, "chat", attribute.String("langsmith.span.kind", "llm"))
	call.End()
	root.End()
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	batches := langsmith.snapshot()
	if len(batches) != 1 {
		t.Fatalf("LangSmith batches=%d", len(batches))
	}
	ids := map[string]bool{}
	for _, span := range batches[0] {
		ids[hex.EncodeToString(span.SpanId)] = true
	}
	for _, span := range batches[0] {
		if len(span.ParentSpanId) > 0 && !ids[hex.EncodeToString(span.ParentSpanId)] {
			t.Fatalf("missing parent for %s: %x", span.Name, span.ParentSpanId)
		}
	}
	found := false
	for _, batch := range generic.snapshot() {
		for _, span := range batch {
			if span.Name == "request" {
				found = true
				parentID := parent.SpanID()
				if !bytes.Equal(span.ParentSpanId, parentID[:]) {
					t.Fatalf("generic parent=%x", span.ParentSpanId)
				}
			}
		}
	}
	if !found {
		t.Fatal("generic request span missing")
	}
}

func TestLangSmithSeparatesOverlappingLocalRoots(t *testing.T) {
	receiver := newTraceReceiver(t)
	tp := configureTraceReceiver(t, receiver, false)
	base := trace.ContextWithRemoteSpanContext(t.Context(), callerSpanContext(t))
	firstCtx, first := Start(base, "first request")
	secondCtx, second := Start(base, "second request")
	_, firstCall := Start(firstCtx, "first chat", attribute.String("langsmith.span.kind", "llm"))
	firstCall.End()
	first.End()
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if batches := receiver.snapshot(); len(batches) != 1 {
		t.Fatalf("completed request exports=%d while the other request is active", len(batches))
	}
	_, secondCall := Start(secondCtx, "second chat", attribute.String("langsmith.span.kind", "llm"))
	secondCall.End()
	second.End()
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	batches := receiver.snapshot()
	if len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 2 {
		t.Fatalf("independent batches=%v", batches)
	}
}

func TestLangSmithFiltersNonModelTraces(t *testing.T) {
	receiver := newTraceReceiver(t)
	tp := configureTraceReceiver(t, receiver, false)
	for range 1000 {
		_, probe := Start(t.Context(), "GET /livez")
		probe.End()
	}
	ctx, root := Start(t.Context(), "request")
	_, call := Start(ctx, "chat", attribute.String("langsmith.span.kind", "llm"))
	call.End()
	root.End()
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	batches := receiver.snapshot()
	if len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("probe flood exports=%d, want one model trace", len(batches))
	}
}
