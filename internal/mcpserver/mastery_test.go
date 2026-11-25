package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/test/resourcelinkfixture"
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
	for _, tc := range []struct {
		name          string
		enabled       bool
		empty         bool
		defaultPolicy bool
		kept          int
		dropped       int
		wantChecks    int32
	}{
		{name: "disabled", kept: 2},
		{name: "enabled", enabled: true, dropped: 2, wantChecks: 1},
		{name: "default-policy", enabled: true, defaultPolicy: true, dropped: 1},
		{name: "empty", enabled: true, empty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := resourcelinkfixture.New(t)
			cfg := masteryConfig(t)
			cfg.ResourceLinks.Enabled = tc.enabled
			planner := plannerFunc(func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
				item := domain.PlanItem{Topic: req.Topic}
				if !tc.empty {
					item.Resources = fixture.Resources()
				}
				if tc.defaultPolicy {
					item.Resources = item.Resources[:1]
				}
				return []domain.PlanItem{item}, nil
			})
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			g := graph.NewInMemoryGraph()
			var s *mcp.Server
			if tc.defaultPolicy {
				s = New(cfg, g, planner, logger, "test-version")
			} else {
				s = newServer(cfg, graph.StaticCurriculum(g), planner, logger, "test-version", mcp.ServerOptions{}, fixture)
			}
			client := connectTestClient(t, s)
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: PlanInput{FailedTopics: []string{"Recursion"}, VARKProfile: "visual"}})
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var got domain.PlanResponse
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if result.IsError || len(got.Items) != 1 || got.Items[0].Topic != "Recursion" || got.Items[0].Resources == nil || len(got.Items[0].Resources) != tc.kept || got.Meta.ResourcesUnreachable != tc.dropped {
				t.Fatalf("plan=%s", data)
			}
			if fixture.Hits() != tc.wantChecks {
				t.Errorf("outbound requests=%d, want %d", fixture.Hits(), tc.wantChecks)
			}
			if !tc.enabled {
				if len(result.Content) != 1 {
					t.Fatalf("content=%v", result.Content)
				}
				content, ok := result.Content[0].(*mcp.TextContent)
				if !ok {
					t.Fatalf("content=%T", result.Content[0])
				}
				stable := resourcelinkfixture.NormalizePlanLatency(content.Text)
				want := fmt.Sprintf(`{"_meta":{"items_after_validation":1,"items_dropped":0,"items_generated":1,"llm_latency_ms":0,"subgraph_tuples":3,"topics_processed":1,"total_latency_ms":0},`+
					`"course_id":"","items":[{"explanation":"","prerequisite_chain":["Functions"],"priority":1,"resources":[`+
					`{"resource_type":"video","title":"First","url":%[1]q},{"resource_type":"diagram","title":"Second","url":%[1]q}],"topic":"Recursion"}],"student_id":""}`, fixture.URL)
				if stable != want {
					t.Errorf("disabled response=%s\nwant=%s", stable, want)
				}
			}
		})
	}
}
