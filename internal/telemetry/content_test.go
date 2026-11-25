package telemetry

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
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

type countingValue struct{ marshaled *int }

func (v countingValue) MarshalJSON() ([]byte, error) { *v.marshaled++; return []byte(`"value"`), nil }

// TestDeniedCaptureBuildsNoContent shows a denied policy skips serialization and leaves the span structural.
func TestDeniedCaptureBuildsNoContent(t *testing.T) {
	for _, capture := range []bool{false, true} {
		t.Run("capture="+strconv.FormatBool(capture), func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer func() { _ = tp.Shutdown(t.Context()) }()
			ctx, span := tp.Tracer("test").Start(WithSettings(t.Context(), capture, false), "request")
			marshaled := 0
			CaptureJSON(ctx, "input.value", countingValue{&marshaled})
			span.SetAttributes(attribute.Int("http.response.status_code", 200))
			span.End()
			attrs := attributeMap(recorder.Ended()[0].Attributes())
			if _, ok := attrs["input.value"]; ok != capture || marshaled != map[bool]int{false: 0, true: 1}[capture] {
				t.Fatalf("input present=%v marshaled=%d", ok, marshaled)
			}
			if attrs["http.response.status_code"].AsInt64() != 200 {
				t.Fatal("structural attribute lost")
			}
		})
	}
}
