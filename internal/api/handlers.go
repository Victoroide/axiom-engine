// Package api implements the HTTP transport layer for the AxiomEngine
// microservice using the Fiber v2 framework. It defines request validation,
// error mapping, structured logging, and route registration for all public
// API endpoints.
package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Victoroide/axiom-reasoning-svc/internal/domain"
	"github.com/Victoroide/axiom-reasoning-svc/internal/graph"
	"github.com/Victoroide/axiom-reasoning-svc/internal/service"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// requestTimeout is the maximum duration allowed for a single adaptive-plan
// request, including all pipeline stages and parallel LLM calls. If exceeded,
// the handler returns HTTP 504 Gateway Timeout.
const requestTimeout = 20 * time.Second

// Handler aggregates the dependencies required by all HTTP endpoint handlers.
// It holds a reference to the ReasoningService (for adaptive-plan generation)
// and the InMemoryGraph (for cognitive shadow graph queries).
type Handler struct {
	reasoning *service.ReasoningService
	graph     *graph.InMemoryGraph
}

// NewHandler constructs a Handler with the given ReasoningService and
// InMemoryGraph. Both dependencies are required and must not be nil.
func NewHandler(r *service.ReasoningService, g *graph.InMemoryGraph) *Handler {
	return &Handler{reasoning: r, graph: g}
}

// RegisterRoutes attaches all versioned API routes to the given Fiber
// application under the /api/v1 prefix. Routes registered:
//   - POST /api/v1/adaptive-plan          (handleAdaptivePlan)
//   - GET  /api/v1/tutor/student/:student_id/cognitive-graph (handleCognitiveGraph)
func (h *Handler) RegisterRoutes(app *fiber.App) {
	v1 := app.Group("/api/v1")
	v1.Post("/adaptive-plan", h.handleAdaptivePlan)
	v1.Get("/tutor/student/:student_id/cognitive-graph", h.handleCognitiveGraph)
}

// handleAdaptivePlan godoc
// @Summary      Generate adaptive study plan
// @Description  Accepts a student VARK profile and a list of failed concept IDs.
// @Description  Executes GraphRAG subgraph extraction, topological sort, and
// @Description  parallel BAML fan-out to Amazon Nova Micro. Returns a structured
// @Description  study plan ordered by prerequisite depth.
// @Tags         Adaptive Plan
// @Accept       json
// @Produce      json
// @Param        body  body      domain.AdaptivePlanRequest  true  "Student profile and failed concepts"
// @Success      200   {object}  domain.PlanResponse
// @Failure      400   {object}  domain.ErrorResponse
// @Failure      429   {object}  domain.ErrorResponse
// @Failure      500   {object}  domain.ErrorResponse
// @Failure      503   {object}  domain.ErrorResponse
// @Failure      504   {object}  domain.ErrorResponse
// @Router       /api/v1/adaptive-plan [post]
func (h *Handler) handleAdaptivePlan(c *fiber.Ctx) error {
	requestID := uuid.New().String()
	logger := slog.With("request_id", requestID)

	var req domain.AdaptivePlanRequest
	if err := c.BodyParser(&req); err != nil {
		logger.Warn("request.parse_failed", "error", err.Error())
		return c.Status(fiber.StatusBadRequest).JSON(domain.ErrorResponse{
			Error:   "invalid_request_body",
			Details: err.Error(),
		})
	}

	if len(req.FailedTopics) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(domain.ErrorResponse{
			Error:   "validation_error",
			Details: "failed_topics must contain at least one topic",
		})
	}

	if req.VARKProfile == "" {
		return c.Status(fiber.StatusBadRequest).JSON(domain.ErrorResponse{
			Error:   "validation_error",
			Details: "vark_profile is required (visual, auditory, read_write, kinesthetic)",
		})
	}

	logger.Info("request.received",
		"student_id", req.StudentID,
		"failed_topics", req.FailedTopics,
		"vark_profile", req.VARKProfile,
	)

	ctx, cancel := context.WithTimeout(c.UserContext(), requestTimeout)
	defer cancel()

	start := time.Now()
	plan, err := h.reasoning.GeneratePlan(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			logger.Error("request.timeout", "elapsed_ms", elapsed.Milliseconds())
			return c.Status(fiber.StatusGatewayTimeout).JSON(domain.ErrorResponse{
				Error:   "gateway_timeout",
				Details: "LLM reasoning exceeded deadline",
			})
		}
		if errors.Is(err, service.ErrCircuitOpen) {
			logger.Error("request.circuit_open", "elapsed_ms", elapsed.Milliseconds())
			return c.Status(fiber.StatusServiceUnavailable).JSON(domain.ErrorResponse{
				Error:   "service_unavailable",
				Details: "LLM backend circuit breaker is open; try again later",
			})
		}
		logger.Error("request.failed", "error", err.Error(), "elapsed_ms", elapsed.Milliseconds())
		return c.Status(fiber.StatusInternalServerError).JSON(domain.ErrorResponse{
			Error:   "reasoning_failed",
			Details: err.Error(),
		})
	}

	logger.Info("request.success",
		"items", len(plan.Items),
		"elapsed_ms", elapsed.Milliseconds(),
	)

	return c.Status(fiber.StatusOK).JSON(plan)
}

// handleCognitiveGraph godoc
// @Summary      Get cognitive shadow graph
// @Description  Returns a filtered directed subgraph of curriculum concepts annotated
// @Description  with the student's cognitive state (failed, learning, mastered).
// @Description  When target_topic is provided, only the depth-1 neighborhood of that
// @Description  topic is returned. Otherwise, the depth-1 neighborhoods of all failed
// @Description  topics are merged. Only nodes with at least one edge are included.
// @Description  Output is directly consumable by Cytoscape.js and D3.js renderers.
// @Tags         Tutor
// @Produce      json
// @Param        student_id    path   string  true   "Student identifier"                          example(student-42)
// @Param        topics        query  string  true   "Comma-separated failed topic names"          example(Polymorphism,Recursion)
// @Param        target_topic  query  string  false  "Single topic to center the subgraph on"      example(Recursion)
// @Success      200  {object}  domain.CognitiveGraphResponse
// @Failure      400  {object}  domain.ErrorResponse
// @Router       /api/v1/tutor/student/{student_id}/cognitive-graph [get]
func (h *Handler) handleCognitiveGraph(c *fiber.Ctx) error {
	studentID := c.Params("student_id")
	if studentID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(domain.ErrorResponse{
			Error:   "validation_error",
			Details: "student_id path parameter is required",
		})
	}

	topicsParam := c.Query("topics")
	if topicsParam == "" {
		return c.Status(fiber.StatusBadRequest).JSON(domain.ErrorResponse{
			Error:   "validation_error",
			Details: "topics query parameter is required (comma-separated, e.g. ?topics=Polymorphism,Recursion)",
		})
	}

	topics := strings.Split(topicsParam, ",")
	for i := range topics {
		topics[i] = strings.TrimSpace(topics[i])
	}

	targetTopic := strings.TrimSpace(c.Query("target_topic"))

	slog.Info("cognitive_graph.requested",
		"student_id", studentID,
		"topics", topics,
		"target_topic", targetTopic,
	)

	resp := h.graph.GenerateCognitiveShadow(topics, targetTopic)

	slog.Info("cognitive_graph.generated",
		"student_id", studentID,
		"nodes", len(resp.Nodes),
		"edges", len(resp.Edges),
	)

	return c.Status(fiber.StatusOK).JSON(resp)
}
