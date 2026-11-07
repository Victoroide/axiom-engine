package telemetry

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/internal/config"
)

func newLangSmithProcessor(ctx context.Context, cfg config.LangSmith) (sdktrace.SpanProcessor, error) {
	headers := map[string]string{"x-api-key": cfg.APIKey}
	if cfg.Project != "" {
		headers["Langsmith-Project"] = cfg.Project
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.Endpoint+"/otel/v1/traces"),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithEncoding(otlptracehttp.EncodingProtobuf),
		otlptracehttp.WithCompression(otlptracehttp.NoCompression),
		otlptracehttp.WithTimeout(10*time.Second),
		otlptracehttp.WithHTTPClient(&http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}),
	)
	if err != nil {
		return nil, errors.New("configure LangSmith exporter")
	}
	exportCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p := &traceProcessor{exporter: exporter, pending: make(map[spanKey]*pendingTrace), parents: make(map[spanKey]*pendingTrace), queue: make(chan traceBatch, 128), space: make(chan struct{}, 1), done: make(chan struct{}), cancel: cancel}
	go p.export(exportCtx)
	return p, nil
}

const (
	langsmithMaxBufferedSpans = 2048
	langsmithMaxGroupSpans    = 256
)

type spanKey struct {
	traceID trace.TraceID
	spanID  trace.SpanID
}

func keyForSpan(sc trace.SpanContext) spanKey { return spanKey{sc.TraceID(), sc.SpanID()} }

type pendingTrace struct {
	root     spanKey
	active   int
	members  []spanKey
	spans    []sdktrace.ReadOnlySpan
	hasModel bool
}

type traceBatch struct {
	spans   []sdktrace.ReadOnlySpan
	flushed chan error
}

type localRootSpan struct{ sdktrace.ReadOnlySpan }

func (localRootSpan) Parent() trace.SpanContext { return trace.SpanContext{} }

// traceProcessor buffers bounded groups rooted in this process.
type traceProcessor struct {
	mu        sync.Mutex
	stopped   bool
	pending   map[spanKey]*pendingTrace
	parents   map[spanKey]*pendingTrace
	buffered  int
	queue     chan traceBatch
	space     chan struct{}
	done      chan struct{}
	cancel    context.CancelFunc
	exporter  sdktrace.SpanExporter
	exportErr error
}

func (p *traceProcessor) OnStart(_ context.Context, span sdktrace.ReadWriteSpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	id := keyForSpan(span.SpanContext())
	parent := span.Parent()
	var group *pendingTrace
	if !parent.IsValid() || parent.IsRemote() {
		if p.buffered >= langsmithMaxBufferedSpans {
			countDroppedSpans("buffer_full", 1)
			return
		}
		group = &pendingTrace{root: id}
		p.pending[id] = group
	} else {
		group = p.parents[keyForSpan(parent)]
		if group == nil {
			countDroppedSpans("missing_parent", 1)
			return
		}
	}
	reason := ""
	if len(group.members) >= langsmithMaxGroupSpans {
		reason = "group_full"
	} else if p.buffered >= langsmithMaxBufferedSpans {
		reason = "buffer_full"
	}
	if reason != "" {
		p.dropGroup(group, reason)
		countDroppedSpans(reason, 1)
		return
	}
	group.active++
	group.members = append(group.members, id)
	p.parents[id] = group
	p.buffered++
}

func (p *traceProcessor) removeGroup(group *pendingTrace) {
	delete(p.pending, group.root)
	for _, id := range group.members {
		delete(p.parents, id)
	}
}

func (p *traceProcessor) dropGroup(group *pendingTrace, reason string) {
	p.removeGroup(group)
	p.buffered -= len(group.members)
	countDroppedSpans(reason, len(group.members))
}

func (p *traceProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	group := p.parents[keyForSpan(span.SpanContext())]
	if group == nil {
		return
	}
	group.active--
	for _, attr := range span.Attributes() {
		if string(attr.Key) == "langsmith.span.kind" && attr.Value.AsString() == "llm" {
			group.hasModel = true
			break
		}
	}
	if span.Parent().IsRemote() {
		span = localRootSpan{span}
	}
	group.spans = append(group.spans, span)
	if group.active > 0 {
		return
	}
	p.removeGroup(group)
	if !group.hasModel {
		p.buffered -= len(group.members)
		return
	}
	select {
	case p.queue <- traceBatch{spans: group.spans}:
	default:
		p.buffered -= len(group.members)
		countDroppedSpans("queue_full", len(group.members))
	}
}

func (p *traceProcessor) export(base context.Context) {
	var lastErr, drainErr error
	defer func() { p.exportErr = drainErr; close(p.done) }()
	for batch := range p.queue {
		select {
		case p.space <- struct{}{}:
		default:
		}
		if batch.flushed != nil {
			batch.flushed <- lastErr
			lastErr = nil
			continue
		}
		ctx, cancel := context.WithTimeout(base, 10*time.Second)
		err := p.exporter.ExportSpans(ctx, batch.spans)
		cancel()
		if err != nil {
			lastErr = errors.New("LangSmith trace export failed")
		}
		p.mu.Lock()
		if p.stopped && err != nil {
			drainErr = lastErr
		}
		p.buffered -= len(batch.spans)
		p.mu.Unlock()
		if err != nil {
			otel.Handle(lastErr)
		}
	}
}

func (p *traceProcessor) ForceFlush(ctx context.Context) error {
	flushed := make(chan error, 1)
	for {
		p.mu.Lock()
		if p.stopped {
			p.mu.Unlock()
			return nil
		}
		sent := false
		select {
		case p.queue <- traceBatch{flushed: flushed}:
			sent = true
		default:
		}
		p.mu.Unlock()
		if sent {
			break
		}
		select {
		case <-p.space:
		case <-p.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case err := <-flushed:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *traceProcessor) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if !p.stopped {
		p.stopped = true
		close(p.queue)
		for _, group := range p.pending {
			p.dropGroup(group, "shutdown")
		}
		p.pending = nil
		p.parents = nil
	}
	p.mu.Unlock()
	select {
	case <-p.done:
		p.cancel()
		return errors.Join(p.exportErr, p.exporter.Shutdown(ctx))
	case <-ctx.Done():
		p.cancel()
		_ = p.exporter.Shutdown(ctx)
		return ctx.Err()
	}
}
