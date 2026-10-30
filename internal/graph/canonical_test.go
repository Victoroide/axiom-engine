package graph

import (
	"strings"
	"testing"
)

func TestResolveCanonicalTopics(t *testing.T) {
	g := NewInMemoryGraph()
	for _, tc := range []struct{ input, want string }{
		{"recursion", "Recursion"}, {"  Recursion ", "Recursion"}, {"Merge  Sort", "Merge Sort"}, {"MERGE\tSORT", "Merge Sort"},
		{"Recur\u200bsion\u200d", "Recursion"}, {"Recur\u00adsion", "Recursion"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got, ok := g.Resolve(tc.input); !ok || got != tc.want {
				t.Fatalf("Resolve(%q) = %q, %v; want %q", tc.input, got, ok, tc.want)
			}
		})
	}
	if got, ok := g.Resolve("unknown"); ok || got != "" {
		t.Fatalf("unknown topic resolved to %q, %v", got, ok)
	}
	custom, err := Decode(strings.NewReader(`[{"subject":"Recursión","relation":"depends_on","object":"Funciones"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := custom.Resolve("  RECURSIO\u0301N "); !ok || got != "Recursión" {
		t.Fatalf("NFD topic resolved to %q, %v", got, ok)
	}
}

func TestCanonicalFoldsUnicode(t *testing.T) {
	for _, pair := range [][2]string{{"ΟΔΟΣ", "οδος"}, {"STRASSE", "straße"}, {"E\u200b\u0301", "é"}} {
		if Canonical(pair[0]) != Canonical(pair[1]) {
			t.Errorf("%q and %q did not resolve to the same key", pair[0], pair[1])
		}
	}
}
