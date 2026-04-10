package graph

import (
	"testing"
)

func TestGenerateCognitiveShadow(t *testing.T) {
	g := NewInMemoryGraph()

	tests := []struct {
		name           string
		studentID      string
		failedTopics   []string
		wantFailed     []string
		wantLearning   []string
		wantMastered   []string // spot-check, not exhaustive
		wantEdgeCount  int
	}{
		{
			name:         "Polymorphism failed marks prerequisite chain as learning",
			studentID:    "student-001",
			failedTopics: []string{"Polymorphism"},
			wantFailed:   []string{"Polymorphism"},
			wantLearning: []string{"Inheritance", "Classes", "Objects", "Variables"},
			wantMastered: []string{"Recursion", "Arrays", "Functions"},
			wantEdgeCount: 20,
		},
		{
			name:         "multiple failed topics",
			studentID:    "student-002",
			failedTopics: []string{"Polymorphism", "Recursion"},
			wantFailed:   []string{"Polymorphism", "Recursion"},
			wantLearning: []string{"Inheritance", "Classes", "Objects", "Variables", "Functions"},
			wantMastered: []string{"Arrays", "Pointers"},
			wantEdgeCount: 20,
		},
		{
			name:         "no failed topics marks all as mastered",
			studentID:    "student-003",
			failedTopics: []string{},
			wantFailed:   []string{},
			wantLearning: []string{},
			wantMastered: []string{"Polymorphism", "Recursion", "Variables"},
			wantEdgeCount: 20,
		},
		{
			name:         "unknown topic does not crash",
			studentID:    "student-004",
			failedTopics: []string{"QuantumComputing"},
			wantFailed:   []string{},
			wantLearning: []string{},
			wantMastered: []string{"Polymorphism"},
			wantEdgeCount: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := g.GenerateCognitiveShadow(tt.studentID, tt.failedTopics)

			if resp.StudentID != tt.studentID {
				t.Errorf("student_id = %q, want %q", resp.StudentID, tt.studentID)
			}

			// Build status lookup
			statusMap := make(map[string]string, len(resp.Nodes))
			for _, n := range resp.Nodes {
				statusMap[n.ID] = n.Status
			}

			// Check no duplicate nodes
			if len(statusMap) != len(resp.Nodes) {
				t.Errorf("duplicate nodes: %d unique vs %d total", len(statusMap), len(resp.Nodes))
			}

			// Verify all graph nodes are present
			totalNodes := len(g.nodes)
			if len(resp.Nodes) != totalNodes {
				t.Errorf("node count = %d, want %d", len(resp.Nodes), totalNodes)
			}

			// Check edge count
			if len(resp.Edges) != tt.wantEdgeCount {
				t.Errorf("edge count = %d, want %d", len(resp.Edges), tt.wantEdgeCount)
			}

			// Verify expected statuses
			for _, name := range tt.wantFailed {
				if statusMap[name] != "failed" {
					t.Errorf("node %q status = %q, want \"failed\"", name, statusMap[name])
				}
			}
			for _, name := range tt.wantLearning {
				if statusMap[name] != "learning" {
					t.Errorf("node %q status = %q, want \"learning\"", name, statusMap[name])
				}
			}
			for _, name := range tt.wantMastered {
				if statusMap[name] != "mastered" {
					t.Errorf("node %q status = %q, want \"mastered\"", name, statusMap[name])
				}
			}

			// Verify only valid statuses
			for _, n := range resp.Nodes {
				switch n.Status {
				case "failed", "learning", "mastered":
					// ok
				default:
					t.Errorf("node %q has invalid status %q", n.ID, n.Status)
				}
			}
		})
	}
}
