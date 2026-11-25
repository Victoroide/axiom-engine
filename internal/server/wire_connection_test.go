package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

type wireDisconnectCase struct {
	name, path                 string
	requestTimeout, llmTimeout time.Duration
	cause                      error
	recoveryPause              time.Duration
}

const wireRecoveryID = "wire-capacity-restored"
const wireQueueTimeout = 600 * time.Millisecond

func TestWireDisconnectRetainsJSONUntilDeadlineAndCancelsSSEOnWrite(t *testing.T) {
	t.Parallel()
	for _, tc := range []wireDisconnectCase{
		{"JSON close", wirePlan, 1500 * time.Millisecond, 1500 * time.Millisecond, context.DeadlineExceeded, wireQueueTimeout + 100*time.Millisecond},
		{"JSON shorter topic deadline", wirePlan, 6 * time.Second, time.Second, context.DeadlineExceeded, 0},
		{"SSE reset", wireStream, 10 * time.Second, 10 * time.Second, context.Canceled, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !runWireSubprocess(t) {
				return
			}
			cfg, err := config.Load(func(key string) string {
				switch key {
				case "AXIOM_AUTH_DISABLED":
					return "true"
				case "REQUEST_TIMEOUT":
					return tc.requestTimeout.String()
				case "LLM_TIMEOUT":
					return tc.llmTimeout.String()
				case "LLM_QUEUE_TIMEOUT":
					return wireQueueTimeout.String()
				case "LLM_MAX_CONCURRENCY":
					return "1"
				default:
					return ""
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			blocked, recovery := newWireCall(t), newWireCall(t)
			observations := observeWirePlan(t, blocked.unblock)
			recovery.unblock()
			logs := streamLogWriter{records: make(chan []byte, 40)}
			addr, _, _ := startWireApp(t, cfg, planWireCalls(map[string]*wireCall{"wire-close": blocked, wireRecoveryID: recovery}), logs)
			assertWireDisconnect(t, addr, tc, blocked, recovery, observations, logs)
		})
	}
}

type wirePlanProbe struct {
	request        chan wireEntry
	topic          chan wireEntry
	recoveryWait   chan struct{}
	unblockPlanner func()
}

func observeWirePlan(t *testing.T, unblockPlanner func()) wirePlanProbe {
	t.Helper()
	probe := wirePlanProbe{
		request: make(chan wireEntry, 1), topic: make(chan wireEntry, 1),
		recoveryWait: make(chan struct{}, 1), unblockPlanner: unblockPlanner,
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(probe))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return probe
}

func (p wirePlanProbe) OnStart(parent context.Context, span sdktrace.ReadWriteSpan) {
	id := telemetry.RequestID(parent)
	if id == wireRecoveryID && span.Name() == "plan.queue.wait" {
		// The recovery request can proceed while the client is paused.
		p.unblockPlanner()
		p.recoveryWait <- struct{}{}
		return
	}
	if id != "wire-close" {
		return
	}
	destination := p.request
	switch span.Name() {
	case "plan.validate":
	case "plan.topic":
		destination = p.topic
	default:
		return
	}
	deadline, _ := parent.Deadline()
	destination <- wireEntry{at: time.Now(), deadline: deadline}
}

func (wirePlanProbe) OnEnd(sdktrace.ReadOnlySpan)      {}
func (wirePlanProbe) Shutdown(context.Context) error   { return nil }
func (wirePlanProbe) ForceFlush(context.Context) error { return nil }

func assertWireDisconnect(t *testing.T, addr wireAddress, tc wireDisconnectCase, blocked, recovery *wireCall, probe wirePlanProbe, logs streamLogWriter) {
	t.Helper()
	client := addr.dial(t)
	headers := wireHeaders("wire-close")
	headers.Set("Idempotency-Key", "wire-close")
	sent := time.Now()
	req := client.send(t, http.MethodPost, tc.path, wireTopicBody, headers)
	request := receiveWire(t, probe.request, "request deadline at validation")
	topic := receiveWire(t, probe.topic, "topic queue entry")
	entry := receiveWire(t, blocked.entered, "planner entry")
	assertWireDeadline(t, "request", request.deadline, sent.Add(tc.requestTimeout), request.at.Add(tc.requestTimeout))
	if tc.llmTimeout < tc.requestTimeout {
		if !entry.deadline.Before(request.deadline) {
			t.Fatalf("shorter topic deadline %s did not precede request deadline %s", entry.deadline, request.deadline)
		}
		assertWireDeadline(t, "topic", entry.deadline, topic.at.Add(tc.llmTimeout), entry.at.Add(tc.llmTimeout))
	} else if !entry.deadline.Equal(request.deadline) {
		t.Fatalf("planner deadline %s, want request deadline %s", entry.deadline, request.deadline)
	}
	if tc.path == wireStream {
		resp := client.response(t, req)
		assertWireStreamHeaders(t, resp, "wire-close")
		assertWireBytes(t, resp.Body, ": keepalive\n\n")
		if err := client.SetLinger(0); err != nil {
			t.Fatal(err)
		}
	}
	closed := time.Now()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	other := addr.dial(t)
	if tc.path == wirePlan {
		headers.Set("X-Request-ID", "wire-still-reserved")
		resp, body := other.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
		assertWireError(t, resp, body, http.StatusConflict, "idempotency_key_in_progress", "request with this Idempotency-Key is still in progress", "wire-still-reserved")
	}
	canceled := receiveWire(t, blocked.canceled, "planner cancellation")
	if canceled.at.Before(closed) || !errors.Is(canceled.err, tc.cause) {
		t.Fatalf("cancel=%v after %s, want %v", canceled.err, canceled.at.Sub(closed), tc.cause)
	}
	if tc.path == wireStream {
		assertWireAccess(t, logs, "wire-close", http.StatusOK, true)
	} else {
		if canceled.at.Before(entry.deadline) {
			t.Fatalf("planner canceled %s before its deadline", entry.deadline.Sub(canceled.at))
		}
		assertWireAccess(t, logs, "wire-close", http.StatusGatewayTimeout, false)
	}
	assertWirePermitHeld(t, other, "wire-after-cancel")

	headers.Set("X-Request-ID", wireRecoveryID)
	req = other.send(t, http.MethodPost, wirePlan, wireTopicBody, headers)
	receiveWire(t, probe.recoveryWait, "recovery request to encounter the occupied permit")
	// Simulate an idle client for longer than the server's queue budget.
	time.Sleep(tc.recoveryPause)
	returned := receiveWire(t, blocked.returned, "blocked planner return")
	resp := other.response(t, req)
	body := readWireBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("permit or reservation not released: status=%d headers=%v body=%s", resp.StatusCode, resp.Header, body)
	}
	assertWireID(t, resp, wireRecoveryID)
	assertWirePlan(t, body)
	permit := receiveWire(t, recovery.entered, "recovery planner entry").at
	if permit.Before(returned) {
		t.Fatal("recovery request acquired the permit before the blocked planner returned")
	}
	t.Logf("close to cancellation=%s, planner return to admission=%s, close to admission=%s", canceled.at.Sub(closed), permit.Sub(returned), permit.Sub(closed))
}

func assertWireDeadline(t *testing.T, scope string, deadline, earliest, latest time.Time) {
	t.Helper()
	if deadline.Before(earliest) || deadline.After(latest) {
		t.Fatalf("%s deadline %s outside observed creation window [%s, %s]", scope, deadline, earliest, latest)
	}
}

func assertWirePermitHeld(t *testing.T, c wireConn, id string) {
	t.Helper()
	resp, body := c.exchange(t, http.MethodPost, wirePlan, wireTopicBody, wireHeaders(id))
	assertWireError(t, resp, body, http.StatusServiceUnavailable, "service_unavailable", "reasoning temporarily unavailable", id)
	if resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("Retry-After=%q", resp.Header.Get("Retry-After"))
	}
}

