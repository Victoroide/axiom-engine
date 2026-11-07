package server

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/goccy/go-json"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/internal/api"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

const traceSettingsKey = "trace_settings"

type traceSettings struct{ capture, langsmith bool }

func traceRequest(c fiber.Ctx) error {
	span := startRequestTrace(c)
	defer func() {
		if finished := api.StreamFinished(c); finished != nil {
			go func() { <-finished; span.End() }()
		} else {
			span.End()
		}
	}()
	err := c.Next()
	finishRequestTrace(c, span, err)
	return err
}

func startRequestTrace(c fiber.Ctx) trace.Span {
	id := requestid.FromContext(c)
	if id == "" {
		id = c.GetRespHeader("X-Request-ID")
	}
	carrier := propagation.MapCarrier{"traceparent": c.Get("traceparent"), "tracestate": c.Get("tracestate")}
	propagator := propagation.TraceContext{}
	base := telemetry.WithRequestID(c.Context(), id)
	if cfg, ok := fiber.GetState[traceSettings](c.App().State(), traceSettingsKey); ok {
		base = telemetry.WithSettings(base, cfg.capture, cfg.langsmith)
	}
	ctx := propagator.Extract(base, carrier)
	method := traceMethod(c.Method())
	attrs := []attribute.KeyValue{semconv.HTTPRequestMethodKey.String(method), attribute.String("request_id", id)}
	if method == "_OTHER" {
		attrs = append(attrs, semconv.HTTPRequestMethodOriginal(c.Method()))
	}
	ctx, span := otel.Tracer("github.com/Victoroide/axiom-engine").Start(ctx, traceName(c.Method(), ""), trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...))
	telemetry.Annotate(ctx, span, "chain", c.Method())
	c.SetContext(ctx)
	out := propagation.MapCarrier{}
	propagator.Inject(ctx, out)
	for key, value := range out {
		c.Set(key, value)
	}
	return span
}

func finishRequestTrace(c fiber.Ctx, span trace.Span, err error) {
	route := "unmatched"
	if endpoint := c.Endpoint(); endpoint != nil {
		route = endpoint.Path
	}
	status := c.Response().StatusCode()
	span.SetName(traceName(c.Method(), route))
	telemetry.Annotate(c.Context(), span, "chain", traceName(c.Method(), route))
	span.SetAttributes(semconv.HTTPRoute(route), semconv.HTTPResponseStatusCode(status))
	if status >= 400 {
		var response domain.ErrorResponse
		class := "http_" + strconv.Itoa(status)
		if json.Unmarshal(c.Response().Body(), &response) == nil && response.Error != "" {
			class = response.Error
		}
		span.SetAttributes(attribute.String("error.type", class))
		if status >= 500 {
			telemetry.Fail(span, class)
		}
	}
	if err != nil {
		telemetry.Fail(span, fmt.Sprintf("http_%d", fiber.StatusInternalServerError))
	}
}

func traceMethod(method string) string {
	switch method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace, "QUERY":
		return method
	default:
		return "_OTHER"
	}
}

func traceName(method, route string) string {
	if traceMethod(method) == "_OTHER" {
		return "HTTP"
	}
	if route == "" {
		return method
	}
	return method + " " + route
}
