package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	axiomauth "github.com/Victoroide/axiom-engine/internal/auth"
	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

const (
	mcpPublicURL   = "https://mcp.example.test/mcp"
	mcpMetadataURL = "https://mcp.example.test/.well-known/oauth-protected-resource/mcp"
	mcpRESTAud     = "https://api.example.test/axiom"
	mcpProtocol    = "2026-07-28"
)

// mcpIssuer is a local HTTPS authorization server with a key set and a token
// endpoint that issues tokens for the resource a client names.
type mcpIssuer struct {
	t      *testing.T
	server *httptest.Server
	issuer string
	keys   atomic.Value
	mu     sync.Mutex
	signer jose.Signer
	minted []string
}

func newMCPIssuer(t *testing.T) *mcpIssuer {
	t.Helper()
	key, signer := verifiedSigningKey(t, "mcp-key-1")
	i := &mcpIssuer{t: t, signer: signer}
	i.keys.Store(verifiedJWKS(t, key))
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(i.keys.Load().([]byte)) })
	mux.HandleFunc("/issuer/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		audience := r.Form.Get("audience")
		if audience == "" {
			audience = r.Form.Get("resource")
		}
		i.mu.Lock()
		i.minted = append(i.minted, r.Form.Get("resource"))
		i.mu.Unlock()
		token := i.token(t, audience, r.Form.Get("tenant"), r.Form.Get("subject"), r.Form.Get("scope"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300})
	})
	i.server = httptest.NewUnstartedServer(mux)
	i.server.Config.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError)
	i.server.StartTLS()
	t.Cleanup(i.server.Close)
	i.issuer = i.server.URL + "/issuer"
	return i
}

// rotate replaces the signing key and publishes only the new one.
func (i *mcpIssuer) rotate(t *testing.T, id string) {
	t.Helper()
	key, signer := verifiedSigningKey(t, id)
	i.mu.Lock()
	i.signer = signer
	i.mu.Unlock()
	i.keys.Store(verifiedJWKS(t, key))
}

func (i *mcpIssuer) token(t *testing.T, audience, tenant, caller, scope string) string {
	t.Helper()
	i.mu.Lock()
	signer := i.signer
	i.mu.Unlock()
	return signVerifiedClaims(t, signer, map[string]any{
		"iss": i.issuer, "aud": audience, "exp": time.Now().Add(5 * time.Minute).Unix(),
		"tenant_id": tenant, "sub": caller, "scope": scope,
	})
}

func (i *mcpIssuer) mintedResources() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.minted...)
}

