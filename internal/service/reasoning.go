// Package service orchestrates graph-guided study plan generation.
package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sony/gobreaker/v2"
	"golang.org/x/sync/errgroup"

	"github.com/Victoroide/axiom-reasoning-svc/internal/domain"
	"github.com/Victoroide/axiom-reasoning-svc/internal/graph"
)

// ErrCircuitOpen indicates that the upstream circuit rejects requests.
var ErrCircuitOpen = errors.New("LLM circuit unavailable")

// ErrUpstream identifies a failure of the external planner.
var ErrUpstream = errors.New("upstream planner failed")

// TopicRequest carries graph context for a single topic call.
type TopicRequest struct {
	GraphContext      []domain.GraphTuple
	Topic             string
	PrerequisiteOrder []string
	VARKProfile       string
	StudentID         string
	CourseID          string
}

// Planner generates study items for one topic.
type Planner interface {
	GenerateTopic(context.Context, TopicRequest) ([]domain.PlanItem, error)
}

// Options configures the bounded reasoning pipeline.
type Options struct {
	LLMTimeout      time.Duration
	MaxFailedTopics int
	Logger          *slog.Logger
}

// ReasoningService coordinates validated, concurrent topic generation.
type ReasoningService struct {
	logger          *slog.Logger
	graph           *graph.InMemoryGraph
	planner         Planner
	llmTimeout      time.Duration
	maxFailedTopics int
	breaker         *gobreaker.CircuitBreaker[[]domain.PlanItem]
}

// NewReasoningService constructs a pipeline with a shared backend circuit breaker.
func NewReasoningService(g *graph.InMemoryGraph, planner Planner, opts Options) *ReasoningService {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cb := gobreaker.NewCircuitBreaker[[]domain.PlanItem](breakerSettings(logger))
	return &ReasoningService{logger: logger, graph: g, planner: planner, llmTimeout: opts.LLMTimeout, maxFailedTopics: opts.MaxFailedTopics, breaker: cb}
}

func breakerSettings(logger *slog.Logger) gobreaker.Settings {
	return gobreaker.Settings{
		Name: "bedrock-nova-micro", MaxRequests: 2, Interval: 30 * time.Second, Timeout: 15 * time.Second,
		ReadyToTrip: func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= 3 },
		IsExcluded:  func(err error) bool { return errors.Is(err, context.Canceled) },
		OnStateChange: func(name string, from, to gobreaker.State) {
			logger.Warn("circuit_breaker.state_change", "name", name, "from", from.String(), "to", to.String())
		},
	}
}

// GeneratePlan returns a deterministic plan or a classified pipeline error.
func (s *ReasoningService) GeneratePlan(ctx context.Context, req domain.AdaptivePlanRequest) (domain.PlanResponse, error) {
	start := time.Now()
	req, err := validateRequest(req, s.maxFailedTopics, s.graph)
	if err != nil {
		return domain.PlanResponse{}, err
	}
	subgraph := s.graph.GetLocalSubgraph(req.FailedTopics, 2)
	llmStart := time.Now()
	eg, egCtx := errgroup.WithContext(ctx)
	plans := make([][]domain.PlanItem, len(req.FailedTopics))
	for i, topic := range req.FailedTopics {
		eg.Go(func() error {
			llmCtx, cancel := context.WithTimeout(egCtx, s.llmTimeout)
			defer cancel()
			items, err := s.breaker.Execute(func() ([]domain.PlanItem, error) {
				items, err := s.planner.GenerateTopic(llmCtx, TopicRequest{GraphContext: subgraph, Topic: topic, PrerequisiteOrder: s.graph.GetPrerequisiteChain(topic), VARKProfile: req.VARKProfile, StudentID: req.StudentID, CourseID: req.CourseID})
				if llmCtx.Err() != nil {
					return nil, llmCtx.Err()
				}
				return items, err
			})
			if err != nil {
				if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
					return &TopicError{Topic: topic, cause: ErrCircuitOpen}
				}
				return &TopicError{Topic: topic, cause: errors.Join(ErrUpstream, err)}
			}
			// Each goroutine owns one slot; completion order cannot change priorities.
			plans[i] = items
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return domain.PlanResponse{}, err
	}
	llmLatency := time.Since(llmStart)
	merged := mergeAndDeduplicate(plans)
	validated, dropped := validateAgainstGraph(merged, s.graph)
	if dropped > 0 {
		s.logger.WarnContext(ctx, "stage.validation", "request_id", requestIDFromContext(ctx), "items_dropped", dropped)
	}
	return domain.PlanResponse{StudentID: req.StudentID, CourseID: req.CourseID, Items: validated, Meta: domain.PipelineMeta{SubgraphTuples: len(subgraph), TopicsProcessed: len(req.FailedTopics), ItemsGenerated: countTotalItems(plans), ItemsValidated: len(validated), LLMLatencyMs: float64(llmLatency.Milliseconds()), TotalLatencyMs: float64(time.Since(start).Milliseconds())}}, nil
}

func countTotalItems(plans [][]domain.PlanItem) int {
	n := 0
	for _, p := range plans {
		n += len(p)
	}
	return n
}
