package telemetry

import (
	"context"
	"encoding/json"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const contentLimit = 32 * 1024

// SetContent limits a captured attribute to a valid UTF-8 prefix of 32 KiB.
func SetContent(span trace.Span, key, value string) {
	span.SetAttributes(attribute.String(key, capContent(value)))
}

// CaptureJSON records an explicitly selected value when content capture is on.
func CaptureJSON(ctx context.Context, key string, value any) {
	span := trace.SpanFromContext(ctx)
	if !CaptureContent(ctx) || !span.IsRecording() {
		return
	}
	body, err := json.Marshal(value)
	if err == nil {
		SetContent(span, key, string(body))
	}
}
