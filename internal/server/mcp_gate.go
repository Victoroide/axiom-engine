package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/net/http/httpguts"

	"github.com/Victoroide/axiom-engine/internal/api"
	"github.com/Victoroide/axiom-engine/internal/auth"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/mcpserver"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type mcpPrincipalKey struct{}
type mcpRequestKey struct{}

// mcpRequest carries the operation that the gate authorized, for the access
// log. It holds only protocol method and tool names from a fixed list.
type mcpRequest struct{ method, tool string }

// mcpGate authenticates remote callers and admits their operations. It is
// the only way a request reaches a tool.
type mcpGate struct {
	verifier  *auth.Verifier
	admission *RedisAdmission
	proxies   proxyPolicy
	metadata  string
}

var _ mcpserver.Gate = (*mcpGate)(nil)

// Authenticate verifies a bearer token issued for this resource. Missing
// credentials and invalid tokens receive a challenge that points to the
// protected resource metadata.
func (g *mcpGate) Authenticate(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	token, malformed := bearerToken(r.Header)
	if malformed {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_request"`)
		writeMCPJSON(w, http.StatusBadRequest, domain.ErrorResponse{Error: "invalid_request", Details: "malformed access token credentials"})
		return nil, false
	}
	if token == "" {
		g.unauthorized(w, "")
		return nil, false
	}
	principal, err := g.verifier.Verify(r.Context(), token)
	if err != nil {
		if ctx := r.Context(); ctx.Err() != nil {
			writeMCPCancellation(w, ctx)
		} else {
			g.unauthorized(w, "invalid_token")
		}
		return nil, false
	}
	return r.WithContext(context.WithValue(r.Context(), mcpPrincipalKey{}, principal)), true
}

// Authorize requires one scope of the operation, then charges the caller's IP
// and, for a tool invocation, exactly one unit of the shared issuer and tenant
// budget. Protocol discovery and listing use only the IP budget.
func (g *mcpGate) Authorize(w http.ResponseWriter, r *http.Request, op mcpserver.Operation) bool {
	ctx := r.Context()
	principal, ok := ctx.Value(mcpPrincipalKey{}).(auth.Principal)
	if !ok {
		g.unauthorized(w, "")
		return false
	}
	if request, ok := ctx.Value(mcpRequestKey{}).(*mcpRequest); ok {
		request.method, request.tool = op.Method, op.Tool
	}
	if !slices.ContainsFunc(op.Scopes, principal.HasScope) {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+op.Scopes[0]+`", resource_metadata="`+g.metadata+`"`)
		writeMCPJSON(w, http.StatusForbidden, domain.ErrorResponse{Error: "insufficient_scope", Details: "access token lacks the required scope"})
		return false
	}
	ip := g.proxies.clientIP(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
	var decision AdmissionDecision
	if op.RunsTool() {
		decision = g.admission.Admit(ctx, ip, principal)
	} else {
		decision = g.admission.AdmitIP(ctx, ip)
	}
	decision.setHeaders(w.Header().Set)
	switch decision.Status {
	case http.StatusTooManyRequests:
		writeMCPJSON(w, http.StatusTooManyRequests, domain.ErrorResponse{Error: "rate_limit_exceeded", Details: "too many requests"})
		return false
	case http.StatusServiceUnavailable:
		writeMCPJSON(w, http.StatusServiceUnavailable, domain.ErrorResponse{Error: "service_unavailable", Details: "rate limit storage temporarily unavailable"})
		return false
	}
	return true
}

func (g *mcpGate) unauthorized(w http.ResponseWriter, code string) {
	challenge := `Bearer resource_metadata="` + g.metadata + `"`
	if code != "" {
		challenge = `Bearer error="` + code + `", resource_metadata="` + g.metadata + `"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	writeMCPJSON(w, http.StatusUnauthorized, domain.ErrorResponse{Error: "unauthorized", Details: "missing or invalid access token"})
}

// bearerToken distinguishes an absent Bearer credential from malformed headers.
func bearerToken(header http.Header) (token string, malformed bool) {
	values := header.Values("Authorization")
	if len(header.Values("X-Internal-Token")) != 0 || len(values) > 1 {
		return "", true
	}
	if len(values) == 0 {
		return "", false
	}
	scheme, credentials, _ := strings.Cut(values[0], " ")
	if scheme == "" || strings.ContainsAny(scheme, ",\t") {
		return "", true
	}
	if !strings.EqualFold(scheme, "Bearer") {
		// Other schemes can have comma-separated parameters in a single field.
		// A comma that separates different credentials still makes it malformed.
		return "", strings.Contains(credentials, ",") && !validAuthParams(credentials)
	}
	token = strings.TrimLeft(credentials, " ")
	if token == "" || strings.ContainsAny(token, ", \t") {
		return "", true
	}
	return token, false
}

// validAuthParams checks that a comma-separated credential is a single
// auth-param list under RFC 9110 §11.2.
func validAuthParams(params string) bool {
	params = strings.TrimLeft(params, " ,\t")
	if params == "" {
		return false
	}
	for params != "" {
		name, value, found := strings.Cut(params, "=")
		if !found || !httpguts.ValidHeaderFieldName(strings.TrimRight(name, " \t")) {
			return false
		}
		rest, ok := afterAuthParamValue(strings.TrimLeft(value, " \t"))
		rest = strings.TrimLeft(rest, " \t")
		if !ok || (rest != "" && rest[0] != ',') {
			return false
		}
		params = strings.TrimLeft(rest, " ,\t")
	}
	return true
}

func afterAuthParamValue(value string) (string, bool) {
	if !strings.HasPrefix(value, `"`) {
		token, _, _ := strings.Cut(value, ",")
		return value[len(token):], httpguts.ValidHeaderFieldName(strings.TrimRight(token, " \t"))
	}
	// A quoted-string can be empty. Escapes consume the next byte so a
	// quoted quote or comma cannot end the current parameter.
	for i := 1; i < len(value); i++ {
		switch value[i] {
		case '\\':
			i++
		case '"':
			return value[i+1:], true
		}
	}
	return "", false
}

func writeMCPCancellation(w http.ResponseWriter, ctx context.Context) {
	failure := service.ClassifyError(ctx.Err(), ctx.Err() == context.Canceled && !api.ClientDisconnected(ctx))
	if failure.RetryAfter > 0 {
		w.Header().Set("Retry-After", "1")
	}
	writeMCPJSON(w, failure.Status, failure.Response)
}

func writeMCPJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
