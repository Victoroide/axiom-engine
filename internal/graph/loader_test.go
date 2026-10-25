package graph

import (
	"encoding/json"
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
