package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type traceReceiver struct {
	server  *httptest.Server
	mu      sync.Mutex
	batches [][]*tracepb.Span
}

func newTraceReceiver(t *testing.T) *traceReceiver {
	t.Helper()
	receiver := &traceReceiver{}
	receiver.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var batch collector.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &batch); err != nil {
			t.Error(err)
		}
		spans := make([]*tracepb.Span, 0)
		for _, resource := range batch.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				spans = append(spans, scope.Spans...)
			}
		}
		receiver.mu.Lock()
		receiver.batches = append(receiver.batches, spans)
		receiver.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(receiver.server.Close)
	return receiver
}

func (r *traceReceiver) snapshot() [][]*tracepb.Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]*tracepb.Span(nil), r.batches...)
}
