// Package service orchestrates graph-guided study plan generation.
package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sony/gobreaker/v2"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

const circuitOpenTimeout = 15 * time.Second

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
	MaxConcurrency  int
	QueueTimeout    time.Duration
	Logger          *slog.Logger
}

// ReasoningService coordinates validated, concurrent topic generation.
type ReasoningService struct {
	logger          *slog.Logger
	graph           *graph.InMemoryGraph
	planner         Planner
	llmTimeout      time.Duration
	maxFailedTopics int
	queueTimeout    time.Duration
	slots           *semaphore.Weighted
	breaker         *gobreaker.CircuitBreaker[[]domain.PlanItem]
}

// NewReasoningService constructs a pipeline with a shared backend circuit breaker.
func NewReasoningService(g *graph.InMemoryGraph, planner Planner, opts Options) *ReasoningService {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = 16
	}
	if opts.QueueTimeout <= 0 {
		opts.QueueTimeout = 2 * time.Second
	}
	cb := gobreaker.NewCircuitBreaker[[]domain.PlanItem](breakerSettings(logger))
	return &ReasoningService{logger: logger, graph: g, planner: planner, llmTimeout: opts.LLMTimeout, maxFailedTopics: opts.MaxFailedTopics, breaker: cb, queueTimeout: opts.QueueTimeout, slots: semaphore.NewWeighted(int64(opts.MaxConcurrency))}
}

func breakerSettings(logger *slog.Logger) gobreaker.Settings {
	return gobreaker.Settings{
		Name: "bedrock-nova-micro", MaxRequests: 2, Interval: 30 * time.Second, Timeout: circuitOpenTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= 3 },
		IsExcluded: func(err error) bool {
			return errors.Is(err, context.Canceled) || errors.Is(err, ErrInvalidOutput) || errors.Is(err, ErrUpstreamRejected)
		},
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
			queueCtx, queueCancel := context.WithTimeout(egCtx, s.queueTimeout)
			err := s.slots.Acquire(queueCtx, 1)
			queueCancel()
			if err != nil {
				if egCtx.Err() != nil {
					return &TopicError{Topic: topic, cause: egCtx.Err()}
				}
				return &TopicError{Topic: topic, cause: &UnavailableError{RetryAfter: time.Second}}
			}
			started := false
			defer func() {
				if !started {
					s.slots.Release(1)
				}
			}()
			llmCtx, cancel := context.WithTimeout(egCtx, s.llmTimeout)
			defer cancel()
			items, err := s.breaker.Execute(func() ([]domain.PlanItem, error) {
				started = true
				return s.generateTopic(llmCtx, TopicRequest{GraphContext: subgraph, Topic: topic, PrerequisiteOrder: s.graph.GetPrerequisiteChain(topic), VARKProfile: req.VARKProfile, StudentID: req.StudentID, CourseID: req.CourseID})
			})
			if err != nil {
				if errors.Is(err, gobreaker.ErrTooManyRequests) {
					return &TopicError{Topic: topic, cause: &UnavailableError{RetryAfter: time.Second, Err: ErrCircuitOpen}}
				}
				if errors.Is(err, gobreaker.ErrOpenState) {
					return &TopicError{Topic: topic, cause: &UnavailableError{RetryAfter: circuitOpenTimeout, Err: ErrCircuitOpen}}
				}
				if errors.Is(err, ErrThrottled) {
					return &TopicError{Topic: topic, cause: &UnavailableError{RetryAfter: 5 * time.Second, Err: err}}
				}
				return &TopicError{Topic: topic, cause: errors.Join(ErrUpstream, err)}
			}
			// Each goroutine owns one result index; completion order cannot change priorities.
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

func (s *ReasoningService) generateTopic(ctx context.Context, req TopicRequest) ([]domain.PlanItem, error) {
	type result struct {
		items []domain.PlanItem
		err   error
	}
	results := make(chan result, 1)
	go func() {
		defer s.slots.Release(1)
		items, err := s.planner.GenerateTopic(ctx, req)
		results <- result{items: items, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case got := <-results:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return got.items, got.err
	}
}

func countTotalItems(plans [][]domain.PlanItem) int {
	n := 0
	for _, p := range plans {
		n += len(p)
	}
	return n
}
