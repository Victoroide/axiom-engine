//go:build integration

package contract_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Victoroide/axiom-engine/internal/auth"
	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/service"
)

const tenantTopics = "/api/v1/topics"

func TestTenantQuota(t *testing.T) {
	f := newTenantFixture(t)
	t.Run("concurrent callers and boundary", f.testConcurrentAndBoundary)
	t.Run("IP admission and public routes", f.testIPAdmission)
	t.Run("one Redis call per decision", f.testSingleRedisDecision)
	t.Run("replay and streams", f.testReplayAndStreams)
	t.Run("policy restart", f.testPolicyRestart)
	t.Run("expiry and storage recovery", f.testExpiryAndRecovery)
}

func (f tenantFixture) testConcurrentAndBoundary(t *testing.T) {
	const tenant = "private-hot-tenant"
	key := f.tenantKey(t, tenant)
	env := map[string]string{"AXIOM_TENANT_QUOTA_MAX": "1", "AXIOM_TENANT_QUOTA_OVERRIDES": `{"private-hot-tenant":13}`}
	replicas := [...]string{f.serve(t, env, nil), f.serve(t, env, nil)}
	callers := [...]string{
		f.token(t, tenant, "private-first-caller", "graph:read"),
		f.token(t, tenant, "private-second-caller", "graph:read"),
		f.token(t, tenant, "private-third-caller", "graph:read"),
	}
	// Seed an empty window with Redis time. Assert the same expiry afterward
	// so the concurrency assertion covers exactly one window.
	expires := f.seed(t, key, 0, 60)
	results := make(chan int, 100)
	start := make(chan struct{})
	for i := range 100 {
		go func() {
			<-start
			res, err := f.request(t.Context(), replicas[i%len(replicas)], http.MethodGet, tenantTopics, callers[i%len(callers)], f.ip(i), "", "")
			if err != nil {
				t.Error(err)
			}
			results <- res.StatusCode
		}()
	}
	close(start)
	var admitted, rejected int
	for range 100 {
		switch <-results {
		case http.StatusOK:
			admitted++
		case http.StatusTooManyRequests:
			rejected++
		}
	}
	if admitted != 13 || rejected != 87 {
		t.Fatalf("100 concurrent actions admitted=%d rejected=%d, want 13 and 87", admitted, rejected)
	}
	if fields := f.bucket(t, key); fields["current"] != "13" || fields["previous"] != "0" || fields["expires"] != expires {
		t.Fatalf("unexpected bucket after 100 concurrent actions: %v", fields)
	}
	ttl := f.client.PTTL(t.Context(), key).Val()
	for i := 100; i < 105; i++ {
		res := f.check(t, replicas[i%2], http.MethodGet, tenantTopics, callers[i%3], f.ip(i), "", "", http.StatusTooManyRequests)
		checkRetryAfter(t, res, 60)
	}
	if fields := f.bucket(t, key); fields["current"] != "13" || f.client.PTTL(t.Context(), key).Val() > ttl {
		t.Fatalf("denials prolonged the hot tenant's budget: %v", fields)
	}
	independent := f.token(t, "ordinary-tenant", "private-first-caller", "graph:read")
	f.check(t, replicas[1], http.MethodGet, tenantTopics, independent, f.ip(0), "", "", http.StatusOK)
	f.check(t, replicas[0], http.MethodGet, tenantTopics, independent, f.ip(105), "", "", http.StatusTooManyRequests)

	// First retain enough previous-window weight to deny, then advance far
	// enough to allow a request. Neither step waits on the wall-clock boundary.
	f.seed(t, key, 13, -1)
	checkRetryAfter(t, f.check(t, replicas[0], http.MethodGet, tenantTopics, callers[0], f.ip(106), "", "", http.StatusTooManyRequests), 60)
	f.seed(t, key, 13, -30)
	f.check(t, replicas[1], http.MethodGet, tenantTopics, callers[1], f.ip(107), "", "", http.StatusOK)
	if fields := f.bucket(t, key); fields["current"] != "1" || fields["previous"] != "13" {
		t.Fatalf("weighted window did not advance: %v", fields)
	}
	t.Logf("100 concurrent actions in one Redis window: %d admitted, %d rejected across two listeners", admitted, rejected)
}

