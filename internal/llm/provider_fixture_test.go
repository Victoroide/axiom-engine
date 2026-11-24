package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/test/fakebedrock"
)

const (
	fixtureModel = "local-planner"
	fixtureKey   = "dummy-provider-key"
	providerText = "private-provider-diagnostic"
	validPlan    = `{"items":[{"topic":"Recursion","priority":1,"prerequisite_chain":["Functions"],"explanation":"Practice recursion.","resources":[{"title":"Guide","url":"https://example.com/recursion","resource_type":"video"}]}]}`
)

var nativeProviders = []string{"aws-bedrock", "openai", "anthropic", "google-ai", "ollama", "openai-generic"}

type providerFixture struct {
	settings                config.LLM
	endpoint                string
	started                 chan time.Time
	disconnected            chan time.Time
	released                <-chan struct{}
	release                 context.CancelFunc
	mu                      sync.Mutex
	requests                []providerRequest
	mode                    string
	beforeStartNotification func()
}

type providerRequest struct{ body, headers, target string }

func newProviderFixture(t *testing.T, provider, mode string) *providerFixture {
	t.Helper()
	// Native clients read process environment on each call. Keep these tests serial.
	settings := config.LLM{Provider: provider, Model: fixtureModel}
	if provider != "aws-bedrock" && provider != "ollama" {
		settings.APIKey = fixtureKey
	}
	fixtureCtx, release := context.WithCancel(t.Context())
	f := &providerFixture{
		settings:     settings,
		started:      make(chan time.Time, 1),
		disconnected: make(chan time.Time, 1),
		released:     fixtureCtx.Done(),
		release:      release,
		mode:         mode,
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.serve(t, w, r)
	}))
	f.endpoint = s.URL
	t.Cleanup(func() {
		f.release()
		s.CloseClientConnections()
		s.Close()
	})
	switch provider {
	case "aws-bedrock":
		setBedrockEndpoint(t, s.URL, "1")
	case "anthropic":
		f.settings.BaseURL = s.URL
	case "google-ai":
		f.settings.BaseURL = s.URL + "/v1beta"
	default:
		f.settings.BaseURL = s.URL + "/v1"
	}
	return f
}

func (f *providerFixture) setMode(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mode = mode
}

func (f *providerFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *providerFixture) capturedRequests() []providerRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]providerRequest(nil), f.requests...)
}

func (f *providerFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	path, header, headerValue := "/v1/chat/completions", "Authorization", "Bearer "+f.settings.APIKey
	switch f.settings.Provider {
	case "aws-bedrock":
		path, headerValue = "/model/"+fixtureModel+"/converse", ""
	case "anthropic":
		path, header, headerValue = "/v1/messages", "X-API-Key", f.settings.APIKey
	case "google-ai":
		path, header, headerValue = "/v1beta/models/"+fixtureModel+":generateContent", "X-Goog-Api-Key", f.settings.APIKey
	case "ollama":
		headerValue = ""
	}
	if r.Method != http.MethodPost || r.URL.Path != path {
		t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, path)
		http.NotFound(w, r)
		return
	}
	if headerValue != "" && r.Header.Get(header) != headerValue {
		t.Errorf("request missing dummy credential in %s", header)
	}
	if f.settings.Provider == "aws-bedrock" && !strings.Contains(r.Header.Get(header), "Credential=dummy/") {
		t.Error("Converse request was not signed with the dummy credential")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read provider request: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	receivedAt := time.Now()
	var headers strings.Builder
	_ = r.Header.Write(&headers)
	f.mu.Lock()
	f.requests = append(f.requests, providerRequest{body: string(body), headers: headers.String(), target: r.URL.RequestURI()})
	mode, beforeStartNotification := f.mode, f.beforeStartNotification
	f.mu.Unlock()
	switch mode {
	case "hang":
		if beforeStartNotification != nil {
			beforeStartNotification()
		}
		select {
		case f.started <- receivedAt:
		default:
		}
		select {
		case <-r.Context().Done():
			select {
			case f.disconnected <- time.Now():
			default:
			}
		case <-f.released:
			return
		}
		return
	case "closed":
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack provider connection: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := conn.Close(); err != nil {
			t.Errorf("close provider connection: %v", err)
		}
		return
	case "unknown":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMultipleChoices)
		_, _ = io.WriteString(w, `{"message":"`+providerText+`"}`)
		return
	}
	if f.settings.Provider == "aws-bedrock" {
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		fakebedrock.NewHandler(mode).ServeHTTP(w, r)
		return
	}
	writeProviderResponse(w, f.settings.Provider, mode)
}

func writeProviderResponse(w http.ResponseWriter, provider, mode string) {
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	var googleCode, anthropicType, openAIType string
	switch mode {
	case "throttle":
		status, googleCode, anthropicType, openAIType = http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "rate_limit_error", "rate_limit_error"
	case "unauthorized":
		status, googleCode, anthropicType, openAIType = http.StatusUnauthorized, "UNAUTHENTICATED", "authentication_error", "invalid_request_error"
	case "denied":
		status, googleCode, anthropicType, openAIType = http.StatusForbidden, "PERMISSION_DENIED", "permission_error", "invalid_request_error"
	case "internal":
		status, googleCode, anthropicType, openAIType = http.StatusInternalServerError, "INTERNAL", "api_error", "server_error"
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		switch provider {
		case "anthropic":
			_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":%q}}`, anthropicType, providerText)
		case "google-ai":
			_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"status":%q,"message":%q}}`, status, googleCode, providerText)
		default:
			_, _ = fmt.Fprintf(w, `{"error":{"type":%q,"message":%q}}`, openAIType, providerText)
		}
		return
	}
	text := validPlan
	switch mode {
	case "empty":
		text = `{"items":[]}`
	case "garbage":
		text = "I cannot produce a study plan."
	case "incomplete":
		text = `{"items":[{"topic":"Recursion","priority":1,`
	case "malformed":
		_, _ = io.WriteString(w, `{"unexpected":true}`)
		return
	}
	finish, length := "stop", "length"
	switch provider {
	case "anthropic":
		finish, length = "end_turn", "max_tokens"
	case "google-ai":
		finish, length = "STOP", "MAX_TOKENS"
	}
	if mode == "max-tokens" {
		finish = length
	}
	_, _ = io.WriteString(w, completionResponse(provider, text, finish, mode == "no-content"))
}

func completionResponse(provider, text, finish string, empty bool) string {
	var content, envelope string
	switch provider {
	case "anthropic":
		content = fmt.Sprintf(`{"type":"text","text":%s}`, strconv.Quote(text))
		envelope = `{"id":"message","type":"message","role":"assistant","model":"local-planner","content":[%s],"stop_reason":%q,"usage":{"input_tokens":17,"output_tokens":23}}`
	case "google-ai":
		content = fmt.Sprintf(`{"index":0,"content":{"role":"model","parts":[{"text":%s}]},"finishReason":%q}`, strconv.Quote(text), finish)
		envelope = `{"candidates":[%s],"usageMetadata":{"promptTokenCount":17,"candidatesTokenCount":23,"totalTokenCount":40}}`
	default:
		content = fmt.Sprintf(`{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":%q}`, strconv.Quote(text), finish)
		envelope = `{"id":"completion","object":"chat.completion","created":1,"model":"local-planner","choices":[%s],"usage":{"prompt_tokens":17,"completion_tokens":23,"total_tokens":40}}`
	}
	if empty {
		content = ""
	}
	if provider == "anthropic" {
		return fmt.Sprintf(envelope, content, finish)
	}
	return fmt.Sprintf(envelope, content)
}
