package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/service"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func TestPlanStreamSuccess(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), nil, nil)
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan/stream", validBody, nil)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d type=%q body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	events := parseEvents(t, body)
	if len(events) != 3 || events[0].name != "topic" || events[1].name != "topic" || events[2].name != "plan" {
		t.Fatalf("events=%+v", events)
	}
	var plan domain.PlanResponse
	if err := json.Unmarshal(events[2].data, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 || plan.StudentID != "student-42" || plan.CourseID != "cs101" || plan.Meta.TopicsProcessed != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	seen := map[string]int{}
	for _, event := range events[:2] {
		var progress struct {
			Topic string `json:"topic"`
			Index int    `json:"index"`
			Items int    `json:"items_generated"`
		}
		if err := json.Unmarshal(event.data, &progress); err != nil {
			t.Fatal(err)
		}
		if progress.Items != 1 {
			t.Fatalf("progress=%+v", progress)
		}
		seen[progress.Topic] = progress.Index
	}
	if len(seen) != 2 || seen["Recursion"] != 0 || seen["Polymorphism"] != 1 {
		t.Fatalf("progress=%v", seen)
	}
}

type streamEvent struct {
	name string
	data json.RawMessage
}

func parseEvents(t *testing.T, body string) []streamEvent {
	t.Helper()
	var events []streamEvent
	for _, block := range strings.Split(body, "\n\n") {
		if !strings.HasPrefix(block, "event: ") {
			continue
		}
		name, data, ok := strings.Cut(block, "\ndata: ")
		if !ok {
			t.Fatalf("malformed event %q", block)
		}
		events = append(events, streamEvent{name: strings.TrimPrefix(name, "event: "), data: json.RawMessage(data)})
	}
	return events
}

func TestPlanStreamValidatesBeforeStreaming(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
		t.Error("planner called for invalid input")
		return nil, nil
	}, nil)
	for _, tc := range []struct{ body, contentType string }{
		{`{`, "application/json"},
		{`{"failed_topics":["unknown"],"vark_profile":"visual"}`, "application/json"},
		{`{"failed_topics":[],"vark_profile":"visual"}`, "application/json"},
		{validBody, "text/plain"},
	} {
		headers := map[string]string{"Content-Type": tc.contentType}
		expected, expectedBody := call(t, app, "POST", "/api/v1/adaptive-plan", tc.body, headers)
		resp, body := call(t, app, "POST", "/api/v1/adaptive-plan/stream", tc.body, headers)
		if resp.StatusCode != expected.StatusCode || body != expectedBody || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("status=%d want=%d body=%s want=%s", resp.StatusCode, expected.StatusCode, body, expectedBody)
		}
	}
}

func TestPlanStreamErrorMatchesJSON(t *testing.T) {
	for _, failure := range []error{service.ErrUpstreamRejected, service.ErrInvalidOutput, service.ErrThrottled, context.DeadlineExceeded, errors.New("upstream failed")} {
		t.Run(failure.Error(), func(t *testing.T) {
			app := newTestApp(t, defaultConfig(t), func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) { return nil, failure }, nil)
			body := strings.Replace(validBody, `"Recursion","Polymorphism"`, `"Recursion"`, 1)
			expected, expectedBody := call(t, app, "POST", "/api/v1/adaptive-plan", body, nil)
			resp, stream := call(t, app, "POST", "/api/v1/adaptive-plan/stream", body, nil)
			events := parseEvents(t, stream)
			if resp.StatusCode != http.StatusOK || len(events) != 2 || events[0].name != "topic" || events[1].name != "error" {
				t.Fatalf("status=%d events=%+v", resp.StatusCode, events)
			}
			var got domain.PlanStreamError
			var want domain.ErrorResponse
			if err := json.Unmarshal(events[1].data, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(expectedBody), &want); err != nil {
				t.Fatal(err)
			}
			if got.Status != expected.StatusCode || got.ErrorResponse != want {
				t.Fatalf("error=%+v status=%d envelope=%+v", got, expected.StatusCode, want)
			}
		})
	}
}

