//go:build linux || darwin

package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestGraphReloadHTTPRoutesReplayAndQuota(t *testing.T) {
	const oldGraph = `[{"subject":"Recursion","relation":"depends_on","object":"Old Root"},{"subject":"Retired","relation":"depends_on","object":"Old Root"}]`
	const newGraph = `[{"subject":"Recursion","relation":"depends_on","object":"New Root"}]`
	load := func(contents string) *graph.Snapshot {
		t.Helper()
		path := filepath.Join(t.TempDir(), "curriculum.json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshot, err := graph.LoadSnapshot(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	curriculum, replacement := graph.NewCurriculum(load(oldGraph)), load(newGraph)
	redis := miniredis.RunT(t)
	cfg := redisConfig(t, "redis://"+redis.Addr())
	cfg.RateLimitMax = 12
	cfg.Idempotency.TTL = 10 * time.Second
	var started, release chan struct{}
	var calls atomic.Int32
	planner := fakePlanner(func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		if req.Topic == "Recursion" && strings.Join(req.PrerequisiteOrder, ",") == "Old Root" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []domain.PlanItem{{Topic: req.Topic}}, nil
	})
	app := New(cfg, Dependencies{Curriculum: curriculum, Planner: planner, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { _ = app.Shutdown() })
	assertReads := func(root, other string) {
		t.Helper()
		for _, path := range []string{
			"/api/v1/topics?q=recursion",
			"/api/v1/topics/Recursion/path",
			"/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion",
		} {
			resp, body := call(t, app, http.MethodGet, path, "", nil)
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, root) || strings.Contains(body, other) {
				t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
			}
		}
	}
	assertReads("Old Root", "New Root")
	const retired = `{"failed_topics":["Retired"],"vark_profile":"visual"}`
	key := map[string]string{"Idempotency-Key": "retained-across-reload"}
	first, retained := call(t, app, http.MethodPost, wirePlan, retired, key)
	if first.StatusCode != http.StatusOK || !strings.Contains(retained, `"Old Root"`) {
		t.Fatalf("original response: %d %s", first.StatusCode, retained)
	}
	redis.FastForward(9 * time.Second)
	type streamResult struct {
		status int
		body   string
		err    error
	}
	callStream := func(ctx context.Context) streamResult {
		deadline, _ := ctx.Deadline()
		timeout := time.Until(deadline)
		if timeout <= 0 {
			return streamResult{err: context.DeadlineExceeded}
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, wireStream, strings.NewReader(wireTopicBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, fiber.TestConfig{Timeout: timeout, FailOnTimeout: true})
		if err != nil {
			return streamResult{err: err}
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		return streamResult{status: resp.StatusCode, body: string(body), err: err}
	}
	assertStream := func(t *testing.T, got streamResult, root, other string) {
		t.Helper()
		if got.err != nil {
			t.Fatal(got.err)
		}
		events := parseEvents(t, got.body)
		if got.status != http.StatusOK || len(events) != 2 || events[0].name != "topic" || events[1].name != "plan" || !strings.Contains(string(events[1].data), root) || strings.Contains(string(events[1].data), other) {
			t.Fatalf("stream: %d %s", got.status, got.body)
		}
	}
	// Allow the entire HTTP request budget plus time to collect the response.
	streamTimeout := cfg.RequestTimeout + time.Second
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), streamTimeout)
		defer cancel()
		started, release = make(chan struct{}), make(chan struct{})
		releaseStream := sync.OnceFunc(func() { close(release) })
		defer releaseStream()
		stream := make(chan streamResult, 1)
		go func() { stream <- callStream(ctx) }()
		select {
		case <-started:
		case got := <-stream:
			t.Fatalf("stream ended before reload: %+v", got)
		case <-ctx.Done():
			t.Fatal("stream did not dispatch")
		}
		// Pass Fiber's one-second default while the planner waits for publication.
		time.Sleep(2 * time.Second)
		if !curriculum.Publish(replacement) {
			t.Fatal("replacement failed")
		}
		releaseStream()
		select {
		case got := <-stream:
			assertStream(t, got, "Old Root", "New Root")
		case <-ctx.Done():
			t.Fatal("stream did not finish")
		}
	})
	assertReads("New Root", "Old Root")
	replay, replayBody := call(t, app, http.MethodPost, wirePlan, retired, key)
	if replay.StatusCode != http.StatusOK || replayBody != retained || replay.Header.Get("Idempotent-Replayed") != "true" || calls.Load() != 2 {
		t.Fatalf("retained response: %d %s planner calls=%d", replay.StatusCode, replayBody, calls.Load())
	}
	redis.FastForward(2 * time.Second)
	expired, expiredBody := call(t, app, http.MethodPost, wirePlan, retired, key)
	if expired.StatusCode != http.StatusUnprocessableEntity || expired.Header.Get("Idempotent-Replayed") != "" || !strings.Contains(expiredBody, `"validation_error"`) || calls.Load() != 2 {
		t.Fatalf("original TTL: %d %s planner calls=%d", expired.StatusCode, expiredBody, calls.Load())
	}
	fresh, freshBody := call(t, app, http.MethodPost, wirePlan, wireTopicBody, nil)
	if fresh.StatusCode != http.StatusOK || !strings.Contains(freshBody, `"New Root"`) || strings.Contains(freshBody, `"Old Root"`) {
		t.Fatalf("fresh JSON plan: %d %s", fresh.StatusCode, freshBody)
	}
	ctx, cancel := context.WithTimeout(t.Context(), streamTimeout)
	defer cancel()
	assertStream(t, callStream(ctx), "New Root", "Old Root")
	limited, _ := call(t, app, http.MethodGet, "/api/v1/topics", "", nil)
	if limited.StatusCode != http.StatusTooManyRequests || calls.Load() != 4 {
		t.Fatalf("quota after reload: status=%d planner calls=%d", limited.StatusCode, calls.Load())
	}
}
