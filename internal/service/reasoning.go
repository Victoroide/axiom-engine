// Package service implements the core business logic of the AxiomEngine
// microservice. It orchestrates a six-stage reasoning pipeline that transforms
// a student's failed-topic list into a personalized adaptive study plan by
// combining knowledge-graph traversal with LLM-powered content generation.
//
// The pipeline stages are:
//  1. Subgraph extraction (BFS over the knowledge graph)
//  2. Topological prerequisite sorting (DFS per failed topic)
//  3. Parallel BAML fan-out (concurrent LLM calls via errgroup)
//  4. Merge and deduplication of per-topic results
//  5. Hallucination validation (drop items absent from the graph)
//  6. Response enrichment with pipeline telemetry metadata
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	baml "axiom-reasoning-svc/baml_client"
	"axiom-reasoning-svc/baml_client/types"
	"axiom-reasoning-svc/internal/domain"
	"axiom-reasoning-svc/internal/graph"

	"github.com/sony/gobreaker/v2"
	"golang.org/x/sync/errgroup"
)

// ErrCircuitOpen is a sentinel error returned when the sony/gobreaker circuit
// breaker is in the Open state, indicating that the LLM backend (Amazon Bedrock)
// has failed consecutively and further requests are being rejected to prevent
// cascade failure. The HTTP handler maps this to a 503 Service Unavailable.
var ErrCircuitOpen = errors.New("circuit breaker is open: LLM backend unavailable")

// ReasoningService is the central orchestrator of the AxiomEngine reasoning
// pipeline. It holds references to the knowledge graph, configuration for
// traversal depth and LLM timeout, and a circuit breaker that protects
// against sustained LLM backend failures.
//
// Fields:
//   - graph:         the in-memory knowledge graph used for subgraph extraction
//   - subgraphDepth: maximum BFS depth for GetLocalSubgraph (default: 2)
//   - llmTimeout:    per-topic context deadline for each BAML call (default: 15s)
//   - breaker:       sony/gobreaker instance wrapping all Bedrock calls
type ReasoningService struct {
	graph         *graph.InMemoryGraph
	subgraphDepth int
	llmTimeout    time.Duration
	breaker       *gobreaker.CircuitBreaker[types.AdaptiveStudyPlan]
}

// NewReasoningService constructs a ReasoningService with the given knowledge
// graph and initializes the circuit breaker with production-grade settings:
//   - Name:        "bedrock-nova-micro"
//   - MaxRequests: 2 (half-open probe limit)
//   - Interval:    30s (closed-state failure counting window)
//   - Timeout:     15s (open-state duration before half-open transition)
//   - ReadyToTrip: trips after 3 consecutive failures
func NewReasoningService(g *graph.InMemoryGraph) *ReasoningService {
	cb := gobreaker.NewCircuitBreaker[types.AdaptiveStudyPlan](gobreaker.Settings{
		Name:        "bedrock-nova-micro",
		MaxRequests: 2,
		Interval:    30 * time.Second,
		Timeout:     15 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= 3
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			slog.Warn("circuit_breaker.state_change",
				"name", name,
				"from", from.String(),
				"to", to.String(),
			)
		},
	})

	return &ReasoningService{
		graph:         g,
		subgraphDepth: 2,
		llmTimeout:    15 * time.Second,
		breaker:       cb,
	}
}

