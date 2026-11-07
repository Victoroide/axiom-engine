// Package llm adapts the generated BAML client to the reasoning pipeline.
package llm

import (
	"context"

	runtime "github.com/boundaryml/baml/engine/language_client_go/pkg"

	"github.com/Victoroide/axiom-engine/internal/config"

	baml "github.com/Victoroide/axiom-engine/baml_client"
	"github.com/Victoroide/axiom-engine/baml_client/types"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type generateFunc func(context.Context, []types.GraphTuple, string, []string, string) (types.AdaptiveStudyPlan, error)

// BAML generates per-topic plans using the configured provider.
type BAML struct{ generate generateFunc }

// New constructs the BAML adapter.
func New(settings ...config.LLM) *BAML {
	cfg := config.DefaultLLM()
	if len(settings) > 0 {
		cfg = settings[0]
	}
	registry := &runtime.ClientRegistry{}
	registry.AddLlmClient("Planner", cfg.Provider, clientOptions(cfg))
	registry.SetPrimaryClient("Planner")
	return &BAML{generate: func(ctx context.Context, g []types.GraphTuple, t string, p []string, v string) (types.AdaptiveStudyPlan, error) {
		return baml.GenerateTopicPlan(ctx, g, t, p, v, baml.WithClientRegistry(registry))
	}}
}

func clientOptions(cfg config.LLM) map[string]any {
	opts := map[string]any{"model": cfg.Model}
	reason := "length"
	switch cfg.Provider {
	case "aws-bedrock":
		opts["inference_configuration"] = map[string]any{"max_tokens": 2048, "temperature": 0.1}
		reason = "max_tokens"
	case "google-ai":
		opts["generationConfig"] = map[string]any{"maxOutputTokens": 2048, "temperature": 0.1}
		reason = "MAX_TOKENS"
	default:
		opts["max_tokens"] = 2048
		opts["temperature"] = 0.1
		if cfg.Provider == "anthropic" {
			reason = "max_tokens"
		}
	}
	opts["finish_reason_deny_list"] = []string{reason}
	if cfg.BaseURL != "" {
		opts["base_url"] = cfg.BaseURL
	}
	if cfg.APIKey != "" {
		opts["api_key"] = cfg.APIKey
	}
	return opts
}

// GenerateTopic translates the domain boundary to and from generated types.
func (b *BAML) GenerateTopic(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
	tuples := make([]types.GraphTuple, len(r.GraphContext))
	for i, t := range r.GraphContext {
		tuples[i] = types.GraphTuple{Subject: t.Subject, Relation: t.Relation, Object: t.Object}
	}
	plan, err := b.generate(ctx, tuples, r.Topic, r.PrerequisiteOrder, r.VARKProfile)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, classify(err)
	}
	items := make([]domain.PlanItem, len(plan.Items))
	for i, item := range plan.Items {
		resources := make([]domain.Resource, len(item.Resources))
		for j, res := range item.Resources {
			resources[j] = domain.Resource{Title: res.Title, URL: res.Url, ResourceType: res.Resource_type}
		}
		items[i] = domain.PlanItem{Topic: item.Topic, Priority: int(item.Priority), PrerequisiteChain: item.Prerequisite_chain, Explanation: item.Explanation, Resources: resources}
	}
	return items, nil
}
