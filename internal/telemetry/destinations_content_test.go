package telemetry

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"

	"github.com/Victoroide/axiom-engine/test/tracereceiver"
)

const (
	resourceMarker = "SYNTHETIC-RESOURCE-MARKER"
	scopeMarker    = "SYNTHETIC-SCOPE-MARKER"
)

type listCase struct {
	list                   string
	otlpContent, lsContent bool
}

// listCases names which of the two receivers a destination list approves when both exporters run.
var listCases = []listCase{{"langsmith", false, true}, {"otlp", true, false}, {"langsmith,otlp", true, true}}

func (c listCase) approves(receiver string) bool {
	if receiver == "OTLP" {
		return c.otlpContent
	}
	return c.lsContent
}

func receivers(f *destinationFixture) map[string]*tracereceiver.Receiver {
	return map[string]*tracereceiver.Receiver{"OTLP": f.otlp, "LangSmith": f.langsmith}
}

func TestResourceAndScopeContentFollowsDestinationList(t *testing.T) {
	for _, tc := range listCases {
		t.Run(tc.list, func(t *testing.T) {
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment=test,input.value="+resourceMarker)
			f := startDestinations(t, withDestinations(tc.list))
			tracer := f.provider.Tracer("axiom-metadata", trace.WithInstrumentationAttributes(attribute.String("output.value", scopeMarker), attribute.String("scope.kind", "test")))
			ctx, span := tracer.Start(f.settingsContext(t.Context()), "scoped request")
			Annotate(ctx, span, "llm", "scoped request")
			span.End()
			if err := f.stop(5 * time.Second); err != nil {
				t.Fatal(err)
			}
			for name, receiver := range receivers(f) {
				meta := receiver.Metadata()
				if len(meta) == 0 {
					t.Fatalf("%s received no spans", name)
				}
				wantResource, wantScope := []string{"deployment.environment", "service.name"}, []string{"scope.kind"}
				if tc.approves(name) {
					wantResource, wantScope = []string{"deployment.environment", "input.value", "service.name"}, []string{"output.value", "scope.kind"}
				}
				for _, m := range meta {
					if got := mapKeys(m.Resource); !slices.Equal(got, wantResource) {
						t.Errorf("%s resource attributes=%v want %v", name, got, wantResource)
					}
					if got := mapKeys(m.ScopeKV); !slices.Equal(got, wantScope) {
						t.Errorf("%s scope attributes=%v want %v", name, got, wantScope)
					}
					if m.Resource["service.name"] != "axiom-engine" || m.Resource["deployment.environment"] != "test" || m.Scope != "axiom-metadata" {
						t.Errorf("%s lost structural metadata: %+v", name, m)
					}
				}
				want := 0
				if tc.approves(name) {
					want = len(meta)
				}
				if got := receiver.Count(resourceMarker); got != want {
					t.Errorf("%s received %d copies of the resource marker, want %d", name, got, want)
				}
				if got := receiver.Count(scopeMarker); got != want {
					t.Errorf("%s received %d copies of the scope marker, want %d", name, got, want)
				}
			}
		})
	}
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// textSizes lists the length of every text and byte value in v, keys included, and whether all text is valid UTF-8.
func textSizes(v *commonpb.AnyValue) (sizes []int, valid bool) {
	valid = true
	var walk func(*commonpb.AnyValue)
	walk = func(v *commonpb.AnyValue) {
		switch x := v.GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			sizes, valid = append(sizes, len(x.StringValue)), valid && utf8.ValidString(x.StringValue)
		case *commonpb.AnyValue_BytesValue:
			sizes = append(sizes, len(x.BytesValue))
		case *commonpb.AnyValue_ArrayValue:
			for _, item := range x.ArrayValue.GetValues() {
				walk(item)
			}
		case *commonpb.AnyValue_KvlistValue:
			for _, kv := range x.KvlistValue.GetValues() {
				sizes, valid = append(sizes, len(kv.Key)), valid && utf8.ValidString(kv.Key)
				walk(kv.Value)
			}
		}
	}
	walk(v)
	return sizes, valid
}

var structuredKeys = []string{"gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.tool.call.arguments", "gen_ai.system_instructions"}

// emitStructuredTrace records a model call whose message fields are structured values, as the GenAI conventions describe.
func emitStructuredTrace(f *destinationFixture) {
	ascii := promptMarker + strings.Repeat("a", 33792-len(promptMarker))
	wide := strings.Repeat("界", 11264)
	blob := bytes.Repeat([]byte{0xAB}, 40000)
	ctx, call := Start(f.settingsContext(context.Background()), "chat structured")
	Annotate(ctx, call, "llm", "chat structured")
	call.SetAttributes(
		attribute.Slice("gen_ai.input.messages",
			attribute.MapValue(attribute.String("role", "user"), attribute.Slice("parts", attribute.MapValue(attribute.String("type", "text"), attribute.String("content", ascii)))),
			attribute.MapValue(attribute.String("role", "assistant"), attribute.String("content", "short"))),
		attribute.Slice("gen_ai.output.messages", attribute.MapValue(attribute.String("role", "assistant"), attribute.String("content", wide))),
		attribute.Map("gen_ai.tool.call.arguments", attribute.ByteSlice("blob", blob), attribute.String("query", "q")),
		attribute.ByteSlice("gen_ai.system_instructions", blob),
		attribute.Int("gen_ai.usage.input_tokens", 11),
	)
	call.End()
}

