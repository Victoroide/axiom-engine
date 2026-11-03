package service

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

func TestMergeDeduplicateProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		item := rapid.Custom(func(t *rapid.T) domain.PlanItem {
			topic := rapid.SampledFrom([]string{"Recursion", "recursion", " RECURSION ", "Recur\u200bsion", "Merge Sort", "MERGE\tSORT", "Recursión", "RECURSIO\u0301N", "Other", ""}).Draw(t, "topic")
			return domain.PlanItem{Topic: topic, Explanation: rapid.String().Draw(t, "explanation"), Priority: rapid.IntRange(0, 100).Draw(t, "priority")}
		})
		plans := rapid.SliceOfN(rapid.SliceOfN(item, 0, 10), 0, 6).Draw(t, "plans")
		merged := mergeAndDeduplicate(plans)
		if twice := mergeAndDeduplicate([][]domain.PlanItem{merged, merged}); !reflect.DeepEqual(merged, twice) {
			t.Fatalf("merge is not idempotent: %v -> %v", merged, twice)
		}
		seen := make(map[string]bool)
		position := 0
		for _, plan := range plans {
			for _, candidate := range plan {
				key := graph.Canonical(candidate.Topic)
				if seen[key] {
					continue
				}
				seen[key] = true
				if position >= len(merged) || !reflect.DeepEqual(merged[position], candidate) {
					t.Fatalf("first item for %q was not retained at %d", key, position)
				}
				position++
			}
		}
		if len(merged) != position {
			t.Fatalf("merge has %d items, want %d", len(merged), position)
		}
	})
}

func TestValidateGraphOrderProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 12).Draw(t, "nodes")
		var data strings.Builder
		data.WriteByte('[')
		for i := 1; i < n; i++ {
			if i > 1 {
				data.WriteByte(',')
			}
			fmt.Fprintf(&data, `{"subject":"Node%d","relation":"depends_on","object":"Node%d"}`, i, i-1)
		}
		data.WriteByte(']')
		g, err := graph.Decode(strings.NewReader(data.String()))
		if err != nil {
			t.Fatal(err)
		}
		requested := rapid.SliceOfN(rapid.IntRange(-1, n), 0, n).Draw(t, "requested")
		topics := make([]string, len(requested))
		highest := -1
		for i, index := range requested {
			topics[i] = fmt.Sprintf(" node%d ", index)
			if index >= 0 && index < n {
				highest = max(highest, index)
			}
		}
		candidates := rapid.SliceOfN(rapid.IntRange(-1, n), 0, 30).Draw(t, "items")
		items := make([]domain.PlanItem, len(candidates))
		expected := make([]int, 0, len(candidates))
		for i, index := range candidates {
			items[i] = domain.PlanItem{Topic: fmt.Sprintf(" NODE%d ", index), Priority: 99, PrerequisiteChain: []string{"Wrong"}, Explanation: fmt.Sprint(i)}
			if index >= 0 && index <= highest {
				expected = append(expected, index)
			}
		}
		slices.Sort(expected)
		got, dropped := validateAgainstGraph(items, g, topics)
		if len(got) != len(expected) || dropped != len(items)-len(expected) {
			t.Fatalf("kept %d, dropped %d, want %d and %d", len(got), dropped, len(expected), len(items)-len(expected))
		}
		for i, index := range expected {
			if got[i].Topic != fmt.Sprintf("Node%d", index) || got[i].Priority != i+1 || len(got[i].PrerequisiteChain) != index {
				t.Fatalf("incorrect item at rank %d: %+v", i, got[i])
			}
			for j, prerequisite := range got[i].PrerequisiteChain {
				if prerequisite != fmt.Sprintf("Node%d", j) {
					t.Fatalf("incorrect chain at %d: %v", j, got[i].PrerequisiteChain)
				}
			}
		}
	})
}
