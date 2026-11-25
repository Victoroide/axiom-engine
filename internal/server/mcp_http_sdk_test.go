package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

const waitFor = 20 * time.Second

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(waitFor):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func connected(t *testing.T, i *mcpInstance, tenant, caller, scope string) (*mcp.ClientSession, *oauthFlow) {
	t.Helper()
	cs, flow, _ := connectedWithClient(t, i, tenant, caller, scope)
	return cs, flow
}

// connectedWithClient also returns the HTTP client, so a test can close the
// idle connections that Go's transport may leave unused after a dial race.
// The server waits up to five seconds for a connection that never sent a
// request, which would outlast a short shutdown budget.
func connectedWithClient(t *testing.T, i *mcpInstance, tenant, caller, scope string) (*mcp.ClientSession, *oauthFlow, *http.Client) {
	t.Helper()
	flow := i.newFlow(t, tenant, caller, scope)
	client := i.client()
	cs, err := i.connect(t.Context(), flow, client)
	if err != nil {
		t.Fatalf("connect: %v (events %v)", err, flow.seen())
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, flow, client
}

func closeIdle(clients ...*http.Client) {
	for _, client := range clients {
		client.CloseIdleConnections()
	}
}

func TestMCPHTTPSDKDiscoversMetadataAndUsesAResourceToken(t *testing.T) {
	fx := newMCPFixture(t)
	var seen syncBuffer
	planner := &mcpPlanner{next: func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		_, _ = fmt.Fprintf(&seen, "%+v", req)
		return []domain.PlanItem{{Topic: req.Topic, Explanation: "Practice", Resources: []domain.Resource{}}}, nil
	}}
	i := fx.start(t, mcpOptions{planner: planner})
	cs, flow := connected(t, i, privateID, callerID, allScopes)
	if events := flow.seen(); len(events) != 3 || events[0] != "challenge:401 Unauthorized" || events[1] != "metadata:"+mcpMetadataURL || events[2] != "token" {
		t.Fatalf("discovery events = %v", events)
	}
	if resources := fx.issuer.mintedResources(); len(resources) != 1 || resources[0] != mcpPublicURL {
		t.Fatalf("tokens were requested for %v", resources)
	}
	if info := cs.InitializeResult(); info == nil || info.ProtocolVersion != mcpProtocol || info.ServerInfo.Name != "axiom-engine" || info.ServerInfo.Version != "test-version" {
		t.Fatalf("discovery result = %+v", info)
	}
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 4 {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
	for _, call := range []*mcp.CallToolParams{
		{Name: "list_topics", Arguments: map[string]any{"q": "Recursion"}},
		{Name: "get_topic_path", Arguments: map[string]any{"topic": "Recursion"}},
		{Name: "get_cognitive_graph", Arguments: map[string]any{"topics": []string{"Recursion"}}},
		{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "visual", "student_id": "learner-7"}},
	} {
		result, err := cs.CallTool(t.Context(), call)
		if err != nil || result.IsError && call.Name == "generate_study_plan" {
			t.Fatalf("%s: err=%v result=%+v", call.Name, err, result)
		}
	}
	if planner.calls.Load() != 1 {
		t.Fatalf("planner calls = %d", planner.calls.Load())
	}
	for _, secret := range []string{privateID, callerID, flow.token, fx.issuer.issuer} {
		if strings.Contains(seen.String(), secret) || strings.Contains(i.logs.String(), secret) {
			t.Errorf("a prompt input or log line contains %q", secret)
		}
	}
	if !strings.Contains(seen.String(), "Recursion") || strings.Contains(seen.String(), "learner-7") || strings.Contains(i.logs.String(), "learner-7") {
		t.Fatalf("planner input = %s", seen.String())
	}
}

