package service

import (
	"context"
	"slices"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

// CognitiveGraph validates topics and an optional target before querying the graph.
func CognitiveGraph(requested []string, targets, mastered []string, maxTopics, maxMastered int, g *graph.InMemoryGraph) (domain.CognitiveGraphResponse, error) {
	return CognitiveGraphContext(context.Background(), requested, targets, mastered, maxTopics, maxMastered, g)
}

// CognitiveGraphContext validates input and queries the graph until canceled.
func CognitiveGraphContext(ctx context.Context, requested []string, targets, mastered []string, maxTopics, maxMastered int, g *graph.InMemoryGraph) (domain.CognitiveGraphResponse, error) {
	if err := ctx.Err(); err != nil {
		return domain.CognitiveGraphResponse{}, err
	}
	topics, err := ValidateTopics("topics", requested, maxTopics, g)
	if err != nil {
		return domain.CognitiveGraphResponse{}, err
	}
	if len(targets) > 1 {
		return domain.CognitiveGraphResponse{}, &ValidationError{Details: "target_topic must not be repeated"}
	}
	target := ""
	if len(targets) > 0 {
		target = strings.TrimSpace(targets[0])
	}
	if target != "" {
		stored, ok := g.Resolve(target)
		if !ok {
			return domain.CognitiveGraphResponse{}, &ValidationError{Details: "unknown target_topic: " + QuoteValue(target), UnknownTopics: []string{target}}
		}
		if !slices.Contains(topics, stored) {
			return domain.CognitiveGraphResponse{}, &ValidationError{Details: "target_topic must be one of topics"}
		}
		target = stored
	}
	if mastered == nil {
		return g.GenerateCognitiveShadowContext(ctx, topics, target)
	}
	history := make([]string, 0, len(mastered))
	for _, topic := range mastered {
		if err := ctx.Err(); err != nil {
			return domain.CognitiveGraphResponse{}, err
		}
		if strings.TrimSpace(topic) != "" {
			history = append(history, topic)
		}
	}
	history, err = ValidateMasteredTopics("mastered", history, topics, maxMastered, g)
	if err != nil {
		return domain.CognitiveGraphResponse{}, err
	}
	return g.GenerateCognitiveShadowContext(ctx, topics, target, history)
}

// TopicPath resolves a topic with the same validation error as other requests.
func TopicPath(topic string, g *graph.InMemoryGraph) (domain.TopicPathResponse, error) {
	return TopicPathContext(context.Background(), topic, g)
}

// TopicPathContext resolves and follows a topic path until canceled.
func TopicPathContext(ctx context.Context, topic string, g *graph.InMemoryGraph) (domain.TopicPathResponse, error) {
	path, ok, err := g.PathContext(ctx, topic)
	if err != nil {
		return domain.TopicPathResponse{}, err
	}
	if !ok {
		return domain.TopicPathResponse{}, &ValidationError{Details: "unknown topic: " + QuoteValue(topic), UnknownTopics: []string{topic}}
	}
	return path, nil
}
