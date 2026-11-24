package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func wireHeaders(requestID string) http.Header {
	return http.Header{"X-Request-Id": {requestID}}
}

func assertWireError(t *testing.T, resp wireResponse, body string, status int, code, details, requestID string) {
	t.Helper()
	want, err := json.Marshal(struct {
		Error   string `json:"error"`
		Details string `json:"details"`
	}{code, details})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status || resp.Header.Get("Content-Type") != "application/json; charset=utf-8" || body != string(want) {
		t.Fatalf("status=%d headers=%v body=%s, want %d %s", resp.StatusCode, resp.Header, body, status, want)
	}
	assertWireID(t, resp, requestID)
}

func assertWireID(t *testing.T, resp wireResponse, requestID string) {
	t.Helper()
	ids := resp.Header.Values("X-Request-ID")
	if len(ids) != 1 {
		t.Fatalf("request ID headers=%q, want exactly one", ids)
	}
	if requestID == "" {
		if decoded, err := base64.RawURLEncoding.DecodeString(ids[0]); err != nil || len(decoded) != 32 {
			t.Fatalf("generated request ID=%q, want 32 bytes of base64url data", ids[0])
		}
	} else if ids[0] != requestID {
		t.Fatalf("request ID=%q, want %q", ids[0], requestID)
	}
}

func TestWirePlanReplayAndKeepAlive(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	wirePlanReplayAndKeepAlive(t, defaultConfig(t))
}

func wirePlanReplayAndKeepAlive(t *testing.T, cfg config.Config) {
	t.Helper()
	cfg.RateLimitMax = 10
	var calls atomic.Int32
	addr, _, _ := startWireApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return wireItems(r), nil
	}, nil)
	assertWirePlanReplayAndKeepAlive(t, addr, calls.Load)
}

func assertWirePlanReplayAndKeepAlive(t *testing.T, addr wireAddress, plannerCalls func() int32) {
	t.Helper()
	conn := addr.dial(t)
	headers := wireHeaders("wire-original")
	headers.Set("Idempotency-Key", "wire-replay")
	first, original := conn.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
	assertWirePlan(t, original)
	assertWireSuccessHeaders(t, first, "wire-original", "9", "", original)

	// Read the next response from the same socket and the same buffered reader.
	headers.Set("X-Request-ID", "wire-replay")
	replay, body := conn.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
	assertWireSuccessHeaders(t, replay, "wire-replay", "8", "true", body)
	if calls := plannerCalls(); body != original || calls != 1 {
		t.Fatalf("replay body=%s, original=%s, planner calls=%d", body, original, calls)
	}
}

func assertWireSuccessHeaders(t *testing.T, resp wireResponse, id, remaining, replay, body string) {
	t.Helper()
	if resp.StatusCode != http.StatusOK || resp.Proto != "HTTP/1.1" || resp.Close || resp.ContentLength != int64(len(body)) {
		t.Fatalf("unexpected success response: %+v", resp)
	}
	assertWireID(t, resp, id)
	if _, err := http.ParseTime(resp.Header.Get("Date")); err != nil {
		t.Fatalf("Date: %v", err)
	}
	if reset, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Reset")); err != nil || reset < 0 || reset > 60 {
		t.Fatalf("rate limit reset=%q", resp.Header.Get("X-RateLimit-Reset"))
	}
	want := map[string]string{
		"Content-Length":                    strconv.Itoa(len(body)),
		"Content-Type":                      "application/json; charset=utf-8",
		"Cross-Origin-Embedder-Policy":      "require-corp",
		"Cross-Origin-Opener-Policy":        "same-origin",
		"Cross-Origin-Resource-Policy":      "same-origin",
		"Origin-Agent-Cluster":              "?1",
		"Referrer-Policy":                   "no-referrer",
		"X-RateLimit-Limit":                 "10",
		"X-RateLimit-Remaining":             remaining,
		"X-Content-Type-Options":            "nosniff",
		"X-Dns-Prefetch-Control":            "off",
		"X-Download-Options":                "noopen",
		"X-Frame-Options":                   "SAMEORIGIN",
		"X-Permitted-Cross-Domain-Policies": "none",
		"X-Request-ID":                      id,
		"X-XSS-Protection":                  "0",
	}
	if replay != "" {
		want["Idempotent-Replayed"] = replay
	}
	if len(resp.Header) != len(want)+2 {
		t.Errorf("unexpected header set: %v", resp.Header)
	}
	for name, value := range want {
		if got := resp.Header.Values(name); len(got) != 1 || got[0] != value {
			t.Errorf("%s=%q, want [%q]", name, got, value)
		}
	}
}

