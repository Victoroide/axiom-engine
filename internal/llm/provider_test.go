package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestAdapterUsesConfiguredOpenAIEndpoint(t *testing.T) {
	for _, provider := range []string{"openai", "ollama", "openai-generic"} {
		t.Run(provider, func(t *testing.T) {
			for _, status := range []int{200, 429, 500} {
				t.Run(http.StatusText(status), func(t *testing.T) {
					var calls atomic.Int64
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
							t.Errorf("request = %s %s", r.Method, r.URL.Path)
						}
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if body["model"] != "local-planner" || body["max_tokens"] != float64(2048) || body["temperature"] != 0.1 {
							t.Errorf("request settings = %v", body)
						}
						if r.Header.Get("Authorization") != "Bearer test-key" {
							t.Errorf("authorization missing")
						}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(status)
						if status != 200 {
							_, _ = w.Write([]byte(`{"error":{"message":"unavailable","type":"server_error"}}`))
							return
						}
						content := `{"items":[{"topic":"Recursion","priority":1,"prerequisite_chain":["Functions"],"explanation":"Practice recursion.","resources":[]}]}`
						_ = json.NewEncoder(w).Encode(map[string]any{"id": "completion-1", "object": "chat.completion", "created": 1, "model": "local-planner", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 17, "completion_tokens": 23, "total_tokens": 40}})
					}))
					defer s.Close()
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					items, err := New(config.LLM{Provider: provider, Model: "local-planner", BaseURL: s.URL + "/v1", APIKey: "test-key"}).GenerateTopic(ctx, topicRequest())
					var want error
					switch status {
					case 429:
						want = service.ErrThrottled
					case 500:
						want = service.ErrUpstreamUnavailable
					}
					if !errors.Is(err, want) || (status == 200 && (len(items) != 1 || items[0].Topic != "Recursion")) || calls.Load() != 1 {
						t.Fatalf("items=%v error=%v calls=%d", items, err, calls.Load())
					}
				})
			}
		})
	}
}
