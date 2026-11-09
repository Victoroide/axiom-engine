package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeometryRejectsCollisions(t *testing.T) {
	data, err := os.ReadFile("testdata/colliding.json")
	if err != nil {
		t.Fatal(err)
	}
	var d diagram
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	if err := validate(&d); err == nil || !strings.Contains(err.Error(), "clearance") {
		t.Fatalf("want clearance failure, got %v", err)
	}
}

func TestGeometryRejectsClippedArrowhead(t *testing.T) {
	var d diagram
	if err := readJSON("testdata/clipped-arrowhead.json", &d); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		y    float64
		want string
	}{
		{"inside target", 190, "arrowhead"},
		{"on target border", 180, "arrowhead"},
		{"outside target", 179, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d.Edges[0].Points[1].Y = tc.y
			err := validate(&d)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestLayoutArrowheadsClearTargets(t *testing.T) {
	for _, tc := range []struct{ name, edge string }{
		{"request-path", "mcp/pipeline"},
		{"request-path", "router/knowledge"},
		{"system-components", "server/service"},
	} {
		t.Run(tc.edge, func(t *testing.T) {
			d, err := loadDiagram(filepath.Join("..", ".."), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range d.Edges {
				if e.ID == tc.edge {
					d.Edges = []edge{e}
					d.Motion = nil
					if err := validate(d); err != nil {
						t.Fatal(err)
					}
					return
				}
			}
			t.Fatal("missing edge")
		})
	}
}

func TestGeometryBoundaries(t *testing.T) {
	base := func() *diagram {
		return &diagram{Width: 400, Height: 240, Nodes: []node{{ID: "a", X: 20, Y: 80, W: 80, H: 80}, {ID: "b", X: 280, Y: 80, W: 80, H: 80}}, Edges: []edge{{From: "a", To: "b", Points: []point{{100, 120}, {280, 120}}}}}
	}
	cases := []struct {
		name string
		edit func(*diagram)
		want string
	}{
		{"valid", func(d *diagram) {}, ""},
		{"outside canvas", func(d *diagram) { d.Nodes[0].X = -1 }, "viewBox"},
		{"arrow inside node", func(d *diagram) { d.Edges[0].Points[1].X = 290 }, "border"},
		{"arrow misses node", func(d *diagram) { d.Edges[0].Points[1].X = 270 }, "border"},
		{"label crosses edge", func(d *diagram) { d.Labels = []label{{Text: "crossing", X: 160, Y: 124, Style: "edge"}} }, "label"},
		{"label crosses node", func(d *diagram) { d.Labels = []label{{Text: "crossing", X: 30, Y: 110, Style: "edge"}} }, "label"},
		{"labels overlap", func(d *diagram) {
			d.Labels = []label{{Text: "first", X: 160, Y: 40, Style: "edge"}, {Text: "second", X: 170, Y: 40, Style: "edge"}}
		}, "label"},
		{"empty edge", func(d *diagram) { d.Edges[0].Points = nil }, "points"},
		{"unknown endpoint", func(d *diagram) { d.Edges[0].To = "missing" }, "endpoint"},
		{"circle corner", func(d *diagram) { d.Nodes[1].R = 40; d.Edges[0].Points[1] = point{280, 80} }, "border"},
		{"rounded corner", func(d *diagram) { d.Edges[0].Points[1] = point{280, 80} }, "border"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := base()
			tc.edit(d)
			err := validate(d)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func TestCommittedOutputMatchesGenerator(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, name := range []string{"request-path", "system-components", "seed-graph"} {
		d, err := loadDiagram(root, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := validate(d); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, theme := range []string{"light", "dark"} {
			data, err := render(d, theme)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "docs", "assets", "diagrams", name+"-"+theme+".svg")
			stored, err := readFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != string(stored) {
				t.Errorf("%s is stale; run make diagrams", path)
			}
		}
	}
}

func TestGeometryRejectsMissingMotionTargets(t *testing.T) {
	d := &diagram{Width: 200, Height: 200, Motion: []cue{{Node: "missing", Start: 0}}}
	if err := validate(d); err == nil || !strings.Contains(err.Error(), "unknown highlight") {
		t.Fatalf("got %v", err)
	}
	d.Motion = []cue{{Edge: "missing", Start: 0}}
	if err := validate(d); err == nil || !strings.Contains(err.Error(), "unknown pulse") {
		t.Fatalf("got %v", err)
	}
	d.Motion = []cue{{Node: "missing", Start: 7.5}}
	if err := validate(d); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("got %v", err)
	}
}

func TestLegendStaysInsideCanvas(t *testing.T) {
	d := &diagram{Width: 200, Height: 100, Legend: []legend{{Kind: "depends_on", Text: "", X: -10, Y: 50}}}
	if err := validate(d); err == nil || !strings.Contains(err.Error(), "legend") {
		t.Fatalf("want legend clipping failure, got %v", err)
	}
}

func TestGeometryRejectsTangledEdges(t *testing.T) {
	for _, tc := range []struct{ name, want string }{{"crossing", "cross"}, {"overlap", "collinear"}, {"collinear", "collinear"}, {"parallel-close", "parallel"}, {"shared-attachment", "attachment"}, {"unrelated-group", "group boundary"}, {"wide", "viewBox"}, {"tall", "viewBox"}, {"seed-tall", "viewBox"}} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var d diagram
			if err := json.Unmarshal(data, &d); err != nil {
				t.Fatal(err)
			}
			if tc.name == "seed-tall" {
				d.Name = "seed-graph"
			}
			if err := validate(&d); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestCanvasBudgets(t *testing.T) {
	for _, name := range []string{"request-path", "system-components", "seed-graph"} {
		d, err := loadDiagram(filepath.Join("..", ".."), name)
		if err != nil {
			t.Fatal(err)
		}
		height := 640.0
		if name == "seed-graph" {
			height = 1000
		}
		if d.Width > 960 || d.Height > height {
			t.Errorf("%s viewBox %gx%g exceeds 960x%g", name, d.Width, d.Height, height)
		}
		if name == "request-path" {
			for _, e := range d.Edges {
				if len(e.Points) > 3 {
					t.Errorf("%s has more than one bend", e.ID)
				}
			}
		}
	}
}

func TestDeclaredGridAlignment(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"row position", `{"width":400,"height":240,"nodes":[{"id":"a","x":20,"y":20,"w":40,"h":40,"row":"top"},{"id":"b","x":100,"y":22,"w":40,"h":40,"row":"top"}]}`, "row"},
		{"row height", `{"width":400,"height":240,"nodes":[{"id":"a","x":20,"y":20,"w":40,"h":40,"row":"top"},{"id":"b","x":100,"y":20,"w":40,"h":42,"row":"top"}]}`, "row"},
		{"column position", `{"width":400,"height":240,"nodes":[{"id":"a","x":20,"y":20,"w":40,"h":40,"column":"left"},{"id":"b","x":22,"y":100,"w":40,"h":40,"column":"left"}]}`, "column"},
		{"half pixel tolerance", `{"width":400,"height":240,"nodes":[{"id":"a","x":20,"y":20,"w":40,"h":40,"row":"top"},{"id":"b","x":100,"y":20.5,"w":40,"h":40.5,"row":"top"}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d diagram
			if err := json.Unmarshal([]byte(tc.data), &d); err != nil {
				t.Fatal(err)
			}
			err := validate(&d)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s error, got %v", tc.want, err)
			}
		})
	}
}

func TestSeparateLinksOnDeclaredGridLine(t *testing.T) {
	data, err := os.ReadFile("testdata/collinear.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, raw := range doc["nodes"].([]any) {
		raw.(map[string]any)["row"] = "top"
	}
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var d diagram
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	if err := validate(&d); err != nil {
		t.Fatal(err)
	}
}

func TestSeedRowsHaveAlignedCenters(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "seed-graph")
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]node{}
	for _, n := range d.Nodes {
		nodes[n.ID] = n
	}
	for _, row := range [][]string{{"Variables", "Objects", "Classes", "Inheritance", "Polymorphism", "Generics"}, {"Abstract Classes", "Interfaces", "Design Patterns"}, {"Functions", "Recursion", "Merge Sort", "Pointers", "Linked Lists", "Graphs"}, {"Binary Search", "Dynamic Programming", "Trees", "Graph Traversal"}, {"Binary Search Trees", "AVL Trees"}, {"Arrays", "Hash Tables"}} {
		y := nodes[row[0]].Y + nodes[row[0]].R
		for _, id := range row {
			n := nodes[id]
			if n.Y+n.R != y {
				t.Errorf("%s has center y=%g, want %g", id, n.Y+n.R, y)
			}
		}
	}
}

func TestRequestRowsHaveUniformHeightsAndGaps(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "request-path")
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]node{}
	for _, n := range d.Nodes {
		nodes[n.ID] = n
	}
	for _, row := range [][]string{{"client", "router", "public"}, {"mcp", "plan", "knowledge"}, {"baml", "pipeline"}} {
		tallest := 0.0
		for _, id := range row {
			tallest = max(tallest, nodes[id].H)
		}
		top := nodes[row[0]].Y
		gap := nodes[row[1]].X - nodes[row[0]].X - nodes[row[0]].W
		for i, id := range row {
			n := nodes[id]
			if n.Y != top || n.H != tallest {
				t.Errorf("%s row geometry y=%g h=%g, want y=%g h=%g", id, n.Y, n.H, top, tallest)
			}
			if i > 0 {
				prev := nodes[row[i-1]]
				if n.X-prev.X-prev.W != gap {
					t.Errorf("unequal gap before %s", id)
				}
			}
		}
	}
}

func TestLayoutGridDeclarations(t *testing.T) {
	for _, name := range []string{"request-path", "system-components", "seed-graph"} {
		d, err := loadDiagram(filepath.Join("..", ".."), name)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateAlignment(d); err != nil {
			t.Fatal(err)
		}
		nodes := map[string]node{}
		for _, n := range d.Nodes {
			nodes[n.ID] = n
			if n.Row == "" {
				t.Errorf("%s: %s has no row", name, n.ID)
			}
			if name == "seed-graph" && n.Column == "" {
				t.Errorf("%s has no column", n.ID)
			}
		}
		if name != "seed-graph" {
			continue
		}
		for _, e := range d.Edges {
			from, to := nodes[e.From], nodes[e.To]
			a, b := e.Points[0], e.Points[len(e.Points)-1]
			if from.Row == to.Row && a.Y != b.Y {
				t.Errorf("%s is not horizontal", e.ID)
			}
			if from.Column == to.Column && a.X != b.X {
				t.Errorf("%s is not vertical", e.ID)
			}
		}
	}
}

func TestOverlappingLinksOnDeclaredGridLineAreRejected(t *testing.T) {
	d := &diagram{Edges: []edge{{ID: "ab", From: "a", To: "b", Points: []point{{20, 100}, {180, 100}}}, {ID: "cd", From: "c", To: "d", Points: []point{{100, 100}, {300, 100}}}}}
	nodes := map[string]node{"a": {Row: "top"}, "b": {Row: "top"}, "c": {Row: "top"}, "d": {Row: "top"}}
	if err := validateEdgeRelations(d, nodes); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("want grid overlap rejection, got %v", err)
	}
}

func TestAlignmentRejectsDriftAcrossThreeNodes(t *testing.T) {
	for _, kind := range []string{"row", "column", "height"} {
		t.Run(kind, func(t *testing.T) {
			d := &diagram{Width: 400, Height: 300}
			for i, offset := range []float64{0, .4, -.4} {
				n := node{ID: string(rune('a' + i)), X: 20 + float64(i)*80, Y: 20, W: 40, H: 40, Row: "top"}
				switch kind {
				case "row":
					n.Y += offset
				case "column":
					n.X = 20 + offset
					n.Y = 20 + float64(i)*80
					n.Row = ""
					n.Column = "left"
				case "height":
					n.H += offset
				}
				d.Nodes = append(d.Nodes, n)
			}
			if err := validate(d); err == nil || !strings.Contains(err.Error(), "misaligned") {
				t.Fatalf("want misaligned error, got %v", err)
			}
		})
	}
}
