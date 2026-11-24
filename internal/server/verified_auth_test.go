package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/Victoroide/axiom-engine/internal/auth"
	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

const verifiedAudience = "https://api.example/axiom"

type verifiedFixture struct {
	issuer string
	signer jose.Signer
	next   jose.Signer
	keys   *atomic.Value
	latest []byte
}

func newVerifiedFixture(t *testing.T, rotate bool) (config.Config, verifiedFixture) {
	t.Helper()
	key, signer := verifiedSigningKey(t, "first-key")
	keys := new(atomic.Value)
	keys.Store(verifiedJWKS(t, key))
	endpoint := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/keys" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected key request")
			http.Error(w, "unexpected key request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(keys.Load().([]byte))
	}))
	endpoint.Config.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError)
	endpoint.StartTLS()
	t.Cleanup(endpoint.Close)
	fixture := verifiedFixture{issuer: endpoint.URL + "/issuer", signer: signer, keys: keys}
	if rotate {
		nextKey, next := verifiedSigningKey(t, "next-key")
		fixture.next = next
		fixture.latest = verifiedJWKS(t, nextKey)
	}
	verifier, err := auth.New(auth.Config{
		Issuer: fixture.issuer, Audience: verifiedAudience, JWKSURL: endpoint.URL + "/keys",
		Algorithms: []string{"RS256"}, Claims: auth.ClaimMapping{Tenant: "tenant_id"},
		Transport: endpoint.Client().Transport, RefreshInterval: time.Second, RefreshTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig(t)
	cfg.AuthDisabled = false
	cfg.VerifiedAuth = verifier
	return cfg, fixture
}

func verifiedSigningKey(t *testing.T, keyID string) (jose.JSONWebKey, jose.Signer) {
	t.Helper()
	return verifiedSigningKeyWith(t, keyID, jose.RS256)
}

func verifiedSigningKeyWith(t *testing.T, keyID string, algorithm jose.SignatureAlgorithm) (jose.JSONWebKey, jose.Signer) {
	t.Helper()
	var privateKey any
	var err error
	if algorithm == jose.ES256 {
		privateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	} else {
		privateKey, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		t.Fatal(err)
	}
	key := jose.JSONWebKey{Key: privateKey, KeyID: keyID, Algorithm: string(algorithm), Use: "sig"}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: algorithm, Key: key}, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		t.Fatal(err)
	}
	return key.Public(), signer
}

