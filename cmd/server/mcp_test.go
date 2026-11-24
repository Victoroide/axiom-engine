package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/test/fakebedrock"
)

func TestMCPProcess(t *testing.T) {
	dir := t.TempDir()
	graphFile := filepath.Join(dir, "graph.json")
	graphData, err := os.ReadFile("../../internal/graph/seed.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(graphFile, graphData, 0600); err != nil { //nolint:gosec // The destination is inside a fresh test directory.
		t.Fatal(err)
	}
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "ambient-mcp-bearer-fixture")
	handler := fakebedrock.NewHandler("ok")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=dummy/") {
			t.Error("Converse request was not signed with the dummy credential")
		}
		handler.ServeHTTP(w, r)
	}))
	defer backend.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	env := append(mcpFixtureEnv(t), "AXIOM_INTERNAL_TOKEN=", "AXIOM_INTERNAL_TOKEN_PREVIOUS=", "AXIOM_AUTH_DISABLED=invalid", "PORT=invalid", "RATE_LIMIT_MAX=invalid", "BAML_LOG=off",
		"GRAPH_FILE="+graphFile, "AWS_ENDPOINT_URL="+backend.URL, "AWS_ENDPOINT_URL_BEDROCK_RUNTIME="+backend.URL,
		"AWS_ACCESS_KEY_ID=dummy", "AWS_SECRET_ACCESS_KEY=dummy", "AWS_SESSION_TOKEN=", "AWS_REGION=us-east-1", "AWS_DEFAULT_REGION=us-east-1", "AWS_EC2_METADATA_DISABLED=true", "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=false", "AWS_RETRY_MODE=standard", "AWS_MAX_ATTEMPTS=1")
	cmd := serverCommand(ctx, env, "mcp")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("process: %v stderr=%s", err, &stderr)
		}
	}()
	scanner := bufio.NewScanner(stdout)
	exchange := func(message string, id int) map[string]any {
		t.Helper()
		if _, err := io.WriteString(stdin, message+"\n"); err != nil {
			t.Fatal(err)
		}
		for scanner.Scan() {
			var response map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				t.Fatalf("non JSON-RPC stdout: %s", scanner.Text())
			}
			if response["jsonrpc"] != "2.0" {
				t.Fatalf("non JSON-RPC stdout: %v", response)
			}
			if response["id"] == float64(id) {
				if response["error"] != nil {
					t.Fatal(response)
				}
				return response["result"].(map[string]any)
			}
		}
		t.Fatalf("stdout ended before response %d: %v", id, scanner.Err())
		return nil
	}
	initialized := exchange(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`, 1)
	if initialized["serverInfo"].(map[string]any)["name"] != "axiom-engine" {
		t.Fatal(initialized)
	}
	if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	listed := exchange(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, 2)
	if len(listed["tools"].([]any)) != 4 {
		t.Fatal(listed)
	}
	result := exchange(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_topics","arguments":{"q":"recursion"}}}`, 3)
	data, _ := json.Marshal(result["structuredContent"])
	if !strings.Contains(string(data), `"name":"Recursion"`) {
		t.Fatalf("result=%s", data)
	}
	plan := exchange(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"mastered_topics":["functions"],"vark_profile":"visual"}}}`, 4)
	if plan["isError"] == true || handler.Count() != 1 {
		t.Fatalf("plan=%v provider calls=%d", plan, handler.Count())
	}
	data, _ = json.Marshal(plan["structuredContent"])
	if !strings.Contains(string(data), `"items_skipped_mastered":1`) {
		t.Fatalf("mastered history not applied: %s", data)
	}
	if !strings.Contains(string(data), `"topic":"Recursion"`) {
		t.Fatalf("plan=%s", data)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
		var message map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil || message["jsonrpc"] != "2.0" {
			t.Fatalf("non JSON-RPC stdout: %s", scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

}
