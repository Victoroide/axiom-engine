package main

import "testing"

func TestMCPLangSmithTraceProcess(t *testing.T) {
	backend, _ := localPlanner(t)
	receiver := newMCPTraceReceiver(t)
	p := startConfiguredMCP(t, map[string]string{"LLM_PROVIDER": "openai-generic", "LLM_MODEL": "local-planner", "LLM_BASE_URL": backend.URL + "/v1", "LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "test-key", "LANGSMITH_ENDPOINT": receiver.server.URL})
	if result := p.exchange(t, mcpPlanCall, 2); result["isError"] == true {
		t.Fatal(result)
	}
	p.stop(t, nil)
	root, call := false, false
	for _, span := range receiver.snapshot() {
		kind := ""
		for _, attr := range span.Attributes {
			if attr.Key == "langsmith.span.kind" {
				kind = attr.Value.GetStringValue()
			}
		}
		if span.Name == "tools/call generate_study_plan" && kind == "chain" {
			root = true
		}
		if span.Name == "chat local-planner" && kind == "llm" {
			call = true
		}
	}
	if !root || !call {
		t.Fatalf("LangSmith tool root=%v model call=%v", root, call)
	}
}
