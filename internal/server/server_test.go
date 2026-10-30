package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type fakePlanner func(context.Context, service.TopicRequest) ([]domain.PlanItem, error)

func (f fakePlanner) GenerateTopic(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
	return f(ctx, r)
}
func defaultConfig(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Load(func(k string) string {
		if k == "AXIOM_AUTH_DISABLED" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func newTestApp(t *testing.T, c config.Config, p fakePlanner, logs io.Writer) *fiber.App {
	t.Helper()
	if p == nil {
		p = func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
			return []domain.PlanItem{{Topic: r.Topic}}, nil
		}
	}
	if logs == nil {
		logs = io.Discard
	}
	return New(c, Dependencies{Graph: graph.NewInMemoryGraph(), Planner: p, Logger: slog.New(slog.NewJSONHandler(logs, nil))})
}

type response struct {
	StatusCode int
	Header     http.Header
}

func call(t *testing.T, app *fiber.App, method, path, body string, headers map[string]string) (response, string) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{StatusCode: resp.StatusCode, Header: resp.Header}, string(data)
}

const validBody = `{"student_id":"student-42","course_id":"cs101","failed_topics":["Recursion","Polymorphism"],"vark_profile":"visual"}`

func TestServiceUnavailableCarriesRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name             string
		failure          error
		attempts         int
		backoff, details string
	}{
		{"open circuit", service.ErrUpstreamUnavailable, 4, "15", "reasoning temporarily unavailable"},
		{"throttled", service.ErrThrottled, 1, "5", "upstream throttled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t, defaultConfig(t), func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
				return nil, tc.failure
			}, nil)
			var resp response
			var body string
			for range tc.attempts {
				resp, body = call(t, app, "POST", "/api/v1/adaptive-plan", `{"failed_topics":["Recursion"],"vark_profile":"visual"}`, nil)
			}
			var got domain.ErrorResponse
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != tc.backoff || got.Error != "service_unavailable" || got.Details != tc.details {
				t.Fatalf("status=%d retry-after=%q body=%s", resp.StatusCode, resp.Header.Get("Retry-After"), body)
			}
		})
	}
}

func TestValidationAndUnknownTopics(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.MaxFailedTopics = 2
	app := newTestApp(t, cfg, func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
		t.Error("invalid request reached planner")
		return nil, nil
	}, nil)
	cases := []struct {
		body     string
		status   int
		contains string
	}{
		{`{`, 400, "invalid_request_body"}, {`{}`, 400, "vark_profile"}, {`{"vark_profile":"visual"}`, 400, "failed_topics"},
		{`{"vark_profile":"visual","failed_topics":[" ",""]}`, 400, "failed_topics"},
		{`{"vark_profile":"other","failed_topics":["Recursion"]}`, 400, "vark_profile"},
		{`{"vark_profile":"visual","failed_topics":["Recursion"],"student_id":"bad id"}`, 400, "student_id"},
		{`{"vark_profile":"visual","failed_topics":["Recursion"],"course_id":"bad/id"}`, 400, "course_id"},
		{`{"vark_profile":"visual","failed_topics":["Recursion","Polymorphism","Arrays"]}`, 400, "at most 2"},
		{`{"vark_profile":"visual","failed_topics":["unknown"]}`, 422, "unknown"},
		{`{"vark_profile":"visual","failed_topics":["Recursion","unknown"]}`, 422, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.contains+tc.body, func(t *testing.T) {
			r, b := call(t, app, "POST", "/api/v1/adaptive-plan", tc.body, nil)
			if r.StatusCode != tc.status || !strings.Contains(b, tc.contains) {
				t.Fatalf("%d: %s", r.StatusCode, b)
			}
		})
	}
}

