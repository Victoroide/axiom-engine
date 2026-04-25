package graph

import (
	"testing"
)

func TestGenerateCognitiveShadow_TargetTopic(t *testing.T) {
	g := NewInMemoryGraph()

	// Query for Recursion with target_topic="Recursion"
	// Expected depth-1 neighborhood:
	//   Recursion depends_on Functions      → Recursion, Functions
	//   Merge Sort depends_on Recursion     → Merge Sort
	//   Dynamic Programming depends_on Recursion → Dynamic Programming
	// So nodes: Recursion, Functions, Merge Sort, Dynamic Programming
	// Edges: 3 edges (Recursion→Functions, MergeSort→Recursion, DP→Recursion)
	resp := g.GenerateCognitiveShadow("student-001", []string{"Recursion"}, "Recursion")

	statusMap := make(map[string]string, len(resp.Nodes))
	for _, n := range resp.Nodes {
		statusMap[n.ID] = n.CognitiveState
	}

	// Verify target topic is marked as "failed"
	if statusMap["Recursion"] != "failed" {
		t.Errorf("Recursion cognitive_state = %q, want \"failed\"", statusMap["Recursion"])
	}

	// Verify prerequisite is marked as "learning"
	if statusMap["Functions"] != "learning" {
		t.Errorf("Functions cognitive_state = %q, want \"learning\"", statusMap["Functions"])
	}

	// Verify dependent topics are marked as "mastered" (not failed, not prereqs)
	for _, topic := range []string{"Merge Sort", "Dynamic Programming"} {
		if statusMap[topic] != "mastered" {
			t.Errorf("%s cognitive_state = %q, want \"mastered\"", topic, statusMap[topic])
		}
	}

	// Verify that unrelated topics are NOT present
	for _, absent := range []string{"Binary Search Trees", "Polymorphism", "Inheritance", "Pointers", "Variables"} {
		if _, found := statusMap[absent]; found {
			t.Errorf("unexpected node %q present in filtered subgraph", absent)
		}
	}

	// Verify only valid cognitive states
	for _, n := range resp.Nodes {
		switch n.CognitiveState {
		case "failed", "learning", "mastered":
			// ok
		default:
			t.Errorf("node %q has invalid cognitive_state %q", n.ID, n.CognitiveState)
		}
	}

	// Verify every node appears in at least one edge (no orphans)
	edgeNodes := make(map[string]bool)
	for _, e := range resp.Edges {
		edgeNodes[e.Source] = true
		edgeNodes[e.Target] = true
	}
	for _, n := range resp.Nodes {
		if !edgeNodes[n.ID] {
			t.Errorf("orphan node %q has no edges", n.ID)
		}
	}

	// Verify edges only reference nodes in the node list
	for _, e := range resp.Edges {
		if _, ok := statusMap[e.Source]; !ok {
			t.Errorf("edge source %q not in node list", e.Source)
		}
		if _, ok := statusMap[e.Target]; !ok {
			t.Errorf("edge target %q not in node list", e.Target)
		}
	}
}

func TestGenerateCognitiveShadow_NoTargetTopic(t *testing.T) {
	g := NewInMemoryGraph()

	// Without target_topic, all failed topics are BFS seeds
	resp := g.GenerateCognitiveShadow("student-002", []string{"Polymorphism", "Recursion"}, "")

	statusMap := make(map[string]string, len(resp.Nodes))
	for _, n := range resp.Nodes {
		statusMap[n.ID] = n.CognitiveState
	}

	// Both failed topics must be present and marked "failed"
	if statusMap["Polymorphism"] != "failed" {
		t.Errorf("Polymorphism cognitive_state = %q, want \"failed\"", statusMap["Polymorphism"])
	}
	if statusMap["Recursion"] != "failed" {
		t.Errorf("Recursion cognitive_state = %q, want \"failed\"", statusMap["Recursion"])
	}

	// Prerequisites should be "learning"
	if statusMap["Inheritance"] != "learning" {
		t.Errorf("Inheritance cognitive_state = %q, want \"learning\"", statusMap["Inheritance"])
	}
	if statusMap["Functions"] != "learning" {
		t.Errorf("Functions cognitive_state = %q, want \"learning\"", statusMap["Functions"])
	}

	// Nodes that are only in the depth-1 neighborhood should be present
	// but FAR nodes should be absent (depth-1 only, not the full graph)
	if len(resp.Nodes) >= 20 {
		t.Errorf("expected filtered subgraph, got %d nodes (full graph has 20+)", len(resp.Nodes))
	}

	// No orphan nodes
	edgeNodes := make(map[string]bool)
	for _, e := range resp.Edges {
		edgeNodes[e.Source] = true
		edgeNodes[e.Target] = true
	}
	for _, n := range resp.Nodes {
		if !edgeNodes[n.ID] {
			t.Errorf("orphan node %q has no edges", n.ID)
		}
	}
}

func TestGenerateCognitiveShadow_EmptyFailedTopics(t *testing.T) {
	g := NewInMemoryGraph()

	resp := g.GenerateCognitiveShadow("student-003", []string{}, "")

	// No failed topics → no seeds → empty subgraph
	if len(resp.Nodes) != 0 {
		t.Errorf("expected 0 nodes for empty failed topics, got %d", len(resp.Nodes))
	}
	if len(resp.Edges) != 0 {
		t.Errorf("expected 0 edges for empty failed topics, got %d", len(resp.Edges))
	}
}

func TestGenerateCognitiveShadow_UnknownTopic(t *testing.T) {
	g := NewInMemoryGraph()

	resp := g.GenerateCognitiveShadow("student-004", []string{"QuantumComputing"}, "QuantumComputing")

	// Unknown topic has no edges → empty result
	if len(resp.Nodes) != 0 {
		t.Errorf("expected 0 nodes for unknown topic, got %d", len(resp.Nodes))
	}
	if len(resp.Edges) != 0 {
		t.Errorf("expected 0 edges for unknown topic, got %d", len(resp.Edges))
	}
}

func TestGenerateCognitiveShadow_NoStudentIDInResponse(t *testing.T) {
	g := NewInMemoryGraph()

	// The response should NOT contain student_id (removed per Change 3)
	resp := g.GenerateCognitiveShadow("student-005", []string{"Recursion"}, "Recursion")

	// CognitiveGraphResponse no longer has StudentID field - this test verifies
	// the struct compiles correctly without it
	if resp.Nodes == nil {
		t.Error("Nodes should not be nil")
	}
	if resp.Edges == nil {
		t.Error("Edges should not be nil")
	}
}

func TestGenerateCognitiveShadow_CognitiveStateFieldName(t *testing.T) {
	g := NewInMemoryGraph()

	resp := g.GenerateCognitiveShadow("student-006", []string{"Recursion"}, "Recursion")

	// Verify CognitiveState field is populated (not the old Status field)
	for _, n := range resp.Nodes {
		if n.CognitiveState == "" {
			t.Errorf("node %q has empty CognitiveState", n.ID)
		}
		// Verify label equals ID
		if n.Label != n.ID {
			t.Errorf("node %q label = %q, want ID to match label", n.ID, n.Label)
		}
	}
}
