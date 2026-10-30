package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestShutdownAnswersInFlightPlanBeforeDeadline(t *testing.T) {
	type planResponse struct {
		status int
		header http.Header
		body   domain.ErrorResponse
		err    error
	}
	for _, tc := range []struct {
		name   string
		delay  time.Duration
		status int
	}{
		{"cancels slow work", 5 * time.Second, http.StatusServiceUnavailable},
		{"finishes short work", 50 * time.Millisecond, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfig(t)
			cfg.ShutdownTimeout = 2 * time.Second
			started := make(chan struct{})
			app := newTestApp(t, cfg, func(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
				close(started)
				timer := time.NewTimer(tc.delay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return []domain.PlanItem{{Topic: r.Topic}}, nil
				}
			}, nil)
			ready := make(chan string, 1)
			app.Hooks().OnListen(func(data fiber.ListenData) error { ready <- data.Host + ":" + data.Port; return nil })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, app, "127.0.0.1:0", cfg.ShutdownTimeout) }()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+<-ready+"/api/v1/adaptive-plan", strings.NewReader(`{"failed_topics":["Recursion"],"vark_profile":"visual"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			result := make(chan planResponse, 1)
			go func() {
				resp, err := (&http.Client{Timeout: 7 * time.Second}).Do(req)
				if err != nil {
					result <- planResponse{err: err}
					return
				}
				defer func() { _ = resp.Body.Close() }()
				var body domain.ErrorResponse
				err = json.NewDecoder(resp.Body).Decode(&body)
				result <- planResponse{status: resp.StatusCode, header: resp.Header, body: body, err: err}
			}()
			<-started
			shutdownStart := time.Now()
			cancel()
			got := <-result
			runErr := <-done
			if got.err != nil {
				t.Fatalf("request error=%v, Run error=%v", got.err, runErr)
			}
			if got.status != tc.status || runErr != nil || time.Since(shutdownStart) >= cfg.ShutdownTimeout {
				t.Fatalf("status=%d, Run error=%v, shutdown elapsed=%s", got.status, runErr, time.Since(shutdownStart))
			}
			if tc.status == http.StatusServiceUnavailable {
				if got.body.Error != "service_unavailable" || got.body.Details != "server shutting down" || got.header.Get("Retry-After") != "1" || got.header.Get("X-Request-ID") == "" {
					t.Fatalf("headers=%v, body=%+v", got.header, got.body)
				}
			}
		})
	}
}

func TestRunListenFailure(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Run(ctx, newTestApp(t, defaultConfig(t), nil, nil), ln.Addr().String(), time.Second); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("listen failure did not propagate: %v", err)
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, newTestApp(t, defaultConfig(t), nil, nil), "127.0.0.1:0", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	app := newTestApp(t, defaultConfig(t), nil, nil)
	ready := make(chan string, 1)
	app.Hooks().OnListen(func(data fiber.ListenData) error { ready <- data.Host + ":" + data.Port; return nil })
	done := make(chan error, 1)
	go func() { done <- Run(ctx, app, "127.0.0.1:0", time.Second) }()
	address := <-ready
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHealthcheck(t *testing.T) {
	for _, status := range []int{200, 503, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if status == 302 {
					w.Header().Set("Location", "/health")
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()
			u, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			err = Healthcheck(context.Background(), u.Port())
			if (err == nil) != (status == 200) {
				t.Fatalf("status=%d error=%v", status, err)
			}
		})
	}
	if err := Healthcheck(context.Background(), "not a port"); err == nil {
		t.Fatal("invalid URL accepted")
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strings.Split(ln.Addr().String(), ":")[1]
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Healthcheck(context.Background(), port); err == nil {
		t.Fatal("unreachable server accepted")
	}
}

func TestReadinessFlipsWhileDraining(t *testing.T) {
	cfg, err := config.Load(func(k string) string {
		switch k {
		case "AXIOM_AUTH_DISABLED":
			return "true"
		case "SHUTDOWN_DRAIN_DELAY":
			return "1s"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	app := newTestApp(t, cfg, nil, nil)
	ready := make(chan string, 1)
	app.Hooks().OnListen(func(data fiber.ListenData) error { ready <- data.Host + ":" + data.Port; return nil })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, app, "127.0.0.1:0", cfg.ShutdownTimeout) }()
	address := <-ready
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	probe := func(path string) (int, string, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+path, nil)
		if err != nil {
			return 0, "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		var body domain.HealthResponse
		err = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body.Status, err
	}
	if status, body, err := probe("/readyz"); err != nil || status != http.StatusOK || body != "ok" {
		t.Errorf("initial readiness: status=%d body=%q error=%v", status, body, err)
	}
	shutdownStart := time.Now()
	cancel()
	for {
		status, body, err := probe("/readyz")
		if err != nil || time.Since(shutdownStart) >= time.Second {
			t.Errorf("readiness did not report draining before listener closure: status=%d body=%q error=%v", status, body, err)
			break
		}
		if status == http.StatusServiceUnavailable {
			if body != "draining" {
				t.Errorf("draining readiness body=%q", body)
			}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, path := range []string{"/livez", "/health", "/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion"} {
		if status, _, err := probe(path); err != nil || status != http.StatusOK {
			t.Errorf("draining %s: status=%d error=%v", path, status, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(shutdownStart); elapsed < time.Second || elapsed >= 3*time.Second {
		t.Fatalf("shutdown elapsed=%s, want a one-second drain before closure", elapsed)
	}
}

func TestHealthcheckProbesLivez(t *testing.T) {
	var logs bytes.Buffer
	cfg := defaultConfig(t)
	app := newTestApp(t, cfg, nil, &logs)
	ready := make(chan string, 1)
	app.Hooks().OnListen(func(data fiber.ListenData) error { ready <- data.Host + ":" + data.Port; return nil })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, app, "127.0.0.1:0", cfg.ShutdownTimeout) }()
	_, port, err := net.SplitHostPort(<-ready)
	if err != nil {
		t.Fatal(err)
	}
	probeErr := Healthcheck(t.Context(), port)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == "http.access" {
			if event["path"] != "/livez" || event["status"] != float64(http.StatusOK) {
				t.Fatalf("healthcheck request=%v", event)
			}
			return
		}
	}
	t.Fatal("missing healthcheck access record")
}