func (f tenantFixture) testIPAdmission(t *testing.T) {
	const tenant = "ip-admission-tenant"
	endpoint := f.serve(t, map[string]string{"RATE_LIMIT_MAX": "1", "AXIOM_TENANT_QUOTA_MAX": "1"}, nil)
	token := f.token(t, tenant, "first-caller", "graph:read")
	other := f.token(t, "ip-admission-other", "second-caller", "graph:read")
	noScope := f.token(t, tenant, "first-caller", "plan:generate")
	ip1, ip2, ip3 := f.ip(110), f.ip(111), f.ip(112)
	for _, path := range []string{"/health", "/livez", "/readyz", "/swagger/doc.json", "/metrics"} {
		res := f.check(t, endpoint, http.MethodGet, path, "", ip1, "", "", http.StatusOK)
		if res.Header.Get("X-RateLimit-Limit") != "" || res.Header.Get("Retry-After") != "" {
			t.Fatalf("public route %s returned admission headers", path)
		}
	}
	f.check(t, endpoint, http.MethodGet, tenantTopics, "", ip1, "", "", http.StatusUnauthorized)
	f.check(t, endpoint, http.MethodGet, tenantTopics, noScope, ip1, "", "", http.StatusForbidden)
	f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip1, "", "", http.StatusOK)
	ipDenied := f.check(t, endpoint, http.MethodGet, tenantTopics, other, ip1, "", "", http.StatusTooManyRequests)
	if ipDenied.Header.Get("X-RateLimit-Limit") != "" {
		t.Fatalf("expected IP rejection, headers=%v", ipDenied.Header)
	}
	f.check(t, endpoint, http.MethodGet, tenantTopics, other, ip2, "", "", http.StatusOK)
	tenantDenied := f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip3, "", "", http.StatusTooManyRequests)
	if tenantDenied.Header.Get("X-RateLimit-Limit") != "1" || tenantDenied.Header.Get("X-RateLimit-Remaining") != "0" ||
		!strings.Contains(string(tenantDenied.Body), `"error":"rate_limit_exceeded"`) {
		t.Fatalf("tenant denial headers=%v body=%s", tenantDenied.Header, tenantDenied.Body)
	}
	checkRetryAfter(t, tenantDenied, 60)
	_, router := loadContract(t)
	validateResponse(t, router, request(http.MethodGet, tenantTopics, ""), tenantDenied)
	ipDenied = f.check(t, endpoint, http.MethodGet, tenantTopics, other, ip3, "", "", http.StatusTooManyRequests)
	if ipDenied.Header.Get("X-RateLimit-Limit") != "" {
		t.Fatalf("tenant rejection did not retain its IP charge: headers=%v", ipDenied.Header)
	}
	metrics := f.check(t, endpoint, http.MethodGet, "/metrics", "", ip1, "", "", http.StatusOK)
	for _, private := range []string{tenant, "first-caller", f.issuer.URL, token} {
		if strings.Contains(string(metrics.Body), private) {
			t.Fatal("metrics contain verified identity or credentials")
		}
	}
}

