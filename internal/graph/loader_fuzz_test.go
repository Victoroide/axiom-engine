package graph

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
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
	f.Add([]byte(`[{"subject":"A","relation":"is_a","object":"B","extra":true}]`))
	f.Add([]byte(`[{"subject":"A","relation":"is_a","object":"B"},null]`))
	f.Add([]byte(`[{"subject":"A","relation":"unknown","object":"B"}]`))
	f.Add([]byte(strings.Repeat(" ", 30) + "桐"))
	f.Fuzz(func(t *testing.T, data []byte) {
		g, err := Decode(bytes.NewReader(data))
		again, nextErr := Decode(iotest.HalfReader(bytes.NewReader(data)))
		if err != nil {
			if again != nil || nextErr == nil || err.Error() != nextErr.Error() {
				t.Fatalf("diagnostic changed with read size: %v vs %v", err, nextErr)
			}
			return
		}
		if nextErr != nil || !slices.Equal(g.All(), again.All()) {
			t.Fatalf("tuple order or acceptance changed with read size: %v", nextErr)
		}

		// Check the accepted tuple sequence against a whole-array decode.
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var input []domain.GraphTuple
		if err := dec.Decode(&input); err != nil || len(input) == 0 {
			t.Fatalf("accepted invalid tuple array: %v", err)
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			t.Fatalf("accepted trailing data: %v", err)
		}
		seenTuples := make(map[domain.GraphTuple]bool)
		var unique []domain.GraphTuple
		for _, tuple := range input {
			tuple.Subject = strings.TrimSpace(tuple.Subject)
			tuple.Object = strings.TrimSpace(tuple.Object)
			tuple.Relation = strings.TrimSpace(tuple.Relation)
			if tuple.Subject == "" || tuple.Object == "" || tuple.Subject == tuple.Object || (tuple.Relation != "depends_on" && tuple.Relation != "is_a") {
				t.Fatalf("accepted invalid tuple: %+v", tuple)
			}
			if !seenTuples[tuple] {
				seenTuples[tuple] = true
				unique = append(unique, tuple)
			}
		}
		if !slices.Equal(g.All(), unique) {
			t.Fatalf("unexpected distinct tuple order: got %v, want %v", g.All(), unique)
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
