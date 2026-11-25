package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestTenantQuotaVerifiedAdmission(t *testing.T) {
	redis := miniredis.RunT(t)
	redis.SetTime(time.Unix(1000, 0))
	cfg, fixture := newVerifiedFixture(t, false)
	cfg = loadTestQuota(t, cfg, "redis://"+redis.Addr())
	cfg.TrustedProxies = []string{"0.0.0.0/0", "::/0"}
	cfg.CORSAllowedOrigins = []string{"https://client.example"}
	first, second := newTestApp(t, cfg, nil, nil), newTestApp(t, cfg, nil, nil)
	t.Cleanup(func() { _ = first.Shutdown(); _ = second.Shutdown() })
	const topics = "/api/v1/topics"
	tenant := "private-tenant"
	graph := fixture.token(t, fixture.signer, tenant, "first-caller", "graph:read")
	graph2 := fixture.token(t, fixture.signer, tenant, "second-caller", "graph:read")
	wrong := fixture.token(t, fixture.signer, tenant, "first-caller", "plan:generate")
	other := fixture.token(t, fixture.signer, "another-tenant", "first-caller", "graph:read")
	third := fixture.token(t, fixture.signer, "third-tenant", "first-caller", "graph:read")
	send := func(app *fiber.App, method, path, credential, ip string, status int) response {
		t.Helper()
		res, body := call(t, app, method, path, "", map[string]string{"Authorization": "Bearer " + credential, "X-Forwarded-For": ip})
		if res.StatusCode != status {
			t.Fatalf("%s %s status=%d, want %d: %s", method, path, res.StatusCode, status, body)
		}
		return res
	}
	const firstIP, secondIP = "192.0.2.1", "192.0.2.2"
	send(first, http.MethodGet, "/livez", "", firstIP, http.StatusOK)
	send(first, http.MethodGet, "/readyz", "", firstIP, http.StatusOK)
	send(first, http.MethodGet, "/swagger/doc.json", "", firstIP, http.StatusOK)
	preflight, _ := call(t, first, http.MethodOptions, topics, "", map[string]string{
		"Origin": "https://client.example", "Access-Control-Request-Method": "GET",
	})
	if preflight.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight=%d", preflight.StatusCode)
	}
	send(first, http.MethodGet, topics, "", firstIP, http.StatusUnauthorized)
	send(first, http.MethodGet, topics, wrong, firstIP, http.StatusForbidden)
	if keys := redis.Keys(); len(keys) != 0 {
		t.Fatalf("public or unauthorized requests wrote %d keys", len(keys))
	}
	send(first, http.MethodGet, topics, graph, firstIP, http.StatusOK)
	// IP denial for an independent tenant leaves its tenant budget untouched.
	ipLimited := send(second, http.MethodGet, topics, other, firstIP, http.StatusTooManyRequests)
	if ipLimited.Header.Get("X-RateLimit-Limit") != "" || ipLimited.Header.Get("Retry-After") != "60" {
		t.Fatalf("IP rejection headers=%v", ipLimited.Header)
	}
	limited := send(second, http.MethodHead, topics, graph2, secondIP, http.StatusTooManyRequests)
	if limited.Header.Get("X-RateLimit-Limit") != "1" ||
		limited.Header.Get("X-RateLimit-Remaining") != "0" ||
		limited.Header.Get("X-RateLimit-Reset") != "60" ||
		limited.Header.Get("Retry-After") != "60" {
		t.Fatalf("tenant rejection headers=%v", limited.Header)
	}
	// The rejected tenant retained its admitted IP unit.
	send(first, http.MethodGet, topics, third, secondIP, http.StatusTooManyRequests)
	send(second, http.MethodGet, topics, other, "192.0.2.3", http.StatusOK)
	// Ties do not increment the tenant count or extend its expiry.
	redis.SetTime(time.Unix(1060, 0))
	send(first, http.MethodGet, topics, graph, "192.0.2.4", http.StatusTooManyRequests)
	redis.SetTime(time.Unix(1120, 0))
	send(first, http.MethodGet, topics, graph, "192.0.2.5", http.StatusOK)
	var tenantKeys int
	for _, key := range redis.Keys() {
		if strings.HasPrefix(key, "axiom:tenant-rate:v1:") {
			tenantKeys++
			if strings.Contains(key, tenant) || strings.Contains(key, fixture.issuer) || strings.Contains(key, "caller") {
				t.Error("tenant budget key contains verified identity")
			}
		}
	}
	if tenantKeys != 2 {
		t.Fatalf("tenant bucket count=%d, want 2", tenantKeys)
	}
}

