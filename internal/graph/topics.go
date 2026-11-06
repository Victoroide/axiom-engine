package graph

import (
	"maps"
	"slices"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

// Topics returns concepts sorted by name, optionally filtered by canonical substring.
func (g *InMemoryGraph) Topics(filter string) []domain.Topic {
	topics := make([]domain.Topic, 0, len(g.nodes))
	filter = Canonical(filter)
	for _, name := range slices.Sorted(maps.Keys(g.nodes)) {
		if strings.Contains(Canonical(name), filter) {
			topics = append(topics, domain.Topic{Name: name, Prerequisites: g.Prerequisites(name), Dependents: g.Dependents(name)})
		}
	}
	return topics
}

// Prerequisites returns sorted, distinct direct prerequisites for a stored name.
func (g *InMemoryGraph) Prerequisites(topic string) []string {
	names := make([]string, 0, len(g.forward[topic]))
	for _, edge := range g.forward[topic] {
		names = append(names, edge.Object)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// Dependents returns sorted, distinct direct dependents for a stored name.
func (g *InMemoryGraph) Dependents(topic string) []string {
	names := make([]string, 0, len(g.reverse[topic]))
	for _, edge := range g.reverse[topic] {
		names = append(names, edge.Subject)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// Path resolves a canonical name and returns its full prerequisite study order.
func (g *InMemoryGraph) Path(topic string) (domain.TopicPathResponse, bool) {
	name, ok := g.Resolve(topic)
	if !ok {
		return domain.TopicPathResponse{}, false
	}
	chain := g.GetPrerequisiteChain(name)
	if chain == nil {
		chain = []string{}
	}
	return domain.TopicPathResponse{Topic: name, PrerequisiteChain: chain, Dependents: g.Dependents(name)}, true
}
