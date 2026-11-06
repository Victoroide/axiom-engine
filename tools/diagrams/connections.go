package main

import (
	"fmt"
	"math"
)

func cross(a, b point) float64 { return a.X*b.Y - a.Y*b.X }

func subtract(a, b point) point { return point{a.X - b.X, a.Y - b.Y} }

func parallel(a, b, c, d point) bool {
	return math.Abs(cross(subtract(b, a), subtract(d, c))) < 1e-7*hypot(b.X-a.X, b.Y-a.Y)*hypot(d.X-c.X, d.Y-c.Y)
}

func segmentsCross(a, b, c, d point) bool {
	u, v := subtract(b, a), subtract(d, c)
	denom := cross(u, v)
	if math.Abs(denom) < 1e-9 {
		return false
	}
	t := cross(subtract(c, a), v) / denom
	s := cross(subtract(c, a), u) / denom
	return t >= -1e-8 && t <= 1+1e-8 && s >= -1e-8 && s <= 1+1e-8
}

func validateEdgeRelations(d *diagram, nodes map[string]node) error {
	for i, e := range d.Edges {
		for _, other := range d.Edges[:i] {
			for _, a := range []struct {
				id string
				p  point
			}{{e.From, e.Points[0]}, {e.To, e.Points[len(e.Points)-1]}} {
				for _, b := range []struct {
					id string
					p  point
				}{{other.From, other.Points[0]}, {other.To, other.Points[len(other.Points)-1]}} {
					if a.id == b.id && hypot(a.p.X-b.p.X, a.p.Y-b.p.Y) < .1 {
						return fmt.Errorf("shared attachment on %s: %s, %s", a.id, e.ID, other.ID)
					}
				}
			}
			crossed := false
			for j := 1; j < len(e.Points); j++ {
				a, b := e.Points[j-1], e.Points[j]
				for k := 1; k < len(other.Points); k++ {
					c, f := other.Points[k-1], other.Points[k]
					if parallel(a, b, c, f) {
						distance := math.Abs(cross(subtract(c, a), subtract(b, a))) / hypot(b.X-a.X, b.Y-a.Y)
						if distance < .01 && (!sharedGridLine(e, other, a, b, nodes) || !separateProjections(a, b, c, f)) {
							return fmt.Errorf("collinear or overlapping edges: %s, %s", e.ID, other.ID)
						}
						gap := math.Min(math.Min(segmentDistance(a, c, f), segmentDistance(b, c, f)), math.Min(segmentDistance(c, a, b), segmentDistance(f, a, b)))
						if gap < 12-1e-6 {
							return fmt.Errorf("parallel edge clearance %.2f: %s, %s", gap, e.ID, other.ID)
						}
					} else if segmentsCross(a, b, c, f) {
						crossed = true
					}
				}
			}
			if crossed {
				return fmt.Errorf("edges cross: %s, %s", e.ID, other.ID)
			}
		}
		for _, g := range d.Groups {
			boundary := box{g.X, g.Y, g.W, g.H}
			from, to := nodes[e.From], nodes[e.To]
			if boundary.contains(point{from.X + from.W/2, from.Y + from.H/2}) || boundary.contains(point{to.X + to.W/2, to.Y + to.H/2}) {
				continue
			}
			for j := 1; j < len(e.Points); j++ {
				if intersects(e.Points[j-1], e.Points[j], boundary) {
					return fmt.Errorf("edge %s crosses unrelated group boundary", e.ID)
				}
			}
		}
	}
	return nil
}

func sharedGridLine(e, other edge, a, b point, nodes map[string]node) bool {
	horizontal := math.Abs(a.Y-b.Y) < .01
	vertical := math.Abs(a.X-b.X) < .01
	if !horizontal && !vertical {
		return false
	}
	key := ""
	for _, id := range []string{e.From, e.To, other.From, other.To} {
		value := nodes[id].Column
		if horizontal {
			value = nodes[id].Row
		}
		if value == "" || key != "" && key != value {
			return false
		}
		key = value
	}
	return true
}

func separateProjections(a, b, c, d point) bool {
	if math.Abs(a.X-b.X) >= math.Abs(a.Y-b.Y) {
		return math.Max(math.Min(a.X, b.X), math.Min(c.X, d.X)) > math.Min(math.Max(a.X, b.X), math.Max(c.X, d.X))+.01
	}
	return math.Max(math.Min(a.Y, b.Y), math.Min(c.Y, d.Y)) > math.Min(math.Max(a.Y, b.Y), math.Max(c.Y, d.Y))+.01
}