func TestTenantQuotaDefaultKeepsLegacyResponses(t *testing.T) {
	// These requests pin the existing shared-token gate and Redis IP accounting
	// without configuring any tenant setting.
	redis := miniredis.RunT(t)
	redis.SetTime(time.Unix(1000, 0))
	token := strings.Repeat("a", 32)
	env := map[string]string{"AXIOM_INTERNAL_TOKEN": token, "REDIS_URL": "redis://" + redis.Addr(), "RATE_LIMIT_MAX": "1"}
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	first, second := newTestApp(t, cfg, nil, nil), newTestApp(t, cfg, nil, nil)
	t.Cleanup(func() { _ = first.Shutdown(); _ = second.Shutdown() })
	const path = "/api/v1/topics"
	res, body := call(t, first, http.MethodGet, path, "", nil)
	if res.StatusCode != http.StatusUnauthorized || body != `{"error":"unauthorized","details":"missing or invalid internal token"}` ||
		res.Header.Get("Retry-After") != "" {
		t.Fatalf("legacy authentication status=%d headers=%v body=%s", res.StatusCode, res.Header, body)
	}
	headers := map[string]string{"X-Internal-Token": token}
	res, _ = call(t, first, http.MethodGet, path, "", headers)
	if res.StatusCode != http.StatusOK || res.Header.Get("X-RateLimit-Limit") != "1" ||
		res.Header.Get("X-RateLimit-Remaining") != "0" || res.Header.Get("X-RateLimit-Reset") != "60" {
		t.Fatalf("legacy admission status=%d headers=%v", res.StatusCode, res.Header)
	}
	res, body = call(t, second, http.MethodGet, path, "", headers)
	if res.StatusCode != http.StatusTooManyRequests || body != `{"error":"rate_limit_exceeded","details":"too many requests"}` ||
		res.Header.Get("Retry-After") != "60" || res.Header.Get("X-RateLimit-Limit") != "" {
		t.Fatalf("legacy limit status=%d headers=%v body=%s", res.StatusCode, res.Header, body)
	}
	if keys := redis.Keys(); len(keys) != 1 || !strings.HasPrefix(keys[0], "axiom:rate:") || redis.HGet(keys[0], "current") != "2" {
		t.Fatalf("legacy IP bucket accounting keys=%v", keys)
	}
}

func TestTenantQuotaAdmissionAcrossTransports(t *testing.T) {
	redis := miniredis.RunT(t)
	redis.SetTime(time.Unix(1000, 0))
	cfg, fixture := newVerifiedFixture(t, false)
	cfg = loadTestQuota(t, cfg, "redis://"+redis.Addr())
	admission, err := NewRedisAdmission(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admission.Close() })
	app := newTestApp(t, cfg, nil, nil)
	t.Cleanup(func() { _ = app.Shutdown() })
	token := fixture.token(t, fixture.signer, "tools-tenant", "tools-caller", "graph:read plan:generate")
	principal, err := cfg.VerifiedAuth.Verify(t.Context(), token, "plan:generate")
	if err != nil {
		t.Fatal(err)
	}
	decision := admission.Admit(t.Context(), "203.0.113.10", principal)
	if decision.Status != http.StatusOK || decision.IPLimit != 1 || decision.IPRemaining != 0 {
		t.Fatalf("first action got %+v", decision)
	}
	res, body := call(t, app, http.MethodGet, "/api/v1/topics", "", map[string]string{"Authorization": "Bearer " + token})
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("X-RateLimit-Limit") != "1" {
		t.Fatalf("shared tenant decision status=%d headers=%v body=%s", res.StatusCode, res.Header, body)
	}
}

