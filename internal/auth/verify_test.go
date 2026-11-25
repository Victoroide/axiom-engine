package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const testAudience = "https://resource.example/api"

var testTime = time.Unix(1_800_000_000, 0)

type signingKey struct {
	jwk     jose.JSONWebKey
	signer  jose.Signer
	private jose.JSONWebKey
}

func newSigningKey(t *testing.T, id string, algorithm jose.SignatureAlgorithm) signingKey {
	t.Helper()
	var privateKey any
	var err error
	if algorithm == jose.RS256 {
		privateKey, err = rsa.GenerateKey(rand.Reader, 2048)
	} else {
		privateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		t.Fatal(err)
	}
	key := jose.JSONWebKey{Key: privateKey, KeyID: id, Algorithm: string(algorithm), Use: "sig"}
	options := (&jose.SignerOptions{}).WithType("at+jwt")
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: algorithm, Key: key}, options)
	if err != nil {
		t.Fatal(err)
	}
	return signingKey{key.Public(), signer, key}
}

func sign(t *testing.T, key signingKey, claims any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return signJSON(t, key.signer, payload)
}

func signJSON(t *testing.T, signer jose.Signer, payload []byte) string {
	t.Helper()
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

func claimsFor(issuer string) map[string]any {
	return map[string]any{
		"iss": issuer, "aud": testAudience, "exp": testTime.Add(time.Minute).Unix(),
		"nbf": testTime.Add(-time.Minute).Unix(), "iat": testTime.Add(-time.Minute).Unix(),
		"org_id": "tenant-canary", "sub": "caller-canary", "scope": "plan:read graph:read",
		"learner_id": "learner-canary",
	}
}

func jwks(t *testing.T, keys ...jose.JSONWebKey) []byte {
	t.Helper()
	if keys == nil {
		keys = []jose.JSONWebKey{}
	}
	data, err := json.Marshal(jose.JSONWebKeySet{Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureConfig(server *httptest.Server) Config {
	return Config{
		Issuer: server.URL + "/issuer", Audience: testAudience, JWKSURL: server.URL + "/keys",
		Algorithms: []string{"ES256", "RS256"}, Claims: ClaimMapping{Tenant: "org_id"},
		Transport: server.Client().Transport,
	}
}

func fixtureVerifier(t *testing.T, config Config) (*Verifier, func(time.Time)) {
	t.Helper()
	verifier, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	var clock atomic.Pointer[time.Time]
	setTime := func(now time.Time) { clock.Store(&now) }
	setTime(testTime)
	verifier.now = func() time.Time { return *clock.Load() }
	return verifier, setTime
}

func assertRejected(t *testing.T, verifier *Verifier, token string, want error, scopes ...string) {
	t.Helper()
	principal, err := verifier.Verify(t.Context(), token, scopes...)
	if !errors.Is(err, want) || principal != (Principal{}) {
		t.Errorf("rejected principal = %v, error = %v, want zero principal and %v", principal, err, want)
		return
	}
	if err.Error() != want.Error() {
		t.Error("verification error contains additional details")
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	base := Config{
		Issuer: "https://issuer.example/tenant", Audience: testAudience, JWKSURL: "https://issuer.example/keys",
		Algorithms: []string{"RS256"}, Claims: ClaimMapping{Tenant: "org_id"},
	}
	if _, err := New(base); err != nil {
		t.Fatal(err)
	}
	for name, update := range map[string]func(*Config){
		"missing audience":          func(c *Config) { c.Audience = "" },
		"issuer credentials":        func(c *Config) { c.Issuer = "https://user:password@issuer.example" },
		"issuer query":              func(c *Config) { c.Issuer += "?" },
		"plaintext endpoint":        func(c *Config) { c.JWKSURL = "http://issuer.example/keys" },
		"endpoint fragment":         func(c *Config) { c.JWKSURL += "#key" },
		"unsupported algorithm":     func(c *Config) { c.Algorithms = []string{"HS256"} },
		"duplicate algorithm":       func(c *Config) { c.Algorithms = []string{"RS256", "RS256"} },
		"reserved tenant claim":     func(c *Config) { c.Claims.Tenant = "iss" },
		"overlapping scope claim":   func(c *Config) { c.Claims.Scope = c.Claims.Tenant },
		"empty client registry":     func(c *Config) { c.Claims.ClientID = "client_id" },
		"unmapped client registry":  func(c *Config) { c.Claims.RegisteredClients = []string{"registered"} },
		"excessive clock skew":      func(c *Config) { c.ClockSkew = time.Minute + time.Second },
		"excessive refresh timeout": func(c *Config) { c.RefreshTimeout = 11 * time.Second },
		"excessive refresh rate":    func(c *Config) { c.RefreshInterval = time.Second },
		"interval exceeds key age":  func(c *Config) { c.MaxKeyAge = 20 * time.Second },
		"excessive key age":         func(c *Config) { c.MaxKeyAge = 25 * time.Hour },
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			update(&config)
			if _, err := New(config); err == nil {
				t.Error("invalid configuration was accepted")
			}
		})
	}
}

func TestVerifySignedAccessToken(t *testing.T) {
	key := newSigningKey(t, "current", jose.ES256)
	material := jwks(t, key.jwk)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/keys" || r.Header.Get("Authorization") != "" {
			t.Error("key request must use the configured endpoint without authorization")
		}
		_, _ = w.Write(material)
	}))
	t.Cleanup(server.Close)
	config := fixtureConfig(server)
	config.ClockSkew = 5 * time.Second
	verifier, _ := fixtureVerifier(t, config)

	tests := []struct {
		name   string
		change func(map[string]any)
		scopes []string
		want   error
	}{
		{name: "valid", scopes: []string{"plan:read", "graph:read"}},
		{name: "audience list", change: setClaim("aud", []string{"other", testAudience})},
		{name: "wrong audience", change: setClaim("aud", testAudience+"/"), want: ErrInvalidToken},
		{name: "wrong audience list", change: setClaim("aud", []string{"other", testAudience + "/"}), want: ErrInvalidToken},
		{name: "absent audience", change: removeClaim("aud"), want: ErrInvalidToken},
		{name: "duplicate audience", change: setClaim("aud", []string{testAudience, testAudience}), want: ErrInvalidToken},
		{name: "wrong issuer", change: setClaim("iss", server.URL), want: ErrInvalidToken},
		{name: "absent subject", change: removeClaim("sub"), want: ErrInvalidToken},
		{name: "null subject", change: setClaim("sub", nil), want: ErrInvalidToken},
		{name: "list subject", change: setClaim("sub", []string{"caller"}), want: ErrInvalidToken},
		{name: "oversized subject", change: setClaim("sub", strings.Repeat("x", 129)), want: ErrInvalidToken},
		{name: "padded subject", change: setClaim("sub", " caller "), want: ErrInvalidToken},
		{name: "client metadata in subject mode", change: setClaim("client_id", "oauth-client")},
		{name: "absent tenant", change: removeClaim("org_id"), want: ErrInvalidToken},
		{name: "ambiguous tenant", change: setClaim("org_id", []string{"tenant", "other"}), want: ErrInvalidToken},
		{name: "sender constrained", change: setClaim("cnf", map[string]string{"jkt": "thumbprint"}), want: ErrInvalidToken},
		{name: "missing expiry", change: removeClaim("exp"), want: ErrInvalidToken},
		{name: "past expiry with skew", change: setClaim("exp", testTime.Add(-time.Second).Unix())},
		{name: "expiry at skew boundary", change: setClaim("exp", testTime.Add(-5*time.Second).Unix()), want: ErrInvalidToken},
		{name: "future validity with skew", change: setClaim("nbf", testTime.Add(5*time.Second).Unix())},
		{name: "future validity beyond skew", change: setClaim("nbf", testTime.Add(6*time.Second).Unix()), want: ErrInvalidToken},
		{name: "future issuance beyond skew", change: setClaim("iat", testTime.Add(6*time.Second).Unix()), want: ErrInvalidToken},
		{name: "inverted validity window", change: setClaim("nbf", testTime.Add(time.Minute).Unix()), want: ErrInvalidToken},
		{name: "inverted validity window within skew", change: func(claims map[string]any) {
			claims["exp"] = testTime.Add(-time.Second).Unix()
			claims["nbf"] = testTime.Add(time.Second).Unix()
		}, want: ErrInvalidToken},
		{name: "bad timestamp type", change: setClaim("nbf", nil), want: ErrInvalidToken},
		{name: "oversized timestamp", change: setClaim("exp", json.Number("1000000000000000000000")), want: ErrInvalidToken},
		{name: "missing scope claim", change: removeClaim("scope"), want: ErrInvalidToken},
		{name: "missing required scope", scopes: []string{"plan:read", "graph:write"}, want: ErrInsufficientScope},
		{name: "scope case mismatch", scopes: []string{"Plan:read"}, want: ErrInsufficientScope},
		{name: "duplicate scope", change: setClaim("scope", "plan:read plan:read"), want: ErrInvalidToken},
		{name: "scope array", change: setClaim("scope", []string{"plan:read"}), want: ErrInvalidToken},
		{name: "invalid scope separator", change: setClaim("scope", "plan:read\tgraph:read"), want: ErrInvalidToken},
		{name: "oversized token", change: setClaim("padding", strings.Repeat("x", 8192)), want: ErrInvalidToken},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := claimsFor(config.Issuer)
			if tc.change != nil {
				tc.change(claims)
			}
			token := sign(t, key, claims)
			if tc.want != nil {
				assertRejected(t, verifier, token, tc.want, tc.scopes...)
				return
			}
			principal, err := verifier.Verify(t.Context(), token, tc.scopes...)
			if err != nil {
				t.Fatal(err)
			}
			if principal.Issuer() != config.Issuer || principal.Tenant() != "tenant-canary" || principal.Caller() != "caller-canary" {
				t.Fatal("principal does not match verified issuer, tenant and subject")
			}
			encoded, err := json.Marshal(principal)
			if err != nil {
				t.Fatal(err)
			}
			diagnostics := string(encoded) + fmt.Sprintf("%v %+v %#v", principal, principal, principal)
			for _, secret := range []string{config.Issuer, "tenant-canary", "caller-canary", "learner-canary", token} {
				if strings.Contains(diagnostics, secret) {
					t.Error("principal formatting exposed verifier input")
				}
			}
		})
	}

	claims, err := json.Marshal(claimsFor(config.Issuer))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := append([]byte(`{"sub":"another-caller",`), claims[1:]...)
	assertRejected(t, verifier, signJSON(t, key.signer, duplicate), ErrInvalidToken)
	assertRejected(t, verifier, strings.Repeat("x", 8193), ErrInvalidToken)
	assertRejected(t, verifier, sign(t, newSigningKey(t, key.jwk.KeyID, jose.ES256), claimsFor(config.Issuer)), ErrInvalidToken)
}

