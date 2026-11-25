package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/idempotency"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestVerifiedReplay(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testVerifiedReplay(t, "") })
	t.Run("miniredis", func(t *testing.T) {
		testVerifiedReplay(t, "redis://"+miniredis.RunT(t).Addr())
	})
	t.Run("real Redis", func(t *testing.T) {
		url := os.Getenv("AXIOM_TEST_REDIS_URL")
		if url == "" {
			t.Skip("set AXIOM_TEST_REDIS_URL to run against a Redis server")
		}
		testVerifiedReplay(t, url)
	})
}

func testVerifiedReplay(t *testing.T, redisURL string) {
	t.Helper()
	cfg, fixture := newVerifiedFixture(t, true)
	cfg.Redis.URL = redisURL
	cfg.RateLimitMax = 5_000
	cfg.Idempotency.TTL = time.Minute
	const tenant, caller = "tenant:one", "verified-caller"
	initial := fixture.token(t, fixture.signer, tenant, caller, "graph:read plan:generate")
	rotated := fixture.token(t, fixture.next, tenant, caller, "plan:generate")
	reduced := fixture.token(t, fixture.next, tenant, caller, "graph:read")
	key := uuid.NewString()
	if redisURL != "" {
		seedOtherReplayNamespaces(t, redisURL, key, initial)
	}
	var calls atomic.Int32
	var failNext atomic.Bool
	started := make(chan struct{}, 1)
	gate, release := context.WithCancel(t.Context())
	defer release()
	planner := func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		n := calls.Add(1)
		if n == 1 {
			started <- struct{}{}
			select {
			case <-gate.Done():
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if failNext.Swap(false) {
			return nil, service.ErrUpstreamRejected
		}
		items := wireItems(req)
		items[0].Explanation = fmt.Sprintf("Study session %d", n)
		return items, nil
	}
	first := newTestApp(t, cfg, planner, nil)
	second := first
	if redisURL != "" {
		second = newTestApp(t, cfg, planner, nil)
		t.Cleanup(func() { _ = first.Shutdown(); _ = second.Shutdown() })
	}
	type result struct {
		response response
		body     string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		// The planner gate controls completion. The result wait bounds hangs.
		resp, body, err := callWithError(context.Background(), first, http.MethodPost, wirePlan, wireTopicBody, replayHeaders(initial, key), fiber.TestConfig{})
		done <- result{resp, body, err}
	}()
	receiveWire(t, started, "first reservation's planner")
	busy, body := call(t, second, http.MethodPost, wirePlan, wireTopicBody, replayHeaders(initial, key))
	if busy.StatusCode != http.StatusConflict || busy.Header.Get("Retry-After") != "1" ||
		!strings.Contains(body, `"error":"idempotency_key_in_progress"`) || calls.Load() != 1 {
		t.Errorf("concurrent response status=%d headers=%v body=%s calls=%d", busy.StatusCode, busy.Header, body, calls.Load())
	}

	// These principals would collide if tenant and caller were joined by ':'.
	independent := fixture.token(t, fixture.signer, "tenant", "one:"+caller, "plan:generate")
	isolated, isolatedBody := call(t, second, http.MethodPost, wirePlan, wireTopicBody, replayHeaders(independent, key))
	if isolated.StatusCode != http.StatusOK || isolated.Header.Get("Idempotent-Replayed") != "" || !strings.Contains(isolatedBody, "Study session 2") {
		t.Errorf("independent reservation status=%d body=%s", isolated.StatusCode, isolatedBody)
	}
	refreshAfter := time.Now().Add(time.Second)
	if redisURL == "" {
		// Exercise a held request after the default test client deadline.
		time.Sleep(1100 * time.Millisecond)
	}
	release()
	original := receiveWire(t, done, "first reservation's response")
	if original.err != nil || original.response.StatusCode != http.StatusOK ||
		original.response.Header.Get("Idempotent-Replayed") != "" || !strings.Contains(original.body, "Study session 1") {
		t.Fatalf("original status=%d body=%s err=%v", original.response.StatusCode, original.body, original.err)
	}
	var plan domain.PlanResponse
	if err := json.Unmarshal([]byte(original.body), &plan); err != nil || plan.StudentID != "student-42" || plan.CourseID != "cs101" {
		t.Fatal("original response must echo the request identifiers")
	}
	for _, identity := range [][2]string{{tenant, "another-caller"}, {"other-tenant", caller}} {
		token := fixture.token(t, fixture.signer, identity[0], identity[1], "plan:generate")
		headers := replayHeaders(token, key)
		headers["X-Tenant-ID"], headers["X-Subject"] = tenant, caller
		isolated, body := call(t, second, http.MethodPost, wirePlan, wireTopicBody, headers)
		if isolated.StatusCode != http.StatusOK || isolated.Header.Get("Idempotent-Replayed") != "" || body == original.body {
			t.Errorf("isolated status=%d body=%s", isolated.StatusCode, body)
		}
		replay, replayBody := call(t, second, http.MethodPost, wirePlan, wireTopicBody, headers)
		if replay.StatusCode != http.StatusOK || replay.Header.Get("Idempotent-Replayed") != "true" || replayBody != body {
			t.Errorf("isolated replay status=%d body=%s", replay.StatusCode, replayBody)
		}
	}

	// The verifier permits a fresh key fetch one second after the first.
	fixture.keys.Store(fixture.latest)
	time.Sleep(time.Until(refreshAfter))
	assertVerifiedReplay(t, second, rotated, key, original.body)
	denied, body := call(t, second, http.MethodPost, wirePlan, wireTopicBody, replayHeaders(reduced, key))
	if denied.StatusCode != http.StatusForbidden || denied.Header.Get("Idempotent-Replayed") != "" ||
		denied.Header.Get("X-RateLimit-Limit") != "" || !strings.Contains(body, `"error":"insufficient_scope"`) {
		t.Errorf("scope loss status=%d headers=%v body=%s", denied.StatusCode, denied.Header, body)
	}
	retired, body := call(t, second, http.MethodPost, wirePlan, wireTopicBody, replayHeaders(initial, key))
	if retired.StatusCode != http.StatusUnauthorized || retired.Header.Get("Idempotent-Replayed") != "" || retired.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("retired key status=%d headers=%v body=%s", retired.StatusCode, retired.Header, body)
	}
	mismatch, body := call(t, second, http.MethodPost, wirePlan, wireTopicBody+" ", replayHeaders(rotated, key))
	if mismatch.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `"error":"idempotency_key_mismatch"`) || calls.Load() != 4 {
		t.Errorf("mismatch status=%d calls=%d body=%s", mismatch.StatusCode, calls.Load(), body)
	}

	failedKey := uuid.NewString()
	failNext.Store(true)
	failed, body := call(t, first, http.MethodPost, wirePlan, wireTopicBody, replayHeaders(rotated, failedKey))
	if failed.StatusCode != http.StatusBadGateway {
		t.Errorf("planner failure status=%d body=%s", failed.StatusCode, body)
	}
	retry, body := call(t, second, http.MethodPost, wirePlan, wireTopicBody, replayHeaders(rotated, failedKey))
	if retry.StatusCode != http.StatusOK || retry.Header.Get("Idempotent-Replayed") != "" || calls.Load() != 6 {
		t.Errorf("retry after abort status=%d calls=%d body=%s", retry.StatusCode, calls.Load(), body)
	}
	assertVerifiedReplay(t, second, rotated, failedKey, body)
	if redisURL != "" {
		assertVerifiedRedisStorage(t, second, redisURL, key, replayMarkers{fixture.issuer, tenant, caller, initial, rotated})
	}
	if got := calls.Load(); got != 6 {
		t.Errorf("planner calls after replay and storage checks=%d, want 6", got)
	}
}

