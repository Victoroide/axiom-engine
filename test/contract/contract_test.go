package contract_test

import (
	"bufio"
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
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/service"
)

const (
	planPath  = "/api/v1/adaptive-plan"
	graphPath = "/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion"
	validBody = `{"student_id":"student-42","course_id":"cs101","failed_topics":["Recursion"],"vark_profile":"visual"}`
)

type plannerFunc func(context.Context, service.TopicRequest) ([]domain.PlanItem, error)

func (f plannerFunc) GenerateTopic(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
	return f(ctx, req)
}

func successfulPlan(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
	return []domain.PlanItem{{Topic: req.Topic, Explanation: "Practice the topic", PrerequisiteChain: req.PrerequisiteOrder, Resources: []domain.Resource{{Title: "Practice", URL: "https://example.com/practice", ResourceType: "diagram"}}}}, nil
}

func newApp(t *testing.T, configure func(*config.Config), planner plannerFunc) *fiber.App {
	t.Helper()
	cfg, err := config.Load(func(key string) string {
		if key == "AXIOM_INTERNAL_TOKEN" {
			return "contract-internal-token-0123456789"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if configure != nil {
		configure(&cfg)
	}
	if planner == nil {
		planner = successfulPlan
	}
	return server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Planner: planner, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
}

func request(method, path, body string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("X-Internal-Token", "contract-internal-token-0123456789")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "request-example")
	return req
}

type response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func call(t *testing.T, app *fiber.App, req *http.Request, status int, code string) response {
	t.Helper()
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return checkResponse(t, resp, status, code)
}

func checkResponse(t *testing.T, resp *http.Response, status int, code string) response {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, status, body)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID")
	}
	if code != "" {
		var envelope domain.ErrorResponse
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error != code {
			t.Errorf("error=%q want=%q body=%s", envelope.Error, code, body)
		}
	}
	return response{StatusCode: resp.StatusCode, Header: resp.Header, Body: body}
}