func TestPlanStreamUsesAuthenticationAndSharedBudget(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.AuthDisabled = false
	cfg.InternalToken = strings.Repeat("s", 32)
	cfg.RateLimitMax = 1
	app := newTestApp(t, cfg, nil, nil)
	resp, _ := call(t, app, "POST", "/api/v1/adaptive-plan/stream", validBody, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", resp.StatusCode)
	}
	headers := map[string]string{"X-Internal-Token": cfg.InternalToken}
	resp, _ = call(t, app, "POST", "/api/v1/adaptive-plan/stream", validBody, headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status=%d", resp.StatusCode)
	}
	resp, _ = call(t, app, "POST", "/api/v1/adaptive-plan", validBody, headers)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("budget status=%d", resp.StatusCode)
	}
}

func TestPlanStreamDeadlineCancelsTopics(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.RequestTimeout = 40 * time.Millisecond
	canceled := make(chan struct{}, 2)
	app := newTestApp(t, cfg, func(ctx context.Context, _ service.TopicRequest) ([]domain.PlanItem, error) {
		<-ctx.Done()
		canceled <- struct{}{}
		return nil, ctx.Err()
	}, nil)
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan/stream", validBody, nil)
	events := parseEvents(t, body)
	if resp.StatusCode != http.StatusOK || len(events) == 0 || events[len(events)-1].name != "error" {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var failure domain.PlanStreamError
	if err := json.Unmarshal(events[len(events)-1].data, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Status != http.StatusGatewayTimeout || failure.Error != "gateway_timeout" {
		t.Fatalf("failure=%+v", failure)
	}
	for range 2 {
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("topic did not observe deadline")
		}
	}
}

type streamLogWriter struct{ records chan []byte }

func (w streamLogWriter) Write(p []byte) (int, error) {
	w.records <- append([]byte(nil), p...)
	return len(p), nil
}

func TestPlanStreamDisconnectCancelsAndReleasesSlots(t *testing.T) {
	testPlanStreamDisconnect(t, true)
}

func TestPlanStreamNormalCloseCancelsAndReleasesSlots(t *testing.T) {
	testPlanStreamDisconnect(t, false)
}

func testPlanStreamDisconnect(t *testing.T, reset bool) {
	t.Helper()
	cfg := defaultConfig(t)
	cfg.RequestTimeout = 10 * time.Second
	cfg.LLMTimeout = 8 * time.Second
	cfg.LLMMaxConcurrency = 2
	cfg.LLMQueueTimeout = 200 * time.Millisecond
	started, canceled := make(chan struct{}, 2), make(chan struct{}, 2)
	var block atomic.Bool
	block.Store(true)
	var active atomic.Int32
	logs := streamLogWriter{records: make(chan []byte, 50)}
	app := newTestApp(t, cfg, func(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		if block.Load() {
			active.Add(1)
			defer func() { active.Add(-1); canceled <- struct{}{} }()
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}, logs)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() {
		if err := app.ShutdownWithTimeout(time.Second); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(6 * time.Second)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(context.Background(), "POST", "http://"+ln.Addr().String()+"/api/v1/adaptive-plan/stream", strings.NewReader(validBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "stream-disconnect")
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != ": keepalive\n" {
		t.Fatalf("first comment=%q err=%v", line, err)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("topics did not start")
		}
	}
	if tcp, ok := conn.(*net.TCPConn); ok && reset {
		if err := tcp.SetLinger(0); err != nil {
			t.Fatal(err)
		}
	}
	disconnected := time.Now()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for range 2 {
		select {
		case <-canceled:
		case <-deadline.C:
			t.Fatalf("disconnect did not cancel topics; active=%d", active.Load())
		}
	}
	if active.Load() != 0 || time.Since(disconnected) > 5*time.Second {
		t.Fatalf("active=%d elapsed=%s", active.Load(), time.Since(disconnected))
	}
	t.Logf("disconnect cancellation elapsed=%s active=%d", time.Since(disconnected), active.Load())
	block.Store(false)
	recovered, body := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, nil)
	if recovered.StatusCode != http.StatusOK {
		t.Fatalf("slots were not released: status=%d body=%s", recovered.StatusCode, body)
	}
	for {
		select {
		case record := <-logs.records:
			var entry struct {
				Message      string `json:"msg"`
				RequestID    string `json:"request_id"`
				Disconnected bool   `json:"client_disconnected"`
			}
			if err := json.Unmarshal(record, &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Message == "http.access" && entry.RequestID == "stream-disconnect" {
				if !entry.Disconnected {
					t.Fatal("disconnect absent from access log")
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("missing disconnect access log")
		}
	}
}