func assertWirePlan(t *testing.T, body string) {
	t.Helper()
	const expected = `{"student_id":"student-42","course_id":"cs101","items":[{"topic":"Recursion","priority":1,"prerequisite_chain":["Functions"],"explanation":"Study Recursion","resources":[]}],"_meta":{"subgraph_tuples":3,"topics_processed":1,"items_generated":1,"items_after_validation":1,"items_dropped":0}}`
	var got, want map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	meta, ok := got["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("plan metadata=%v", got["_meta"])
	}
	for _, name := range []string{"llm_latency_ms", "total_latency_ms"} {
		if value, ok := meta[name].(float64); !ok || value < 0 || value != float64(int64(value)) {
			t.Fatalf("%s=%v, want non-negative integer milliseconds", name, meta[name])
		}
		delete(meta, name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan=%s\nwant=%s (without latency fields)", body, expected)
	}
}

const wireHeaderReadTimeout = 200 * time.Millisecond

func TestWireRejectedRequests(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	wireRejectedRequests(t, defaultConfig(t))
}

func wireRejectedRequests(t *testing.T, cfg config.Config) {
	t.Helper()
	cfg.AuthDisabled = false
	cfg.InternalToken = strings.Repeat("w", 32)
	var calls atomic.Int32
	addr, _, _ := startWireApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return wireItems(r), nil
	}, nil)
	const id = "wire-invalid"
	validHeaders := "X-Request-ID: " + id + "\r\nX-Internal-Token: " + cfg.InternalToken + "\r\nContent-Type: application/json\r\n"
	fixedBody := func(body string) string { return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body) }
	chunk := strings.Repeat(" ", 32*1024)
	for _, tc := range []struct {
		name, raw, code, details, id string
		status                       int
	}{
		{"fixed length limit", validHeaders + "Content-Length: 65537\r\n\r\n", "payload_too_large", "Request Entity Too Large", id, http.StatusRequestEntityTooLarge},
		{"chunked limit", validHeaders + "Transfer-Encoding: chunked\r\n\r\n8000\r\n" + chunk + "\r\n8000\r\n" + chunk + "\r\n1\r\nx\r\n0\r\n\r\n", "payload_too_large", "Request Entity Too Large", "", http.StatusRequestEntityTooLarge},
		{"malformed chunk", validHeaders + "Transfer-Encoding: chunked\r\n\r\nZ\r\n", "bad_request", "Bad Request", "", http.StatusBadRequest},
		{"malformed JSON", validHeaders + fixedBody("{"), "invalid_request_body", "request body must be valid JSON", id, http.StatusBadRequest},
		{"unauthorized", "X-Request-ID: " + id + "\r\nContent-Type: application/json\r\n" + fixedBody(wireTopicBody), "unauthorized", "missing or invalid internal token", id, http.StatusUnauthorized},
		{"unsupported media type", strings.Replace(validHeaders, "application/json", "text/plain", 1) + fixedBody(wireTopicBody), "unsupported_media_type", "Content-Type must be application/json", id, http.StatusUnsupportedMediaType},
		{"unknown topic", validHeaders + fixedBody(strings.Replace(wireTopicBody, "Recursion", "Unknown", 1)), "validation_error", `failed_topics contains unknown topics: "Unknown"`, id, http.StatusUnprocessableEntity},
	} {
		for _, path := range []string{wirePlan, wireStream} {
			t.Run(tc.name+strings.TrimPrefix(path, wirePlan), func(t *testing.T) {
				conn := addr.dial(t)
				if tc.status == http.StatusUnauthorized {
					// Simulate an idle client before sending its unauthorized request.
					time.Sleep(wireHeaderReadTimeout + 50*time.Millisecond)
				}
				conn.sendRaw(t, path, tc.raw)
				resp := conn.response(t, nil)
				assertWireError(t, resp, readWireBody(t, resp.Body), tc.status, tc.code, tc.details, tc.id)
			})
		}
	}
	t.Run("duplicate idempotency headers", func(t *testing.T) {
		conn := addr.dial(t)
		conn.sendRaw(t, wirePlan, validHeaders+"Idempotency-Key: same\r\nidempotency-key: same\r\n"+fixedBody(wireTopicBody))
		resp := conn.response(t, nil)
		assertWireError(t, resp, readWireBody(t, resp.Body), http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must contain 1 to 255 visible ASCII characters", id)
	})
	if calls.Load() != 0 {
		t.Fatalf("rejected requests dispatched %d planner calls", calls.Load())
	}
}

