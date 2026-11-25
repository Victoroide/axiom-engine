package telemetry

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/test/tracereceiver"
)

const (
	promptMarker     = "SYNTHETIC-PROMPT-MARKER"
	completionMarker = "SYNTHETIC-COMPLETION-MARKER"
	inputMarker      = "SYNTHETIC-INPUT-MARKER"
	outputMarker     = "SYNTHETIC-OUTPUT-MARKER"
	topicMarker      = "SYNTHETIC-TOPIC-MARKER"
)

var contentMarkers = []string{promptMarker, completionMarker, inputMarker, outputMarker, topicMarker}

// destinationFixture runs Setup against one OTLP-shaped and one LangSmith-shaped local receiver.
type destinationFixture struct {
	otlp, langsmith *tracereceiver.Receiver
	cfg             config.Config
	provider        *sdktrace.TracerProvider
	shutdown        func(context.Context) error
}

func loadTraceConfig(t *testing.T, env map[string]string) (config.Config, error) {
	t.Helper()
	values := map[string]string{"AXIOM_AUTH_DISABLED": "true"}
	for key, value := range env {
		values[key] = value
	}
	return config.Load(func(key string) string { return values[key] })
}

func startDestinations(t *testing.T, env map[string]string) *destinationFixture {
	t.Helper()
	f := &destinationFixture{otlp: tracereceiver.NewOTLP(t), langsmith: tracereceiver.NewLangSmith(t, "langsmith-key")}
	values := map[string]string{"LANGSMITH_ENDPOINT": f.langsmith.URL(), "LANGSMITH_API_KEY": "langsmith-key"}
	for key, value := range env {
		values[key] = value
	}
	cfg, err := loadTraceConfig(t, values)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = cfg
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", f.otlp.TracesURL())
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	previous, previousPropagation, previousHandler := otel.GetTracerProvider(), otel.GetTextMapPropagator(), otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		otel.SetTextMapPropagator(previousPropagation)
		otel.SetErrorHandler(previousHandler)
	})
	f.shutdown, err = Setup(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { _ = f.stop(5 * time.Second) }) })
	f.provider, _ = otel.GetTracerProvider().(*sdktrace.TracerProvider)
	return f
}

func (f *destinationFixture) stop(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), timeout)
	defer cancel()
	return f.shutdown(ctx)
}

func (f *destinationFixture) settingsContext(ctx context.Context) context.Context {
	return WithSettings(ctx, f.cfg.Tracing.CaptureContent(), f.cfg.LangSmith.Enabled)
}

// emitPlanTrace records a request with a topic, a model call and a graph-only sibling the way the service does.
func emitPlanTrace(ctx context.Context, tag string, remote trace.SpanContext) {
	if remote.IsValid() {
		ctx = trace.ContextWithRemoteSpanContext(ctx, remote)
	}
	ctx, root := Start(WithRequestID(ctx, "req-"+tag), "POST /api/v1/adaptive-plan", attribute.String("http.route", "/api/v1/adaptive-plan"))
	CaptureJSON(ctx, "input.value", map[string]string{"failed_topics": inputMarker + tag})
	topicAttrs := []attribute.KeyValue{attribute.Int("axiom.topic.index", 0)}
	if CaptureContent(ctx) {
		topicAttrs = append(topicAttrs, attribute.String("axiom.topic", topicMarker+tag))
	}
	topicCtx, topic := Start(ctx, "plan.topic", topicAttrs...)
	callCtx, call := Start(topicCtx, "chat planner", attribute.String("gen_ai.request.model", "planner"), attribute.Int("gen_ai.usage.input_tokens", 11))
	Annotate(callCtx, call, "llm", "chat planner")
	if CaptureContent(callCtx) {
		SetContent(call, "gen_ai.prompt.0.role", "user")
		SetContent(call, "gen_ai.prompt.0.content", promptMarker+tag)
		SetContent(call, "gen_ai.completion.0.role", "assistant")
		SetContent(call, "gen_ai.completion.0.content", completionMarker+tag)
	}
	call.End()
	topic.End()
	CaptureJSON(ctx, "output.value", map[string]string{"items": outputMarker + tag})
	root.End()
}

func assertNoContentMarkers(t *testing.T, name string, receiver *tracereceiver.Receiver) {
	t.Helper()
	for _, marker := range contentMarkers {
		if n := receiver.Count(marker); n != 0 {
			t.Errorf("%s received %d copies of %s", name, n, marker)
		}
	}
	for _, span := range receiver.Spans() {
		for _, a := range span.Attributes {
			if contentKey(a.Key) {
				t.Errorf("%s received content attribute %s on %s", name, a.Key, span.Name)
			}
		}
	}
}