func TestTenantQuotaIsolatesIssuers(t *testing.T) {
	redis := miniredis.RunT(t)
	redis.SetTime(time.Unix(1000, 0))
	redisURL := "redis://" + redis.Addr()
	for range 2 {
		cfg, fixture := newVerifiedFixture(t, false)
		cfg = loadTestQuota(t, cfg, redisURL)
		cfg.RateLimitMax = 10
		app := newTestApp(t, cfg, nil, nil)
		t.Cleanup(func() { _ = app.Shutdown() })
		token := fixture.token(t, fixture.signer, "same-tenant", "same-caller", "graph:read")
		res, body := call(t, app, http.MethodGet, "/api/v1/topics", "", map[string]string{"Authorization": "Bearer " + token})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("issuer's first action status=%d body=%s", res.StatusCode, body)
		}
	}
}

func TestTenantQuotaNoRefundAfterPlannerDispatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{{name: "provider failure"}, {name: "cancellation", cancel: true}} {
		t.Run(tc.name, func(t *testing.T) {
			redis := miniredis.RunT(t)
			redis.SetTime(time.Unix(1000, 0))
			cfg, fixture := newVerifiedFixture(t, false)
			cfg = loadTestQuota(t, cfg, "redis://"+redis.Addr())
			cfg.HTTPListener, cfg.RateLimitMax = config.HTTPListenerNetHTTP, 10
			started, plannerDone := make(chan struct{}, 1), make(chan error, 1)
			address, _, _ := startWireApp(t, cfg, func(ctx context.Context, _ service.TopicRequest) ([]domain.PlanItem, error) {
				started <- struct{}{}
				if tc.cancel {
					<-ctx.Done()
					plannerDone <- ctx.Err()
					return nil, ctx.Err()
				}
				return nil, service.ErrUpstreamRejected
			}, nil)
			token := fixture.token(t, fixture.signer, "tenant", "caller", "graph:read plan:generate")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+string(address)+wirePlan, strings.NewReader(wireTopicBody))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			requestDone := make(chan error, 1)
			go func() {
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					defer func() { _ = resp.Body.Close() }()
					_, err = io.Copy(io.Discard, resp.Body)
					if err == nil && resp.StatusCode != http.StatusBadGateway {
						err = errors.New("expected provider failure response")
					}
				}
				requestDone <- err
			}()
			receiveWire(t, started, "planner dispatch")
			if tc.cancel {
				cancel()
				if err := receiveWire(t, plannerDone, "planner cancellation"); !errors.Is(err, context.Canceled) {
					t.Fatalf("planner context error=%v, want cancellation", err)
				}
			}
			err = receiveWire(t, requestDone, "plan response")
			if (tc.cancel && !errors.Is(err, context.Canceled)) || (!tc.cancel && err != nil) {
				t.Fatalf("plan request error=%v", err)
			}
			res, data := address.dial(t).exchange(t, http.MethodGet, "/api/v1/topics", "", http.Header{"Authorization": {"Bearer " + token}})
			if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("X-RateLimit-Limit") != "10" {
				t.Fatalf("expected tenant exhaustion after %s: status=%d headers=%v body=%s", tc.name, res.StatusCode, res.Header, data)
			}
		})
	}
}

func loadTestQuota(t *testing.T, cfg config.Config, url string) config.Config {
	t.Helper()
	env := map[string]string{
		"AXIOM_AUTH_MODE": "verified", "AXIOM_AUTH_ISSUER": "https://issuer.example",
		"AXIOM_AUTH_AUDIENCE": "https://api.example", "AXIOM_AUTH_JWKS_URL": "https://issuer.example/keys",
		"REDIS_URL": url, "RATE_LIMIT_MAX": "1",
		"AXIOM_TENANT_QUOTA_ENABLED": "true", "AXIOM_TENANT_QUOTA_MAX": "1",
	}
	loaded, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	loaded.VerifiedAuth = cfg.VerifiedAuth
	return loaded
}
