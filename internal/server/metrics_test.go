package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func metricsConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(func(key string) string {
		if key == "AXIOM_AUTH_DISABLED" || key == "METRICS_ENABLED" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMetricsDisabledByDefault(t *testing.T) {
	app := newTestApp(t, defaultConfig(t), nil, nil)
	resp, body := call(t, app, "GET", "/metrics", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestMetricsAfterPlan(t *testing.T) {
	cfg := metricsConfig(t)
	cfg.AuthDisabled = false
	cfg.InternalToken = strings.Repeat("x", 32)
	cfg.RateLimitMax = 1
	app := newTestApp(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: req.Topic, Resources: []domain.Resource{{URL: "javascript:alert(1)", ResourceType: "video"}}}, {Topic: "Hash Tables"}}, nil
	}, nil)
	resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"X-Internal-Token": cfg.InternalToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", resp.StatusCode, body)
	}
	for range 2 {
		resp, body = call(t, app, "GET", "/metrics", "", nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/plain") {
			t.Fatalf("scrape status=%d content-type=%q body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
		for _, sample := range []string{
			`axiom_llm_calls_total{outcome="ok"} 2`,
			`http_server_request_duration_seconds_count{route="/api/v1/adaptive-plan"} 1`,
			`http_server_requests_total{method="POST",route="/api/v1/adaptive-plan",status="200"} 1`,
			"axiom_llm_inflight 0",
			"axiom_circuit_breaker_state 0",
			`axiom_plan_items_dropped_total{reason="validation"} 1`,
			`axiom_plan_items_dropped_total{reason="duplicate"} 1`,
			"axiom_plan_resources_dropped_total 2",
			"axiom_llm_queue_wait_seconds_count 2",
			`axiom_llm_duration_seconds_count{outcome="ok"} 2`,
		} {
			if !strings.Contains(body, sample+"\n") {
				t.Errorf("missing sample %s", sample)
			}
		}
	}
}

func TestHTTPMetricsCaptureRejectedRequests(t *testing.T) {
	cfg := metricsConfig(t)
	cfg.AuthDisabled = false
	cfg.InternalToken = strings.Repeat("x", 32)
	cfg.RateLimitMax = 2
	app := newTestApp(t, cfg, nil, nil)
	for i, status := range []int{http.StatusUnauthorized, http.StatusOK, http.StatusTooManyRequests} {
		var headers map[string]string
		if i > 0 {
			headers = map[string]string{"X-Internal-Token": cfg.InternalToken}
		}
		resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, headers)
		if resp.StatusCode != status {
			t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, status, body)
		}
	}
	resp, body := call(t, app, "GET", "/metrics", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape status=%d body=%s", resp.StatusCode, body)
	}
	for _, status := range []string{"401", "200", "429"} {
		sample := `http_server_requests_total{method="POST",route="/api/v1/adaptive-plan",status="` + status + `"} 1` + "\n"
		if !strings.Contains(body, sample) {
			t.Errorf("missing sample %s", sample)
		}
	}
}

func TestRouteLabelIsTemplate(t *testing.T) {
	app := newTestApp(t, metricsConfig(t), nil, nil)
	for _, id := range []string{"private-first", "private-second"} {
		resp, body := call(t, app, "GET", "/api/v1/tutor/student/"+id+"/cognitive-graph?topics=Recursion", "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	}
	unknown := "/" + strings.Repeat("z", 10*1024)
	var req fasthttp.Request
	req.Header.SetMethod(http.MethodGet)
	req.SetRequestURI(unknown)
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	app.Handler()(&ctx)
	if ctx.Response.StatusCode() != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	resp, body := call(t, app, "GET", "/metrics", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `http_server_request_duration_seconds_count{route="/api/v1/tutor/student/:student_id/cognitive-graph"} 2`) {
		t.Error("missing registered route template")
	}
	for _, raw := range []string{"private-first", "private-second", strings.Repeat("z", 100), `route="/"`, `route=""`} {
		if strings.Contains(body, raw) {
			t.Errorf("scrape contains unbounded or unmatched route %q", raw)
		}
	}
}

func TestMetricsIsolatedPerApplication(t *testing.T) {
	first := newTestApp(t, metricsConfig(t), nil, nil)
	second := newTestApp(t, metricsConfig(t), nil, nil)
	call(t, first, "POST", "/api/v1/adaptive-plan", validBody, nil)
	resp, body := call(t, second, "GET", "/metrics", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `axiom_llm_calls_total{outcome="ok"} 0`+"\n") {
		t.Fatalf("independent scrape status=%d body=%s", resp.StatusCode, body)
	}
}

func TestMetricsClassifiesLLMOutcomes(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		err     error
	}{
		{"throttled", service.ErrThrottled},
		{"unavailable", service.ErrUpstreamUnavailable},
		{"rejected", service.ErrUpstreamRejected},
		{"invalid_output", service.ErrInvalidOutput},
		{"deadline", context.DeadlineExceeded},
		{"canceled", context.Canceled},
		{"error", errors.New("backend failure")},
		{"invalid_output", nil},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			app := newTestApp(t, metricsConfig(t), func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
				return nil, tc.err
			}, nil)
			call(t, app, "POST", "/api/v1/adaptive-plan", `{"failed_topics":["Recursion"],"vark_profile":"visual"}`, nil)
			resp, body := call(t, app, "GET", "/metrics", "", nil)
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, `axiom_llm_calls_total{outcome="`+tc.outcome+`"} 1`+"\n") {
				t.Fatalf("outcome %s: status=%d body=%s", tc.outcome, resp.StatusCode, body)
			}
		})
	}
}

