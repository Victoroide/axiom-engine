package graph

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func FuzzDecode(f *testing.F) {
	f.Add(seed)
	for _, name := range []string{"", " ", "Graphs, Trees", "Rec\x1bursion", "Rec\x00ursion", "Rec\tursion", "Recur\u00adsion", "Recur\u200bsion", "Rec\u202eursion", "Bad\ufffdName", strings.Repeat("界", 129)} {
		data, err := json.Marshal([]domain.GraphTuple{{Subject: name, Relation: "depends_on", Object: "Base"}})
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte("[{\"subject\":\"Bad\xffName\",\"relation\":\"depends_on\",\"object\":\"Base\"}]"))
	f.Add([]byte(`[{"subject":"A","relation":"depends_on","object":"B"},{"subject":"B","relation":"is_a","object":"A"}]`))
	f.Add([]byte(`[{"subject":"Merge Sort","relation":"depends_on","object":"Base"},{"subject":"merge  sort","relation":"depends_on","object":"Base"}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		g, err := Decode(bytes.NewReader(data))
		if err != nil {
			return
		}
		keys := make(map[string]string)
		for name := range g.nodes {
			if name == "" || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) > 128 || strings.ContainsAny(name, ",\ufffd") || strings.ContainsFunc(name, func(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Cf) }) {
				t.Fatalf("accepted invalid node name %q", name)
			}
			key := Canonical(name)
			if previous, exists := keys[key]; exists && previous != name {
				t.Fatalf("canonical collision: %q and %q", previous, name)
			}
			keys[key] = name
			seen := map[string]bool{name: true}
			for _, prerequisite := range g.GetPrerequisiteChain(name) {
				if seen[prerequisite] {
					t.Fatalf("chain for %q repeats %q", name, prerequisite)
				}
				seen[prerequisite] = true
			}
		}
		// Kahn's algorithm, independent of the loader's own cycle detector.
		indegree := make(map[string]int, len(g.nodes))
		next := make(map[string][]string)
		for _, tuple := range g.All() {
			next[tuple.Subject] = append(next[tuple.Subject], tuple.Object)
			indegree[tuple.Object]++
		}
		var queue []string
		for name := range g.nodes {
			if indegree[name] == 0 {
				queue = append(queue, name)
			}
		}
		removed := 0
		for len(queue) > 0 {
			node := queue[0]
			queue = queue[1:]
			removed++
			for _, object := range next[node] {
				if indegree[object]--; indegree[object] == 0 {
					queue = append(queue, object)
				}
			}
		}
		if removed != len(g.nodes) {
			t.Fatalf("accepted a prerequisite cycle among %d nodes", len(g.nodes)-removed)
		}
	})
}

func FuzzCanonical(f *testing.F) {
	for _, s := range []string{"", "  Merge\t Sort  ", "Recursio\u0301n", "Straße", "ΟΔΟΣ", "Recur\u200bsion", "\xff\u202eA"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := Canonical(s)
		if twice := Canonical(got); twice != got {
			t.Fatalf("canonicalization is not idempotent: %q -> %q -> %q", s, got, twice)
		}
	})
}