func (f tenantFixture) testReplayAndStreams(t *testing.T) {
	env := map[string]string{"AXIOM_TENANT_QUOTA_MAX": "4"}
	var calls atomic.Int32
	planner := func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return successfulPlan(ctx, req)
	}
	first, second := f.serve(t, env, planner), f.serve(t, env, planner)
	token := f.token(t, "replay-tenant", "replay-caller", "graph:read plan:generate")
	id := uuid.NewString()
	original := f.check(t, first, http.MethodPost, planPath, token, f.ip(120), validBody, id, http.StatusOK)
	replayed := f.check(t, second, http.MethodPost, planPath, token, f.ip(121), validBody, id, http.StatusOK)
	if replayed.Header.Get("Idempotent-Replayed") != "true" || string(replayed.Body) != string(original.Body) || calls.Load() != 1 {
		t.Fatalf("replay did not serve the original response once, planner calls=%d", calls.Load())
	}
	multiTopic := strings.Replace(validBody, `["Recursion"]`, `["Recursion","Polymorphism"]`, 1)
	stream := f.check(t, first, http.MethodPost, planPath+"/stream", token, f.ip(122), multiTopic, "", http.StatusOK)
	if !strings.Contains(string(stream.Body), "event: plan") {
		t.Fatalf("stream did not return a plan: %s", stream.Body)
	}
	f.check(t, second, http.MethodPost, planPath, token, f.ip(123), "}", "", http.StatusBadRequest)
	f.check(t, first, http.MethodGet, tenantTopics, token, f.ip(124), "", "", http.StatusTooManyRequests)
	f.check(t, second, http.MethodPost, planPath, token, f.ip(125), validBody, id, http.StatusTooManyRequests)
	if calls.Load() != 3 {
		t.Fatalf("planner calls=%d, want one JSON topic and two SSE topics", calls.Load())
	}
}

func (f tenantFixture) testSingleRedisDecision(t *testing.T) {
	endpoint := f.serve(t, nil, nil)
	token := f.token(t, "roundtrip-tenant", "roundtrip-caller", "graph:read")
	_ = f.tenantKey(t, "roundtrip-tenant")
	ip := f.ip(127)
	f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip, "", "", http.StatusOK)
	before := f.evalSHACalls(t)
	f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip, "", "", http.StatusOK)
	if calls := f.evalSHACalls(t) - before; calls != 1 {
		t.Fatalf("a warm protected success made %d Redis script calls, want 1", calls)
	}
}

func (f tenantFixture) testPolicyRestart(t *testing.T) {
	const tenant = "policy-restart-tenant"
	_ = f.tenantKey(t, tenant)
	token := f.token(t, tenant, "restart-caller", "graph:read")
	ip := f.ip(128)

	// Each subtest stops its listener before the next start. The issuer,
	// caller and Redis database are the same for every start.
	t.Run("original policy", func(t *testing.T) {
		endpoint := f.serve(t, map[string]string{"AXIOM_TENANT_QUOTA_WINDOW": "1h", "AXIOM_TENANT_QUOTA_MAX": "1"}, nil)
		f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip, "", "", http.StatusOK)
	})
	t.Run("maximum change retains usage", func(t *testing.T) {
		endpoint := f.serve(t, map[string]string{"AXIOM_TENANT_QUOTA_WINDOW": "1h", "AXIOM_TENANT_QUOTA_MAX": "2"}, nil)
		f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip, "", "", http.StatusOK)
		f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip, "", "", http.StatusTooManyRequests)
	})
	t.Run("duration change starts a fresh window", func(t *testing.T) {
		endpoint := f.serve(t, map[string]string{"AXIOM_TENANT_QUOTA_WINDOW": "1m", "AXIOM_TENANT_QUOTA_MAX": "2"}, nil)
		f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip, "", "", http.StatusOK)
		f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip, "", "", http.StatusOK)
		denied := f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip, "", "", http.StatusTooManyRequests)
		checkRetryAfter(t, denied, 60)
	})
}

