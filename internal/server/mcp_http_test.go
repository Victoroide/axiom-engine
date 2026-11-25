package server

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
)

const (
	allScopes = "graph:read plan:generate"
	privateID = "private-tenant-canary"
	callerID  = "private-caller-canary"
)

func TestMCPHTTPServesProtectedResourceMetadata(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	reply := i.get(t, "/.well-known/oauth-protected-resource/mcp")
	var metadata struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
		BearerMethods        []string `json:"bearer_methods_supported"`
	}
	if err := json.Unmarshal([]byte(reply.Body), &metadata); err != nil || reply.Status != http.StatusOK {
		t.Fatalf("status=%d body=%s err=%v", reply.Status, reply.Body, err)
	}
	if metadata.Resource != mcpPublicURL || len(metadata.AuthorizationServers) != 1 || metadata.AuthorizationServers[0] != fx.issuer.issuer ||
		strings.Join(metadata.ScopesSupported, " ") != allScopes || strings.Join(metadata.BearerMethods, " ") != "header" {
		t.Fatalf("metadata = %s", reply.Body)
	}
	if reply.Header.Get("Content-Type") != "application/json" || reply.Header.Get("Cache-Control") != "public, max-age=300" ||
		reply.Header.Get("X-Content-Type-Options") != "nosniff" || reply.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("headers = %v", reply.Header)
	}
	if head := i.send(t, i.client(), mustRequest(t, http.MethodHead, "https://mcp.example.test/.well-known/oauth-protected-resource/mcp")); head.Status != http.StatusOK || head.Body != "" {
		t.Fatalf("HEAD status=%d body=%q", head.Status, head.Body)
	}
	post := i.send(t, i.client(), mustRequest(t, http.MethodPost, "https://mcp.example.test/.well-known/oauth-protected-resource/mcp"))
	if post.Status != http.StatusMethodNotAllowed || post.Header.Get("Allow") != "GET, HEAD, OPTIONS" {
		t.Fatalf("POST status=%d allow=%q", post.Status, post.Header.Get("Allow"))
	}
	if len(fx.redisKeys()) != 0 {
		t.Fatalf("public metadata used request budgets: %v", fx.redisKeys())
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp/", "/.well-known/oauth-authorization-server"} {
		if got := i.get(t, path); got.Status != http.StatusNotFound {
			t.Errorf("%s status = %d", path, got.Status)
		}
	}
}

func nopCloser(body string) io.ReadCloser { return io.NopCloser(strings.NewReader(body)) }

