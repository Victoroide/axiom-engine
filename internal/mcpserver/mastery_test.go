package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func masteryConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(func(key string) string {
		switch key {
		case "AXIOM_AUTH_DISABLED":
			return "true"
		case "MAX_MASTERED_TOPICS":
			return "1"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMasteredToolSchemas(t *testing.T) {
	client := session(t, successfulPlanner)
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		field := ""
		switch tool.Name {
		case "generate_study_plan":
			field = "mastered_topics"
		case "get_cognitive_graph":
			field = "mastered"
		default:
			continue
		}
		data, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties map[string]any
			Required   []string
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Properties[field] == nil || slices.Contains(schema.Required, field) {
			t.Fatalf("schema=%s", data)
		}
	}
}

func TestCognitiveToolMasteredHistory(t *testing.T) {
	g := graph.NewInMemoryGraph()
	for _, tc := range []struct {
		name     string
		mastered any
		present  bool
		want     domain.CognitiveGraphResponse
	}{
		{"omitted", nil, false, g.GenerateCognitiveShadow([]string{"Recursion"}, "")},
		{"explicit empty", []string{}, true, g.GenerateCognitiveShadow([]string{"Recursion"}, "", []string{})},
		{"canonical duplicates", []string{" FUNCTIONS ", "functions"}, true, g.GenerateCognitiveShadow([]string{"Recursion"}, "", []string{"Functions"})},
		{"blank history", []string{" "}, true, g.GenerateCognitiveShadow([]string{"Recursion"}, "", []string{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := sessionWithConfig(t, masteryConfig(t), successfulPlanner)
			args := map[string]any{"topics": []string{" recursion "}}
			if tc.present {
				args["mastered"] = tc.mastered
			}
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_cognitive_graph", Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(result.StructuredContent)
			want, _ := json.Marshal(tc.want)
			if result.IsError || string(data) != string(want) {
				var got domain.CognitiveGraphResponse
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				actual, _ := json.Marshal(got)
				if result.IsError || string(actual) != string(want) {
					t.Fatalf("result=%s want=%s", data, want)
				}
			}
		})
	}
}

func TestPlanToolAppliesMasteredTopics(t *testing.T) {
	client := sessionWithConfig(t, masteryConfig(t), func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		if len(req.PrerequisiteOrder) != 0 {
			t.Errorf("mastered prerequisite reached provider: %v", req.PrerequisiteOrder)
		}
		return []domain.PlanItem{{Topic: req.Topic}, {Topic: "Functions"}}, nil
	})
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{"Recursion"}, "mastered_topics": []string{" FUNCTIONS ", "functions"}, "vark_profile": "visual"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var got domain.PlanResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(got.Items) != 1 || got.Items[0].Topic != "Recursion" || !slices.Equal(got.Items[0].PrerequisiteChain, []string{"Functions"}) || got.Meta.ItemsSkippedMastered != 1 || got.Meta.ItemsDropped != 1 {
		t.Fatalf("plan=%s", data)
	}
}

func TestMasteredToolValidation(t *testing.T) {
	for _, tool := range []string{"generate_study_plan", "get_cognitive_graph"} {
		for _, tc := range []struct {
			name    string
			history []string
			details string
		}{
			{"unknown", []string{"Unknown"}, "unknown topics"},
			{"contradictory", []string{" RECURSION "}, "both failed and mastered"},
			{"over limit", []string{"Functions", "Variables"}, "at most 1 unique topics"},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				client := sessionWithConfig(t, masteryConfig(t), func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
					t.Error("invalid history reached provider")
					return nil, nil
				})
				args := map[string]any{"topics": []string{"Recursion"}, "mastered": tc.history}
				if tool == "generate_study_plan" {
					args = map[string]any{"failed_topics": []string{"Recursion"}, "mastered_topics": tc.history, "vark_profile": "visual"}
				}
				result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(result.StructuredContent)
				var failure domain.ErrorResponse
				if err := json.Unmarshal(data, &failure); err != nil {
					t.Fatal(err)
				}
				if !result.IsError || failure.Error != "validation_error" || !strings.Contains(failure.Details, tc.details) {
					t.Fatalf("error=%s", data)
				}
			})
		}
	}
}

func TestPlanToolUsesResourceLinkPolicy(t *testing.T) {
	cfg := masteryConfig(t)
	cfg.ResourceLinks.Enabled = true
	client := sessionWithConfig(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: req.Topic, Resources: []domain.Resource{{Title: "Private", URL: "http://127.0.0.1/private", ResourceType: "video"}}}}, nil
	})
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "visual"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var got domain.PlanResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(got.Items) != 1 || len(got.Items[0].Resources) != 0 || got.Meta.ResourcesUnreachable != 1 {
		t.Fatalf("plan=%s", data)
	}
}
