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
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

// Handler contains the dependencies of the versioned HTTP API.
type Handler struct {
	reasoning   *service.ReasoningService
	graph       *graph.InMemoryGraph
	timeout     time.Duration
	maxTopics   int
	maxMastered int
	logger      *slog.Logger
}

// NewHandler constructs the reasoning HTTP boundary.
func NewHandler(r *service.ReasoningService, g *graph.InMemoryGraph, timeout time.Duration, maxTopics, maxMastered int, logger *slog.Logger) *Handler {
	return &Handler{reasoning: r, graph: g, timeout: timeout, maxTopics: maxTopics, maxMastered: maxMastered, logger: logger}
}

// RegisterRoutes attaches the versioned API routes.
func (h *Handler) RegisterRoutes(app *fiber.App, planMiddleware ...fiber.Handler) {
	v1 := app.Group("/api/v1")
	handlers := make([]any, 0, len(planMiddleware)+1)
	for _, middleware := range planMiddleware {
		handlers = append(handlers, middleware)
	}
	handlers = append(handlers, h.handleAdaptivePlan)
	v1.Post("/adaptive-plan", handlers[0], handlers[1:]...)
	v1.Get("/topics", h.handleTopics)
	v1.Get("/topics/:topic/path", h.handleTopicPath)
	v1.Post("/adaptive-plan/stream", h.handlePlanStream)
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
// @Param Idempotency-Key header string false "1 to 255 visible ASCII characters; successful responses retained for 24h by default"
// @Failure 409 {object} domain.ErrorResponse "idempotency_key_in_progress"
// @Header 409 {integer} Retry-After "Seconds to wait before retrying"
// @Param body body domain.AdaptivePlanRequest true "Student profile and failed concepts"
// @Success 200 {object} domain.PlanResponse
// @Header 200 {boolean} Idempotent-Replayed "True when replaying a stored response"
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 408 {object} domain.ErrorResponse "Request not read within the 10 s read timeout"
// @Failure 413 {object} domain.ErrorResponse
// @Failure 415 {object} domain.ErrorResponse
// @Failure 422 {object} domain.ErrorResponse
// @Failure 429 {object} domain.ErrorResponse
// @Failure 502 {object} domain.ErrorResponse "Upstream failure or no usable item for a requested topic"
// @Failure 503 {object} domain.ErrorResponse "Temporary unavailability, upstream throttling or server shutdown"
// @Header 503 {integer} Retry-After "Seconds to wait before retrying"
// @Failure 504 {object} domain.ErrorResponse
// @Failure 431 {object} domain.ErrorResponse "Request headers exceed the server limit"
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse "Shared storage unavailable"
// @Header 503 {integer} Retry-After "Seconds to wait before retrying"
// @Header all {string} X-Request-ID "Resolved request correlation identifier"
// @Header 200,400,409,413,415,422,502,503,504 {integer} X-RateLimit-Limit "Maximum requests per rate-limit window"
// @Header 200,400,409,413,415,422,502,503,504 {integer} X-RateLimit-Remaining "Requests remaining in the rate-limit window"
// @Header 200,400,409,413,415,422,502,503,504 {integer} X-RateLimit-Reset "Seconds until the rate-limit window resets"
// @Header 429 {integer} Retry-After "Seconds to wait before retrying"
// @Router /api/v1/adaptive-plan [post]
func (h *Handler) handleAdaptivePlan(c fiber.Ctx) error {
	req, accepted, err := readPlanRequest(c)
	if !accepted {
		return err
	}
	ctx, cancel := context.WithTimeout(service.WithRequestID(c.Context(), requestid.FromContext(c)), h.timeout)
	defer cancel()
	telemetry.CaptureJSON(ctx, "input.value", struct {
		FailedTopics []string `json:"failed_topics"`
		VARKProfile  string   `json:"vark_profile"`
	}{req.FailedTopics, req.VARKProfile})
	plan, err := h.reasoning.GeneratePlan(ctx, req)
	if err != nil {
		return h.writePipelineError(c, ctx, err)
	}
	telemetry.CaptureJSON(ctx, "output.value", plan.Items)
	return c.JSON(plan)
}

type pipelineFailure struct {
	status       int
	envelope     domain.ErrorResponse
	retrySeconds int64
}

func (h *Handler) writePipelineError(c fiber.Ctx, ctx context.Context, err error) error {
	failure := h.classifyPipelineError(ctx, requestid.FromContext(c), err, errors.Is(c.Context().Err(), context.Canceled))
	if failure.retrySeconds > 0 {
		c.Set("Retry-After", strconv.FormatInt(failure.retrySeconds, 10))
	}
	return c.Status(failure.status).JSON(failure.envelope)
}

func (h *Handler) classifyPipelineError(ctx context.Context, requestID string, err error, parentCanceled bool) pipelineFailure {
	failure := service.ClassifyError(err, errors.Is(err, context.Canceled) && parentCanceled)
	retrySeconds := int64(failure.RetryAfter / time.Second)
	if failure.RetryAfter%time.Second != 0 {
		retrySeconds++
	}
	if failure.Response.Error != "validation_error" {
		attrs := []slog.Attr{slog.String("request_id", requestID), slog.String("error_class", failure.Response.Error)}
		var topicErr *service.TopicError
		if errors.As(err, &topicErr) {
			attrs = append(attrs, slog.String("topic", fmt.Sprintf("%.128s", topicErr.Topic)))
		}
		h.logger.LogAttrs(ctx, slog.LevelError, "request.failed", attrs...)
	}
	return pipelineFailure{status: failure.Status, envelope: failure.Response, retrySeconds: retrySeconds}
}

func validationResponse(c fiber.Ctx, err *service.ValidationError) error {
	failure := service.ClassifyError(err, false)
	return c.Status(failure.Status).JSON(failure.Response)
}

// handleCognitiveGraph returns a deterministic concept neighborhood.
// @Summary Get cognitive shadow graph
// @Description Return concept nodes annotated with failed, learning, mastered or unassessed state.
// @ID getCognitiveGraph
// @Tags Tutor
// @Produce json
// @Security InternalToken
// @Param student_id path string true "Student identifier" example(student-42)
// @Param topics query string true "Comma-separated failed topic names; may be repeated" example(Polymorphism,Recursion)
// @Param mastered query []string false "Mastered topic names; may be repeated or comma-separated" collectionFormat(multi)
// @Param target_topic query string false "Center the subgraph on one topic; must be one of topics" example(Recursion)
// @Success 200 {object} domain.CognitiveGraphResponse
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 408 {object} domain.ErrorResponse "Request not read within the 10 s read timeout"
// @Failure 413 {object} domain.ErrorResponse "Request body exceeds 64 KiB"
// @Failure 422 {object} domain.ErrorResponse
// @Failure 429 {object} domain.ErrorResponse
// @Failure 431 {object} domain.ErrorResponse "Request headers exceed the server limit"
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse "Shared storage unavailable"
// @Header 503 {integer} Retry-After "Seconds to wait before retrying"
// @Header all {string} X-Request-ID "Resolved request correlation identifier"
// @Header 200,400,413,422 {integer} X-RateLimit-Limit "Maximum requests per rate-limit window"
// @Header 200,400,413,422 {integer} X-RateLimit-Remaining "Requests remaining in the rate-limit window"
// @Header 200,400,413,422 {integer} X-RateLimit-Reset "Seconds until the rate-limit window resets"
// @Header 429 {integer} Retry-After "Seconds to wait before retrying"
// @Router /api/v1/tutor/student/{student_id}/cognitive-graph [get]
func (h *Handler) handleCognitiveGraph(c fiber.Ctx) error {
	if !service.ValidIdentifier(c.Params("student_id")) {
		return validationResponse(c, &service.ValidationError{Details: "student_id must match ^[A-Za-z0-9._:-]{1,128}$"})
	}
	var requested []string
	for _, value := range c.Request().URI().QueryArgs().PeekMulti("topics") {
		requested = append(requested, strings.Split(string(value), ",")...)
	}
	targets := make([]string, 0, 1)
	for _, value := range c.Request().URI().QueryArgs().PeekMulti("target_topic") {
		targets = append(targets, string(value))
	}
	var mastered []string
	if values := c.Request().URI().QueryArgs().PeekMulti("mastered"); len(values) > 0 {
		mastered = make([]string, 0)
		for _, value := range values {
			mastered = append(mastered, strings.Split(string(value), ",")...)
		}
	}
	result, err := service.CognitiveGraph(requested, targets, mastered, h.maxTopics, h.maxMastered, h.graph)
	if err != nil {
		var validation *service.ValidationError
		errors.As(err, &validation)
		return validationResponse(c, validation)
	}
	return c.JSON(result)
}

func readPlanRequest(c fiber.Ctx) (domain.AdaptivePlanRequest, bool, error) {
	mediaType, params, err := mime.ParseMediaType(c.Get("Content-Type"))
	charset, hasCharset := params["charset"]
	if err != nil || mediaType != "application/json" || (hasCharset && !strings.EqualFold(charset, "utf-8")) {
		return domain.AdaptivePlanRequest{}, false, c.Status(fiber.StatusUnsupportedMediaType).JSON(domain.ErrorResponse{Error: "unsupported_media_type", Details: "Content-Type must be application/json"})
	}
	var req domain.AdaptivePlanRequest
	if err := decodeStrictJSON(c.Body(), &req); err != nil {
		return domain.AdaptivePlanRequest{}, false, c.Status(fiber.StatusBadRequest).JSON(domain.ErrorResponse{Error: "invalid_request_body", Details: err.Error()})
	}
	return req, true, nil
}
