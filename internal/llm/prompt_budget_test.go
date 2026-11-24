package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestPromptBudgetProviderRequests(t *testing.T) {
	for _, tc := range []struct {
		provider, selection string
		capBytes, edges     int
	}{
		{"aws-bedrock", "amazon.nova-micro-v1:0", 16_384, 60},
		{"openai", "gpt-4.1-mini", 16_384, 60},
		{"anthropic", "claude-haiku-4-5", 16_384, 60},
		{"google-ai", "gemini-2.5-flash", 16_384, 60},
		{"ollama", "mistral:v0.3", 4480, 6},
		{"openai-generic", "meta-llama/Llama-3.1-8B-Instruct", 16_384, 60},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			env := map[string]string{
				"AXIOM_AUTH_DISABLED": "true", "LLM_PROVIDER": tc.provider, "LLM_MODEL": tc.selection,
				"OPENAI_API_KEY": "fixture-key", "ANTHROPIC_API_KEY": "fixture-key", "GOOGLE_API_KEY": "fixture-key",
			}
			var baseURL string
			var bodies func() []string
			if tc.provider == "aws-bedrock" {
				bodies = bedrockEndpoint(t, "ok").Bodies
			} else {
				baseURL, bodies = promptFixtureEndpoint(t, tc.provider, "Recursion")
				if tc.provider == "openai-generic" {
					env["LLM_BASE_URL"] = baseURL
				}
			}
			cfg, err := config.Load(func(key string) string { return env[key] })
			if err != nil {
				t.Fatal(err)
			}
			if cfg.PromptBudget.MaxTopicBytes != tc.capBytes {
				t.Fatalf("topic budget = %d bytes, want %d", cfg.PromptBudget.MaxTopicBytes, tc.capBytes)
			}
			// Direct the adapter to the local endpoint for each fixture.
			cfg.LLM.BaseURL = baseURL
			planner := New(cfg.LLM)
			edges := providerPromptEdges(t, tc.capBytes, tc.edges)
			req := domain.AdaptivePlanRequest{FailedTopics: []string{"Recursion"}, VARKProfile: "visual"}
			plan, err := generatePromptFixture(t, planner, cfg.PromptBudget, edges, req)
			if err != nil || len(plan.Items) != 1 || plan.Items[0].Topic != "Recursion" || plan.Items[0].Priority != 1 {
				t.Fatalf("at-cap plan = %+v, error = %v", plan, err)
			}
			got := bodies()
			if len(got) != 1 {
				t.Fatalf("at-cap requests = %d, want 1", len(got))
			}
			text := fixturePromptText(t, tc.provider, got[0])
			if tc.provider == "ollama" {
				checkMistralContext(t, text, "d6a4b4c903fa88973ebfc6213b98dc662321f9d6408883cccbf5c76d3eeec892", 1083)
			}
			for _, edge := range edges {
				if !strings.Contains(text, edge.Subject+" -[depends_on]-> Recursion\n") {
					t.Fatal("render omitted a curriculum tuple")
				}
			}
			if len(got[0]) > tc.capBytes {
				t.Fatalf("at-cap JSON payload = %d bytes, budget = %d", len(got[0]), tc.capBytes)
			}
			t.Logf("%s: %d accounted input bytes, %d rendered UTF-8 bytes, %d JSON payload bytes", tc.selection, tc.capBytes, len(text), len(got[0]))
			edges[len(edges)-1].Subject += "x"
			var budgetErr *service.PromptBudgetError
			if _, err := generatePromptFixture(t, planner, cfg.PromptBudget, edges, req); !errors.As(err, &budgetErr) || len(bodies()) != 1 {
				t.Fatalf("one-over error = %v, requests = %d, want a prompt budget error and no new request", err, len(bodies()))
			}
		})
	}
}

func providerPromptEdges(t *testing.T, capBytes, count int) []domain.GraphTuple {
	t.Helper()
	// Each prefix repeat uses 7 JSON bytes: 3 for 界 and 2 each for the backslash and quote.
	const fixed = config.PromptScaffoldingBytes + len("Recursionvisual")
	const edgeBytes = 24*7 + 3 + len(` -[depends_on]-> Recursion\n`)
	padding := capBytes - fixed - count*edgeBytes
	edges := make([]domain.GraphTuple, count)
	for i := range edges {
		n := min(padding, 50)
		name := strings.Repeat("界\\\"", 24) + fmt.Sprintf("%03d", i) + strings.Repeat("x", n)
		edges[i] = domain.GraphTuple{Subject: name, Relation: "depends_on", Object: "Recursion"}
		padding -= n
	}
	if padding != 0 {
		t.Fatalf("fixture missed the byte cap by %d bytes", padding)
	}
	return edges
}

func fixturePromptText(t *testing.T, provider, body string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal(err)
	}
	var outputLimit json.RawMessage
	switch provider {
	case "aws-bedrock", "google-ai":
		configKey, limitKey := "inferenceConfig", "maxTokens"
		if provider == "google-ai" {
			configKey, limitKey = "generationConfig", "maxOutputTokens"
		}
		var generation map[string]json.RawMessage
		if err := json.Unmarshal(fields[configKey], &generation); err != nil {
			t.Fatal(err)
		}
		outputLimit = generation[limitKey]
	default:
		outputLimit = fields["max_tokens"]
	}
	if string(outputLimit) != "2048" {
		t.Fatalf("output limit = %s, want 2048", outputLimit)
	}
	if provider == "ollama" {
		var messages []struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(fields["messages"], &messages); err != nil || len(messages) != 1 || messages[0].Role != "user" {
			t.Fatalf("Ollama calibration needs one user message, got %v (%v)", messages, err)
		}
	}
	var text strings.Builder
	for _, message := range promptMessages(provider, body) {
		text.WriteString(message.content)
	}
	return text.String()
}

func promptFixtureEndpoint(t *testing.T, provider, topic string) (string, func() []string) {
	t.Helper()
	content := strconv.Quote(`{"items":[{"topic":` + strconv.Quote(topic) + `,"priority":1,"prerequisite_chain":[],"explanation":"Practice the topic.","resources":[]}]}`)
	var reply string
	switch provider {
	case "anthropic":
		reply = `{"id":"msg-fixture","type":"message","role":"assistant","model":"fixture","content":[{"type":"text","text":` + content + `}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`
	case "google-ai":
		reply = `{"candidates":[{"content":{"role":"model","parts":[{"text":` + content + `}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	default:
		reply = `{"id":"chat-fixture","object":"chat.completion","created":1,"model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":` + content + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	}
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	baseURL := srv.URL
	if provider != "anthropic" {
		baseURL += "/v1"
	}
	return baseURL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}
