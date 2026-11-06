package main

import (
	"fmt"
	"math"
)

type box struct{ x, y, w, h float64 }

func hypot(x, y float64) float64 { return math.Hypot(x, y) }

func (b box) contains(p point) bool {
	return p.X >= b.x && p.Y >= b.y && p.X <= b.x+b.w && p.Y <= b.y+b.h
}

func (b box) overlaps(c box) bool {
	return b.x < c.x+c.w && c.x < b.x+b.w && b.y < c.y+c.h && c.y < b.y+b.h
}

func (b box) padded(p float64) box { return box{b.x - p, b.y - p, b.w + 2*p, b.h + 2*p} }

func nodeBox(n node) box { return box{n.X, n.Y, n.W, n.H} }

func borderDistance(n node, p point) float64 {
	if n.R > 0 {
		return hypot(p.X-n.X-n.R, p.Y-n.Y-n.R) - n.R
	}
	r := 10.0
	qx := math.Abs(p.X-n.X-n.W/2) - (n.W/2 - r)
	qy := math.Abs(p.Y-n.Y-n.H/2) - (n.H/2 - r)
	return hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

func segmentDistance(p, a, b point) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	if dx == 0 && dy == 0 {
		return hypot(p.X-a.X, p.Y-a.Y)
	}
	t := math.Max(0, math.Min(1, ((p.X-a.X)*dx+(p.Y-a.Y)*dy)/(dx*dx+dy*dy)))
	return hypot(p.X-a.X-t*dx, p.Y-a.Y-t*dy)
}

func intersects(a, b point, r box) bool {
	t0, t1 := 0.0, 1.0
	for _, v := range [][2]float64{{-(b.X - a.X), a.X - r.x}, {b.X - a.X, r.x + r.w - a.X}, {-(b.Y - a.Y), a.Y - r.y}, {b.Y - a.Y, r.y + r.h - a.Y}} {
		if v[0] == 0 {
			if v[1] < 0 {
				return false
			}
			continue
		}
		t := v[1] / v[0]
		if v[0] < 0 {
			t0 = math.Max(t0, t)
		} else {
			t1 = math.Min(t1, t)
		}
	}
	return t0 <= t1
}

