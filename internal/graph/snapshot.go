package graph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync/atomic"
)

// Snapshot pairs an immutable curriculum graph with the digest of its source bytes.
type Snapshot struct {
	graph    *InMemoryGraph
	revision string
}

// Graph returns the immutable in-memory graph.
func (s *Snapshot) Graph() *InMemoryGraph { return s.graph }

// Revision returns the SHA-256 of the local file, if loaded for reload.
func (s *Snapshot) Revision() string { return s.revision }

// Curriculum is the source of the current snapshot for a serving process.
type Curriculum struct {
	current atomic.Pointer[Snapshot]
}

// NewCurriculum makes initial available to readers.
func NewCurriculum(initial *Snapshot) *Curriculum {
	c := &Curriculum{}
	c.current.Store(initial)
	return c
}

// StaticCurriculum wraps a startup-only graph.
func StaticCurriculum(g *InMemoryGraph) *Curriculum {
	return NewCurriculum(&Snapshot{graph: g})
}

// Current pins one immutable snapshot. Call it once per graph invocation.
func (c *Curriculum) Current() *Snapshot { return c.current.Load() }

// Publish replaces the active snapshot unless its source bytes are identical.
func (c *Curriculum) Publish(next *Snapshot) bool {
	for {
		active := c.Current()
		if next.revision == active.revision {
			return false
		}
		if c.current.CompareAndSwap(active, next) {
			return true
		}
	}
}

// LoadSnapshot opens one local regular file and validates and hashes the same byte stream.
func LoadSnapshot(ctx context.Context, path string) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := openSnapshotFile(path)
	if err != nil {
		return nil, fmt.Errorf("open graph: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat graph: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("graph file must be a regular file")
	}
	if info.Size() > maxGraphBytes {
		return nil, errGraphBytes
	}
	return decodeSnapshot(ctx, f)
}

func decodeSnapshot(ctx context.Context, r io.Reader) (*Snapshot, error) {
	digest := sha256.New()
	g, err := decode(io.TeeReader(r, digest), ctx.Err)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Snapshot{graph: g, revision: hex.EncodeToString(digest.Sum(nil))}, nil
}