func mustRequest(t testing.TB, method, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestMCPHTTPChallengesUnauthenticatedCallers(t *testing.T) {
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner})
	valid := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	rest := fx.issuer.token(t, mcpRESTAud, privateID, callerID, allScopes)
	list := func(token string, mutate ...func(*http.Request)) mcpReply {
		return i.call(t, token, "tools/list", "", `{}`, mutate...)
	}
	expired := signVerifiedClaims(t, fx.issuer.signer, map[string]any{
		"iss": fx.issuer.issuer, "aud": mcpPublicURL, "exp": time.Now().Add(-time.Hour).Unix(), "tenant_id": privateID, "sub": callerID, "scope": allScopes,
	})
	foreign := signVerifiedClaims(t, fx.issuer.signer, map[string]any{
		"iss": "https://other-issuer.example.test", "aud": mcpPublicURL, "exp": time.Now().Add(time.Hour).Unix(), "tenant_id": privateID, "sub": callerID, "scope": allScopes,
	})
	missing := `Bearer resource_metadata="` + mcpMetadataURL + `"`
	invalid := `Bearer error="invalid_token", resource_metadata="` + mcpMetadataURL + `"`
	malformed := `Bearer error="invalid_request"`
	for _, tc := range []struct {
		name      string
		token     string
		mutate    []func(*http.Request)
		challenge string
	}{
		{name: "no credential", challenge: missing},
		{name: "other scheme", mutate: []func(*http.Request){setHeader("Authorization", "Basic dXNlcjpwYXNz")}, challenge: missing},
		{name: "other scheme with parameters", mutate: []func(*http.Request){setHeader("Authorization", `Digest username="user", realm="test"`)}, challenge: missing},
		{name: "Digest with an empty realm", mutate: []func(*http.Request){setHeader("Authorization", `Digest username="user", realm="", nonce="test", uri="/mcp", response="0123456789abcdef", qop=auth, nc=00000001, cnonce="test"`)}, challenge: missing},
		{name: "Digest with escaped quotes and commas", mutate: []func(*http.Request){setHeader("Authorization", `Digest realm="quoted\", value", nonce="test"`)}, challenge: missing},
		{name: "Digest with an empty unquoted realm", mutate: []func(*http.Request){setHeader("Authorization", `Digest username="user", realm=, nonce="test"`)}, challenge: malformed},
		{name: "empty bearer", mutate: []func(*http.Request){setHeader("Authorization", "Bearer ")}, challenge: malformed},
		{name: "repeated header", token: valid, mutate: []func(*http.Request){func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+valid) }}, challenge: malformed},
		{name: "token list", token: valid, mutate: []func(*http.Request){setHeader("Authorization", "Bearer "+valid+", "+valid)}, challenge: malformed},
		{name: "combined schemes", mutate: []func(*http.Request){setHeader("Authorization", "Basic dXNlcjpwYXNz, Bearer "+valid)}, challenge: malformed},
		{name: "combined schemes with an empty Digest realm", mutate: []func(*http.Request){setHeader("Authorization", `Digest username="user", realm="", Bearer `+valid)}, challenge: malformed},
		{name: "space inside credential", token: "token another", challenge: malformed},
		{name: "legacy header beside a token", token: valid, mutate: []func(*http.Request){setHeader("X-Internal-Token", strings.Repeat("s", 32))}, challenge: malformed},
		{name: "legacy header alone", mutate: []func(*http.Request){setHeader("X-Internal-Token", strings.Repeat("s", 32))}, challenge: malformed},
		{name: "unsigned text", token: "not-a-token", challenge: invalid},
		{name: "token for the API", token: rest, challenge: invalid},
		{name: "expired token", token: expired, challenge: invalid},
		{name: "token from another issuer", token: foreign, challenge: invalid},
		{name: "tampered token", token: valid[:len(valid)-3] + "AAA", challenge: invalid},
		{name: "oversized token", token: strings.Repeat("a", 9000), challenge: invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := list(tc.token, tc.mutate...)
			status, code := http.StatusUnauthorized, "unauthorized"
			if tc.challenge == malformed {
				status, code = http.StatusBadRequest, "invalid_request"
			}
			if reply.Status != status || reply.Header.Get("WWW-Authenticate") != tc.challenge ||
				!strings.Contains(reply.Body, `"error":"`+code+`"`) {
				t.Fatalf("status=%d challenge=%q body=%s", reply.Status, reply.Header.Get("WWW-Authenticate"), reply.Body)
			}
		})
	}
	if got := toolCall(t, i, "", "generate_study_plan", planArgs); got.Status != http.StatusUnauthorized || planner.calls.Load() != 0 {
		t.Fatalf("status=%d planner calls=%d", got.Status, planner.calls.Load())
	}
	if keys := fx.redisKeys(); len(keys) != 0 {
		t.Fatalf("unauthenticated requests wrote budget keys: %v", keys)
	}
	if reply := list(valid); reply.Status != http.StatusOK {
		t.Fatalf("control request status=%d body=%s", reply.Status, reply.Body)
	}
}

func TestMCPHTTPChecksScopesPerOperation(t *testing.T) {
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner})
	graphOnly := fx.issuer.token(t, mcpPublicURL, privateID, callerID, "graph:read")
	planOnly := fx.issuer.token(t, mcpPublicURL, privateID, callerID, "plan:generate")
	neither := fx.issuer.token(t, mcpPublicURL, privateID, callerID, "profile")
	challenge := func(scope string) string {
		return `Bearer error="insufficient_scope", scope="` + scope + `", resource_metadata="` + mcpMetadataURL + `"`
	}
	for _, tc := range []struct {
		name, token, method, tool, params, scope string
		status                                   int
	}{
		{"discover with graph", graphOnly, "server/discover", "", `{}`, "", 200},
		{"discover with plan", planOnly, "server/discover", "", `{}`, "", 200},
		{"list with graph", graphOnly, "tools/list", "", `{}`, "", 200},
		{"list with plan", planOnly, "tools/list", "", `{}`, "", 200},
		{"discover with neither", neither, "server/discover", "", `{}`, "graph:read", 403},
		{"list with neither", neither, "tools/list", "", `{}`, "graph:read", 403},
		{"topics with graph", graphOnly, "tools/call", "list_topics", `{"name":"list_topics","arguments":{}}`, "", 200},
		{"path with graph", graphOnly, "tools/call", "get_topic_path", `{"name":"get_topic_path","arguments":` + topicArgs + `}`, "", 200},
		{"graph with graph", graphOnly, "tools/call", "get_cognitive_graph", `{"name":"get_cognitive_graph","arguments":{"topics":["Recursion"]}}`, "", 200},
		{"plan with plan", planOnly, "tools/call", "generate_study_plan", `{"name":"generate_study_plan","arguments":` + planArgs + `}`, "", 200},
		{"topics with plan", planOnly, "tools/call", "list_topics", `{"name":"list_topics","arguments":{}}`, "graph:read", 403},
		{"path with plan", planOnly, "tools/call", "get_topic_path", `{"name":"get_topic_path","arguments":` + topicArgs + `}`, "graph:read", 403},
		{"graph with plan", planOnly, "tools/call", "get_cognitive_graph", `{"name":"get_cognitive_graph","arguments":{"topics":["Recursion"]}}`, "graph:read", 403},
		{"plan with graph", graphOnly, "tools/call", "generate_study_plan", `{"name":"generate_study_plan","arguments":` + planArgs + `}`, "plan:generate", 403},
		{"plan with neither", neither, "tools/call", "generate_study_plan", `{"name":"generate_study_plan","arguments":` + planArgs + `}`, "plan:generate", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, keys := planner.calls.Load(), len(fx.redisKeys())
			reply := i.call(t, tc.token, tc.method, tc.tool, tc.params)
			if reply.Status != tc.status {
				t.Fatalf("status=%d body=%s", reply.Status, reply.Body)
			}
			if tc.status == http.StatusForbidden {
				if reply.Header.Get("WWW-Authenticate") != challenge(tc.scope) || !strings.Contains(reply.Body, `"error":"insufficient_scope"`) {
					t.Fatalf("challenge=%q body=%s", reply.Header.Get("WWW-Authenticate"), reply.Body)
				}
				if planner.calls.Load() != before || len(fx.redisKeys()) != keys {
					t.Fatalf("a denied request ran work or consumed budget")
				}
			} else if code, failed := reply.rpcError(); failed {
				t.Fatalf("protocol error %d: %s", code, reply.Body)
			}
		})
	}
}

