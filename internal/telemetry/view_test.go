package telemetry

import (
	"bytes"
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"
	"weak"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

var contentAttributeKeys = []string{
	"gen_ai.prompt.0.role", "gen_ai.prompt.0.content", "gen_ai.prompt.12.content", "gen_ai.completion.0.role", "gen_ai.completion.0.content",
	"gen_ai.prompt", "gen_ai.completion", "gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.system_instructions",
	"gen_ai.tool.definitions", "gen_ai.tool.call.arguments", "gen_ai.tool.call.result",
	"input.value", "output.value", "input.mime_type", "output.mime_type", "axiom.topic",
}

var structuralAttributeKeys = []string{
	"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.system", "gen_ai.request.model", "gen_ai.request.max_tokens", "gen_ai.request.temperature",
	"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "axiom.topic.index", "axiom.queue_wait_ms", "http.route", "http.request.method",
	"http.response.status_code", "error.type", "request_id", "langsmith.span.kind", "langsmith.trace.name", "langsmith.metadata.request_id",
	"rpc.method", "mcp.tool.name", "gen_ai.promptly", "gen_ai.completions", "inputs", "outputs",
}

func endedSpan(t *testing.T, build func(trace.Span)) sdktrace.ReadOnlySpan {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = tp.Shutdown(context.WithoutCancel(t.Context())) })
	_, span := tp.Tracer("view").Start(t.Context(), "chat model")
	build(span)
	span.End()
	return recorder.Ended()[0]
}

func attributeMap(attrs []attribute.KeyValue) map[string]attribute.Value {
	out := make(map[string]attribute.Value, len(attrs))
	for _, a := range attrs {
		out[string(a.Key)] = a.Value
	}
	return out
}

func TestDeniedViewRemovesEveryContentAttribute(t *testing.T) {
	span := endedSpan(t, func(span trace.Span) {
		for _, key := range contentAttributeKeys {
			span.SetAttributes(attribute.String(key, "secret-"+key))
		}
		for _, key := range structuralAttributeKeys {
			span.SetAttributes(attribute.String(key, "visible-"+key))
		}
	})
	view := newSpanView(span, false)
	got := attributeMap(view.Attributes())
	for _, key := range contentAttributeKeys {
		if _, ok := got[key]; ok {
			t.Errorf("denied view kept %s", key)
		}
	}
	for _, key := range structuralAttributeKeys {
		if got[key].AsString() != "visible-"+key {
			t.Errorf("denied view lost structural attribute %s", key)
		}
	}
	if len(got) != len(structuralAttributeKeys) {
		t.Errorf("denied view attributes=%v", got)
	}
}

func TestDeniedViewRemovesContentEventsAndEventAttributes(t *testing.T) {
	span := endedSpan(t, func(span trace.Span) {
		span.AddEvent("gen_ai.user.message", trace.WithAttributes(attribute.String("content", "secret-user")))
		span.AddEvent("gen_ai.choice", trace.WithAttributes(attribute.String("message", "secret-choice")))
		span.AddEvent("gen_ai.client.inference.operation.details", trace.WithAttributes(attribute.String("gen_ai.input.messages", "secret-details")))
		span.AddEvent("exception", trace.WithAttributes(attribute.String("exception.type", "upstream_error"), attribute.String("gen_ai.prompt.0.content", "secret-event-attr"), attribute.String("output.value", "secret-output")))
		span.AddEvent("retry", trace.WithAttributes(attribute.Int("attempt", 2)))
	})
	view := newSpanView(span, false)
	names := make([]string, 0, len(view.Events()))
	for _, event := range view.Events() {
		names = append(names, event.Name)
		for _, a := range event.Attributes {
			if strings.Contains(a.Value.String(), "secret") {
				t.Errorf("event %s kept content attribute %s", event.Name, a.Key)
			}
		}
	}
	if !slices.Equal(names, []string{"exception", "retry"}) {
		t.Fatalf("events=%v", names)
	}
	if got := attributeMap(view.Events()[0].Attributes); got["exception.type"].AsString() != "upstream_error" || len(got) != 1 {
		t.Fatalf("exception attributes=%v", got)
	}
	if len(span.Events()) != 5 {
		t.Fatalf("original span events=%d", len(span.Events()))
	}
}

