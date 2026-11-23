package graph

import (
	"maps"
	"slices"
)

// Summary describes a validated curriculum's size and prerequisite structure.
type Summary struct {
	Concepts     int            `json:"concepts"`
	Relations    map[string]int `json:"relations"`
	Roots        []string       `json:"roots"`
	LongestChain []string       `json:"longest_chain"`
}

// Summary returns counts, sorted roots and one longest prerequisite chain.
// Equal-length chains are selected deterministically by sorted concept names.
func (g *InMemoryGraph) Summary() Summary {
	summary := Summary{Concepts: len(g.nodes), Relations: make(map[string]int), Roots: []string{}, LongestChain: []string{}}
	for _, edge := range g.all {
		summary.Relations[edge.Relation]++
	}
	lengths := make(map[string]int, len(g.nodes))
	previous := make(map[string]string, len(g.nodes))
	walk := prerequisiteWalk{state: make(map[string]visitState, len(g.nodes))}
	finish := func(name string) {
		longest := 0
		for _, edge := range g.forward[name] {
			prerequisite := edge.Object
			if n := lengths[prerequisite]; n > longest || (n == longest && prerequisite < previous[name]) {
				longest = n
				previous[name] = prerequisite
			}
		}
		lengths[name] = longest + 1
	}
	end := ""
	for _, name := range slices.Sorted(maps.Keys(g.nodes)) {
		if len(g.forward[name]) == 0 {
			summary.Roots = append(summary.Roots, name)
		}
		walk.visit(g, name, finish)
		if lengths[name] > lengths[end] {
			end = name
		}
	}
	for end != "" {
		summary.LongestChain = append(summary.LongestChain, end)
		end = previous[end]
	}
	slices.Reverse(summary.LongestChain)
	return summary
}