func TestMCPHTTPRejectsUnknownOperationsBeforeAdmission(t *testing.T) {
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	for _, tc := range []struct {
		name, method, tool, params string
		code                       int64
	}{
		{"unknown tool", "tools/call", "drop_tables", `{"name":"drop_tables","arguments":{}}`, -32602},
		{"subscription stream", "subscriptions/listen", "", `{}`, -32601},
		{"resource listing", "resources/list", "", `{}`, -32601},
		{"prompt retrieval", "prompts/get", "plan", `{"name":"plan"}`, -32601},
		{"completion", "completion/complete", "", `{}`, -32601},
		{"initialization", "initialize", "", `{}`, -32601},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := i.call(t, token, tc.method, tc.tool, tc.params)
			if code, ok := reply.rpcError(); !ok || code != tc.code || reply.Status != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", reply.Status, reply.Body)
			}
		})
	}
	if len(fx.redisKeys()) != 0 || planner.calls.Load() != 0 {
		t.Fatalf("rejected operations consumed budget %v or ran %d plans", fx.redisKeys(), planner.calls.Load())
	}
}

func TestMCPHTTPChecksHeadersBeforeScopesOrBudgets(t *testing.T) {
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		code   int64
	}{
		{"method header mismatch", setHeader("Mcp-Method", "tools/list"), mcp.CodeHeaderMismatch},
		{"name header mismatch", setHeader("Mcp-Name", "list_topics"), mcp.CodeHeaderMismatch},
		{"missing version", setHeader("Mcp-Protocol-Version", ""), mcp.CodeHeaderMismatch},
		{"version header differs from the body", setHeader("Mcp-Protocol-Version", "2025-11-25"), mcp.CodeHeaderMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := toolCall(t, i, token, "generate_study_plan", planArgs, tc.mutate)
			if code, ok := reply.rpcError(); !ok || reply.Status != http.StatusBadRequest || code != tc.code {
				t.Fatalf("status=%d body=%s", reply.Status, reply.Body)
			}
		})
	}
	for _, body := range []string{`{"jsonrpc":"2.0","id":1`, `[]`, `{"jsonrpc":"1.0","id":1,"method":"tools/list"}`} {
		req := mcpRequestFor(t, "tools/list", "", `{}`, token)
		req.Body, req.ContentLength = nopCloser(body), int64(len(body))
		if reply := i.send(t, i.client(), req); reply.Status != http.StatusBadRequest {
			t.Errorf("body %q status = %d", body, reply.Status)
		}
	}
	oversized := mcpRequestFor(t, "tools/list", "", `{}`, token)
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` + strings.Repeat("x", mcp.DefaultMaxRequestBodyBytes) + `"}}`
	oversized.Body, oversized.ContentLength = nopCloser(big), int64(len(big))
	if reply := i.send(t, i.client(), oversized); reply.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body status = %d", reply.Status)
	}
	if len(fx.redisKeys()) != 0 || planner.calls.Load() != 0 {
		t.Fatalf("malformed requests consumed budget %v or ran %d plans", fx.redisKeys(), planner.calls.Load())
	}
}

