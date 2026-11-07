package telemetry

import (
	"log/slog"
	"sync"
	"time"
)

type exportErrorHandler struct {
	mu     sync.Mutex
	next   time.Time
	logger *slog.Logger
}

func (h *exportErrorHandler) Handle(_ error) {
	h.mu.Lock()
	now := time.Now()
	if now.Before(h.next) {
		h.mu.Unlock()
		return
	}
	h.next = now.Add(time.Minute)
	h.mu.Unlock()
	h.logger.Warn("tracing.export_failed", "error_class", "trace_export_failed")
}
