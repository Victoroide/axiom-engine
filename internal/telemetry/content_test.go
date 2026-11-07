package telemetry

import (
	"strings"
	"testing"
	"unicode/utf8"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestContentTruncationPreservesUTF8(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer func() { _ = tp.Shutdown(t.Context()) }()
	_, span := tp.Tracer("test").Start(t.Context(), "content")
	SetContent(span, "unicode", strings.Repeat("界", 12000))
	SetContent(span, "ascii", strings.Repeat("a", 40000))
	SetContent(span, "exact", strings.Repeat("b", 32768))
	span.End()
	attrs := recorder.Ended()[0].Attributes()
	values := map[string]string{}
	for _, a := range attrs {
		values[string(a.Key)] = a.Value.AsString()
	}
	if len(values["unicode"]) != 32766 || !utf8.ValidString(values["unicode"]) || len(values["ascii"]) != 32768 || len(values["exact"]) != 32768 {
		t.Fatalf("lengths=%d/%d/%d", len(values["unicode"]), len(values["ascii"]), len(values["exact"]))
	}
}
