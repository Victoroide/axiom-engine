package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

type plannerFunc func(context.Context, service.TopicRequest) ([]domain.PlanItem, error)

func (f plannerFunc) GenerateTopic(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
	return f(ctx, req)
}

func session(t *testing.T, planner plannerFunc, middleware ...mcp.Middleware) *mcp.ClientSession {
	t.Helper()
	cfg, err := config.Load(func(key string) string {
		if key == "AXIOM_AUTH_DISABLED" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return sessionWithConfig(t, cfg, planner, middleware...)
}

func sessionWithConfig(t *testing.T, cfg config.Config, planner plannerFunc, middleware ...mcp.Middleware) *mcp.ClientSession {
	t.Helper()
	s := New(cfg, graph.NewInMemoryGraph(), planner, slog.New(slog.NewJSONHandler(io.Discard, nil)), "test-version")
	s.AddReceivingMiddleware(middleware...)
	return connectTestClient(t, s)
}

func connectTestClient(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestPlannerPanicPrivacyOverMCP(t *testing.T) {
	const marker = "private-mcp-panic-value"
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = tp.Shutdown(t.Context()) }()
	var logs bytes.Buffer
	logger := slog.New(telemetry.LogHandler(slog.NewJSONHandler(&logs, nil)))
	cfg := config.Config{RequestTimeout: time.Second, LLMTimeout: time.Second, MaxFailedTopics: 1}
	cfg.Tracing.Enabled = true
	cfg.Tracing.CaptureContent = true
	planner := plannerFunc(func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) { panic(errors.New(marker)) })
	cs := connectTestClient(t, New(cfg, graph.NewInMemoryGraph(), planner, logger, "test-version"))
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: PlanInput{FailedTopics: []string{"Recursion"}, VARKProfile: "visual"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	const envelope = `{"error":"internal_error","details":"reasoning failed"}`
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var failure domain.ErrorResponse
	if err := json.Unmarshal(structured, &failure); err != nil {
		t.Fatal(err)
	}
	if !result.IsError || failure != (domain.ErrorResponse{Error: "internal_error", Details: "reasoning failed"}) || len(result.Content) != 1 {
		t.Fatalf("unexpected MCP failure envelope: %s", data)
	}
	if content, ok := result.Content[0].(*mcp.TextContent); !ok || content.Text != envelope {
		t.Fatalf("unexpected MCP error content: %s", data)
	}
	spans := exporter.GetSpans()
	traceData, err := json.Marshal(spans)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(marker)) || strings.Contains(logs.String(), marker) || bytes.Contains(traceData, []byte(marker)) {
		t.Error("recovered error appeared in MCP response, log or trace")
	}
	var id string
	for _, span := range spans {
		if span.Name == "tools/call generate_study_plan" && span.Status.Code == codes.Error && span.Status.Description == "internal_error" {
			for _, attr := range span.Attributes {
				if attr.Key == "request_id" {
					id = attr.Value.AsString()
				}
			}
		}
	}
	if id == "" {
		t.Errorf("missing classified MCP span with request ID: %s", traceData)
	}
	count := 0
	for line := range strings.Lines(logs.String()) {
		var record struct {
			Message    string `json:"msg"`
			Level      string `json:"level"`
			RequestID  string `json:"request_id"`
			ErrorClass string `json:"error_class"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Level != "ERROR" {
			continue
		}
		count++
		if record.Message != "planner.panic" || record.RequestID == "" || record.RequestID != id || record.ErrorClass != "internal_error" || len(line) > 1024 {
			t.Errorf("unexpected MCP panic log fields or size (%d bytes, event %q)", len(line), record.Message)
		}
	}
	if count != 1 {
		t.Errorf("got %d error records, want one", count)
	}
	if next, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_topics", Arguments: ListTopicsInput{Q: "Recursion"}}); err != nil || next.IsError {
		t.Fatalf("MCP session failed after planner panic: result=%v err=%v", next, err)
	}
}

func successfulPlanner(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
	return []domain.PlanItem{{Topic: req.Topic, Explanation: "Practice", Resources: []domain.Resource{}}}, nil
}

func TestToolsAndSchemas(t *testing.T) {
	cs := session(t, successfulPlanner)
	if info := cs.InitializeResult().ServerInfo; info.Name != "axiom-engine" || info.Version != "test-version" {
		t.Fatal(info)
	}
	result, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string][]string{"generate_study_plan": {"failed_topics", "vark_profile"}, "get_cognitive_graph": {"topics"}, "list_topics": {}, "get_topic_path": {"topic"}}
	if len(result.Tools) != len(wants) {
		t.Fatal(result)
	}
	for _, tool := range result.Tools {
		required, ok := wants[tool.Name]
		if !ok {
			t.Fatal(tool.Name)
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Type       string
			Required   []string
			Properties map[string]any
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		slices.Sort(required)
		slices.Sort(schema.Required)
		if schema.Type != "object" || !slices.Equal(required, schema.Required) || tool.OutputSchema == nil {
			t.Fatalf("tool %s: %s output=%v", tool.Name, data, tool.OutputSchema)
		}
		for _, field := range required {
			if schema.Properties[field] == nil {
				t.Fatal(field)
			}
		}
		if tool.Name == "generate_study_plan" {
			for _, field := range []string{"student_id", "course_id"} {
				if schema.Properties[field] == nil {
					t.Fatal(field)
				}
			}
		}
	}
}

func TestToolCalls(t *testing.T) {
	cs := session(t, successfulPlanner)
	g := graph.NewInMemoryGraph()
	path, _ := g.Path("Recursion")
	for _, tc := range []struct {
		name  string
		input any
		want  any
	}{
		{"list_topics", map[string]any{"q": " reCURSION "}, domain.TopicsResponse{Topics: g.Topics("Recursion")}},
		{"get_topic_path", map[string]any{"topic": " recursion "}, path},
		{"get_cognitive_graph", map[string]any{"topics": []string{"recursion"}, "target_topic": "Recursion"}, g.GenerateCognitiveShadow([]string{"Recursion"}, "Recursion")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.input})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(result.StructuredContent)
			want, _ := json.Marshal(tc.want)
			var a, b any
			_ = json.Unmarshal(got, &a)
			_ = json.Unmarshal(want, &b)
			if result.IsError || !reflect.DeepEqual(a, b) {
				t.Fatalf("result=%s want=%s", got, want)
			}
		})
	}
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{"recursion"}, "vark_profile": "visual", "student_id": "student-42", "course_id": "cs101"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var plan domain.PlanResponse
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(plan.Items) != 1 || plan.Items[0].Topic != "Recursion" || plan.StudentID != "student-42" || plan.CourseID != "cs101" {
		t.Fatalf("result=%s", data)
	}
}

func TestToolErrors(t *testing.T) {
	for _, tc := range []struct {
		name, tool, code string
		input            any
		cause            error
	}{
		{"unknown path", "get_topic_path", "validation_error", map[string]any{"topic": "absent"}, nil},
		{"invalid target", "get_cognitive_graph", "validation_error", map[string]any{"topics": []string{"Recursion"}, "target_topic": "Polymorphism"}, nil},
		{"unknown topics", "get_cognitive_graph", "validation_error", map[string]any{"topics": []string{"absent"}}, nil},
		{"schema mismatch", "list_topics", "validation_error", map[string]any{"q": 42}, nil},
		{"missing required", "get_topic_path", "validation_error", map[string]any{}, nil},
		{"invalid profile", "generate_study_plan", "validation_error", map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "invalid"}, nil},
		{"unavailable", "generate_study_plan", "service_unavailable", nil, service.ErrThrottled},
		{"deadline", "generate_study_plan", "gateway_timeout", nil, context.DeadlineExceeded},
		{"upstream", "generate_study_plan", "bad_gateway", nil, service.ErrUpstream},
		{"invalid output", "generate_study_plan", "bad_gateway", nil, service.ErrInvalidOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := session(t, func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) { return nil, tc.cause })
			input := tc.input
			if input == nil {
				input = map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "visual"}
			}
			result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.tool, Arguments: input})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(result.StructuredContent)
			var failure domain.ErrorResponse
			if err := json.Unmarshal(data, &failure); err != nil {
				t.Fatal(err)
			}
			if !result.IsError || failure.Error != tc.code || failure.Details == "" {
				t.Fatalf("error=%s want=%s", data, tc.code)
			}
		})
	}
}
