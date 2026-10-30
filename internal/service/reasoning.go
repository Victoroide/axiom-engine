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
	"github.com/Victoroide/axiom-engine/internal/metrics"
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
	Metrics         *metrics.Metrics
}

// ReasoningService coordinates validated, concurrent topic generation.
type ReasoningService struct {
	metrics         *metrics.Metrics
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
	opts.Metrics.SetBreakerStateSource(func() int { return int(cb.State()) })
	return &ReasoningService{metrics: opts.Metrics, logger: logger, graph: g, planner: planner, llmTimeout: opts.LLMTimeout, maxFailedTopics: opts.MaxFailedTopics, breaker: cb, queueTimeout: opts.QueueTimeout, slots: semaphore.NewWeighted(int64(opts.MaxConcurrency))}
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
			queueStart := time.Now()
			queueCtx, queueCancel := context.WithTimeout(egCtx, s.queueTimeout)
			err := s.slots.Acquire(queueCtx, 1)
			queueCancel()
			s.metrics.ObserveQueueWait(time.Since(queueStart), errors.Is(err, context.DeadlineExceeded) && egCtx.Err() == nil)
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
			items, err := s.breaker.Execute(func() (items []domain.PlanItem, callErr error) {
				callStart := time.Now()
				defer func() { s.metrics.ObserveLLM(llmOutcome(callErr), time.Since(callStart)) }()
				started = true
				items, callErr = s.generateTopic(llmCtx, TopicRequest{GraphContext: subgraph, Topic: topic, PrerequisiteOrder: s.graph.GetPrerequisiteChain(topic), VARKProfile: req.VARKProfile})
				if callErr != nil {
					return nil, callErr
				}
				if missing := missingTopic(items, []string{topic}); missing != "" {
					s.logger.WarnContext(llmCtx, "stage.validation", "request_id", requestIDFromContext(llmCtx), "topic", missing)
					return nil, ErrInvalidOutput
				}
				return items, nil
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
	duplicates := countTotalItems(plans) - len(merged)
	merged, resourcesDropped := sanitizeOutput(merged, req.VARKProfile)
	s.metrics.ObserveDroppedResources(resourcesDropped)
	if resourcesDropped > 0 {
		s.logger.WarnContext(ctx, "stage.output", "request_id", requestIDFromContext(ctx), "resources_dropped", resourcesDropped)
	}
	validated, dropped := validateAgainstGraph(merged, s.graph, req.FailedTopics)
	s.metrics.ObserveDroppedItems(duplicates, dropped)
	if dropped > 0 {
		s.logger.WarnContext(ctx, "stage.validation", "request_id", requestIDFromContext(ctx), "items_dropped", dropped)
	}
	// Guard the coverage invariant if deduplication or graph filtering changes.
	if missing := missingTopic(validated, req.FailedTopics); missing != "" {
		s.logger.WarnContext(ctx, "stage.validation", "request_id", requestIDFromContext(ctx), "topic", missing)
		return domain.PlanResponse{}, &TopicError{Topic: missing, cause: ErrInvalidOutput}
	}
	return domain.PlanResponse{StudentID: req.StudentID, CourseID: req.CourseID, Items: validated, Meta: domain.PipelineMeta{SubgraphTuples: len(subgraph), TopicsProcessed: len(req.FailedTopics), ItemsGenerated: countTotalItems(plans), ItemsValidated: len(validated), ItemsDropped: dropped, LLMLatencyMs: float64(llmLatency.Milliseconds()), TotalLatencyMs: float64(time.Since(start).Milliseconds())}}, nil
}

func (s *ReasoningService) generateTopic(ctx context.Context, req TopicRequest) ([]domain.PlanItem, error) {
	type result struct {
		items []domain.PlanItem
		err   error
	}
	results := make(chan result, 1)
	s.metrics.StartCall()
	go func() {
		got := func() result {
			defer s.slots.Release(1)
			defer s.metrics.FinishCall()
			items, err := s.planner.GenerateTopic(ctx, req)
			return result{items: items, err: err}
		}()
		results <- got
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

func llmOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrThrottled):
		return "throttled"
	case errors.Is(err, ErrUpstreamUnavailable):
		return "unavailable"
	case errors.Is(err, ErrUpstreamRejected):
		return "rejected"
	case errors.Is(err, ErrInvalidOutput):
		return "invalid_output"
	default:
		return "error"
	}
}