func verifiedJWKS(t *testing.T, keys ...jose.JSONWebKey) []byte {
	t.Helper()
	data, err := json.Marshal(jose.JSONWebKeySet{Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f verifiedFixture) token(t *testing.T, signer jose.Signer, tenant, caller, scope string) string {
	t.Helper()
	return signVerifiedClaims(t, signer, map[string]any{
		"iss": f.issuer, "aud": verifiedAudience, "exp": time.Now().Add(time.Minute).Unix(),
		"tenant_id": tenant, "sub": caller, "scope": scope,
	})
}

func signVerifiedClaims(t *testing.T, signer jose.Signer, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestWireVerifiedConfiguration(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	rsaKey, rsaSigner := verifiedSigningKey(t, "rsa-configured")
	ecKey, ecSigner := verifiedSigningKeyWith(t, "ec-configured", jose.ES256)
	jwks := verifiedJWKS(t, rsaKey, ecKey)
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwks)
	}))
	t.Cleanup(endpoint.Close)
	// The isolated test process trusts the TLS fixture through the same
	// default transport the configured verifier uses.
	oldTransport := http.DefaultTransport
	http.DefaultTransport = endpoint.Client().Transport
	defer func() { http.DefaultTransport = oldTransport }()
	issuer := endpoint.URL + "/issuer"
	defaults := map[string]string{
		"AXIOM_AUTH_MODE": "verified", "AXIOM_AUTH_ISSUER": issuer,
		"AXIOM_AUTH_AUDIENCE": verifiedAudience, "AXIOM_AUTH_JWKS_URL": endpoint.URL + "/keys",
	}
	custom := map[string]string{
		"AXIOM_AUTH_TENANT_CLAIM": "org", "AXIOM_AUTH_SCOPE_CLAIM": "permissions",
		"AXIOM_AUTH_CLIENT_ID_CLAIM": "client_id", "AXIOM_AUTH_REGISTERED_CLIENTS": "registered",
	}
	subjectClaims := map[string]any{"tenant_id": "tenant", "sub": "caller", "scope": "plan:generate"}
	clientClaims := map[string]any{"org": "tenant", "client_id": "registered", "permissions": "plan:generate"}
	for _, tc := range []struct {
		name   string
		signer jose.Signer
		env    map[string]string
		claims map[string]any
		status int
	}{
		{"default RS256", rsaSigner, nil, subjectClaims, http.StatusOK},
		{"ES256 requires opt-in", ecSigner, nil, subjectClaims, http.StatusUnauthorized},
		{"explicit ES256", ecSigner, map[string]string{"AXIOM_AUTH_ALGORITHMS": "ES256"}, subjectClaims, http.StatusOK},
		{"mapped client", rsaSigner, custom, clientClaims, http.StatusOK},
		{"matching subject", rsaSigner, custom, map[string]any{"org": "tenant", "client_id": "registered", "sub": "registered", "permissions": "plan:generate"}, http.StatusOK},
		{"different subject", rsaSigner, custom, map[string]any{"org": "tenant", "client_id": "registered", "sub": "different", "permissions": "plan:generate"}, http.StatusUnauthorized},
		{"unregistered client", rsaSigner, custom, map[string]any{"org": "tenant", "client_id": "unknown", "permissions": "plan:generate"}, http.StatusUnauthorized},
		{"mapped insufficient scope", rsaSigner, custom, map[string]any{"org": "tenant", "client_id": "registered", "permissions": "graph:read", "scope": "plan:generate"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := maps.Clone(defaults)
			maps.Copy(env, tc.env)
			cfg, err := config.Load(func(key string) string { return env[key] })
			if err != nil {
				t.Fatal(err)
			}
			claims := map[string]any{"iss": issuer, "aud": verifiedAudience, "exp": time.Now().Add(time.Minute).Unix()}
			maps.Copy(claims, tc.claims)
			token := signVerifiedClaims(t, tc.signer, claims)
			addr, _, _ := startWireApp(t, cfg, nil, nil)
			resp, body := addr.dial(t).exchange(t, http.MethodPost, wirePlan, wireTopicBody, http.Header{"Authorization": {"Bearer " + token}})
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, tc.status, body)
			}
		})
	}
}

