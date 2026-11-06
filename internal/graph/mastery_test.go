package graph

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCognitiveMasteryEvidence(t *testing.T) {
	g := NewInMemoryGraph()
	for _, tc := range []struct {
		mastered []string
		want     map[string]string
	}{
		{[]string{}, map[string]string{"Recursion": "failed", "Functions": "learning", "Merge Sort": "unassessed", "Dynamic Programming": "unassessed"}},
		{[]string{"Functions", "Merge Sort"}, map[string]string{"Recursion": "failed", "Functions": "mastered", "Merge Sort": "mastered", "Dynamic Programming": "unassessed"}},
	} {
		got := g.GenerateCognitiveShadow([]string{"Recursion"}, "", tc.mastered)
		states := map[string]string{}
		for _, node := range got.Nodes {
			states[node.ID] = node.CognitiveState
		}
		if !reflect.DeepEqual(states, tc.want) {
			t.Fatalf("states=%v want=%v", states, tc.want)
		}
	}
}

func TestCognitiveGraphWithoutMasteryKeepsResponseBytes(t *testing.T) {
	data, err := json.Marshal(NewInMemoryGraph().GenerateCognitiveShadow([]string{"Recursion"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"nodes":[{"id":"Dynamic Programming","label":"Dynamic Programming","cognitive_state":"mastered"},{"id":"Functions","label":"Functions","cognitive_state":"learning"},{"id":"Merge Sort","label":"Merge Sort","cognitive_state":"mastered"},{"id":"Recursion","label":"Recursion","cognitive_state":"failed"}],"edges":[{"source":"Dynamic Programming","target":"Recursion","relation":"depends_on"},{"source":"Merge Sort","target":"Recursion","relation":"depends_on"},{"source":"Recursion","target":"Functions","relation":"depends_on"}]}`
	if string(data) != want {
		t.Fatalf("response=%s", data)
	}
}