func assertWireAccess(t *testing.T, logs streamLogWriter, id string, status int, disconnected bool) time.Time {
	t.Helper()
	timer := time.NewTimer(wireWait)
	defer timer.Stop()
	for {
		select {
		case record := <-logs.records:
			var entry struct {
				Time         time.Time `json:"time"`
				Message      string    `json:"msg"`
				RequestID    string    `json:"request_id"`
				Status       int       `json:"status"`
				Disconnected bool      `json:"client_disconnected"`
			}
			if err := json.Unmarshal(record, &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Message == "http.access" && entry.RequestID == id {
				if entry.Status != status || entry.Disconnected != disconnected {
					t.Fatalf("unexpected access record: %s", record)
				}
				return entry.Time
			}
		case <-timer.C:
			t.Fatalf("missing access record for %q", id)
		}
	}
}

func TestWireGracefulDrain(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	wireGracefulDrain(t, defaultConfig(t))
}

func wireGracefulDrain(t *testing.T, cfg config.Config) {
	t.Helper()
	cfg.ShutdownDrainDelay = time.Second
	pending := newWireCall(t)
	addr, stop, done := startWireApp(t, cfg, planWireCalls(map[string]*wireCall{"wire-draining": pending}), nil)
	probe := addr.dial(t)
	resp, body := probe.exchange(t, http.MethodGet, "/readyz", "", nil)
	if resp.StatusCode != http.StatusOK || body != `{"status":"ok"}` {
		t.Fatalf("initial readiness status=%d body=%s", resp.StatusCode, body)
	}

	conn := addr.dial(t)
	req := conn.send(t, http.MethodPost, wirePlan, wireTopicBody, wireHeaders("wire-draining"))
	receiveWire(t, pending.entered, "in-flight planner entry")
	stop()
	timer := time.NewTimer(cfg.ShutdownDrainDelay)
	defer timer.Stop()
	retry := time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for resp.StatusCode == http.StatusOK {
		select {
		case <-timer.C:
			t.Fatal("readiness did not change during the drain window")
		case <-retry.C:
			resp, body = probe.exchange(t, http.MethodGet, "/readyz", "", nil)
		}
	}
	if resp.StatusCode != http.StatusServiceUnavailable || body != `{"status":"draining"}` {
		t.Fatalf("draining readiness status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = probe.exchange(t, http.MethodGet, "/livez", "", nil)
	if resp.StatusCode != http.StatusOK || body != `{"status":"ok"}` {
		t.Fatalf("draining liveness status=%d body=%s", resp.StatusCode, body)
	}
	_ = probe.Close()
	waitWireListenerClosed(t, addr, 2*cfg.ShutdownDrainDelay)
	pending.unblock()
	resp = conn.response(t, req)
	body = readWireBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("drained request status=%d body=%s", resp.StatusCode, body)
	}
	assertWireID(t, resp, "wire-draining")
	assertWirePlan(t, body)
	_ = conn.Close()
	if err := receiveWire(t, done, "graceful server exit"); err != nil {
		t.Fatal(err)
	}
}

func waitWireListenerClosed(t *testing.T, addr wireAddress, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("listener still accepts connections after the drain window")
		case <-ticker.C:
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", string(addr))
			if err != nil && ctx.Err() == nil {
				return
			}
			if conn != nil {
				_ = conn.Close()
			}
		}
	}
}