func TestViewsFilterLinkAttributes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer func() { _ = tp.Shutdown(t.Context()) }()
	_, span := tp.Tracer("view").Start(t.Context(), "linked", trace.WithLinks(trace.Link{SpanContext: callerSpanContext(t), Attributes: []attribute.KeyValue{attribute.String("input.value", "secret-link"), attribute.String("kind", "follows")}}))
	span.End()
	links := newSpanView(recorder.Ended()[0], false).Links()
	if len(links) != 1 || len(links[0].Attributes) != 1 || links[0].Attributes[0].Key != "kind" || links[0].SpanContext.SpanID() != callerSpanContext(t).SpanID() {
		t.Fatalf("links=%v", links)
	}
}

func TestAllowedViewCapsEveryContentValue(t *testing.T) {
	long := strings.Repeat("a", contentLimit+10)
	wide := strings.Repeat("界", 12000)
	invalid := strings.Repeat("b", contentLimit-1) + "\xff\xfe"
	span := endedSpan(t, func(span trace.Span) {
		span.SetAttributes(attribute.String("gen_ai.prompt.0.content", long), attribute.String("input.value", wide), attribute.String("axiom.topic", invalid),
			attribute.StringSlice("gen_ai.input.messages", []string{long, "short"}), attribute.String("gen_ai.request.model", long), attribute.Int("gen_ai.usage.input_tokens", 7))
		span.AddEvent("exception", trace.WithAttributes(attribute.String("output.value", long)))
	})
	view := newSpanView(span, true)
	got := attributeMap(view.Attributes())
	if n := len(got["gen_ai.prompt.0.content"].AsString()); n != contentLimit {
		t.Errorf("prompt length=%d", n)
	}
	if text := got["input.value"].AsString(); len(text) != 32766 || !utf8.ValidString(text) {
		t.Errorf("input length=%d valid=%v", len(text), utf8.ValidString(text))
	}
	if text := got["axiom.topic"].AsString(); !utf8.ValidString(text) || len(text) > contentLimit {
		t.Errorf("topic length=%d valid=%v", len(text), utf8.ValidString(text))
	}
	if slice := got["gen_ai.input.messages"].AsStringSlice(); len(slice) != 2 || len(slice[0]) != contentLimit || slice[1] != "short" {
		t.Errorf("message slice lengths=%d", len(slice))
	}
	if n := len(got["gen_ai.request.model"].AsString()); n != len(long) || got["gen_ai.usage.input_tokens"].AsInt64() != 7 {
		t.Errorf("structural values changed: model length=%d", n)
	}
	if n := len(view.Events()[0].Attributes[0].Value.AsString()); n != contentLimit {
		t.Errorf("event content length=%d", n)
	}
	if n := len(attributeMap(span.Attributes())["gen_ai.prompt.0.content"].AsString()); n != len(long) {
		t.Errorf("original span changed to %d bytes", n)
	}
}

func TestAllowedViewOwnsTruncatedContent(t *testing.T) {
	const capturedKey, directKey = "gen_ai.prompt.0.content", "gen_ai.completion.0.content"
	input := strings.Repeat("界", (4<<20)/len("界"))
	source := endedSpan(t, func(span trace.Span) {
		SetContent(span, capturedKey, input)
		span.SetAttributes(attribute.String(directKey, input))
	})
	attrs := attributeMap(newSpanView(source, true).Attributes())
	want := input[:contentLimit-contentLimit%len("界")]
	for _, key := range []string{capturedKey, directKey} {
		got := attrs[key].AsString()
		if got != want {
			t.Errorf("%s has %d bytes, want the %d-byte UTF-8 prefix", key, len(got), len(want))
		}
		// Pointer equality detects a shared backing allocation without relying on GC.
		if unsafe.StringData(got) == unsafe.StringData(input) { //nolint:gosec // Compare pointers without dereferencing them.
			t.Errorf("%s keeps the original %d-byte string allocation", key, len(input))
		}
	}
}