func TestMCPHTTPOriginPolicy(t *testing.T) {
	const app = "https://app.example.test"
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner, remote: func(r *config.MCPHTTP) { r.AllowedOrigins = []string{app} }})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	for _, tc := range []struct {
		name   string
		mutate []func(*http.Request)
		status int
	}{
		{"native client", nil, 200},
		{"configured origin", []func(*http.Request){setHeader("Origin", app)}, 200},
		{"configured origin from another site", []func(*http.Request){setHeader("Origin", app), setHeader("Sec-Fetch-Site", "cross-site")}, 200},
		{"origin of the endpoint itself", []func(*http.Request){setHeader("Origin", "https://mcp.example.test")}, 403},
		{"other origin", []func(*http.Request){setHeader("Origin", "https://other.example.test")}, 403},
		{"plain HTTP origin", []func(*http.Request){setHeader("Origin", "http://app.example.test")}, 403},
		{"opaque origin", []func(*http.Request){setHeader("Origin", "null")}, 403},
		{"empty origin", []func(*http.Request){setHeader("Origin", "")}, 403},
		{"repeated origin", []func(*http.Request){setHeader("Origin", app), func(r *http.Request) { r.Header.Add("Origin", app) }}, 403},
		{"uppercase origin", []func(*http.Request){setHeader("Origin", "https://APP.example.test")}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := i.call(t, token, "tools/list", "", `{}`, tc.mutate...)
			if reply.Status != tc.status {
				t.Fatalf("status=%d body=%s", reply.Status, reply.Body)
			}
			if tc.status == http.StatusForbidden && (reply.Header.Get("WWW-Authenticate") != "" || reply.Header.Get("Access-Control-Allow-Origin") != "") {
				t.Fatalf("a refused origin received credentials guidance or CORS headers: %v", reply.Header)
			}
			if tc.status == http.StatusOK {
				want := ""
				if len(tc.mutate) > 0 {
					want = app
				}
				if got := reply.Header.Get("Access-Control-Allow-Origin"); got != want {
					t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, want)
				}
			}
		})
	}
	for _, other := range []string{"https://other.example.test", "null"} {
		reply := i.call(t, "", "tools/list", "", `{}`, setHeader("Origin", other))
		if reply.Status != http.StatusForbidden {
			t.Errorf("unauthenticated request from %q: status = %d", other, reply.Status)
		}
		if got := i.get(t, "/.well-known/oauth-protected-resource/mcp", setHeader("Origin", other)); got.Status != http.StatusForbidden {
			t.Errorf("metadata from %q: status = %d", other, got.Status)
		}
	}
	if got := i.get(t, "/.well-known/oauth-protected-resource/mcp", setHeader("Origin", app)); got.Status != http.StatusOK || got.Header.Get("Access-Control-Allow-Origin") != app || got.Header.Get("Vary") != "Origin" {
		t.Fatalf("metadata for the configured origin: status=%d headers=%v", got.Status, got.Header)
	}
	if planner.calls.Load() != 0 {
		t.Fatalf("a refused origin ran %d plans", planner.calls.Load())
	}
}

func TestMCPHTTPDefaultOriginIsThePublicOrigin(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	if reply := i.call(t, token, "tools/list", "", `{}`, setHeader("Origin", "https://mcp.example.test")); reply.Status != http.StatusOK {
		t.Fatalf("status = %d: %s", reply.Status, reply.Body)
	}
	if reply := i.call(t, token, "tools/list", "", `{}`, setHeader("Origin", "https://app.example.test")); reply.Status != http.StatusForbidden {
		t.Fatalf("status = %d", reply.Status)
	}
}

func TestMCPHTTPPreflightCannotInvokeTools(t *testing.T) {
	const app = "https://app.example.test"
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner, remote: func(r *config.MCPHTTP) { r.AllowedOrigins = []string{app} }})
	preflight := func(path, origin string) mcpReply {
		req := mustRequest(t, http.MethodOptions, "https://mcp.example.test"+path)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type, mcp-protocol-version")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		return i.send(t, i.client(), req)
	}
	reply := preflight("/mcp", app)
	if reply.Status != http.StatusNoContent || reply.Header.Get("Access-Control-Allow-Origin") != app ||
		reply.Header.Get("Access-Control-Allow-Methods") != "POST" || reply.Header.Get("Access-Control-Max-Age") != "600" ||
		!strings.Contains(reply.Header.Get("Access-Control-Allow-Headers"), "Authorization") ||
		!strings.Contains(reply.Header.Get("Access-Control-Allow-Headers"), "Mcp-Protocol-Version") || reply.Body != "" {
		t.Fatalf("preflight status=%d headers=%v body=%q", reply.Status, reply.Header, reply.Body)
	}
	if reply := preflight("/.well-known/oauth-protected-resource/mcp", app); reply.Status != http.StatusNoContent || reply.Header.Get("Access-Control-Allow-Methods") != "GET, HEAD" {
		t.Fatalf("metadata preflight status=%d headers=%v", reply.Status, reply.Header)
	}
	for _, origin := range []string{"https://other.example.test", "null"} {
		if reply := preflight("/mcp", origin); reply.Status != http.StatusForbidden || reply.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("preflight from %q: status=%d headers=%v", origin, reply.Status, reply.Header)
		}
	}
	// Without an Origin the request is not a browser's permission check.
	if reply := preflight("/mcp", ""); reply.Status != http.StatusMethodNotAllowed {
		t.Errorf("OPTIONS without an origin status = %d", reply.Status)
	}
	if planner.calls.Load() != 0 || len(fx.redisKeys()) != 0 {
		t.Fatalf("a preflight ran %d plans and wrote %v", planner.calls.Load(), fx.redisKeys())
	}
}

