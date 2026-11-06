package graph

import (
	"reflect"
	"strings"
	"testing"
)

func TestSummary(t *testing.T) {
	g, err := Decode(strings.NewReader(`[
 {"subject":"End","relation":"depends_on","object":"Middle"},
 {"subject":"End","relation":"depends_on","object":"Side"},
 {"subject":"Middle","relation":"is_a","object":"Root"},
 {"subject":"Side","relation":"depends_on","object":"Root"},
 {"subject":"Long End","relation":"depends_on","object":"End"}
 ]`))
	if err != nil {
		t.Fatal(err)
	}
	got := g.Summary()
	if got.Concepts != 5 || !reflect.DeepEqual(got.Relations, map[string]int{"depends_on": 4, "is_a": 1}) || !reflect.DeepEqual(got.Roots, []string{"Root"}) || !reflect.DeepEqual(got.LongestChain, []string{"Root", "Middle", "End", "Long End"}) {
		t.Fatalf("summary=%+v", got)
	}
	for range 10 {
		if !reflect.DeepEqual(g.Summary(), got) {
			t.Fatal("summary is nondeterministic")
		}
	}
}
