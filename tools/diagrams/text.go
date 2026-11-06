package main

import (
	"embed"
	"errors"
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

//go:embed fonts/*.ttf
var fontFiles embed.FS
var fontsOnce sync.Once
var fonts [2]*sfnt.Font
var fontsErr error

func loadFonts() ([2]*sfnt.Font, error) {
	fontsOnce.Do(func() {
		for i, name := range []string{"Inter-Regular.ttf", "Inter-SemiBold.ttf"} {
			data, err := fontFiles.ReadFile("fonts/" + name)
			if err != nil {
				fontsErr = err
				return
			}
			fonts[i], err = sfnt.Parse(data)
			if err != nil {
				fontsErr = err
				return
			}
		}
	})
	return fonts, fontsErr
}

func textStyle(style string) (size float64, weight int, spacing float64) {
	switch style {
	case "title":
		return 14, 1, 0
	case "group":
		return 12, 1, .72
	case "edge":
		return 11.5, 0, 0
	default:
		return 12, 0, 0
	}
}

type placedGlyph struct {
	id     string
	x      float64
	path   string
	bounds box
}

func glyphs(l label) ([]placedGlyph, float64, error) {
	fs, err := loadFonts()
	if err != nil {
		return nil, 0, err
	}
	size, weight, spacing := textStyle(l.Style)
	f := fs[weight]
	var buf sfnt.Buffer
	var out []placedGlyph
	var previous sfnt.GlyphIndex
	x := 0.0
	for i, r := range l.Text {
		idx, err := f.GlyphIndex(&buf, r)
		if err != nil {
			return nil, 0, err
		}
		if idx == 0 {
			return nil, 0, fmt.Errorf("font lacks %q", r)
		}
		if i > 0 {
			k, err := f.Kern(&buf, previous, idx, fixed.I(64), font.HintingNone)
			if err != nil && !errors.Is(err, sfnt.ErrNotFound) {
				return nil, 0, err
			}
			x += float64(k)/64*size/64 + spacing
		}
		segments, err := f.LoadGlyph(&buf, idx, fixed.I(64), nil)
		if err != nil {
			return nil, 0, err
		}
		var p strings.Builder
		for _, s := range segments {
			switch s.Op {
			case sfnt.SegmentOpMoveTo:
				p.WriteByte('M')
			case sfnt.SegmentOpLineTo:
				p.WriteByte('L')
			case sfnt.SegmentOpQuadTo:
				p.WriteByte('Q')
			case sfnt.SegmentOpCubeTo:
				p.WriteByte('C')
			}
			count := 1
			if s.Op == sfnt.SegmentOpQuadTo {
				count = 2
			}
			if s.Op == sfnt.SegmentOpCubeTo {
				count = 3
			}
			for j := 0; j < count; j++ {
				fmt.Fprintf(&p, "%s %s ", number(float64(s.Args[j].X)/64), number(float64(s.Args[j].Y)/64))
			}
		}
		bounds := segments.Bounds()
		scale := size / 64
		b := box{x + float64(bounds.Min.X)/64*scale, float64(bounds.Min.Y) / 64 * scale, float64(bounds.Max.X-bounds.Min.X) / 64 * scale, float64(bounds.Max.Y-bounds.Min.Y) / 64 * scale}
		out = append(out, placedGlyph{id: fmt.Sprintf("g%d-%d", weight, idx), x: x, path: strings.TrimSpace(p.String()), bounds: b})
		advance, err := f.GlyphAdvance(&buf, idx, fixed.I(64), font.HintingNone)
		if err != nil {
			return nil, 0, err
		}
		x += float64(advance) / 64 * scale
		previous = idx
	}
	return out, x, nil
}

func labelBox(l label) (box, error) {
	gs, width, err := glyphs(l)
	if err != nil {
		return box{}, err
	}
	x := l.X
	if l.Owner != "" {
		x -= width / 2
	}
	minX, minY, maxX, maxY := 0.0, 0.0, width, 0.0
	for _, g := range gs {
		minX = math.Min(minX, g.bounds.x)
		minY = math.Min(minY, g.bounds.y)
		maxX = math.Max(maxX, g.bounds.x+g.bounds.w)
		maxY = math.Max(maxY, g.bounds.y+g.bounds.h)
	}
	return box{x + minX, l.Y + minY, maxX - minX, maxY - minY}, nil
}

func outlineText(labels []label) (string, string, error) {
	paths := map[string]string{}
	var body strings.Builder
	for _, l := range labels {
		gs, width, err := glyphs(l)
		if err != nil {
			return "", "", err
		}
		x := l.X
		if l.Owner != "" {
			x -= width / 2
		}
		size, _, _ := textStyle(l.Style)
		class := "text"
		if l.Style == "sublabel" || l.Style == "edge" {
			class = "muted"
		}
		fmt.Fprintf(&body, "<g class=\"%s\" aria-label=\"%s\" transform=\"translate(%s %s) scale(%s)\">", class, html.EscapeString(l.Text), number(x), number(l.Y), number(size/64))
		for _, g := range gs {
			paths[g.id] = g.path
			if g.path != "" {
				fmt.Fprintf(&body, "<use href=\"#%s\" x=\"%s\"/>", g.id, number(g.x*64/size))
			}
		}
		body.WriteString("</g>\n")
	}
	ids := make([]string, 0, len(paths))
	for id := range paths {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var defs strings.Builder
	for _, id := range ids {
		if paths[id] != "" {
			fmt.Fprintf(&defs, "<path id=\"%s\" d=\"%s\"/>\n", id, paths[id])
		}
	}
	return defs.String(), body.String(), nil
}
