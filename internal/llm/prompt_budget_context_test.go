package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestPromptBudgetPreservesPromptBytes(t *testing.T) {
	upstream := bedrockEndpoint(t, "ok")
	svc := service.NewReasoningService(graph.NewInMemoryGraph(), New(), service.Options{LLMTimeout: 5 * time.Second, MaxFailedTopics: 10})
	for i, tc := range []struct {
		mastered []string
		digest   string
	}{
		{nil, "7b9aaaec3391cf574e75fc91ec8a3a955a081c27a9ef04341692b2312f09700a"},
		{[]string{"Functions"}, "0af57299358683337d3f4aaee19da7827cc5cfd84e1f355a7036e1ef6a4f4227"},
	} {
		plan, err := svc.GeneratePlan(t.Context(), domain.AdaptivePlanRequest{
			FailedTopics: []string{" rEcursion ", "Recursion"}, MasteredTopics: tc.mastered, VARKProfile: "visual",
		})
		if err != nil || len(plan.Items) != 1 || plan.Items[0].Priority != 1 || plan.Meta.SubgraphTuples != 3 {
			t.Fatalf("plan = %+v, error = %v", plan, err)
		}
		bodies := upstream.Bodies()
		if len(bodies) != i+1 {
			t.Fatalf("requests = %d, want %d", len(bodies), i+1)
		}
		if digest := fmt.Sprintf("%x", sha256.Sum256([]byte(bodies[i]))); digest != tc.digest {
			t.Fatalf("provider body digest with mastery %v = %s, want %s", tc.mastered, digest, tc.digest)
		}
	}
}

func TestPromptBudgetMaximumRender(t *testing.T) {
	topic := strings.Repeat("𐌰", 128)
	baseURL, bodies := promptFixtureEndpoint(t, "openai", topic)
	planner := New(config.LLM{Provider: "openai", Model: "gpt-4.1-mini", BaseURL: baseURL, APIKey: "fixture-key"})
	checkMaximumRender(t, planner, config.DefaultPromptBudget(), bodies, "openai", topic, 16_384, 15_466, 13)
}

func TestPromptBudgetOllamaContext(t *testing.T) {
	topic := strings.Repeat("𐌰", 128)
	baseURL, bodies := promptFixtureEndpoint(t, "ollama", topic)
	env := map[string]string{"AXIOM_AUTH_DISABLED": "true", "LLM_PROVIDER": "ollama", "LLM_MODEL": "mistral:v0.3", "LLM_BASE_URL": baseURL}
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	text := checkMaximumRender(t, New(cfg.LLM), cfg.PromptBudget, bodies, "ollama", topic, 4480, 3574, 1)
	checkMistralContext(t, text, "44f2decaee6cc88e1ea099f3de74f26315c64f0f729f9c25074e2415af1acc43", 1997)
}

// Counts include the leading special token and Ollama's single-message framing.
// The digest pins the complete input measured with the v0.3 tokenizer at c170c70.
func checkMistralContext(t *testing.T, text, wantDigest string, inputTokens int) {
	t.Helper()
	framed := "[INST] " + text + "[/INST]"
	if digest := fmt.Sprintf("%x", sha256.Sum256([]byte(framed))); digest != wantDigest {
		t.Fatalf("framed prompt digest = %s, want %s", digest, wantDigest)
	}
	const contextTokens = 4096
	total := inputTokens + config.PlannerOutputTokens
	if total > contextTokens {
		t.Fatalf("input plus completion = %d tokens, context = %d", total, contextTokens)
	}
	t.Logf("Ollama input %d tokens plus %d completion tokens = %d of %d", inputTokens, config.PlannerOutputTokens, total, contextTokens)
}

func checkMaximumRender(t *testing.T, planner *BAML, budget config.PromptBudget, bodies func() []string, provider, topic string, capBytes, renderBytes, edgeCount int) string {
	t.Helper()
	// Multi-byte names fill the budget without requiring JSON escapes. The only
	// difference between the accounted and rendered curriculum is the line break.
	const line = ` -[depends_on]-> \n`
	padding := capBytes - config.PromptScaffoldingBytes - len(topic) - len("kinesthetic") - edgeCount*(3+len(line)+len(topic))
	edges := make([]domain.GraphTuple, edgeCount)
	for i := range edges {
		n := min(padding, 500)
		name := fmt.Sprintf("%03d", i) + strings.Repeat("𐌰", n/4) + strings.Repeat("x", n%4)
		edges[i] = domain.GraphTuple{Subject: name, Relation: "depends_on", Object: topic}
		padding -= n
	}
	if padding != 0 {
		t.Fatalf("fixture missed the byte cap by %d bytes", padding)
	}
	req := domain.AdaptivePlanRequest{FailedTopics: []string{topic}, VARKProfile: "kinesthetic"}
	if _, err := generatePromptFixture(t, planner, budget, edges, req); err != nil {
		t.Fatal(err)
	}
	got := bodies()
	if len(got) != 1 {
		t.Fatalf("requests = %d, want 1", len(got))
	}
	text := fixturePromptText(t, provider, got[0])
	if len(text) != renderBytes {
		t.Fatalf("rendered UTF-8 bytes = %d, want %d", len(text), renderBytes)
	}
	edges[len(edges)-1].Subject += "x"
	var budgetErr *service.PromptBudgetError
	if _, err := generatePromptFixture(t, planner, budget, edges, req); !errors.As(err, &budgetErr) || len(bodies()) != 1 {
		t.Fatalf("one-over error = %v, requests = %d, want budget error and no new request", err, len(bodies()))
	}
	t.Logf("%s: %d maximum rendered UTF-8 bytes at %d accounted bytes, %d JSON payload bytes", provider, len(text), capBytes, len(got[0]))
	return text
}

func generatePromptFixture(t *testing.T, planner *BAML, budget config.PromptBudget, edges []domain.GraphTuple, req domain.AdaptivePlanRequest) (domain.PlanResponse, error) {
	t.Helper()
	data, err := json.Marshal(edges)
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewReasoningService(g, planner, service.Options{PromptBudget: budget, LLMTimeout: 5 * time.Second, MaxFailedTopics: 10})
	return svc.GeneratePlan(t.Context(), req)
}