func TestWireHeaderTimeout(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	cfg := defaultConfig(t)
	cfg.AuthDisabled = false
	cfg.InternalToken = strings.Repeat("w", 32)
	cfg.ReadTimeout = wireHeaderReadTimeout
	reject := func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
		t.Error("incomplete header reached the planner")
		return nil, nil
	}
	partial := "X-Request-ID: wire-incomplete\r\nX-Internal-Token: " + cfg.InternalToken + "\r\nContent-Type: application/json\r\nX-Incomplete: "
	for _, path := range []string{wirePlan, wireStream} {
		t.Run(strings.TrimPrefix(path, "/api/v1/"), func(t *testing.T) {
			conn, elapsed := sendPartialWireRequest(t, cfg, reject, path, partial)
			resp := conn.response(t, nil)
			assertWireError(t, resp, readWireBody(t, resp.Body), http.StatusRequestTimeout, "request_timeout", "Request Timeout", "")
			if elapsed < cfg.ReadTimeout {
				t.Fatalf("header response arrived after %s, before the %s read deadline", elapsed, cfg.ReadTimeout)
			}
		})
	}
}

func TestWireRateLimitSharedByPlanRoutes(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	wireRateLimitSharedByPlanRoutes(t, defaultConfig(t))
}

func wireRateLimitSharedByPlanRoutes(t *testing.T, cfg config.Config) {
	t.Helper()
	cfg.RateLimitMax = 1
	var calls atomic.Int32
	addr, _, _ := startWireApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return wireItems(r), nil
	}, nil)
	conn := addr.dial(t)
	first, body := conn.exchange(t, http.MethodPost, wirePlan, wireTopicBody, nil)
	if first.StatusCode != http.StatusOK || first.Header.Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("first status=%d headers=%v body=%s", first.StatusCode, first.Header, body)
	}
	second, body := conn.exchange(t, http.MethodPost, wireStream, wireTopicBody, wireHeaders("wire-quota"))
	assertWireError(t, second, body, http.StatusTooManyRequests, "rate_limit_exceeded", "too many requests", "wire-quota")
	if retry, err := strconv.Atoi(second.Header.Get("Retry-After")); err != nil || retry <= 0 || retry > 60 || calls.Load() != 1 {
		t.Fatalf("Retry-After=%q planner calls=%d", second.Header.Get("Retry-After"), calls.Load())
	}
}

func TestWireIdempotencyInFlightConflicts(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	wireIdempotencyInFlightConflicts(t, defaultConfig(t))
}

func wireIdempotencyInFlightConflicts(t *testing.T, cfg config.Config) {
	t.Helper()
	first := newWireCall(t)
	addr, _, _ := startWireApp(t, cfg, planWireCalls(map[string]*wireCall{"wire-pending": first}), nil)
	headers := wireHeaders("wire-pending")
	headers.Set("Idempotency-Key", "wire-conflict")
	original := addr.dial(t)
	request := original.send(t, http.MethodPost, wirePlan, wireTopicBody, headers)
	receiveWire(t, first.entered, "reserved request planner")

	other := addr.dial(t)
	headers.Set("X-Request-ID", "wire-conflict")
	resp, body := other.exchange(t, http.MethodPost, wirePlan, wireTopicBody, headers)
	assertWireError(t, resp, body, http.StatusConflict, "idempotency_key_in_progress", "request with this Idempotency-Key is still in progress", "wire-conflict")
	if resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("Retry-After=%q", resp.Header.Get("Retry-After"))
	}
	headers.Set("X-Request-ID", "wire-mismatch")
	resp, body = other.exchange(t, http.MethodPost, wirePlan, wireTopicBody+" ", headers)
	assertWireError(t, resp, body, http.StatusUnprocessableEntity, "idempotency_key_mismatch", "Idempotency-Key was used with a different request body", "wire-mismatch")

	first.unblock()
	resp = original.response(t, request)
	body = readWireBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("original status=%d headers=%v body=%s", resp.StatusCode, resp.Header, body)
	}
	assertWireID(t, resp, "wire-pending")
	assertWirePlan(t, body)
}
