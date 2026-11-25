package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

type preparedTopic struct {
	request TopicRequest
	index   int
}

// PreparedPlan holds the validated inputs for a plan that can be dispatched.
type PreparedPlan struct {
	service  *ReasoningService
	snapshot *graph.Snapshot
	start    time.Time
	request  domain.AdaptivePlanRequest
	topics   []preparedTopic
	edges    int
	skipped  int
}

// PreparePlan validates the request and budgets all topic calls before dispatch.
func (s *ReasoningService) PreparePlan(ctx context.Context, req domain.AdaptivePlanRequest) (*PreparedPlan, error) {
	start := time.Now()
	snapshot := s.curriculum.Current()
	requested := req.FailedTopics
	_, validationSpan := telemetry.Start(ctx, "plan.validate")
	req, err := validateRequest(req, s.maxFailedTopics, s.maxMasteredTopics, snapshot.Graph())
	if err != nil {
		telemetry.Fail(validationSpan, "validation_error")
	}
	validationSpan.End()
	if err != nil {
		return nil, err
	}
	plan := &PreparedPlan{service: s, snapshot: snapshot, start: start, request: req}
	if err := plan.prepareCurriculum(ctx, requested); err != nil {
		var budget *PromptBudgetError
		if errors.As(err, &budget) {
			s.metrics.RejectPromptBudget()
		}
		return nil, err
	}
	return plan, nil
}

func (p *PreparedPlan) prepareCurriculum(ctx context.Context, requested []string) error {
	req := p.request
	budget := p.service.promptBudget
	g := p.snapshot.Graph()
	_, subgraphSpan := telemetry.Start(ctx, "plan.subgraph")
	subgraph, err := g.LocalSubgraphContext(ctx, req.FailedTopics, 2)
	subgraphSpan.End()
	if err != nil {
		return err
	}
	p.edges = len(subgraph)
	if p.edges > budget.MaxEdges {
		return &PromptBudgetError{}
	}
	sharedBytes := config.PromptScaffoldingBytes + escapedPromptBytes(req.VARKProfile)
	for _, tuple := range subgraph {
		if err := ctx.Err(); err != nil {
			return err
		}
		// GenerateTopicPlan renders one line per tuple as subject -[relation]-> object.
		sharedBytes += escapedPromptBytes(tuple.Subject) + escapedPromptBytes(tuple.Relation) + escapedPromptBytes(tuple.Object) + len(` -[]-> \n`)
		if sharedBytes > budget.MaxTopicBytes {
			return &PromptBudgetError{}
		}
	}
	mastered := make(map[string]bool, len(req.MasteredTopics))
	for _, topic := range req.MasteredTopics {
		mastered[topic] = true
	}
	indices := make(map[string]int, len(req.FailedTopics))
	for index, topic := range requested {
		key := graph.Canonical(topic)
		if _, found := indices[key]; !found {
			indices[key] = index
		}
	}
	remainingBytes := budget.MaxRequestBytes
	skipped := make(map[string]bool)
	p.topics = make([]preparedTopic, 0, len(req.FailedTopics))
	for _, topic := range req.FailedTopics {
		topicBytes := sharedBytes + escapedPromptBytes(topic)
		maxBytes := min(budget.MaxTopicBytes, remainingBytes)
		if topicBytes > maxBytes {
			return &PromptBudgetError{}
		}
		chain, err := g.PrerequisiteChainContext(ctx, topic)
		if err != nil {
			return err
		}
		order := make([]string, 0)
		for _, prerequisite := range chain {
			if err := ctx.Err(); err != nil {
				return err
			}
			if mastered[prerequisite] {
				skipped[prerequisite] = true
				continue
			}
			order = append(order, prerequisite)
			// The decimal ordinal, punctuation and escaped line break also render.
			topicBytes += len(strconv.Itoa(len(order))) + len(`. \n`) + escapedPromptBytes(prerequisite)
			if len(order) > budget.MaxPrerequisites || topicBytes > maxBytes {
				return &PromptBudgetError{}
			}
		}
		remainingBytes -= topicBytes
		p.topics = append(p.topics, preparedTopic{
			request: TopicRequest{GraphContext: subgraph, Topic: topic, PrerequisiteOrder: order, VARKProfile: req.VARKProfile},
			index:   indices[graph.Canonical(topic)],
		})
	}
	p.skipped = len(skipped)
	return ctx.Err()
}

func escapedPromptBytes(text string) int {
	// Include conservative HTML escaping and exclude the enclosing JSON quotes.
	escaped, _ := json.Marshal(text)
	return len(escaped) - 2
}
