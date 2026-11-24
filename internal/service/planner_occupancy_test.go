package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/metrics"
)

func TestPlannerCallsRunningAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const queueTimeout = 50 * time.Millisecond
		type instance struct {
			service *ReasoningService
			limit   int
			started chan struct{}
			release chan struct{}
			cancel  context.CancelFunc
			done    chan error
		}
		instances := make([]instance, 0, 2)
		for _, limit := range []int{2, 3} {
			started, release := make(chan struct{}, limit+1), make(chan struct{})
			defer close(release)
			svc := NewReasoningService(graph.NewInMemoryGraph(), plannerFunc(func(_ context.Context, req TopicRequest) ([]domain.PlanItem, error) {
				started <- struct{}{}
				if req.Topic == "Recursion" {
					<-release
				}
				return []domain.PlanItem{{Topic: req.Topic}}, nil
			}), Options{Metrics: metrics.New(), MaxConcurrency: limit, MaxFailedTopics: 10, LLMTimeout: time.Second, QueueTimeout: queueTimeout})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, limit)
			for range limit {
				go func() {
					_, err := svc.GeneratePlan(ctx, request("Recursion"))
					done <- err
				}()
			}
			instances = append(instances, instance{service: svc, limit: limit, started: started, release: release, cancel: cancel, done: done})
		}
		synctest.Wait()
		for _, inst := range instances {
			if got := len(inst.started); got != inst.limit {
				t.Fatalf("instance with limit %d started %d calls", inst.limit, got)
			}
			assertPlannerOccupancy(t, inst.service.metrics, inst.limit, 0)
			inst.cancel()
			for range inst.limit {
				if err := <-inst.done; !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled request returned %v", err)
				}
			}
			assertPlannerOccupancy(t, inst.service.metrics, inst.limit, inst.limit)
		}
		for _, inst := range instances {
			assertAdmission := func(waits int) {
				if got := len(inst.started); got != inst.limit {
					t.Fatalf("limit %d: queue attempts dispatched extra calls, total %d", inst.limit, got)
				}
				assertPlannerOccupancy(t, inst.service.metrics, inst.limit, inst.limit,
					fmt.Sprintf("axiom_llm_queue_wait_seconds_count %d", waits),
					"axiom_llm_queue_timeouts_total 1")
			}
			start := time.Now()
			_, err := inst.service.GeneratePlan(t.Context(), request("Polymorphism"))
			var unavailable *UnavailableError
			if !errors.As(err, &unavailable) || unavailable.RetryAfter != time.Second || time.Since(start) != queueTimeout {
				t.Fatalf("limit %d: queued request returned %v after %v", inst.limit, err, time.Since(start))
			}
			assertAdmission(inst.limit + 1)
			prepared, err := inst.service.PreparePlan(t.Context(), request("Polymorphism"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			start = time.Now()
			if _, err := prepared.Generate(ctx); !errors.Is(err, context.Canceled) || time.Since(start) != 0 {
				t.Fatalf("already canceled request returned %v after %v", err, time.Since(start))
			}
			assertAdmission(inst.limit + 2)

			waitingCtx, cancelWaiting := context.WithCancel(t.Context())
			defer cancelWaiting()
			waiting := make(chan error, 1)
			start = time.Now()
			go func() {
				_, err := prepared.Generate(waitingCtx)
				waiting <- err
			}()
			synctest.Wait()
			select {
			case err := <-waiting:
				t.Fatalf("queued request returned before cancellation: %v", err)
			default:
			}
			assertAdmission(inst.limit + 2)
			cancelWaiting()
			if err := <-waiting; !errors.Is(err, context.Canceled) || time.Since(start) != 0 {
				t.Fatalf("queued request returned %v after %v", err, time.Since(start))
			}
			assertAdmission(inst.limit + 3)
		}
		for _, inst := range instances {
			for remaining := inst.limit - 1; remaining >= 0; remaining-- {
				inst.release <- struct{}{}
				synctest.Wait()
				assertPlannerOccupancy(t, inst.service.metrics, remaining, remaining)
				if remaining == inst.limit-1 {
					if _, err := inst.service.GeneratePlan(t.Context(), request("Polymorphism")); err != nil {
						t.Fatalf("new request after first planner return: %v", err)
					}
					assertPlannerOccupancy(t, inst.service.metrics, remaining, remaining)
				}
			}
		}
	})
}

func TestPlannerCancelReturnTie(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wantErr     error
		cooperative bool
	}{
		{name: "cancellation", wantErr: context.Canceled},
		{name: "deadline", wantErr: context.DeadlineExceeded},
		{name: "cooperative", wantErr: context.Canceled, cooperative: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const returnAfter = time.Second
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				timeout := returnAfter
				if errors.Is(tc.wantErr, context.Canceled) {
					timer := time.AfterFunc(returnAfter, cancel)
					defer timer.Stop()
					timeout = 2 * returnAfter
				}
				svc := NewReasoningService(graph.NewInMemoryGraph(), plannerFunc(func(ctx context.Context, req TopicRequest) ([]domain.PlanItem, error) {
					if tc.cooperative {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					time.Sleep(returnAfter)
					return []domain.PlanItem{{Topic: req.Topic}}, nil
				}), Options{Metrics: metrics.New(), MaxConcurrency: 1, MaxFailedTopics: 10, LLMTimeout: timeout})
				start := time.Now()
				_, err := svc.GeneratePlan(ctx, request("Recursion"))
				if (err != nil || tc.cooperative) && !errors.Is(err, tc.wantErr) {
					t.Fatalf("planner returned %v, want %v when canceled", err, tc.wantErr)
				}
				if got := time.Since(start); got != returnAfter {
					t.Fatalf("request waited %v, want %v", got, returnAfter)
				}
				synctest.Wait()
				assertPlannerOccupancy(t, svc.metrics, 0, 0)
				if !svc.slots.TryAcquire(1) {
					t.Fatal("planner retained its permit after return")
				}
				svc.slots.Release(1)
			})
		})
	}
}

func assertPlannerOccupancy(t *testing.T, recorder *metrics.Metrics, inflight, afterCancel int, extraSamples ...string) {
	t.Helper()
	scrape := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(scrape, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if scrape.Code != http.StatusOK {
		t.Fatalf("metrics scrape returned %d", scrape.Code)
	}
	samples := append([]string{
		fmt.Sprintf("axiom_llm_inflight %d", inflight),
		fmt.Sprintf("axiom_llm_inflight_after_cancel %d", afterCancel),
	}, extraSamples...)
	for _, sample := range samples {
		if !strings.Contains(scrape.Body.String(), "\n"+sample+"\n") {
			t.Errorf("metrics scrape missing %q", sample)
		}
	}
}
