package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/mcpserver"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

const (
	mcpAllowedHeaders = "Authorization, Content-Type, Accept, Mcp-Protocol-Version, Mcp-Method, Mcp-Name"
	mcpExposedHeaders = "WWW-Authenticate, Retry-After"
)

// MCPHTTP serves the protected remote MCP endpoint and its resource metadata.
// It is a separate listener from the HTTP API and shares only the verified
// identity settings and the Redis request budgets.
type MCPHTTP struct {
	cfg       config.Config
	remote    config.MCPHTTP
	logger    *slog.Logger
	admission *RedisAdmission
	lifecycle *mcpserver.Lifecycle
	proxies   proxyPolicy
	origins   mcpserver.OriginPolicy
	endpoint  http.Handler
	metadata  []byte
	authority string
}

// NewMCPHTTP constructs the remote endpoint without opening a listener. It
// checks the shared request storage, so it fails before anything is served if
// Redis, the tenant quota or the verifier is not usable.
func NewMCPHTTP(cfg config.Config, remote config.MCPHTTP, deps Dependencies, version string) (*MCPHTTP, error) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.TenantQuota == nil || remote.Verifier == nil || remote.PublicURL == "" {
		return nil, errors.New("remote MCP requires a tenant quota, a verifier and a public URL")
	}
	admission, err := NewRedisAdmission(cfg, logger)
	if err != nil {
		return nil, err
	}
	m := &MCPHTTP{
		cfg: cfg, remote: remote, logger: logger, admission: admission,
		lifecycle: mcpserver.NewLifecycle(context.Background()),
		proxies:   newProxyPolicy(cfg.TrustedProxies),
		origins:   mcpserver.NewOriginPolicy(remote.AllowedOrigins...),
		authority: strings.ToLower(remote.Authority()),
	}
	gate := &mcpGate{verifier: remote.Verifier, admission: admission, proxies: m.proxies, metadata: remote.MetadataURL()}
	curriculum := deps.Curriculum
	if curriculum == nil {
		curriculum = graph.StaticCurriculum(deps.Graph)
	}
	m.endpoint, err = mcpserver.NewRemoteHandler(cfg, curriculum, deps.Planner, logger, version, mcpserver.RemoteOptions{
		Gate: gate, Origins: m.origins, RequestTimeout: cfg.RequestTimeout, ReadTimeout: cfg.ReadTimeout, Lifecycle: m.lifecycle,
	})
	if err != nil {
		_ = admission.Close()
		return nil, err
	}
	m.metadata, err = json.Marshal(oauthex.ProtectedResourceMetadata{
		Resource:               remote.PublicURL,
		AuthorizationServers:   []string{remote.Issuer},
		ScopesSupported:        []string{mcpserver.ScopeGraphRead, mcpserver.ScopePlanGenerate},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "AxiomEngine tools",
	})
	if err != nil {
		_ = admission.Close()
		return nil, err
	}
	return m, nil
}

// Handler returns the endpoint's HTTP handler. Serve wraps it with TLS.
func (m *MCPHTTP) Handler() http.Handler { return http.HandlerFunc(m.serveHTTP) }

// Close releases the shared request storage.
func (m *MCPHTTP) Close() error { return m.admission.Close() }

type mcpResponse struct {
	http.ResponseWriter
	status int
}

func (w *mcpResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *mcpResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *mcpResponse) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

// Unwrap lets the handler set deadlines on the underlying connection.
func (w *mcpResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (m *MCPHTTP) serveHTTP(rw http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := uuid.NewString()
	w := &mcpResponse{ResponseWriter: rw}
	info := &mcpRequest{}
	route := "unmatched"
	ctx, method, ip := r.Context(), r.Method, m.proxies.clientIP(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
	defer func() {
		if recovered := recover(); recovered != nil {
			if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(recovered)
			}
			telemetry.LogRequestPanic(ctx, m.logger, "panic.recovered", id)
			if w.status == 0 {
				writeMCPJSON(w, http.StatusInternalServerError, domain.ErrorResponse{Error: "internal_error", Details: "Internal Server Error"})
			}
		}
		m.logger.InfoContext(ctx, "mcp.access", mcpAccess(id, method, route, w.status, time.Since(start), ip, info)...)
	}()
	h := w.Header()
	h.Set("X-Request-ID", id)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Strict-Transport-Security", "max-age=31536000")
	if !m.hostAllowed(r.Host) {
		writeMCPJSON(w, http.StatusForbidden, domain.ErrorResponse{Error: "forbidden", Details: "unexpected host"})
		return
	}
	if !m.origins.Allows(r.Header) {
		writeMCPJSON(w, http.StatusForbidden, domain.ErrorResponse{Error: "forbidden", Details: "unexpected origin"})
		return
	}
	// Metadata is cacheable, and the allowed origin changes its headers.
	h.Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if origin != "" {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", mcpExposedHeaders)
	}
	switch r.URL.Path {
	case config.MCPEndpointPath:
		route = config.MCPEndpointPath
		if origin != "" && r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			m.preflight(w, http.MethodPost)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), mcpRequestKey{}, info))
		m.endpoint.ServeHTTP(w, r)
	case config.MCPMetadataPath:
		route = "metadata"
		switch {
		case origin != "" && r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "":
			m.preflight(w, "GET, HEAD")
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			h.Set("Content-Type", "application/json")
			h.Set("Cache-Control", "public, max-age=300")
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = w.Write(m.metadata)
			}
		default:
			h.Set("Allow", "GET, HEAD, OPTIONS")
			writeMCPJSON(w, http.StatusMethodNotAllowed, domain.ErrorResponse{Error: "method_not_allowed", Details: "Method Not Allowed"})
		}
	default:
		writeMCPJSON(w, http.StatusNotFound, domain.ErrorResponse{Error: "not_found", Details: "Not Found"})
	}
}

// preflight answers a browser's permission check. It names what an allowed
// origin may send and never reaches authentication or a tool.
func (m *MCPHTTP) preflight(w http.ResponseWriter, methods string) {
	h := w.Header()
	h.Set("Access-Control-Allow-Methods", methods)
	h.Set("Access-Control-Allow-Headers", mcpAllowedHeaders)
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// hostAllowed accepts only the configured public authority, so a request
// reaching the listener under another name, such as through DNS rebinding, is
// refused. The default HTTPS port may be written or omitted.
func (m *MCPHTTP) hostAllowed(host string) bool {
	host = strings.ToLower(host)
	return host == m.authority || (!strings.Contains(m.authority, ":") && host == m.authority+":443") ||
		(strings.HasPrefix(m.authority, "[") && strings.HasSuffix(m.authority, "]") && host == m.authority+":443")
}

func mcpAccess(id, method, route string, status int, elapsed time.Duration, ip string, info *mcpRequest) []any {
	attrs := []any{"request_id", id, "method", traceMethod(method), "path", route, "status", status, "duration_ms", float64(elapsed.Microseconds()) / 1000, "ip", ip}
	if info.method != "" {
		attrs = append(attrs, "rpc_method", info.method)
	}
	if info.tool != "" {
		attrs = append(attrs, "tool", info.tool)
	}
	return attrs
}
