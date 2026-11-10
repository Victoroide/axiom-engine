package main

import (
	"strings"
	"testing"
)

func TestDiagramsValidate(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if err := validate(d, f); err != nil {
			t.Error(err)
		}
	}
}

func TestRejectGeometryDefects(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, want string
		d          diagram
	}{
		{"diagonal", "not orthogonal", diagram{Edges: []edge{{ID: "e", Points: []point{{40, 100}, {200, 140}}}}}},
		{"crossing", "cross or overlap", diagram{Edges: []edge{{ID: "a", Points: []point{{40, 100}, {240, 100}}}, {ID: "b", Points: []point{{120, 80}, {120, 180}}}}}},
		{"overlap", "cross or overlap", diagram{Edges: []edge{{ID: "a", Points: []point{{40, 100}, {240, 100}}}, {ID: "b", Points: []point{{120, 100}, {300, 100}}}}}},
		{"collinear gap", "collinear", diagram{Edges: []edge{{ID: "a", Points: []point{{40, 100}, {100, 100}}}, {ID: "b", Points: []point{{108, 100}, {200, 100}}}}}},
		{"parallel clearance", "parallel edges", diagram{Edges: []edge{{ID: "a", Points: []point{{40, 100}, {240, 100}}}, {ID: "b", Points: []point{{40, 110}, {240, 110}}}}}},
		{"shared port", "share a node port", diagram{Nodes: []node{{ID: "n", Box: box{40, 100, 184, 56}}}, Edges: []edge{{ID: "a", From: "n", Points: []point{{224, 128}, {280, 128}}}, {ID: "b", From: "n", Points: []point{{224, 128}, {260, 128}, {260, 200}}}}}},
		{"node clearance", "clearance of node", diagram{Nodes: []node{{ID: "n", Box: box{100, 100, 184, 56}}}, Edges: []edge{{ID: "a", Points: []point{{40, 128}, {340, 128}}}}}},
		{"node border", "nodes b and a overlap", diagram{Nodes: []node{{ID: "a", Box: box{40, 100, 184, 56}}, {ID: "b", Box: box{224.5, 100, 184, 56}}}}},
		{"label clearance", "overlaps edge", diagram{Labels: []label{{Text: "Label", X: 120, Y: 100, Size: 14}}, Edges: []edge{{ID: "a", Points: []point{{40, 96}, {240, 96}}}}}},
		{"text overlap", "labels", diagram{Labels: []label{{Text: "First", X: 100, Y: 100, Size: 14}, {Text: "Second", X: 102, Y: 100, Size: 14}}}},
		{"text overflow", "overflows", diagram{Nodes: []node{{ID: "a", Title: strings.Repeat("W", 20), Box: box{40, 100, 184, 56}}}}},
		{"group traversal", "crosses unrelated group", diagram{Groups: []group{{ID: "g", Box: box{100, 100, 120, 100}}}, Edges: []edge{{ID: "a", Points: []point{{40, 160}, {300, 160}}}}}},
		{"group exit", "leaves its group", diagram{Groups: []group{{ID: "g", Box: box{40, 80, 300, 200}}}, Nodes: []node{{ID: "a", Group: "g", Box: box{80, 160, 184, 56}}}, Edges: []edge{{ID: "a", From: "a", Points: []point{{264, 188}, {400, 188}}}}}},
		{"edge margin", "violates viewBox margin", diagram{Edges: []edge{{ID: "a", Points: []point{{10, 100}, {200, 100}}}}}},
		{"label margin", "violates viewBox margin", diagram{Labels: []label{{Text: "Label", X: 10, Y: 100, Size: 14}}}},
		{"node margin", "outside viewBox", diagram{Nodes: []node{{ID: "a", Box: box{10, 100, 184, 56}}}}},
		{"long edge", "exceeds 600", diagram{W: 840, Edges: []edge{{ID: "a", Points: []point{{20, 100}, {621, 100}}}}}},
		{"wide canvas", "width exceeds", diagram{W: 841}},
		{"tall canvas", "height exceeds", diagram{H: 801}},
		{"tall seed", "height exceeds", diagram{Name: "seed-graph", H: 921}},
		{"card size", "184 x 56", diagram{Nodes: []node{{ID: "a", Box: box{40, 100, 185, 56}}}}},
		{"inward attachment", "inward or tangent", diagram{Nodes: []node{{ID: "a", Box: box{40, 100, 184, 56}}}, Edges: []edge{{ID: "a", From: "a", Points: []point{{224, 128}, {200, 128}}}}}},
		{"arrowhead clearance", "arrowhead lacks", diagram{Nodes: []node{{ID: "a", Box: box{240, 100, 184, 56}}}, Edges: []edge{{ID: "a", To: "a", Points: []point{{230, 128}, {240, 128}}}}}},
		{"corner port", "too close to node corner", diagram{Nodes: []node{{ID: "a", Box: box{40, 100, 184, 56}}}, Edges: []edge{{ID: "a", From: "a", Points: []point{{224, 105}, {280, 105}}}}}},
		{"circle port", "endpoint is not on circle border", diagram{Nodes: []node{{ID: "circle", Circle: true, Box: box{100, 100, 84, 84}}}, Edges: []edge{{ID: "a", From: "circle", Points: []point{{184, 120}, {240, 120}}}}}},
		{"circle text", "overflows", diagram{Nodes: []node{{ID: "a", Title: "Programming", Circle: true, Box: box{100, 100, 60, 60}}}}},
		{"ellipse", "must be a circle", diagram{Nodes: []node{{ID: "a", Circle: true, Box: box{100, 100, 84, 80}}}}},
		{"self crossing", "crosses itself", diagram{Edges: []edge{{ID: "a", Points: []point{{40, 100}, {200, 100}, {200, 200}, {100, 200}, {100, 80}}}}}},
		{"repeated segment", "collinear consecutive", diagram{Edges: []edge{{ID: "a", Points: []point{{40, 100}, {100, 100}, {200, 100}}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.d.W == 0 {
				tt.d.W = 600
			}
			if tt.d.H == 0 {
				tt.d.H = 500
			}
			err := validate(tt.d, f)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q; got %v", tt.want, err)
			}
		})
	}
}