func loadContract(t *testing.T) (*openapi3.T, routers.Router) {
	t.Helper()
	data, err := os.ReadFile("../../docs/swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var swagger openapi2.T
	if err := json.Unmarshal(data, &swagger); err != nil {
		t.Fatal(err)
	}
	doc, err := openapi2conv.ToV3(&swagger)
	if err != nil {
		t.Fatal(err)
	}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return doc, router
}

func checkHeaders(header http.Header, response *openapi3.Response) error {
	var missing []error
	for name := range header {
		if !strings.EqualFold(name, "X-Request-ID") && !strings.EqualFold(name, "Retry-After") && !strings.HasPrefix(strings.ToLower(name), "x-ratelimit-") {
			continue
		}
		declared := false
		if response != nil {
			for documented := range response.Headers {
				if strings.EqualFold(name, documented) {
					declared = true
					break
				}
			}
		}
		if !declared {
			missing = append(missing, fmt.Errorf("undeclared response header %s", name))
		}
	}
	return errors.Join(missing...)
}

func validateResponse(t *testing.T, router routers.Router, req *http.Request, resp response) {
	t.Helper()
	route, params, err := router.FindRoute(req)
	if err != nil {
		t.Fatal(err)
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(resp.Body)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		t.Errorf("status=%d: %v", resp.StatusCode, err)
	}
	var declared *openapi3.Response
	if ref := route.Operation.Responses.Status(resp.StatusCode); ref != nil {
		declared = ref.Value
	}
	if err := checkHeaders(resp.Header, declared); err != nil {
		t.Errorf("status=%d: %v", resp.StatusCode, err)
	}
}

func TestSwaggerResponses(t *testing.T) {
	_, router := loadContract(t)
	for _, tc := range []struct {
		name      string
		method    string
		path      string
		body      string
		status    int
		code      string
		configure func(*config.Config)
		planner   plannerFunc
		prepare   func(*http.Request)
		prime     bool
	}{
		{name: "plan success", method: http.MethodPost, path: planPath, body: validBody, status: 200},
		{name: "plan invalid body", method: http.MethodPost, path: planPath, body: `{`, status: 400, code: "invalid_request_body"},
		{name: "plan invalid fields", method: http.MethodPost, path: planPath, body: `{"failed_topics":[],"vark_profile":"visual"}`, status: 400, code: "validation_error"},
		{name: "plan unauthorized", method: http.MethodPost, path: planPath, body: validBody, status: 401, code: "unauthorized", prepare: func(r *http.Request) { r.Header.Del("X-Internal-Token") }},
		{name: "plan unsupported media", method: http.MethodPost, path: planPath, body: validBody, status: 415, code: "unsupported_media_type", prepare: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{name: "plan unknown topic", method: http.MethodPost, path: planPath, body: `{"failed_topics":["Unknown"],"vark_profile":"visual"}`, status: 422, code: "validation_error"},
		{name: "plan rate limited", method: http.MethodPost, path: planPath, body: validBody, status: 429, code: "rate_limit_exceeded", configure: func(c *config.Config) { c.RateLimitMax = 1 }, prime: true},
		{name: "plan unusable output", method: http.MethodPost, path: planPath, body: validBody, status: 502, code: "bad_gateway", planner: func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) { return nil, nil }},
		{name: "plan throttled", method: http.MethodPost, path: planPath, body: validBody, status: 503, code: "service_unavailable", planner: func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
			return nil, service.ErrThrottled
		}},
		{name: "plan deadline", method: http.MethodPost, path: planPath, body: validBody, status: 504, code: "gateway_timeout", configure: func(c *config.Config) { c.LLMTimeout = 10 * time.Millisecond }, planner: func(ctx context.Context, _ service.TopicRequest) ([]domain.PlanItem, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
		{name: "graph success", method: http.MethodGet, path: graphPath, status: 200},
		{name: "graph invalid student", method: http.MethodGet, path: "/api/v1/tutor/student/bad!/cognitive-graph?topics=Recursion", status: 400, code: "validation_error"},
		{name: "graph invalid target", method: http.MethodGet, path: graphPath + "&target_topic=Polymorphism", status: 400, code: "validation_error"},
		{name: "graph unauthorized", method: http.MethodGet, path: graphPath, status: 401, code: "unauthorized", prepare: func(r *http.Request) { r.Header.Del("X-Internal-Token") }},
		{name: "graph unknown topic", method: http.MethodGet, path: graphPath + ",Unknown", status: 422, code: "validation_error"},
		{name: "graph rate limited", method: http.MethodGet, path: graphPath, status: 429, code: "rate_limit_exceeded", configure: func(c *config.Config) { c.RateLimitMax = 1 }, prime: true},
		{name: "health", method: http.MethodGet, path: "/health", status: 200},
		{name: "liveness", method: http.MethodGet, path: "/livez", status: 200},
		{name: "readiness", method: http.MethodGet, path: "/readyz", status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newApp(t, tc.configure, tc.planner)
			if tc.prime {
				call(t, app, request(tc.method, tc.path, tc.body), 200, "")
			}
			req := request(tc.method, tc.path, tc.body)
			if tc.prepare != nil {
				tc.prepare(req)
			}
			validateResponse(t, router, req, call(t, app, req, tc.status, tc.code))
		})
	}
}

func oversizedHeader(req *http.Request) {
	req.Header.Set("X-Large", strings.Repeat("x", 8192))
}

func TestSwaggerOpenCircuit(t *testing.T) {
	_, router := loadContract(t)
	var calls atomic.Int32
	app := newApp(t, nil, func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return nil, service.ErrThrottled
	})
	for range 3 {
		req := request(http.MethodPost, planPath, validBody)
		validateResponse(t, router, req, call(t, app, req, 503, "service_unavailable"))
	}
	req := request(http.MethodPost, planPath, validBody)
	resp := call(t, app, req, 503, "service_unavailable")
	if resp.Header.Get("Retry-After") != "15" || calls.Load() != 3 {
		t.Errorf("circuit retry-after=%q planner calls=%d", resp.Header.Get("Retry-After"), calls.Load())
	}
	validateResponse(t, router, req, resp)
}

func TestSwaggerQueueSaturation(t *testing.T) {
	_, router := loadContract(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	app := newApp(t, func(c *config.Config) {
		c.LLMMaxConcurrency = 1
		c.LLMQueueTimeout = 10 * time.Millisecond
	}, func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return successfulPlan(ctx, req)
	})
	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := app.Test(request(http.MethodPost, planPath, validBody))
		status := 0
		if err == nil {
			status = resp.StatusCode
			err = resp.Body.Close()
		}
		done <- result{status, err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("first request did not reach planner")
	}
	defer func() {
		close(release)
		first := <-done
		if first.err != nil {
			t.Error(first.err)
			return
		}
		if first.status != http.StatusOK {
			t.Errorf("admitted request status=%d", first.status)
		}
	}()
	req := request(http.MethodPost, planPath, validBody)
	resp := call(t, app, req, 503, "service_unavailable")
	if resp.Header.Get("Retry-After") != "1" {
		t.Errorf("queue retry-after=%q", resp.Header.Get("Retry-After"))
	}
	validateResponse(t, router, req, resp)
}