func TestViewsAreIndependentCopies(t *testing.T) {
	span := endedSpan(t, func(span trace.Span) {
		span.SetAttributes(attribute.String("gen_ai.prompt.0.content", "secret"), attribute.String("gen_ai.request.model", "m"))
		span.AddEvent("exception", trace.WithAttributes(attribute.String("exception.type", "class")))
	})
	before := slices.Clone(span.Attributes())
	allowed, denied := newSpanView(span, true), newSpanView(span, false)
	// A consumer that edits its own view must not change the other view or the source span.
	denied.Attributes()[0] = attribute.String("overwritten", "by denied consumer")
	denied.Events()[0].Name = "overwritten"
	allowed.Attributes()[0] = attribute.String("overwritten", "by allowed consumer")
	if !slices.Equal(span.Attributes(), before) || span.Events()[0].Name != "exception" {
		t.Fatal("a view changed the source span")
	}
	if got := attributeMap(newSpanView(span, true).Attributes()); got["gen_ai.prompt.0.content"].AsString() != "secret" {
		t.Fatalf("a fresh allowed view lost content: %v", got)
	}
	if got := attributeMap(newSpanView(span, false).Attributes()); got["gen_ai.request.model"].AsString() != "m" || len(got) != 1 {
		t.Fatalf("a fresh denied view changed: %v", got)
	}
	if &denied.Attributes()[0] == &allowed.Attributes()[0] {
		t.Fatal("views share attribute storage")
	}
}

func TestViewKeepsSpanIdentity(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	limits := sdktrace.NewSpanLimits()
	limits.AttributeCountLimit, limits.EventCountLimit, limits.LinkCountLimit = 2, 1, 1
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithRawSpanLimits(limits))
	defer func() { _ = tp.Shutdown(t.Context()) }()
	tracer := tp.Tracer("view", trace.WithInstrumentationVersion("2.0"))
	parent, root := tracer.Start(trace.ContextWithRemoteSpanContext(t.Context(), callerSpanContext(t)), "request", trace.WithSpanKind(trace.SpanKindServer))
	_, child := tracer.Start(parent, "child")
	child.End()
	root.SetAttributes(attribute.String("input.value", "secret"), attribute.String("a", "1"), attribute.String("b", "2"))
	root.AddEvent("one")
	root.AddEvent("two")
	root.AddLink(trace.Link{SpanContext: callerSpanContext(t)})
	root.AddLink(trace.Link{SpanContext: callerSpanContext(t)})
	root.SetStatus(codes.Error, "boom")
	root.End()
	span := recorder.Ended()[1]
	for _, allow := range []bool{false, true} {
		view := newSpanView(span, allow)
		if view.Name() != span.Name() || !view.SpanContext().Equal(span.SpanContext()) || !view.Parent().Equal(span.Parent()) || !view.Parent().IsRemote() || view.StartTime() != span.StartTime() || view.EndTime() != span.EndTime() ||
			view.SpanKind() != span.SpanKind() || view.Status() != span.Status() || view.Status().Description != "boom" || view.ChildSpanCount() != span.ChildSpanCount() || view.ChildSpanCount() != 1 ||
			view.DroppedAttributes() != span.DroppedAttributes() || view.DroppedAttributes() != 1 || view.DroppedEvents() != span.DroppedEvents() || view.DroppedEvents() != 1 || view.DroppedLinks() != span.DroppedLinks() || view.DroppedLinks() != 1 ||
			view.Resource() != span.Resource() || view.InstrumentationScope().Name != "view" || view.InstrumentationScope().Version != "2.0" {
			t.Fatalf("view (content allowed=%v) changed span identity, timing, status or counts", allow)
		}
	}
}

// trackedSpan lets a test watch whether anything still points at the span a view was built from.
type trackedSpan struct{ sdktrace.ReadOnlySpan }

func viewOfTrackedSpan(t *testing.T, allowContent bool) (sdktrace.ReadOnlySpan, weak.Pointer[trackedSpan]) {
	t.Helper()
	source := &trackedSpan{endedSpan(t, func(span trace.Span) {
		span.SetAttributes(attribute.String("gen_ai.prompt.0.content", "secret"), attribute.String("gen_ai.request.model", "m"))
		span.AddEvent("exception", trace.WithAttributes(attribute.String("exception.type", "class")))
	})}
	return newSpanView(source, allowContent), weak.Make(source)
}

func TestViewsDoNotRetainTheSourceSpan(t *testing.T) {
	for _, allow := range []bool{false, true} {
		view, source := viewOfTrackedSpan(t, allow)
		// The view stands in for a span waiting in an exporter queue. Once the
		// caller drops the source, nothing the queue holds may keep it alive.
		for range 100 {
			if source.Value() == nil {
				break
			}
			runtime.GC()
		}
		if source.Value() != nil {
			t.Errorf("a view built with content allowed=%v keeps the source span reachable", allow)
		}
		runtime.KeepAlive(view)
	}
}