func validate(d *diagram) error {
	height := 640.0
	if d.Name == "seed-graph" {
		height = 1000
	}
	if d.Width <= 0 || d.Height <= 0 || d.Width > 960 || d.Height > height {
		return fmt.Errorf("invalid viewBox")
	}
	canvas := box{0, 0, d.Width, d.Height}
	nodes := map[string]node{}
	for i, n := range d.Nodes {
		if n.ID == "" || n.W <= 0 || n.H <= 0 {
			return fmt.Errorf("invalid node %q", n.ID)
		}
		if _, exists := nodes[n.ID]; exists {
			return fmt.Errorf("duplicate node %s", n.ID)
		}
		nodes[n.ID] = n
		b := nodeBox(n).padded(1.25)
		if !canvas.contains(point{b.x, b.y}) || !canvas.contains(point{b.x + b.w, b.y + b.h}) {
			return fmt.Errorf("node %s leaves viewBox", n.ID)
		}
		for _, other := range d.Nodes[:i] {
			if nodeBox(n).overlaps(nodeBox(other)) {
				return fmt.Errorf("node overlap: %s, %s", n.ID, other.ID)
			}
		}
	}
	if err := validateAlignment(d); err != nil {
		return err
	}
	for _, g := range d.Groups {
		b := box{g.X, g.Y, g.W, g.H}
		if !canvas.contains(point{b.x - .5, b.y - .5}) || !canvas.contains(point{b.x + b.w + .5, b.y + b.h + .5}) {
			return fmt.Errorf("group leaves viewBox")
		}
	}
	for _, sample := range d.Legend {
		b := box{sample.X - 1, sample.Y - 4, 38, 8}
		if sample.Kind != "depends_on" && sample.Kind != "is_a" {
			b = box{sample.X + 13, sample.Y - 7, 14, 14}
		}
		if !canvas.contains(point{b.x, b.y}) || !canvas.contains(point{b.x + b.w, b.y + b.h}) {
			return fmt.Errorf("legend leaves viewBox")
		}
		for _, n := range d.Nodes {
			if b.overlaps(nodeBox(n)) {
				return fmt.Errorf("legend overlaps node %s", n.ID)
			}
		}
	}
	edgeIDs := map[string]bool{}
	for _, e := range d.Edges {
		if len(e.Points) < 2 {
			return fmt.Errorf("edge %s needs points", e.ID)
		}
		from, ok1 := nodes[e.From]
		to, ok2 := nodes[e.To]
		if !ok1 || !ok2 {
			return fmt.Errorf("edge %s has unknown endpoint", e.ID)
		}
		if math.Abs(borderDistance(from, e.Points[0])) > .1 || math.Abs(borderDistance(to, e.Points[len(e.Points)-1])) > .1 {
			return fmt.Errorf("edge %s misses node border", e.ID)
		}
		edgeIDs[e.ID] = true
		for i, p := range e.Points {
			if !canvas.padded(-4).contains(p) {
				return fmt.Errorf("edge %s leaves viewBox", e.ID)
			}
			if i == 0 {
				continue
			}
			a := e.Points[i-1]
			if hypot(p.X-a.X, p.Y-a.Y) < .1 {
				return fmt.Errorf("edge %s has duplicate points", e.ID)
			}
			for _, n := range d.Nodes {
				if n.ID == e.From || n.ID == e.To {
					continue
				}
				collision := intersects(a, p, nodeBox(n).padded(8.75))
				if n.R > 0 {
					collision = segmentDistance(point{n.X + n.R, n.Y + n.R}, a, p) < n.R+8.75
				}
				if collision {
					return fmt.Errorf("edge %s violates clearance at %s", e.ID, n.ID)
				}
			}
		}
	}
	if err := validateEdgeRelations(d, nodes); err != nil {
		return err
	}
	labels := allLabels(d)
	boxes := make([]box, 0, len(labels))
	for _, l := range labels {
		b, err := labelBox(l)
		if err != nil {
			return err
		}
		if !canvas.contains(point{b.x, b.y}) || !canvas.contains(point{b.x + b.w, b.y + b.h}) {
			return fmt.Errorf("label %q leaves viewBox", l.Text)
		}
		for _, n := range d.Nodes {
			if n.ID == l.Owner {
				if !nodeBox(n).padded(-4).contains(point{b.x, b.y}) || !nodeBox(n).padded(-4).contains(point{b.x + b.w, b.y + b.h}) {
					return fmt.Errorf("label %q leaves its node", l.Text)
				}
				continue
			}
			if b.overlaps(nodeBox(n)) {
				return fmt.Errorf("label %q overlaps node %s", l.Text, n.ID)
			}
		}
		for _, e := range d.Edges {
			for i := 1; i < len(e.Points); i++ {
				if intersects(e.Points[i-1], e.Points[i], b.padded(2)) {
					return fmt.Errorf("label %q overlaps edge %s", l.Text, e.ID)
				}
			}
		}
		for i, other := range boxes {
			if b.padded(1).overlaps(other) {
				return fmt.Errorf("label %q overlaps label %q", l.Text, labels[i].Text)
			}
		}
		boxes = append(boxes, b)
	}
	if len(d.Motion) > 40 {
		return fmt.Errorf("too many animated elements")
	}
	for _, c := range d.Motion {
		if c.Start < 0 || c.Start+.6 > 8 {
			return fmt.Errorf("motion leaves cycle")
		}
		if c.Node != "" {
			if _, ok := nodes[c.Node]; !ok {
				return fmt.Errorf("unknown highlight %s", c.Node)
			}
		} else if !edgeIDs[c.Edge] {
			return fmt.Errorf("unknown pulse %s", c.Edge)
		}
	}
	return nil
}