func TestMCPHTTPSDKFollowsTheDiscoveryScopeChallenge(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	flow := i.newFlow(t, privateID, callerID, "profile")
	flow.followScopeChallenge = true
	flow.token = fx.issuer.token(t, mcpPublicURL, privateID, callerID, "profile")
	cs, err := i.connect(t.Context(), flow, i.client())
	if err != nil {
		t.Fatalf("connect after scope challenge: %v (events %v)", err, flow.seen())
	}
	defer func() { _ = cs.Close() }()
	if events := flow.seen(); !slices.Equal(events, []string{"challenge:403 Forbidden", "metadata:" + mcpMetadataURL, "token"}) {
		t.Fatalf("discovery events = %v", events)
	}
	principal, err := fx.issuer.verifier(t, mcpPublicURL).Verify(t.Context(), flow.token)
	if err != nil || !principal.HasScope("graph:read") || principal.HasScope("plan:generate") {
		t.Fatalf("discovery retry should have only graph access: %v", err)
	}
	if tools, err := cs.ListTools(t.Context(), nil); err != nil || len(tools.Tools) != 4 {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
}

func TestMCPHTTPSDKRefusesTokensForOtherResources(t *testing.T) {
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner})
	for name, audience := range map[string]string{"API audience": mcpRESTAud, "sibling path": "https://mcp.example.test/other", "other host": "https://other.example.test/mcp"} {
		t.Run(name, func(t *testing.T) {
			flow := i.newFlow(t, privateID, callerID, allScopes)
			flow.audience = audience
			if cs, err := i.connect(t.Context(), flow, i.client()); err == nil {
				_ = cs.Close()
				t.Fatal("a token for another resource opened a session")
			}
		})
	}
	if planner.calls.Load() != 0 || len(fx.redisKeys()) != 0 {
		t.Fatalf("refused tokens ran %d plans and wrote %v", planner.calls.Load(), fx.redisKeys())
	}
	// The same flow with a token for this resource works, so the refusal came from the audience.
	if cs, _ := connected(t, i, privateID, callerID, allScopes); cs == nil {
		t.Fatal("no session")
	}
}

func TestMCPHTTPSDKScopesLimitTools(t *testing.T) {
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner})
	cs, _ := connected(t, i, privateID, callerID, "graph:read")
	if result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_topics", Arguments: map[string]any{}}); err != nil || result.IsError {
		t.Fatalf("graph tool: err=%v result=%+v", err, result)
	}
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "visual"}}); err == nil {
		t.Fatal("a token without plan:generate ran the model tool")
	}
	if planner.calls.Load() != 0 {
		t.Fatalf("planner calls = %d", planner.calls.Load())
	}
}

func TestMCPHTTPSDKAcceptsRotatedKeysAndRejectsRetiredOnes(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	_, first := connected(t, i, privateID, callerID, allScopes)
	retired := first.token
	if reply := i.call(t, retired, "tools/list", "", `{}`); reply.Status != http.StatusOK {
		t.Fatalf("token before rotation: %d", reply.Status)
	}
	fx.issuer.rotate(t, "mcp-key-2")
	// The verifier may refetch keys once per second.
	time.Sleep(1100 * time.Millisecond)
	next := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	if reply := i.call(t, next, "tools/list", "", `{}`); reply.Status != http.StatusOK {
		t.Fatalf("token from the rotated key: %d %s", reply.Status, reply.Body)
	}
	if reply := i.call(t, retired, "tools/list", "", `{}`); reply.Status != http.StatusUnauthorized {
		t.Fatalf("token from the retired key: %d", reply.Status)
	}
	// An SDK client holding the retired token recovers through the challenge.
	if _, err := first.TokenSource(t.Context()); err != nil {
		t.Fatal(err)
	}
	cs, err := i.connect(t.Context(), first, i.client())
	if err != nil {
		t.Fatalf("a client with a retired token did not recover: %v", err)
	}
	_ = cs.Close()
}

