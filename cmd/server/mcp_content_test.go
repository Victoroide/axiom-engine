package main

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestMCPLangSmithContentProcess(t *testing.T) {
	for _, capture := range []bool{true, false} {
		t.Run(strconv.FormatBool(capture), func(t *testing.T) {
			backend, _ := localPlanner(t)
			receiver := newMCPTraceReceiver(t)
			generic := newMCPTraceReceiver(t)
			p := startConfiguredMCP(t, map[string]string{"LLM_PROVIDER": "openai-generic", "LLM_MODEL": "local-planner", "LLM_BASE_URL": backend.URL + "/v1", "LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "private-tracing-key", "LANGSMITH_PROJECT": "mcp-plans", "LANGSMITH_ENDPOINT": receiver.server.URL, "TRACING_CAPTURE_CONTENT": strconv.FormatBool(capture), "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": generic.server.URL + "/v1/traces"})
			if result := p.exchange(t, mcpPlanCall, 2); result["isError"] == true {
				t.Fatal(result)
			}
			p.stop(t, nil)
			spans := receiver.snapshot()
			byName := map[string]*tracepb.Span{}
			ids := map[string]bool{}
			for _, span := range spans {
				byName[span.Name] = span
				ids[hex.EncodeToString(span.SpanId)] = true
			}
			root := byName["tools/call generate_study_plan"]
			call := byName["chat local-planner"]
			if root == nil || call == nil {
				t.Fatalf("MCP LangSmith tree missing: %v", byName)
			}
			for _, span := range spans {
				if len(span.ParentSpanId) > 0 && !ids[hex.EncodeToString(span.ParentSpanId)] {
					t.Errorf("missing parent for %s", span.Name)
				}
				for _, a := range span.Attributes {
					if strings.Contains(a.Value.GetStringValue(), "private-") {
						t.Errorf("private value in %s", a.Key)
					}
					if !capture && (strings.HasPrefix(a.Key, "gen_ai.prompt.") || strings.HasPrefix(a.Key, "gen_ai.completion.") || a.Key == "input.value" || a.Key == "output.value") {
						t.Errorf("content captured: %s", a.Key)
					}
				}
			}
			attrs := func(span *tracepb.Span) map[string]string {
				values := map[string]string{}
				for _, a := range span.Attributes {
					values[a.Key] = a.Value.GetStringValue()
				}
				return values
			}
			rootAttrs, callAttrs := attrs(root), attrs(call)
			if rootAttrs["langsmith.span.kind"] != "chain" || callAttrs["langsmith.span.kind"] != "llm" || rootAttrs["langsmith.metadata.request_id"] == "" {
				t.Fatalf("run attributes root=%v call=%v", rootAttrs, callAttrs)
			}
			if capture && (!strings.Contains(rootAttrs["input.value"], "Recursion") || !strings.Contains(rootAttrs["output.value"], "Recursion") || callAttrs["gen_ai.prompt.0.content"] == "" || !strings.Contains(callAttrs["gen_ai.completion.0.content"], "Practice recursion.")) {
				t.Fatalf("content attributes root=%v call=%v", rootAttrs, callAttrs)
			}
			if len(generic.snapshot()) != 0 {
				t.Fatal("generic exporter started with tracing disabled")
			}
			if strings.Contains(p.logs.text(), "private-tracing-key") {
				t.Fatal("tracing key logged")
			}
		})
	}
}
