package main

import (
	"fmt"
	"math"
	"strings"
)

func intersects(a, b box) bool {
	return a.X < b.X+b.W && a.X+a.W > b.X && a.Y < b.Y+b.H && a.Y+a.H > b.Y
}
func grow(b box, n float64) box { return box{b.X - n, b.Y - n, b.W + 2*n, b.H + 2*n} }
func contains(a, b box) bool    { return b.X > a.X && b.Y > a.Y && b.X+b.W < a.X+a.W && b.Y+b.H < a.Y+a.H }
func segmentHits(a, b point, r box) bool {
	if a[0] == b[0] {
		return a[0] >= r.X && a[0] <= r.X+r.W && math.Max(a[1], b[1]) >= r.Y && math.Min(a[1], b[1]) <= r.Y+r.H
	}
	return a[1] >= r.Y && a[1] <= r.Y+r.H && math.Max(a[0], b[0]) >= r.X && math.Min(a[0], b[0]) <= r.X+r.W
}
func segmentsCross(a, b, c, d point) bool {
	if a[0] == b[0] && c[0] == d[0] {
		return a[0] == c[0] && math.Max(math.Min(a[1], b[1]), math.Min(c[1], d[1])) <= math.Min(math.Max(a[1], b[1]), math.Max(c[1], d[1]))
	}
	if a[1] == b[1] && c[1] == d[1] {
		return a[1] == c[1] && math.Max(math.Min(a[0], b[0]), math.Min(c[0], d[0])) <= math.Min(math.Max(a[0], b[0]), math.Max(c[0], d[0]))
	}
	if a[1] == b[1] {
		a, b, c, d = c, d, a, b
	}
	return a[0] >= math.Min(c[0], d[0]) && a[0] <= math.Max(c[0], d[0]) && c[1] >= math.Min(a[1], b[1]) && c[1] <= math.Max(a[1], b[1])
}
func onBorder(p point, b box) bool {
	return (p[0] == b.X || p[0] == b.X+b.W) && p[1] >= b.Y && p[1] <= b.Y+b.H || (p[1] == b.Y || p[1] == b.Y+b.H) && p[0] >= b.X && p[0] <= b.X+b.W
}
func validate(d diagram, f *fontSet) error {
	var defects []string
	fail := func(format string, args ...any) { defects = append(defects, fmt.Sprintf(format, args...)) }
	if d.W > 840 || d.W < 200 {
		fail("viewBox width exceeds readability limit")
	}
	heightLimit := maxDiagramHeight
	if d.Name == "seed-graph" {
		heightLimit = maxSeedHeight
	}
	if d.H > heightLimit || d.H < 100 {
		fail("viewBox height exceeds readability limit")
	}
	if err := validateMotion(d); err != nil {
		fail("%v", err)
	}
	if err := validateSceneGrid(d, f); err != nil {
		fail("%v", err)
	}
	canvas := box{0, 0, d.W, d.H}
	nodes := map[string]node{}
	for i, n := range d.Nodes {
		if _, ok := nodes[n.ID]; ok {
			fail("duplicate node %s", n.ID)
		}
		nodes[n.ID] = n
		if !withinMargin(canvas, n.Box, 16) {
			fail("node %s outside viewBox", n.ID)
		}
		for _, other := range d.Nodes[:i] {
			a, b := n.Box, other.Box
			a = grow(a, nodeStroke/2)
			b = grow(b, nodeStroke/2)
			if intersects(a, b) {
				fail("nodes %s and %s overlap", n.ID, other.ID)
			}
			if !n.Circle && !other.Circle {
				dx := max(0, n.Box.X-other.Box.X-other.Box.W, other.Box.X-n.Box.X-n.Box.W)
				dy := max(0, n.Box.Y-other.Box.Y-other.Box.H, other.Box.Y-n.Box.Y-n.Box.H)
				if math.Hypot(dx, dy) < minCardGap {
					fail("nodes %s and %s card gap is below %g px", n.ID, other.ID, minCardGap)
				}
			}
		}
	}
	for i, g := range d.Groups {
		if !withinMargin(canvas, g.Box, 16) {
			fail("group %s violates viewBox margin", g.ID)
		}
		for _, other := range d.Groups[:i] {
			if intersects(grow(g.Box, groupStroke/2), grow(other.Box, groupStroke/2)) {
				fail("groups overlap")
			}
		}
	}
	for _, n := range d.Nodes {
		if n.Circle && (n.Box.W != n.Box.H || n.Box.W <= 0) {
			fail("node %s must be a circle", n.ID)
		}
		if !n.Circle && (n.Box.W != 184 || n.Box.H < 56 || math.Mod(n.Box.H-56, 20) != 0) {
			fail("node %s must be a 184 x 56 card or taller in 20 px steps", n.ID)
		}
		if n.Group != "" {
			found := false
			for _, g := range d.Groups {
				if g.ID == n.Group && withinMargin(g.Box, n.Box, groupPadding) {
					found = true
				}
			}
			if !found {
				fail("node %s is outside its group", n.ID)
			}
		}
	}

	for _, e := range d.Edges {
		if edgeLength(e) > maxEdgeLength {
			fail("edge %s exceeds %g units", e.ID, maxEdgeLength)
		}
		if len(e.Points) < 2 {
			fail("edge %s needs at least two points", e.ID)
			continue
		}
		if err := validateShaft(e, nodes[e.From], nodes[e.To]); err != nil {
			fail("%v", err)
		}

		for _, end := range []struct {
			id string
			p  point
		}{{e.From, e.Points[0]}, {e.To, e.Points[len(e.Points)-1]}} {
			if end.id != "" {
				n, ok := nodes[end.id]
				if !ok || !onNodeBorder(end.p, n) {
					fail("edge %s endpoint is not on %s border", e.ID, end.id)
				} else if !n.Circle {
					for _, corner := range []point{{n.Box.X, n.Box.Y}, {n.Box.X + n.Box.W, n.Box.Y}, {n.Box.X, n.Box.Y + n.Box.H}, {n.Box.X + n.Box.W, n.Box.Y + n.Box.H}} {
						if math.Hypot(end.p[0]-corner[0], end.p[1]-corner[1]) < nodeRadius+4 {
							fail("edge %s port is too close to node corner", e.ID)
						}
					}
				}
			}
		}
		if len(e.Points) >= 2 {
			for _, end := range []struct {
				id       string
				at, near point
			}{{e.From, e.Points[0], e.Points[1]}, {e.To, e.Points[len(e.Points)-1], e.Points[len(e.Points)-2]}} {
				n, ok := nodes[end.id]
				if ok && !outward(end.at, end.near, n) {
					fail("edge %s has inward or tangent attachment", e.ID)
				}
			}
			if e.To != "" && math.Hypot(e.Points[len(e.Points)-1][0]-e.Points[len(e.Points)-2][0], e.Points[len(e.Points)-1][1]-e.Points[len(e.Points)-2][1]) < arrowGap+arrowLength {
				fail("edge %s arrowhead lacks target clearance", e.ID)
			}
		}

		for i := 1; i < len(e.Points); i++ {
			a, b := e.Points[i-1], e.Points[i]
			if a == b {
				fail("edge %s has zero-length segment", e.ID)
			}
			if i >= 2 {
				prev := e.Points[i-2]
				if prev[0] == a[0] && a[0] == b[0] || prev[1] == a[1] && a[1] == b[1] {
					fail("edge %s has collinear consecutive segments", e.ID)
				}
				for j := 1; j < i-1; j++ {
					if segmentsCross(a, b, e.Points[j-1], e.Points[j]) {
						fail("edge %s crosses itself", e.ID)
					}
				}
			}
			if a[0] != b[0] && a[1] != b[1] {
				fail("edge %s is not orthogonal", e.ID)
				continue
			}
			if !withinMargin(canvas, box{a[0], a[1], 0, 0}, 16) || !withinMargin(canvas, box{b[0], b[1], 0, 0}, 16) {
				fail("edge %s violates viewBox margin", e.ID)
			}
			for _, n := range d.Nodes {
				r := grow(n.Box, edgeClearance+edgeStroke/2)
				if n.ID == e.From || n.ID == e.To {
					r = grow(n.Box, -0.01)
				}
				if segmentHitsNode(a, b, n, r) {
					fail("edge %s violates clearance of node %s", e.ID, n.ID)
				}
			}
		}
	}
	labels, err := sceneLabels(d, f)
	if err != nil {
		fail("%v", err)
	}
	bounds := make([]box, len(labels))
	for i, l := range labels {
		if l.Size < 12 {
			fail("label %q is below 12 px at natural width", l.Text)
		}
		b, err := f.measure(l)
		if err != nil {
			fail("%s: %v", l.Text, err)
			continue
		}
		bounds[i] = b
		if !withinMargin(canvas, b, 16) {
			fail("label %q violates viewBox margin", l.Text)
		}
		for _, n := range d.Nodes {
			if n.ID == l.Owner {
				if !textFitsNode(n, b) {
					fail("text %q overflows or touches its node border", l.Text)
				}
			} else if intersects(grow(b, 2), grow(n.Box, nodeStroke/2)) {
				fail("label %q overlaps node %s", l.Text, n.ID)
			}
		}
		for j := 0; j < i; j++ {
			if intersects(grow(b, labelPadding(l, labels[j])), grow(bounds[j], labelPadding(l, labels[j]))) {
				fail("labels %q and %q overlap", l.Text, labels[j].Text)
			}
		}
		for _, e := range d.Edges {
			points := drawingPoints(e)
			for j := 1; j < len(points); j++ {
				if segmentHits(points[j-1], points[j], grow(b, edgeStroke/2+3)) {
					fail("label %q overlaps edge %s", l.Text, e.ID)
				}
			}
		}
		for _, e := range d.Edges {
			if len(e.Points) < 2 {
				continue
			}
			points := drawingPoints(e)
			tip := points[len(points)-1]
			prev := points[len(points)-2]
			length := math.Hypot(tip[0]-prev[0], tip[1]-prev[1])
			if length > 0 {
				base := point{tip[0] + (prev[0]-tip[0])*math.Min(8, length)/length, tip[1] + (prev[1]-tip[1])*math.Min(8, length)/length}
				if segmentHits(base, tip, grow(b, 3.5+3)) {
					fail("label %q overlaps edge %s arrowhead", l.Text, e.ID)
				}
			}
		}
		for _, g := range d.Groups {
			if intersects(b, g.Box) && !contains(grow(g.Box, -groupStroke/2-2), b) {
				fail("label %q touches a group border", l.Text)
			}
		}
	}

	ports := map[point]string{}
	edgeIDs := map[string]bool{}
	for _, e := range d.Edges {
		if edgeIDs[e.ID] {
			fail("duplicate edge %s", e.ID)
		}
		edgeIDs[e.ID] = true
		if len(e.Points) < 2 {
			continue
		}
		for _, port := range []struct {
			id string
			p  point
		}{{e.From, e.Points[0]}, {e.To, e.Points[len(e.Points)-1]}} {
			if port.id == "" {
				continue
			}
			if previous, ok := ports[port.p]; ok {
				fail("edges %s and %s share a node port", previous, e.ID)
			}
			ports[port.p] = e.ID
		}
		for _, g := range d.Groups {
			inside := contains(g.Box, box{e.Points[0][0], e.Points[0][1], 0, 0}) || contains(g.Box, box{e.Points[len(e.Points)-1][0], e.Points[len(e.Points)-1][1], 0, 0})
			if inside {
				for _, p := range e.Points {
					if !contains(g.Box, box{p[0], p[1], 0, 0}) {
						fail("edge %s leaves its group %s", e.ID, g.Title)
					}
				}
				continue
			}
			for j := 1; j < len(e.Points); j++ {
				if segmentHits(e.Points[j-1], e.Points[j], g.Box) {
					fail("edge %s crosses unrelated group %s", e.ID, g.Title)
				}
			}
		}
	}
	for _, n := range d.Nodes {
		for _, g := range d.Groups {
			if contains(g.Box, n.Box) && n.Group != g.ID {
				fail("node %s category differs from group %s", n.ID, g.Title)
			}
		}
	}
	for i, e := range d.Edges {
		for _, other := range d.Edges[:i] {
			for a := 1; a < len(e.Points); a++ {
				for b := 1; b < len(other.Points); b++ {
					u, v, w, z := e.Points[a-1], e.Points[a], other.Points[b-1], other.Points[b]
					if segmentsCross(u, v, w, z) {
						fail("edges %s and %s cross or overlap", e.ID, other.ID)
						continue
					}
					vertical := u[0] == v[0] && w[0] == z[0]
					horizontal := u[1] == v[1] && w[1] == z[1]
					if vertical && u[0] == w[0] || horizontal && u[1] == w[1] {
						gap := math.Max(math.Min(u[0], v[0]), math.Min(w[0], z[0])) - math.Min(math.Max(u[0], v[0]), math.Max(w[0], z[0]))
						if vertical {
							gap = math.Max(math.Min(u[1], v[1]), math.Min(w[1], z[1])) - math.Min(math.Max(u[1], v[1]), math.Max(w[1], z[1]))
						}
						if gap < parallelClearance {
							fail("edges %s and %s are collinear with gap under 12", e.ID, other.ID)
						}
					}
					tooClose := vertical && math.Abs(u[0]-w[0]) < parallelClearance && math.Max(math.Min(u[1], v[1]), math.Min(w[1], z[1])) <= math.Min(math.Max(u[1], v[1]), math.Max(w[1], z[1])) || horizontal && math.Abs(u[1]-w[1]) < parallelClearance && math.Max(math.Min(u[0], v[0]), math.Min(w[0], z[0])) <= math.Min(math.Max(u[0], v[0]), math.Max(w[0], z[0]))
					if tooClose {
						fail("parallel edges %s and %s are closer than 12 px", e.ID, other.ID)
					}
				}
			}
		}
	}

	if len(defects) > 0 {
		return fmt.Errorf("%s layout:\n%s", d.Name, strings.Join(defects, "\n"))
	}
	return nil
}

