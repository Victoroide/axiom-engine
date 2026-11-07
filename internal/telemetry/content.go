package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const contentLimit = 32 * 1024

// SetContent limits a captured attribute to a valid UTF-8 prefix of 32 KiB.
func SetContent(span trace.Span, key, value string) {
	value = strings.ToValidUTF8(value, "�")
	if len(value) > contentLimit {
		value = value[:contentLimit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	span.SetAttributes(attribute.String(key, value))
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