func TestViewsFilterResourceAndScopeContent(t *testing.T) {
	const resourceSchema, scopeSchema = "https://opentelemetry.io/schemas/1.43.0", "https://opentelemetry.io/schemas/1.42.0"
	long := strings.Repeat("r", contentLimit+10)
	res := resource.NewWithAttributes(resourceSchema, attribute.String("service.name", "axiom-engine"), attribute.String("deployment.environment", "test"),
		attribute.String("input.value", "secret-resource"), attribute.String("gen_ai.prompt.0.content", long))
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSpanProcessor(recorder))
	defer func() { _ = tp.Shutdown(t.Context()) }()
	_, span := tp.Tracer("view", trace.WithInstrumentationVersion("2.0"), trace.WithSchemaURL(scopeSchema),
		trace.WithInstrumentationAttributes(attribute.String("output.value", "secret-scope"), attribute.String("scope.kind", "test"))).Start(t.Context(), "chat")
	span.End()
	source := recorder.Ended()[0]

	denied := newSpanView(source, false)
	resourceKeys := func(span sdktrace.ReadOnlySpan) []string {
		keys := make([]string, 0, 4)
		for _, a := range span.Resource().Attributes() {
			keys = append(keys, string(a.Key))
		}
		slices.Sort(keys)
		return keys
	}
	scopeAttrs := func(span sdktrace.ReadOnlySpan) map[string]attribute.Value {
		scope := span.InstrumentationScope()
		return attributeMap(scope.Attributes.ToSlice())
	}
	if got := resourceKeys(denied); !slices.Equal(got, []string{"deployment.environment", "service.name"}) {
		t.Errorf("denied resource attributes=%v", got)
	}
	if got := scopeAttrs(denied); len(got) != 1 || got["scope.kind"].AsString() != "test" {
		t.Errorf("denied scope attributes=%v", got)
	}
	if denied.Resource().SchemaURL() != resourceSchema || denied.InstrumentationScope().Name != "view" || denied.InstrumentationScope().Version != "2.0" || denied.InstrumentationScope().SchemaURL != scopeSchema {
		t.Errorf("denied view lost structural metadata: schema=%q scope=%+v", denied.Resource().SchemaURL(), denied.InstrumentationScope())
	}

	allowed := newSpanView(source, true)
	got := attributeMap(allowed.Resource().Attributes())
	if got["input.value"].AsString() != "secret-resource" || len(got["gen_ai.prompt.0.content"].AsString()) != contentLimit || got["service.name"].AsString() != "axiom-engine" || allowed.Resource().SchemaURL() != resourceSchema {
		t.Errorf("allowed resource attributes=%v", got)
	}
	if got := scopeAttrs(allowed); got["output.value"].AsString() != "secret-scope" || got["scope.kind"].AsString() != "test" {
		t.Errorf("allowed scope attributes=%v", got)
	}

	// The source keeps its own metadata, and a second denied view is not affected by the first.
	if n := len(attributeMap(source.Resource().Attributes())["gen_ai.prompt.0.content"].AsString()); n != len(long) {
		t.Errorf("source resource changed to %d bytes", n)
	}
	if got := scopeAttrs(source); got["output.value"].AsString() != "secret-scope" {
		t.Errorf("source scope attributes=%v", got)
	}
}

func structuredText(size int) string { return "A" + strings.Repeat("a", size-1) }

// leaves lists the length of every text and byte value in v, keys included.
func leaves(v attribute.Value) (sizes []int, valid bool) {
	valid = true
	var walk func(attribute.Value)
	walk = func(v attribute.Value) {
		switch v.Type() {
		case attribute.STRING:
			sizes, valid = append(sizes, len(v.AsString())), valid && utf8.ValidString(v.AsString())
		case attribute.STRINGSLICE:
			for _, s := range v.AsStringSlice() {
				sizes, valid = append(sizes, len(s)), valid && utf8.ValidString(s)
			}
		case attribute.BYTESLICE:
			sizes = append(sizes, len(v.AsByteSlice()))
		case attribute.SLICE:
			for _, item := range v.AsSlice() {
				walk(item)
			}
		case attribute.MAP:
			for _, kv := range v.AsMap() {
				sizes, valid = append(sizes, len(kv.Key)), valid && utf8.ValidString(string(kv.Key))
				walk(kv.Value)
			}
		}
	}
	walk(v)
	return sizes, valid
}

