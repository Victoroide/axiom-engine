package main

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func diagramInputs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	fs, err := os.OpenRoot("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fs.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, path := range []string{"docs/assets/diagrams/src/request-path.json", "docs/assets/diagrams/src/system-components.json", "docs/assets/diagrams/src/seed-graph.json", "internal/graph/seed.json"} {
		data, err := fs.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFixture(t, root, path, data)
	}
	return root
}

func writeFixture(t *testing.T, root, path string, data []byte) {
	t.Helper()
	fs, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fs.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := fs.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRejectExtraVisibleRoute(t *testing.T) {
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := registeredRoutes("../..")
	if err != nil {
		t.Fatal(err)
	}
	delete(registered, "GET /livez")
	d := ds[0]
	for i := range d.Nodes {
		if d.Nodes[i].ID == "probes" {
			d.Nodes[i].Routes = slices.DeleteFunc(d.Nodes[i].Routes, func(route string) bool { return route == "GET /livez" })
		}
	}
	if err := checkRoutes(d, registered); err == nil || !strings.Contains(err.Error(), "undeclared visible route") {
		t.Fatalf("accepted a removed registration still visible on its card: %v", err)
	}
}

func TestSeedGroupPadding(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	for _, side := range []string{"left", "right", "bottom"} {
		t.Run(side, func(t *testing.T) {
			d := diagram{W: 400, H: 300, Nodes: []node{{ID: "topic", Circle: true, Group: "g", Title: "Topic", Box: box{100, 120, 92, 92}}}, Groups: []group{{ID: "g", Title: "Group", Box: box{84, 80, 124, 148}}}}
			if err := validate(d, f); err != nil {
				t.Fatalf("16 px padding must pass: %v", err)
			}
			switch side {
			case "left":
				d.Groups[0].Box.X++
				d.Groups[0].Box.W--
			case "right":
				d.Groups[0].Box.W--
			case "bottom":
				d.Groups[0].Box.H--
			}
			if err := validate(d, f); err == nil || !strings.Contains(err.Error(), "outside its group") {
				t.Fatalf("accepted 15 px group padding: %v", err)
			}
		})
	}
}

func TestSeedCountsFollowCurriculum(t *testing.T) {
	root := diagramInputs(t)
	tuples, err := readCurriculum(root)
	if err != nil {
		t.Fatal(err)
	}
	tuples = append(tuples, tuple{Subject: "New topic", Relation: "depends_on", Object: "Variables"})
	data, err := json.Marshal(tuples)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "internal/graph/seed.json", data)
	ds, err := buildDiagrams(root, testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	if ds[2].Subtitle != "24 TOPICS · 21 RELATIONS" || !strings.Contains(ds[2].Description, "24 topics and 21 relations") {
		t.Fatalf("curriculum counts did not reach caption: %s; %s", ds[2].Subtitle, ds[2].Description)
	}
}

func TestCardGapMinimum(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	for _, gap := range []float64{23, 24} {
		for _, vertical := range []bool{false, true} {
			a, b := node{ID: "a", Title: "A", Box: box{40, 100, 184, 56}}, node{ID: "b", Title: "B", Box: box{224 + gap, 100, 184, 56}}
			if vertical {
				b.Box.X, b.Box.Y = 40, 156+gap
			}
			err := validate(diagram{W: 600, H: 400, Nodes: []node{a, b}}, f)
			if gap < 24 && (err == nil || !strings.Contains(err.Error(), "card gap")) {
				t.Fatalf("accepted %g px card gap: %v", gap, err)
			}
			if gap == 24 && err != nil {
				t.Fatalf("24 px card gap must pass: %v", err)
			}
		}
	}
}

func TestModulePackageInventory(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"cmd/server", "baml_client", "internal/known", "extra", "docs", "test/example"} {
		writeFixture(t, root, dir+"/code.go", []byte("package example\n"))
	}
	writeFixture(t, root, "tools/go.mod", []byte("module separate\n"))
	writeFixture(t, root, "tools/diagram/code.go", []byte("package example\n"))
	inventory, _, err := packageImports(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"cmd/server": true, "baml_client": true, "internal/known": true, "extra": true, "docs": true, "test/example": true}
	if !maps.Equal(inventory, want) {
		t.Fatalf("module inventory missed a package or crossed a module boundary: %v", inventory)
	}
}

func TestUnknownMotionEdgeReturnsWithoutPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("unknown motion edge panicked: %v", r)
		}
	}()
	if err := schedule(&diagram{Name: "sample"}, []string{"missing"}); err == nil || !strings.Contains(err.Error(), "has no edge missing") {
		t.Fatalf("missing motion edge: %v", err)
	}
}

func TestCircleMeasurementsRejectMissingGlyphs(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	if lines, err := wrapCircle("Topic \U0010ffff", 80, f); err == nil || len(lines) != 0 {
		t.Errorf("failed measurement returned wrapped lines: %q", lines)
	}
	if labels, err := circleLabels(node{Title: "\U0010ffff", Circle: true, Box: box{0, 0, 92, 92}}, f); err == nil || len(labels) != 0 {
		t.Fatalf("failed measurement returned circle labels: %v", labels)
	}
	if lines, err := wrapCircle("\U0010ffff", 80, f); err == nil || len(lines) != 0 {
		t.Fatalf("first-word measurement returned wrapped lines: %q", lines)
	}
}

var cachedTestFonts = sync.OnceValues(loadFonts)

func testFonts(t *testing.T) *fontSet {
	t.Helper()
	f, err := cachedTestFonts()
	if err != nil {
		t.Fatal(err)
	}
	return f
}