func TestAuthenticationAndSuccess(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.AuthDisabled = false
	cfg.InternalToken = "test-secret"
	app := newTestApp(t, cfg, nil, nil)
	for _, token := range []string{"", "wrong"} {
		r, b := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"X-Internal-Token": token})
		if r.StatusCode != http.StatusUnauthorized || !strings.Contains(b, "unauthorized") {
			t.Fatalf("%d: %s", r.StatusCode, b)
		}
	}
	for _, headers := range []map[string]string{{"X-Internal-Token": "test-secret", "X-Request-ID": "request-42"}, {"Authorization": "Bearer test-secret"}} {
		r, b := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, headers)
		if r.StatusCode != http.StatusOK || !strings.Contains(b, "Recursion") || !strings.Contains(b, "Polymorphism") {
			t.Fatalf("%d: %s", r.StatusCode, b)
		}
		if r.Header.Get("X-Request-ID") == "" || (headers["X-Request-ID"] != "" && r.Header.Get("X-Request-ID") != headers["X-Request-ID"]) {
			t.Fatal("missing request ID")
		}
	}
	r, b := call(t, app, "GET", "/health", "", nil)
	if r.StatusCode != http.StatusOK || b != `{"status":"ok"}` {
		t.Fatalf("%d: %s", r.StatusCode, b)
	}
}

func TestAuthenticationRejectsEmptyConfiguredToken(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.AuthDisabled = false
	app := newTestApp(t, cfg, func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
		t.Error("unauthorized request reached planner")
		return nil, nil
	}, nil)
	for _, headers := range []map[string]string{nil, {"Authorization": "Bearer "}, {"X-Internal-Token": "unexpected"}} {
		r, b := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, headers)
		if r.StatusCode != http.StatusUnauthorized || !strings.Contains(b, "unauthorized") {
			t.Fatalf("%d: %s", r.StatusCode, b)
		}
	}
}

func TestPipelineErrorMappingAndLogging(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{context.DeadlineExceeded, 504, "gateway_timeout"}, {service.ErrCircuitOpen, 503, "service_unavailable"}, {fmt.Errorf("%w: secret-backend", service.ErrUpstream), 502, "bad_gateway"}, {errors.New("secret-backend"), 502, "bad_gateway"}} {
		t.Run(tc.code, func(t *testing.T) {
			var logs bytes.Buffer
			app := newTestApp(t, defaultConfig(t), func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) { return nil, tc.err }, &logs)
			r, b := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"X-Request-ID": "error-42"})
			if r.StatusCode != tc.status || !strings.Contains(b, tc.code) || strings.Contains(b, "secret-backend") {
				t.Fatalf("%d: %s", r.StatusCode, b)
			}
			if !strings.Contains(logs.String(), "error-42") || !strings.Contains(logs.String(), "request.failed") || !strings.Contains(logs.String(), "http.access") {
				t.Fatal(logs.String())
			}
		})
	}
	cfg := defaultConfig(t)
	cfg.LLMTimeout = time.Millisecond
	app := newTestApp(t, cfg, func(ctx context.Context, _ service.TopicRequest) ([]domain.PlanItem, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	if r, b := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, nil); r.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("%d: %s", r.StatusCode, b)
	}
}

func TestFrameworkErrorsAndRecovery(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), nil, nil)
	app.Get("/failure", func(fiber.Ctx) error { return errors.New("internal secret") })
	app.Get("/panic", func(fiber.Ctx) error { panic("internal secret") })
	app.Get("/method", func(fiber.Ctx) error { return fiber.ErrMethodNotAllowed })
	for _, tc := range []struct {
		path   string
		status int
		code   string
	}{{"/missing", 404, "not_found"}, {"/method", 405, "method_not_allowed"}, {"/failure", 500, "internal_error"}, {"/panic", 500, "internal_error"}} {
		r, b := call(t, app, "GET", tc.path, "", nil)
		var envelope domain.ErrorResponse
		if err := json.Unmarshal([]byte(b), &envelope); err != nil {
			t.Fatal(err)
		}
		if r.StatusCode != tc.status || envelope.Error != tc.code || strings.Contains(b, "secret") {
			t.Fatalf("%d: %s", r.StatusCode, b)
		}
	}

}

func TestRateLimitAndProxyTrust(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprint(trusted), func(t *testing.T) {
			cfg := defaultConfig(t)
			cfg.RateLimitMax = 1
			if trusted {
				cfg.TrustedProxies = []string{"0.0.0.0/0", "::/0"}
			}
			app := newTestApp(t, cfg, nil, nil)
			for i, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.1"} {
				r, b := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"X-Forwarded-For": ip})
				want := 200
				if i == 2 || (!trusted && i == 1) {
					want = 429
				}
				if r.StatusCode != want {
					t.Fatalf("trusted=%v i=%d %d: %s", trusted, i, r.StatusCode, b)
				}
			}
		})
	}
}

