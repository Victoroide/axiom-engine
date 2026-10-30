// Package fakebedrock provides a local Bedrock Converse endpoint for tests.
package fakebedrock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Server records Converse requests and serves deterministic responses.
// Its handler can also be served on a listener with a chosen address.
type Server struct {
	*httptest.Server
	mode         string
	mu           sync.Mutex
	bodies       []string
	disconnected chan struct{}
	once         sync.Once
}

// New starts a server in one of the response modes handled by ServeHTTP.
// Unknown modes panic.
// Call Close after use; hang requests must first be canceled by their client.
func New(mode string) *Server {
	s := NewHandler(mode)
	s.Server = httptest.NewServer(s)
	return s
}

// NewHandler returns a Converse handler without opening a listener.
// Unknown modes panic.
func NewHandler(mode string) *Server {
	switch mode {
	case "ok", "throttle", "denied", "hang", "garbage", "empty", "validation", "not-found", "timeout", "error", "internal", "unavailable", "malformed", "html", "max-tokens":
	default:
		panic(fmt.Sprintf("unknown Bedrock response mode: %q", mode))
	}
	return &Server{mode: mode, disconnected: make(chan struct{})}
}

// Count returns the number of accepted Converse requests.
func (s *Server) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

// Bodies returns an independent snapshot of the accepted request bodies.
func (s *Server) Bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// Disconnected closes when a hanging request's client cancels its connection.
func (s *Server) Disconnected() <-chan struct{} { return s.disconnected }

// ServeHTTP implements POST /model/{id}/converse for any nonempty model ID.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/model/") || !strings.HasSuffix(r.URL.Path, "/converse") || len(r.URL.Path) <= len("/model//converse") {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.bodies = append(s.bodies, string(body))
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch s.mode {
	case "hang":
		<-r.Context().Done()
		s.once.Do(func() { close(s.disconnected) })
		return
	case "throttle", "denied", "validation", "not-found", "timeout", "error", "internal", "unavailable":
		reply := map[string]struct {
			status int
			code   string
		}{
			"throttle":    {429, "ThrottlingException"},
			"denied":      {403, "AccessDeniedException"},
			"validation":  {400, "ValidationException"},
			"not-found":   {404, "ResourceNotFoundException"},
			"timeout":     {408, "ModelTimeoutException"},
			"error":       {424, "ModelErrorException"},
			"internal":    {500, "InternalServerException"},
			"unavailable": {503, "ServiceUnavailableException"},
		}[s.mode]
		w.Header().Set("x-amzn-ErrorType", reply.code+":http://internal.amazon.com/coral/com.amazon.bedrock/")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, `{"message":"timeout connection reset validationerror finish reason"}`)
		return
	case "malformed":
		_, _ = io.WriteString(w, `{}`)
		return
	case "html":
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html>timeout validationerror</html>`)
		return
	}
	text := `{"items":[{"topic":"Recursion","priority":1,"prerequisite_chain":["Functions"],"explanation":"Practice recursion.","resources":[{"title":"Guide","url":"https://example.com/recursion","resource_type":"video"}]}]}`
	stopReason := "end_turn"
	switch s.mode {
	case "garbage":
		text = "I cannot produce a study plan."
	case "empty":
		text = `{"items":[]}`
	case "max-tokens":
		text = strings.TrimSuffix(text, "]}")
		stopReason = "max_tokens"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"output":     map[string]any{"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"text": text}}}},
		"stopReason": stopReason,
		"usage":      map[string]int{"inputTokens": 100, "outputTokens": 50, "totalTokens": 150},
		"metrics":    map[string]int{"latencyMs": 1},
	})
}
