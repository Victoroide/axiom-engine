// Package api exposes the versioned reasoning HTTP endpoints.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"

	"github.com/Victoroide/axiom-reasoning-svc/internal/domain"
	"github.com/Victoroide/axiom-reasoning-svc/internal/graph"
	"github.com/Victoroide/axiom-reasoning-svc/internal/service"
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
// @Failure 422 {object} domain.ErrorResponse
// @Failure 429 {object} domain.ErrorResponse
// @Failure 502 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse
// @Failure 504 {object} domain.ErrorResponse
// @Router /api/v1/adaptive-plan [post]
func (h *Handler) handleAdaptivePlan(c fiber.Ctx) error {
	var req domain.AdaptivePlanRequest
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(domain.ErrorResponse{Error: "invalid_request_body", Details: "request body must be valid JSON"})
	}
	ctx, cancel := context.WithTimeout(service.WithRequestID(c.Context(), requestid.FromContext(c)), h.timeout)
	defer cancel()
	plan, err := h.reasoning.GeneratePlan(ctx, req)
	if err != nil {
		var validation *service.ValidationError
		if errors.As(err, &validation) {
			return validationResponse(c, validation)
		}
		status, code, message := fiber.StatusInternalServerError, "internal_error", "reasoning failed"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			status, code, message = fiber.StatusGatewayTimeout, "gateway_timeout", "reasoning exceeded deadline"
		case errors.Is(err, service.ErrCircuitOpen):
			status, code, message = fiber.StatusServiceUnavailable, "service_unavailable", "reasoning temporarily unavailable"
		case errors.Is(err, context.Canceled):
			status, code, message = fiber.StatusRequestTimeout, "request_timeout", "request canceled"
		case errors.Is(err, service.ErrUpstream):
			status, code, message = fiber.StatusBadGateway, "bad_gateway", "upstream reasoning failed"
		}
		attrs := []slog.Attr{slog.String("request_id", requestid.FromContext(c)), slog.String("error_class", code)}
		var topicErr *service.TopicError
		if errors.As(err, &topicErr) {
			attrs = append(attrs, slog.String("topic", fmt.Sprintf("%.128s", topicErr.Topic)))
		}
		h.logger.LogAttrs(ctx, slog.LevelError, "request.failed", attrs...)
		return c.Status(status).JSON(domain.ErrorResponse{Error: code, Details: message})
	}
	return c.JSON(plan)
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
	if target != "" && !h.graph.NodeExists(target) {
		return validationResponse(c, &service.ValidationError{Details: "unknown target_topic: " + target, UnknownTopics: []string{target}})
	}
	return c.JSON(h.graph.GenerateCognitiveShadow(topics, target))
}