func (f tenantFixture) testExpiryAndRecovery(t *testing.T) {
	const tenant = "private-expiry-tenant"
	env := map[string]string{"AXIOM_TENANT_QUOTA_MAX": "1", "AXIOM_TENANT_QUOTA_WINDOW": "1s"}
	first, second := f.serve(t, env, nil), f.serve(t, env, nil)
	token := f.token(t, tenant, "private-expiry-caller", "graph:read")
	key := f.tenantKey(t, tenant)
	f.check(t, first, http.MethodGet, tenantTopics, token, f.ip(130), "", "", http.StatusOK)
	if ttl := f.client.PTTL(t.Context(), key).Val(); ttl <= 0 || ttl > 2*time.Second {
		t.Fatalf("tenant TTL=%v, want within two windows", ttl)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		exists, err := f.client.Exists(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			break
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("tenant bucket did not expire")
		}
	}
	f.check(t, second, http.MethodGet, tenantTopics, token, f.ip(131), "", "", http.StatusOK)

	// Make only the tenant operation fail after successful IP admission.
	if err := f.client.Set(t.Context(), key, "storage-fault", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	res := f.check(t, first, http.MethodGet, tenantTopics, token, f.ip(132), "", "", http.StatusServiceUnavailable)
	checkRetryAfter(t, res, 1)
	if res.Header.Get("X-RateLimit-Limit") == "" || !strings.Contains(string(res.Body), `"error":"service_unavailable"`) ||
		strings.Contains(string(res.Body), tenant) || strings.Contains(string(res.Body), "storage-fault") {
		t.Fatalf("storage failure headers=%v body=%s", res.Header, res.Body)
	}
	_, router := loadContract(t)
	validateResponse(t, router, request(http.MethodGet, tenantTopics, ""), res)
	f.check(t, first, http.MethodGet, "/readyz", "", f.ip(132), "", "", http.StatusOK)
	if err := f.client.Del(t.Context(), key).Err(); err != nil {
		t.Fatal(err)
	}
	f.check(t, second, http.MethodGet, tenantTopics, token, f.ip(133), "", "", http.StatusOK)

	// Pause a disposable Redis longer than the runtime operation deadline.
	if err := f.client.ClientPause(t.Context(), 1500*time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	unavailable := f.check(t, first, http.MethodGet, tenantTopics, token, f.ip(134), "", "", http.StatusServiceUnavailable)
	checkRetryAfter(t, unavailable, 1)
	if unavailable.Header.Get("X-RateLimit-Limit") != "" {
		t.Fatal("store outage admitted the IP")
	}
	if err := f.client.Ping(t.Context()).Err(); err != nil {
		t.Fatal("disposable Redis did not resume")
	}
	recovered := f.token(t, "recovered-tenant", "private-expiry-caller", "graph:read")
	f.check(t, first, http.MethodGet, tenantTopics, recovered, f.ip(134), "", "", http.StatusOK)
}

type tenantFixture struct {
	client  *redis.Client
	url     string
	issuer  *httptest.Server
	signer  jose.Signer
	http    *http.Client
	ipBlock string
}

func newTenantFixture(t testing.TB) tenantFixture {
	t.Helper()
	redisURL := os.Getenv("AXIOM_TEST_REDIS_URL")
	if redisURL == "" {
		t.Fatal("set AXIOM_TEST_REDIS_URL to a disposable real Redis instance")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal("invalid AXIOM_TEST_REDIS_URL")
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal("cannot reach the disposable Redis instance")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "quota-test", Algorithm: string(jose.ES256), Use: "sig"}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jwk}, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
	if err != nil {
		t.Fatal(err)
	}
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(keys) }))
	t.Cleanup(issuer.Close)
	httpClient := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 100}}
	t.Cleanup(httpClient.CloseIdleConnections)
	id := uuid.New()
	return tenantFixture{
		client: client, url: redisURL, issuer: issuer, signer: signer, http: httpClient,
		ipBlock: fmt.Sprintf("fd00:%x:%x", id[:2], id[2:4]),
	}
}