func TestMetricsTracksOpenCircuit(t *testing.T) {
	app := newTestApp(t, metricsConfig(t), func(context.Context, service.TopicRequest) ([]domain.PlanItem, error) {
		return nil, service.ErrUpstreamUnavailable
	}, nil)
	for range 4 {
		call(t, app, "POST", "/api/v1/adaptive-plan", `{"failed_topics":["Recursion"],"vark_profile":"visual"}`, nil)
	}
	resp, body := call(t, app, "GET", "/metrics", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "axiom_circuit_breaker_state 2\n") || !strings.Contains(body, `axiom_llm_calls_total{outcome="unavailable"} 3`+"\n") {
		t.Fatalf("circuit scrape status=%d body=%s", resp.StatusCode, body)
	}
}

func TestMetricsRetainsInflightUntilPlannerReturns(t *testing.T) {
	cfg := metricsConfig(t)
	cfg.LLMMaxConcurrency = 1
	cfg.LLMQueueTimeout = 5 * time.Millisecond
	cfg.LLMTimeout = 20 * time.Millisecond
	cfg.RequestTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	defer finish()
	app := newTestApp(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		<-release
		return []domain.PlanItem{{Topic: req.Topic}}, nil
	}, nil)
	body := `{"failed_topics":["Recursion"],"vark_profile":"visual"}`
	resp, got := call(t, app, "POST", "/api/v1/adaptive-plan", body, nil)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("deadline status=%d body=%s", resp.StatusCode, got)
	}
	_, got = call(t, app, "GET", "/metrics", "", nil)
	if !strings.Contains(got, "axiom_llm_inflight 1\n") || !strings.Contains(got, `axiom_llm_calls_total{outcome="deadline"} 1`+"\n") {
		t.Fatalf("deadline scrape=%s", got)
	}
	resp, got = call(t, app, "POST", "/api/v1/adaptive-plan", body, nil)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("queue status=%d body=%s", resp.StatusCode, got)
	}
	_, got = call(t, app, "GET", "/metrics", "", nil)
	if !strings.Contains(got, "axiom_llm_queue_timeouts_total 1\n") || !strings.Contains(got, "axiom_llm_queue_wait_seconds_count 2\n") || !strings.Contains(got, "axiom_llm_inflight 1\n") {
		t.Fatalf("queue scrape=%s", got)
	}
	finish()
	deadline := time.Now().Add(time.Second)
	for {
		_, got = call(t, app, "GET", "/metrics", "", nil)
		if strings.Contains(got, "axiom_llm_inflight 0\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inflight remained occupied after planner returned")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestUnmatchedRequestsHaveBoundedMetrics(t *testing.T) {
	app := newTestApp(t, metricsConfig(t), nil, nil)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/missing-first", http.StatusNotFound},
		{"GET", "/missing-second", http.StatusNotFound},
		{"DELETE", "/api/v1/adaptive-plan", http.StatusMethodNotAllowed},
	} {
		resp, body := call(t, app, tc.method, tc.path, "", nil)
		if resp.StatusCode != tc.status {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	}
	_, body := call(t, app, "GET", "/metrics", "", nil)
	for _, sample := range []string{
		`http_server_requests_total{method="unmatched",route="unmatched",status="404"} 2`,
		`http_server_requests_total{method="unmatched",route="unmatched",status="405"} 1`,
	} {
		if !strings.Contains(body, sample+"\n") {
			t.Errorf("missing sample %s", sample)
		}
	}
	if strings.Contains(body, "missing-first") || strings.Contains(body, "missing-second") {
		t.Error("raw paths reached the metric labels")
	}
}
