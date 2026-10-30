package service

import (
	"cmp"
	"slices"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

func mergeAndDeduplicate(plans [][]domain.PlanItem) []domain.PlanItem {
	seen := make(map[string]bool)
	merged := make([]domain.PlanItem, 0)
	for _, plan := range plans {
		for _, item := range plan {
			key := graph.Canonical(item.Topic)
			if !seen[key] {
				seen[key] = true
				merged = append(merged, item)
			}
		}
	}
	return merged
}

func validateAgainstGraph(items []domain.PlanItem, g *graph.InMemoryGraph, requested []string) ([]domain.PlanItem, int) {
	allowed := make(map[string]bool)
	for _, name := range requested {
		topic, ok := g.Resolve(name)
		if !ok {
			continue
		}
		allowed[topic] = true
		for _, prerequisite := range g.GetPrerequisiteChain(topic) {
			allowed[prerequisite] = true
		}
	}
	valid := make([]domain.PlanItem, 0, len(items))
	dropped := 0
	for _, item := range items {
		if topic, ok := g.Resolve(item.Topic); ok && allowed[topic] {
			item.Topic = topic
			item.PrerequisiteChain = g.GetPrerequisiteChain(item.Topic)
			valid = append(valid, item)
		} else {
			dropped++
		}
	}
	ranks := make(map[string]int)
	for _, item := range valid {
		for _, topic := range append(append([]string{}, item.PrerequisiteChain...), item.Topic) {
			if _, exists := ranks[topic]; !exists {
				ranks[topic] = len(ranks)
			}
		}
	}
	slices.SortStableFunc(valid, func(a, b domain.PlanItem) int { return cmp.Compare(ranks[a.Topic], ranks[b.Topic]) })
	for i := range valid {
		valid[i].Priority = i + 1
	}
	return valid, dropped
}

func missingTopic(items []domain.PlanItem, requested []string) string {
	present := make(map[string]bool, len(items))
	for _, item := range items {
		present[graph.Canonical(item.Topic)] = true
	}
	for _, topic := range requested {
		if !present[graph.Canonical(topic)] {
			return topic
		}
	}
	return ""
}