func nested(depth int) attribute.Value {
	v := attribute.StringValue("leaf")
	for range depth - 1 {
		v = attribute.SliceValue(v)
	}
	return v
}

func TestAllowedViewBoundsStructuredContent(t *testing.T) {
	long, wide := structuredText(contentLimit+1024), strings.Repeat("界", 11264)
	invalid := strings.Repeat("b", contentLimit-1) + "\xff\xfe"
	blob := bytes.Repeat([]byte{0xAB}, contentLimit+1024)
	longKey := attribute.Key(strings.Repeat("k", contentLimit+1))
	span := endedSpan(t, func(span trace.Span) {
		span.SetAttributes(
			attribute.Slice("gen_ai.input.messages",
				attribute.MapValue(attribute.String("role", "user"), attribute.Slice("parts", attribute.MapValue(attribute.String("type", "text"), attribute.String("content", long)))),
				attribute.MapValue(attribute.String("role", "assistant"), attribute.String("content", "short"))),
			attribute.Slice("gen_ai.output.messages", attribute.StringValue(wide), attribute.StringValue(invalid), attribute.Int64Value(7), attribute.BoolValue(true), attribute.Float64Value(1.5)),
			attribute.Map("gen_ai.tool.call.arguments", attribute.ByteSlice("blob", blob), attribute.String("query", "q"), attribute.KeyValue{Key: longKey, Value: attribute.StringValue("v")}),
			attribute.ByteSlice("gen_ai.system_instructions", blob),
			attribute.Map("gen_ai.request.model", attribute.String("name", long)),
		)
		span.AddEvent("exception", trace.WithAttributes(attribute.ByteSlice("output.value", blob), attribute.Map("input.value", attribute.String("content", long))))
	})
	got := attributeMap(newSpanView(span, true).Attributes())
	for _, key := range []string{"gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.tool.call.arguments", "gen_ai.system_instructions"} {
		value, ok := got[key]
		if !ok {
			t.Errorf("allowed view lost %s", key)
			continue
		}
		sizes, valid := leaves(value)
		for _, size := range sizes {
			if size > contentLimit {
				t.Errorf("%s keeps a %d byte value, over the %d byte limit", key, size, contentLimit)
			}
		}
		if !valid {
			t.Errorf("%s holds invalid UTF-8", key)
		}
	}
	messages := got["gen_ai.input.messages"].AsSlice()
	first, second := messages[0].AsMap(), messages[1].AsMap()
	if len(messages) != 2 || len(first) != 2 || len(second) != 2 {
		t.Fatalf("structured message changed shape: %v", got["gen_ai.input.messages"])
	}
	for _, kv := range second {
		if (kv.Key == "role" && kv.Value.AsString() != "assistant") || (kv.Key == "content" && kv.Value.AsString() != "short") {
			t.Errorf("short structured field changed: %v", kv)
		}
	}
	for _, kv := range first {
		if kv.Key == "parts" {
			if sizes, _ := leaves(kv.Value); !slices.Contains(sizes, contentLimit) {
				t.Errorf("long text inside the parts was not cut to the limit: %v", sizes)
			}
		}
	}
	output := got["gen_ai.output.messages"].AsSlice()
	if len(output) != 5 || len(output[0].AsString()) != 32766 || output[2].AsInt64() != 7 || !output[3].AsBool() || output[4].AsFloat64() != 1.5 {
		t.Errorf("output messages=%v", got["gen_ai.output.messages"])
	}
	if n := len(got["gen_ai.system_instructions"].AsByteSlice()); n != contentLimit {
		t.Errorf("byte value length=%d", n)
	}
	if n := len(got["gen_ai.request.model"].AsMap()[0].Value.AsString()); n != len(long) {
		t.Errorf("structural structured value changed to %d bytes", n)
	}
	for _, a := range newSpanView(span, true).Events()[0].Attributes {
		if sizes, _ := leaves(a.Value); len(sizes) == 0 || slices.Max(sizes) > contentLimit {
			t.Errorf("event attribute %s not bounded: %v", a.Key, sizes)
		}
	}
	if sizes, _ := leaves(attributeMap(span.Attributes())["gen_ai.system_instructions"]); sizes[0] != len(blob) {
		t.Errorf("the source span changed to %d bytes", sizes[0])
	}
}

