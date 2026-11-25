package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type recordingGate struct {
	mu            sync.Mutex
	authenticate  func(http.ResponseWriter, *http.Request) (*http.Request, bool)
	authorize     func(http.ResponseWriter, *http.Request, Operation) bool
	authenticated int
	operations    []Operation
}

func (g *recordingGate) Authenticate(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	g.mu.Lock()
	g.authenticated++
	g.mu.Unlock()
	if g.authenticate != nil {
		return g.authenticate(w, r)
	}
	return r, true
}

func (g *recordingGate) Authorize(w http.ResponseWriter, r *http.Request, op Operation) bool {
	g.mu.Lock()
	g.operations = append(g.operations, op)
	g.mu.Unlock()
	if g.authorize != nil {
		return g.authorize(w, r, op)
	}
	return true
}

func (g *recordingGate) seen() (int, []Operation) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.authenticated, slices.Clone(g.operations)
}

type countingPlanner struct {
	calls atomic.Int32
	next  plannerFunc
}

func (p *countingPlanner) GenerateTopic(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
	p.calls.Add(1)
	if p.next != nil {
		return p.next(ctx, req)
	}
	return successfulPlanner(ctx, req)
}

func remoteHandler(t *testing.T, planner service.Planner, opts RemoteOptions) http.Handler {
	t.Helper()
	h, err := NewRemoteHandler(streamableConfig(t), graph.StaticCurriculum(graph.NewInMemoryGraph()), planner, streamableLogger(), "test-version", opts)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRemoteHandlerRequiresAGate(t *testing.T) {
	if _, err := NewRemoteHandler(streamableConfig(t), graph.StaticCurriculum(graph.NewInMemoryGraph()), &countingPlanner{}, streamableLogger(), "test-version", RemoteOptions{}); err == nil {
		t.Fatal("a remote handler was built without a gate")
	}
}

func TestRemoteHandlerNegotiatesTheResponseFormat(t *testing.T) {
	srv := httptest.NewServer(remoteHandler(t, &countingPlanner{}, RemoteOptions{Gate: &recordingGate{}}))
	t.Cleanup(srv.Close)
	for _, tc := range []struct{ accept, want string }{
		{"application/json, text/event-stream", "application/json"},
		{"text/event-stream, application/json", "application/json"},
		{"text/event-stream;q=0.9, application/json;q=0.5", "text/event-stream"},
		{"application/json;q=0.5, text/event-stream", "text/event-stream"},
		{"application/json;q=0.9, text/event-stream;q=0.9", "application/json"},
		{"*/*;q=0.1, application/json, text/event-stream;q=0.8", "application/json"},
	} {
		req := streamableRequest(t, srv.URL, "tools/list", "", streamableMessage("tools/list", julyProtocol, `{}`))
		req.Header.Set("Accept", tc.accept)
		status, headers, body := streamableHTTPReply(t, srv.Client(), req)
		if status != http.StatusOK || headers.Get("Content-Type") != tc.want || !strings.Contains(body, "list_topics") {
			t.Errorf("Accept %q: status=%d type=%q body=%.80s", tc.accept, status, headers.Get("Content-Type"), body)
		}
	}
}

func TestOriginPolicy(t *testing.T) {
	policy := NewOriginPolicy("https://app.example.test", "https://admin.example.test:8443")
	for _, tc := range []struct {
		name   string
		values []string
		want   bool
	}{
		{name: "absent", want: true},
		{name: "first", values: []string{"https://app.example.test"}, want: true},
		{name: "second", values: []string{"https://admin.example.test:8443"}, want: true},
		{name: "other", values: []string{"https://other.example.test"}},
		{name: "plain HTTP", values: []string{"http://app.example.test"}},
		{name: "different port", values: []string{"https://app.example.test:8443"}},
		{name: "uppercase", values: []string{"https://APP.example.test"}},
		{name: "trailing slash", values: []string{"https://app.example.test/"}},
		{name: "path", values: []string{"https://app.example.test/x"}},
		{name: "null", values: []string{"null"}},
		{name: "empty", values: []string{""}},
		{name: "list", values: []string{"https://app.example.test, https://admin.example.test:8443"}},
		{name: "repeated equal", values: []string{"https://app.example.test", "https://app.example.test"}},
		{name: "repeated different", values: []string{"https://app.example.test", "https://other.example.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			for _, value := range tc.values {
				header.Add("Origin", value)
			}
			if got := policy.Allows(header); got != tc.want {
				t.Fatalf("Allows(%q) = %v, want %v", tc.values, got, tc.want)
			}
		})
	}
	if NewOriginPolicy().Allows(http.Header{"Origin": {"https://app.example.test"}}) {
		t.Fatal("an empty policy allowed a browser origin")
	}
	if !NewOriginPolicy().Allows(http.Header{}) {
		t.Fatal("an empty policy rejected a native client")
	}
}

