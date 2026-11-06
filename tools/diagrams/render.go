package main

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
)

type palette struct {
	surface, stroke, edge, group, text, muted, accent, soft string
	rings                                                   map[string]string
}

func colors(theme string) (palette, error) {
	if theme == "light" {
		return palette{"#f6f8fa", "#d0d7de", "#8c959f", "#d8dee4", "#1f2328", "#59636e", "#0969da", "#d4e8ff", map[string]string{"object-oriented": "#8250df", "algorithms": "#bc4c00", "linear and hashing": "#1b7c83", "graphs and trees": "#1a7f37"}}, nil
	}
	if theme == "dark" {
		return palette{"#161b22", "#30363d", "#6e7681", "#30363d", "#e6edf3", "#9198a1", "#4493f8", "#1c3a5e", map[string]string{"object-oriented": "#ab7df8", "algorithms": "#db6d28", "linear and hashing": "#39c5cf", "graphs and trees": "#3fb950"}}, nil
	}
	return palette{}, fmt.Errorf("unknown theme %q", theme)
}

func number(x float64) string { return strconv.FormatFloat(math.Round(x*100000)/100000, 'f', -1, 64) }

func pathData(points []point) string {
	var s strings.Builder
	for i, p := range points {
		if i == 0 {
			s.WriteByte('M')
		} else {
			s.WriteByte('L')
		}
		fmt.Fprintf(&s, "%s %s", number(p.X), number(p.Y))
	}
	return s.String()
}

func shape(n node, class, extra string) string {
	if n.R > 0 {
		return fmt.Sprintf("<circle class=\"%s\" cx=\"%s\" cy=\"%s\" r=\"%s\" %s/>\n", class, number(n.X+n.R), number(n.Y+n.R), number(n.R), extra)
	}
	return fmt.Sprintf("<rect class=\"%s\" x=\"%s\" y=\"%s\" width=\"%s\" height=\"%s\" rx=\"10\" %s/>\n", class, number(n.X), number(n.Y), number(n.W), number(n.H), extra)
}

func render(d *diagram, theme string) ([]byte, error) {
	p, err := colors(theme)
	if err != nil {
		return nil, err
	}
	defs, texts, err := outlineText(allLabels(d))
	if err != nil {
		return nil, err
	}
	var s strings.Builder
	fmt.Fprintf(&s, "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 %s %s\" role=\"img\" aria-labelledby=\"title description\">\n<title id=\"title\">%s</title>\n<desc id=\"description\">%s</desc>\n", number(d.Width), number(d.Height), html.EscapeString(d.Title), html.EscapeString(d.Description))
	s.WriteString("<defs>\n" + defs + "<marker id=\"arrow\" viewBox=\"0 0 8 7\" refX=\"8\" refY=\"3.5\" markerWidth=\"8\" markerHeight=\"7\" markerUnits=\"userSpaceOnUse\" orient=\"auto\"><path d=\"M0 0L8 3.5L0 7Z\" fill=\"" + p.edge + "\"/></marker>\n</defs>\n")
	fmt.Fprintf(&s, "<style>\n.node{fill:%s;stroke:%s;stroke-width:1.25}.edge{fill:none;stroke:%s;stroke-width:1.5;marker-end:url(#arrow)}.group{fill:none;stroke:%s;stroke-width:1;stroke-dasharray:4 4}.text{fill:%s}.muted{fill:%s}.pulse{fill:none;stroke:%s;stroke-width:2.5;stroke-linecap:round;opacity:0;animation:pulse 8s cubic-bezier(0.4,0,0.2,1) infinite}.highlight{fill:%s;stroke:%s;stroke-width:1.25;opacity:0;animation:highlight 8s cubic-bezier(0.4,0,0.2,1) infinite}@keyframes pulse{0%%{stroke-dashoffset:18;opacity:0}0.01%%{opacity:1}7.49%%{opacity:1}7.5%%,100%%{stroke-dashoffset:var(--travel);opacity:0}}@keyframes highlight{0%%,7.5%%,100%%{opacity:0}2.5%%,5%%{opacity:1}}@media (prefers-reduced-motion:reduce){.pulse,.highlight{animation:none;opacity:0}}\n</style>\n", p.surface, p.stroke, p.edge, p.group, p.text, p.muted, p.accent, p.soft, p.accent)
	for _, sample := range d.Legend {
		if hue, ok := p.rings[sample.Kind]; ok {
			fmt.Fprintf(&s, "<circle class=\"legend-sample\" cx=\"%s\" cy=\"%s\" r=\"6\" fill=\"%s\" stroke=\"%s\" stroke-width=\"2\"/>\n", number(sample.X+20), number(sample.Y), p.surface, hue)
		} else {
			dash := ""
			if sample.Kind == "is_a" {
				dash = " stroke-dasharray=\"5 4\""
			} else if sample.Kind != "depends_on" {
				return nil, fmt.Errorf("unknown legend kind %s", sample.Kind)
			}
			fmt.Fprintf(&s, "<path class=\"legend-sample\" d=\"M%s %sL%s %s\" fill=\"none\" stroke=\"%s\" stroke-width=\"1.5\" marker-end=\"url(#arrow)\"%s/>\n", number(sample.X), number(sample.Y), number(sample.X+36), number(sample.Y), p.edge, dash)
		}
	}
	for _, g := range d.Groups {
		fmt.Fprintf(&s, "<rect class=\"group\" x=\"%s\" y=\"%s\" width=\"%s\" height=\"%s\" rx=\"14\"/>\n", number(g.X), number(g.Y), number(g.W), number(g.H))
	}
	edges := map[string]edge{}
	nodes := map[string]node{}
	for _, e := range d.Edges {
		edges[e.ID] = e
		extra := ""
		if e.Relation == "is_a" {
			extra = " stroke-dasharray=\"5 4\""
		}
		fmt.Fprintf(&s, "<path class=\"edge\" d=\"%s\"%s/>\n", pathData(e.Points), extra)
	}
	for _, n := range d.Nodes {
		nodes[n.ID] = n
		extra := ""
		if n.Category != "" {
			hue, ok := p.rings[n.Category]
			if !ok {
				return nil, fmt.Errorf("unknown category %s", n.Category)
			}
			extra = "style=\"stroke:" + hue + ";stroke-width:2\""
		}
		s.WriteString(shape(n, "node", extra))
	}
	for _, c := range d.Motion {
		delay := "animation-delay:" + number(c.Start) + "s"
		if c.Node != "" {
			s.WriteString(shape(nodes[c.Node], "highlight", "style=\""+delay+"\""))
			continue
		}
		e := edges[c.Edge]
		points := append([]point(nil), e.Points...)
		if c.Reverse {
			for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
				points[i], points[j] = points[j], points[i]
			}
		}
		length := 0.0
		for i := 1; i < len(points); i++ {
			length += hypot(points[i].X-points[i-1].X, points[i].Y-points[i-1].Y)
		}
		fmt.Fprintf(&s, "<path class=\"pulse\" d=\"%s\" stroke-dasharray=\"18 %s\" style=\"%s;--travel:%s\"/>\n", pathData(points), number(length+18), delay, number(-length))
	}
	s.WriteString(texts)
	s.WriteString("</svg>\n")
	data := []byte(s.String())
	if len(data) >= 80000 {
		return nil, fmt.Errorf("SVG exceeds 80 KB: %d bytes", len(data))
	}
	return data, nil
}