func TestMCPHTTPChecksTheHostAndIgnoresForwardedIdentity(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	host := func(value string) func(*http.Request) { return func(r *http.Request) { r.Host = value } }
	for _, tc := range []struct {
		name   string
		mutate []func(*http.Request)
		status int
	}{
		{"configured host", nil, 200},
		{"default port written out", []func(*http.Request){host("mcp.example.test:443")}, 200},
		{"mixed case", []func(*http.Request){host("MCP.Example.Test")}, 200},
		{"other host", []func(*http.Request){host("evil.example.test")}, 403},
		{"other port", []func(*http.Request){host("mcp.example.test:8443")}, 403},
		{"address", []func(*http.Request){host("127.0.0.1")}, 403},
		{"trailing dot", []func(*http.Request){host("mcp.example.test.")}, 403},
		{"suffix", []func(*http.Request){host("mcp.example.test.evil.example")}, 403},
		{"forwarded host cannot fix a wrong host", []func(*http.Request){host("evil.example.test"), setHeader("X-Forwarded-Host", "mcp.example.test"), setHeader("Forwarded", "host=mcp.example.test")}, 403},
		{"forwarded identity cannot break a right host", []func(*http.Request){setHeader("X-Forwarded-Host", "evil.example.test"), setHeader("X-Forwarded-Proto", "http"), setHeader("X-Forwarded-For", "203.0.113.9"), setHeader("Forwarded", "host=evil.example.test;proto=http")}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if reply := i.call(t, token, "tools/list", "", `{}`, tc.mutate...); reply.Status != tc.status {
				t.Fatalf("status=%d body=%s", reply.Status, reply.Body)
			}
		})
	}
	if reply := i.call(t, "", "tools/list", "", `{}`, host("evil.example.test")); reply.Status != http.StatusForbidden || reply.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("unauthenticated wrong host: status=%d headers=%v", reply.Status, reply.Header)
	}
}

func TestMCPHTTPServesOnlyPOSTAtTheEndpoint(t *testing.T) {
	fx := newMCPFixture(t)
	planner := &mcpPlanner{}
	i := fx.start(t, mcpOptions{planner: planner})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions, "PROPFIND"} {
		req := mustRequest(t, method, mcpPublicURL)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "text/event-stream")
		reply := i.send(t, i.client(), req)
		if reply.Status != http.StatusMethodNotAllowed || reply.Header.Get("Allow") != http.MethodPost {
			t.Errorf("%s status=%d allow=%q", method, reply.Status, reply.Header.Get("Allow"))
		}
	}
	for _, path := range []string{"/", "/mcp/", "/MCP", "/mcp/extra", "/api/v1/topics", "/health", "/metrics", "/swagger/doc.json", "//mcp", "/mcp%2f"} {
		req := mustRequest(t, http.MethodPost, "https://mcp.example.test"+path)
		req.Header.Set("Authorization", "Bearer "+token)
		if reply := i.send(t, i.client(), req); reply.Status != http.StatusNotFound && reply.Status != http.StatusMovedPermanently {
			t.Errorf("POST %s status = %d", path, reply.Status)
		}
	}
	if planner.calls.Load() != 0 || len(fx.redisKeys()) != 0 {
		t.Fatalf("other methods and paths ran %d plans and wrote %v", planner.calls.Load(), fx.redisKeys())
	}
}

