package mcpserver

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

func traceTools(cfg config.Config, logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			name := "unmatched"
			if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
				switch params.Name {
				case "generate_study_plan", "get_cognitive_graph", "list_topics", "get_topic_path":
					name = params.Name
				}
			}
			id := uuid.NewString()
			ctx = service.WithRequestID(ctx, id)
			ctx = telemetry.WithSettings(ctx, cfg.Tracing.CaptureContent, cfg.LangSmith.Enabled)
			ctx, span := otel.Tracer("github.com/Victoroide/axiom-engine").Start(ctx, "tools/call "+name, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("rpc.system.name", "mcp"), attribute.String("rpc.method", "tools/call"), attribute.String("mcp.tool.name", name), attribute.String("request_id", id)))
			telemetry.Annotate(ctx, span, "chain", "tools/call "+name)
			defer span.End()
			start := time.Now()
			result, err := next(ctx, method, req)
			class := "ok"
			if err != nil {
				class = "protocol_error"
			} else if call, ok := result.(*mcp.CallToolResult); ok && call.IsError {
				class = "validation_error"
				if failure, ok := call.StructuredContent.(domain.ErrorResponse); ok && failure.Error != "" {
					class = failure.Error
				}
			}
			if class != "ok" {
				telemetry.Fail(span, class)
			}
			logger.InfoContext(ctx, "mcp.tool", "request_id", id, "tool", name, "error_class", class, "duration_ms", float64(time.Since(start).Microseconds())/1000)
			return result, err
		}
	}
}