func TestWireVerifiedAuthorization(t *testing.T) {
	if !runWireSubprocess(t) {
		return
	}
	for _, mode := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		t.Run(string(mode), func(t *testing.T) {
			cfg, fixture := newVerifiedFixture(t, false)
			cfg.HTTPListener = mode
			cfg.CORSAllowedOrigins = []string{"https://client.example"}
			cfg.MetricsEnabled = true
			cfg.RateLimitMax = 8
			var calls atomic.Int32
			addr, _, _ := startWireApp(t, cfg, func(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
				calls.Add(1)
				return wireItems(req), nil
			}, nil)
			conn := addr.dial(t)
			graph := "Bearer " + fixture.token(t, fixture.signer, "tenant", "caller", "graph:read")
			plan := "bEaReR " + fixture.token(t, fixture.signer, "tenant", "caller", "plan:generate")
			other := "Bearer " + fixture.token(t, fixture.signer, "tenant", "caller", "Graph:read graph:read:other plan:generate:other")
			identity := http.Header{"X-Tenant-Id": {"other-tenant"}, "X-Subject": {"other-caller"}, "X-Scope": {"graph:read plan:generate"}}
			for _, tc := range []struct {
				name   string
				header http.Header
			}{
				{name: "missing", header: identity},
				{name: "duplicate same credential", header: http.Header{"Authorization": {plan, plan}}},
				{name: "duplicate different credential", header: http.Header{"Authorization": {plan, graph}}},
				{name: "combined credentials", header: http.Header{"Authorization": {plan + ", " + graph}}},
				{name: "conflicting credentials", header: http.Header{"Authorization": {plan}, "X-Internal-Token": {"legacy"}}},
				{name: "empty conflicting credential", header: http.Header{"Authorization": {plan}, "X-Internal-Token": {""}}},
				{name: "shared credential alone", header: http.Header{"X-Internal-Token": {"legacy"}}},
				{name: "wrong scheme", header: http.Header{"Authorization": {"Basic " + strings.TrimPrefix(graph, "Bearer ")}}},
				{name: "invalid token", header: http.Header{"Authorization": {"Bearer invalid"}}},
			} {
				resp, body := conn.exchange(t, http.MethodPost, wireStream, wireTopicBody, tc.header)
				if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != "Bearer" ||
					!strings.Contains(body, `"error":"unauthorized"`) || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") ||
					resp.Header.Get("X-RateLimit-Limit") != "" {
					t.Errorf("%s: status=%d headers=%v body=%s", tc.name, resp.StatusCode, resp.Header, body)
				}
			}
			for _, path := range []string{"/health", "/livez", "/readyz", "/metrics", "/swagger/doc.json"} {
				resp, _ := conn.exchange(t, http.MethodGet, path, "", nil)
				if resp.StatusCode != http.StatusOK || resp.Header.Get("X-RateLimit-Limit") != "" {
					t.Errorf("public %s: status=%d headers=%v", path, resp.StatusCode, resp.Header)
				}
			}
			preflight, _ := conn.exchange(t, http.MethodOptions, wireStream, "", http.Header{
				"Origin": {"https://client.example"}, "Access-Control-Request-Method": {"POST"}, "Access-Control-Request-Headers": {"Authorization"},
			})
			if preflight.StatusCode != http.StatusNoContent || preflight.Header.Get("Access-Control-Allow-Origin") != "https://client.example" {
				t.Errorf("preflight status=%d headers=%v", preflight.StatusCode, preflight.Header)
			}
			for _, route := range []struct{ method, path, body, allowed, denied string }{
				{http.MethodGet, "/api/v1/topics", "", graph, plan},
				{http.MethodHead, "/api/v1/topics", "", graph, other},
				{http.MethodGet, "/api/v1/topics/Recursion/path", "", graph, plan},
				{http.MethodHead, "/api/v1/topics/Recursion/path", "", graph, other},
				{http.MethodGet, "/api/v1/tutor/student/learner/cognitive-graph?topics=Recursion", "", graph, plan},
				{http.MethodHead, "/api/v1/tutor/student/learner/cognitive-graph?topics=Recursion", "", graph, other},
				{http.MethodPost, wirePlan, wireTopicBody, plan, other},
				{http.MethodPost, wireStream, wireTopicBody, plan, graph},
			} {
				headers := identity.Clone()
				headers.Set("Authorization", route.denied)
				resp, body := conn.exchange(t, route.method, route.path, route.body, headers)
				scope := "graph:read"
				if route.method == http.MethodPost {
					scope = "plan:generate"
				}
				if resp.StatusCode != http.StatusForbidden || resp.Header.Get("X-RateLimit-Limit") != "" ||
					resp.Header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="`+scope+`"` ||
					!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") ||
					(route.method != http.MethodHead && !strings.Contains(body, `"error":"insufficient_scope"`)) {
					t.Errorf("%s %s denied: status=%d headers=%v body=%s", route.method, route.path, resp.StatusCode, resp.Header, body)
				}
				headers.Set("Authorization", route.allowed)
				resp, body = conn.exchange(t, route.method, route.path, route.body, headers)
				if resp.StatusCode != http.StatusOK || (route.method == http.MethodHead && body != "") {
					t.Errorf("%s %s allowed: status=%d body=%s", route.method, route.path, resp.StatusCode, body)
				}
				if route.path == wireStream && (!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || !strings.Contains(body, "event: plan\n")) {
					t.Errorf("authorized stream did not emit a plan: headers=%v body=%s", resp.Header, body)
				}
			}
			resp, _ := conn.exchange(t, http.MethodGet, "/api/v1/topics", "", http.Header{"Authorization": {graph}})
			if resp.StatusCode != http.StatusTooManyRequests || calls.Load() != 2 {
				t.Errorf("shared IP budget status=%d, planner calls=%d", resp.StatusCode, calls.Load())
			}
		})
	}
}
