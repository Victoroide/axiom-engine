package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"pgregory.net/rapid"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func drawDAG(t *rapid.T) ([]string, []domain.GraphTuple, *InMemoryGraph) {
	n := rapid.IntRange(2, 12).Draw(t, "nodes")
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("Node%02d", i)
	}
	tuples := make([]domain.GraphTuple, 0, n*(n-1)/2)
	for source := 1; source < n; source++ {
		for target := range source {
			if rapid.Bool().Draw(t, fmt.Sprintf("edge%d_%d", source, target)) {
				relation := rapid.SampledFrom([]string{"depends_on", "is_a"}).Draw(t, "relation")
				tuples = append(tuples, domain.GraphTuple{Subject: names[source], Relation: relation, Object: names[target]})
			}
		}
	}
	if len(tuples) == 0 {
		tuples = append(tuples, domain.GraphTuple{Subject: names[1], Relation: "depends_on", Object: names[0]})
	}
	data, err := json.Marshal(tuples)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return names, tuples, g
}

func TestPrerequisiteOrderProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		names, tuples, g := drawDAG(t)
		index := make(map[string]int, len(names))
		reach := make([][]bool, len(names))
		for i, name := range names {
			index[name] = i
			reach[i] = make([]bool, len(names))
		}
		for _, edge := range tuples {
			reach[index[edge.Subject]][index[edge.Object]] = true
		}
		for k := range names {
			for i := range names {
				for j := range names {
					reach[i][j] = reach[i][j] || reach[i][k] && reach[k][j]
				}
			}
		}
		for i, name := range names {
			positions := make(map[string]int)
			for position, prerequisite := range g.GetPrerequisiteChain(name) {
				j, known := index[prerequisite]
				if _, duplicate := positions[prerequisite]; duplicate || !known || prerequisite == name || !reach[i][j] {
					t.Fatalf("invalid prerequisite %q for %q", prerequisite, name)
				}
				positions[prerequisite] = position
			}
			for j, expected := range reach[i] {
				_, present := positions[names[j]]
				if present != expected {
					t.Fatalf("chain for %q: membership of %q = %v, want %v", name, names[j], present, expected)
				}
			}
			for _, edge := range tuples {
				subject, hasSubject := positions[edge.Subject]
				object, hasObject := positions[edge.Object]
				if hasSubject && (!hasObject || object >= subject) {
					t.Fatalf("prerequisite must come first: %+v in %v", edge, positions)
				}
			}
		}
	})
}

func TestLocalSubgraphDistanceProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		names, tuples, g := drawDAG(t)
		index := make(map[string]int, len(names))
		distance := make([][]int, len(names))
		for i, name := range names {
			index[name] = i
			distance[i] = make([]int, len(names))
			for j := range names {
				if i != j {
					distance[i][j] = len(names) + 1
				}
			}
		}
		for _, edge := range tuples {
			i, j := index[edge.Subject], index[edge.Object]
			distance[i][j], distance[j][i] = 1, 1
		}
		for k := range names {
			for i := range names {
				for j := range names {
					distance[i][j] = min(distance[i][j], distance[i][k]+distance[k][j])
				}
			}
		}
		seeds := rapid.SliceOfN(rapid.SampledFrom(names), 0, len(names)).Draw(t, "seeds")
		depth := rapid.IntRange(-1, len(names)).Draw(t, "depth")
		nearest := func(name string) int {
			d := len(names) + 1
			for _, seed := range seeds {
				d = min(d, distance[index[seed]][index[name]])
			}
			return d
		}
		expected := make(map[domain.GraphTuple]bool)
		for _, edge := range tuples {
			// Edges incident to the boundary are included.
			if min(nearest(edge.Subject), nearest(edge.Object)) <= depth {
				expected[edge] = true
			}
		}
		seen := make(map[domain.GraphTuple]bool)
		for _, edge := range g.GetLocalSubgraph(seeds, depth) {
			if !expected[edge] || seen[edge] {
				t.Fatalf("unexpected or repeated edge at depth %d: %+v", depth, edge)
			}
			seen[edge] = true
		}
		if len(seen) != len(expected) {
			t.Fatalf("subgraph at depth %d has %d edges, want %d", depth, len(seen), len(expected))
		}
	})
}