func TestCORS(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := defaultConfig(t)
		if enabled {
			cfg.CORSAllowedOrigins = []string{"https://example.com"}
		}
		app := newTestApp(t, cfg, nil, nil)
		r, b := call(t, app, "OPTIONS", "/api/v1/adaptive-plan", "", map[string]string{"Origin": "https://example.com", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "X-Internal-Token,X-Request-ID"})
		if enabled {
			if r.StatusCode != http.StatusNoContent || !strings.Contains(r.Header.Get("Access-Control-Allow-Headers"), "X-Internal-Token") || !strings.Contains(r.Header.Get("Access-Control-Allow-Headers"), "X-Request-ID") || strings.Contains(r.Header.Get("Access-Control-Allow-Methods"), "DELETE") {
				t.Fatalf("%d %v %s", r.StatusCode, r.Header, b)
			}
		} else if r.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("CORS unexpectedly enabled")
		}
	}
}

func TestCognitiveGraph(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), nil, nil)
	for _, tc := range []struct {
		path   string
		status int
	}{{"/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion,,Polymorphism&target_topic=Recursion", 200}, {"/api/v1/tutor/student/bad%20id/cognitive-graph?topics=Recursion", 400}, {"/api/v1/tutor/student/student-42/cognitive-graph", 400}, {"/api/v1/tutor/student/student-42/cognitive-graph?topics=,", 400}, {"/api/v1/tutor/student/student-42/cognitive-graph?topics=unknown", 422}, {"/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion&target_topic=" + url.QueryEscape("unknown"), 422}} {
		r, b := call(t, app, "GET", tc.path, "", nil)
		if r.StatusCode != tc.status {
			t.Fatalf("%s %d: %s", tc.path, r.StatusCode, b)
		}
		if tc.status == 200 && !strings.Contains(b, `"nodes":[`) {
			t.Fatal(b)
		}
	}
}

func TestSwaggerAssets(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), nil, nil)
	for _, path := range []string{"/swagger/index.html", "/swagger/doc.json", "/swagger/swagger-ui.css", "/swagger/swagger-ui-bundle.js"} {
		r, b := call(t, app, "GET", path, "", nil)
		if r.StatusCode != http.StatusOK || len(b) == 0 {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), nil, nil)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/adaptive-plan", strings.NewReader(strings.Repeat("a", 64*1024+1)))
	resp, err := app.Test(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, fasthttp.ErrBodyTooLarge) {
		t.Fatalf("body limit not enforced: %v", err)
	}
	// Fiber's Test returns the parser error before exposing its response.
	// Exercise the wire response separately rather than weakening the body limit.
	var logs bytes.Buffer
	app = newTestApp(t, defaultConfig(t), nil, &logs)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	defer func() {
		if err := app.ShutdownWithTimeout(time.Second); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	req = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+ln.Addr().String()+"/api/v1/adaptive-plan", strings.NewReader(strings.Repeat("a", 64*1024+1)))
	req.RequestURI = ""
	client := &http.Client{Timeout: time.Second}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "payload_too_large") {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(logs.String(), `"status":413`) {
		t.Fatal(logs.String())
	}
}

func TestSwaggerConcurrentSpecReads(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), nil, nil)
	_, _ = call(t, app, "GET", "/swagger/doc.json", "", nil)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { _, _ = call(t, app, "GET", "/swagger/doc.json", "", nil) })
	}
	wg.Wait()
}

