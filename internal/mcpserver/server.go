// Package mcpserver exposes reasoning and curriculum tools over MCP.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

// PlanInput describes a study plan request.
type PlanInput struct {
	FailedTopics   []string `json:"failed_topics" jsonschema:"Topics to study"`
	MasteredTopics []string `json:"mastered_topics,omitempty" jsonschema:"Known topics to omit from study items"`
	VARKProfile    string   `json:"vark_profile" jsonschema:"Learning style: visual, auditory, read_write or kinesthetic"`
	StudentID      string   `json:"student_id,omitempty"`
	CourseID       string   `json:"course_id,omitempty"`
}

// CognitiveGraphInput selects failed concepts and an optional target.
type CognitiveGraphInput struct {
	Topics      []string `json:"topics"`
	Mastered    []string `json:"mastered,omitempty" jsonschema:"Optional known topics; an empty list marks other nodes unassessed"`
	TargetTopic string   `json:"target_topic,omitempty"`
}

// ListTopicsInput optionally filters concepts by canonical substring.
type ListTopicsInput struct {
	Q string `json:"q,omitempty"`
}

// TopicPathInput selects one concept by canonical name.
type TopicPathInput struct {
	Topic string `json:"topic"`
}

// New constructs tools backed by the same bounded pipeline as the HTTP API.
func New(cfg config.Config, g *graph.InMemoryGraph, planner service.Planner, logger *slog.Logger, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "axiom-engine", Version: version}, &mcp.ServerOptions{Logger: logger})
	var resourceLinks *service.ResourceLinkChecker
	if cfg.ResourceLinks.Enabled {
		resourceLinks = service.NewResourceLinkChecker(cfg.ResourceLinks.Timeout, nil, nil)
	}
	reasoning := service.NewReasoningService(g, planner, service.Options{ResourceLinks: resourceLinks, MaxMasteredTopics: cfg.Mastery.MaxTopics, LLMTimeout: cfg.LLMTimeout, MaxFailedTopics: cfg.MaxFailedTopics, MaxConcurrency: cfg.LLMMaxConcurrency, QueueTimeout: cfg.LLMQueueTimeout, Logger: logger})
	mcp.AddTool(s, &mcp.Tool{Name: "generate_study_plan", Description: "Generate a study plan for failed curriculum topics"}, func(ctx context.Context, _ *mcp.CallToolRequest, in PlanInput) (*mcp.CallToolResult, domain.PlanResponse, error) {
		ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
		defer cancel()
		telemetry.CaptureJSON(ctx, "input.value", struct {
			FailedTopics []string `json:"failed_topics"`
			VARKProfile  string   `json:"vark_profile"`
		}{in.FailedTopics, in.VARKProfile})
		out, err := reasoning.GeneratePlan(ctx, domain.AdaptivePlanRequest{FailedTopics: in.FailedTopics, MasteredTopics: in.MasteredTopics, VARKProfile: in.VARKProfile, StudentID: in.StudentID, CourseID: in.CourseID})
		if err == nil {
			telemetry.CaptureJSON(ctx, "output.value", out.Items)
		}
		return nil, out, toolError(err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_cognitive_graph", Description: "Explore failed concepts and their prerequisite neighborhood"}, func(_ context.Context, _ *mcp.CallToolRequest, in CognitiveGraphInput) (*mcp.CallToolResult, domain.CognitiveGraphResponse, error) {
		out, err := service.CognitiveGraph(in.Topics, []string{in.TargetTopic}, in.Mastered, cfg.MaxFailedTopics, cfg.Mastery.MaxTopics, g)
		return nil, out, toolError(err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_topics", Description: "List curriculum topics and direct prerequisites and dependents"}, func(_ context.Context, _ *mcp.CallToolRequest, in ListTopicsInput) (*mcp.CallToolResult, domain.TopicsResponse, error) {
		return nil, domain.TopicsResponse{Topics: g.Topics(in.Q)}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_topic_path", Description: "Get a topic's prerequisite chain in study order"}, func(_ context.Context, _ *mcp.CallToolRequest, in TopicPathInput) (*mcp.CallToolResult, domain.TopicPathResponse, error) {
		out, err := service.TopicPath(in.Topic, g)
		return nil, out, toolError(err)
	})
	s.AddReceivingMiddleware(classifyToolErrors)
	if cfg.Tracing.Enabled || cfg.LangSmith.Enabled {
		s.AddReceivingMiddleware(traceTools(cfg, logger))
	}
	return s
}

func toolError(err error) error {
	if err == nil {
		return nil
	}
	data, _ := json.Marshal(service.ClassifyError(err, false).Response)
	return fmt.Errorf("%s", data)
}

func classifyToolErrors(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, req)
		if method != "tools/call" || err != nil {
			return result, err
		}
		call, ok := result.(*mcp.CallToolResult)
		if !ok || !call.IsError {
			return result, nil
		}
		failure := domain.ErrorResponse{Error: "validation_error", Details: "invalid tool arguments"}
		if len(call.Content) == 1 {
			if content, ok := call.Content[0].(*mcp.TextContent); ok {
				var classified domain.ErrorResponse
				if json.Unmarshal([]byte(content.Text), &classified) == nil && classified.Error != "" {
					failure = classified
				}
			}
		}
		data, _ := json.Marshal(failure)
		call.Content = []mcp.Content{&mcp.TextContent{Text: string(data)}}
		call.StructuredContent = failure
		return call, nil
	}
}
