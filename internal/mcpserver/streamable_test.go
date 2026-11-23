package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

const julyProtocol = "2026-07-28"

func streamableConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.LoadMCP(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func streamableLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func connectStreamableClient(t *testing.T, endpoint string, hc *http.Client) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: hc, MaxRetries: -1,
	}, &mcp.ClientSessionOptions{ProtocolVersion: julyProtocol})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if result := cs.InitializeResult(); result == nil || result.ProtocolVersion != julyProtocol || result.Capabilities.Tools == nil ||
		result.ServerInfo == nil || result.ServerInfo.Name != "axiom-engine" || result.ServerInfo.Version != "test-version" {
		t.Fatalf("discovery result = %+v", result)
	}
	return cs
}

func TestStreamableHTTPTools(t *testing.T) {
	for _, mediaType := range []string{"application/json", "text/event-stream"} {
		t.Run(mediaType, func(t *testing.T) {
			h := NewStreamableHTTPHandler(streamableConfig(t), graph.NewInMemoryGraph(), plannerFunc(successfulPlanner), streamableLogger(), "test-version", mediaType == "application/json")
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			cs := connectStreamableClient(t, srv.URL, srv.Client())
			local := session(t, successfulPlanner)
			got, err := cs.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			want, err := local.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Tools) != 4 || !reflect.DeepEqual(got.Tools, want.Tools) {
				t.Fatalf("HTTP tools = %+v, local tools = %+v", got.Tools, want.Tools)
			}
			for _, call := range []*mcp.CallToolParams{
				{Name: "generate_study_plan", Arguments: PlanInput{FailedTopics: []string{"Recursion"}, VARKProfile: "visual", StudentID: "student-http"}},
				{Name: "get_cognitive_graph", Arguments: CognitiveGraphInput{Topics: []string{"Recursion"}}},
				{Name: "list_topics", Arguments: ListTopicsInput{Q: "Recursion"}},
				{Name: "get_topic_path", Arguments: TopicPathInput{Topic: "Recursion"}},
				{Name: "get_topic_path", Arguments: TopicPathInput{Topic: "unknown topic"}},
			} {
				got, err := cs.CallTool(t.Context(), call)
				if err != nil {
					t.Fatalf("%s: %v", call.Name, err)
				}
				want, err := local.CallTool(t.Context(), call)
				if err != nil {
					t.Fatalf("%s: %v", call.Name, err)
				}
				if call.Name == "generate_study_plan" {
					normalizeStreamablePlanTiming(t, got)
					normalizeStreamablePlanTiming(t, want)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: HTTP result = %+v, local result = %+v", call.Name, got, want)
				}
			}

			req := streamableRequest(t, srv.URL, "server/discover", "", streamableMessage("server/discover", julyProtocol, `{}`))
			req.Header.Set("Origin", srv.URL)
			req.Header.Set("Mcp-Session-Id", "old-session")
			req.Header.Set("Last-Event-ID", "old-cursor")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != mediaType || resp.Header.Get("Mcp-Session-Id") != "" {
				t.Fatalf("discovery status = %d, headers = %v", resp.StatusCode, resp.Header)
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if mediaType == "text/event-stream" {
				data = []byte(streamableEventData(t, string(data)))
			}
			var discovery struct {
				ID     int                `json:"id"`
				Result mcp.DiscoverResult `json:"result"`
			}
			if err := json.Unmarshal(data, &discovery); err != nil || discovery.ID != 1 || !slices.Equal(discovery.Result.SupportedVersions, []string{julyProtocol}) {
				t.Fatalf("discovery body = %s, error = %v", data, err)
			}
		})
	}
}