func TestHTTPTransportTimeouts(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.ReadTimeout = 25 * time.Millisecond
	app := newTestApp(t, cfg, nil, nil)
	if app.Config().ReadTimeout != cfg.ReadTimeout || app.Config().WriteTimeout <= cfg.RequestTimeout || app.Config().IdleTimeout != time.Minute {
		t.Fatal("HTTP deadlines are not configured")
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * cfg.ReadTimeout)
	if _, err := io.WriteString(conn, "GET /health HTTP/1.1\r\nHost: localhost\r\n"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	defer func() {
		if err := app.ShutdownWithTimeout(time.Second); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(2 * cfg.ReadTimeout)
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("partial headers outlived server read deadline: %v", err)
	}
}

func TestUpstreamFailureLogsAreBounded(t *testing.T) {
	for _, longTopic := range []bool{false, true} {
		t.Run(fmt.Sprint(longTopic), func(t *testing.T) {
			var logs bytes.Buffer
			cfg := defaultConfig(t)
			topic := "Recursion"
			g := graph.NewInMemoryGraph()
			if longTopic {
				topic = strings.Repeat("sensitive-topic-", 100)
				var err error
				g, err = graph.Decode(strings.NewReader(fmt.Sprintf(`[{"subject":%q,"relation":"depends_on","object":"Functions"}]`, topic)))
				if err != nil {
					t.Fatal(err)
				}
			}
			app := New(cfg, Dependencies{Graph: g, Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Planner: fakePlanner(func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
				return nil, errors.New(strings.Repeat("private-prompt-student-42\n", 1000))
			})})
			body := fmt.Sprintf(`{"failed_topics":[%q],"vark_profile":"visual"}`, topic)
			r, _ := call(t, app, "POST", "/api/v1/adaptive-plan", body, map[string]string{"X-Request-ID": "bounded-42"})
			if r.StatusCode != http.StatusBadGateway {
				t.Fatal(r.StatusCode)
			}
			if strings.Contains(logs.String(), "private-prompt") || strings.Contains(logs.String(), "student-42") {
				t.Fatal("upstream content leaked to logs")
			}
			found := false
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var event map[string]any
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				if event["msg"] != "request.failed" {
					continue
				}
				found = true
				if len(line) > 1024 || event["request_id"] != "bounded-42" || event["error_class"] != "bad_gateway" || event["error"] != nil {
					t.Fatal(event)
				}
				if got, ok := event["topic"].(string); !ok || len([]rune(got)) > 128 || !strings.HasPrefix(topic, got) {
					t.Fatal(event)
				}
			}
			if !found {
				t.Fatal("missing failure summary")
			}
		})
	}
}

func TestHTTPHallucinationWarningHasRequestID(t *testing.T) {
	var logs bytes.Buffer
	app := newTestApp(t, defaultConfig(t), func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: "invented"}, {Topic: "Recursion"}}, nil
	}, &logs)
	r, _ := call(t, app, "POST", "/api/v1/adaptive-plan", `{"failed_topics":["Recursion"],"vark_profile":"visual"}`, map[string]string{"X-Request-ID": "drop-42"})
	if r.StatusCode != http.StatusOK {
		t.Fatal(r.StatusCode)
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == "stage.validation" {
			if event["request_id"] != "drop-42" || event["items_dropped"] != float64(1) {
				t.Fatal(event)
			}
			return
		}
	}
	t.Fatal("missing request-correlated validation warning")
}

func TestPublicRoutesDoNotShareAPIRateBudget(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.RateLimitMax = 1
	app := newTestApp(t, cfg, nil, nil)
	public := []string{"/health", "/livez", "/readyz", "/swagger/index.html", "/swagger/doc.json", "/swagger/swagger-ui.css", "/swagger/swagger-ui-bundle.js"}
	for range 2 {
		for _, path := range public {
			r, b := call(t, app, "GET", path, "", nil)
			if r.StatusCode != http.StatusOK {
				t.Fatalf("public route %s limited: %d %s", path, r.StatusCode, b)
			}
		}
	}
	for i, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		r, b := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, nil)
		if r.StatusCode != want {
			t.Fatalf("API request %d: %d %s", i, r.StatusCode, b)
		}
	}
	for _, path := range public {
		r, b := call(t, app, "GET", path, "", nil)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("API saturation limited %s: %d %s", path, r.StatusCode, b)
		}
	}
}

func TestBearerSchemeAndTokenLengths(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.AuthDisabled = false
	cfg.InternalToken = "test-secret"
	app := newTestApp(t, cfg, nil, nil)
	for _, scheme := range []string{"Bearer", "bearer", "bEaReR"} {
		r, b := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"Authorization": scheme + " test-secret"})
		if r.StatusCode != http.StatusOK {
			t.Fatalf("scheme %s: %d %s", scheme, r.StatusCode, b)
		}
	}
	for _, headers := range []map[string]string{{"Authorization": "test-secret"}, {"X-Internal-Token": "t"}, {"X-Internal-Token": "test-secret-extra"}, {"X-Internal-Token": "wrong-token", "Authorization": "Bearer test-secret"}} {
		r, b := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, headers)
		if r.StatusCode != http.StatusUnauthorized || b != `{"error":"unauthorized","details":"missing or invalid internal token"}` {
			t.Fatalf("%d %s", r.StatusCode, b)
		}
	}
}