func TestVerifyRejectsUntrustedHeadersBeforeFetchingKeys(t *testing.T) {
	key := newSigningKey(t, "ec", jose.ES256)
	rsaKey := newSigningKey(t, "rsa", jose.RS256)
	material := jwks(t, key.jwk, rsaKey.jwk)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/keys" || r.URL.RawQuery != "" {
			t.Error("verification requested an unconfigured endpoint")
		}
		_, _ = w.Write(material)
	}))
	t.Cleanup(server.Close)
	config := fixtureConfig(server)
	config.Algorithms = []string{"ES256"}
	verifier, _ := fixtureVerifier(t, config)
	claims := claimsFor(config.Issuer)
	endpoint := server.URL + "/unconfigured?secret=key-url-canary"

	assertRejected(t, verifier, sign(t, rsaKey, claims), ErrInvalidToken)
	for _, tc := range []struct {
		typ    jose.ContentType
		header jose.HeaderKey
		value  any
	}{
		{typ: ""},
		{typ: "JWT"},
		{typ: "at+jwt", header: "jku", value: endpoint},
		{typ: "at+jwt", header: "x5u", value: endpoint},
		{typ: "at+jwt", header: "jwk", value: key.jwk},
	} {
		options := &jose.SignerOptions{}
		if tc.typ != "" {
			options.WithType(tc.typ)
		}
		if tc.header != "" {
			options.WithHeader(tc.header, tc.value)
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key.private}, options)
		if err != nil {
			t.Fatal(err)
		}
		assertRejected(t, verifier, sign(t, signingKey{signer: signer}, claims), ErrInvalidToken)
	}
	assertRequests(t, &requests, 0)
	if _, err := verifier.Verify(t.Context(), sign(t, key, claims)); err != nil {
		t.Fatal(err)
	}
	assertRequests(t, &requests, 1)
}

