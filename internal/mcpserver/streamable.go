package mcpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

const (
	streamableProtocolVersion = "2026-07-28"
	protocolVersionHeader     = "Mcp-Protocol-Version"
	methodHeader              = "Mcp-Method"
	nameHeader                = "Mcp-Name"
	streamableIntegerID       = "_"
	streamableEncodedID       = `"` + streamableIntegerID + `"`
)

// NewStreamableHTTPHandler constructs a stateless July 2026 MCP handler. Reuse
// the handler across requests so they share the tool registry and reasoning limits.
// With a configured write timeout, response writer wrappers must expose
// SetWriteDeadline or Unwrap. The handler rejects requests if setup fails.
func NewStreamableHTTPHandler(cfg config.Config, g *graph.InMemoryGraph, planner service.Planner, logger *slog.Logger, version string, jsonResponse bool) http.Handler {
	return NewStreamableHTTPHandlerWithCurriculum(cfg, graph.StaticCurriculum(g), planner, logger, version, jsonResponse)
}

// NewStreamableHTTPHandlerWithCurriculum serves tools on the current snapshot.
func NewStreamableHTTPHandlerWithCurriculum(cfg config.Config, curriculum *graph.Curriculum, planner service.Planner, logger *slog.Logger, version string, jsonResponse bool) http.Handler {
	s := newServer(cfg, curriculum, planner, logger, version, mcp.ServerOptions{
		SupportedProtocolVersions: []string{streamableProtocolVersion},
		Capabilities:              &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	}, nil)
	return streamableHTTPHandler(s, logger, cfg.WriteTimeout, jsonResponse)
}

func streamableHTTPHandler(s *mcp.Server, logger *slog.Logger, writeTimeout time.Duration, jsonResponse bool) http.Handler {
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 jsonResponse,
		Logger:                       logger,
		PropagateRequestCancellation: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validStreamableOrigin(r) {
			http.Error(w, "Forbidden: invalid Origin", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if writeTimeout > 0 {
			if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				http.Error(w, "Unable to set MCP response write deadline", http.StatusInternalServerError)
				return
			}
		}
		body := readStreamableBody(w, r)
		if body == nil {
			return
		}
		request, ok := decodeStreamableEnvelope(body)
		if !ok {
			http.Error(w, "malformed MCP request", http.StatusBadRequest)
			return
		}
		rewriteID, validID := streamableID(request.ID)
		if !validID {
			writeStreamableError(w, nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "request ID must be a string or integer"})
			return
		}
		name, failure := validateStreamableHeaders(r.Header, request)
		if failure != nil {
			writeStreamableError(w, request.ID, failure)
			return
		}
		r = r.Clone(r.Context())
		// The July transport ignores resume cursors. Older bindings reject this
		// header on POST, which the SDK also does on its July path.
		r.Header.Del("Last-Event-ID")
		if name != "" {
			r.Header.Set(nameHeader, name)
		}
		if rewriteID {
			id, _ := jsonrpc.MakeID(streamableIntegerID)
			var err error
			body, err = jsonrpc.EncodeMessage(&jsonrpc.Request{ID: id, Method: request.Method, Params: request.Params})
			if err != nil {
				http.Error(w, "invalid MCP request", http.StatusBadRequest)
				return
			}
			w = &streamableResponseIDWriter{ResponseWriter: w, id: request.ID, subscription: request.Method == "subscriptions/listen"}
		}
		r.ContentLength = int64(len(body))
		r.Body = io.NopCloser(bytes.NewReader(body))
		sdk.ServeHTTP(w, r)
	})
}

func validStreamableOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	scheme, defaultPort := "http", ":80"
	if r.TLS != nil {
		scheme, defaultPort = "https", ":443"
	}
	return len(origins) == 1 && strings.EqualFold(origins[0], scheme+"://"+strings.TrimSuffix(r.Host, defaultPort))
}

func readStreamableBody(w http.ResponseWriter, r *http.Request) []byte {
	if r.Body == nil {
		http.Error(w, "POST requires a body", http.StatusBadRequest)
		return nil
	}
	reader := http.MaxBytesReader(w, r.Body, mcp.DefaultMaxRequestBodyBytes)
	body, err := io.ReadAll(reader)
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			http.Error(w, "request body exceeds the MCP size limit", http.StatusRequestEntityTooLarge)
			return nil
		}
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return nil
	}
	_ = reader.Close()
	return body
}

// The envelope check rejects trailing messages and retains the original ID
// before SDK decoding can convert a JSON number to float64.
type streamableEnvelope struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

func decodeStreamableEnvelope(body []byte) (streamableEnvelope, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return streamableEnvelope{}, false
	}
	var version, method string
	if json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" ||
		json.Unmarshal(fields["method"], &method) != nil || method == "" {
		return streamableEnvelope{}, false
	}
	return streamableEnvelope{ID: fields["id"], Method: method, Params: fields["params"]}, true
}