// GeneratePlan executes the complete six-stage reasoning pipeline for the
// given request. It returns a PlanResponse containing priority-sorted study
// items and pipeline telemetry, or an error if any stage fails.
//
// The method is safe for concurrent invocation. Each call spawns its own
// errgroup for parallel LLM fan-out, bounded by the parent context.
func (s *ReasoningService) GeneratePlan(ctx context.Context, req domain.AdaptivePlanRequest) (domain.PlanResponse, error) {
	pipelineStart := time.Now()
	logger := slog.With(
		"student_id", req.StudentID,
		"course_id", req.CourseID,
		"vark_profile", req.VARKProfile,
	)

	// ── Stage 1: Extract subgraph ────────────────────────────────────
	subgraph := s.graph.GetLocalSubgraph(req.FailedTopics, s.subgraphDepth)
	if len(subgraph) == 0 {
		return domain.PlanResponse{}, fmt.Errorf("no knowledge-graph context found for topics: %v", req.FailedTopics)
	}
	logger.Info("stage.subgraph",
		"failed_topics", req.FailedTopics,
		"tuples", len(subgraph),
	)

	// ── Stage 2: Topological prerequisite sort per topic ─────────────
	prereqs := make(map[string][]string, len(req.FailedTopics))
	for _, topic := range req.FailedTopics {
		prereqs[topic] = s.graph.GetPrerequisiteChain(topic)
		logger.Info("stage.topo_sort",
			"topic", topic,
			"prerequisite_chain", prereqs[topic],
		)
	}

	// ── Stage 3: Parallel BAML fan-out (one call per failed topic) ───
	bamlTuples := mapDomainTuplesToBAML(subgraph)

	llmStart := time.Now()
	eg, egCtx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	topicPlans := make([]types.AdaptiveStudyPlan, 0, len(req.FailedTopics))

	for _, topic := range req.FailedTopics {
		topic := topic
		chain := prereqs[topic]
		eg.Go(func() error {
			llmCtx, cancel := context.WithTimeout(egCtx, s.llmTimeout)
			defer cancel()

			plan, err := s.breaker.Execute(func() (types.AdaptiveStudyPlan, error) {
				return baml.GenerateTopicPlan(
					llmCtx,
					bamlTuples,
					topic,
					chain,
					req.VARKProfile,
					req.StudentID,
					req.CourseID,
				)
			})
			if err != nil {
				if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
					logger.Error("stage.circuit_breaker_open", "topic", topic)
					return fmt.Errorf("%w: %s", ErrCircuitOpen, topic)
				}
				logger.Error("stage.llm_call_failed",
					"topic", topic,
					"error", err.Error(),
				)
				return fmt.Errorf("BAML GenerateTopicPlan(%s): %w", topic, err)
			}

			mu.Lock()
			topicPlans = append(topicPlans, plan)
			mu.Unlock()

			logger.Info("stage.llm_call_ok",
				"topic", topic,
				"items_returned", len(plan.Items),
			)
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return domain.PlanResponse{}, err
	}
	llmLatency := time.Since(llmStart)

	// ── Stage 4: Merge & deduplicate ─────────────────────────────────
	merged := mergeAndDeduplicate(topicPlans)
	logger.Info("stage.merge",
		"items_before_dedup", countTotalItems(topicPlans),
		"items_after_dedup", len(merged),
	)

	// ── Stage 5: Validate against graph (drop hallucinated topics) ───
	validated, dropped := validateAgainstGraph(merged, s.graph)
	if dropped > 0 {
		logger.Warn("stage.validation",
			"items_dropped", dropped,
		)
	}

	// ── Stage 6: Build enriched response ─────────────────────────────
	totalLatency := time.Since(pipelineStart)

	resp := domain.PlanResponse{
		StudentID: req.StudentID,
		CourseID:  req.CourseID,
		Items:     validated,
		Meta: domain.PipelineMeta{
			SubgraphTuples:  len(subgraph),
			TopicsProcessed: len(req.FailedTopics),
			ItemsGenerated:  countTotalItems(topicPlans),
			ItemsValidated:  len(validated),
			LLMLatencyMs:    float64(llmLatency.Milliseconds()),
			TotalLatencyMs:  float64(totalLatency.Milliseconds()),
		},
	}

	logger.Info("pipeline.complete",
		"llm_latency_ms", resp.Meta.LLMLatencyMs,
		"total_latency_ms", resp.Meta.TotalLatencyMs,
		"items_final", len(validated),
	)

	return resp, nil
}

// countTotalItems sums the number of StudyItems across all AdaptiveStudyPlans.
// It is used internally to compute the ItemsGenerated metric before deduplication.
func countTotalItems(plans []types.AdaptiveStudyPlan) int {
	n := 0
	for _, p := range plans {
		n += len(p.Items)
	}
	return n
}
