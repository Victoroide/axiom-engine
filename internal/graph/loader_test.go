package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeValidation(t *testing.T) {
	for _, input := range []string{`invalid`, `[]`, `null`, `[{"subject":"","relation":"depends_on","object":"b"}]`, `[{"subject":"a","relation":"unknown","object":"b"}]`, `[{"subject":"a","relation":"is_a","object":"a"}]`, `[{"subject":"a","relation":"is_a","object":" "}]`, `[{"subject":"a","relation":"is_a","object":"b","typo":true}]`, `[{"subject":"a","relation":"is_a","object":"b"}] {}`} {
		t.Run(input, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(input)); err == nil {
				t.Fatal("expected invalid graph")
			}
		})
	}
}

func TestLoadAndDefensiveCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(path, []byte(`[{"subject":" A ","relation":"depends_on","object":"B"},{"subject":"B","relation":"is_a","object":"C"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !g.NodeExists("A") || !reflect.DeepEqual(g.GetPrerequisiteChain("A"), []string{"C", "B"}) {
		t.Fatal(g.All())
	}
	all := g.All()
	all[0].Subject = "mutated"
	if g.NodeExists("mutated") || g.All()[0].Subject == "mutated" {
		t.Fatal("graph mutated through copy")
	}
	if _, err := Load(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected missing file error")
	}
	if _, err := Load(""); err != nil {
		t.Fatal(err)
	}
}

func TestShadowDeterministic(t *testing.T) {
	g := NewInMemoryGraph()
	want, _ := json.Marshal(g.GenerateCognitiveShadow([]string{"Recursion", "Polymorphism"}, ""))
	for range 100 {
		got, _ := json.Marshal(g.GenerateCognitiveShadow([]string{"Recursion", "Polymorphism"}, ""))
		if string(got) != string(want) {
			t.Fatal("nondeterministic shadow")
		}
	}
}

func TestDecodeRejectsCycles(t *testing.T) {
	for _, tc := range []struct {
		name, input, path string
	}{
		{"two nodes", `[{"subject":"B","relation":"depends_on","object":"A"},{"subject":"A","relation":"depends_on","object":"B"}]`, "A -> B -> A"},
		{"three nodes", `[{"subject":"C","relation":"depends_on","object":"A"},{"subject":"A","relation":"depends_on","object":"B"},{"subject":"B","relation":"depends_on","object":"C"}]`, "A -> B -> C -> A"},
		{"mixed relations", `[{"subject":"A","relation":"is_a","object":"B"},{"subject":"B","relation":"depends_on","object":"A"}]`, "A -> B -> A"},
		{"cycle below root", `[{"subject":"A","relation":"depends_on","object":"C"},{"subject":"C","relation":"depends_on","object":"B"},{"subject":"B","relation":"depends_on","object":"C"}]`, "B -> C -> B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tc.input))
			want := "graph contains a prerequisite cycle: " + tc.path
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	}
}

func TestDecodeAllowsSharedPrerequisites(t *testing.T) {
	_, err := Decode(strings.NewReader(`[{"subject":"A","relation":"depends_on","object":"B"},{"subject":"A","relation":"depends_on","object":"C"},{"subject":"B","relation":"is_a","object":"D"},{"subject":"C","relation":"depends_on","object":"D"}]`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestDecodeBoundsCycleDetails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  int
		prefix string
	}{
		{"many nodes", 1000, "Node"},
		{"long names", 2, strings.Repeat("界", 1000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tuples := make([]map[string]string, tc.count)
			for i := range tc.count {
				tuples[i] = map[string]string{"subject": fmt.Sprintf("%s%04d", tc.prefix, i), "relation": "depends_on", "object": fmt.Sprintf("%s%04d", tc.prefix, (i+1)%tc.count)}
			}
			data, err := json.Marshal(tuples)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Decode(strings.NewReader(string(data)))
			if err == nil || len(err.Error()) > 1500 || !strings.Contains(err.Error(), "…") {
				t.Fatalf("cycle details were not bounded: %v", err)
			}
			if tc.count > 16 && (!strings.Contains(err.Error(), "Node0000 -> Node0001") || !strings.Contains(err.Error(), "(1000 nodes)")) {
				t.Fatalf("missing cycle origin or size: %v", err)
			}
		})
	}
}

func TestDecodeRejectsCanonicalCollisions(t *testing.T) {
	for _, name := range []string{"recursion", "RECURSION", "Recursion  Topic", "Recursio\u0301n"} {
		t.Run(name, func(t *testing.T) {
			original := "Recursion"
			if name == "Recursion  Topic" {
				original = "Recursion Topic"
			}
			if name == "Recursio\u0301n" {
				original = "Recursión"
			}
			input, err := json.Marshal([]map[string]string{{"subject": original, "relation": "depends_on", "object": "Functions"}, {"subject": name, "relation": "depends_on", "object": "Functions"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(strings.NewReader(string(input))); err == nil || !strings.Contains(err.Error(), "canonical") {
				t.Fatalf("expected canonical name collision, got %v", err)
			}
		})
	}
}

func TestDecodeRejectsFoldedNameCollisions(t *testing.T) {
	for _, pair := range [][2]string{{"Straße", "STRASSE"}, {"ΟΔΟΣ", "οδος"}, {"Recursion", "Recur\u200bsion"}} {
		data, err := json.Marshal([]map[string]string{{"subject": pair[0], "relation": "depends_on", "object": "Base"}, {"subject": pair[1], "relation": "depends_on", "object": "Base"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(strings.NewReader(string(data))); err == nil || !strings.Contains(err.Error(), "canonical node name collision") {
			t.Fatalf("expected colliding names %q and %q to fail, got %v", pair[0], pair[1], err)
		}
	}
}

func TestDecodeRejectsEmptyCanonicalName(t *testing.T) {
	if _, err := Decode(strings.NewReader(`[{"subject":"\u200b","relation":"depends_on","object":"Base"}]`)); err == nil {
		t.Fatal("format-only name was accepted")
	}
}
