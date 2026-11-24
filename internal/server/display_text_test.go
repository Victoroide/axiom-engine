package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/mcpserver"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestStudyDisplayTextAcrossTransports(t *testing.T) {
	const option = "PRESERVE_DISPLAY_JOINS"
	const child = "AXIOM_TEST_DISPLAY_TEXT_STDIO"
	const empty = "\x00\u202e \u200c\u200d\t"
	const displayTitle = "می\u200cخواهم क्\u200dष 🧑🏽\u200d💻"
	const legacyTitle = "میخواهم क्ष 🧑🏽💻"
	const validURL = "https://xn--bcher-kva.example/learn"
	const input = `{"student_id":"student-42","course_id":"cs101","failed_topics":["Recursion"],"vark_profile":"visual"}`
	const boundaryURL = "https://example.com/boundary"
	badID := strings.Replace(input, "student-42", "stu\u200cdent-42", 1)
	prefix := "Amend " + strings.Repeat("a", 992)
	titlePrefix := strings.Repeat("a", 198)
	planner := fakePlanner(func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{
			Topic: req.Topic, Explanation: "  Am\u202ee\u00adnd\n\t" + strings.Repeat("a", 992) + "ب\u200cت",
			Resources: []domain.Resource{
				{Title: "\t" + displayTitle + " \u2066\u2069 ", URL: validURL, ResourceType: " Video "},
				{Title: empty, URL: "https://example.com/empty", ResourceType: "video"},
				{Title: "Unicode host", URL: "https://bücher.example", ResourceType: "video"},
				{Title: "Joiner in path", URL: "https://example.com/می\u200cخواهم", ResourceType: "video"},
				{Title: titlePrefix + "\u0d4e b", URL: boundaryURL, ResourceType: "video"},
			},
		}}, nil
	})
	if os.Getenv(child) == "1" {
		cfg, err := config.LoadMCP(func(key string) string {
			if key == option {
				return os.Getenv(key)
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		s := mcpserver.New(cfg, graph.NewInMemoryGraph(), planner, logger, "test-version")
		if err := s.Run(t.Context(), &mcp.StdioTransport{}); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	t.Setenv("LLM_MAX_CONTEXT_EDGES", "1")
	latency := regexp.MustCompile(`("(llm|total)_latency_ms":)[0-9]+`)
	for _, tc := range []struct{ name, setting, title, boundaryTitle, explanation string }{
		{"default", "", legacyTitle, titlePrefix + "\u0d4e", prefix + "بت"},
		{"disabled", "false", legacyTitle, titlePrefix + "\u0d4e", prefix + "بت"},
		{"enabled", "true", displayTitle, titlePrefix, prefix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := func(t *testing.T, raw []byte, want string) {
				t.Helper()
				got := latency.ReplaceAllString(string(raw), "${1}0")
				if got != want {
					t.Errorf("plan bytes = %s", got)
				}
			}
			t.Run("HTTP", func(t *testing.T) {
				env := map[string]string{"AXIOM_AUTH_DISABLED": "true", option: tc.setting}
				cfg, err := config.Load(func(k string) string { return env[k] })
				if err != nil {
					t.Fatal(err)
				}
				want := `{"student_id":"student-42","course_id":"cs101","items":[{"topic":"Recursion","priority":1,"prerequisite_chain":["Functions"],"explanation":"` +
					tc.explanation + `","resources":[{"title":"` + tc.title + `","url":"` + validURL +
					`","resource_type":"video"},{"title":"","url":"https://example.com/empty","resource_type":"video"},{"title":"` +
					tc.boundaryTitle + `","url":"` + boundaryURL + `","resource_type":"video"}]}],"_meta":{"subgraph_tuples":3,"topics_processed":1,"items_generated":1,"items_after_validation":1,"items_dropped":0,"llm_latency_ms":0,"total_latency_ms":0}}`
				testStudyDisplayHTTP(t, newTestApp(t, cfg, planner, nil), input, badID, want, check)
			})
			t.Run("MCP", func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStudyDisplayTextAcrossTransports$") //nolint:gosec // Reexecute this test binary as a stdio server.
				cmd.Env = append(cmd.Environ(), child+"=1", option+"="+tc.setting)
				client := mcp.NewClient(&mcp.Implementation{Name: "display-client", Version: "1"}, nil)
				session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := session.Close(); err != nil {
						t.Error(err)
					}
				}()
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "generate_study_plan", Arguments: json.RawMessage(input)})
				if err != nil || result.IsError || len(result.Content) != 1 {
					t.Fatalf("result=%v err=%v", result, err)
				}
				content, ok := result.Content[0].(*mcp.TextContent)
				if !ok {
					t.Fatalf("expected text content, got %T", result.Content[0])
				}
				want := `{"_meta":{"items_after_validation":1,"items_dropped":0,"items_generated":1,"llm_latency_ms":0,"subgraph_tuples":3,"topics_processed":1,"total_latency_ms":0},"course_id":"cs101","items":[{"explanation":"` +
					tc.explanation + `","prerequisite_chain":["Functions"],"priority":1,"resources":[{"resource_type":"video","title":"` +
					tc.title + `","url":"` + validURL + `"},{"resource_type":"video","title":"","url":"https://example.com/empty"},{"resource_type":"video","title":"` +
					tc.boundaryTitle + `","url":"` + boundaryURL + `"}],"topic":"Recursion"}],"student_id":"student-42"}`
				check(t, []byte(content.Text), want)
				var decoded any
				if err := json.Unmarshal([]byte(content.Text), &decoded); err != nil {
					t.Fatal(err)
				}
				structured, err := json.Marshal(result.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				textContent, err := json.Marshal(decoded)
				if err != nil || !bytes.Equal(structured, textContent) {
					t.Fatalf("structured content=%s, text content=%s, err=%v", structured, textContent, err)
				}
				result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "generate_study_plan", Arguments: json.RawMessage(badID)})
				if err != nil || !result.IsError || len(result.Content) != 1 {
					t.Fatalf("invalid ID result=%v err=%v", result, err)
				}
				content, ok = result.Content[0].(*mcp.TextContent)
				if !ok || !strings.Contains(content.Text, "student_id must match") {
					t.Fatalf("invalid ID error=%v", result.Content)
				}
			})
		})
	}
}

func testStudyDisplayHTTP(t *testing.T, app *fiber.App, body, badID, want string, check func(*testing.T, []byte, string)) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() {
		if err := app.ShutdownWithTimeout(time.Second); err != nil {
			t.Error(err)
		}
		if err := <-stopped; err != nil {
			t.Error(err)
		}
	})
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	post := func(t *testing.T, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+ln.Addr().String()+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("path=%s read=%v close=%v", path, err, closeErr)
		}
		return resp.StatusCode, data
	}
	for _, tc := range []struct{ name, path string }{
		{"JSON", "/api/v1/adaptive-plan"},
		{"SSE", "/api/v1/adaptive-plan/stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, data := post(t, tc.path, body)
			if status != http.StatusOK {
				t.Fatalf("status=%d body=%s", status, data)
			}
			if tc.name == "SSE" {
				events := parseEvents(t, string(data))
				if len(events) != 2 || events[0].name != "topic" || events[1].name != "plan" {
					t.Fatalf("events=%+v", events)
				}
				data = events[1].data
			}
			check(t, data, want)
		})
	}
	if status, data := post(t, "/api/v1/adaptive-plan", badID); status != http.StatusBadRequest || !bytes.Contains(data, []byte("student_id must match")) {
		t.Fatalf("invalid ID status=%d body=%s", status, data)
	}
}
