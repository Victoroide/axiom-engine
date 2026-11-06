package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func TestMasteredPrerequisitesAreExcluded(t *testing.T) {
	for _, generated := range []bool{false, true} {
		t.Run(map[bool]string{false: "omitted by planner", true: "returned by planner"}[generated], func(t *testing.T) {
			svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
				if len(r.PrerequisiteOrder) != 0 {
					t.Errorf("prerequisite order=%v, want no mastered topics", r.PrerequisiteOrder)
				}
				items := []domain.PlanItem{{Topic: r.Topic}}
				if generated {
					items = append(items, domain.PlanItem{Topic: "functions"})
				}
				return items, nil
			})
			var req domain.AdaptivePlanRequest
			if err := json.Unmarshal([]byte(`{"failed_topics":["Recursion"],"mastered_topics":[" functions ","Functions"],"vark_profile":"visual"}`), &req); err != nil {
				t.Fatal(err)
			}
			got, err := svc.GeneratePlan(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Items) != 1 || got.Items[0].Topic != "Recursion" || !reflect.DeepEqual(got.Items[0].PrerequisiteChain, []string{"Functions"}) {
				t.Fatalf("items=%+v", got.Items)
			}
			data, _ := json.Marshal(got.Meta)
			var meta map[string]any
			_ = json.Unmarshal(data, &meta)
			dropped := float64(0)
			if generated {
				dropped = 1
			}
			if meta["items_skipped_mastered"] != float64(1) || meta["items_dropped"] != dropped {
				t.Fatalf("meta=%s", data)
			}
		})
	}
}

func TestMasteryValidationPrecedesPlanning(t *testing.T) {
	for _, topics := range []string{`["Unknown"]`, `["recursion"]`} {
		svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
			t.Error("invalid history reached planner")
			return nil, nil
		})
		var req domain.AdaptivePlanRequest
		_ = json.Unmarshal([]byte(`{"failed_topics":["Recursion"],"mastered_topics":`+topics+`,"vark_profile":"visual"}`), &req)
		if _, err := svc.GeneratePlan(t.Context(), req); err == nil {
			t.Fatal("accepted invalid mastery")
		}
	}
}

func TestMasteryCountUsesDistinctPrerequisites(t *testing.T) {
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	})
	req := request("Merge Sort", "Dynamic Programming")
	req.MasteredTopics = []string{"Functions", "Recursion", "Variables"}
	got, err := svc.GeneratePlan(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Meta.ItemsSkippedMastered != 2 || got.Meta.ItemsDropped != 0 {
		t.Fatalf("plan=%+v", got)
	}
}

func TestMasteryLimitAndCanonicalDuplicates(t *testing.T) {
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	})
	svc.maxMasteredTopics = 1
	req := request("Recursion")
	req.MasteredTopics = []string{"Functions", " functions "}
	if _, err := svc.GeneratePlan(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	req.MasteredTopics = append(req.MasteredTopics, "Variables")
	if _, err := svc.GeneratePlan(t.Context(), req); err == nil {
		t.Fatal("accepted history beyond limit")
	}
}

func TestPlanWithoutMasteryKeepsResponseBytes(t *testing.T) {
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	})
	got, err := svc.GeneratePlan(t.Context(), request("Recursion"))
	if err != nil {
		t.Fatal(err)
	}
	// Ignore measured latency when comparing the response contract.
	got.Meta.LLMLatencyMs = 0
	got.Meta.TotalLatencyMs = 0
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"student_id":"student-42","course_id":"cs101","items":[{"topic":"Recursion","priority":1,"prerequisite_chain":["Functions"],"explanation":"","resources":[]}],"_meta":{"subgraph_tuples":3,"topics_processed":1,"items_generated":1,"items_after_validation":1,"items_dropped":0,"llm_latency_ms":0,"total_latency_ms":0}}`
	if string(data) != want {
		t.Fatalf("response=%s", data)
	}
}