func TestMCPHTTPChargesIPAndTenantPerOperation(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	count := func(prefix string) (n int, last string) {
		for _, key := range fx.redisKeys() {
			if strings.HasPrefix(key, prefix) {
				n++
				last = fx.redis.HGet(key, "current")
			}
		}
		return n, last
	}
	for range 2 {
		if reply := i.call(t, token, "server/discover", "", `{}`); reply.Status != http.StatusOK {
			t.Fatalf("discover: %d %s", reply.Status, reply.Body)
		}
		if reply := i.call(t, token, "tools/list", "", `{}`); reply.Status != http.StatusOK {
			t.Fatalf("list: %d %s", reply.Status, reply.Body)
		}
	}
	if n, current := count("axiom:rate:"); n != 1 || current != "4" {
		t.Fatalf("IP buckets = %d, count %q, want one bucket at 4", n, current)
	}
	if n, _ := count("axiom:tenant-rate:v1:"); n != 0 {
		t.Fatalf("protocol traffic used the tenant budget")
	}
	if reply := toolCall(t, i, token, "list_topics", `{}`); reply.Status != http.StatusOK {
		t.Fatalf("tool: %d %s", reply.Status, reply.Body)
	}
	if n, current := count("axiom:rate:"); n != 1 || current != "5" {
		t.Fatalf("IP bucket after a tool = %d, %q", n, current)
	}
	if n, current := count("axiom:tenant-rate:v1:"); n != 1 || current != "1" {
		t.Fatalf("tenant bucket after one tool = %d, %q", n, current)
	}
	reply := toolCall(t, i, token, "list_topics", `{}`)
	if reply.Header.Get("X-RateLimit-Limit") != "1000" || reply.Header.Get("X-RateLimit-Remaining") == "" {
		t.Fatalf("admission headers = %v", reply.Header)
	}
	for _, key := range fx.redisKeys() {
		for _, secret := range []string{privateID, callerID, fx.issuer.issuer, token} {
			if strings.Contains(key, secret) {
				t.Fatalf("budget key %q contains %q", key, secret)
			}
		}
	}
}

func TestMCPHTTPEnforcesTheTenantAndIPBudgets(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{settings: map[string]string{"AXIOM_TENANT_QUOTA_MAX": "2", "RATE_LIMIT_MAX": "5"}})
	other := fx.start(t, mcpOptions{settings: map[string]string{"AXIOM_TENANT_QUOTA_MAX": "2", "RATE_LIMIT_MAX": "5"}})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, "graph:read")
	second := fx.issuer.token(t, mcpPublicURL, privateID, "another-caller", "graph:read")
	elsewhere := fx.issuer.token(t, mcpPublicURL, "another-tenant", callerID, "graph:read")
	if toolCall(t, i, token, "list_topics", `{}`).Status != http.StatusOK || toolCall(t, other, second, "list_topics", `{}`).Status != http.StatusOK {
		t.Fatal("the first two actions of a tenant must be admitted on either instance")
	}
	denied := toolCall(t, i, token, "list_topics", `{}`)
	if denied.Status != http.StatusTooManyRequests || !strings.Contains(denied.Body, `"error":"rate_limit_exceeded"`) ||
		denied.Header.Get("X-RateLimit-Limit") != "5" || denied.Header.Get("X-RateLimit-Remaining") != "2" ||
		denied.Header.Get("X-RateLimit-Reset") == "" {
		t.Fatalf("tenant denial: %d %v %s", denied.Status, denied.Header, denied.Body)
	}
	if retry, err := strconv.Atoi(denied.Header.Get("Retry-After")); err != nil || retry < 1 || retry > 60 {
		t.Fatalf("Retry-After = %q", denied.Header.Get("Retry-After"))
	}
	if again := toolCall(t, other, second, "list_topics", `{}`); again.Status != http.StatusTooManyRequests {
		t.Fatalf("the budget was not shared across instances: %d", again.Status)
	}
	// Discovery and listing never use the tenant budget, only the IP budget.
	if reply := i.call(t, token, "tools/list", "", `{}`); reply.Status != http.StatusOK {
		t.Fatalf("listing for a tenant over budget: %d", reply.Status)
	}
	// Four tool requests and one listing used the IP budget of five.
	ipDenied := i.call(t, elsewhere, "tools/list", "", `{}`)
	if ipDenied.Status != http.StatusTooManyRequests || ipDenied.Header.Get("X-RateLimit-Limit") != "" {
		t.Fatalf("IP denial: %d %v", ipDenied.Status, ipDenied.Header)
	}
	if got := toolCall(t, other, elsewhere, "list_topics", `{}`); got.Status != http.StatusTooManyRequests {
		t.Fatalf("a tool for another tenant bypassed the IP budget: %d", got.Status)
	}
}

func TestMCPHTTPFailsClosedWithoutRedis(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	fx.redis.SetError("ERR unavailable")
	planner := i.planner
	for _, reply := range []mcpReply{
		toolCall(t, i, token, "generate_study_plan", planArgs),
		i.call(t, token, "tools/list", "", `{}`),
	} {
		if reply.Status != http.StatusServiceUnavailable || reply.Header.Get("Retry-After") != "1" || !strings.Contains(reply.Body, `"error":"service_unavailable"`) {
			t.Fatalf("outage response: %d %v %s", reply.Status, reply.Header, reply.Body)
		}
	}
	if planner.calls.Load() != 0 {
		t.Fatal("a plan ran while admission storage was unavailable")
	}
	if meta := i.get(t, "/.well-known/oauth-protected-resource/mcp"); meta.Status != http.StatusOK {
		t.Fatalf("metadata during an outage: %d", meta.Status)
	}
	fx.redis.SetError("")
	if reply := toolCall(t, i, token, "generate_study_plan", planArgs); reply.Status != http.StatusOK {
		t.Fatalf("recovery status = %d", reply.Status)
	}
}