func TestPlacementIsStrict(t *testing.T) {
	for _, input := range []string{`{"Title":"Content"}`, `{"Nodes":{"a":{"X":10,"Y":20,"W":184,"H":56,"title":"Content"}}}`, `{} {}`} {
		if err := decodePlacement([]byte(input), new(placement)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestRejectAppearanceDefects(t *testing.T) {
	for _, theme := range []string{"light", "dark"} {
		p := palettes[theme]
		if err := validatePalette(p); err != nil {
			t.Fatal(err)
		}
		bad := p
		bad.Muted = p.Surface
		if err := validatePalette(bad); err == nil || !strings.Contains(err.Error(), "text contrast") {
			t.Fatalf("%s low contrast accepted: %v", theme, err)
		}
		bad = p
		bad.Soft = p.Surface
		if err := validatePalette(bad); err == nil || !strings.Contains(err.Error(), "highlight contrast") {
			t.Fatalf("%s invisible highlight accepted: %v", theme, err)
		}
		bad = p
		bad.Page = "transparent"
		if err := validatePalette(bad); err == nil {
			t.Fatal("transparent page accepted")
		}
	}
}

func TestRejectEdgeInkContrast(t *testing.T) {
	for _, tc := range []struct {
		theme, faint string
	}{
		{"light", "#959595"},
		{"dark", "#5f5f5f"},
	} {
		for _, defect := range []string{"equal", "faint"} {
			t.Run(tc.theme+"/"+defect, func(t *testing.T) {
				p := palettes[tc.theme]
				p.Edge = p.Page
				if defect == "faint" {
					p.Edge = tc.faint
				}
				if err := validatePalette(p); err == nil || !strings.Contains(err.Error(), "edge contrast") {
					t.Fatalf("accepted indistinct shafts, arrowheads and legend samples: %v", err)
				}
			})
		}
	}
}

func TestRejectMotionDefects(t *testing.T) {
	for _, kind := range []string{"overlap", "highlight", "rest", "duration"} {
		ds, err := buildDiagrams("../..", testFonts(t))
		if err != nil {
			t.Fatal(err)
		}
		d := ds[0]
		switch kind {
		case "overlap":
			d.Edges[0].Times = append(d.Edges[0].Times, d.Edges[0].Times[0])
		case "highlight":
			d.Nodes[0].Times = nil
		case "rest":
			d.Edges[0].Times = []float64{d.Duration - 1}
		case "duration":
			d.Duration++
		}
		if err := validateMotion(d); err == nil {
			t.Fatalf("accepted %s defect", kind)
		}
	}
}

func TestSeedUsesUniformCirclesAndStraightRelations(t *testing.T) {
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	d := ds[2]
	for _, n := range d.Nodes {
		if n.Box.W != 92 || n.Box.H != 92 {
			t.Errorf("%s does not use the shared measured circle size", n.ID)
		}
		column := (n.Box.X + n.Box.W/2 - 78) / 136
		if column != float64(int(column)) {
			t.Errorf("%s is off the shared column grid", n.ID)
		}
	}
	for _, e := range d.Edges {
		if len(e.Points) != 2 {
			t.Errorf("%s has an unnecessary elbow", e.ID)
		}
	}
}

func TestRequestDetailsStayInsideCards(t *testing.T) {
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	d := ds[0]
	if len(d.Labels) != 0 {
		t.Error("request details must not use footer labels")
	}
	for _, n := range d.Nodes {
		if n.ID == "provider" {
			for _, option := range []string{"Amazon Bedrock (default)", "OpenAI", "Anthropic", "Google", "Ollama", "OpenAI-compatible"} {
				if !strings.Contains(strings.Join(n.Lines, " "), option) {
					t.Errorf("provider option %q is missing from its card", option)
				}
			}
		}
		if n.ID == "topics" {
			text := strings.Join(strings.Fields(strings.Join(n.Lines, "")), "")
			for _, route := range []string{"/api/v1", "GET/topics", "GET/topics/:topic/path", "GET/tutor/student/:student_id/cognitive-graph"} {
				if !strings.Contains(text, route) {
					t.Errorf("topic route %s is missing from its card", route)
				}
			}
		}
	}
}

func TestCardsCanGrowForDetails(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	d := diagram{W: 400, H: 300, Nodes: []node{{ID: "routes", Title: "Topic handlers", Lines: []string{"Base /api/v1", "GET /topics", "GET /topics/:topic/path"}, Box: box{40, 100, 184, 96}}}}
	if err := validate(d, f); err != nil {
		t.Fatal(err)
	}
}

func TestPackageCardsUseFourColumnLayers(t *testing.T) {
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	d := ds[1]
	for _, n := range d.Nodes {
		column := (n.Box.X - 16) / 208
		if column != float64(int(column)) || column < 0 || column > 3 {
			t.Errorf("%s is off the four-column grid", n.ID)
		}
	}
	if len(d.Labels) != 1 || d.Labels[0].Kind != "muted" {
		t.Error("missing reduced-import footnote")
	}
}

func TestSeedCircleRadiusIsMinimal(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, radius := range []float64{45, 46} {
		allFit := true
		for _, original := range ds[2].Nodes {
			n := original
			n.Box = box{0, 0, 2 * radius, 2 * radius}
			labels, err := sceneLabels(diagram{Nodes: []node{n}}, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range labels {
				if l.Owner != n.ID {
					continue
				}
				b, err := f.measure(l)
				if err != nil {
					t.Fatal(err)
				}
				allFit = allFit && textFitsNode(n, b)
			}
		}
		if allFit != (radius == 46) {
			t.Errorf("radius %g fit=%v", radius, allFit)
		}
	}
}

func TestRejectSceneGridDefects(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"unequal circles", "oversized circles", "seed column", "bent relation", "package column", "package detour"} {
		ds, err := buildDiagrams("../..", testFonts(t))
		if err != nil {
			t.Fatal(err)
		}
		d := ds[2]
		want := "uniform measured circles"
		switch kind {
		case "unequal circles":
			d.Nodes[0].Box = grow(d.Nodes[0].Box, -1)
		case "oversized circles":
			for i := range d.Nodes {
				d.Nodes[i].Box = grow(d.Nodes[i].Box, 1)
			}
		case "seed column":
			d.Nodes[0].Box.X++
			want = "column grid"
		case "bent relation":
			a, b := d.Edges[0].Points[0], d.Edges[0].Points[1]
			d.Edges[0].Points = []point{a, {a[0], a[1] + 60}, {b[0], b[1] + 60}, b}
			want = "straight route"
		case "package column":
			d = ds[1]
			d.Nodes[0].Box.X++
			want = "column grid"
		case "package detour":
			d = ds[1]
			d.Edges[0].Points = []point{{260, 144}, {260, 164}, {824, 164}, {824, 184}, {108, 184}}
			want = "endpoint span"
		}
		if err := validate(d, f); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q; got %v", kind, want, err)
		}
	}
}

func TestVisibleShaftThresholds(t *testing.T) {
	cases := []struct {
		name                    string
		gap                     float64
		dashed, circles, accept bool
	}{
		{"card shaft 9", 22, false, false, false},
		{"card shaft 10", 23, false, false, true},
		{"approved card shaft 11", 24, false, false, true},
		{"dashed shaft 23", 36, true, false, false},
		{"dashed shaft 24", 37, true, false, true},
		{"circle shaft 23", 36, false, true, false},
		{"circle shaft 24", 37, false, true, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			width, height := 184.0, 56.0
			if tt.circles {
				width, height = 92, 92
			}
			source := node{ID: "source", Title: "Source", Circle: tt.circles, Box: box{40, 100, width, height}}
			target := node{ID: "target", Title: "Target", Circle: tt.circles, Box: box{40 + width + tt.gap, 100, width, height}}
			e := edge{ID: "e", From: "source", To: "target", Points: []point{{40 + width, 100 + height/2}, {40 + width + tt.gap, 100 + height/2}}}
			if tt.dashed {
				e.Relation = "is_a"
			}
			err := validateShaft(e, source, target)
			if tt.accept {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "visible shaft") {
				t.Fatalf("short shaft accepted or wrong error: %v", err)
			}
		})
	}
}

func TestSeedVerticalGapMinimum(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	for _, gap := range []float64{39, 40} {
		d := diagram{Name: "seed-graph", W: 840, H: 400, Nodes: []node{{ID: "a", Title: "Polymorphism", Circle: true, Box: box{576, 104, 92, 92}}, {ID: "b", Title: "Design Patterns", Circle: true, Box: box{576, 196 + gap, 92, 92}}}, Edges: []edge{{ID: "a/b", From: "a", To: "b", Points: []point{{622, 196}, {622, 196 + gap}}}}}
		if gap == 39 {
			withoutEdge := d
			withoutEdge.Edges = nil
			if err := validate(withoutEdge, f); err == nil || !strings.Contains(err.Error(), "vertical circle gap") {
				t.Errorf("unconnected circles may not crowd a column: %v", err)
			}
		}
		err := validate(d, f)
		if gap == 40 {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "vertical circle gap") {
			t.Fatalf("39 px gap accepted or wrong error: %v", err)
		}
	}
}

func TestSeedHeightLimitAllows920(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(diagram{Name: "seed-graph", W: 840, H: 920}, f); err != nil {
		t.Fatal(err)
	}
}

func TestSeedVerticalLinksMatchHorizontalSpacing(t *testing.T) {
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ds[2].Edges {
		if len(e.Points) == 2 && e.Points[0][0] == e.Points[1][0] && edgeLength(e) != 44 {
			t.Errorf("%s: vertical gap %g, want 44", e.ID, edgeLength(e))
		}
	}
}

func TestRoundedDashedShaftUsesRenderedLength(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	for _, targetY := range []float64{117, 120} {
		d := diagram{W: 500, H: 300, Nodes: []node{{ID: "target", Title: "Target", Box: box{128, targetY, 184, 56}}}, Edges: []edge{{ID: "dashed", To: "target", Relation: "is_a", Points: []point{{200, 100}, {220, 100}, {220, targetY}}}}}
		err := validate(d, f)
		if targetY == 120 {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "visible shaft") {
			t.Fatalf("short rounded shaft accepted or wrong error: %v", err)
		}
	}
}

func TestSeedLegendSharesSubtitleLine(t *testing.T) {
	f, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	ds, err := buildDiagrams("../..", testFonts(t))
	if err != nil {
		t.Fatal(err)
	}
	d := ds[2]
	for _, l := range d.Legend {
		if l.Y != 52 {
			t.Errorf("legend sample is not on the subtitle line")
		}
	}
	last := d.Legend[len(d.Legend)-1]
	b, err := f.measure(label{Text: last.Text, X: last.X + 48, Y: last.Y + 4, Size: 12})
	if err != nil {
		t.Fatal(err)
	}
	right := b.X + b.W
	if right < d.W-16-.001 || right > d.W-16+.001 {
		t.Errorf("legend right edge %g, want %g", right, d.W-16)
	}
}
