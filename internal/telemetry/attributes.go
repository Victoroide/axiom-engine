package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type settingsKey struct{}
type settings struct{ capture, langsmith bool }

// WithSettings attaches the configured content and exporter policies.
func WithSettings(ctx context.Context, capture, langsmith bool) context.Context {
	return context.WithValue(ctx, settingsKey{}, settings{capture, langsmith})
}

// CaptureContent reports whether prompt and result content may be recorded.
func CaptureContent(ctx context.Context) bool {
	s, _ := ctx.Value(settingsKey{}).(settings)
	return s.capture
}

// Annotate adds the optional LangSmith attributes to a span.
func Annotate(ctx context.Context, span trace.Span, kind, name string) {
	s, _ := ctx.Value(settingsKey{}).(settings)
	if !s.langsmith {
		return
	}
	span.SetAttributes(attribute.String("langsmith.span.kind", kind), attribute.String("langsmith.trace.name", name))
	if id := RequestID(ctx); id != "" {
		span.SetAttributes(attribute.String("langsmith.metadata.request_id", id))
	}
}
