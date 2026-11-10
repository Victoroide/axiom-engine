package main

import (
	"fmt"
	"math"
)

func onGrid(value, origin, step float64) bool {
	return math.Abs(value-origin-step*math.Round((value-origin)/step)) < .001
}

func minimumCircleRadius(nodes []node, f *fontSet) (float64, error) {
	for radius := 1.0; radius <= 128; radius++ {
		fits := true
		for _, original := range nodes {
			n := original
			n.Box = box{0, 0, 2 * radius, 2 * radius}
			labels, err := circleLabels(n, f)
			if err != nil {
				return 0, err
			}
			for _, l := range labels {
				bounds, err := f.measure(l)
				if err != nil {
					return 0, err
				}
				if !textFitsNode(n, bounds) {
					fits = false
					break
				}
			}
			if !fits {
				break
			}
		}
		if fits {
			return radius, nil
		}
	}
	return 0, fmt.Errorf("circle labels do not fit within the radius limit")
}

func validateSceneGrid(d diagram, f *fontSet) error {
	switch d.Name {
	case "seed-graph":
		radius, err := minimumCircleRadius(d.Nodes, f)
		if err != nil {
			return err
		}
		nodes := map[string]node{}
		for i, n := range d.Nodes {
			if !n.Circle || n.Box.W != 2*radius || n.Box.H != 2*radius {
				return fmt.Errorf("seed nodes must use uniform measured circles with radius %g", radius)
			}
			if !onGrid(n.Box.X+n.Box.W/2, 78, 136) {
				return fmt.Errorf("node %s leaves the shared column grid", n.ID)
			}
			for _, other := range d.Nodes[:i] {
				x, y := n.Box.X+n.Box.W/2, n.Box.Y+n.Box.H/2
				X, Y := other.Box.X+other.Box.W/2, other.Box.Y+other.Box.H/2
				if math.Abs(x-X) < .001 && math.Abs(y-Y)-(n.Box.H+other.Box.H)/2 < minCircleGap {
					return fmt.Errorf("nodes %s and %s vertical circle gap is below %g px", n.ID, other.ID, minCircleGap)
				}
			}

			nodes[n.ID] = n
		}
		for _, g := range d.Groups {
			left, top, right, bottom := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for _, n := range d.Nodes {
				if n.Group == g.ID {
					left = min(left, n.Box.X)
					top = min(top, n.Box.Y)
					right = max(right, n.Box.X+n.Box.W)
					bottom = max(bottom, n.Box.Y+n.Box.H)
				}
			}
			if left-g.Box.X != groupPadding || g.Box.X+g.Box.W-right != groupPadding || g.Box.Y+g.Box.H-bottom != groupPadding || top-g.Box.Y != groupTitleBand {
				return fmt.Errorf("group %s needs uniform %g px padding and a title band", g.ID, groupPadding)
			}
		}
		for _, e := range d.Edges {
			a, b := nodes[e.From], nodes[e.To]
			if a.ID == "" || b.ID == "" {
				continue
			}
			x, y := a.Box.X+a.Box.W/2, a.Box.Y+a.Box.H/2
			X, Y := b.Box.X+b.Box.W/2, b.Box.Y+b.Box.H/2
			if (x == X || y == Y) && len(e.Points) != 2 {
				return fmt.Errorf("aligned relation %s needs a straight route", e.ID)
			}
		}
	case "system-components":
		nodes := map[string]node{}
		for _, n := range d.Nodes {
			if !onGrid(n.Box.X, 16, 208) {
				return fmt.Errorf("package %s leaves the four-column grid", n.ID)
			}
			if !onGrid(n.Box.Y, 88, 96) {
				return fmt.Errorf("package %s leaves the layer grid", n.ID)
			}
			nodes[n.ID] = n
		}
		for _, e := range d.Edges {
			a, b := nodes[e.From], nodes[e.To]
			if a.ID == "" || b.ID == "" {
				continue
			}
			for _, p := range e.Points {
				if p[0] < math.Min(a.Box.X, b.Box.X) || p[0] > math.Max(a.Box.X+a.Box.W, b.Box.X+b.Box.W) || p[1] < math.Min(a.Box.Y, b.Box.Y) || p[1] > math.Max(a.Box.Y+a.Box.H, b.Box.Y+b.Box.H) {
					return fmt.Errorf("package route %s extends beyond its endpoint span", e.ID)
				}
				if p[0] <= 16 || p[0] >= d.W-16 {
					return fmt.Errorf("package route %s follows a canvas margin", e.ID)
				}
			}
		}
	}
	return nil
}
