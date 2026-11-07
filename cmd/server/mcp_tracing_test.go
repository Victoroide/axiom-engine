package main

import (
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type mcpTraceReceiver struct {
	server *httptest.Server
	mu     sync.Mutex
	spans  []*tracepb.Span
}

func newMCPTraceReceiver(t *testing.T) *mcpTraceReceiver {
	t.Helper()
	receiver := &mcpTraceReceiver{}
	receiver.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var batch collector.ExportTraceServiceRequest
		if err := proto.Unmarshal(data, &batch); err != nil {
			t.Error(err)
		}
		receiver.mu.Lock()
		for _, resource := range batch.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				receiver.spans = append(receiver.spans, scope.Spans...)
			}
		}
		receiver.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(receiver.server.Close)
	return receiver
}

func (r *mcpTraceReceiver) snapshot() []*tracepb.Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*tracepb.Span(nil), r.spans...)
}

func TestMCPExportsToolTraceProcess(t *testing.T) {
	backend, _ := localPlanner(t)
	receiver := newMCPTraceReceiver(t)
	p := startConfiguredMCP(t, map[string]string{"LLM_PROVIDER": "openai-generic", "LLM_MODEL": "local-planner", "LLM_BASE_URL": backend.URL + "/v1", "TRACING_ENABLED": "true", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": receiver.server.URL + "/v1/traces", "OTEL_BSP_SCHEDULE_DELAY": "60000", "SHUTDOWN_TIMEOUT": "2s"})
	if result := p.exchange(t, mcpPlanCall, 2); result["isError"] == true {
		t.Fatal(result)
	}
	p.stop(t, syscall.SIGTERM)
	spans := receiver.snapshot()
	byName := map[string]*tracepb.Span{}
	ids := map[string]bool{}
	for _, span := range spans {
		byName[span.Name] = span
		ids[hex.EncodeToString(span.SpanId)] = true
		for _, a := range span.Attributes {
			if strings.Contains(a.Value.GetStringValue(), "private-") {
				t.Errorf("learner identifier exported in %s", a.Key)
			}
		}
	}
	root := byName["tools/call generate_study_plan"]
	if root == nil || root.Kind != tracepb.Span_SPAN_KIND_SERVER {
		t.Fatalf("tool root missing: %v", byName)
	}
	for _, name := range []string{"plan.validate", "plan.subgraph", "plan.topic", "chat local-planner", "plan.merge", "plan.validate_output"} {
		if byName[name] == nil {
			t.Errorf("missing %s", name)
		}
	}
	for _, span := range spans {
		if len(span.ParentSpanId) > 0 && !ids[hex.EncodeToString(span.ParentSpanId)] {
			t.Errorf("missing parent for %s", span.Name)
		}
	}
	if !strings.Contains(p.logs.text(), `"msg":"mcp.tool"`) || !strings.Contains(p.logs.text(), `"trace_id"`) {
		t.Fatalf("uncorrelated tool logs: %s", p.logs.text())
	}
}
