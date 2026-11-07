// Package telemetry configures tracing and safe span annotations.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/internal/config"
)

// Setup installs the enabled exporter and returns its bounded shutdown hook.
func Setup(ctx context.Context, cfg config.Config) (func(context.Context) error, error) {
	if !cfg.Tracing.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	otel.SetErrorHandler(&exportErrorHandler{logger: slog.Default()})
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("axiom-engine")), resource.WithFromEnv())
	if err != nil {
		return nil, fmt.Errorf("configure trace resource: %w", err)
	}
	var options []otlptracehttp.Option
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		options = append(options, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("configure OTLP exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exporter))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return provider.Shutdown, nil
}

type requestIDKey struct{}

// WithRequestID attaches transport-independent trace correlation.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the correlation identifier attached to the context.
func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }

// Start begins an internal span without recording request bodies or identifiers.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if id := RequestID(ctx); id != "" {
		attrs = append(attrs, attribute.String("request_id", id))
	}
	return otel.Tracer("github.com/Victoroide/axiom-engine").Start(ctx, name, trace.WithAttributes(attrs...))
}

// Fail records only a stable error class, keeping upstream details private.
func Fail(span trace.Span, class string) {
	span.SetAttributes(attribute.String("error.type", class))
	span.RecordError(errors.New(class))
	span.SetStatus(codes.Error, class)
}

type logHandler struct{ slog.Handler }

// LogHandler adds trace correlation to records with a valid span context.
func LogHandler(next slog.Handler) slog.Handler { return logHandler{next} }

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{h.Handler.WithAttrs(attrs)}
}
func (h logHandler) WithGroup(name string) slog.Handler { return logHandler{h.Handler.WithGroup(name)} }
