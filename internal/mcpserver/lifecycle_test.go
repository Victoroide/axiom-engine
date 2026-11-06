package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestLifecycleCancelsToolWork(t *testing.T) {
	lifecycle := NewLifecycle(t.Context())
	defer lifecycle.Cancel()
	entered, canceled := make(chan struct{}), make(chan struct{})
	client := session(t, func(ctx context.Context, _ service.TopicRequest) ([]domain.PlanItem, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	}, lifecycle.Middleware)
	done := make(chan error, 1)
	go func() {
		_, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "visual"}})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("provider not called")
	}
	drained := lifecycle.StopAccepting()
	select {
	case <-drained:
		t.Fatal("active call was not tracked")
	case <-canceled:
		t.Fatal("work canceled before deadline")
	default:
	}
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_topics", Arguments: map[string]any{}})
	if err != nil || !result.IsError {
		t.Fatalf("new call during drain: result=%v error=%v", result, err)
	}
	lifecycle.Cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("provider context did not observe shutdown")
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("canceled call did not drain")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if lifecycle.StopAccepting() != drained {
		t.Fatal("drain channel changed")
	}
}