func TestAuthDisabledLogsWarning(t *testing.T) {
	for _, token := range []string{"", "configured-secret"} {
		var logs bytes.Buffer
		cfg := defaultConfig(t)
		cfg.AuthDisabled = token == ""
		cfg.InternalToken = token
		_ = newTestApp(t, cfg, nil, &logs)
		if token != "" {
			if strings.Contains(logs.String(), "auth.disabled") {
				t.Fatal(logs.String())
			}
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] != "auth.disabled" || event["level"] != "WARN" {
			t.Fatal(event)
		}
	}
}

func TestCognitiveValidationNamesTopicsField(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.MaxFailedTopics = 1
	app := newTestApp(t, cfg, nil, nil)
	for _, query := range []string{"", "?topics=,", "?topics=Recursion,Polymorphism", "?topics=unknown"} {
		r, body := call(t, app, "GET", "/api/v1/tutor/student/student-42/cognitive-graph"+query, "", nil)
		if r.StatusCode != http.StatusBadRequest && r.StatusCode != http.StatusUnprocessableEntity {
			t.Fatal(r.StatusCode)
		}
		var envelope domain.ErrorResponse
		if err := json.Unmarshal([]byte(body), &envelope); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(envelope.Details, "topics") || strings.Contains(envelope.Details, "failed_topics") {
			t.Fatal(envelope.Details)
		}
	}
}