func replayHeaders(token, key string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": key}
}

func assertVerifiedReplay(t *testing.T, app *fiber.App, token, key, wantBody string) {
	t.Helper()
	resp, body := call(t, app, http.MethodPost, wirePlan, wireTopicBody, replayHeaders(token, key))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "true" ||
		resp.Header.Get("Content-Type") != "application/json; charset=utf-8" || body != wantBody {
		t.Errorf("replay status=%d headers=%v body=%s, want original %s", resp.StatusCode, resp.Header, body, wantBody)
	}
}

const legacyPlanBody = `{"student_id": "student-42", "course_id": "cs101", "items": [], "_meta": {}}`

func seedOtherReplayNamespaces(t *testing.T, redisURL, key, token string) {
	t.Helper()
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	cfg := defaultConfig(t)
	cfg.Redis.URL = redisURL
	cfg.RateLimitMax = 5_000
	cfg.Idempotency.TTL = time.Minute
	anonymous := newTestApp(t, cfg, nil, nil)
	cfg.AuthDisabled = false
	cfg.InternalToken = token
	shared := newTestApp(t, cfg, nil, nil)
	t.Cleanup(func() { _ = anonymous.Shutdown(); _ = shared.Shutdown() })
	for _, tc := range []struct {
		app       *fiber.App
		namespace string
	}{{anonymous, "anonymous"}, {shared, fmt.Sprintf("%x", sha256.Sum256([]byte(token)))}} {
		seedLegacyReplay(t, client, tc.namespace+":"+key)
		headers := replayHeaders(token, key)
		if tc.app == anonymous {
			headers["Authorization"] = "Bearer unrelated"
			headers["X-Internal-Token"] = "also unrelated"
		}
		replay, replayBody := call(t, tc.app, http.MethodPost, wirePlan, wireTopicBody, headers)
		if replay.StatusCode != http.StatusOK || replay.Header.Get("Idempotent-Replayed") != "true" || replayBody != legacyPlanBody {
			t.Fatalf("legacy replay status=%d body=%s", replay.StatusCode, replayBody)
		}
	}
}

