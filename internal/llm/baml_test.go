package llm

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Victoroide/axiom-engine/baml_client/types"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestBAMLTranslation(t *testing.T) {
	adapter := New()
	req := service.TopicRequest{GraphContext: []domain.GraphTuple{{Subject: "Recursion", Relation: "depends_on", Object: "Functions"}}, Topic: "Recursion", PrerequisiteOrder: []string{"Functions"}, VARKProfile: "visual"}
	adapter.generate = func(ctx context.Context, g []types.GraphTuple, topic string, prereqs []string, vark string) (types.AdaptiveStudyPlan, error) {
		if ctx != context.Background() || len(g) != 1 || g[0].Subject != "Recursion" || g[0].Relation != "depends_on" || g[0].Object != "Functions" || topic != req.Topic || !reflect.DeepEqual(prereqs, req.PrerequisiteOrder) || vark != req.VARKProfile {
			t.Fatal("incorrect request translation")
		}
		return types.AdaptiveStudyPlan{Items: []types.StudyItem{{Topic: "Recursion", Priority: 2, Prerequisite_chain: []string{"Functions"}, Explanation: "Practice", Resources: []types.Resource{{Title: "Guide", Url: "https://example.com", Resource_type: "video"}}}}}, nil
	}
	items, err := adapter.GenerateTopic(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.PlanItem{{Topic: "Recursion", Priority: 2, PrerequisiteChain: []string{"Functions"}, Explanation: "Practice", Resources: []domain.Resource{{Title: "Guide", URL: "https://example.com", ResourceType: "video"}}}}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("got %+v", items)
	}
	failure := errors.New("upstream")
	adapter.generate = func(context.Context, []types.GraphTuple, string, []string, string) (types.AdaptiveStudyPlan, error) {
		return types.AdaptiveStudyPlan{}, failure
	}
	if _, err := adapter.GenerateTopic(context.Background(), req); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
