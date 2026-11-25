//go:build linux || darwin

package mcpserver

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/graph"
)

func TestGraphReloadStreamableHTTP(t *testing.T) {
	load := func(root string) *graph.Snapshot {
		t.Helper()
		path := filepath.Join(t.TempDir(), "graph.json")
		contents := fmt.Sprintf(`[{"subject":"Recursion","relation":"depends_on","object":%q}]`, root)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshot, err := graph.LoadSnapshot(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	curriculum := graph.NewCurriculum(load("Old Root"))
	server := httptest.NewServer(NewStreamableHTTPHandlerWithCurriculum(streamableConfig(t), curriculum, plannerFunc(successfulPlanner), streamableLogger(), "test-version", false))
	t.Cleanup(server.Close)
	client := connectStreamableClient(t, server.URL, server.Client())
	assertPath := func(root, other string) {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_topic_path", Arguments: TopicPathInput{Topic: "Recursion"}})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil || result.IsError || !strings.Contains(string(data), root) || strings.Contains(string(data), other) {
			t.Fatalf("tool=%+v data=%s err=%v", result, data, err)
		}
	}
	assertPath("Old Root", "New Root")
	if !curriculum.Publish(load("New Root")) {
		t.Fatal("replacement failed")
	}
	assertPath("New Root", "Old Root")
}
