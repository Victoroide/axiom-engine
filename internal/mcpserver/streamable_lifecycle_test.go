package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestStreamableHTTPConcurrentCalls(t *testing.T) {
	cfg := streamableConfig(t)
	cfg.LLMMaxConcurrency = 2
	cfg.LLMQueueTimeout = 100 * time.Millisecond
	started := make(chan string, 3)
	canceled := make(chan string, 2)
	release := make(chan struct{})
	planner := plannerFunc(func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		started <- req.Topic
		if req.Topic == "Arrays" {
			return successfulPlanner(ctx, req)
		}
		select {
		case <-ctx.Done():
			canceled <- req.Topic
			return nil, ctx.Err()
		case <-release:
			return successfulPlanner(ctx, req)
		}
	})
	srv := httptest.NewUnstartedServer(NewStreamableHTTPHandler(cfg, graph.NewInMemoryGraph(), planner, streamableLogger(), "test-version", false))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	clientA := connectStreamableClient(t, srv.URL, srv.Client())
	clientB := connectStreamableClient(t, srv.URL, srv.Client())

	ctxA, cancelA := context.WithCancel(t.Context())
	defer cancelA()
	doneA := make(chan error, 1)
	go func() {
		_, err := clientA.CallTool(ctxA, streamablePlan("Recursion", "alpha"))
		doneA <- err
	}()
	if topic := awaitStreamable(t, started); topic != "Recursion" {
		t.Fatalf("first topic = %q", topic)
	}

	type call struct {
		result *mcp.CallToolResult
		err    error
	}
	doneB := make(chan call, 1)
	go func() {
		result, err := clientB.CallTool(t.Context(), streamablePlan("Polymorphism", "beta"))
		doneB <- call{result, err}
	}()
	if topic := awaitStreamable(t, started); topic != "Polymorphism" {
		t.Fatalf("second topic = %q", topic)
	}

	busy, err := clientB.CallTool(t.Context(), streamablePlan("Arrays", "beta"))
	if err != nil {
		t.Fatal(err)
	}
	var failure domain.ErrorResponse
	decodeStreamableContent(t, busy, &failure)
	if !busy.IsError || failure.Error != "service_unavailable" {
		t.Fatalf("shared capacity result = %+v", busy)
	}
	select {
	case topic := <-started:
		t.Fatalf("capacity was exceeded by %q", topic)
	default:
	}

	cancelA()
	if err := awaitStreamable(t, doneA); !errors.Is(err, context.Canceled) {
		t.Fatalf("first client result = %v", err)
	}
	if topic := awaitStreamable(t, canceled); topic != "Recursion" {
		t.Fatalf("canceled topic = %q", topic)
	}
	result, err := clientA.CallTool(t.Context(), streamablePlan("Arrays", "alpha-next"))
	checkStreamablePlan(t, result, err, "Arrays", "alpha-next")
	if topic := awaitStreamable(t, started); topic != "Arrays" {
		t.Fatalf("next topic = %q", topic)
	}
	close(release)
	completed := awaitStreamable(t, doneB)
	checkStreamablePlan(t, completed.result, completed.err, "Polymorphism", "beta")
}

func TestStreamableHTTPCloseResponseCancelsWork(t *testing.T) {
	cfg, logger := streamableConfig(t), streamableLogger()
	started := make(chan struct{})
	canceled := make(chan error, 1)
	planner := plannerFunc(func(ctx context.Context, _ service.TopicRequest) ([]domain.PlanItem, error) {
		close(started)
		<-ctx.Done()
		canceled <- ctx.Err()
		return nil, ctx.Err()
	})
	s := newServer(cfg, graph.NewInMemoryGraph(), planner, logger, "test-version", mcp.ServerOptions{SupportedProtocolVersions: []string{julyProtocol}})
	// Send one progress event so the client can close an open response body.
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if call, ok := req.(*mcp.CallToolRequest); ok {
				if err := call.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: call.Params.GetProgressToken(), Progress: 1}); err != nil {
					return nil, err
				}
			}
			return next(ctx, method, req)
		}
	})
	srv := httptest.NewServer(streamableHTTPHandler(s, logger, cfg.WriteTimeout, false))
	t.Cleanup(srv.Close)
	const method, name = "tools/call", "generate_study_plan"
	message := streamableMessage(method, julyProtocol, `{"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"vark_profile":"visual"}}`)
	message = strings.Replace(message, `"_meta":{`, `"_meta":{"progressToken":"open-response",`, 1)
	req := streamableRequest(t, srv.URL, method, name, message)
	client := srv.Client()
	client.Timeout = 5 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status = %d, headers = %v", resp.StatusCode, resp.Header)
	}
	r := bufio.NewReader(resp.Body)
	if line, err := r.ReadString('\n'); err != nil || line != "event: message\n" {
		t.Fatalf("event = %q, error = %v", line, err)
	}
	if line, err := r.ReadString('\n'); err != nil || !strings.Contains(line, `"method":"notifications/progress"`) || !strings.Contains(line, `"progressToken":"open-response"`) {
		t.Fatalf("event data = %q, error = %v", line, err)
	}
	awaitStreamable(t, started)
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := awaitStreamable(t, canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("planner error = %v", err)
	}
}

