package main

import (
	"cmp"
	"fmt"
	"math"
	"slices"
)

type color [3]float64

func rgb(hex string) (color, error) {
	var r, g, b uint8
	if len(hex) != 7 {
		return color{}, fmt.Errorf("color must be opaque: %s", hex)
	}
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return color{}, err
	}
	return color{float64(r) / 255, float64(g) / 255, float64(b) / 255}, nil
}
func luminance(c color) float64 {
	for i, v := range c {
		if v <= .04045 {
			c[i] = v / 12.92
		} else {
			c[i] = math.Pow((v+.055)/1.055, 2.4)
		}
	}
	return .2126*c[0] + .7152*c[1] + .0722*c[2]
}
func blend(a, b color, alpha float64) color {
	for i := range a {
		a[i] = a[i]*(1-alpha) + b[i]*alpha
	}
	return a
}
func contrast(a, b color) float64 {
	x, y := luminance(a), luminance(b)
	return (math.Max(x, y) + .05) / (math.Min(x, y) + .05)
}
func validatePalette(p palette) error {
	colors := map[string]color{}
	for _, hex := range []string{p.Page, p.Surface, p.Text, p.Muted, p.Accent, p.Soft, p.Border, p.Edge, p.Group} {
		c, err := rgb(hex)
		if err != nil {
			return err
		}
		colors[hex] = c
	}
	// Shafts, arrowheads and legend samples all use edge ink over the page.
	if contrast(colors[p.Edge], colors[p.Page]) < 3 {
		return fmt.Errorf("insufficient edge contrast: %s on %s", p.Edge, p.Page)
	}
	for _, fg := range []string{p.Text, p.Muted} {
		for _, bg := range []string{p.Page, p.Surface} {
			for step := 0; step <= 1000; step++ {
				alpha := float64(step) / 1000
				if contrast(blend(colors[fg], colors[p.Text], alpha), blend(colors[bg], colors[p.Soft], alpha)) < 4.5 {
					return fmt.Errorf("insufficient text contrast: %s on %s", fg, bg)
				}
			}
		}
	}
	if contrast(colors[p.Soft], colors[p.Surface]) < 1.15 {
		return fmt.Errorf("insufficient highlight contrast")
	}
	return nil
}

func validateMotion(d diagram) error {
	type trip struct{ start, end float64 }
	var trips []trip
	for _, e := range d.Edges {
		for _, at := range e.Times {
			end := at + pulseDuration(edgeLength(e))
			if at <= 0 || end+.001 > d.Duration-restSeconds {
				return fmt.Errorf("pulse %s violates loop seam or rest", e.ID)
			}
			trips = append(trips, trip{at, end})
			for _, id := range []string{e.From, e.To} {
				want := at
				if id == e.To {
					want = end + targetHighlightDelay
				}
				found := false
				for _, n := range d.Nodes {
					if n.ID == id {
						for _, hit := range n.Times {
							if math.Abs(hit-want) < .00001 {
								found = true
							}
						}
					}
				}
				if !found {
					return fmt.Errorf("pulse %s lacks matching node highlight", e.ID)
				}
			}
		}
	}
	slices.SortFunc(trips, func(a, b trip) int { return cmp.Compare(a.start, b.start) })
	for i := 1; i < len(trips); i++ {
		if trips[i].start-trips[i-1].end < stepPause-.00001 {
			return fmt.Errorf("pulses overlap or leave before target lights")
		}
	}
	for _, n := range d.Nodes {
		for _, at := range n.Times {
			if at <= 0 || at+highlightSpan > d.Duration-restSeconds {
				return fmt.Errorf("highlight %s violates loop seam or rest", n.ID)
			}
		}
	}
	if d.Duration > 0 && d.Duration != loopDuration(d) {
		return fmt.Errorf("loop duration differs from final highlight plus rest")
	}
	return nil
}