func TestRemoteHandlerAuthenticatesBeforeReadingTheBody(t *testing.T) {
	gate := &recordingGate{authenticate: func(w http.ResponseWriter, _ *http.Request) (*http.Request, bool) {
		w.WriteHeader(http.StatusUnauthorized)
		return nil, false
	}}
	planner := &countingPlanner{}
	h := remoteHandler(t, planner, RemoteOptions{Gate: gate})
	req := streamableRequest(t, "https://mcp.example.test/mcp", "tools/list", "", streamableMessage("tools/list", julyProtocol, `{}`))
	req.Body = failingBody{t}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if authenticated, operations := gate.seen(); rec.Code != http.StatusUnauthorized || authenticated != 1 || len(operations) != 0 || planner.calls.Load() != 0 {
		t.Fatalf("status=%d authenticated=%d operations=%v", rec.Code, authenticated, operations)
	}
}

type failingBody struct{ t *testing.T }

func (b failingBody) Read([]byte) (int, error) {
	b.t.Error("the handler read the body of an unauthenticated request")
	return 0, errors.New("unexpected read")
}
func (failingBody) Close() error { return nil }

func TestRemoteHandlerRoutesGetAndOtherMethodsWithoutAuthentication(t *testing.T) {
	gate := &recordingGate{}
	h := remoteHandler(t, &countingPlanner{}, RemoteOptions{Gate: gate})
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodPut, http.MethodOptions, http.MethodPatch} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, "https://mcp.example.test/mcp", nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: status=%d allow=%q", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if authenticated, _ := gate.seen(); authenticated != 0 {
		t.Fatalf("a rejected method reached authentication %d times", authenticated)
	}
}

func TestRemoteHandlerAuthorizesDecodedOperations(t *testing.T) {
	both := []string{ScopeGraphRead, ScopePlanGenerate}
	for _, tc := range []struct {
		method, tool, params string
		want                 Operation
	}{
		{method: "server/discover", params: `{}`, want: Operation{Method: "server/discover", Scopes: both}},
		{method: "tools/list", params: `{}`, want: Operation{Method: "tools/list", Scopes: both}},
		{method: "tools/call", tool: "list_topics", params: `{"name":"list_topics","arguments":{}}`, want: Operation{Method: "tools/call", Tool: "list_topics", Scopes: []string{ScopeGraphRead}}},
		{method: "tools/call", tool: "get_topic_path", params: `{"name":"get_topic_path","arguments":{"topic":"Recursion"}}`, want: Operation{Method: "tools/call", Tool: "get_topic_path", Scopes: []string{ScopeGraphRead}}},
		{method: "tools/call", tool: "get_cognitive_graph", params: `{"name":"get_cognitive_graph","arguments":{"topics":["Recursion"]}}`, want: Operation{Method: "tools/call", Tool: "get_cognitive_graph", Scopes: []string{ScopeGraphRead}}},
		{method: "tools/call", tool: "generate_study_plan", params: `{"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"vark_profile":"visual"}}`, want: Operation{Method: "tools/call", Tool: "generate_study_plan", Scopes: []string{ScopePlanGenerate}}},
	} {
		t.Run(tc.method+" "+tc.tool, func(t *testing.T) {
			gate := &recordingGate{}
			srv := httptest.NewServer(remoteHandler(t, &countingPlanner{}, RemoteOptions{Gate: gate}))
			t.Cleanup(srv.Close)
			req := streamableRequest(t, srv.URL, tc.method, tc.tool, streamableMessage(tc.method, julyProtocol, tc.params))
			status, _, body := streamableHTTPReply(t, srv.Client(), req)
			if status != http.StatusOK || strings.Contains(body, `"error":{"code"`) {
				t.Fatalf("status=%d body=%s", status, body)
			}
			if authenticated, operations := gate.seen(); authenticated != 1 || len(operations) != 1 || !operationEqual(operations[0], tc.want) {
				t.Fatalf("authenticated=%d operations=%+v, want %+v", authenticated, operations, tc.want)
			}
		})
	}
}

