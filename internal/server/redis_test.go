package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"

	"github.com/alicebob/miniredis/v2"

	"github.com/Victoroide/axiom-engine/internal/config"
)

func redisConfig(t *testing.T, url string) config.Config {
	t.Helper()
	cfg, err := config.Load(func(key string) string {
		switch key {
		case "AXIOM_AUTH_DISABLED":
			return "true"
		case "REDIS_URL":
			return url
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRedisSharesRateBudget(t *testing.T) {
	redis := miniredis.RunT(t)
	cfg := redisConfig(t, "redis://"+redis.Addr())
	cfg.RateLimitMax = 1
	first := newTestApp(t, cfg, nil, nil)
	second := newTestApp(t, cfg, nil, nil)
	t.Cleanup(func() { _ = first.Shutdown(); _ = second.Shutdown() })
	resp, _ := call(t, first, "GET", "/api/v1/tutor/student/s/cognitive-graph?topics=Recursion", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status=%d", resp.StatusCode)
	}
	resp, body := call(t, second, "GET", "/api/v1/tutor/student/s/cognitive-graph?topics=Recursion", "", nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("shared budget status=%d want=429 body=%s", resp.StatusCode, body)
	}
}

func TestRedisBudgetIsAtomicAcrossReplicas(t *testing.T) {
	redis := miniredis.RunT(t)
	cfg := redisConfig(t, "redis://"+redis.Addr())
	cfg.RateLimitMax = 5
	first, second := newTestApp(t, cfg, nil, nil), newTestApp(t, cfg, nil, nil)
	t.Cleanup(func() { _ = first.Shutdown(); _ = second.Shutdown() })
	start := make(chan struct{})
	statuses := make(chan int, 24)
	for i := range 24 {
		go func() {
			<-start
			app := first
			if i%2 == 1 {
				app = second
			}
			resp, _ := call(t, app, "GET", "/api/v1/tutor/student/s/cognitive-graph?topics=Recursion", "", nil)
			statuses <- resp.StatusCode
		}()
	}
	close(start)
	admitted := 0
	for range 24 {
		status := <-statuses
		if status == http.StatusOK {
			admitted++
		} else if status != http.StatusTooManyRequests {
			t.Errorf("status=%d", status)
		}
	}
	if admitted != 5 {
		t.Fatalf("admitted=%d want=5", admitted)
	}
	for _, key := range redis.Keys() {
		if !strings.HasPrefix(key, "axiom:") {
			t.Errorf("unprefixed key %q", key)
		}
	}
}

func TestRedisSharesIdempotencyAndExpires(t *testing.T) {
	redis := miniredis.RunT(t)
	cfg := redisConfig(t, "redis://"+redis.Addr())
	cfg.Idempotency.TTL = time.Second
	var calls atomic.Int32
	planner := func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}
	first, second := newTestApp(t, cfg, planner, nil), newTestApp(t, cfg, planner, nil)
	t.Cleanup(func() { _ = first.Shutdown(); _ = second.Shutdown() })
	headers := map[string]string{"Idempotency-Key": "shared"}
	original, body := call(t, first, "POST", "/api/v1/adaptive-plan", validBody, headers)
	replay, replayed := call(t, second, "POST", "/api/v1/adaptive-plan", validBody, headers)
	if original.StatusCode != http.StatusOK || replay.StatusCode != http.StatusOK || replay.Header.Get("Idempotent-Replayed") != "true" || body != replayed || calls.Load() != 2 {
		t.Fatalf("statuses=%d,%d calls=%d replayed=%q", original.StatusCode, replay.StatusCode, calls.Load(), replay.Header.Get("Idempotent-Replayed"))
	}
	mismatch, body := call(t, second, "POST", "/api/v1/adaptive-plan", strings.Replace(validBody, "student-42", "student-43", 1), headers)
	if mismatch.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "idempotency_key_mismatch") {
		t.Fatalf("status=%d body=%s", mismatch.StatusCode, body)
	}
	redis.FastForward(time.Second)
	expired, _ := call(t, second, "POST", "/api/v1/adaptive-plan", validBody, headers)
	if expired.StatusCode != http.StatusOK || expired.Header.Get("Idempotent-Replayed") != "" || calls.Load() != 4 {
		t.Fatalf("expiry status=%d calls=%d", expired.StatusCode, calls.Load())
	}
}

func TestRedisStartupFailure(t *testing.T) {
	redis := miniredis.RunT(t)
	cfg := redisConfig(t, "redis://"+redis.Addr())
	redis.SetError("ERR unavailable")
	start := time.Now()
	app, err := NewWithError(cfg, Dependencies{})
	if err == nil || app != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("app=%v err=%v elapsed=%s", app, err, time.Since(start))
	}
	if strings.Contains(err.Error(), cfg.Redis.URL) {
		t.Fatal("startup error exposes connection URL")
	}
}

func TestRedisRuntimeFailureFailsClosed(t *testing.T) {
	redis := miniredis.RunT(t)
	cfg := redisConfig(t, "redis://"+redis.Addr())
	var calls atomic.Int32
	var logs bytes.Buffer
	app := newTestApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}, &logs)
	t.Cleanup(func() { _ = app.Shutdown() })
	redis.SetError("ERR unavailable")
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": "unavailable"})
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" || !strings.Contains(body, "service_unavailable") || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", resp.StatusCode, calls.Load(), body)
	}
	if !strings.Contains(logs.String(), `"operation":"rate_limit.consume"`) {
		t.Fatalf("missing storage operation log: %s", logs.String())
	}
	redis.SetError("")
	resp, _ = call(t, app, "POST", "/api/v1/adaptive-plan", validBody, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("recovery status=%d", resp.StatusCode)
	}
}

