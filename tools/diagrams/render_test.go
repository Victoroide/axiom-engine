package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedLegendShowsCategoriesAndRelations(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "seed-graph")
	if err != nil {
		t.Fatal(err)
	}
	data, err := render(d, "light")
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	count := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		for _, a := range el.Attr {
			if a.Name.Local == "class" && a.Value == "legend-sample" {
				count++
			}
		}
	}
	if count != 6 {
		t.Fatalf("want six visual legend samples, got %d", count)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "request-path")
	if err != nil {
		t.Fatal(err)
	}
	first, err := render(d, "light")
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		next, err := render(d, "light")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, next) {
			t.Fatal("nondeterministic output")
		}
	}
	if _, err := render(d, "invalid"); err == nil {
		t.Fatal("accepted unknown theme")
	}
	for _, property := range []string{"animation:pulse 8s cubic-bezier(0.4,0,0.2,1) infinite", "7.5%,100%{stroke-dashoffset", "2.5%,5%{opacity:1}"} {
		if !strings.Contains(string(first), property) {
			t.Errorf("missing animation contract %s", property)
		}
	}
}

func TestStudyHighlightsDoNotOverlap(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "seed-graph")
	if err != nil {
		t.Fatal(err)
	}
	end := 0.0
	for _, c := range d.Motion {
		if c.Node == "" {
			continue
		}
		if c.Start < end {
			t.Fatalf("highlight %s begins at %.2f before the prior highlight ends at %.2f", c.Node, c.Start, end)
		}
		end = c.Start + .6
	}
}

func TestSeedCirclesHaveUniformMeasuredClearance(t *testing.T) {
	d, err := loadDiagram(filepath.Join("..", ".."), "seed-graph")
	if err != nil {
		t.Fatal(err)
	}
	radius := d.Nodes[0].R
	closest := 1000.0
	for _, n := range d.Nodes {
		if n.R != radius {
			t.Errorf("unequal circle radius for %s", n.ID)
		}
		for _, l := range allLabels(&diagram{Nodes: []node{n}}) {
			b, err := labelBox(l)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []point{{b.x, b.y}, {b.x + b.w, b.y}, {b.x, b.y + b.h}, {b.x + b.w, b.y + b.h}} {
				clearance := radius - 1 - hypot(p.X-n.X-radius, p.Y-n.Y-radius)
				closest = min(closest, clearance)
				if clearance < 8 {
					t.Errorf("%s has %.2f px ring clearance", n.ID, clearance)
				}
			}
		}
	}
	if closest >= 9 {
		t.Errorf("radius %g can be reduced; closest clearance %.2f", radius, closest)
	}
	for i, n := range d.Nodes {
		for _, other := range d.Nodes[:i] {
			gap := hypot(n.X+n.R-other.X-other.R, n.Y+n.R-other.Y-other.R) - n.R - other.R - 2
			if gap < 24 {
				t.Errorf("%s and %s have %.2f px gap", n.ID, other.ID, gap)
			}
		}
	}
}
