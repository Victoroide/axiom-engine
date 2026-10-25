package graph

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Victoroide/axiom-reasoning-svc/internal/domain"
)

//go:embed seed.json
var seed []byte

// Load reads a graph file, or uses the embedded curriculum when path is empty.
func Load(path string) (*InMemoryGraph, error) {
	if path == "" {
		return NewInMemoryGraph(), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open graph: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Decode(f)
}

// Decode validates a JSON tuple array and constructs an immutable graph.
func Decode(r io.Reader) (*InMemoryGraph, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var tuples []domain.GraphTuple
	if err := dec.Decode(&tuples); err != nil {
		return nil, fmt.Errorf("decode graph: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("graph must contain exactly one JSON array")
	}
	if len(tuples) == 0 {
		return nil, fmt.Errorf("graph must contain at least one tuple")
	}
	g := &InMemoryGraph{forward: make(map[string][]domain.GraphTuple), reverse: make(map[string][]domain.GraphTuple), nodes: make(map[string]bool), all: make([]domain.GraphTuple, 0, len(tuples))}
	seen := make(map[domain.GraphTuple]bool, len(tuples))
	for i, t := range tuples {
		t.Subject = strings.TrimSpace(t.Subject)
		t.Relation = strings.TrimSpace(t.Relation)
		t.Object = strings.TrimSpace(t.Object)
		if t.Subject == "" || t.Object == "" || t.Relation == "" {
			return nil, fmt.Errorf("tuple %d has an empty field", i)
		}
		if t.Relation != "depends_on" && t.Relation != "is_a" {
			return nil, fmt.Errorf("tuple %d has unsupported relation %q", i, t.Relation)
		}
		if t.Subject == t.Object {
			return nil, fmt.Errorf("tuple %d is a self loop", i)
		}
		if !seen[t] {
			g.add(t)
			seen[t] = true
		}
	}
	return g, nil
}
