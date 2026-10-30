// Package api exposes the versioned reasoning HTTP endpoints.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

// Handler contains the dependencies of the versioned HTTP API.
type Handler struct {
	reasoning *service.ReasoningService
	graph     *graph.InMemoryGraph
	timeout   time.Duration
	maxTopics int
	logger    *slog.Logger
}

// NewHandler constructs the reasoning HTTP boundary.
func NewHandler(r *service.ReasoningService, g *graph.InMemoryGraph, timeout time.Duration, maxTopics int, logger *slog.Logger) *Handler {
	return &Handler{reasoning: r, graph: g, timeout: timeout, maxTopics: maxTopics, logger: logger}
}

// RegisterRoutes attaches the versioned API routes.
func (h *Handler) RegisterRoutes(app *fiber.App) {
	v1 := app.Group("/api/v1")
	v1.Post("/adaptive-plan", h.handleAdaptivePlan)
	v1.Get("/tutor/student/:student_id/cognitive-graph", h.handleCognitiveGraph)
}

// handleAdaptivePlan generates a validated study plan.
// @Summary Generate adaptive study plan
// @Description Generate a graph-guided study plan for known topics and a VARK profile.
// @ID generateAdaptivePlan
// @Tags Adaptive Plan
// @Accept json
// @Produce json
// @Security InternalToken
// @Param body body domain.AdaptivePlanRequest true "Student profile and failed concepts"
// @Success 200 {object} domain.PlanResponse
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 413 {object} domain.ErrorResponse
// @Failure 415 {object} domain.ErrorResponse
// @Failure 422 {object} domain.ErrorResponse
// @Failure 429 {object} domain.ErrorResponse
// @Failure 502 {object} domain.ErrorResponse "Upstream failure or no usable item for a requested topic"
// @Failure 503 {object} domain.ErrorResponse "Temporary unavailability, upstream throttling or server shutdown"
// @Header 503 {integer} Retry-After "Seconds to wait before retrying"
// @Failure 504 {object} domain.ErrorResponse
// @Router /api/v1/adaptive-plan [post]
func (h *Handler) handleAdaptivePlan(c fiber.Ctx) error {
	mediaType, params, err := mime.ParseMediaType(c.Get("Content-Type"))
	charset, hasCharset := params["charset"]
	if err != nil || mediaType != "application/json" || (hasCharset && !strings.EqualFold(charset, "utf-8")) {
		return c.Status(fiber.StatusUnsupportedMediaType).JSON(domain.ErrorResponse{Error: "unsupported_media_type", Details: "Content-Type must be application/json"})
	}
	var req domain.AdaptivePlanRequest
	if err := decodeStrictJSON(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(domain.ErrorResponse{Error: "invalid_request_body", Details: err.Error()})
	}
	ctx, cancel := context.WithTimeout(service.WithRequestID(c.Context(), requestid.FromContext(c)), h.timeout)
	defer cancel()
	plan, err := h.reasoning.GeneratePlan(ctx, req)
	if err != nil {
		return h.writePipelineError(c, ctx, err)
	}
	return c.JSON(plan)
}

func (h *Handler) writePipelineError(c fiber.Ctx, ctx context.Context, err error) error {
	var validation *service.ValidationError
	if errors.As(err, &validation) {
		return validationResponse(c, validation)
	}
	var unavailable *service.UnavailableError
	shuttingDown := errors.Is(err, context.Canceled) && errors.Is(c.Context().Err(), context.Canceled)
	status, code, message := fiber.StatusInternalServerError, "internal_error", "reasoning failed"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = fiber.StatusGatewayTimeout, "gateway_timeout", "reasoning exceeded deadline"
	case errors.Is(err, service.ErrThrottled):
		status, code, message = fiber.StatusServiceUnavailable, "service_unavailable", "upstream throttled"
	case errors.Is(err, service.ErrCircuitOpen) || errors.As(err, &unavailable):
		status, code, message = fiber.StatusServiceUnavailable, "service_unavailable", "reasoning temporarily unavailable"
	case shuttingDown:
		status, code, message = fiber.StatusServiceUnavailable, "service_unavailable", "server shutting down"
	case errors.Is(err, context.Canceled):
		status, code, message = fiber.StatusRequestTimeout, "request_timeout", "request canceled"
	case errors.Is(err, service.ErrInvalidOutput):
		status, code, message = fiber.StatusBadGateway, "bad_gateway", "model returned no usable item"
	case errors.Is(err, service.ErrUpstream):
		status, code, message = fiber.StatusBadGateway, "bad_gateway", "upstream reasoning failed"
	}

	if status == fiber.StatusServiceUnavailable {
		retryAfter := 15 * time.Second
		if errors.Is(err, service.ErrThrottled) {
			retryAfter = 5 * time.Second
		}
		if errors.As(err, &unavailable) && unavailable.RetryAfter > 0 {
			retryAfter = unavailable.RetryAfter
		}
		if shuttingDown {
			retryAfter = time.Second
		}
		seconds := retryAfter / time.Second
		if retryAfter%time.Second != 0 {
			seconds++
		}
		c.Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
	}
	attrs := []slog.Attr{slog.String("request_id", requestid.FromContext(c)), slog.String("error_class", code)}
	var topicErr *service.TopicError
	if errors.As(err, &topicErr) {
		attrs = append(attrs, slog.String("topic", fmt.Sprintf("%.128s", topicErr.Topic)))
	}
	h.logger.LogAttrs(ctx, slog.LevelError, "request.failed", attrs...)
	return c.Status(status).JSON(domain.ErrorResponse{Error: code, Details: message})
}

func validationResponse(c fiber.Ctx, err *service.ValidationError) error {
	status := fiber.StatusBadRequest
	if len(err.UnknownTopics) > 0 {
		status = fiber.StatusUnprocessableEntity
	}
	return c.Status(status).JSON(domain.ErrorResponse{Error: "validation_error", Details: err.Details})
}

// handleCognitiveGraph returns a deterministic concept neighborhood.
// @Summary Get cognitive shadow graph
// @Description Return concept nodes annotated with failed, learning or mastered state.
// @ID getCognitiveGraph
// @Tags Tutor
// @Produce json
// @Security InternalToken
// @Param student_id path string true "Student identifier" example(student-42)
// @Param topics query string true "Comma-separated failed topic names" example(Polymorphism,Recursion)
// @Param target_topic query string false "Center the subgraph on one topic" example(Recursion)
// @Success 200 {object} domain.CognitiveGraphResponse
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 422 {object} domain.ErrorResponse
// @Failure 429 {object} domain.ErrorResponse
// @Router /api/v1/tutor/student/{student_id}/cognitive-graph [get]
func (h *Handler) handleCognitiveGraph(c fiber.Ctx) error {
	if !service.ValidIdentifier(c.Params("student_id")) {
		return validationResponse(c, &service.ValidationError{Details: "student_id must match ^[A-Za-z0-9._:-]{1,128}$"})
	}
	topics, err := service.ValidateTopics("topics", strings.Split(c.Query("topics"), ","), h.maxTopics, h.graph)
	if err != nil {
		var validation *service.ValidationError
		errors.As(err, &validation)
		return validationResponse(c, validation)
	}
	target := strings.TrimSpace(c.Query("target_topic"))
	if target != "" {
		stored, ok := h.graph.Resolve(target)
		if !ok {
			return validationResponse(c, &service.ValidationError{Details: "unknown target_topic: " + target, UnknownTopics: []string{target}})
		}
		target = stored
	}
	return c.JSON(h.graph.GenerateCognitiveShadow(topics, target))
}