func TestStreamableHTTPGracefulShutdown(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	planner := plannerFunc(func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return successfulPlanner(ctx, req)
		}
	})
	cfg := streamableConfig(t)
	srv := httptest.NewServer(NewStreamableHTTPHandler(cfg, graph.NewInMemoryGraph(), planner, streamableLogger(), "test-version", true))
	t.Cleanup(srv.Close)
	client := connectStreamableClient(t, srv.URL, srv.Client())
	draining := make(chan struct{})
	srv.Config.RegisterOnShutdown(func() { close(draining) })
	done := make(chan error, 1)
	go func() {
		result, err := client.CallTool(t.Context(), streamablePlan("Recursion", "draining"))
		if err == nil && (result == nil || result.IsError) {
			err = fmt.Errorf("drained call result = %+v", result)
		}
		done <- err
	}()
	awaitStreamable(t, started)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	shutdown := make(chan error, 1)
	go func() { shutdown <- srv.Config.Shutdown(ctx) }()
	awaitStreamable(t, draining)

	req := streamableRequest(t, srv.URL, "tools/list", "", streamableMessage("tools/list", julyProtocol, `{}`))
	resp, err := srv.Client().Do(req.WithContext(ctx))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("server admitted a new connection during shutdown: status = %d", resp.StatusCode)
	}
	select {
	case err := <-shutdown:
		t.Fatalf("shutdown finished before the in-flight call: %v", err)
	default:
	}
	close(release)
	if err := awaitStreamable(t, done); err != nil {
		t.Fatal(err)
	}
	if err := awaitStreamable(t, shutdown); err != nil {
		t.Fatal(err)
	}
}

func TestStreamableHTTPShutdownWithToolChangeCallback(t *testing.T) {
	cfg := streamableConfig(t)
	srv := httptest.NewServer(NewStreamableHTTPHandler(cfg, graph.NewInMemoryGraph(), plannerFunc(successfulPlanner), streamableLogger(), "test-version", true))
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "callback-client", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: srv.Client(), MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: julyProtocol})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if tools, err := cs.ListTools(ctx, nil); err != nil || len(tools.Tools) != 4 {
		t.Fatalf("list before shutdown = %+v, %v", tools, err)
	}
	drain, cancelDrain := context.WithTimeout(t.Context(), time.Second)
	defer cancelDrain()
	if err := srv.Config.Shutdown(drain); err != nil {
		t.Fatalf("shutdown with connected client = %v", err)
	}
	if caps := cs.InitializeResult().Capabilities.Tools; caps == nil || caps.ListChanged {
		t.Fatalf("fixed tool registry advertised change subscriptions: %+v", caps)
	}
}