func (f tenantFixture) serve(t testing.TB, settings map[string]string, planner plannerFunc) string {
	t.Helper()
	env := map[string]string{
		"AXIOM_AUTH_MODE": "verified", "AXIOM_AUTH_ISSUER": f.issuer.URL,
		"AXIOM_AUTH_AUDIENCE": "quota-test", "AXIOM_AUTH_JWKS_URL": f.issuer.URL + "/keys",
		"AXIOM_AUTH_ALGORITHMS": "ES256", "AXIOM_TENANT_QUOTA_ENABLED": "true", "REDIS_URL": f.url,
		"TRUSTED_PROXIES": "127.0.0.1", "METRICS_ENABLED": "true",
	}
	for k, v := range settings {
		env[k] = v
	}
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.VerifiedAuth, err = auth.New(auth.Config{
		Issuer: f.issuer.URL, JWKSURL: f.issuer.URL + "/keys", Audience: "quota-test", Algorithms: []string{"ES256"},
		Claims: auth.ClaimMapping{Tenant: "tenant_id"}, Transport: f.issuer.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if planner == nil {
		planner = successfulPlan
	}
	app, err := server.NewWithError(cfg, server.Dependencies{
		Graph: graph.NewInMemoryGraph(), Planner: planner, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() {
		f.http.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := app.ShutdownWithContext(ctx); err != nil {
			t.Error(err)
		}
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("HTTP listener failed: %v", err)
			}
		case <-ctx.Done():
			t.Error("HTTP listener did not shut down")
		}
	})
	return "http://" + listener.Addr().String()
}

func (f tenantFixture) token(t testing.TB, tenant, caller, scopes string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"iss": f.issuer.URL, "aud": "quota-test", "exp": time.Now().Add(5 * time.Minute).Unix(),
		"tenant_id": tenant, "sub": caller, "scope": scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := f.signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (f tenantFixture) request(ctx context.Context, endpoint, method, path, token, ip, body, replay string) (response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, strings.NewReader(body))
	if err != nil {
		return response{}, err
	}
	req.Header.Set("X-Forwarded-For", ip)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if replay != "" {
		req.Header.Set("Idempotency-Key", replay)
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return response{StatusCode: resp.StatusCode, Header: resp.Header, Body: data}, err
}

func (f tenantFixture) check(t testing.TB, endpoint, method, path, token, ip, body, replay string, status int) response {
	t.Helper()
	res, err := f.request(t.Context(), endpoint, method, path, token, ip, body, replay)
	if err != nil || res.StatusCode != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s err=%v", method, path, res.StatusCode, status, res.Body, err)
	}
	return res
}

func (f tenantFixture) ip(i int) string { return fmt.Sprintf("%s:%x::1", f.ipBlock, i+1) }

func (f tenantFixture) tenantKey(t testing.TB, tenant string) string {
	t.Helper()
	var framed []byte
	for _, value := range []string{f.issuer.URL, tenant} {
		framed = binary.BigEndian.AppendUint64(framed, uint64(len(value)))
		framed = append(framed, value...)
	}
	key := fmt.Sprintf("axiom:tenant-rate:v1:%x", sha256.Sum256(framed))
	t.Cleanup(func() { _ = f.client.Del(context.WithoutCancel(t.Context()), key).Err() })
	return key
}

func (f tenantFixture) seed(t *testing.T, key string, count, offset int) string {
	t.Helper()
	const script = `
local expires = tonumber(redis.call('TIME')[1]) + tonumber(ARGV[2])
redis.call('HSET', KEYS[1], 'current', ARGV[1], 'previous', 0, 'expires', expires, 'window', 60)
redis.call('EXPIRE', KEYS[1], 120)
return expires
`
	expires, err := f.client.Eval(t.Context(), script, []string{key}, count, offset).Int64()
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(expires, 10)
}

func (f tenantFixture) bucket(t *testing.T, key string) map[string]string {
	t.Helper()
	fields, err := f.client.HGetAll(t.Context(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

func (f tenantFixture) evalSHACalls(t *testing.T) int {
	t.Helper()
	info, err := f.client.Info(t.Context(), "commandstats").Result()
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(info, "\r\n") {
		if stats, found := strings.CutPrefix(line, "cmdstat_evalsha:calls="); found {
			count, _, _ := strings.Cut(stats, ",")
			n, err := strconv.Atoi(count)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatal("Redis did not report any EVALSHA calls")
	return 0
}

func checkRetryAfter(t *testing.T, res response, maxSeconds int) {
	t.Helper()
	retry, err := strconv.Atoi(res.Header.Get("Retry-After"))
	if err != nil || retry < 1 || retry > maxSeconds {
		t.Fatalf("Retry-After=%q, want between 1 and %d seconds", res.Header.Get("Retry-After"), maxSeconds)
	}
}