var (
	structuralRootKeys  = []string{"http.route", "request_id"}
	structuralTopicKeys = []string{"axiom.topic.index", "request_id"}
	structuralCallKeys  = []string{"gen_ai.request.model", "gen_ai.usage.input_tokens", "request_id"}
	contentRootKeys     = []string{"input.value", "output.value"}
	contentTopicKeys    = []string{"axiom.topic"}
	contentCallKeys     = []string{"gen_ai.completion.0.content", "gen_ai.completion.0.role", "gen_ai.prompt.0.content", "gen_ai.prompt.0.role"}
)

func sortedKeys(groups ...[]string) []string {
	keys := slices.Concat(groups...)
	slices.Sort(keys)
	return keys
}

// assertExactFields checks the attribute keys of the three spans received at one destination.
func assertExactFields(t *testing.T, name string, receiver *tracereceiver.Receiver, langsmith, content bool) {
	t.Helper()
	root, topic, call := slices.Clone(structuralRootKeys), slices.Clone(structuralTopicKeys), slices.Clone(structuralCallKeys)
	if langsmith {
		root = append(root, "langsmith.span.kind", "langsmith.trace.name", "langsmith.metadata.request_id")
		topic = append(topic, "langsmith.span.kind", "langsmith.trace.name", "langsmith.metadata.request_id")
		call = append(call, "langsmith.span.kind", "langsmith.trace.name", "langsmith.metadata.request_id")
	}
	if content {
		root, topic, call = append(root, contentRootKeys...), append(topic, contentTopicKeys...), append(call, contentCallKeys...)
	}
	for span, want := range map[string][]string{"POST /api/v1/adaptive-plan": root, "plan.topic": topic, "chat planner": call} {
		if got := receiver.Keys(span); !slices.Equal(got, sortedKeys(want)) {
			t.Errorf("%s span %q attributes=%v want %v", name, span, got, sortedKeys(want))
		}
	}
}

type destinationCase struct {
	name                   string
	env                    map[string]string
	otlpEnabled, lsEnabled bool
	otlpContent, lsContent bool
	invalid                bool
}

func destinationMatrix() []destinationCase {
	cases := make([]destinationCase, 0, 36)
	for _, exporters := range []struct {
		name         string
		otlp, langsm bool
	}{{"otlp", true, false}, {"langsmith", false, true}, {"both", true, true}} {
		for _, capture := range []string{"", "true", "false"} {
			for _, list := range []string{"", "langsmith", "otlp", "langsmith,otlp"} {
				c := destinationCase{name: fmt.Sprintf("%s/capture=%s/list=%s", exporters.name, capture, list), otlpEnabled: exporters.otlp, lsEnabled: exporters.langsm}
				c.env = map[string]string{"TRACING_CAPTURE_CONTENT": capture, "TRACING_CONTENT_DESTINATIONS": list}
				if exporters.otlp {
					c.env["TRACING_ENABLED"] = "true"
				}
				if exporters.langsm {
					c.env["LANGSMITH_TRACING"] = "true"
				}
				listed := func(destination string) bool {
					return slices.Contains(strings.Split(list, ","), destination)
				}
				if (listed("otlp") && !exporters.otlp) || (listed("langsmith") && !exporters.langsm) {
					c.invalid = true
				}
				c.otlpContent = c.otlpEnabled && capture != "false" && listed("otlp")
				c.lsContent = c.lsEnabled && capture != "false" && listed("langsmith")
				cases = append(cases, c)
			}
		}
	}
	return cases
}

func TestContentReachesOnlyApprovedDestinations(t *testing.T) {
	for _, tc := range destinationMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.invalid {
				if _, err := loadTraceConfig(t, tc.env); err == nil {
					t.Fatal("configuration with a disabled destination loaded")
				}
				return
			}
			f := startDestinations(t, tc.env)
			emitPlanTrace(f.settingsContext(t.Context()), "", trace.SpanContext{})
			if err := f.stop(5 * time.Second); err != nil {
				t.Fatal(err)
			}
			for name, receiver := range map[string]*tracereceiver.Receiver{"OTLP": f.otlp, "LangSmith": f.langsmith} {
				enabled, allowed := tc.otlpEnabled, tc.otlpContent
				if name == "LangSmith" {
					enabled, allowed = tc.lsEnabled, tc.lsContent
				}
				if !enabled {
					if receiver.Requests() != 0 {
						t.Errorf("%s received a trace while disabled", name)
					}
					continue
				}
				if len(receiver.Spans()) != 3 {
					t.Fatalf("%s spans=%d", name, len(receiver.Spans()))
				}
				assertExactFields(t, name, receiver, tc.lsEnabled, allowed)
				if allowed {
					for _, marker := range contentMarkers {
						if receiver.Count(marker) != 1 {
							t.Errorf("%s received %d copies of %s", name, receiver.Count(marker), marker)
						}
					}
				} else {
					assertNoContentMarkers(t, name, receiver)
				}
			}
		})
	}
}
