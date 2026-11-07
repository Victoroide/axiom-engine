package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/internal/config"
)

func boundedProvider(t *testing.T, endpoint string) (*sdktrace.TracerProvider, *traceProcessor) {
	t.Helper()
	processor, err := newLangSmithProcessor(t.Context(), config.LangSmith{Endpoint: endpoint, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	p, ok := processor.(*traceProcessor)
	if !ok {
		t.Fatal("processor missing")
	}
	return tp, p
}

func dropCount(t *testing.T, reason string) float64 {
	t.Helper()
	var metric dto.Metric
	if err := langsmithDroppedSpans.WithLabelValues(reason).Write(&metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}

func assertBufferBound(t *testing.T, p *traceProcessor, want int) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buffered != want || p.buffered > langsmithMaxBufferedSpans || len(p.parents) > langsmithMaxBufferedSpans || len(p.pending) > langsmithMaxBufferedSpans {
		t.Fatalf("buffered=%d parents=%d groups=%d want=%d", p.buffered, len(p.parents), len(p.pending), want)
	}
}

func TestLangSmithBoundsEachGroup(t *testing.T) {
	receiver := newTraceReceiver(t)
	tp, p := boundedProvider(t, receiver.server.URL)
	before := dropCount(t, "group_full")
	tracer := tp.Tracer("limits")
	ctx, root := tracer.Start(t.Context(), "request")
	for range langsmithMaxGroupSpans {
		_, child := tracer.Start(ctx, "chat", trace.WithAttributes(attribute.String("langsmith.span.kind", "llm")))
		child.End()
	}
	root.End()
	assertBufferBound(t, p, 0)
	if got := dropCount(t, "group_full") - before; got != langsmithMaxGroupSpans+1 {
		t.Fatalf("group drops=%v", got)
	}
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(receiver.snapshot()) != 0 {
		t.Fatal("partial group exported")
	}
}

func TestLangSmithBoundsTotalBufferedSpans(t *testing.T) {
	receiver := newTraceReceiver(t)
	tp, p := boundedProvider(t, receiver.server.URL)
	before := dropCount(t, "buffer_full")
	tracer := tp.Tracer("limits")
	roots := make([]trace.Span, 0, langsmithMaxBufferedSpans+1)
	for range langsmithMaxBufferedSpans + 1 {
		_, root := tracer.Start(t.Context(), "open request")
		roots = append(roots, root)
	}
	assertBufferBound(t, p, langsmithMaxBufferedSpans)
	if got := dropCount(t, "buffer_full") - before; got != 1 {
		t.Fatalf("buffer drops=%v", got)
	}
	for _, root := range roots {
		root.End()
	}
	assertBufferBound(t, p, 0)
	if len(receiver.snapshot()) != 0 {
		t.Fatal("non-model group exported")
	}
}

func TestLangSmithCountsQueueDropsAndFlushesFullQueue(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(receiver.Close)
	tp, p := boundedProvider(t, receiver.URL)
	t.Cleanup(unblock)
	before := dropCount(t, "queue_full")
	tracer := tp.Tracer("limits")
	endModel := func() {
		_, span := tracer.Start(t.Context(), "chat", trace.WithAttributes(attribute.String("langsmith.span.kind", "llm")))
		span.End()
	}
	endModel()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("receiver never started")
	}
	for range cap(p.queue) + 1 {
		endModel()
	}
	if got := dropCount(t, "queue_full") - before; got != 1 {
		t.Fatalf("queue drops=%v", got)
	}
	assertBufferBound(t, p, cap(p.queue)+1)
	unblock()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	assertBufferBound(t, p, 0)
}
