package graph

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

const (
	maxGraphBytes  = 8 * 1024 * 1024
	maxGraphTuples = 50_000
	maxGraphNodes  = 20_000
	jsonWhitespace = " \t\r\n"
)

var errGraphBytes = fmt.Errorf("graph exceeds %d bytes", maxGraphBytes)

//go:embed seed.json
var seed []byte

// Load reads a graph file, or uses the embedded curriculum when path is empty.
func Load(path string) (*InMemoryGraph, error) {
	if path == "" {
		return NewInMemoryGraph(), nil
	}
	f, err := os.Open(filepath.Clean(path)) //nolint:gosec // GRAPH_FILE is an operator-selected startup file.
	if err != nil {
		return nil, fmt.Errorf("open graph: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Decode(f)
}

// Decode validates a JSON tuple array within the curriculum limits and constructs an immutable graph.
func Decode(r io.Reader) (*InMemoryGraph, error) {
	input := &graphReader{
		LimitedReader:     io.LimitedReader{R: r, N: maxGraphBytes},
		leadingWhitespace: true,
	}
	dec := json.NewDecoder(input)
	dec.DisallowUnknownFields()
	start, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("decode graph: %w", stableJSONError(err))
	}
	if start != json.Delim('[') {
		return nil, fmt.Errorf("graph must contain exactly one JSON array")
	}
	g := &InMemoryGraph{
		forward:   make(map[string][]domain.GraphTuple),
		reverse:   make(map[string][]domain.GraphTuple),
		nodes:     make(map[string]bool),
		canonical: make(map[string]string),
	}
	seen := make(map[domain.GraphTuple]bool)
	var tuple domain.GraphTuple
	for i := 0; dec.More(); i++ {
		if i == maxGraphTuples {
			return nil, fmt.Errorf("graph exceeds %d input tuples", maxGraphTuples)
		}
		tuple = domain.GraphTuple{}
		if err := dec.Decode(&tuple); err != nil {
			return nil, fmt.Errorf("decode graph tuple %d: %w", i, stableJSONError(err))
		}
		tuple.Subject = strings.TrimSpace(tuple.Subject)
		tuple.Relation = strings.TrimSpace(tuple.Relation)
		tuple.Object = strings.TrimSpace(tuple.Object)
		if seen[tuple] {
			continue
		}
		if err := g.validateTuple(tuple); err != nil {
			return nil, fmt.Errorf("tuple %d %w", i, err)
		}
		if err := g.add(tuple); err != nil {
			return nil, fmt.Errorf("tuple %d %w", i, err)
		}
		seen[tuple] = true
	}
	end, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("decode graph: %w", stableJSONError(err))
	}
	if end != json.Delim(']') {
		return nil, fmt.Errorf("graph must contain exactly one JSON array")
	}
	if err := checkGraphEnd(io.MultiReader(dec.Buffered(), input)); err != nil {
		return nil, err
	}
	if len(g.all) == 0 {
		return nil, fmt.Errorf("graph must contain at least one tuple")
	}
	if cycle := g.prerequisiteCycle(); len(cycle) > 0 {
		return nil, fmt.Errorf("graph contains a prerequisite cycle: %s", cycleSummary(cycle))
	}
	return g, nil
}

type graphSyntaxError struct {
	syntax *json.SyntaxError
}

func (e graphSyntaxError) Error() string {
	return "invalid JSON syntax"
}

func (e graphSyntaxError) Unwrap() error { return e.syntax }

func stableJSONError(err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		// The quoted character and offset can vary when a read ends inside an
		// invalid rune. Keep the original error available to callers.
		return graphSyntaxError{syntax: syntax}
	}
	return err
}

// graphReader exposes at most the byte limit and reads one extra byte to
// distinguish exact-length input from overlong input. It retains terminal
// errors because Decoder.More may read ahead without returning the read error.
type graphReader struct {
	io.LimitedReader
	leadingWhitespace bool
	err               error
}

func (r *graphReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.N > 0 {
		// Only the prefix before the first non-whitespace byte is a known gap.
		// Later reads may continue a token where whitespace changes its validity.
		n := 0
		for n < len(p) && r.N > 0 && r.err == nil {
			read, err := r.LimitedReader.Read(p[n:])
			r.err = err
			chunk := p[n : n+read]
			n += read
			if !r.leadingWhitespace || len(bytes.TrimLeft(chunk, jsonWhitespace)) != 0 {
				r.leadingWhitespace = false
				break
			}
		}
		return n, r.err
	}
	var extra [1]byte
	_, r.err = io.ReadFull(r.R, extra[:])
	if r.err == nil {
		r.err = errGraphBytes
	}
	return 0, r.err
}

// checkGraphEnd skips JSON whitespace without buffering or decoding any trailing value.
func checkGraphEnd(r io.Reader) error {
	var block [4096]byte
	for {
		n, err := r.Read(block[:])
		if len(bytes.TrimLeft(block[:n], jsonWhitespace)) != 0 {
			return fmt.Errorf("graph must contain exactly one JSON array")
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode graph: %w", err)
		}
	}
}

func (g *InMemoryGraph) validateTuple(t domain.GraphTuple) error {
	if t.Subject == "" || t.Object == "" || t.Relation == "" {
		return fmt.Errorf("has an empty field")
	}
	if !prerequisiteRelation(t.Relation) {
		return fmt.Errorf("has unsupported relation %q", t.Relation)
	}
	if !g.nodes[t.Subject] {
		if err := validNodeName(t.Subject); err != nil {
			return fmt.Errorf("subject %.64q %w", t.Subject, err)
		}
	}
	if !g.nodes[t.Object] {
		if err := validNodeName(t.Object); err != nil {
			return fmt.Errorf("object %.64q %w", t.Object, err)
		}
	}
	if t.Subject == t.Object {
		return fmt.Errorf("is a self loop")
	}
	return nil
}

func validNodeName(name string) error {
	if utf8.RuneCountInString(name) > 128 {
		return fmt.Errorf("exceeds 128 runes")
	}
	for _, r := range name {
		switch {
		case r == ',':
			return fmt.Errorf("contains a comma")
		case unicode.Is(unicode.Cc, r):
			return fmt.Errorf("contains a control character")
		case unicode.Is(unicode.Cf, r):
			return fmt.Errorf("contains a format character")
		case r == utf8.RuneError:
			return fmt.Errorf("contains a replacement character")
		}
	}
	return nil
}

func (g *InMemoryGraph) prerequisiteCycle() []string {
	walk := prerequisiteWalk{state: make(map[string]visitState, len(g.nodes))}
	for _, node := range slices.Sorted(maps.Keys(g.nodes)) {
		if cycle := walk.visit(g, node, nil); cycle != nil {
			return cycle
		}
	}
	return nil
}

func cycleSummary(cycle []string) string {
	const maxNodes = 16
	shown := cycle
	if len(cycle)-1 > maxNodes {
		shown = cycle[:maxNodes]
	}
	names := make([]string, len(shown))
	for i, node := range shown {
		names[i] = fmt.Sprintf("%.64s", node)
		if len(names[i]) < len(node) {
			names[i] += "…"
		}
	}
	summary := strings.Join(names, " -> ")
	if len(cycle)-1 > maxNodes {
		summary += fmt.Sprintf(" -> … (%d nodes)", len(cycle)-1)
	}
	return summary
}