func (i *mcpIssuer) verifier(t *testing.T, audience string) *axiomauth.Verifier {
	t.Helper()
	verifier, err := axiomauth.New(axiomauth.Config{
		Issuer: i.issuer, Audience: audience, JWKSURL: i.server.URL + "/keys", Algorithms: []string{"RS256"},
		Claims: axiomauth.ClaimMapping{Tenant: "tenant_id"}, Transport: i.server.Client().Transport,
		RefreshInterval: time.Second, RefreshTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

type mcpFixture struct {
	redis    *miniredis.Miniredis
	redisURL string
	issuer   *mcpIssuer
	cert     tls.Certificate
	roots    *x509.CertPool
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	return newMCPFixtureWithRedis(t, "")
}

func newMCPFixtureWithRedis(t *testing.T, redisURL string) *mcpFixture {
	t.Helper()
	var redis *miniredis.Miniredis
	if redisURL == "" {
		redis = miniredis.RunT(t)
		redis.SetTime(time.Now())
		redisURL = "redis://" + redis.Addr()
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "mcp.example.test"},
		DNSNames: []string{"mcp.example.test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return &mcpFixture{
		redis: redis, redisURL: redisURL, issuer: newMCPIssuer(t), roots: roots,
		cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
	}
}

// config loads HTTP settings for a verified, quota-enforcing deployment that
// shares the fixture's Redis.
func (f *mcpFixture) config(t *testing.T, settings map[string]string) config.Config {
	t.Helper()
	env := map[string]string{
		"AXIOM_AUTH_MODE": "verified", "AXIOM_AUTH_ISSUER": f.issuer.issuer, "AXIOM_AUTH_AUDIENCE": mcpRESTAud,
		"AXIOM_AUTH_JWKS_URL": f.issuer.server.URL + "/keys", "REDIS_URL": f.redisURL,
		"AXIOM_TENANT_QUOTA_ENABLED": "true", "AXIOM_TENANT_QUOTA_MAX": "1000", "RATE_LIMIT_MAX": "1000",
	}
	for key, value := range settings {
		env[key] = value
	}
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.VerifiedAuth = f.issuer.verifier(t, mcpRESTAud)
	return cfg
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type mcpPlanner struct {
	calls atomic.Int32
	next  fakePlanner
}

func (p *mcpPlanner) GenerateTopic(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
	p.calls.Add(1)
	if p.next != nil {
		return p.next(ctx, req)
	}
	return []domain.PlanItem{{Topic: req.Topic, Explanation: "Practice", Resources: []domain.Resource{}}}, nil
}

type mcpOptions struct {
	settings map[string]string
	cfg      func(*config.Config)
	remote   func(*config.MCPHTTP)
	planner  *mcpPlanner
	graph    *graph.InMemoryGraph
	logs     io.Writer
}

type mcpInstance struct {
	fx      *mcpFixture
	app     *MCPHTTP
	addr    string
	planner *mcpPlanner
	logs    *syncBuffer
	served  chan error
	cancel  context.CancelFunc
}

// start runs one service instance on a real socket.
func (f *mcpFixture) start(t *testing.T, opts mcpOptions) *mcpInstance {
	t.Helper()
	cfg := f.config(t, opts.settings)
	if opts.cfg != nil {
		opts.cfg(&cfg)
	}
	remote := config.MCPHTTP{
		PublicURL: mcpPublicURL, Issuer: f.issuer.issuer, BindAddr: "127.0.0.1:0", Certificate: f.cert,
		AllowedOrigins: []string{"https://mcp.example.test"}, Verifier: f.issuer.verifier(t, mcpPublicURL),
	}
	if opts.remote != nil {
		opts.remote(&remote)
	}
	planner := opts.planner
	if planner == nil {
		planner = &mcpPlanner{}
	}
	g := opts.graph
	if g == nil {
		g = graph.NewInMemoryGraph()
	}
	logs := &syncBuffer{}
	var sink io.Writer = logs
	if opts.logs != nil {
		sink = io.MultiWriter(logs, opts.logs)
	}
	app, err := NewMCPHTTP(cfg, remote, Dependencies{Graph: g, Planner: planner, Logger: slog.New(slog.NewJSONHandler(sink, nil))}, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	i := &mcpInstance{fx: f, app: app, addr: ln.Addr().String(), planner: planner, logs: logs, served: make(chan error, 1), cancel: cancel}
	go func() { i.served <- app.Serve(ctx, ln) }()
	t.Cleanup(func() {
		i.stop(t)
		_ = app.Close()
	})
	return i
}

// stop begins graceful shutdown and waits for the listener to finish.
func (i *mcpInstance) stop(t testing.TB) {
	t.Helper()
	i.cancel()
	select {
	case err := <-i.served:
		// A listener stopped before its goroutine ran reports the canceled context.
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("serve: %v", err)
		}
		i.served <- nil
	case <-time.After(30 * time.Second):
		t.Error("the listener did not stop")
	}
}

type mcpClientOptions struct {
	http2          bool
	minTLS, maxTLS uint16
}

// client dials this instance for every address, as a load balancer would.
func (i *mcpInstance) client(opts ...mcpClientOptions) *http.Client {
	tlsConfig := &tls.Config{RootCAs: i.fx.roots}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", i.addr)
		},
	}
	for _, opt := range opts {
		transport.ForceAttemptHTTP2 = opt.http2
		tlsConfig.MinVersion, tlsConfig.MaxVersion = opt.minTLS, opt.maxTLS
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type mcpReply struct {
	Status int
	Header http.Header
	Body   string
	Proto  int
}

func (r mcpReply) rpcError() (int64, bool) {
	var parsed struct {
		Error *struct{ Code int64 } `json:"error"`
	}
	if json.Unmarshal([]byte(r.Body), &parsed) != nil || parsed.Error == nil {
		return 0, false
	}
	return parsed.Error.Code, true
}

func mcpMessage(method, params string) string {
	prefix := strings.TrimSuffix(params, "}")
	if prefix != "{" {
		prefix += ","
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s"_meta":{"io.modelcontextprotocol/protocolVersion":%q,"io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, method, prefix, mcpProtocol)
}

// mcpRequest builds a request for a protocol method and tool.
func mcpRequestFor(t testing.TB, method, tool, params, token string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, mcpPublicURL, strings.NewReader(mcpMessage(method, params)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", mcpProtocol)
	req.Header.Set("Mcp-Method", method)
	if tool != "" {
		req.Header.Set("Mcp-Name", tool)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func (i *mcpInstance) send(t testing.TB, client *http.Client, req *http.Request) mcpReply {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return mcpReply{Status: resp.StatusCode, Header: resp.Header, Body: string(body), Proto: resp.ProtoMajor}
}

// call sends one protocol request with the token.
func (i *mcpInstance) call(t testing.TB, token, method, tool, params string, mutate ...func(*http.Request)) mcpReply {
	t.Helper()
	req := mcpRequestFor(t, method, tool, params, token)
	for _, m := range mutate {
		m(req)
	}
	return i.send(t, i.client(), req)
}

func toolCall(t testing.TB, i *mcpInstance, token, tool, args string, mutate ...func(*http.Request)) mcpReply {
	t.Helper()
	return i.call(t, token, "tools/call", tool, `{"name":"`+tool+`","arguments":`+args+`}`, mutate...)
}

const (
	planArgs  = `{"failed_topics":["Recursion"],"vark_profile":"visual"}`
	topicArgs = `{"topic":"Recursion"}`
)

func (i *mcpInstance) get(t testing.TB, path string, mutate ...func(*http.Request)) mcpReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://mcp.example.test"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mutate {
		m(req)
	}
	return i.send(t, i.client(), req)
}

func setHeader(key, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// redisKeys lists the budget keys written so far.
func (f *mcpFixture) redisKeys() []string { return f.redis.Keys() }

// oauthFlow is an OAuth handler that finds the authorization server through
// the resource's challenge and metadata, as a real client must.
type oauthFlow struct {
	t                    *testing.T
	http                 *http.Client
	scope                string
	followScopeChallenge bool
	tenant               string
	subject              string
	audience             string // overrides the requested resource when set
	mu                   sync.Mutex
	token                string
	events               []string
}

var _ auth.OAuthHandler = (*oauthFlow)(nil)

func (f *oauthFlow) TokenSource(context.Context) (oauth2.TokenSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token == "" {
		return nil, nil
	}
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: f.token, TokenType: "Bearer"}), nil
}

func (f *oauthFlow) Authorize(ctx context.Context, _ *http.Request, resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()
	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if err != nil || len(challenges) != 1 || challenges[0].Scheme != "bearer" {
		return fmt.Errorf("unusable challenge %q", resp.Header.Values("WWW-Authenticate"))
	}
	f.record("challenge:" + resp.Status)
	metadataURL := challenges[0].Params["resource_metadata"]
	if metadataURL == "" {
		return fmt.Errorf("the challenge names no resource metadata")
	}
	metadata, err := oauthex.GetProtectedResourceMetadata(ctx, metadataURL, mcpPublicURL, f.http)
	if err != nil {
		return err
	}
	f.record("metadata:" + metadataURL)
	if len(metadata.AuthorizationServers) != 1 {
		return fmt.Errorf("authorization servers: %v", metadata.AuthorizationServers)
	}
	scope := f.scope
	if f.followScopeChallenge && resp.StatusCode == http.StatusForbidden && challenges[0].Params["error"] == "insufficient_scope" {
		scope = challenges[0].Params["scope"]
	}
	form := strings.NewReader("resource=" + mcpPublicURL + "&scope=" + strings.ReplaceAll(scope, " ", "+") +
		"&tenant=" + f.tenant + "&subject=" + f.subject + "&audience=" + f.audience)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.AuthorizationServers[0]+"/token", form)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reply, err := f.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = reply.Body.Close() }()
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(reply.Body).Decode(&issued); err != nil || issued.AccessToken == "" {
		return fmt.Errorf("token endpoint answered %d", reply.StatusCode)
	}
	f.mu.Lock()
	f.token = issued.AccessToken
	f.mu.Unlock()
	f.record("token")
	return nil
}

func (f *oauthFlow) record(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *oauthFlow) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// newFlow returns a handler for the instance, whose client also trusts the issuer.
func (i *mcpInstance) newFlow(t *testing.T, tenant, subject, scope string) *oauthFlow {
	t.Helper()
	issuerClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: i.issuerRoots()}}}
	base := i.client().Transport.(*http.Transport)
	flow := &oauthFlow{t: t, scope: scope, tenant: tenant, subject: subject}
	flow.http = &http.Client{Transport: roundTripperByHost{
		"mcp.example.test": base, "": issuerClient.Transport,
	}}
	t.Cleanup(issuerClient.CloseIdleConnections)
	return flow
}

func (i *mcpInstance) issuerRoots() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(i.fx.issuer.server.Certificate())
	return pool
}

type roundTripperByHost map[string]http.RoundTripper

// CloseIdleConnections closes the idle connections of every transport.
func (m roundTripperByHost) CloseIdleConnections() {
	for _, transport := range m {
		if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

func (m roundTripperByHost) RoundTrip(r *http.Request) (*http.Response, error) {
	if transport, ok := m[r.URL.Hostname()]; ok {
		return transport.RoundTrip(r)
	}
	return m[""].RoundTrip(r)
}

// connect opens an SDK client session using the OAuth flow.
func (i *mcpInstance) connect(ctx context.Context, flow *oauthFlow, httpClient *http.Client) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "sdk-test", Version: "1"}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: mcpPublicURL, HTTPClient: httpClient, OAuthHandler: flow, MaxRetries: -1,
	}, &mcp.ClientSessionOptions{ProtocolVersion: mcpProtocol})
}