func TestStructuredContentIsBoundedAtTheApprovedReceiver(t *testing.T) {
	for _, tc := range listCases {
		t.Run(tc.list, func(t *testing.T) {
			f := startDestinations(t, withDestinations(tc.list))
			emitStructuredTrace(f)
			if err := f.stop(5 * time.Second); err != nil {
				t.Fatal(err)
			}
			for name, receiver := range receivers(f) {
				if len(receiver.Spans()) != 1 {
					t.Fatalf("%s spans=%d", name, len(receiver.Spans()))
				}
				if !tc.approves(name) {
					for _, key := range structuredKeys {
						if got := receiver.Values("chat structured", key); len(got) != 0 {
							t.Errorf("%s received %s", name, key)
						}
					}
					assertNoContentMarkers(t, name, receiver)
					if n := len(receiver.Wire()); n > 4096 {
						t.Errorf("%s received %d bytes for a span without content", name, n)
					}
					continue
				}
				longest := map[string]int{}
				for _, key := range structuredKeys {
					values := receiver.Values("chat structured", key)
					if len(values) != 1 {
						t.Fatalf("%s received %d values of %s", name, len(values), key)
					}
					sizes, valid := textSizes(values[0])
					if !valid {
						t.Errorf("%s received invalid UTF-8 in %s", name, key)
					}
					longest[key] = slices.Max(sizes)
					if longest[key] > contentLimit {
						t.Errorf("%s received a %d byte value in %s, over the %d byte limit", name, longest[key], key, contentLimit)
					}
				}
				if longest["gen_ai.input.messages"] != contentLimit || longest["gen_ai.output.messages"] != 32766 || longest["gen_ai.tool.call.arguments"] != contentLimit || longest["gen_ai.system_instructions"] != contentLimit {
					t.Errorf("%s cut structured values to the wrong size: %v", name, longest)
				}
				if got := receiver.Count(promptMarker); got != 1 {
					t.Errorf("%s received %d copies of the approved prompt", name, got)
				}
				if got := receiver.Count("short"); got < 1 {
					t.Errorf("%s lost a short structured field", name)
				}
			}
		})
	}
}

func TestGenAIEventContentIsBoundedAtTheApprovedReceiver(t *testing.T) {
	const userEvent, choiceEvent = "gen_ai.user.message", "gen_ai.choice"
	const userMarker, choiceMarker = "SYNTHETIC-EVENT-USER", "SYNTHETIC-EVENT-CHOICE"
	plain := userMarker + strings.Repeat("a", 33792-len(userMarker))
	nested := choiceMarker + strings.Repeat("界", 11264)
	for _, tc := range listCases {
		t.Run(tc.list, func(t *testing.T) {
			f := startDestinations(t, withDestinations(tc.list))
			ctx, call := Start(f.settingsContext(t.Context()), "chat with events")
			Annotate(ctx, call, "llm", "chat with events")
			call.AddEvent("retry", trace.WithAttributes(attribute.Int("attempt", 2)))
			call.AddEvent(userEvent, trace.WithAttributes(attribute.String("content", plain)))
			call.AddEvent(choiceEvent, trace.WithAttributes(attribute.Map("message",
				attribute.Slice("parts", attribute.MapValue(attribute.String("content", nested))),
			)))
			call.End()
			if err := f.stop(5 * time.Second); err != nil {
				t.Fatal(err)
			}
			for name, receiver := range receivers(f) {
				spans := receiver.Spans()
				if len(spans) != 1 {
					t.Fatalf("%s spans=%d, want 1", name, len(spans))
				}
				events := spans[0].GetEvents()
				if len(events) == 0 || events[0].GetName() != "retry" {
					t.Fatalf("%s lost the structural retry event", name)
				}
				if !tc.approves(name) {
					if len(events) != 1 || receiver.Count(userMarker) != 0 || receiver.Count(choiceMarker) != 0 {
						t.Errorf("%s received GenAI event content", name)
					}
					continue
				}
				if len(events) != 3 || events[1].GetName() != userEvent || events[2].GetName() != choiceEvent {
					t.Fatalf("%s did not receive both GenAI message events", name)
				}
				for _, want := range []struct {
					index              int
					key, marker        string
					longestFieldLength int
				}{
					{1, "content", userMarker, contentLimit},
					{2, "message", choiceMarker, contentLimit - (contentLimit-len(choiceMarker))%len("界")},
				} {
					event := events[want.index]
					attrs := event.GetAttributes()
					if len(attrs) != 1 || attrs[0].GetKey() != want.key || receiver.Count(want.marker) != 1 {
						t.Fatalf("%s did not receive %s on %s", name, want.key, event.GetName())
					}
					sizes, valid := textSizes(attrs[0].GetValue())
					if len(sizes) == 0 || !valid || slices.Max(sizes) != want.longestFieldLength {
						t.Errorf("%s %s %s field sizes=%v valid UTF-8=%v, want a valid %d-byte prefix", name, event.GetName(), want.key, sizes, valid, want.longestFieldLength)
					}
				}
			}
		})
	}
}