func validateStreamableHeaders(header http.Header, request streamableEnvelope) (string, *jsonrpc.Error) {
	versions := header.Values(protocolVersionHeader)
	if len(versions) != 1 || versions[0] == "" {
		return "", streamableHeaderMismatch(protocolVersionHeader)
	}
	var params, meta map[string]json.RawMessage
	if len(request.Params) != 0 && json.Unmarshal(request.Params, &params) != nil {
		return "", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid MCP request parameters"}
	}
	if raw := params["_meta"]; len(raw) != 0 && json.Unmarshal(raw, &meta) != nil {
		return "", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid MCP request metadata"}
	}
	if declared := meta[mcp.MetaKeyProtocolVersion]; len(request.ID) != 0 || len(declared) != 0 {
		var version string
		if json.Unmarshal(declared, &version) != nil || version == "" {
			return "", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "missing or invalid body protocol version"}
		}
		if version != versions[0] {
			return "", streamableHeaderMismatch(protocolVersionHeader)
		}
	}
	if methods := header.Values(methodHeader); len(methods) != 1 || methods[0] != request.Method {
		return "", streamableHeaderMismatch(methodHeader)
	}
	var name string
	switch request.Method {
	case "tools/call", "prompts/get", "resources/read":
		rawName := params["name"]
		if request.Method == "resources/read" {
			rawName = params["uri"]
		}
		names := header.Values(nameHeader)
		if len(names) != 1 || json.Unmarshal(rawName, &name) != nil || name == "" || !matchesStreamableName(names[0], name) {
			return "", streamableHeaderMismatch(nameHeader)
		}
	}
	if versions[0] != streamableProtocolVersion {
		data, _ := json.Marshal(mcp.UnsupportedProtocolVersionData{
			Supported: []string{streamableProtocolVersion}, Requested: versions[0],
		})
		return "", &jsonrpc.Error{Code: mcp.CodeUnsupportedProtocolVersion, Message: "unsupported protocol version", Data: data}
	}
	return name, nil
}

func matchesStreamableName(header, name string) bool {
	if value, ok := strings.CutPrefix(header, "=?base64?"); ok {
		if encoded, ok := strings.CutSuffix(value, "?="); ok {
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			return err == nil && string(decoded) == name
		}
	}
	return header == name
}

func streamableHeaderMismatch(header string) *jsonrpc.Error {
	return &jsonrpc.Error{Code: mcp.CodeHeaderMismatch, Message: header + " must appear once and match the request body"}
}

func writeStreamableError(w http.ResponseWriter, id json.RawMessage, failure *jsonrpc.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Error   *jsonrpc.Error  `json:"error"`
	}{"2.0", id, failure})
}

func streamableID(id json.RawMessage) (rewrite, valid bool) {
	if len(id) == 0 || id[0] == '"' {
		return false, true
	}
	if id[0] != '-' && (id[0] < '0' || id[0] > '9') {
		return false, false
	}
	if bytes.ContainsAny(id, ".eE") {
		integer := isStreamableInteger(string(id))
		return integer, integer
	}
	n, err := strconv.ParseInt(string(id), 10, 64)
	return err != nil || n < -(1<<53) || n > 1<<53, true
}

// isStreamableInteger tests a valid JSON number using its minimum exponent for
// integrality. Counting digits keeps work bounded by the encoded number size.
func isStreamableInteger(number string) bool {
	mantissa, exponent := number, "0"
	if i := strings.IndexAny(number, "eE"); i >= 0 {
		mantissa, exponent = number[:i], number[i+1:]
	}
	whole, fraction, _ := strings.Cut(strings.TrimPrefix(mantissa, "-"), ".")
	minimum := len(strings.TrimRight(fraction, "0"))
	if minimum == 0 {
		digits := strings.TrimRight(whole, "0")
		if digits == "" {
			return true
		}
		minimum = len(digits) - len(whole)
	}
	value, err := strconv.ParseInt(exponent, 10, 64)
	if err != nil {
		// A valid exponent outside int64 exceeds the digit count in either direction.
		return exponent[0] != '-'
	}
	return value >= int64(minimum)
}

// The SDK writes each JSON response or SSE message in one call. Replace its
// synthetic ID in the response envelope and protocol subscription metadata.
type streamableResponseIDWriter struct {
	http.ResponseWriter
	id           json.RawMessage
	subscription bool
}

func (w *streamableResponseIDWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *streamableResponseIDWriter) Write(p []byte) (int, error) {
	const prefix = "event: message\ndata: "
	payload, event := bytes.CutPrefix(p, []byte(prefix))
	if event {
		payload = bytes.TrimSuffix(payload, []byte("\n\n"))
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return w.ResponseWriter.Write(p)
	}
	changed := string(message["id"]) == streamableEncodedID
	if changed {
		message["id"] = w.id
	}
	if w.subscription {
		restored, err := restoreStreamableSubscriptionID(message, w.id)
		if err != nil {
			return 0, err
		}
		changed = changed || restored
	}
	if !changed {
		return w.ResponseWriter.Write(p)
	}
	data, err := json.Marshal(message)
	if err != nil {
		return 0, err
	}
	if event {
		data = append(append([]byte(prefix), data...), '\n', '\n')
	}
	n, err := w.ResponseWriter.Write(data)
	if err != nil {
		return 0, err
	}
	if n != len(data) {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

func restoreStreamableSubscriptionID(message map[string]json.RawMessage, id json.RawMessage) (bool, error) {
	field := "result"
	if len(message["method"]) != 0 {
		field = "params"
	}
	var payload, meta map[string]json.RawMessage
	if len(message[field]) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(message[field], &payload); err != nil {
		return false, err
	}
	if len(payload["_meta"]) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(payload["_meta"], &meta); err != nil {
		return false, err
	}
	if string(meta[mcp.MetaKeySubscriptionID]) != streamableEncodedID {
		return false, nil
	}
	meta[mcp.MetaKeySubscriptionID] = id
	var err error
	payload["_meta"], err = json.Marshal(meta)
	if err != nil {
		return false, err
	}
	message[field], err = json.Marshal(payload)
	return err == nil, err
}
