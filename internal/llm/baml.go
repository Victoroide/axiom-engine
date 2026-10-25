// Package llm adapts the generated BAML client to the reasoning pipeline.
package llm

import (
	"context"

	baml "github.com/Victoroide/axiom-reasoning-svc/baml_client"
	"github.com/Victoroide/axiom-reasoning-svc/baml_client/types"
	"github.com/Victoroide/axiom-reasoning-svc/internal/domain"
	"github.com/Victoroide/axiom-reasoning-svc/internal/service"
)

type generateFunc func(context.Context, []types.GraphTuple, string, []string, string, string, string) (types.AdaptiveStudyPlan, error)

// BAML generates per-topic plans using Amazon Bedrock.
type BAML struct{ generate generateFunc }

// New constructs the BAML adapter.
func New() *BAML {
	return &BAML{generate: func(ctx context.Context, g []types.GraphTuple, t string, p []string, v, s, c string) (types.AdaptiveStudyPlan, error) {
		return baml.GenerateTopicPlan(ctx, g, t, p, v, s, c)
	}}
}

// GenerateTopic translates the domain boundary to and from generated types.
func (b *BAML) GenerateTopic(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
	tuples := make([]types.GraphTuple, len(r.GraphContext))
	for i, t := range r.GraphContext {
		tuples[i] = types.GraphTuple{Subject: t.Subject, Relation: t.Relation, Object: t.Object}
	}
	plan, err := b.generate(ctx, tuples, r.Topic, r.PrerequisiteOrder, r.VARKProfile, r.StudentID, r.CourseID)
	if err != nil {
		return nil, err
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