func operationEqual(a, b Operation) bool {
	return a.Method == b.Method && a.Tool == b.Tool && slices.Equal(a.Scopes, b.Scopes)
}

func TestRemoteHandlerRejectsUnknownOperationsBeforeAuthorization(t *testing.T) {
	const args = `{"name":"delete_everything","arguments":{}}`
	for _, tc := range []struct {
		name, method, tool, body string
		code                     int64
	}{
		{name: "unknown tool", method: "tools/call", tool: "delete_everything", body: streamableMessage("tools/call", julyProtocol, args), code: -32602},
		{name: "tool in another case", method: "tools/call", tool: "LIST_TOPICS", body: streamableMessage("tools/call", julyProtocol, `{"name":"LIST_TOPICS","arguments":{}}`), code: -32602},
		{name: "subscription stream", method: "subscriptions/listen", body: streamableMessage("subscriptions/listen", julyProtocol, `{}`), code: -32601},
		{name: "resource listing", method: "resources/list", body: streamableMessage("resources/list", julyProtocol, `{}`), code: -32601},
		{name: "prompt listing", method: "prompts/list", body: streamableMessage("prompts/list", julyProtocol, `{}`), code: -32601},
		{name: "completion", method: "completion/complete", body: streamableMessage("completion/complete", julyProtocol, `{}`), code: -32601},
		{name: "removed ping", method: "ping", body: streamableMessage("ping", julyProtocol, `{}`), code: -32601},
		{name: "removed initialize", method: "initialize", body: streamableMessage("initialize", julyProtocol, `{}`), code: -32601},
		{name: "notification", method: "notifications/progress", body: `{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`, code: -32601},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &recordingGate{}
			planner := &countingPlanner{}
			srv := httptest.NewServer(remoteHandler(t, planner, RemoteOptions{Gate: gate}))
			t.Cleanup(srv.Close)
			status, _, body := streamableHTTPReply(t, srv.Client(), streamableRequest(t, srv.URL, tc.method, tc.tool, tc.body))
			var failure struct {
				Error struct{ Code int64 } `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &failure); err != nil || status != http.StatusBadRequest || failure.Error.Code != tc.code {
				t.Fatalf("status=%d body=%s", status, body)
			}
			if _, operations := gate.seen(); len(operations) != 0 || planner.calls.Load() != 0 {
				t.Fatalf("operations=%v planner calls=%d", operations, planner.calls.Load())
			}
		})
	}
}

func TestRemoteHandlerChecksHeadersBeforeAuthorization(t *testing.T) {
	const args = `{"name":"list_topics","arguments":{}}`
	valid := streamableMessage("tools/call", julyProtocol, args)
	for _, tc := range []struct {
		name  string
		alter func(*http.Request)
		body  string
		code  int64
	}{
		{name: "method mismatch", body: valid, alter: setStreamableHeader("Mcp-Method", "tools/list")},
		{name: "name mismatch", body: valid, alter: setStreamableHeader("Mcp-Name", "generate_study_plan")},
		{name: "missing version", body: valid, alter: setStreamableHeader("Mcp-Protocol-Version", "")},
		{name: "unsupported version", body: streamableMessage("tools/call", "2099-01-01", args), alter: setStreamableHeader("Mcp-Protocol-Version", "2099-01-01"), code: -32001},
		{name: "malformed envelope", body: `{"jsonrpc":"1.0","id":1,"method":"tools/call"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &recordingGate{}
			srv := httptest.NewServer(remoteHandler(t, &countingPlanner{}, RemoteOptions{Gate: gate}))
			t.Cleanup(srv.Close)
			req := streamableRequest(t, srv.URL, "tools/call", "list_topics", tc.body)
			if tc.alter != nil {
				tc.alter(req)
			}
			if status, _, body := streamableHTTPReply(t, srv.Client(), req); status != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", status, body)
			}
			if authenticated, operations := gate.seen(); authenticated != 1 || len(operations) != 0 {
				t.Fatalf("authenticated=%d operations=%v", authenticated, operations)
			}
		})
	}
}