func TestStreamableHTTPRejectsInvalidRequests(t *testing.T) {
	var calls atomic.Int32
	planner := plannerFunc(func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return successfulPlanner(ctx, req)
	})
	h := NewStreamableHTTPHandler(streamableConfig(t), graph.NewInMemoryGraph(), planner, streamableLogger(), "test-version", false)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	const tool, method = "generate_study_plan", "tools/call"
	const args = `{"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"vark_profile":"visual"}}`
	const future = "2099-01-01"
	valid := streamableMessage(method, julyProtocol, args)
	for _, tc := range []struct {
		name   string
		body   string
		alter  func(*http.Request)
		status int
		code   int64
	}{
		{name: "version mismatch", body: streamableMessage(method, future, args), status: 400, code: mcp.CodeHeaderMismatch},
		{name: "earlier version header mismatch", body: valid, alter: setStreamableHeader("Mcp-Protocol-Version", "2025-11-25"), status: 400, code: mcp.CodeHeaderMismatch},
		{name: "method mismatch", body: valid, alter: setStreamableHeader("Mcp-Method", "tools/list"), status: 400, code: mcp.CodeHeaderMismatch},
		{name: "name mismatch", body: valid, alter: setStreamableHeader("Mcp-Name", "list_topics"), status: 400, code: mcp.CodeHeaderMismatch},
		{name: "repeated method", body: valid, alter: func(r *http.Request) { r.Header.Add("Mcp-Method", "tools/list") }, status: 400, code: mcp.CodeHeaderMismatch},
		{name: "repeated name", body: valid, alter: func(r *http.Request) { r.Header.Add("Mcp-Name", "list_topics") }, status: 400, code: mcp.CodeHeaderMismatch},
		{name: "malformed encoded name", body: valid, alter: setStreamableHeader("Mcp-Name", "=?base64?!?="), status: 400, code: mcp.CodeHeaderMismatch},
		{name: "repeated version", body: valid, alter: func(r *http.Request) { r.Header.Add("Mcp-Protocol-Version", future) }, status: 400, code: mcp.CodeHeaderMismatch},
		{name: "missing version", body: valid, alter: setStreamableHeader("Mcp-Protocol-Version", ""), status: 400, code: mcp.CodeHeaderMismatch},
		{name: "missing version in both places", body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + args + `}`, alter: setStreamableHeader("Mcp-Protocol-Version", ""), status: 400, code: mcp.CodeHeaderMismatch},
		{name: "missing body version", body: strings.Replace(valid, `"io.modelcontextprotocol/protocolVersion":"2026-07-28",`, "", 1), status: 400, code: -32602},
		{name: "numeric body version", body: strings.Replace(valid, `"io.modelcontextprotocol/protocolVersion":"2026-07-28"`, `"io.modelcontextprotocol/protocolVersion":20260728`, 1), status: 400, code: -32602},
		{name: "unsupported future version", body: streamableMessage(method, future, args), alter: setStreamableHeader("Mcp-Protocol-Version", future), status: 400, code: mcp.CodeUnsupportedProtocolVersion},
		{name: "unsupported earlier version", body: streamableMessage(method, "2025-11-25", args), alter: setStreamableHeader("Mcp-Protocol-Version", "2025-11-25"), status: 400, code: mcp.CodeUnsupportedProtocolVersion},
		{name: "malformed JSON", body: valid[:len(valid)-1], status: 400},
		{name: "concatenated messages", body: valid + valid, status: 400},
		{name: "batch", body: "[" + valid + "]", status: 400},
		{name: "client response", body: `{"jsonrpc":"2.0","id":1,"result":{}}`, status: 400},
		{name: "body limit", body: valid + strings.Repeat(" ", mcp.DefaultMaxRequestBodyBytes), status: 413},
		{name: "foreign origin", body: valid, alter: setStreamableHeader("Origin", "http://other.example"), status: 403},
		{name: "wrong origin scheme", body: valid, alter: func(r *http.Request) { r.Header.Set("Origin", "https://"+r.Host) }, status: 403},
		{name: "empty origin", body: valid, alter: setStreamableHeader("Origin", ""), status: 403},
		{name: "opaque origin", body: valid, alter: setStreamableHeader("Origin", "null"), status: 403},
		{name: "multiple origins", body: valid, alter: func(r *http.Request) {
			r.Header.Add("Origin", "http://"+r.Host)
			r.Header.Add("Origin", "http://other.example")
		}, status: 403},
		{name: "get", alter: func(r *http.Request) { r.Method = http.MethodGet }, status: 405},
		{name: "delete", alter: func(r *http.Request) { r.Method = http.MethodDelete }, status: 405},
		{name: "options", alter: func(r *http.Request) { r.Method = http.MethodOptions }, status: 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := streamableRequest(t, srv.URL, method, tool, tc.body)
			if tc.alter != nil {
				tc.alter(req)
			}
			status, headers, body := streamableHTTPReply(t, srv.Client(), req)
			if status != tc.status {
				t.Fatalf("status = %d, want %d, body = %.256s", status, tc.status, body)
			}
			if tc.status == http.StatusMethodNotAllowed && headers.Get("Allow") != http.MethodPost {
				t.Fatalf("Allow = %q, want POST", headers.Get("Allow"))
			}
			if tc.code == 0 {
				return
			}
			var result struct {
				ID    int `json:"id"`
				Error struct {
					Code int64                              `json:"code"`
					Data mcp.UnsupportedProtocolVersionData `json:"data"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &result); err != nil || result.ID != 1 || result.Error.Code != tc.code {
				t.Fatalf("error response = %s, want code %d, decode error = %v", body, tc.code, err)
			}
			if tc.code == mcp.CodeUnsupportedProtocolVersion && (!slices.Equal(result.Error.Data.Supported, []string{julyProtocol}) || result.Error.Data.Requested != req.Header.Get("Mcp-Protocol-Version")) {
				t.Fatalf("unsupported version data = %+v", result.Error.Data)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("invalid requests reached the planner %d times", got)
	}
}

func TestStreamableHTTPEncodedName(t *testing.T) {
	h := NewStreamableHTTPHandler(streamableConfig(t), graph.NewInMemoryGraph(), plannerFunc(successfulPlanner), streamableLogger(), "test-version", true)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	const method, name = "tools/call", "list_topics"
	req := streamableRequest(t, srv.URL, method, "=?base64?bGlzdF90b3BpY3M=?=", streamableMessage(method, julyProtocol, `{"name":"list_topics","arguments":{"q":"Recursion"}}`))
	status, _, body := streamableHTTPReply(t, srv.Client(), req)
	if status != http.StatusOK || !strings.Contains(body, `"name":"Recursion"`) {
		t.Fatalf("encoded name status = %d, body = %s", status, body)
	}
}

func TestStreamableHTTPRequestIDs(t *testing.T) {
	for _, jsonResponse := range []bool{true, false} {
		t.Run(fmt.Sprintf("json=%t", jsonResponse), func(t *testing.T) {
			h := NewStreamableHTTPHandler(streamableConfig(t), graph.NewInMemoryGraph(), plannerFunc(successfulPlanner), streamableLogger(), "test-version", jsonResponse)
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			for _, tc := range []struct {
				name, id, wantID, method, header string
				status                           int
				code                             int64
				toolError                        bool
			}{
				{"large success", "9007199254740993", "9007199254740993", "tools/list", "tools/list", 200, 0, false},
				{"maximum int64", "9223372036854775807", "9223372036854775807", "tools/list", "tools/list", 200, 0, false},
				{"integer in exponent notation", "1e3", "1e3", "tools/list", "tools/list", 200, 0, false},
				{"integer in decimal notation", "1.0", "1.0", "tools/list", "tools/list", 200, 0, false},
				{"large exponent integer", "1e21", "1e21", "tools/list", "tools/list", 200, 0, false},
				{"integer with negative exponent", "1200.00E-2", "1200.00E-2", "tools/list", "tools/list", 200, 0, false},
				{"scaled fractional notation", "1.25e+2", "1.25e+2", "tools/list", "tools/list", 200, 0, false},
				{"zero with large exponent", "0.0e-99999999999999999999", "0.0e-99999999999999999999", "tools/list", "tools/list", 200, 0, false},
				{"integer with exponent beyond int64", "1e99999999999999999999", "1e99999999999999999999", "tools/list", "tools/list", 200, 0, false},
				{"large tool error", "-9007199254740993", "-9007199254740993", "tools/call", "tools/call", 200, 0, true},
				{"large SDK error", "-9007199254740993", "-9007199254740993", "unknown/method", "unknown/method", 404, -32601, false},
				{"large metadata error", "9007199254740993", "9007199254740993", "tools/list", "tools/call", 400, mcp.CodeHeaderMismatch, false},
				{"fractional", "1.5", "", "tools/list", "tools/list", 400, -32600, false},
				{"fraction near integer", "9007199254740993.1", "", "tools/list", "tools/list", 400, -32600, false},
				{"negative exponent fraction", "1200.00E-3", "", "tools/list", "tools/list", 400, -32600, false},
				{"fraction with exponent beyond int64", "1e-99999999999999999999", "", "tools/list", "tools/list", 400, -32600, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					name, args := "", `{}`
					if tc.toolError {
						name, args = "get_topic_path", `{"name":"get_topic_path","arguments":{"topic":"unknown topic"}}`
					}
					message := strings.Replace(streamableMessage(tc.method, julyProtocol, args), `"id":1`, `"id":`+tc.id, 1)
					req := streamableRequest(t, srv.URL, tc.header, name, message)
					status, headers, body := streamableHTTPReply(t, srv.Client(), req)
					if status != tc.status {
						t.Fatalf("status = %d, want %d, body = %.256s", status, tc.status, body)
					}
					mediaType := headers.Get("Content-Type")
					if status == 200 && !jsonResponse {
						if mediaType != "text/event-stream" {
							t.Fatalf("content type = %q", mediaType)
						}
						body = streamableEventData(t, body)
					} else if mediaType != "application/json" {
						t.Fatalf("content type = %q", mediaType)
					}
					var reply struct {
						ID     json.RawMessage        `json:"id"`
						Error  struct{ Code int64 }   `json:"error"`
						Result struct{ IsError bool } `json:"result"`
					}
					if err := json.Unmarshal([]byte(body), &reply); err != nil || string(reply.ID) != tc.wantID || reply.Error.Code != tc.code || reply.Result.IsError != tc.toolError {
						t.Fatalf("reply = %.256s, want id %s, error code %d and tool error %t, decode error = %v", body, tc.wantID, tc.code, tc.toolError, err)
					}
				})
			}
		})
	}
}

func TestStreamableHTTPNotificationErrorOmitsID(t *testing.T) {
	h := NewStreamableHTTPHandler(streamableConfig(t), graph.NewInMemoryGraph(), plannerFunc(successfulPlanner), streamableLogger(), "test-version", false)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	req := streamableRequest(t, srv.URL, "notifications/other", "", `{"jsonrpc":"2.0","method":"notifications/example"}`)
	status, headers, body := streamableHTTPReply(t, srv.Client(), req)
	var reply struct {
		JSONRPC string               `json:"jsonrpc"`
		ID      json.RawMessage      `json:"id"`
		Error   struct{ Code int64 } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil || status != http.StatusBadRequest || headers.Get("Content-Type") != "application/json" ||
		reply.JSONRPC != "2.0" || reply.ID != nil || reply.Error.Code != mcp.CodeHeaderMismatch {
		t.Fatalf("notification error status = %d, body = %s, decode error = %v", status, body, err)
	}
}

func TestStreamableHTTPSubscriptionCorrelation(t *testing.T) {
	for _, jsonResponse := range []bool{true, false} {
		t.Run(fmt.Sprintf("json=%t", jsonResponse), func(t *testing.T) {
			h := NewStreamableHTTPHandler(streamableConfig(t), graph.NewInMemoryGraph(), plannerFunc(successfulPlanner), streamableLogger(), "test-version", jsonResponse)
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			for _, id := range []string{"7", "9007199254740993", "1e21"} {
				t.Run(id, func(t *testing.T) {
					const method = "subscriptions/listen"
					message := strings.Replace(streamableMessage(method, julyProtocol, `{"notifications":{}}`), `"id":1`, `"id":`+id, 1)
					req := streamableRequest(t, srv.URL, method, "", message)
					status, headers, body := streamableHTTPReply(t, srv.Client(), req)
					events := strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n")
					if status != http.StatusOK || headers.Get("Content-Type") != "text/event-stream" || len(events) != 2 {
						t.Fatalf("subscription status = %d, headers = %v, body = %s", status, headers, body)
					}
					type payload struct {
						Meta map[string]json.RawMessage `json:"_meta"`
					}
					var ack struct {
						Method string  `json:"method"`
						Params payload `json:"params"`
					}
					var final struct {
						ID     json.RawMessage `json:"id"`
						Result payload         `json:"result"`
					}
					if err := json.Unmarshal([]byte(streamableEventData(t, events[0]+"\n\n")), &ack); err != nil || ack.Method != "notifications/subscriptions/acknowledged" || string(ack.Params.Meta[mcp.MetaKeySubscriptionID]) != id {
						t.Errorf("acknowledgement = %s, want subscription ID %s, decode error = %v", events[0], id, err)
					}
					if err := json.Unmarshal([]byte(streamableEventData(t, events[1]+"\n\n")), &final); err != nil || string(final.ID) != id || string(final.Result.Meta[mcp.MetaKeySubscriptionID]) != id {
						t.Errorf("final event = %s, want request and subscription IDs %s, decode error = %v", events[1], id, err)
					}
				})
			}
		})
	}
}

func streamableHTTPReply(t *testing.T, client *http.Client, req *http.Request) (int, http.Header, string) {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(data)
}

func normalizeStreamablePlanTiming(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	structured, ok := result.StructuredContent.(map[string]any)
	if result.IsError || !ok || len(result.Content) != 1 {
		t.Fatalf("invalid plan result = %+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("plan content = %+v", result.Content)
	}
	var decoded any
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil || !reflect.DeepEqual(decoded, structured) {
		t.Fatalf("plan text = %s, structured content = %+v, error = %v", text.Text, structured, err)
	}
	meta, ok := structured["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("invalid plan metadata = %+v", structured["_meta"])
	}
	for _, field := range []string{"llm_latency_ms", "total_latency_ms"} {
		if value, ok := meta[field].(float64); !ok || value < 0 {
			t.Fatalf("%s = %+v", field, meta[field])
		}
		delete(meta, field)
	}
	data, err := json.Marshal(structured)
	if err != nil {
		t.Fatal(err)
	}
	text.Text = string(data)
}

func streamableRequest(t *testing.T, endpoint, method, name, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", julyProtocol)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	return req
}

func streamableMessage(method, version, params string) string {
	prefix := strings.TrimSuffix(params, "}")
	if prefix != "{" {
		prefix += ","
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s"_meta":{"io.modelcontextprotocol/protocolVersion":%q,"io.modelcontextprotocol/clientInfo":{"name":"wire-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, method, prefix, version)
}

func setStreamableHeader(key, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

func streamableEventData(t *testing.T, event string) string {
	t.Helper()
	data, ok := strings.CutPrefix(event, "event: message\ndata: ")
	if !ok || !strings.HasSuffix(data, "\n\n") || strings.Count(data, "\n") != 2 {
		t.Fatalf("invalid SSE message = %q", event)
	}
	return strings.TrimSuffix(data, "\n\n")
}
