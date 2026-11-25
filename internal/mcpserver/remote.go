package mcpserver

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

// Scopes that authorize remote tool operations.
const (
	ScopeGraphRead    = "graph:read"
	ScopePlanGenerate = "plan:generate"
)

// Operation is a decoded protocol request that passed the envelope checks.
type Operation struct {
	// Method is the protocol method, one of the few the endpoint serves.
	Method string
	// Tool names the tool a tools/call request runs. It is empty otherwise.
	Tool string
	// Scopes lists the scopes that authorize the operation. One is enough.
	// The first is the scope to request when no scope matches.
	Scopes []string
}

// RunsTool reports whether the operation runs a tool and so uses tenant quota.
func (o Operation) RunsTool() bool { return o.Tool != "" }

// Gate guards a remote endpoint.
type Gate interface {
	// Authenticate verifies the caller before the request body is read. To
	// refuse the request, it writes the response and returns false.
	Authenticate(w http.ResponseWriter, r *http.Request) (*http.Request, bool)
	// Authorize checks the scopes and admission of a decoded operation. To
	// refuse the operation, it writes the response and returns false.
	Authorize(w http.ResponseWriter, r *http.Request, op Operation) bool
}

// RemoteOptions configures a handler that serves callers over the network.
type RemoteOptions struct {
	// Gate is required.
	Gate Gate
	// Origins lists the browser origins allowed to call the endpoint. The
	// zero value allows none, so only native clients without an Origin work.
	Origins OriginPolicy
	// RequestTimeout bounds each authorized request. Zero applies no bound.
	RequestTimeout time.Duration
	// ReadTimeout bounds reading each authorized request body. Zero applies no bound.
	ReadTimeout time.Duration
	// Lifecycle, if set, admits tool calls and cancels them when draining expires.
	Lifecycle *Lifecycle
}

// NewRemoteHandler constructs the stateless handler for a protected endpoint.
// Every request passes the origin policy and the gate before any tool runs.
// Responses are JSON unless the client prefers an event stream. Only discovery,
// tool listing and the four tools are served.
func NewRemoteHandler(cfg config.Config, curriculum *graph.Curriculum, planner service.Planner, logger *slog.Logger, version string, opts RemoteOptions) (http.Handler, error) {
	if opts.Gate == nil {
		return nil, errors.New("mcpserver: a remote handler requires a gate")
	}
	s := newServer(cfg, curriculum, planner, logger, version, mcp.ServerOptions{
		SupportedProtocolVersions: []string{streamableProtocolVersion},
		Capabilities:              &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}, nil)
	if opts.Lifecycle != nil {
		s.AddReceivingMiddleware(opts.Lifecycle.Middleware)
	}
	return newStreamableHandler(s, logger, streamableOptions{
		writeTimeout: cfg.WriteTimeout, readTimeout: opts.ReadTimeout, requestTimeout: opts.RequestTimeout,
		negotiate: true, origins: &opts.Origins, gate: opts.Gate,
	}), nil
}

// OriginPolicy is a set of exact browser origins.
type OriginPolicy struct{ allowed []string }

// NewOriginPolicy allows exactly the given origins, such as https://app.example.
func NewOriginPolicy(origins ...string) OriginPolicy {
	return OriginPolicy{allowed: slices.Clone(origins)}
}

// Allows reports whether the headers carry no Origin, as a native client sends,
// or exactly one Origin equal to a configured one. Repeated, empty, opaque and
// differently written values are refused.
func (p OriginPolicy) Allows(header http.Header) bool {
	values := header.Values("Origin")
	return len(values) == 0 || (len(values) == 1 && slices.Contains(p.allowed, values[0]))
}

var toolScopes = map[string]string{
	"list_topics":         ScopeGraphRead,
	"get_topic_path":      ScopeGraphRead,
	"get_cognitive_graph": ScopeGraphRead,
	"generate_study_plan": ScopePlanGenerate,
}

// classify maps a decoded method and tool name to the operation it needs, or
// to the JSON-RPC error that rejects it before any work.
func classify(method, tool string) (Operation, *jsonrpc.Error) {
	switch method {
	case "server/discover", "tools/list":
		return Operation{Method: method, Scopes: []string{ScopeGraphRead, ScopePlanGenerate}}, nil
	case "tools/call":
		if scope, known := toolScopes[tool]; known {
			return Operation{Method: method, Tool: tool, Scopes: []string{scope}}, nil
		}
		return Operation{}, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown tool"}
	}
	return Operation{}, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not available on this endpoint"}
}

// prefersEventStream reports whether the Accept header ranks the event stream
// above JSON. Equal preference, and any value that cannot be read, selects JSON.
func prefersEventStream(header http.Header) bool {
	stream, plain := -1.0, -1.0
	for _, value := range header.Values("Accept") {
		for _, item := range strings.Split(value, ",") {
			mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(item))
			if err != nil {
				continue
			}
			quality := 1.0
			if raw, ok := params["q"]; ok {
				if quality, err = strconv.ParseFloat(raw, 64); err != nil || quality < 0 || quality > 1 {
					continue
				}
			}
			switch mediaType {
			case "text/event-stream":
				stream = max(stream, quality)
			case "application/json":
				plain = max(plain, quality)
			}
		}
	}
	return stream > plain
}