func TestStreamableHTTPGraphWorkCancels(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
	}{
		{"get_cognitive_graph", `{"topics":["Recursion"]}`},
		{"list_topics", `{}`},
		{"get_topic_path", `{"topic":"Recursion"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, logger := streamableConfig(t), streamableLogger()
			s := newServer(cfg, graph.NewInMemoryGraph(), plannerFunc(successfulPlanner), logger, "test-version", mcp.ServerOptions{SupportedProtocolVersions: []string{julyProtocol}})
			traversing := make(chan struct{})
			completed := make(chan *mcp.CallToolResult, 1)
			s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					if extra := req.GetExtra(); extra == nil || extra.Header.Get("X-Traversal-Probe") != "true" {
						return next(ctx, method, req)
					}
					result, err := next(&graphTraversalProbe{Context: ctx, traversing: traversing}, method, req) //nolint:contextcheck // The probe embeds the SDK request context.
					call, _ := result.(*mcp.CallToolResult)
					completed <- call
					return result, err
				}
			})
			srv := httptest.NewServer(streamableHTTPHandler(s, logger, cfg.WriteTimeout, true))
			t.Cleanup(srv.Close)
			client := connectStreamableClient(t, srv.URL, srv.Client())
			req := streamableRequest(t, srv.URL, "tools/call", tc.name, streamableMessage("tools/call", julyProtocol, fmt.Sprintf(`{"name":%q,"arguments":%s}`, tc.name, tc.args)))
			req.Header.Set("X-Traversal-Probe", "true")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			requestDone := make(chan error, 1)
			go func() {
				resp, err := srv.Client().Do(req.WithContext(ctx))
				if resp != nil {
					_ = resp.Body.Close()
				}
				requestDone <- err
			}()
			select {
			case <-traversing:
			case result := <-completed:
				t.Fatalf("tool completed without checking cancellation during work: %+v", result)
			case <-time.After(5 * time.Second):
				t.Fatal("tool never reached the graph traversal")
			}
			peerCtx, cancelPeer := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancelPeer()
			peer, err := client.CallTool(peerCtx, &mcp.CallToolParams{Name: tc.name, Arguments: json.RawMessage(tc.args)})
			if err != nil || peer == nil || peer.IsError || peer.StructuredContent == nil {
				t.Fatalf("concurrent graph tool = %+v, %v", peer, err)
			}
			cancel()
			if err := awaitStreamable(t, requestDone); !errors.Is(err, context.Canceled) {
				t.Fatalf("caller error = %v", err)
			}
			result := awaitStreamable(t, completed)
			var failure domain.ErrorResponse
			decodeStreamableContent(t, result, &failure)
			if !result.IsError || failure.Error != "request_timeout" {
				t.Fatalf("canceled tool result = %+v", result)
			}
		})
	}
}

type graphTraversalProbe struct {
	context.Context
	traversing chan struct{}
	checks     atomic.Int32
}

func (p *graphTraversalProbe) Err() error {
	// Pause after the entry checks, while the graph operation is running.
	if p.checks.Add(1) == 5 {
		close(p.traversing)
		<-p.Done()
	}
	return p.Context.Err()
}

func TestStreamableHTTPRejectsWriterWithoutDeadline(t *testing.T) {
	var called atomic.Bool
	planner := plannerFunc(func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		called.Store(true)
		return successfulPlanner(ctx, req)
	})
	h := NewStreamableHTTPHandler(streamableConfig(t), graph.NewInMemoryGraph(), planner, streamableLogger(), "test-version", true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(struct{ http.ResponseWriter }{w}, r)
	}))
	t.Cleanup(srv.Close)
	const method, name = "tools/call", "generate_study_plan"
	message := streamableMessage(method, julyProtocol, `{"name":"generate_study_plan","arguments":{"failed_topics":["Recursion"],"vark_profile":"visual"}}`)
	status, _, body := streamableHTTPReply(t, srv.Client(), streamableRequest(t, srv.URL, method, name, message))
	if status != http.StatusInternalServerError || called.Load() {
		t.Fatalf("wrapped writer status = %d, planner called = %t, body = %s", status, called.Load(), body)
	}
}

func streamablePlan(topic, student string) *mcp.CallToolParams {
	return &mcp.CallToolParams{Name: "generate_study_plan", Arguments: PlanInput{FailedTopics: []string{topic}, VARKProfile: "visual", StudentID: student}}
}

func checkStreamablePlan(t *testing.T, result *mcp.CallToolResult, err error, topic, student string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var plan domain.PlanResponse
	decodeStreamableContent(t, result, &plan)
	if result.IsError || plan.StudentID != student || len(plan.Items) != 1 || plan.Items[0].Topic != topic {
		t.Fatalf("plan = %+v, want topic %q and student %q", plan, topic, student)
	}
}

func decodeStreamableContent(t *testing.T, result *mcp.CallToolResult, dst any) {
	t.Helper()
	if result == nil {
		t.Fatal("missing tool result")
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatal(err)
	}
}

func awaitStreamable[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the HTTP request")
		var zero T
		return zero
	}
}
