package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type stdioCapture struct {
	raw    *bytes.Buffer
	client *io.PipeWriter
}

func (w stdioCapture) Write(p []byte) (int, error) {
	n, _ := w.raw.Write(p)
	_, err := w.client.Write(p)
	if errors.Is(err, io.ErrClosedPipe) {
		return n, nil
	}
	return n, err
}

func TestPlannerPanicPrivacyOverMCPStdio(t *testing.T) {
	const marker = "private-mcp-stdio-panic-value"
	if os.Getenv("AXIOM_TEST_MCP_PANIC_CHILD") == "1" {
		cfg := config.Config{RequestTimeout: time.Second, LLMTimeout: time.Second, MaxFailedTopics: 1}
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		planner := plannerFunc(func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) { panic(errors.New(marker)) })
		s := New(cfg, graph.NewInMemoryGraph(), planner, logger, "test-version")
		if err := s.Run(t.Context(), &mcp.StdioTransport{}); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // Keep the test runner's status line off the protocol stream.
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPlannerPanicPrivacyOverMCPStdio$") //nolint:gosec // Reexecute this test as a stdio server.
	cmd.Env = append(cmd.Environ(), "AXIOM_TEST_MCP_PANIC_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	cmd.Stdout, cmd.Stderr = stdioCapture{raw: &stdout, client: writer}, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = writer.Close()
		done <- err
	}()
	waited := false
	defer func() {
		if !waited {
			_ = reader.Close()
			_ = stdin.Close()
			cancel()
			if err := <-done; err != nil {
				t.Logf("stdio server cleanup: %v", err)
			}
		}
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.IOTransport{Reader: reader, Writer: stdin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	for range 2 {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_study_plan", Arguments: PlanInput{FailedTopics: []string{"Recursion"}, VARKProfile: "visual"}})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError || len(result.Content) != 1 {
			t.Fatalf("unexpected tool result: %v", result)
		}
		const envelope = `{"error":"internal_error","details":"reasoning failed"}`
		if content, ok := result.Content[0].(*mcp.TextContent); !ok || content.Text != envelope {
			t.Fatalf("unexpected tool error: %v", result.Content)
		}
		var failure domain.ErrorResponse
		structured, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(structured, &failure); err != nil {
			t.Fatal(err)
		}
		if failure != (domain.ErrorResponse{Error: "internal_error", Details: "reasoning failed"}) {
			t.Fatalf("unexpected structured tool error: %s", structured)
		}
	}
	if next, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_topics", Arguments: ListTopicsInput{Q: "Recursion"}}); err != nil || next.IsError {
		t.Fatalf("MCP session failed after planner panic: result=%v err=%v", next, err)
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	err = <-done
	waited = true
	if err != nil {
		t.Fatalf("stdio server exit: %v\n%s", err, &stderr)
	}
	if strings.Contains(stdout.String(), marker) || strings.Contains(stderr.String(), marker) {
		t.Error("recovered value appeared in MCP process output")
	}
	frames := 0
	for line := range strings.Lines(stdout.String()) {
		if _, err := jsonrpc.DecodeMessage([]byte(line)); err != nil {
			t.Errorf("MCP stdout contains a non-JSON-RPC line: %v", err)
		}
		frames++
	}
	if frames < 4 {
		t.Fatalf("got %d protocol frames", frames)
	}
	ids := map[string]bool{}
	for line := range strings.Lines(stderr.String()) {
		var record map[string]string
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["level"] != "ERROR" {
			continue
		}
		id := record["request_id"]
		if record["msg"] != "planner.panic" || record["error_class"] != "internal_error" || id == "" || ids[id] || len(line) > 1024 {
			t.Errorf("unexpected MCP recovery record %v", record)
		}
		ids[id] = true
	}
	if len(ids) != 2 {
		t.Errorf("got %d distinct MCP recovery IDs, want two", len(ids))
	}
}
