// Package main generates the documentation diagrams from checked layouts.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type node struct {
	Row      string   `json:"row,omitempty"`
	Column   string   `json:"column,omitempty"`
	ID       string   `json:"id"`
	Title    []string `json:"title,omitempty"`
	Subtitle []string `json:"subtitle,omitempty"`
	X        float64  `json:"x"`
	Y        float64  `json:"y"`
	W        float64  `json:"w,omitempty"`
	H        float64  `json:"h,omitempty"`
	R        float64  `json:"r,omitempty"`
	Category string   `json:"category,omitempty"`
	Package  string   `json:"package,omitempty"`
	Routes   []string `json:"routes,omitempty"`
}

type label struct {
	Text  string  `json:"text"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Style string  `json:"style"`
	Owner string  `json:"owner,omitempty"`
}

type edge struct {
	ID       string  `json:"id"`
	From     string  `json:"from"`
	To       string  `json:"to"`
	Points   []point `json:"points"`
	Relation string  `json:"relation,omitempty"`
}

type group struct {
	Category string  `json:"category,omitempty"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
}

type legend struct {
	Kind string  `json:"kind"`
	Text string  `json:"text"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

type cue struct {
	Edge    string  `json:"edge,omitempty"`
	Node    string  `json:"node,omitempty"`
	Start   float64 `json:"start"`
	Reverse bool    `json:"reverse,omitempty"`
}

type diagram struct {
	Name        string   `json:"-"`
	Legend      []legend `json:"legend,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Width       float64  `json:"width"`
	Height      float64  `json:"height"`
	Nodes       []node   `json:"nodes"`
	Edges       []edge   `json:"edges,omitempty"`
	Labels      []label  `json:"labels,omitempty"`
	Groups      []group  `json:"groups,omitempty"`
	Motion      []cue    `json:"motion,omitempty"`
}

type tuple struct {
	Subject  string `json:"subject"`
	Relation string `json:"relation"`
	Object   string `json:"object"`
}

func readJSON(path string, dest any) error {
	data, err := readFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func loadDiagram(root, name string) (*diagram, error) {
	var d diagram
	if err := readJSON(filepath.Join(root, "docs", "assets", "diagrams", "src", name+".json"), &d); err != nil {
		return nil, err
	}
	d.Name = name
	if name == "seed-graph" {
		if err := expandSeed(root, &d); err != nil {
			return nil, err
		}
	}
	return &d, nil
}

func expandSeed(root string, d *diagram) error {
	var tuples []tuple
	if err := readJSON(filepath.Join(root, "internal", "graph", "seed.json"), &tuples); err != nil {
		return err
	}
	if len(d.Edges) > 0 {
		return fmt.Errorf("seed layout must not declare edges")
	}
	positions := map[string]*node{}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if len(n.Title) > 0 || len(n.Subtitle) > 0 || n.W != 0 || n.H != 0 || n.R != 0 {
			return fmt.Errorf("seed layout only declares positions and categories: %s", n.ID)
		}
		n.Title = wrap(n.ID, 10)
		positions[n.ID] = n
	}
	radius, err := seedRadius(d.Nodes)
	if err != nil {
		return err
	}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		n.R = radius
		n.W = 2 * radius
		n.H = 2 * radius
		n.X -= radius
		n.Y -= radius
	}
	concepts := map[string]bool{}
	for _, t := range tuples {
		concepts[t.Subject] = true
		concepts[t.Object] = true
		from, to := positions[t.Object], positions[t.Subject]
		if from == nil || to == nil {
			return fmt.Errorf("missing seed position for %s or %s", t.Object, t.Subject)
		}
		a, b := point{from.X + radius, from.Y + radius}, point{to.X + radius, to.Y + radius}
		dx, dy := b.X-a.X, b.Y-a.Y
		length := hypot(dx, dy)
		if length == 0 {
			return fmt.Errorf("coincident seed nodes")
		}
		start, end := point{a.X + radius*dx/length, a.Y + radius*dy/length}, point{b.X - radius*dx/length, b.Y - radius*dy/length}
		e := edge{ID: t.Object + "/" + t.Subject, From: t.Object, To: t.Subject, Points: []point{start, end}, Relation: t.Relation}
		d.Edges = append(d.Edges, e)
	}
	if len(concepts) != len(positions) {
		return fmt.Errorf("seed positions differ from concepts")
	}
	for id := range positions {
		if !concepts[id] {
			return fmt.Errorf("unknown seed concept %s", id)
		}
	}
	sort.Slice(d.Edges, func(i, j int) bool { return d.Edges[i].ID < d.Edges[j].ID })
	return nil
}

func wrap(s string, limit int) []string {
	var lines []string
	for _, word := range strings.Fields(s) {
		if len(lines) == 0 || len(lines[len(lines)-1])+len(word)+1 > limit {
			lines = append(lines, word)
		} else {
			lines[len(lines)-1] += " " + word
		}
	}
	return lines
}

func allLabels(d *diagram) []label {
	labels := append([]label(nil), d.Labels...)
	for _, sample := range d.Legend {
		labels = append(labels, label{Text: sample.Text, X: sample.X + 50, Y: sample.Y + 4, Style: "sublabel"})
	}
	for _, n := range d.Nodes {
		lineCount := len(n.Title) + len(n.Subtitle)
		y := n.Y + n.H/2 - float64(lineCount-1)*9 + 5
		for _, text := range n.Title {
			labels = append(labels, label{Text: text, X: n.X + n.W/2, Y: y, Style: "title", Owner: n.ID})
			y += 18
		}
		for _, text := range n.Subtitle {
			labels = append(labels, label{Text: text, X: n.X + n.W/2, Y: y, Style: "sublabel", Owner: n.ID})
			y += 18
		}
	}
	return labels
}

func seedRadius(nodes []node) (float64, error) {
	radius := 44.0
	for _, n := range nodes {
		n.X = -100
		n.Y = -100
		n.W = 200
		n.H = 200
		for _, l := range allLabels(&diagram{Nodes: []node{n}}) {
			b, err := labelBox(l)
			if err != nil {
				return 0, err
			}
			for _, p := range []point{{b.x, b.y}, {b.x + b.w, b.y}, {b.x, b.y + b.h}, {b.x + b.w, b.y + b.h}} {
				radius = math.Max(radius, hypot(p.X, p.Y)+9)
			}
		}
	}
	return math.Ceil(radius), nil
}

func readFile(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(filepath.Base(path))
}
