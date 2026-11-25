package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

func loadServingCurriculum(cfg config.Config) (*graph.Curriculum, error) {
	if !cfg.GraphReloadEnabled {
		g, err := graph.Load(cfg.GraphFile)
		if err != nil {
			return nil, err
		}
		return graph.StaticCurriculum(g), nil
	}
	initial, err := graph.LoadSnapshot(context.Background(), cfg.GraphFile)
	if err != nil {
		return nil, fmt.Errorf("initial GRAPH_FILE must be a readable regular file containing a valid curriculum")
	}
	return graph.NewCurriculum(initial), nil
}

type graphReload struct {
	path       string
	curriculum *graph.Curriculum
	logger     *slog.Logger
	signals    chan os.Signal
	done       chan struct{}
	cancel     context.CancelFunc
	mu         sync.Mutex // Serializes publication with cancellation at shutdown.
	published  atomic.Uint64
	failed     atomic.Uint64
}

func startGraphReload(cfg config.Config, curriculum *graph.Curriculum, logger *slog.Logger) *graphReload {
	if !cfg.GraphReloadEnabled {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &graphReload{path: cfg.GraphFile, curriculum: curriculum, logger: logger, signals: make(chan os.Signal, 1), done: make(chan struct{}), cancel: cancel}
	signal.Notify(r.signals, syscall.SIGHUP)
	r.log(ctx, "loaded")
	go r.run(ctx, graph.LoadSnapshot)
	return r
}

func (r *graphReload) run(ctx context.Context, load func(context.Context, string) (*graph.Snapshot, error)) {
	defer close(r.done)
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-r.signals:
			if ctx.Err() != nil {
				return
			}
		}
		next, err := load(ctx, r.path)
		r.mu.Lock()
		if ctx.Err() != nil {
			r.mu.Unlock()
			r.log(ctx, "canceled")
			return
		}
		outcome := "unchanged"
		switch {
		case err != nil:
			r.failed.Add(1)
			outcome = "failed"
		case r.curriculum.Publish(next):
			r.published.Add(1)
			outcome = "published"
		}
		r.mu.Unlock()
		r.log(ctx, outcome)
	}
}

func (r *graphReload) counts() (published, failed uint64) {
	return r.published.Load(), r.failed.Load()
}

func (r *graphReload) log(ctx context.Context, outcome string) {
	published, failed := r.counts()
	level := slog.LevelInfo
	if outcome == "failed" {
		level = slog.LevelWarn
	}
	r.logger.Log(ctx, level, "graph.reload", "outcome", outcome,
		"graph_revision", r.curriculum.Current().Revision(), "published_total", published, "failed_total", failed)
}

func (r *graphReload) stop() {
	if r != nil {
		r.mu.Lock()
		r.cancel()
		r.mu.Unlock()
	}
}

func (r *graphReload) close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.stop()
	defer signal.Stop(r.signals)
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		// A worker that already finished needs no time from the shutdown budget.
		select {
		case <-r.done:
			return nil
		default:
			return ctx.Err()
		}
	}
}

func finishServing(reload *graphReload, traces *traceLifecycle) error {
	reload.stop()
	ctx, cancel := traces.shutdownContext()
	defer cancel()
	// The canceled loader can finish concurrently. Keep SIGHUP intercepted
	// through the trace flush so a late signal cannot interrupt it.
	traces.flush(ctx)
	if err := reload.close(ctx); err != nil {
		return fmt.Errorf("shutdown: wait for curriculum reload: %w", err)
	}
	return nil
}