// depth counts the nested slices of v and reports what they end in.
func depth(v attribute.Value) (levels int, last attribute.Type) {
	for v.Type() == attribute.SLICE {
		levels++
		items := v.AsSlice()
		if len(items) != 1 {
			return levels, attribute.EMPTY
		}
		v = items[0]
	}
	return levels, v.Type()
}

func TestAllowedViewBoundsStructuredNesting(t *testing.T) {
	// The SDK limits nesting by default; remove that limit so the view alone has to hold the bound.
	limits := sdktrace.NewSpanLimits()
	limits.AttributeValueDepthLimit = -1
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithRawSpanLimits(limits))
	defer func() { _ = tp.Shutdown(t.Context()) }()
	_, span := tp.Tracer("view").Start(t.Context(), "chat")
	span.SetAttributes(attribute.Slice("gen_ai.completion.0.content", nested(contentDepthLimit)), attribute.Slice("gen_ai.tool.call.result", nested(contentDepthLimit+5)),
		attribute.Slice("gen_ai.tool.call.arguments", nested(5000)))
	span.End()
	source := recorder.Ended()[0]
	if levels, _ := depth(attributeMap(source.Attributes())["gen_ai.tool.call.result"]); levels != contentDepthLimit+5 {
		t.Fatalf("source nesting=%d: the SDK limit was not removed", levels)
	}
	got := attributeMap(newSpanView(source, true).Attributes())
	if levels, last := depth(got["gen_ai.completion.0.content"]); levels != contentDepthLimit || last != attribute.STRING {
		t.Errorf("nesting at the limit changed: levels=%d ends in %s", levels, last)
	}
	for _, key := range []string{"gen_ai.tool.call.result", "gen_ai.tool.call.arguments"} {
		if levels, last := depth(got[key]); levels != contentDepthLimit || last != attribute.EMPTY {
			t.Errorf("%s past the limit: levels=%d ends in %s", key, levels, last)
		}
	}
}

func TestDeniedViewRemovesStructuredContent(t *testing.T) {
	span := endedSpan(t, func(span trace.Span) {
		span.SetAttributes(attribute.Slice("gen_ai.input.messages", attribute.MapValue(attribute.String("content", "secret"))), attribute.ByteSlice("gen_ai.system_instructions", []byte("secret")),
			attribute.Map("gen_ai.tool.call.arguments", attribute.String("q", "secret")), attribute.Slice("gen_ai.prompt.0.content", attribute.StringValue("secret")), attribute.String("gen_ai.request.model", "m"))
	})
	if got := attributeMap(newSpanView(span, false).Attributes()); len(got) != 1 || got["gen_ai.request.model"].AsString() != "m" {
		t.Fatalf("denied view attributes=%v", got)
	}
}

type recordingProcessor struct {
	started, flushed, stopped int
	ended                     []sdktrace.ReadOnlySpan
}

func (p *recordingProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) { p.started++ }
func (p *recordingProcessor) OnEnd(span sdktrace.ReadOnlySpan)                { p.ended = append(p.ended, span) }
func (p *recordingProcessor) ForceFlush(context.Context) error                { p.flushed++; return nil }
func (p *recordingProcessor) Shutdown(context.Context) error                  { p.stopped++; return nil }

func TestViewProcessorForwardsLifecycleAndDeliversViews(t *testing.T) {
	next := &recordingProcessor{}
	processor := newViewProcessor(next, false)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor))
	_, span := tp.Tracer("view").Start(t.Context(), "chat")
	span.SetAttributes(attribute.String("input.value", "secret"), attribute.String("gen_ai.request.model", "m"))
	span.End()
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := tp.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if next.started != 1 || next.flushed != 1 || next.stopped != 1 || len(next.ended) != 1 {
		t.Fatalf("lifecycle=%+v", next)
	}
	if got := attributeMap(next.ended[0].Attributes()); len(got) != 1 || got["gen_ai.request.model"].AsString() != "m" {
		t.Fatalf("delivered attributes=%v", got)
	}
}
