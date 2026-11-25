package telemetry

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// contentKey reports whether an attribute key carries prompts, completions,
// message text, input/output values or topic text. The list covers the
// indexed and message-shaped GenAI conventions, the input/output values used
// for run inputs and outputs, and the curriculum topic name.
func contentKey(key string) bool {
	switch key {
	case "gen_ai.prompt", "gen_ai.completion", "gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.system_instructions",
		"gen_ai.tool.definitions", "gen_ai.tool.call.arguments", "gen_ai.tool.call.result", "axiom.topic":
		return true
	}
	return strings.HasPrefix(key, "gen_ai.prompt.") || strings.HasPrefix(key, "gen_ai.completion.") ||
		strings.HasPrefix(key, "input.") || strings.HasPrefix(key, "output.")
}

// contentDepthLimit is the deepest nesting of a structured content value that a view forwards.
const contentDepthLimit = 64

// contentEvent reports whether an event name is a GenAI event, which carries message text.
func contentEvent(name string) bool { return strings.HasPrefix(name, "gen_ai.") }

// genAIEventContentKey also recognizes the unprefixed fields used by message and choice events.
func genAIEventContentKey(key string) bool {
	return key == "content" || key == "message" || contentKey(key)
}

// viewAttributes copies attrs for one destination. A destination without
// content consent loses every content attribute; one with consent keeps them,
// each bounded by capValue.
func viewAttributes(attrs []attribute.KeyValue, allowContent bool, isContent func(string) bool) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if !isContent(string(attr.Key)) {
			out = append(out, attr)
			continue
		}
		if !allowContent {
			continue
		}
		if value, ok := capValue(attr.Value, 1); ok {
			out = append(out, attribute.KeyValue{Key: attr.Key, Value: value})
		}
	}
	return out
}

// capValue bounds a content value at the content limit. Text is cut to a
// valid UTF-8 prefix and bytes to a prefix; inside a slice or map, every text,
// byte and key value is cut the same way and the shape is kept. A slice or map
// nested deeper than contentDepthLimit becomes an empty value, as the SDK does
// for its own depth limit. A representation this code cannot bound is dropped.
func capValue(value attribute.Value, depth int) (attribute.Value, bool) {
	switch value.Type() {
	case attribute.EMPTY, attribute.BOOL, attribute.INT64, attribute.FLOAT64, attribute.BOOLSLICE, attribute.INT64SLICE, attribute.FLOAT64SLICE:
		return value, true
	case attribute.STRING:
		return attribute.StringValue(capContent(value.AsString())), true
	case attribute.STRINGSLICE:
		values := slices.Clone(value.AsStringSlice())
		for i, text := range values {
			values[i] = capContent(text)
		}
		return attribute.StringSliceValue(values), true
	case attribute.BYTESLICE:
		data := value.AsByteSlice()
		return attribute.ByteSliceValue(data[:min(len(data), contentLimit)]), true
	case attribute.SLICE:
		if depth > contentDepthLimit {
			return attribute.Value{}, true
		}
		items := value.AsSlice()
		out := make([]attribute.Value, len(items))
		for i, item := range items {
			out[i], _ = capValue(item, depth+1)
		}
		return attribute.SliceValue(out...), true
	case attribute.MAP:
		if depth > contentDepthLimit {
			return attribute.Value{}, true
		}
		entries := value.AsMap()
		out := make([]attribute.KeyValue, len(entries))
		for i, entry := range entries {
			item, _ := capValue(entry.Value, depth+1)
			out[i] = attribute.KeyValue{Key: attribute.Key(capContent(string(entry.Key))), Value: item}
		}
		return attribute.MapValue(out...), true
	default:
		return attribute.Value{}, false
	}
}

// capContent limits a value to a valid UTF-8 prefix of the content limit.
func capContent(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) > contentLimit {
		value = value[:contentLimit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		// Detach the prefix from the larger string before it can reach a queue.
		return strings.Clone(value)
	}
	return value
}

func hasContentAttribute(attrs []attribute.KeyValue) bool {
	return slices.ContainsFunc(attrs, func(attr attribute.KeyValue) bool { return contentKey(string(attr.Key)) })
}

// viewResource applies the destination's content policy to the resource
// attributes. A resource without content attributes is immutable and shared.
func viewResource(res *resource.Resource, allowContent bool) *resource.Resource {
	if res == nil || !hasContentAttribute(res.Attributes()) {
		return res
	}
	return resource.NewWithAttributes(res.SchemaURL(), viewAttributes(res.Attributes(), allowContent, contentKey)...)
}

// viewScope applies the destination's content policy to the scope attributes.
func viewScope(scope instrumentation.Scope, allowContent bool) instrumentation.Scope {
	if attrs := scope.Attributes.ToSlice(); hasContentAttribute(attrs) {
		scope.Attributes = attribute.NewSet(viewAttributes(attrs, allowContent, contentKey)...)
	}
	return scope
}

// newSpanView snapshots an ended span for a single destination. The snapshot
// owns every slice it exposes and keeps no reference to the source span, so a
// queued view holds only what its destination may receive and a consumer that
// edits it cannot affect another destination or the source.
func newSpanView(span sdktrace.ReadOnlySpan, allowContent bool) sdktrace.ReadOnlySpan {
	events := make([]sdktrace.Event, 0, len(span.Events()))
	for _, event := range span.Events() {
		isContent := contentKey
		if contentEvent(event.Name) {
			if !allowContent {
				continue
			}
			isContent = genAIEventContentKey
		}
		event.Attributes = viewAttributes(event.Attributes, allowContent, isContent)
		events = append(events, event)
	}
	links := slices.Clone(span.Links())
	for i := range links {
		links[i].Attributes = viewAttributes(links[i].Attributes, allowContent, contentKey)
	}
	return tracetest.SpanStub{
		Name:                 span.Name(),
		SpanContext:          span.SpanContext(),
		Parent:               span.Parent(),
		SpanKind:             span.SpanKind(),
		StartTime:            span.StartTime(),
		EndTime:              span.EndTime(),
		Attributes:           viewAttributes(span.Attributes(), allowContent, contentKey),
		Events:               events,
		Links:                links,
		Status:               span.Status(),
		DroppedAttributes:    span.DroppedAttributes(),
		DroppedEvents:        span.DroppedEvents(),
		DroppedLinks:         span.DroppedLinks(),
		ChildSpanCount:       span.ChildSpanCount(),
		Resource:             viewResource(span.Resource(), allowContent),
		InstrumentationScope: viewScope(span.InstrumentationScope(), allowContent),
	}.Snapshot()
}

// viewProcessor hands next a detached view of each ended span, so its queue
// never holds content the destination is not approved to receive, nor the
// source span that still carries it.
type viewProcessor struct {
	next         sdktrace.SpanProcessor
	allowContent bool
}

func newViewProcessor(next sdktrace.SpanProcessor, allowContent bool) sdktrace.SpanProcessor {
	return &viewProcessor{next: next, allowContent: allowContent}
}

func (p *viewProcessor) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	p.next.OnStart(ctx, span)
}

func (p *viewProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	p.next.OnEnd(newSpanView(span, p.allowContent))
}

func (p *viewProcessor) ForceFlush(ctx context.Context) error { return p.next.ForceFlush(ctx) }
func (p *viewProcessor) Shutdown(ctx context.Context) error   { return p.next.Shutdown(ctx) }