func TestAdaptivePlanRequiresJSONMediaType(t *testing.T) {
	for _, tc := range []struct {
		name, mediaType, body string
		want                  int
	}{
		{"xml", "application/xml", `<AdaptivePlanRequest><FailedTopics>Recursion</FailedTopics><VARKProfile>visual</VARKProfile></AdaptivePlanRequest>`, 415},
		{"text xml", "text/xml", `<request/>`, 415},
		{"form", "application/x-www-form-urlencoded", `FailedTopics=Recursion&VARKProfile=visual`, 415},
		{"multipart", "multipart/form-data; boundary=example", "--example--\r\n", 415},
		{"text", "text/plain", validBody, 415},
		{"missing", "", validBody, 415},
		{"malformed", "application/json; charset", validBody, 415},
		{"json charset", "application/json; charset=utf-8", validBody, 200},
		{"uppercase charset", "Application/JSON; charset=UTF-8", validBody, 200},
		{"unsupported charset", "application/json; charset=utf-16", validBody, 415},
		{"empty charset", `application/json; charset=""`, validBody, 415},
		{"json", "application/json", validBody, 200},
		{"telemetry", "application/json", `{"failed_topics":["Recursion"],"vark_profile":"visual","telemetry":{"session_id":"x","timestamp_unix":123,"duration_ms":1.5,"client_version":"1"}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t, defaultConfig(t), nil, nil)
			r, b := call(t, app, "POST", "/api/v1/adaptive-plan", tc.body, map[string]string{"Content-Type": tc.mediaType})
			if r.StatusCode != tc.want || (tc.want == 415 && !strings.Contains(b, `"error":"unsupported_media_type"`)) {
				t.Fatalf("got %d: %s, want %d", r.StatusCode, b, tc.want)
			}
		})
	}
}

func TestAdaptivePlanCanonicalTopics(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), nil, nil)
	r, b := call(t, app, "POST", "/api/v1/adaptive-plan", `{"failed_topics":["recursion"],"vark_profile":"visual"}`, nil)
	var plan domain.PlanResponse
	if err := json.Unmarshal([]byte(b), &plan); err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusOK || len(plan.Items) != 1 || plan.Items[0].Topic != "Recursion" {
		t.Fatalf("%d: %s", r.StatusCode, b)
	}
}

func TestQueueSaturationReturnsRetryAfter(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.LLMMaxConcurrency = 1
	cfg.LLMQueueTimeout = 20 * time.Millisecond
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	app := newTestApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}, nil)
	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/adaptive-plan", strings.NewReader(`{"failed_topics":["Recursion"],"vark_profile":"visual"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		status := 0
		if err == nil {
			status = resp.StatusCode
			err = resp.Body.Close()
		}
		done <- result{status: status, err: err}
	}()
	<-entered
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", `{"failed_topics":["Recursion"],"vark_profile":"visual"}`, nil)
	close(release)
	first := <-done
	if first.err != nil {
		t.Fatal(first.err)
	}
	if first.status != http.StatusOK {
		t.Errorf("admitted request status=%d", first.status)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" || !strings.Contains(body, `"error":"service_unavailable"`) {
		t.Fatalf("status=%d retry-after=%q body=%s", resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}
}

func TestRequestTimeoutHoldsWithBlockingPlanner(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.RequestTimeout = 60 * time.Millisecond
	cfg.LLMTimeout = 50 * time.Millisecond
	release := make(chan struct{})
	finished := make(chan struct{})
	app := newTestApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		<-release
		close(finished)
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	}, nil)
	timer := time.AfterFunc(400*time.Millisecond, func() { close(release) })
	start := time.Now()
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", `{"failed_topics":["Recursion"],"vark_profile":"visual"}`, nil)
	elapsed := time.Since(start)
	if timer.Stop() {
		close(release)
	}
	<-finished
	if resp.StatusCode != http.StatusGatewayTimeout || elapsed > cfg.RequestTimeout+250*time.Millisecond {
		t.Fatalf("status=%d elapsed=%v body=%s", resp.StatusCode, elapsed, body)
	}
}

func TestMissingTopicItemReturnsBadGateway(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) { return nil, nil }, nil)
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", `{"failed_topics":["Recursion"],"vark_profile":"visual"}`, nil)
	var got domain.ErrorResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway || got.Error != "bad_gateway" || got.Details != "model returned no usable item" {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestModelOutputIsSanitized(t *testing.T) {
	var logs bytes.Buffer
	app := newTestApp(t, defaultConfig(t), func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic, Explanation: "\u202e" + strings.Repeat("é", 1100) + "\n", Resources: []domain.Resource{
			{Title: "unsafe", URL: "javascript:alert(1)", ResourceType: "video"},
			{Title: strings.Repeat("x", 100000), URL: "data:text/html,evil", ResourceType: "VISUAL"},
			{URL: "/etc/passwd"},
			{URL: "ftp://example.com/x", ResourceType: "lab"},
			{URL: "https://user@example.com", ResourceType: "video"},
			{Title: "\u202e" + strings.Repeat("界", 250) + "\x00", URL: "https://example.com/video", ResourceType: "Video"},
			{Title: "diagram", URL: "http://example.com/diagram", ResourceType: "diagram"},
			{Title: "infographic", URL: "https://example.com/infographic", ResourceType: "infographic"},
			{Title: "extra", URL: "https://example.com/extra", ResourceType: "video"},
			{Title: "wrong type", URL: "https://example.com/audio", ResourceType: "podcast"},
		}}}, nil
	}, &logs)
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", `{"failed_topics":["Recursion"],"vark_profile":"visual"}`, nil)
	var got domain.PlanResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(got.Items) != 1 {
		t.Fatalf("status=%d items=%d", resp.StatusCode, len(got.Items))
	}
	item := got.Items[0]
	if item.Explanation != strings.Repeat("é", 1000) || len(item.Resources) != 3 {
		t.Fatalf("explanation runes=%d resources=%d", len([]rune(item.Explanation)), len(item.Resources))
	}
	for i, kind := range []string{"video", "diagram", "infographic"} {
		if item.Resources[i].ResourceType != kind {
			t.Fatalf("resources=%+v", item.Resources)
		}
	}
	if item.Resources[0].Title != strings.Repeat("界", 200) {
		t.Fatalf("title runes=%d", len([]rune(item.Resources[0].Title)))
	}
	if !strings.Contains(logs.String(), `"msg":"stage.output"`) || !strings.Contains(logs.String(), `"resources_dropped":7`) || !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Fatal(logs.String())
	}
}
