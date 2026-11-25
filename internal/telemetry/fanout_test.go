package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// gatedProcessor waits for a signal before it finishes shutting down.
type gatedProcessor struct {
	recordingProcessor
	gate <-chan struct{}
}

func (p *gatedProcessor) Shutdown(ctx context.Context) error {
	p.stopped++
	select {
	case <-p.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// signalingProcessor reports that its shutdown started.
type signalingProcessor struct {
	recordingProcessor
	started chan struct{}
}

func (p *signalingProcessor) Shutdown(context.Context) error {
	p.stopped++
	close(p.started)
	return nil
}

type failingProcessor struct {
	recordingProcessor
	err error
}

func (p *failingProcessor) Shutdown(context.Context) error { p.stopped++; return p.err }
func (p *failingProcessor) ForceFlush(context.Context) error {
	p.flushed++
	return p.err
}

func TestFanoutStartsEveryShutdownBeforeAnyFinishes(t *testing.T) {
	started := make(chan struct{})
	gated, signaling := &gatedProcessor{gate: started}, &signalingProcessor{started: started}
	fan := newFanoutProcessor(gated, signaling)
	// The first processor cannot finish until the second has started, so
	// a sequential shutdown would run into the deadline.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := fan.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown=%v", err)
	}
	if gated.stopped != 1 || signaling.stopped != 1 {
		t.Fatalf("stopped=%d,%d", gated.stopped, signaling.stopped)
	}
}

func TestFanoutShutsEveryProcessorDownWithAnExpiredContext(t *testing.T) {
	a, b := &recordingProcessor{}, &recordingProcessor{}
	fan := newFanoutProcessor(a, b)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := fan.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown=%v", err)
	}
	if a.stopped != 1 || b.stopped != 1 {
		t.Fatalf("stopped=%d,%d: a processor was skipped", a.stopped, b.stopped)
	}
}

func TestFanoutReportsEveryFailureAndShutsDownOnce(t *testing.T) {
	errA, errB := errors.New("first failed"), errors.New("second failed")
	a, b := &failingProcessor{err: errA}, &failingProcessor{err: errB}
	fan := newFanoutProcessor(a, b)
	if err := fan.Shutdown(t.Context()); !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("shutdown=%v", err)
	}
	if err := fan.Shutdown(t.Context()); err != nil {
		t.Fatalf("second shutdown=%v", err)
	}
	if a.stopped != 1 || b.stopped != 1 {
		t.Fatalf("stopped=%d,%d", a.stopped, b.stopped)
	}
}

func TestFanoutForwardsSpansAndFlushesEveryProcessor(t *testing.T) {
	errA := errors.New("first failed")
	a, b := &failingProcessor{err: errA}, &recordingProcessor{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(newFanoutProcessor(a, b)))
	_, span := tp.Tracer("fanout").Start(t.Context(), "chat")
	span.End()
	if err := tp.ForceFlush(t.Context()); !errors.Is(err, errA) {
		t.Fatalf("flush=%v", err)
	}
	if a.started != 1 || len(a.ended) != 1 || b.started != 1 || len(b.ended) != 1 || a.flushed != 1 || b.flushed != 1 {
		t.Fatalf("a=%+v b=%+v", a.recordingProcessor, b)
	}
}
