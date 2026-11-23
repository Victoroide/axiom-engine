package graph

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

// Topics returns concepts sorted by name, optionally filtered by canonical substring.
func (g *InMemoryGraph) Topics(filter string) []domain.Topic {
	topics, _ := g.TopicsContext(context.Background(), filter)
	return topics
}

// TopicsContext returns matching concepts and stops the scan on cancellation.
func (g *InMemoryGraph) TopicsContext(ctx context.Context, filter string) ([]domain.Topic, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	topics := make([]domain.Topic, 0, len(g.nodes))
	filter = Canonical(filter)
	for name := range g.nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.Contains(Canonical(name), filter) {
			continue
		}
		prerequisites, err := relatedTopics(ctx, g.forward[name], true)
		if err != nil {
			return nil, err
		}
		dependents, err := relatedTopics(ctx, g.reverse[name], false)
		if err != nil {
			return nil, err
		}
		topics = append(topics, domain.Topic{Name: name, Prerequisites: prerequisites, Dependents: dependents})
	}
	slices.SortFunc(topics, func(a, b domain.Topic) int { return cmp.Compare(a.Name, b.Name) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return topics, nil
}

// Prerequisites returns sorted, distinct direct prerequisites for a stored name.
func (g *InMemoryGraph) Prerequisites(topic string) []string {
	names, _ := relatedTopics(context.Background(), g.forward[topic], true)
	return names
}

// Dependents returns sorted, distinct direct dependents for a stored name.
func (g *InMemoryGraph) Dependents(topic string) []string {
	names, _ := relatedTopics(context.Background(), g.reverse[topic], false)
	return names
}

func relatedTopics(ctx context.Context, edges []domain.GraphTuple, outgoing bool) ([]string, error) {
	names := make([]string, 0, len(edges))
	for _, edge := range edges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := edge.Subject
		if outgoing {
			name = edge.Object
		}
		names = append(names, name)
	}
	slices.Sort(names)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return slices.Compact(names), nil
}

// Path resolves a canonical name and returns its full prerequisite study order.
func (g *InMemoryGraph) Path(topic string) (domain.TopicPathResponse, bool) {
	path, ok, _ := g.PathContext(context.Background(), topic)
	return path, ok
}

// PathContext resolves a topic and follows its prerequisites until canceled.
func (g *InMemoryGraph) PathContext(ctx context.Context, topic string) (domain.TopicPathResponse, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.TopicPathResponse{}, false, err
	}
	name, ok := g.Resolve(topic)
	if !ok {
		return domain.TopicPathResponse{}, false, nil
	}
	chain, err := g.prerequisiteChain(name, ctx.Err)
	if err != nil {
		return domain.TopicPathResponse{}, false, err
	}
	if chain == nil {
		chain = []string{}
	}
	dependents, err := relatedTopics(ctx, g.reverse[name], false)
	if err != nil {
		return domain.TopicPathResponse{}, false, err
	}
	return domain.TopicPathResponse{Topic: name, PrerequisiteChain: chain, Dependents: dependents}, true, nil
}
