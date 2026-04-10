package graph

import (
	"testing"

	"axiom-reasoning-svc/internal/domain"
)

func TestGetLocalSubgraph(t *testing.T) {
	g := NewInMemoryGraph()

	tests := []struct {
		name           string
		topics         []string
		depth          int
		wantMinLen     int
		mustContain    [][3]string // subject, relation, object
		mustNotContain [][3]string
	}{
		{
			name:       "single topic depth 0 returns direct edges only",
			topics:     []string{"Polymorphism"},
			depth:      0,
			wantMinLen: 1,
			mustContain: [][3]string{
				{"Polymorphism", "depends_on", "Inheritance"},
				{"Generics", "depends_on", "Polymorphism"},
				{"Design Patterns", "depends_on", "Polymorphism"},
			},
			mustNotContain: [][3]string{
				{"Merge Sort", "depends_on", "Recursion"},
			},
		},
		{
			name:       "single topic depth 1 includes transitive neighbours",
			topics:     []string{"Polymorphism"},
			depth:      1,
			wantMinLen: 3,
			mustContain: [][3]string{
				{"Polymorphism", "depends_on", "Inheritance"},
				{"Inheritance", "depends_on", "Classes"},
			},
		},
		{
			name:       "unknown topic returns empty slice",
			topics:     []string{"Quantum Computing"},
			depth:      2,
			wantMinLen: 0,
		},
		{
			name:       "multiple topics union their subgraphs",
			topics:     []string{"Binary Search", "Merge Sort"},
			depth:      0,
			wantMinLen: 2,
			mustContain: [][3]string{
				{"Binary Search", "depends_on", "Arrays"},
				{"Merge Sort", "depends_on", "Recursion"},
			},
		},
		{
			name:       "depth 2 traversal from Recursion covers Functions",
			topics:     []string{"Recursion"},
			depth:      2,
			wantMinLen: 3,
			mustContain: [][3]string{
				{"Recursion", "depends_on", "Functions"},
			},
		},
		{
			name:       "empty topics returns nothing",
			topics:     []string{},
			depth:      5,
			wantMinLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := g.GetLocalSubgraph(tt.topics, tt.depth)

			if len(result) < tt.wantMinLen {
				t.Errorf("got %d tuples, want at least %d", len(result), tt.wantMinLen)
			}

			resultSet := make(map[[3]string]bool)
			for _, r := range result {
				resultSet[[3]string{r.Subject, r.Relation, r.Object}] = true
			}

			for _, want := range tt.mustContain {
				if !resultSet[want] {
					t.Errorf("missing expected tuple: %v", want)
				}
			}

			for _, bad := range tt.mustNotContain {
				if resultSet[bad] {
					t.Errorf("unexpected tuple present: %v", bad)
				}
			}

			// Verify no duplicates
			if len(resultSet) != len(result) {
				t.Errorf("duplicates detected: %d unique vs %d total", len(resultSet), len(result))
			}
		})
	}
}

func TestNewInMemoryGraph_DefaultTuples(t *testing.T) {
	g := NewInMemoryGraph()
	all := g.All()
	if len(all) < 15 {
		t.Errorf("expected at least 15 default tuples, got %d", len(all))
	}
}

func TestAdd(t *testing.T) {
	g := NewInMemoryGraph()
	before := len(g.All())
	g.Add(domain.GraphTuple{Subject: "TestA", Relation: "rel", Object: "TestB"})
	after := len(g.All())
	if after != before+1 {
		t.Errorf("expected %d tuples after Add, got %d", before+1, after)
	}
}

func TestNodeExists(t *testing.T) {
	g := NewInMemoryGraph()

	tests := []struct {
		node string
		want bool
	}{
		{"Polymorphism", true},
		{"Inheritance", true},
		{"Variables", true},
		{"Quantum Computing", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.node, func(t *testing.T) {
			if got := g.NodeExists(tt.node); got != tt.want {
				t.Errorf("NodeExists(%q) = %v, want %v", tt.node, got, tt.want)
			}
		})
	}
}

func TestGetPrerequisiteChain(t *testing.T) {
	g := NewInMemoryGraph()

	tests := []struct {
		name      string
		topic     string
		wantLen   int
		wantFirst string // deepest prerequisite should be first
		wantLast  string // immediate prerequisite should be last
	}{
		{
			name:      "Polymorphism has 3 prerequisites: Variables→Objects→Classes→Inheritance",
			topic:     "Polymorphism",
			wantLen:   4,
			wantFirst: "Variables",
			wantLast:  "Inheritance",
		},
		{
			name:    "Variables has no prerequisites (leaf node)",
			topic:   "Variables",
			wantLen: 0,
		},
		{
			name:      "Recursion depends on Functions",
			topic:     "Recursion",
			wantLen:   1,
			wantFirst: "Functions",
			wantLast:  "Functions",
		},
		{
			name:    "unknown topic returns empty",
			topic:   "NonExistent",
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain := g.GetPrerequisiteChain(tt.topic)
			if len(chain) != tt.wantLen {
				t.Fatalf("GetPrerequisiteChain(%q) len = %d, want %d; got %v", tt.topic, len(chain), tt.wantLen, chain)
			}
			if tt.wantLen > 0 {
				if chain[0] != tt.wantFirst {
					t.Errorf("first element = %q, want %q; chain = %v", chain[0], tt.wantFirst, chain)
				}
				if chain[len(chain)-1] != tt.wantLast {
					t.Errorf("last element = %q, want %q; chain = %v", chain[len(chain)-1], tt.wantLast, chain)
				}
			}
		})
	}
}
