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

// New starts a server in ok, throttle, denied, hang, garbage or empty mode.
// Call Close after use; hang requests must first be canceled by their client.
func New(mode string) *Server {
	switch mode {
	case "ok", "throttle", "denied", "hang", "garbage", "empty":
	default:
		panic(fmt.Sprintf("unknown Bedrock response mode: %q", mode))
	}
	s := &Server{mode: mode, disconnected: make(chan struct{})}
	s.Server = httptest.NewServer(s)
	return s
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
	case "throttle":
		w.Header().Set("x-amzn-ErrorType", "ThrottlingException:http://internal.amazon.com/coral/com.amazon.bedrock/")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"Too many requests, please wait before trying again."}`)
		return
	case "denied":
		w.Header().Set("x-amzn-ErrorType", "AccessDeniedException:http://internal.amazon.com/coral/com.amazon.bedrock/")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"Access to the specified model is denied."}`)
		return
	}
	text := `{"student_id":"s","course_id":"c","items":[{"topic":"Recursion","priority":1,"prerequisite_chain":["Functions"],"explanation":"Practice recursion.","resources":[{"title":"Guide","url":"https://example.com/recursion","resource_type":"video"}]}]}`
	switch s.mode {
	case "garbage":
		text = "I cannot produce a study plan."
	case "empty":
		text = `{"student_id":"s","course_id":"c","items":[]}`
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"output":     map[string]any{"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"text": text}}}},
		"stopReason": "end_turn",
		"usage":      map[string]int{"inputTokens": 100, "outputTokens": 50, "totalTokens": 150},
		"metrics":    map[string]int{"latencyMs": 1},
	})
}