func withinMargin(canvas, b box, margin float64) bool {
	return b.X >= canvas.X+margin && b.Y >= canvas.Y+margin && b.X+b.W <= canvas.X+canvas.W-margin && b.Y+b.H <= canvas.Y+canvas.H-margin
}

func labelPadding(a, b label) float64 {
	if a.Owner != "" && a.Owner == b.Owner {
		return 1
	}
	return 3
}
func onNodeBorder(p point, n node) bool {
	if !n.Circle {
		return onBorder(p, n.Box)
	}
	x, y, r := n.Box.X+n.Box.W/2, n.Box.Y+n.Box.H/2, n.Box.W/2
	return p == point{x - r, y} || p == point{x + r, y} || p == point{x, y - r} || p == point{x, y + r}
}
func outward(at, near point, n node) bool {
	b := n.Box
	return at[0] == b.X && near[0] < at[0] && near[1] == at[1] || at[0] == b.X+b.W && near[0] > at[0] && near[1] == at[1] || at[1] == b.Y && near[1] < at[1] && near[0] == at[0] || at[1] == b.Y+b.H && near[1] > at[1] && near[0] == at[0]
}
func textFitsNode(n node, b box) bool {
	if !n.Circle {
		return contains(grow(n.Box, -nodeStroke/2-3), b)
	}
	x, y, r := n.Box.X+n.Box.W/2, n.Box.Y+n.Box.H/2, n.Box.W/2-circleTextPadding
	for _, p := range []point{{b.X, b.Y}, {b.X + b.W, b.Y}, {b.X, b.Y + b.H}, {b.X + b.W, b.Y + b.H}} {
		if math.Hypot(p[0]-x, p[1]-y) >= r {
			return false
		}
	}
	return true
}
func segmentHitsNode(a, b point, n node, r box) bool {
	if !n.Circle {
		return segmentHits(a, b, r)
	}
	cx, cy := n.Box.X+n.Box.W/2, n.Box.Y+n.Box.H/2
	radius := r.W / 2
	x := math.Max(math.Min(a[0], b[0]), math.Min(cx, math.Max(a[0], b[0])))
	y := math.Max(math.Min(a[1], b[1]), math.Min(cy, math.Max(a[1], b[1])))
	return math.Hypot(x-cx, y-cy) <= radius
}

func validateShaft(e edge, from, to node) error {
	minimum := minCardShaft
	if e.Relation == "is_a" || from.Circle && to.Circle {
		minimum = minRelationShaft
	}
	if shaft := visibleShaft(e); shaft < minimum {
		return fmt.Errorf("edge %s visible shaft %.3f is below %g px", e.ID, shaft, minimum)
	}
	return nil
}