func TestMCPHTTPIPAdmissionUsesTheDirectPeerUnlessProxiesAreTrusted(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run("trusted="+strconv.FormatBool(trusted), func(t *testing.T) {
			settings := map[string]string{"RATE_LIMIT_MAX": "2"}
			if trusted {
				settings["TRUSTED_PROXIES"] = "127.0.0.1"
			}
			fx := newMCPFixture(t)
			i := fx.start(t, mcpOptions{settings: settings})
			token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
			forward := func(ip string) func(*http.Request) { return setHeader("X-Forwarded-For", ip) }
			statuses := make([]int, 0, 3)
			for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
				statuses = append(statuses, i.call(t, token, "tools/list", "", `{}`, forward(ip)).Status)
			}
			want := []int{200, 200, 429}
			if trusted {
				want = []int{200, 200, 200}
			}
			for n := range want {
				if statuses[n] != want[n] {
					t.Fatalf("statuses = %v, want %v", statuses, want)
				}
			}
			if trusted {
				for _, key := range fx.redisKeys() {
					if strings.Contains(key, "127.0.0.1") {
						t.Fatalf("a proxy address was used as the client: %v", fx.redisKeys())
					}
				}
			}
		})
	}
}

func TestMCPHTTPAndRESTShareIPBucketsBehindMappedProxyRanges(t *testing.T) {
	fx := newMCPFixture(t)
	settings := map[string]string{
		"TRUSTED_PROXIES": "::ffff:127.0.0.0/120,::ffff:192.0.2.0/120",
		"RATE_LIMIT_MAX":  "1",
	}
	remote := fx.start(t, mcpOptions{settings: settings})
	cfg := fx.config(t, settings)
	cfg.HTTPListener = config.HTTPListenerNetHTTP
	addr, _, _ := startWireApp(t, cfg, nil, nil)
	conn := addr.dial(t)
	apiToken := fx.issuer.token(t, mcpRESTAud, privateID, callerID, "graph:read")
	mcpToken := fx.issuer.token(t, mcpPublicURL, privateID, callerID, "graph:read")
	rest := func(chain string) int {
		headers := make(http.Header)
		headers.Set("Authorization", "Bearer "+apiToken)
		headers.Set("X-Forwarded-For", chain)
		res, _ := conn.exchange(t, http.MethodGet, "/api/v1/topics", "", headers)
		return res.StatusCode
	}
	list := func(chain string) int {
		return remote.call(t, mcpToken, "tools/list", "", `{}`, setHeader("X-Forwarded-For", chain)).Status
	}
	const first = "198.51.100.1, 203.0.113.1, 192.0.2.42"
	const second = "198.51.100.1, 203.0.113.2, 192.0.2.42"
	if got := rest(first); got != http.StatusOK {
		t.Fatalf("REST for first client = %d", got)
	}
	if got := list(first); got != http.StatusTooManyRequests {
		t.Errorf("MCP for first client = %d, want shared 429", got)
	}
	if got := list(second); got != http.StatusOK {
		t.Errorf("MCP for second client = %d", got)
	}
	if got := rest(second); got != http.StatusTooManyRequests {
		t.Errorf("REST for second client = %d, want shared 429", got)
	}
}

func TestMCPHTTPDoesNotLogRejectedMethods(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	const marker = "private-method-canary"
	req := httptest.NewRequestWithContext(t.Context(), "EXT-"+marker, mcpPublicURL, nil)
	rec := httptest.NewRecorder()
	i.app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if log := i.logs.String(); strings.Contains(log, marker) || !strings.Contains(log, `"method":"_OTHER"`) {
		t.Fatalf("access log = %s", log)
	}
}

func TestMCPHTTPLogsAndErrorsNeverContainIdentity(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{settings: map[string]string{"AXIOM_TENANT_QUOTA_MAX": "1"}})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, "graph:read")
	bodies := make([]string, 0, 10)
	for _, reply := range []mcpReply{
		toolCall(t, i, token, "list_topics", `{}`),
		toolCall(t, i, token, "list_topics", `{}`),
		toolCall(t, i, token, "generate_study_plan", planArgs),
		i.call(t, token[:len(token)-4]+"AAAA", "tools/list", "", `{}`),
		i.call(t, "", "tools/list", "", `{}`),
	} {
		bodies = append(bodies, reply.Body, reply.Header.Get("WWW-Authenticate"))
	}
	text := i.logs.String()
	if !strings.Contains(text, `"msg":"mcp.access"`) || !strings.Contains(text, `"tool":"list_topics"`) {
		t.Fatalf("access log is missing: %s", text)
	}
	signature := token[strings.LastIndex(token, ".")+1:]
	for _, secret := range []string{privateID, callerID, token, signature, "Bearer", "Authorization", fx.issuer.issuer} {
		if strings.Contains(text, secret) {
			t.Errorf("a log line contains %q", secret)
		}
	}
	for _, secret := range []string{privateID, callerID, token, signature, fx.issuer.issuer} {
		if strings.Contains(strings.Join(bodies, "\n"), secret) {
			t.Errorf("an error body or challenge contains %q", secret)
		}
	}
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
	}
}

