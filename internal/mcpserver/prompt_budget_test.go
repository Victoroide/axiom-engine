package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestPromptBudgetRejectsBeforeMCPDispatch(t *testing.T) {
	cfg, err := config.LoadMCP(func(key string) string {
		if key == "LLM_MAX_CONTEXT_EDGES" {
			return "1"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	g := graph.NewInMemoryGraph()
	var calls atomic.Int32
	planner := plannerFunc(func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return successfulPlanner(ctx, req)
	})
	local := connectTestClient(t, New(cfg, g, planner, streamableLogger(), "test-version"))
	srv := httptest.NewServer(NewStreamableHTTPHandler(cfg, g, planner, streamableLogger(), "test-version", true))
	t.Cleanup(srv.Close)
	remote := connectStreamableClient(t, srv.URL, srv.Client())
	for name, session := range map[string]*mcp.ClientSession{"local": local, "HTTP": remote} {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: PlanInput{
				FailedTopics: []string{"Recursion", "Polymorphism"}, VARKProfile: "visual",
			}})
			if err != nil {
				t.Fatal(err)
			}
			const want = `{"error":"curriculum_context_too_large","details":"curriculum context exceeds the prompt budget"}`
			structured, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var failure domain.ErrorResponse
			if err := json.Unmarshal(structured, &failure); err != nil {
				t.Fatal(err)
			}
			if !result.IsError || failure.Error != "curriculum_context_too_large" || failure.Details != "curriculum context exceeds the prompt budget" || len(result.Content) != 1 {
				t.Fatalf("result = %+v, structured = %s, error = %v", result, structured, err)
			}
			if content, ok := result.Content[0].(*mcp.TextContent); !ok || content.Text != want {
				t.Fatalf("content = %+v", result.Content)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("planner calls = %d, want zero", got)
	}
}
