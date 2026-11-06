package graph

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestTopicProperties(t *testing.T) {
	g := NewInMemoryGraph()
	topics := g.Topics("")
	if len(topics) != 23 {
		t.Fatalf("topics=%d", len(topics))
	}
	names := make([]string, len(topics))
	for i, topic := range topics {
		names[i] = topic.Name
	}
	if !slices.IsSorted(names) {
		t.Fatal(names)
	}
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.SampledFrom(names).Draw(t, "topic")
		path, ok := g.Path("  " + strings.ToUpper(name) + "  ")
		if !ok || path.Topic != name {
			t.Fatalf("unresolved %q", name)
		}
		prereqs, dependents := []string{}, []string{}
		for _, edge := range g.All() {
			if edge.Subject == name {
				prereqs = append(prereqs, edge.Object)
			}
			if edge.Object == name {
				dependents = append(dependents, edge.Subject)
			}
		}
		slices.Sort(prereqs)
		prereqs = slices.Compact(prereqs)
		slices.Sort(dependents)
		dependents = slices.Compact(dependents)
		if !slices.Equal(g.Prerequisites(name), prereqs) || !slices.Equal(g.Dependents(name), dependents) || !slices.Equal(path.Dependents, dependents) {
			t.Fatal("direct neighbors differ from tuples")
		}
		positions := map[string]int{}
		for i, node := range append(path.PrerequisiteChain, name) {
			if _, duplicate := positions[node]; duplicate {
				t.Fatal("duplicate path node")
			}
			positions[node] = i
		}
		for _, edge := range g.All() {
			if subject, exists := positions[edge.Subject]; exists {
				object, found := positions[edge.Object]
				if !found || object >= subject {
					t.Fatalf("invalid study order: %+v in %v", edge, positions)
				}
			}
		}
		filtered := g.Topics(strings.ToUpper(name))
		found := false
		for _, topic := range filtered {
			if !strings.Contains(Canonical(topic.Name), Canonical(name)) {
				t.Fatal("filter mismatch")
			}
			found = found || topic.Name == name
		}
		if !found {
			t.Fatal("filter omitted topic")
		}
	})
	topics[0].Prerequisites = append(topics[0].Prerequisites, "Changed")
	if slices.Contains(g.Topics("")[0].Prerequisites, "Changed") {
		t.Fatal("graph mutated")
	}
	if _, ok := g.Path("Unknown"); ok {
		t.Fatal("unknown path resolved")
	}
}

func TestTopicUnicodeMatching(t *testing.T) {
	g, err := Decode(strings.NewReader(`[{"subject":"Café Studies","relation":"depends_on","object":"Basics"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if topics := g.Topics(" CAFE\u0301\u00a0  STUD "); len(topics) != 1 || topics[0].Name != "Café Studies" {
		t.Fatal(topics)
	}
	if path, ok := g.Path("cafe\u0301\u00a0 studies"); !ok || path.Topic != "Café Studies" {
		t.Fatal(path)
	}
}
