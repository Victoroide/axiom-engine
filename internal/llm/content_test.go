package llm

import (
	"reflect"
	"strconv"
	"testing"

	runtime "github.com/boundaryml/baml/engine/language_client_go/pkg"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

// countingCollector fails any method other than Last, which it counts.
type countingCollector struct {
	runtime.Collector
	last int
}

func (c *countingCollector) Last() (runtime.FunctionLog, error) { c.last++; return nil, nil }

// TestDeniedCaptureNeverReadsTheNativeCollector shows prompts and completions are not requested when no receiver may have them.
func TestDeniedCaptureNeverReadsTheNativeCollector(t *testing.T) {
	for _, capture := range []bool{false, true} {
		t.Run("capture="+strconv.FormatBool(capture), func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer func() { _ = tp.Shutdown(t.Context()) }()
			_, span := tp.Tracer("llm").Start(t.Context(), "chat")
			collector := &countingCollector{}
			captureCollector(telemetry.WithSettings(t.Context(), capture, false), span, collector, "openai-generic")
			span.End()
			if want := map[bool]int{false: 0, true: 1}[capture]; collector.last != want {
				t.Fatalf("collector reads=%d want %d", collector.last, want)
			}
		})
	}
}

func TestPromptMessages(t *testing.T) {
	for _, tc := range []struct {
		provider, body string
		want           []promptMessage
	}{
		{"aws-bedrock", `{"system":[{"text":"system"}],"messages":[{"role":"user","content":[{"text":"first"},{"text":"second"}]}]}`, []promptMessage{{"system", "system"}, {"user", "first\nsecond"}}},
		{"openai", `{"messages":[{"role":"system","content":"system"},{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"private"}}]}]}`, []promptMessage{{"system", "system"}, {"user", "hello"}}},
		{"ollama", `{"messages":[{"role":"user","content":"hello"}]}`, []promptMessage{{"user", "hello"}}},
		{"openai-generic", `{"messages":[{"role":"user","content":"hello"}]}`, []promptMessage{{"user", "hello"}}},
		{"anthropic", `{"system":"system","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`, []promptMessage{{"system", "system"}, {"user", "hello"}}},
		{"anthropic", `{"system":[{"type":"text","text":"system"}],"messages":[{"role":"user","content":"hello"}]}`, []promptMessage{{"system", "system"}, {"user", "hello"}}},
		{"google-ai", `{"systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"reply"}]}]}`, []promptMessage{{"system", "system"}, {"user", "hello"}, {"assistant", "reply"}}},
		{"other", `{"text":"hello"}`, []promptMessage{{"user", `{"text":"hello"}`}}},
		{"openai", `raw text`, []promptMessage{{"user", "raw text"}}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			if got := promptMessages(tc.provider, tc.body); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("messages=%v want=%v", got, tc.want)
			}
		})
	}
}
