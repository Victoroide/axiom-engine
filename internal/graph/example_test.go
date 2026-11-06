package graph_test

import (
	"slices"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestComputerScienceCurriculum(t *testing.T) {
	g, err := graph.Load("../../examples/graphs/computer-science.json")
	if err != nil {
		t.Fatal(err)
	}
	topics := g.Topics("")
	if len(topics) < 100 || len(g.All()) < 120 {
		t.Fatalf("concepts=%d relations=%d", len(topics), len(g.All()))
	}
	counts := map[string]int{}
	for _, edge := range g.All() {
		counts[edge.Relation]++
		if slices.Contains(g.GetPrerequisiteChain(edge.Object), edge.Subject) {
			t.Fatalf("cycle: %+v", edge)
		}
	}
	if counts["depends_on"] == 0 || counts["is_a"] == 0 {
		t.Fatal(counts)
	}
	for _, topic := range topics {
		resolved, err := service.ValidateTopics("topics", []string{topic.Name}, 1, g)
		if err != nil || len(resolved) != 1 || resolved[0] != topic.Name {
			t.Fatalf("topic=%q resolved=%v err=%v", topic.Name, resolved, err)
		}
	}
	for _, topic := range []string{"Variables", "Functions", "Classes", "Polymorphism", "Recursion", "Hash Tables", "Dynamic Programming", "SQL", "Transactions", "Virtual Memory", "TCP", "Routing"} {
		if !g.NodeExists(topic) {
			t.Fatalf("missing subject area %q", topic)
		}
	}
}
