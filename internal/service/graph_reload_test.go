//go:build linux || darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

func loadReloadFixture(t *testing.T, root string) *graph.Snapshot {
	t.Helper()
	path := filepath.Join(t.TempDir(), "curriculum.json")
	contents := fmt.Sprintf(`[{"subject":"Lesson","relation":"depends_on","object":%q}]`, root)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := graph.LoadSnapshot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestGraphReloadQueuedAndActivePlans(t *testing.T) {
	old, next := loadReloadFixture(t, "Old Root"), loadReloadFixture(t, "New Root")
	synctest.Test(t, func(t *testing.T) {
		curriculum := graph.NewCurriculum(old)
		calls := make(chan TopicRequest, 3)
		release := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		planner := plannerFunc(func(ctx context.Context, req TopicRequest) ([]domain.PlanItem, error) {
			calls <- req
			select {
			case <-release:
				return []domain.PlanItem{{Topic: req.Topic}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		svc := NewReasoningServiceWithCurriculum(curriculum, planner, Options{LLMTimeout: time.Second, MaxFailedTopics: 1, MaxConcurrency: 1, Logger: slog.New(slog.DiscardHandler)})
		request := domain.AdaptivePlanRequest{FailedTopics: []string{"Lesson"}, VARKProfile: "visual"}
		type result struct {
			plan domain.PlanResponse
			err  error
		}
		start := func() <-chan result {
			ch := make(chan result, 1)
			go func() {
				plan, err := svc.GeneratePlan(ctx, request)
				ch <- result{plan, err}
			}()
			return ch
		}
		assertPrompt := func(req TopicRequest, root string) {
			t.Helper()
			if !slices.Equal(req.PrerequisiteOrder, []string{root}) || len(req.GraphContext) != 1 || req.GraphContext[0].Object != root {
				t.Errorf("prompt uses mixed or wrong graph: %+v, want %s", req, root)
			}
		}
		assertPlan := func(got result, root string) {
			t.Helper()
			if got.err != nil || len(got.plan.Items) != 1 || !slices.Equal(got.plan.Items[0].PrerequisiteChain, []string{root}) {
				t.Errorf("result uses mixed or wrong graph: %+v err=%v, want %s", got.plan.Items, got.err, root)
			}
		}
		active := start()
		assertPrompt(<-calls, "Old Root")
		queued := start()
		synctest.Wait()
		if len(calls) != 0 {
			t.Fatal("second request dispatched while the single permit was held")
		}
		if !curriculum.Publish(next) {
			t.Fatal("failed to replace the graph")
		}
		fresh := start()
		synctest.Wait()
		if len(calls) != 0 {
			t.Fatal("reload released or replaced the occupied permit")
		}
		close(release)
		assertPlan(<-active, "Old Root")
		assertPlan(<-queued, "Old Root")
		assertPlan(<-fresh, "New Root")
		if len(calls) != 2 {
			t.Fatalf("expected two more planner calls, got %d", len(calls))
		}
		assertPrompt(<-calls, "Old Root")
		assertPrompt(<-calls, "New Root")
	})
}

func TestGraphReloadKeepsCircuitHistory(t *testing.T) {
	old, next := loadReloadFixture(t, "Old Root"), loadReloadFixture(t, "New Root")
	synctest.Test(t, func(t *testing.T) {
		curriculum := graph.NewCurriculum(old)
		calls := make(chan struct{}, 4)
		planner := plannerFunc(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
			calls <- struct{}{}
			return nil, ErrUpstream
		})
		svc := NewReasoningServiceWithCurriculum(curriculum, planner, Options{LLMTimeout: time.Second, MaxFailedTopics: 1, Logger: slog.New(slog.DiscardHandler)})
		req := domain.AdaptivePlanRequest{FailedTopics: []string{"Lesson"}, VARKProfile: "visual"}
		for dispatch := range 3 {
			if dispatch == 2 && !curriculum.Publish(next) {
				t.Fatal("failed to replace the graph")
			}
			if _, err := svc.GeneratePlan(t.Context(), req); !errors.Is(err, ErrUpstream) {
				t.Fatalf("dispatch %d: %v", dispatch, err)
			}
		}
		if _, err := svc.GeneratePlan(t.Context(), req); !errors.Is(err, ErrCircuitOpen) || len(calls) != 3 {
			t.Fatalf("circuit failure history was lost: calls=%d err=%v", len(calls), err)
		}
		curriculum.Publish(old)
		if _, err := svc.GeneratePlan(t.Context(), req); !errors.Is(err, ErrCircuitOpen) || len(calls) != 3 {
			t.Fatalf("open circuit was replaced: calls=%d err=%v", len(calls), err)
		}
	})
}
