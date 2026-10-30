package graph

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

//go:embed seed.json
var seed []byte

// Load reads a graph file, or uses the embedded curriculum when path is empty.
func Load(path string) (*InMemoryGraph, error) {
	if path == "" {
		return NewInMemoryGraph(), nil
	}
	f, err := os.Open(filepath.Clean(path)) //nolint:gosec // GRAPH_FILE is an operator-selected startup file.
	if err != nil {
		return nil, fmt.Errorf("open graph: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Decode(f)
}

// Decode validates a JSON tuple array and constructs an immutable graph.
func Decode(r io.Reader) (*InMemoryGraph, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var tuples []domain.GraphTuple
	if err := dec.Decode(&tuples); err != nil {
		return nil, fmt.Errorf("decode graph: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("graph must contain exactly one JSON array")
	}
	if len(tuples) == 0 {
		return nil, fmt.Errorf("graph must contain at least one tuple")
	}
	g := &InMemoryGraph{forward: make(map[string][]domain.GraphTuple), reverse: make(map[string][]domain.GraphTuple), nodes: make(map[string]bool), all: make([]domain.GraphTuple, 0, len(tuples))}
	seen := make(map[domain.GraphTuple]bool, len(tuples))
	for i, t := range tuples {
		t.Subject = strings.TrimSpace(t.Subject)
		t.Relation = strings.TrimSpace(t.Relation)
		t.Object = strings.TrimSpace(t.Object)
		if t.Subject == "" || t.Object == "" || t.Relation == "" {
			return nil, fmt.Errorf("tuple %d has an empty field", i)
		}
		if t.Relation != "depends_on" && t.Relation != "is_a" {
			return nil, fmt.Errorf("tuple %d has unsupported relation %q", i, t.Relation)
		}
		if t.Subject == t.Object {
			return nil, fmt.Errorf("tuple %d is a self loop", i)
		}
		if !seen[t] {
			g.add(t)
			seen[t] = true
		}
	}
	if cycle := g.prerequisiteCycle(); len(cycle) > 0 {
		return nil, fmt.Errorf("graph contains a prerequisite cycle: %s", cycleSummary(cycle))
	}
	return g, nil
}

func (g *InMemoryGraph) prerequisiteCycle() []string {
	colors := make(map[string]uint8, len(g.nodes))
	var path []string
	var visit func(string) []string
	visit = func(node string) []string {
		colors[node] = 1
		path = append(path, node)
		for _, edge := range g.forward[node] {
			if colors[edge.Object] == 1 {
				start := slices.Index(path, edge.Object)
				cycle := slices.Clone(path[start:])
				first := slices.Index(cycle, slices.Min(cycle))
				cycle = append(cycle[first:], cycle[:first]...)
				return append(cycle, cycle[0])
			}
			if colors[edge.Object] == 0 {
				if cycle := visit(edge.Object); len(cycle) > 0 {
					return cycle
				}
			}
		}
		path = path[:len(path)-1]
		colors[node] = 2
		return nil
	}
	for _, node := range slices.Sorted(maps.Keys(g.nodes)) {
		if colors[node] == 0 {
			if cycle := visit(node); len(cycle) > 0 {
				return cycle
			}
		}
	}
	return nil
}

func cycleSummary(cycle []string) string {
	const maxNodes = 16
	shown := cycle
	if len(cycle)-1 > maxNodes {
		shown = cycle[:maxNodes]
	}
	names := make([]string, len(shown))
	for i, node := range shown {
		names[i] = fmt.Sprintf("%.64s", node)
		if len(names[i]) < len(node) {
			names[i] += "…"
		}
	}
	summary := strings.Join(names, " -> ")
	if len(cycle)-1 > maxNodes {
		summary += fmt.Sprintf(" -> … (%d nodes)", len(cycle)-1)
	}
	return summary
}
