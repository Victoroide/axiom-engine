package service

import (
	"context"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func TestProgressUsesOriginalIndicesAndStaysPerCall(t *testing.T) {
	events := make(chan domain.TopicProgress, 3)
	release := make(chan struct{})
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		if r.Topic == "Recursion" {
			<-release
		}
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	})
	option := WithProgress(func(progress domain.TopicProgress) {
		events <- progress
		if progress.Topic == "Polymorphism" {
			close(release)
		}
	})
	plan, err := svc.GeneratePlan(context.Background(), request(" Recursion ", "recursion", "Polymorphism"), option)
	if err != nil {
		t.Fatal(err)
	}
	first, second := <-events, <-events
	if first.Topic != "Polymorphism" || first.Index != 2 || second.Topic != "Recursion" || second.Index != 0 || first.ItemsGenerated != 1 || second.ItemsGenerated != 1 {
		t.Fatalf("progress=%+v,%+v", first, second)
	}
	if plan.Meta.TopicsProcessed != 2 {
		t.Fatalf("topics=%d", plan.Meta.TopicsProcessed)
	}
	if _, err := svc.GeneratePlan(context.Background(), request("Polymorphism")); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatal("progress callback leaked into the next call")
	}
}