func TestRemoteHandlerStopsWhenAuthorizationFails(t *testing.T) {
	gate := &recordingGate{authorize: func(w http.ResponseWriter, _ *http.Request, _ Operation) bool {
		w.WriteHeader(http.StatusForbidden)
		return false
	}}
	planner := &countingPlanner{}
	srv := httptest.NewServer(remoteHandler(t, planner, RemoteOptions{Gate: gate}))
	t.Cleanup(srv.Close)
	body := streamableMessage("tools/call", julyProtocol, `{"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"vark_profile":"visual"}}`)
	status, _, _ := streamableHTTPReply(t, srv.Client(), streamableRequest(t, srv.URL, "tools/call", "generate_study_plan", body))
	if status != http.StatusForbidden || planner.calls.Load() != 0 {
		t.Fatalf("status=%d planner calls=%d", status, planner.calls.Load())
	}
}

func TestRemoteHandlerOriginPolicyAppliesAtEveryLayer(t *testing.T) {
	const allowed = "https://app.example.test"
	gate := &recordingGate{}
	srv := httptest.NewServer(remoteHandler(t, &countingPlanner{}, RemoteOptions{Gate: gate, Origins: NewOriginPolicy(allowed)}))
	t.Cleanup(srv.Close)
	body := streamableMessage("tools/list", julyProtocol, `{}`)
	for _, tc := range []struct {
		name   string
		alter  func(*http.Request)
		status int
	}{
		{name: "native client without an origin", status: http.StatusOK},
		{name: "configured origin", alter: setStreamableHeader("Origin", allowed), status: http.StatusOK},
		{name: "configured origin from another site", alter: func(r *http.Request) {
			r.Header.Set("Origin", allowed)
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}, status: http.StatusOK},
		{name: "configured origin from the same site", alter: func(r *http.Request) {
			r.Header.Set("Origin", allowed)
			r.Header.Set("Sec-Fetch-Site", "same-site")
		}, status: http.StatusOK},
		{name: "unconfigured origin", alter: setStreamableHeader("Origin", "https://other.example.test"), status: http.StatusForbidden},
		{name: "origin matching only the request host", alter: func(r *http.Request) { r.Header.Set("Origin", "http://"+r.Host) }, status: http.StatusForbidden},
		{name: "opaque origin", alter: setStreamableHeader("Origin", "null"), status: http.StatusForbidden},
		{name: "empty origin", alter: setStreamableHeader("Origin", ""), status: http.StatusForbidden},
		{name: "repeated origin", alter: func(r *http.Request) {
			r.Header.Add("Origin", allowed)
			r.Header.Add("Origin", allowed)
		}, status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := gate.seen()
			req := streamableRequest(t, srv.URL, "tools/list", "", body)
			if tc.alter != nil {
				tc.alter(req)
			}
			status, _, reply := streamableHTTPReply(t, srv.Client(), req)
			after, _ := gate.seen()
			if status != tc.status {
				t.Fatalf("status=%d want %d body=%s", status, tc.status, reply)
			}
			if (after-before == 1) != (tc.status == http.StatusOK) {
				t.Fatalf("authentication calls = %d for status %d", after-before, status)
			}
		})
	}
}