func TestRedisCompletionFailureReturnsUnavailable(t *testing.T) {
	redis := miniredis.RunT(t)
	cfg := redisConfig(t, "redis://"+redis.Addr())
	var logs bytes.Buffer
	app := newTestApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		redis.SetError("ERR unavailable")
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}, &logs)
	t.Cleanup(func() { _ = app.Shutdown() })
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Idempotency-Key": "write-failure"})
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" || !strings.Contains(body, "service_unavailable") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(logs.String(), `"operation":"idempotency.complete"`) {
		t.Fatalf("missing completion failure log: %s", logs.String())
	}
}

func TestRedisAbortFailureReturnsUnavailable(t *testing.T) {
	redis := miniredis.RunT(t)
	cfg := redisConfig(t, "redis://"+redis.Addr())
	var logs bytes.Buffer
	var calls atomic.Int32
	app := newTestApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		switch calls.Add(1) {
		case 1:
			redis.SetError("ERR unavailable")
			return nil, service.ErrUpstreamRejected
		case 2:
			return nil, service.ErrUpstreamRejected
		default:
			return []domain.PlanItem{{Topic: r.Topic}}, nil
		}
	}, &logs)
	t.Cleanup(func() { _ = app.Shutdown() })
	headers := map[string]string{"Idempotency-Key": "abort-failure"}
	requestBody := strings.Replace(validBody, `"Recursion","Polymorphism"`, `"Recursion"`, 1)
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", requestBody, headers)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" || !strings.Contains(body, `"error":"service_unavailable"`) {
		t.Fatalf("status=%d retry_after=%q body=%s", resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}
	if !strings.Contains(logs.String(), `"operation":"idempotency.abort"`) {
		t.Fatalf("missing abort failure log: %s", logs.String())
	}
	redis.SetError("")
	redis.FastForward(cfg.RequestTimeout + 3*time.Second)
	resp, body = call(t, app, "POST", "/api/v1/adaptive-plan", requestBody, headers)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "bad_gateway") {
		t.Fatalf("recovered storage status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = call(t, app, "POST", "/api/v1/adaptive-plan", requestBody, headers)
	if resp.StatusCode != http.StatusOK || calls.Load() != 3 {
		t.Fatalf("failed reservation retained: status=%d calls=%d body=%s", resp.StatusCode, calls.Load(), body)
	}
	replay, replayed := call(t, app, "POST", "/api/v1/adaptive-plan", requestBody, headers)
	if replay.StatusCode != http.StatusOK || replay.Header.Get("Idempotent-Replayed") != "true" || replayed != body || calls.Load() != 3 {
		t.Fatalf("replay status=%d calls=%d body=%s", replay.StatusCode, calls.Load(), replayed)
	}
}
