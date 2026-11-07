package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func TestLangSmithShutdownIgnoresRuntimeFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attempts int64
	}{{"without later export", 1}, {"after recovery", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if calls.Add(1) == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte("private receiver failure"))
					return
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
			}))
			defer receiver.Close()
			tp, p := boundedProvider(t, receiver.URL)
			tracer := tp.Tracer("shutdown")
			for range tc.attempts {
				_, span := tracer.Start(t.Context(), "chat", trace.WithAttributes(attribute.String("langsmith.span.kind", "llm")))
				span.End()
			}
			waitForExportState(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.buffered == 0 })
			if calls.Load() != tc.attempts {
				t.Fatalf("export attempts=%d", calls.Load())
			}
			if err := tp.Shutdown(t.Context()); err != nil {
				t.Fatalf("runtime failure reported during shutdown: %v", err)
			}
		})
	}
}

func TestLangSmithShutdownReportsDrainFailure(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/x-protobuf")
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("private receiver failure"))
	}))
	defer receiver.Close()
	tp, p := boundedProvider(t, receiver.URL)
	defer close(release)
	tracer := tp.Tracer("shutdown")
	for range 2 {
		_, span := tracer.Start(t.Context(), "chat", trace.WithAttributes(attribute.String("langsmith.span.kind", "llm")))
		span.End()
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("export did not start")
	}
	done := make(chan error, 1)
	go func() { done <- tp.Shutdown(t.Context()) }()
	waitForExportState(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.stopped })
	release <- struct{}{}
	select {
	case err := <-done:
		if err == nil || err.Error() != "LangSmith trace export failed" {
			t.Fatalf("shutdown drain error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if calls.Load() != 2 {
		t.Fatalf("export attempts=%d", calls.Load())
	}
}

func waitForExportState(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("export state did not settle")
		}
	}
}