func seedLegacyReplay(t *testing.T, client *redis.Client, key string) {
	t.Helper()
	store := idempotency.NewRedis(client, time.Minute, time.Minute)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(wireTopicBody)))
	reservation, state, err := store.Begin(t.Context(), key, fingerprint)
	if err != nil || state != idempotency.Acquired {
		t.Fatalf("seed legacy reservation: state=%v err=%v", state, err)
	}
	response := idempotency.Response{Status: http.StatusOK, ContentType: "application/json; charset=utf-8", Body: []byte(legacyPlanBody)}
	if err := store.Complete(t.Context(), key, reservation.Token, response); err != nil {
		t.Fatal(err)
	}
}

type replayMarkers struct {
	issuer, tenant, caller, initial, rotated string
}

func assertVerifiedRedisStorage(t *testing.T, app *fiber.App, url, key string, markers replayMarkers) {
	t.Helper()
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	digest := sha256.New()
	for _, field := range []string{markers.issuer, markers.tenant, markers.caller} {
		if err := binary.Write(digest, binary.BigEndian, uint64(len(field))); err != nil {
			t.Fatal(err)
		}
		_, _ = digest.Write([]byte(field))
	}
	namespace := fmt.Sprintf("verified-v1:%x:", digest.Sum(nil))
	cacheKey := func(key string) string {
		return fmt.Sprintf("axiom:idempotency:%x", sha256.Sum256([]byte(namespace+key)))
	}
	legacyKey := uuid.NewString()
	seedLegacyReplay(t, client, namespace+legacyKey)
	assertVerifiedReplay(t, app, markers.rotated, legacyKey, legacyPlanBody)
	hash, err := client.HGetAll(t.Context(), cacheKey(key)).Result()
	if err != nil || hash["response"] == "" || hash["fingerprint"] == "" {
		t.Fatalf("versioned, length-framed Redis key missing success: %v", err)
	}
	if err := checkReplayPrivacy(hash, markers); err != nil {
		t.Fatal(err)
	}
	ttl, err := client.PTTL(t.Context(), cacheKey(key)).Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("successful response TTL=%s err=%v, want a positive TTL of at most one minute", ttl, err)
	}
	badKey := uuid.NewString()
	if err := client.Set(t.Context(), cacheKey(badKey), "wrong type", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	retry, body := call(t, app, http.MethodPost, wirePlan, wireTopicBody, replayHeaders(markers.rotated, badKey))
	if retry.StatusCode != http.StatusServiceUnavailable || retry.Header.Get("Retry-After") != "1" || !strings.Contains(body, `"error":"service_unavailable"`) {
		t.Errorf("replay store failure status=%d headers=%v body=%s", retry.StatusCode, retry.Header, body)
	}
}

