package telemetry

import (
	"context"
	"errors"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// fanoutProcessor hands every span to each destination's processor and stops
// them together. The provider shuts its processors down one after another and
// checks the deadline before each, so a destination that uses the whole
// deadline would leave the next one running. Each processor honors the
// context it is given, which keeps the caller bounded by the same deadline.
type fanoutProcessor struct {
	processors []sdktrace.SpanProcessor
	stop       sync.Once
}

func newFanoutProcessor(processors ...sdktrace.SpanProcessor) *fanoutProcessor {
	return &fanoutProcessor{processors: processors}
}

func (f *fanoutProcessor) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	for _, processor := range f.processors {
		processor.OnStart(ctx, span)
	}
}

func (f *fanoutProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	for _, processor := range f.processors {
		processor.OnEnd(span)
	}
}

func (f *fanoutProcessor) ForceFlush(ctx context.Context) error {
	return f.each(ctx, sdktrace.SpanProcessor.ForceFlush)
}

// Shutdown starts every processor's shutdown with ctx, even one that has
// already expired, and waits for all of them. Later calls do nothing.
func (f *fanoutProcessor) Shutdown(ctx context.Context) error {
	var err error
	f.stop.Do(func() { err = f.each(ctx, sdktrace.SpanProcessor.Shutdown) })
	return err
}

func (f *fanoutProcessor) each(ctx context.Context, call func(sdktrace.SpanProcessor, context.Context) error) error {
	errs := make([]error, len(f.processors))
	var wg sync.WaitGroup
	for i, processor := range f.processors {
		wg.Go(func() { errs[i] = call(processor, ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}