func TestMCPHTTPSDKRejectsUnsupportedProtocolVersions(t *testing.T) {
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	for _, version := range []string{"2099-01-01", "2025-11-25", "2025-06-18"} {
		t.Run(version, func(t *testing.T) {
			req := mcpRequestFor(t, "tools/list", "", `{}`, token)
			body := strings.Replace(mcpMessage("tools/list", `{}`), mcpProtocol, version, 1)
			req.Body, req.ContentLength = nopCloser(body), int64(len(body))
			req.Header.Set("Mcp-Protocol-Version", version)
			reply := i.send(t, i.client(), req)
			var failure struct {
				Error struct {
					Code int64 `json:"code"`
					Data struct {
						Supported []string `json:"supported"`
						Requested string   `json:"requested"`
					} `json:"data"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(reply.Body), &failure); err != nil || reply.Status != http.StatusBadRequest ||
				failure.Error.Code != mcp.CodeUnsupportedProtocolVersion || !slices.Equal(failure.Error.Data.Supported, []string{mcpProtocol}) || failure.Error.Data.Requested != version {
				t.Fatalf("status=%d body=%s", reply.Status, reply.Body)
			}
		})
	}
	// An SDK client limited to an earlier revision cannot open a session.
	flow := i.newFlow(t, privateID, callerID, allScopes)
	client := mcp.NewClient(&mcp.Implementation{Name: "old-client", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: mcpPublicURL, HTTPClient: i.client(), OAuthHandler: flow, MaxRetries: -1},
		&mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err == nil {
		_ = cs.Close()
		t.Fatal("a client limited to 2025-11-25 opened a session")
	}
	if planner.calls.Load() != 0 {
		t.Fatalf("planner calls = %d", planner.calls.Load())
	}
}

func TestMCPHTTPSDKQuotaIsSharedAcrossInstancesAndTransports(t *testing.T) {
	fx := newMCPFixture(t)
	settings := map[string]string{"AXIOM_TENANT_QUOTA_MAX": "4"}
	first := fx.start(t, mcpOptions{settings: settings})
	second := fx.start(t, mcpOptions{settings: settings})
	restApp := New(fx.config(t, settings), Dependencies{Graph: graph.NewInMemoryGraph(), Planner: &mcpPlanner{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	t.Cleanup(func() { _ = restApp.Shutdown() })
	restToken := fx.issuer.token(t, mcpRESTAud, privateID, "rest-caller", "graph:read")
	rest := func() int {
		res, _ := call(t, restApp, http.MethodGet, "/api/v1/topics", "", map[string]string{"Authorization": "Bearer " + restToken})
		return res.StatusCode
	}
	a, _ := connected(t, first, privateID, callerID, allScopes)
	b, _ := connected(t, second, privateID, "second-caller", allScopes)
	other, _ := connected(t, second, "unrelated-tenant", callerID, allScopes)
	list := func(cs *mcp.ClientSession) error {
		_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_topics", Arguments: map[string]any{}})
		return err
	}
	// Four actions from three entry points use the tenant's four units.
	if rest() != http.StatusOK || list(a) != nil || list(b) != nil || rest() != http.StatusOK {
		t.Fatal("the first four actions of the tenant were not admitted")
	}
	if rest() != http.StatusTooManyRequests {
		t.Fatal("the HTTP API ignored the units used through MCP")
	}
	for name, cs := range map[string]*mcp.ClientSession{"first instance": a, "second instance": b} {
		if err := list(cs); err == nil || !strings.Contains(err.Error(), "429") && !strings.Contains(strings.ToLower(err.Error()), "too many") {
			t.Fatalf("%s: tool call over budget = %v", name, err)
		}
	}
	if err := list(other); err != nil {
		t.Fatalf("another tenant was affected: %v", err)
	}
	// Discovery does not use the tenant budget.
	if _, err := a.ListTools(t.Context(), nil); err != nil {
		t.Fatalf("listing tools over budget: %v", err)
	}
	// An API token cannot authorize MCP, and an MCP token cannot authorize the API.
	if reply := first.call(t, restToken, "tools/list", "", `{}`); reply.Status != http.StatusUnauthorized {
		t.Fatalf("API token on MCP: %d", reply.Status)
	}
	mcpToken := fx.issuer.token(t, mcpPublicURL, privateID, "rest-caller", "graph:read")
	if res, _ := call(t, restApp, http.MethodGet, "/api/v1/topics", "", map[string]string{"Authorization": "Bearer " + mcpToken}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("MCP token on the API: %d", res.StatusCode)
	}
}

func TestMCPHTTPSDKGraphToolsRunWhileModelCallsWait(t *testing.T) {
	fx := newMCPFixture(t)
	started, release := make(chan struct{}, 4), make(chan struct{})
	planner := &mcpPlanner{next: func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []domain.PlanItem{{Topic: req.Topic, Explanation: "Practice", Resources: []domain.Resource{}}}, nil
	}}
	i := fx.start(t, mcpOptions{planner: planner, settings: map[string]string{"LLM_MAX_CONCURRENCY": "1"}})
	model, _ := connected(t, i, privateID, callerID, allScopes)
	reader, _ := connected(t, i, privateID, "reader", allScopes)
	planned := make(chan error, 1)
	go func() {
		_, err := model.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "visual"}})
		planned <- err
	}()
	receive(t, started, "the model call to start")
	for _, call := range []*mcp.CallToolParams{
		{Name: "list_topics", Arguments: map[string]any{}},
		{Name: "get_topic_path", Arguments: map[string]any{"topic": "Recursion"}},
		{Name: "get_cognitive_graph", Arguments: map[string]any{"topics": []string{"Recursion"}}},
	} {
		if result, err := reader.CallTool(t.Context(), call); err != nil || result.IsError {
			t.Fatalf("%s while a model call waits: err=%v result=%+v", call.Name, err, result)
		}
	}
	select {
	case err := <-planned:
		t.Fatalf("the model call finished early: %v", err)
	default:
	}
	close(release)
	if err := receive(t, planned, "the model call"); err != nil {
		t.Fatal(err)
	}
}

func TestMCPHTTPSDKCancelsOnlyTheCallerThatLeft(t *testing.T) {
	fx := newMCPFixture(t)
	type seen struct {
		topic    string
		canceled bool
	}
	started, finished := make(chan string, 2), make(chan seen, 2)
	release := make(chan struct{})
	planner := &mcpPlanner{next: func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		started <- req.Topic
		select {
		case <-release:
			finished <- seen{req.Topic, false}
			return []domain.PlanItem{{Topic: req.Topic, Explanation: "Practice", Resources: []domain.Resource{}}}, nil
		case <-ctx.Done():
			finished <- seen{req.Topic, true}
			return nil, ctx.Err()
		}
	}}
	i := fx.start(t, mcpOptions{planner: planner})
	leaver, _ := connected(t, i, privateID, "leaver", allScopes)
	stayer, _ := connected(t, i, privateID, "stayer", allScopes)
	leaveCtx, leave := context.WithCancel(t.Context())
	defer leave()
	results := make(chan error, 2)
	plan := func(ctx context.Context, cs *mcp.ClientSession, topic string) {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{topic}, "vark_profile": "visual"}})
		results <- err
	}
	go plan(leaveCtx, leaver, "Recursion")
	if first := receive(t, started, "the first call"); first != "Recursion" {
		t.Fatalf("first call topic = %s", first)
	}
	go plan(t.Context(), stayer, "Polymorphism")
	receive(t, started, "the second call")
	leave()
	if got := receive(t, finished, "the canceled call"); got != (seen{"Recursion", true}) {
		t.Fatalf("first planner outcome = %+v", got)
	}
	if err := receive(t, results, "the canceled client"); err == nil {
		t.Fatal("the canceled client reported success")
	}
	select {
	case got := <-finished:
		t.Fatalf("the other call ended with %+v when only one client left", got)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if got := receive(t, finished, "the remaining call"); got != (seen{"Polymorphism", false}) {
		t.Fatalf("second planner outcome = %+v", got)
	}
	if err := receive(t, results, "the remaining client"); err != nil {
		t.Fatalf("the remaining client failed: %v", err)
	}
}

func writeLargeGraph(t *testing.T, topics int) *graph.InMemoryGraph {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("[")
	for n := range topics {
		if n > 0 {
			builder.WriteString(",")
		}
		fmt.Fprintf(&builder, `{"subject":"Topic %05d about stalled output","relation":"depends_on","object":"Topic %05d about stalled output"}`, n+1, n)
	}
	builder.WriteString("]")
	path := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := graph.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestMCPHTTPBoundsStalledOutputWithoutBlockingOthers(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{graph: writeLargeGraph(t, 15000), cfg: func(c *config.Config) { c.WriteTimeout = 750 * time.Millisecond }})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	raw, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", i.addr)
	if err != nil {
		t.Fatal(err)
	}
	// A tiny receive buffer lets the response outgrow what the socket can hold.
	_ = raw.(*net.TCPConn).SetReadBuffer(2048)
	conn := tls.Client(raw, &tls.Config{RootCAs: fx.roots, ServerName: "mcp.example.test"})
	defer func() { _ = conn.Close() }()
	body := mcpMessage("tools/call", `{"name":"list_topics","arguments":{}}`)
	request := "POST /mcp HTTP/1.1\r\nHost: mcp.example.test\r\nAuthorization: Bearer " + token + "\r\nContent-Type: application/json\r\n" +
		"Accept: application/json, text/event-stream\r\nMcp-Protocol-Version: " + mcpProtocol + "\r\nMcp-Method: tools/call\r\nMcp-Name: list_topics\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	// Other callers are served while this writer is stuck.
	if reply := i.call(t, token, "tools/list", "", `{}`); reply.Status != http.StatusOK {
		t.Fatalf("a request during stalled output: %d", reply.Status)
	}
	deadline := time.Now().Add(waitFor)
	for !strings.Contains(i.logs.String(), `"tool":"list_topics"`) {
		if time.Now().After(deadline) {
			t.Fatal("the stalled response was never abandoned")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reply := i.call(t, token, "tools/list", "", `{}`); reply.Status != http.StatusOK {
		t.Fatalf("a request after the stalled write was abandoned: %d", reply.Status)
	}
}

func TestMCPHTTPShutdownDrainsAdmittedCalls(t *testing.T) {
	fx := newMCPFixture(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	planner := &mcpPlanner{next: func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []domain.PlanItem{{Topic: req.Topic, Explanation: "Practice", Resources: []domain.Resource{}}}, nil
	}}
	i := fx.start(t, mcpOptions{planner: planner, cfg: func(c *config.Config) { c.ShutdownTimeout = 20 * time.Second }})
	cs, flow, client := connectedWithClient(t, i, privateID, callerID, allScopes)
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "visual"}})
		done <- err
	}()
	receive(t, started, "the call to start")
	closeIdle(client, flow.http)
	i.cancel()
	deadline := time.Now().Add(waitFor)
	for {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", i.addr)
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("the listener kept accepting connections during shutdown")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-i.served:
		t.Fatalf("shutdown finished with a call in flight: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := receive(t, done, "the admitted call"); err != nil {
		t.Fatalf("the admitted call did not finish: %v", err)
	}
	if err := receive(t, i.served, "the listener to stop"); err != nil {
		t.Fatalf("shutdown = %v", err)
	}
	i.served <- nil
}

func TestMCPHTTPShutdownCancelsCallsAtTheDeadline(t *testing.T) {
	fx := newMCPFixture(t)
	started, canceled := make(chan struct{}, 1), make(chan struct{}, 1)
	planner := &mcpPlanner{next: func(ctx context.Context, _ service.TopicRequest) ([]domain.PlanItem, error) {
		started <- struct{}{}
		<-ctx.Done()
		canceled <- struct{}{}
		return nil, ctx.Err()
	}}
	i := fx.start(t, mcpOptions{planner: planner, cfg: func(c *config.Config) { c.ShutdownTimeout = 2 * time.Second }})
	cs, flow, client := connectedWithClient(t, i, privateID, callerID, allScopes)
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "generate_study_plan", Arguments: map[string]any{"failed_topics": []string{"Recursion"}, "vark_profile": "visual"}})
		done <- err
	}()
	receive(t, started, "the call to start")
	closeIdle(client, flow.http)
	begun := time.Now()
	i.cancel()
	receive(t, canceled, "the stuck call to be canceled")
	if elapsed := time.Since(begun); elapsed < 500*time.Millisecond {
		t.Fatalf("the call was canceled after %s, before the drain period", elapsed)
	}
	_ = receive(t, done, "the stuck client")
	if err := receive(t, i.served, "the listener to stop"); err != nil {
		t.Fatalf("shutdown = %v", err)
	}
	i.served <- nil
}

func TestMCPHTTPConcurrentCallersAreAdmittedExactlyToTheQuota(t *testing.T) {
	t.Run("miniredis", func(t *testing.T) { testMCPConcurrentQuota(t, "") })
	t.Run("real Redis", func(t *testing.T) {
		redisURL := os.Getenv("AXIOM_TEST_REDIS_URL")
		if redisURL == "" {
			t.Skip("set AXIOM_TEST_REDIS_URL to a disposable Redis server")
		}
		testMCPConcurrentQuota(t, redisURL)
	})
}

func testMCPConcurrentQuota(t *testing.T, redisURL string) {
	t.Helper()
	fx := newMCPFixtureWithRedis(t, redisURL)
	probeURL := redisURL
	if probeURL == "" {
		probeURL = fx.redisURL
	}
	opts, err := redis.ParseURL(probeURL)
	if err != nil {
		t.Fatal("invalid Redis URL")
	}
	probe := redis.NewClient(opts)
	t.Cleanup(func() { _ = probe.Close() })
	if err := rateScript.Load(t.Context(), probe).Err(); err != nil {
		t.Fatalf("load quota script before warm-up: %v", err)
	}

	endpoint, err := url.Parse(fx.redisURL)
	if err != nil {
		t.Fatal("invalid Redis URL")
	}
	// Give each instance one connection that the warm-up fully initializes.
	// The burst measures shared quota across ready clients and Redis pools.
	query := endpoint.Query()
	query.Set("pool_size", "1")
	query.Set("max_active_conns", "1")
	query.Set("pool_timeout", "10s")
	endpoint.RawQuery = query.Encode()
	settings := map[string]string{"AXIOM_TENANT_QUOTA_MAX": "7", "RATE_LIMIT_MAX": "10000", "REDIS_URL": endpoint.String()}
	instances := [...]*mcpInstance{fx.start(t, mcpOptions{settings: settings}), fx.start(t, mcpOptions{settings: settings})}
	clients := [...]*http.Client{instances[0].client(mcpClientOptions{http2: true}), instances[1].client(mcpClientOptions{http2: true})}
	t.Cleanup(func() { closeIdle(clients[:]...) })
	const params = `{"name":"list_topics","arguments":{}}`
	warmup := fx.issuer.token(t, mcpPublicURL, uuid.NewString(), callerID, "graph:read")
	for n, instance := range instances {
		req := mcpRequestFor(t, "tools/call", "list_topics", params, warmup)
		if reply := instance.send(t, clients[n], req); reply.Status != http.StatusOK || reply.Proto != 2 {
			t.Fatalf("warm-up on instance %d: HTTP/%d status=%d body=%s", n, reply.Proto, reply.Status, reply.Body)
		}
	}
	tenant := uuid.NewString()
	token := fx.issuer.token(t, mcpPublicURL, tenant, callerID, "graph:read")
	var admitted, rejected atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := range 40 {
		req := mcpRequestFor(t, "tools/call", "list_topics", params, token)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch reply := instances[n%2].send(t, clients[n%2], req); reply.Status {
			case http.StatusOK:
				admitted.Add(1)
			case http.StatusTooManyRequests:
				rejected.Add(1)
			default:
				t.Errorf("status=%d body=%s", reply.Status, reply.Body)
			}
		}()
	}
	close(start)
	wg.Wait()
	if admitted.Load() != 7 || rejected.Load() != 33 {
		t.Fatalf("admitted=%d rejected=%d, want 7 and 33", admitted.Load(), rejected.Load())
	}
	if redisURL != "" {
		key := fmt.Sprintf("%s%x", tenantRatePrefix, identityDigest(fx.issuer.issuer, tenant))
		if count, err := probe.HGet(t.Context(), key, "current").Int(); err != nil || count != 7 {
			t.Fatalf("real Redis tenant counter=%d err=%v, want 7", count, err)
		}
	}
}
