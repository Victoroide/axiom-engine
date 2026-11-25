package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	redisstorage "github.com/gofiber/storage/redis/v3"
	redisclient "github.com/redis/go-redis/v9"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
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
	testDeadline, _ := t.Deadline()
	ctx, cancel := context.WithDeadline(t.Context(), redisBudgetDeadline(time.Now(), testDeadline))
	defer cancel()
	newReplica := func() *fiber.App {
		client := redisclient.NewClient(&redisclient.Options{
			Addr: redis.Addr(), DialTimeout: 30 * time.Second, ReadTimeout: 30 * time.Second,
			WriteTimeout: 30 * time.Second, ContextTimeoutEnabled: true, MaxRetries: -1,
		})
		t.Cleanup(func() { _ = client.Close() })
		if err := client.Ping(ctx).Err(); err != nil {
			t.Fatalf("wait for replica Redis connection: %v", err)
		}
		storage := redisstorage.NewFromConnectionWithContext(ctx, client)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		admission := newRedisAdmission(cfg, logger, storage)
		return newWithRedisAdmission(cfg, Dependencies{Graph: graph.NewInMemoryGraph()}, logger, admission) //nolint:contextcheck // Construction registers handlers that receive their own request contexts.
	}
	first, second := newReplica(), newReplica()
	t.Cleanup(func() { _ = first.Shutdown(); _ = second.Shutdown() })
	statuses, err := collectRedisBudgetStatuses(ctx, 24, func(i int) (int, error) {
		app := first
		if i%2 == 1 {
			app = second
		}
		resp, _, err := callWithDeadline(ctx, app, http.MethodGet, "/api/v1/tutor/student/s/cognitive-graph?topics=Recursion", "", nil)
		return resp.StatusCode, err
	})
	if err != nil {
		t.Fatal(err)
	}

	admitted := 0
	for _, status := range statuses {
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

func redisBudgetDeadline(now, testDeadline time.Time) time.Time {
	deadline := now.Add(30 * time.Second)
	if testDeadline.IsZero() {
		return deadline
	}
	// Leave cleanup time without spending the whole remaining budget.
	reserve := min(time.Second, max(0, testDeadline.Sub(now))/2)
	if reportBy := testDeadline.Add(-reserve); reportBy.Before(deadline) {
		return reportBy
	}
	return deadline
}

type redisBudgetResult struct {
	index  int
	status int
	err    error
}

func collectRedisBudgetStatuses(ctx context.Context, count int, request func(int) (int, error)) ([]int, error) {
	start := make(chan struct{})
	results := make(chan redisBudgetResult, count)
	for i := range count {
		go func() {
			<-start
			status, err := request(i)
			results <- redisBudgetResult{index: i, status: status, err: err}
		}()
	}
	close(start)
	return waitRedisBudgetStatuses(ctx, count, results)
}

func waitRedisBudgetStatuses(ctx context.Context, count int, results <-chan redisBudgetResult) ([]int, error) {
	statuses := make([]int, 0, count)
	var errs []error
	for received := range count {
		var r redisBudgetResult
		select {
		case r = <-results:
		case <-ctx.Done():
			// Keep any ready result even when the deadline wins the select.
			select {
			case r = <-results:
			default:
				return statuses, errors.Join(append(errs, fmt.Errorf("received %d/%d results: %w", received, count, ctx.Err()))...)
			}
		}
		if r.err != nil {
			errs = append(errs, fmt.Errorf("request %d: %w", r.index+1, r.err))
		} else {
			statuses = append(statuses, r.status)
		}
	}
	return statuses, errors.Join(errs...)
}

func TestRedisBudgetDeadline(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name         string
		testDeadline time.Time
		want         time.Time
	}{
		{"short budget", now.Add(500 * time.Millisecond), now.Add(250 * time.Millisecond)},
		{"normal budget", now.Add(4 * time.Second), now.Add(3 * time.Second)},
		{"long budget", now.Add(time.Minute), now.Add(30 * time.Second)},
		{"no deadline", time.Time{}, now.Add(30 * time.Second)},
		{"expired budget", now.Add(-time.Second), now.Add(-time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redisBudgetDeadline(now, tc.testDeadline); !got.Equal(tc.want) {
				t.Fatalf("collection budget = %s, want %s", got.Sub(now), tc.want.Sub(now))
			}
		})
	}
}

func TestRedisBudgetDrainsReadyResultsAtDeadline(t *testing.T) {
	requestErr := errors.New("request failed")
	// Repeat each schedule because the result and deadline are both ready.
	const trials = 64

	t.Run("incomplete batch", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for range trials {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				results := make(chan redisBudgetResult, 2)
				results <- redisBudgetResult{index: 0, err: requestErr}
				results <- redisBudgetResult{index: 1, status: http.StatusOK}
				<-ctx.Done()
				statuses, err := waitRedisBudgetStatuses(ctx, 3, results)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, requestErr) || !strings.Contains(err.Error(), "received 2/3 results") {
					t.Fatalf("collection error = %v, want request error and deadline after 2/3 results", err)
				}
				if len(statuses) != 1 || statuses[0] != http.StatusOK {
					t.Fatalf("statuses = %v, want [200]", statuses)
				}
			}
		})
	})

	t.Run("complete batch", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for range trials {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				results := make(chan redisBudgetResult, 1)
				results <- redisBudgetResult{status: http.StatusOK}
				<-ctx.Done()
				statuses, err := waitRedisBudgetStatuses(ctx, 1, results)
				cancel()
				if err != nil || len(statuses) != 1 || statuses[0] != http.StatusOK {
					t.Fatalf("statuses = %v, error = %v, want [200] and no error", statuses, err)
				}
			}
		})
	})
}

func TestRedisBudgetCollectsRequestErrors(t *testing.T) {
	app := fiber.New()
	app.Get("/ok", func(c fiber.Ctx) error { return c.SendStatus(http.StatusOK) })
	app.Get("/drop", func(c fiber.Ctx) error { return c.Drop() })
	app.Get("/truncated", func(c fiber.Ctx) error {
		c.Response().SkipBody = true
		c.Response().Header.SetContentLength(1)
		return nil
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	paths := [...]string{"/ok", "/drop", "/truncated"}
	statuses, err := collectRedisBudgetStatuses(ctx, len(paths), func(i int) (int, error) {
		resp, _, err := callWithDeadline(ctx, app, http.MethodGet, paths[i], "", nil)
		return resp.StatusCode, err
	})
	if !errors.Is(err, fiber.ErrTestGotEmptyResponse) || !errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request errors = %v, want dropped and truncated responses before the deadline", err)
	}
	if len(statuses) != 1 || statuses[0] != http.StatusOK {
		t.Fatalf("statuses = %v, want [200]", statuses)
	}
}

func TestRedisBudgetTimesOutOnMissingResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		release := make(chan struct{})
		defer close(release)
		requestErr := errors.New("request failed")
		_, err := collectRedisBudgetStatuses(ctx, 2, func(i int) (int, error) {
			if i == 0 {
				return 0, requestErr
			}
			<-release
			return http.StatusOK, nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, requestErr) || !strings.Contains(err.Error(), "received 1/2 results") {
			t.Fatalf("collection error = %v, want request error and deadline after 1/2 results", err)
		}
	})
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