func setClaim(name string, value any) func(map[string]any) {
	return func(claims map[string]any) { claims[name] = value }
}

func removeClaim(name string) func(map[string]any) {
	return func(claims map[string]any) { delete(claims, name) }
}

func TestVerifyRegisteredClient(t *testing.T) {
	key := newSigningKey(t, "rsa", jose.RS256)
	material := jwks(t, key.jwk)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(material)
	}))
	t.Cleanup(server.Close)
	config := fixtureConfig(server)
	config.Claims = ClaimMapping{
		Tenant: "tenant", Scope: "permissions", ClientID: "registered_id", RegisteredClients: []string{"client-a"},
	}
	verifier, _ := fixtureVerifier(t, config)
	// Construction takes ownership of the mapping without sharing the caller's slices.
	config.Claims.RegisteredClients[0] = "client-b"
	config.Algorithms[0] = "HS256"
	config.Algorithms[1] = "HS256"

	for _, tc := range []struct {
		name, client, subject string
		valid                 bool
	}{
		{name: "registered client", client: "client-a", valid: true},
		{name: "matching subject", client: "client-a", subject: "client-a", valid: true},
		{name: "conflicting subject", client: "client-a", subject: "other"},
		{name: "unregistered client", client: "client-b"},
		{name: "no subject fallback", subject: "client-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := claimsFor(config.Issuer)
			delete(claims, "sub")
			delete(claims, "scope")
			delete(claims, "org_id")
			claims["tenant"] = "tenant-b"
			claims["permissions"] = "graph:read"
			if tc.client != "" {
				claims["registered_id"] = tc.client
			}
			if tc.subject != "" {
				claims["sub"] = tc.subject
			}
			token := sign(t, key, claims)
			if !tc.valid {
				assertRejected(t, verifier, token, ErrInvalidToken, "graph:read")
				return
			}
			principal, err := verifier.Verify(t.Context(), token, "graph:read")
			if err != nil || principal.Caller() != tc.client || principal.Tenant() != "tenant-b" {
				t.Fatalf("registered client rejected: %v", err)
			}
		})
	}
}

