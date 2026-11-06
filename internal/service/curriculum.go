package service

import (
	"slices"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

// CognitiveGraph validates topics and an optional target before querying the graph.
func CognitiveGraph(requested []string, targets, mastered []string, maxTopics, maxMastered int, g *graph.InMemoryGraph) (domain.CognitiveGraphResponse, error) {
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
		return g.GenerateCognitiveShadow(topics, target), nil
	}
	history := make([]string, 0, len(mastered))
	for _, topic := range mastered {
		if strings.TrimSpace(topic) != "" {
			history = append(history, topic)
		}
	}
	history, err = ValidateMasteredTopics("mastered", history, topics, maxMastered, g)
	if err != nil {
		return domain.CognitiveGraphResponse{}, err
	}
	return g.GenerateCognitiveShadow(topics, target, history), nil
}

// TopicPath resolves a topic with the same validation error as other requests.
func TopicPath(topic string, g *graph.InMemoryGraph) (domain.TopicPathResponse, error) {
	path, ok := g.Path(topic)
	if !ok {
		return domain.TopicPathResponse{}, &ValidationError{Details: "unknown topic: " + QuoteValue(topic), UnknownTopics: []string{topic}}
	}
	return path, nil
}
