package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

type traceLifecycle struct {
	shutdown func(context.Context) error
	timeout  time.Duration
	deadline atomic.Int64
	logger   *slog.Logger
}

func prepareTracing(cfg config.Config, logger *slog.Logger) (*traceLifecycle, error) {
	shutdown, err := telemetry.Setup(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	return &traceLifecycle{shutdown: shutdown, timeout: cfg.ShutdownTimeout, logger: logger}, nil
}

func (t *traceLifecycle) setDeadline(deadline time.Time) { t.deadline.Store(deadline.UnixNano()) }

func (t *traceLifecycle) flush() {
	deadline := time.Now().Add(t.timeout)
	if ns := t.deadline.Load(); ns != 0 {
		deadline = time.Unix(0, ns)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := t.shutdown(ctx); err != nil {
		t.logger.Error("tracing.flush_failed", "error_class", "trace_export_failed")
	}
}
