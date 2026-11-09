package main

import (
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestStraightBoxConnectorsUseRowAndColumnCenters(t *testing.T) {
	for _, name := range []string{"request-path", "system-components"} {
		d, err := loadDiagram(filepath.Join("..", ".."), name)
		if err != nil {
			t.Fatal(err)
		}
		nodes := map[string]node{}
		for _, n := range d.Nodes {
			nodes[n.ID] = n
		}
		for _, e := range d.Edges {
			if len(e.Points) != 2 {
				continue
			}
			from, to := nodes[e.From], nodes[e.To]
			for _, p := range e.Points {
				if from.Row != "" && from.Row == to.Row && p.Y != from.Y+from.H/2 {
					t.Errorf("%s is off row center", e.ID)
				}
				if from.Column != "" && from.Column == to.Column && p.X != from.X+from.W/2 {
					t.Errorf("%s is off column center", e.ID)
				}
			}
		}
	}
	d, err := loadDiagram(filepath.Join("..", ".."), "request-path")
	if err != nil {
		t.Fatal(err)
	}
	left := d.Nodes[0].X
	for _, n := range d.Nodes {
		if n.ID == "baml" && n.X != left {
			t.Errorf("%s left edge %g differs from client %g", n.ID, n.X, left)
		}
	}
}

func TestSeedFooterAndRelationLegend(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "seed-graph")
	if err != nil {
		t.Fatal(err)
	}
	y := -1.0
	for _, n := range d.Nodes {
		switch n.ID {
		case "Arrays", "Hash Tables", "Binary Search Trees", "AVL Trees":
			if y < 0 {
				y = n.Y + n.R
			}
			if n.Y+n.R != y {
				t.Errorf("%s is off the bottom row", n.ID)
			}
		}
	}
	groups := map[string]group{}
	for _, g := range d.Groups {
		groups[g.Category] = g
	}
	linear := groups["linear and hashing"]
	algo := groups["algorithms"]
	graphs := groups["graphs and trees"]
	if linear.X+linear.W != algo.X+algo.W {
		t.Error("left section right edges differ")
	}
	if linear.Y+linear.H != graphs.Y+graphs.H {
		t.Error("bottom section edges differ")
	}
	found := false
	for _, l := range d.Labels {
		if l.Text == "LINEAR AND HASHING" {
			found = true
			if l.X != linear.X+10 || l.Y != linear.Y-10 {
				t.Error("section heading has inconsistent spacing")
			}
		}
		if l.Text == "is a" {
			t.Error("directional relation text remains on an edge")
		}
	}
	if !found {
		t.Error("missing section heading")
	}
	wording := map[string]string{"depends_on": "required before", "is_a": "has subtype"}
	for _, l := range d.Legend {
		if want, ok := wording[l.Kind]; ok && l.Text != want {
			t.Errorf("legend %s: got %q, want %q", l.Kind, l.Text, want)
		}
	}
}

func TestStudyHighlightHasVisibleFillContrast(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "seed-graph")
	if err != nil {
		t.Fatal(err)
	}
	luminance := func(hex string) float64 {
		v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		result := 0.0
		for i, weight := range []float64{.2126, .7152, .0722} {
			c := float64((v>>uint(16-i*8))&255) / 255
			if c <= .04045 {
				c /= 12.92
			} else {
				c = math.Pow((c+.055)/1.055, 2.4)
			}
			result += c * weight
		}
		return result
	}
	for _, tc := range []struct {
		theme   string
		minimum float64
	}{{"light", 1.15}, {"dark", 1.45}} {
		svg, err := render(d, tc.theme)
		if err != nil {
			t.Fatal(err)
		}
		node := regexp.MustCompile(`\.node\{fill:(#[0-9a-f]{6})`).FindSubmatch(svg)
		fill := regexp.MustCompile(`\.highlight\{fill:(#[0-9a-f]{6})`).FindSubmatch(svg)
		if len(node) != 2 || len(fill) != 2 {
			t.Fatal("missing node or highlight fill")
		}
		a, b := luminance(string(node[1])), luminance(string(fill[1]))
		ratio := (max(a, b) + .05) / (min(a, b) + .05)
		if ratio < tc.minimum {
			t.Errorf("%s highlight contrast %.4f is below %.2f", tc.theme, ratio, tc.minimum)
		}
	}
}

func TestArrayPrerequisiteLinkIsStraight(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "seed-graph")
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]node{}
	for _, n := range d.Nodes {
		nodes[n.ID] = n
	}
	x := nodes["Recursion"].X + nodes["Recursion"].R
	for _, id := range []string{"Arrays", "Binary Search"} {
		n := nodes[id]
		if n.X+n.R != x {
			t.Errorf("%s is not in the column below Recursion", id)
		}
	}
	if n := nodes["Hash Tables"]; n.X+n.R != nodes["Merge Sort"].X+nodes["Merge Sort"].R {
		t.Error("Hash Tables is not one column right of Arrays")
	}
	found := false
	for _, e := range d.Edges {
		if e.ID != "Arrays/Binary Search" {
			continue
		}
		found = true
		if len(e.Points) != 2 {
			t.Fatalf("prerequisite link has %d points, want a straight segment", len(e.Points))
		}
		for _, p := range e.Points {
			if p.X != x {
				t.Errorf("link leaves the shared column at x=%g", p.X)
			}
		}
		if e.Points[0].Y <= e.Points[1].Y {
			t.Error("prerequisite arrow does not point toward Binary Search")
		}
	}
	if !found {
		t.Fatal("missing prerequisite link")
	}
}