func TestMCPHTTPContainsPanics(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	i.app.endpoint = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("panic-canary-" + privateID) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, mcpPublicURL, nil)
	rec := httptest.NewRecorder()
	i.app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "canary") || !strings.Contains(rec.Body.String(), `"error":"internal_error"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if text := i.logs.String(); strings.Contains(text, "canary") || !strings.Contains(text, `"msg":"panic.recovered"`) {
		t.Fatalf("panic log: %s", text)
	}
}

func TestMCPHTTPRequiresTLS12AndSpeaksHTTP2(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	if resp, err := i.client(mcpClientOptions{minTLS: tls.VersionTLS10, maxTLS: tls.VersionTLS11}).Do(mcpRequestFor(t, "tools/list", "", `{}`, token)); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a TLS 1.1 client connected")
	}
	if reply := i.send(t, i.client(mcpClientOptions{maxTLS: tls.VersionTLS12}), mcpRequestFor(t, "tools/list", "", `{}`, token)); reply.Status != http.StatusOK || reply.Proto != 1 {
		t.Fatalf("TLS 1.2 over HTTP/1.1: status=%d proto=%d", reply.Status, reply.Proto)
	}
	if reply := i.send(t, i.client(mcpClientOptions{http2: true}), mcpRequestFor(t, "tools/list", "", `{}`, token)); reply.Status != http.StatusOK || reply.Proto != 2 {
		t.Fatalf("HTTP/2: status=%d proto=%d", reply.Status, reply.Proto)
	}
	// A plaintext request to the TLS port never reaches the endpoint.
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", i.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("POST /mcp HTTP/1.1\r\nHost: mcp.example.test\r\nAuthorization: Bearer " + token + "\r\nContent-Length: 0\r\n\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("plaintext request status = %d", resp.StatusCode)
		}
	}
	if len(fx.redisKeys()) != 1 {
		t.Fatalf("budget keys = %v, want only the two admitted listing calls' IP bucket", fx.redisKeys())
	}
}

func TestMCPHTTPBoundsStalledBodies(t *testing.T) {
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{cfg: func(c *config.Config) { c.ReadTimeout = 300 * time.Millisecond }})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	conn, err := (&tls.Dialer{Config: &tls.Config{RootCAs: fx.roots, ServerName: "mcp.example.test"}}).DialContext(t.Context(), "tcp", i.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	request := "POST /mcp HTTP/1.1\r\nHost: mcp.example.test\r\nAuthorization: Bearer " + token + "\r\nContent-Type: application/json\r\n" +
		"Accept: application/json, text/event-stream\r\nMcp-Protocol-Version: " + mcpProtocol + "\r\nMcp-Method: tools/list\r\nContent-Length: 500\r\n\r\n{\"jsonrpc\""
	_, _ = conn.Write([]byte(request))
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("a stalled request body was never answered: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("stalled body status = %d", resp.StatusCode)
	}
}

func TestNewMCPHTTPRejectsUnsafeConstruction(t *testing.T) {
	fx := newMCPFixture(t)
	cfg := fx.config(t, nil)
	good := config.MCPHTTP{PublicURL: mcpPublicURL, Issuer: fx.issuer.issuer, Certificate: fx.cert, Verifier: fx.issuer.verifier(t, mcpPublicURL)}
	for name, mutate := range map[string]func(*config.Config, *config.MCPHTTP){
		"no tenant quota": func(c *config.Config, _ *config.MCPHTTP) { c.TenantQuota = nil },
		"no verifier":     func(_ *config.Config, r *config.MCPHTTP) { r.Verifier = nil },
		"no public URL":   func(_ *config.Config, r *config.MCPHTTP) { r.PublicURL = "" },
		"unreachable Redis": func(c *config.Config, _ *config.MCPHTTP) {
			c.Redis.URL = "redis://127.0.0.1:1"
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, r := cfg, good
			mutate(&c, &r)
			app, err := NewMCPHTTP(c, r, Dependencies{}, "test")
			if err == nil {
				_ = app.Close()
				t.Fatal("construction succeeded")
			}
		})
	}
}

func TestMCPHTTPRunFailsBeforeServingWhenTheAddressIsTaken(t *testing.T) {
	fx := newMCPFixture(t)
	busy, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	i := fx.start(t, mcpOptions{remote: func(r *config.MCPHTTP) { r.BindAddr = busy.Addr().String() }})
	if err := i.app.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("Run on a busy address = %v", err)
	}
}