func checkReplayPrivacy(hash map[string]string, markers replayMarkers) error {
	const student, course = "student-42", "cs101"
	private := []string{markers.issuer, markers.tenant, markers.caller, markers.initial, markers.rotated, student, course}
	for field, value := range hash {
		if field == "response" {
			continue
		}
		for _, marker := range private {
			if strings.Contains(field, marker) || strings.Contains(value, marker) {
				return fmt.Errorf("private marker in Redis metadata")
			}
		}
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(wireTopicBody)))
	if _, err := uuid.Parse(hash["token"]); err != nil || len(hash) != 3 || hash["fingerprint"] != fingerprint {
		return fmt.Errorf("unexpected Redis metadata")
	}

	var stored idempotency.Response
	decode := json.NewDecoder(strings.NewReader(hash["response"]))
	decode.DisallowUnknownFields()
	if err := decode.Decode(&stored); err != nil || stored.Format != "plan-v1" || stored.Status != http.StatusOK || stored.ContentType != "application/json; charset=utf-8" {
		return fmt.Errorf("invalid stored response envelope")
	}
	var body map[string]any
	if err := json.Unmarshal(stored.Body, &body); err != nil {
		return fmt.Errorf("invalid decoded response body")
	}
	for _, field := range []string{"student_id", "course_id"} {
		if _, present := body[field]; present {
			return fmt.Errorf("found %s in stored response", field)
		}
	}
	remainder, err := json.Marshal(body)
	if err != nil {
		return err
	}
	for _, marker := range private {
		if strings.Contains(string(remainder), marker) {
			return fmt.Errorf("private marker in decoded response")
		}
	}
	return nil
}

func TestVerifiedReplayRecordPrivacy(t *testing.T) {
	markers := replayMarkers{"https://issuer.example", "tenant-marker", "caller-marker", "initial-token-marker", "rotated-token-marker"}
	const plan = `"items":[],"_meta":{}`
	for _, tc := range []struct{ name, fingerprintSuffix, body, want string }{
		{"identifier-free plan", "", "{" + plan + "}", ""},
		{"initial credential in metadata", markers.initial, "{" + plan + "}", "metadata"},
		{"initial credential in decoded body", "", "{" + plan + `,"unexpected":"` + markers.initial + `"}`, "decoded response"},
		{"rotated credential in decoded body", "", "{" + plan + `,"unexpected":"` + markers.rotated + `"}`, "decoded response"},
		{"principal in decoded body", "", "{" + plan + `,"unexpected":"` + markers.caller + `"}`, "decoded response"},
		{"learner in decoded body", "", "{" + plan + `,"unexpected":{"echo":"student-42"}}`, "decoded response"},
		{"course in decoded body", "", "{" + plan + `,"unexpected":"cs101"}`, "decoded response"},
		{"learner field", "", "{" + plan + `,"student_id":"student-42"}`, "student_id"},
		{"course field", "", "{" + plan + `,"course_id":"cs101"}`, "course_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(idempotency.Response{Status: http.StatusOK, Body: []byte(tc.body), ContentType: "application/json; charset=utf-8", Format: "plan-v1"})
			if err != nil {
				t.Fatal(err)
			}
			hash := map[string]string{
				"response": string(encoded), "fingerprint": fmt.Sprintf("%x", sha256.Sum256([]byte(wireTopicBody))) + tc.fingerprintSuffix,
				"token": uuid.NewString(),
			}
			err = checkReplayPrivacy(hash, markers)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want %q", err, tc.want)
			}
		})
	}
}