func assertRequests(t *testing.T, count *atomic.Int32, want int32) {
	t.Helper()
	if got := count.Load(); got != want {
		t.Errorf("key requests = %d, want %d", got, want)
	}
}

func TestPrincipalHasScope(t *testing.T) {
	key := newSigningKey(t, "scopes", jose.ES256)
	material := jwks(t, key.jwk)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(material) }))
	t.Cleanup(server.Close)
	verifier, _ := fixtureVerifier(t, fixtureConfig(server))
	claims := claimsFor(verifier.issuer)
	claims["scope"] = "graph:read plan:generate-draft"
	principal, err := verifier.Verify(t.Context(), sign(t, key, claims))
	if err != nil {
		t.Fatal(err)
	}
	for scope, want := range map[string]bool{
		"graph:read": true, "plan:generate-draft": true, "plan:generate": false, "graph": false,
		"read": false, "graph:read plan:generate-draft": false, "": false,
	} {
		if got := principal.HasScope(scope); got != want {
			t.Errorf("HasScope(%q) = %v, want %v", scope, got, want)
		}
	}
	if (Principal{}).HasScope("graph:read") || (Principal{}).HasScope("") {
		t.Error("the zero principal has a scope")
	}
	if text := fmt.Sprintf("%v %+v %#v", principal, principal, principal); strings.Contains(text, "graph:read") {
		t.Errorf("formatted principal leaks scopes: %s", text)
	}
}