func TestSwaggerPathErrors(t *testing.T) {
	doc, _ := loadContract(t)
	for _, tc := range []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodGet, "/missing", 404, "not_found"},
		{http.MethodGet, planPath, 405, "method_not_allowed"},
		{"PROPFIND", "/health", 501, "not_implemented"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			resp := call(t, newApp(t, nil, nil), request(tc.method, tc.path, ""), tc.status, tc.code)
			var body any
			if err := json.Unmarshal(resp.Body, &body); err != nil {
				t.Fatal(err)
			}
			if err := doc.Components.Schemas["domain.ErrorResponse"].Value.VisitJSON(body, openapi3.VisitAsResponse()); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestSwaggerParserErrors(t *testing.T) {
	_, router := loadContract(t)
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		code                     string
		rawHeaders               string
		readTimeout              time.Duration
	}{
		{name: "plan body", method: http.MethodPost, path: planPath, body: strings.Repeat("x", 65*1024), status: 413, code: "payload_too_large"},
		{name: "graph body", method: http.MethodGet, path: graphPath, body: strings.Repeat("x", 65*1024), status: 413, code: "payload_too_large"},
		{name: "health body", method: http.MethodGet, path: "/health", body: strings.Repeat("x", 65*1024), status: 413, code: "payload_too_large"},
		{name: "liveness body", method: http.MethodGet, path: "/livez", body: strings.Repeat("x", 65*1024), status: 413, code: "payload_too_large"},
		{name: "readiness body", method: http.MethodGet, path: "/readyz", body: strings.Repeat("x", 65*1024), status: 413, code: "payload_too_large"},
		{name: "plan headers", method: http.MethodPost, path: planPath, body: validBody, status: 431, code: "request_header_fields_too_large"},
		{name: "graph headers", method: http.MethodGet, path: graphPath, status: 431, code: "request_header_fields_too_large"},
		{name: "health headers", method: http.MethodGet, path: "/health", status: 431, code: "request_header_fields_too_large"},
		{name: "liveness headers", method: http.MethodGet, path: "/livez", status: 431, code: "request_header_fields_too_large"},
		{name: "readiness headers", method: http.MethodGet, path: "/readyz", status: 431, code: "request_header_fields_too_large"},
		{name: "plan timeout", method: http.MethodPost, path: planPath, status: 408, code: "request_timeout", rawHeaders: "Content-Length: 1\r\n\r\n", readTimeout: 200 * time.Millisecond},
		{name: "graph timeout", method: http.MethodGet, path: graphPath, status: 408, code: "request_timeout", rawHeaders: "Content-Length: 1\r\n\r\n", readTimeout: 200 * time.Millisecond},
		{name: "health timeout", method: http.MethodGet, path: "/health", status: 408, code: "request_timeout", rawHeaders: "Content-Length: 1\r\n\r\n", readTimeout: 200 * time.Millisecond},
		{name: "liveness timeout", method: http.MethodGet, path: "/livez", status: 408, code: "request_timeout", rawHeaders: "Content-Length: 1\r\n\r\n", readTimeout: 200 * time.Millisecond},
		{name: "readiness timeout", method: http.MethodGet, path: "/readyz", status: 408, code: "request_timeout", rawHeaders: "Content-Length: 1\r\n\r\n", readTimeout: 200 * time.Millisecond},
		{name: "health malformed", method: http.MethodGet, path: "/health", status: 400, code: "bad_request", rawHeaders: "Content-Length: invalid\r\n\r\n"},
		{name: "liveness malformed", method: http.MethodGet, path: "/livez", status: 400, code: "bad_request", rawHeaders: "Content-Length: invalid\r\n\r\n"},
		{name: "readiness malformed", method: http.MethodGet, path: "/readyz", status: 400, code: "bad_request", rawHeaders: "Content-Length: invalid\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newApp(t, func(c *config.Config) {
				if tc.readTimeout != 0 {
					c.ReadTimeout = tc.readTimeout
				}
			}, nil)
			listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()
			defer func() {
				if err := app.ShutdownWithTimeout(time.Second); err != nil {
					t.Error(err)
				}
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			if tc.rawHeaders != "" {
				conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "tcp", listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close() }()
				if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
					t.Fatal(err)
				}
				// Timeout cases advertise a body without sending it.
				if _, err := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nX-Internal-Token: contract-internal-token-0123456789\r\n%s", tc.method, tc.path, listener.Addr(), tc.rawHeaders); err != nil {
					t.Fatal(err)
				}
				resp, err := http.ReadResponse(bufio.NewReader(conn), request(tc.method, tc.path, ""))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				validateResponse(t, router, request(tc.method, tc.path, ""), checkResponse(t, resp, tc.status, tc.code))
				return
			}
			req := request(tc.method, "http://"+listener.Addr().String()+tc.path, tc.body)
			req.RequestURI = ""
			if tc.status == 431 {
				oversizedHeader(req)
			}
			client := &http.Client{Timeout: time.Second}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			validateResponse(t, router, request(tc.method, tc.path, tc.body), checkResponse(t, resp, tc.status, tc.code))
		})
	}
}

func TestSwaggerRecoveredPanic(t *testing.T) {
	_, router := loadContract(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, planPath},
		{http.MethodGet, graphPath},
	} {
		t.Run(tc.method, func(t *testing.T) {
			app := newApp(t, nil, nil)
			// Exercise the shared recovery middleware without panicking in a child goroutine.
			app.Add([]string{tc.method}, "/api/v1/panic", func(fiber.Ctx) error { panic("private failure") })
			resp := call(t, app, request(tc.method, "/api/v1/panic", ""), 500, "internal_error")
			validateResponse(t, router, request(tc.method, tc.path, validBody), resp)
		})
	}
}
