package server

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestIdempotencyReplayAndMismatch(t *testing.T) {
	var calls atomic.Int32
	app := newTestApp(t, defaultConfig(t), func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}, nil)
	first, body := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": "retry-1"})
	replay, replayBody := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": "retry-1"})
	if first.StatusCode != http.StatusOK || replay.StatusCode != http.StatusOK || replayBody != body || calls.Load() != 2 || replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("first=%d replay=%d calls=%d replayed=%q same=%v", first.StatusCode, replay.StatusCode, calls.Load(), replay.Header.Get("Idempotent-Replayed"), body == replayBody)
	}
	if first.Header.Get("X-Request-ID") == replay.Header.Get("X-Request-ID") {
		t.Error("replay reused request ID")
	}
	if first.Header.Get("X-RateLimit-Remaining") == replay.Header.Get("X-RateLimit-Remaining") {
		t.Error("replay reused rate limit headers")
	}
	mismatch, got := call(t, app, "POST", "/api/v1/adaptive-plan", strings.Replace(validBody, "student-42", "student-43", 1), map[string]string{"Idempotency-Key": "retry-1"})
	if mismatch.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(got, "idempotency_key_mismatch") {
		t.Fatalf("status=%d body=%s", mismatch.StatusCode, got)
	}
}

func TestIdempotencyInFlight(t *testing.T) {
	started, release := make(chan struct{}, 2), make(chan struct{})
	app := newTestApp(t, defaultConfig(t), func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		started <- struct{}{}
		<-release
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": "busy"})
	}()
	<-started
	response, body := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": "busy"})
	close(release)
	<-done
	if response.StatusCode != http.StatusConflict || response.Header.Get("Retry-After") != "1" || !strings.Contains(body, "idempotency_key_in_progress") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
}

func TestIdempotencyCredentialScope(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.AuthDisabled = false
	cfg.InternalToken = strings.Repeat("a", 32)
	cfg.InternalTokenPrevious = strings.Repeat("b", 32)
	var calls atomic.Int32
	app := newTestApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}, nil)
	for _, credential := range []map[string]string{
		{"X-Internal-Token": cfg.InternalToken},
		{"Authorization": "Bearer " + cfg.InternalTokenPrevious},
		{"Authorization": "Bearer " + cfg.InternalToken},
	} {
		credential["Idempotency-Key"] = "scoped"
		resp, _ := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, credential)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", resp.StatusCode)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("calls=%d want=4", calls.Load())
	}
}

func TestIdempotencyInvalidKeys(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), nil, nil)
	for _, key := range []string{"", "contains space", strings.Repeat("x", 256), "café"} {
		resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": key})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid_idempotency_key") {
			t.Errorf("key=%q status=%d body=%s", key, resp.StatusCode, body)
		}
	}
	for _, key := range []string{"x", strings.Repeat("!", 255)} {
		resp, _ := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": key})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("valid key status=%d", resp.StatusCode)
		}
	}
}

func TestIdempotencyRetriesFailuresAndCanBeDisabled(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		cfg := defaultConfig(t)
		if disabled {
			cfg.Idempotency.TTL = 0
		}
		var calls atomic.Int32
		app := newTestApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
			calls.Add(1)
			return nil, service.ErrUpstreamRejected
		}, nil)
		for range 2 {
			resp, _ := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": "failed"})
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status=%d", resp.StatusCode)
			}
		}
		if calls.Load() < 2 {
			t.Errorf("failure was retained: calls=%d", calls.Load())
		}
	}
	cfg := defaultConfig(t)
	cfg.Idempotency.TTL = 0
	app := newTestApp(t, cfg, nil, nil)
	for range 2 {
		resp, _ := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": "bad key"})
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "" {
			t.Fatalf("disabled status=%d", resp.StatusCode)
		}
	}
}

func TestIdempotencyReplayContentTypeSurvivesKeepAliveReuse(t *testing.T) {
	var calls atomic.Int32
	app := newTestApp(t, defaultConfig(t), func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}, nil)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() {
		if err := app.ShutdownWithTimeout(time.Second); err != nil {
			t.Error(err)
		}
		if err := <-stopped; err != nil {
			t.Error(err)
		}
	})
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	var originalBody []byte
	for index, path := range []string{"/api/v1/adaptive-plan", "/swagger/index.html", "/api/v1/adaptive-plan"} {
		method, body := http.MethodGet, ""
		if path == "/api/v1/adaptive-plan" {
			method, body = http.MethodPost, validBody
		}
		req, err := http.NewRequestWithContext(context.Background(), method, "http://"+ln.Addr().String()+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "keep-alive")
		if err := req.Write(conn); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(reader, req)
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(resp.Body)
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%s", path, resp.StatusCode, got)
		}
		expectedType := "application/json; charset=utf-8"
		if index == 1 {
			expectedType = "text/html; charset=utf-8"
		}
		if resp.Header.Get("Content-Type") != expectedType {
			t.Fatalf("path=%s content_type=%q want=%q", path, resp.Header.Get("Content-Type"), expectedType)
		}
		if index == 0 {
			originalBody = got
		}
		if index == 2 && (resp.Header.Get("Idempotent-Replayed") != "true" || string(got) != string(originalBody) || calls.Load() != 2) {
			t.Fatalf("replayed=%q same_body=%v calls=%d", resp.Header.Get("Idempotent-Replayed"), string(got) == string(originalBody), calls.Load())
		}
	}
}