func TestRemoteHandlerAppliesTheRequestDeadline(t *testing.T) {
	deadline := make(chan time.Duration, 1)
	planner := &countingPlanner{next: func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		if at, ok := ctx.Deadline(); ok {
			deadline <- time.Until(at)
		} else {
			deadline <- -1
		}
		return successfulPlanner(ctx, req)
	}}
	srv := httptest.NewServer(remoteHandler(t, planner, RemoteOptions{Gate: &recordingGate{}, RequestTimeout: 3 * time.Second}))
	t.Cleanup(srv.Close)
	body := streamableMessage("tools/call", julyProtocol, `{"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"vark_profile":"visual"}}`)
	if status, _, reply := streamableHTTPReply(t, srv.Client(), streamableRequest(t, srv.URL, "tools/call", "generate_study_plan", body)); status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, reply)
	}
	// The configured tool budget is 20 seconds, so a shorter deadline comes from the handler.
	if got := <-deadline; got <= 0 || got > 3*time.Second {
		t.Fatalf("tool deadline in %s, want within the 3 s request timeout", got)
	}
}

func TestRemoteHandlerBoundsBodyReads(t *testing.T) {
	srv := httptest.NewServer(remoteHandler(t, &countingPlanner{}, RemoteOptions{Gate: &recordingGate{}, ReadTimeout: 200 * time.Millisecond}))
	t.Cleanup(srv.Close)
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	request := "POST / HTTP/1.1\r\nHost: " + srv.Listener.Addr().String() + "\r\nContent-Type: application/json\r\n" +
		"Accept: application/json, text/event-stream\r\nMcp-Protocol-Version: " + julyProtocol + "\r\nMcp-Method: tools/list\r\n" +
		"Content-Length: 1000\r\n\r\n{\"jsonrpc\""
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("a stalled body was not answered: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408", resp.StatusCode)
	}
}

func TestRemoteHandlerDrainsToolCalls(t *testing.T) {
	lifecycle := NewLifecycle(t.Context())
	planner := &countingPlanner{}
	srv := httptest.NewServer(remoteHandler(t, planner, RemoteOptions{Gate: &recordingGate{}, Lifecycle: lifecycle}))
	t.Cleanup(srv.Close)
	call := func(tool, args string) string {
		t.Helper()
		body := streamableMessage("tools/call", julyProtocol, `{"name":"`+tool+`","arguments":`+args+`}`)
		status, _, reply := streamableHTTPReply(t, srv.Client(), streamableRequest(t, srv.URL, "tools/call", tool, body))
		if status != http.StatusOK {
			t.Fatalf("status=%d body=%s", status, reply)
		}
		return reply
	}
	plan := `{"failed_topics":["Recursion"],"vark_profile":"visual"}`
	if reply := call("generate_study_plan", plan); strings.Contains(reply, `"isError":true`) || planner.calls.Load() != 1 {
		t.Fatalf("admitted call failed: %s", reply)
	}
	<-lifecycle.StopAccepting()
	if reply := call("generate_study_plan", plan); !strings.Contains(reply, `"isError":true`) || planner.calls.Load() != 1 {
		t.Fatalf("a call after draining ran: %s planner calls=%d", reply, planner.calls.Load())
	}
	list := streamableMessage("tools/list", julyProtocol, `{}`)
	if status, _, _ := streamableHTTPReply(t, srv.Client(), streamableRequest(t, srv.URL, "tools/list", "", list)); status != http.StatusOK {
		t.Fatalf("listing while draining status = %d", status)
	}
}

func TestRemoteHandlerUsesTheSDKClientWithAGate(t *testing.T) {
	gate := &recordingGate{authenticate: func(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return nil, false
		}
		return r, true
	}}
	srv := httptest.NewServer(remoteHandler(t, &countingPlanner{}, RemoteOptions{Gate: gate}))
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "1"}, nil)
	if _, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: srv.Client(), MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: julyProtocol}); err == nil {
		t.Fatal("an unauthenticated client connected")
	}
	transport := &mcp.StreamableClientTransport{Endpoint: srv.URL, MaxRetries: -1, HTTPClient: &http.Client{Transport: bearerTransport{base: srv.Client().Transport, token: "synthetic"}}}
	cs, err := client.Connect(t.Context(), transport, &mcp.ClientSessionOptions{ProtocolVersion: julyProtocol})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if tools, err := cs.ListTools(t.Context(), nil); err != nil || len(tools.Tools) != 4 {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}
