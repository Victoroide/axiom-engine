package llm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestAdapterProviderResponses(t *testing.T) {
	wantPlan := []domain.PlanItem{{
		Topic: "Recursion", Priority: 1, PrerequisiteChain: []string{"Functions"}, Explanation: "Practice recursion.",
		Resources: []domain.Resource{{Title: "Guide", URL: "https://example.com/recursion", ResourceType: "video"}},
	}}
	for _, provider := range nativeProviders {
		t.Run(provider, func(t *testing.T) {
			for _, tc := range []struct {
				mode, outcome string
				items         []domain.PlanItem
				class         error
			}{
				{mode: "ok", outcome: "ok", items: wantPlan},
				{mode: "empty", outcome: "ok", items: []domain.PlanItem{}},
				{mode: "no-content", outcome: "invalid_output", class: service.ErrInvalidOutput},
				{mode: "garbage", outcome: "invalid_output", class: service.ErrInvalidOutput},
				{mode: "malformed", outcome: "invalid_output", class: service.ErrInvalidOutput},
				{mode: "max-tokens", outcome: "invalid_output", class: service.ErrInvalidOutput},
				{mode: "throttle", outcome: "throttled", class: service.ErrThrottled},
				{mode: "unauthorized", outcome: "rejected", class: service.ErrUpstreamRejected},
				{mode: "denied", outcome: "rejected", class: service.ErrUpstreamRejected},
				{mode: "internal", outcome: "unavailable", class: service.ErrUpstreamUnavailable},
				{mode: "closed", outcome: "unavailable", class: service.ErrUpstreamUnavailable},
				{mode: "unknown", outcome: "error"},
			} {
				t.Run(tc.mode, func(t *testing.T) {
					f := newProviderFixture(t, provider, tc.mode)
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					items, err := New(f.settings).GenerateTopic(ctx, topicRequest())
					if !reflect.DeepEqual(items, tc.items) || tc.class != nil && !errors.Is(err, tc.class) || service.LLMOutcome(err) != tc.outcome {
						t.Errorf("items = %+v, error = %v, outcome = %s; want %+v, %v, %s", items, err, service.LLMOutcome(err), tc.items, tc.class, tc.outcome)
					}
					if tc.mode == "unknown" && (err == nil || !strings.Contains(err.Error(), providerText)) {
						t.Errorf("native error did not include the provider diagnostic: %v", err)
					}
					if attempts := f.count(); attempts != 1 {
						t.Errorf("attempts = %d, want 1", attempts)
					}
				})
			}
		})
	}
}

const providerCancelTolerance = 250 * time.Millisecond

func TestAdapterProviderCancellation(t *testing.T) {
	const budget = 500 * time.Millisecond
	for _, provider := range nativeProviders {
		t.Run(provider, func(t *testing.T) {
			t.Run("before dispatch", func(t *testing.T) {
				f := newProviderFixture(t, provider, "ok")
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				items, err := New(f.settings).GenerateTopic(ctx, topicRequest())
				if items != nil || !errors.Is(err, context.Canceled) || f.count() != 0 {
					t.Fatalf("items = %v, error = %v, attempts = %d; want cancellation before dispatch", items, err, f.count())
				}
			})
			t.Run("caller cancel", func(t *testing.T) {
				f := newProviderFixture(t, provider, "hang")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				result := startProviderCall(ctx, New(f.settings).GenerateTopic)
				select {
				case <-f.started:
				case got := <-result:
					t.Fatalf("call returned before cancellation: %v", got.err)
				case <-ctx.Done():
					t.Fatal("provider did not receive the request")
				}
				canceledAt := time.Now()
				cancel()
				if problem := providerCancellationProblem(t.Context(), result, f.disconnected, canceledAt, context.Canceled); problem != "" {
					t.Fatal(problem)
				}
				if attempts := f.count(); attempts != 1 {
					t.Fatalf("attempts = %d, want 1", attempts)
				}
			})
			t.Run("deadline", func(t *testing.T) {
				f := newProviderFixture(t, provider, "hang")
				ctx, cancel := context.WithTimeout(t.Context(), budget)
				defer cancel()
				deadline, _ := ctx.Deadline()
				result := startProviderCall(ctx, New(f.settings).GenerateTopic)
				if problem := providerCancellationProblem(t.Context(), result, f.disconnected, deadline, context.DeadlineExceeded); problem != "" {
					t.Fatal(problem)
				}
				assertProviderStarted(t, f, deadline)
				if attempts := f.count(); attempts != 1 {
					t.Fatalf("attempts = %d, want 1", attempts)
				}
			})
		})
	}
}

type providerCompletion struct {
	items           []domain.PlanItem
	err, contextErr error
	at              time.Time
}

func startProviderCall(ctx context.Context, generate func(context.Context, service.TopicRequest) ([]domain.PlanItem, error)) <-chan providerCompletion {
	result := make(chan providerCompletion, 1)
	go func() {
		items, err := generate(ctx, topicRequest())
		result <- providerCompletion{items: items, err: err, at: time.Now(), contextErr: ctx.Err()}
	}()
	return result
}

func providerCancellationProblem(ctx context.Context, result <-chan providerCompletion, disconnected <-chan time.Time, canceledAt time.Time, want error) string {
	limit := canceledAt.Add(providerCancelTolerance)
	wait, stop := context.WithDeadline(ctx, limit)
	defer stop()
	got, ok := receiveProviderEvent(result, wait.Done())
	if !ok {
		return "call did not return in the cancellation window"
	}
	if !errors.Is(got.contextErr, want) {
		return fmt.Sprintf("caller context at adapter return = %v, want %v", got.contextErr, want)
	}
	if got.items != nil || !errors.Is(got.err, want) || got.at.Before(canceledAt) || got.at.After(limit) {
		return fmt.Sprintf("items = %v, error = %v, return offset = %s, want %v within %s of cancellation", got.items, got.err, got.at.Sub(canceledAt), want, providerCancelTolerance)
	}

	observed, ok := receiveProviderEvent(disconnected, wait.Done())
	if !ok {
		return "provider connection remained open"
	}
	if observed.Before(canceledAt) || observed.After(limit) {
		return fmt.Sprintf("provider disconnect offset = %s, want within %s of cancellation", observed.Sub(canceledAt), providerCancelTolerance)
	}
	return ""
}

func receiveProviderEvent[T any](events <-chan T, stop <-chan struct{}) (T, bool) {
	select {
	case event := <-events:
		return event, true
	case <-stop:
		// Both channels may be ready if the driver was paused.
		select {
		case event := <-events:
			return event, true
		default:
			var zero T
			return zero, false
		}
	}
}

func assertProviderStarted(t *testing.T, f *providerFixture, deadline time.Time) {
	t.Helper()
	wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	if problem := providerStartProblem(wait, f, deadline); problem != "" {
		t.Fatal(problem)
	}
}

func providerStartProblem(ctx context.Context, f *providerFixture, deadline time.Time) string {
	started, ok := receiveProviderEvent(f.started, ctx.Done())
	if !ok {
		return "provider start notification missing"
	}
	if !started.Before(deadline) {
		return "provider received the request after the deadline"
	}
	return ""
}

func holdProviderStart(t *testing.T, f *providerFixture, receiptReady func() bool) (<-chan struct{}, context.CancelFunc) {
	t.Helper()
	recorded := make(chan struct{}, 1)
	released, publish := context.WithCancel(t.Context())
	t.Cleanup(publish)
	f.mu.Lock()
	f.beforeStartNotification = func() {
		if receiptReady != nil && !receiptReady() {
			return
		}
		select {
		case recorded <- struct{}{}:
		default:
		}
		<-released.Done()
	}
	f.mu.Unlock()
	return recorded, publish
}

func assertDelayedProviderStart(t *testing.T, f *providerFixture, deadline time.Time, publish context.CancelFunc, wantProblem string) {
	t.Helper()
	wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	ctx := watchedCancellation{Context: wait, watched: make(chan struct{}, 1)}
	result := make(chan string, 1)
	go func() { result <- providerStartProblem(ctx, f, deadline) }()
	select {
	case <-ctx.watched:
	case problem := <-result:
		t.Fatalf("start check finished with its notification held: %q", problem)
	case <-wait.Done():
		t.Fatal("start check did not wait for the notification")
	}
	// Let the handler publish only once the driver has begun waiting.
	publish()
	if problem, ok := receiveProviderEvent(result, wait.Done()); !ok || problem != wantProblem {
		t.Fatalf("delayed start check = %q, finished = %t, want %q", problem, ok, wantProblem)
	}
}

func TestAdapterProviderCancellationControls(t *testing.T) {
	t.Run("paused driver", func(t *testing.T) {
		canceledAt := time.Now().Add(-time.Second)
		result := make(chan providerCompletion, 1)
		disconnected := make(chan time.Time, 1)
		// An already-expired driver timer must accept the on-time observations.
		for range 20 {
			result <- providerCompletion{err: context.Canceled, contextErr: context.Canceled, at: canceledAt.Add(time.Millisecond)}
			disconnected <- canceledAt.Add(2 * time.Millisecond)
			if problem := providerCancellationProblem(t.Context(), result, disconnected, canceledAt, context.Canceled); problem != "" {
				t.Fatal(problem)
			}
		}
	})

	t.Run("premature deadline", func(t *testing.T) {
		f := newProviderFixture(t, "openai", "hang")
		recorded, publish := holdProviderStart(t, f, nil)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		planner := New(f.settings)
		result := startProviderCall(ctx, func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
			short, stop := context.WithTimeout(ctx, 500*time.Millisecond)
			defer stop()
			return planner.GenerateTopic(short, req)
		})
		deadline, _ := ctx.Deadline()
		if _, ok := receiveProviderEvent(recorded, ctx.Done()); !ok {
			t.Fatal("provider did not receive the request")
		}
		problem := providerCancellationProblem(t.Context(), result, f.disconnected, deadline, context.DeadlineExceeded)
		if !strings.Contains(problem, "caller context at adapter return") {
			t.Fatalf("premature deadline check = %q", problem)
		}
		assertDelayedProviderStart(t, f, deadline, publish, "")
	})

	t.Run("lost context", func(t *testing.T) {
		for _, tc := range []struct {
			name, wantProblem     string
			dispatchAfterDeadline bool
		}{
			{name: "on-time request with delayed receipt acknowledgement"},
			{name: "late dispatch", dispatchAfterDeadline: true, wantProblem: "provider received the request after the deadline"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newProviderFixture(t, "openai", "hang")
				ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
				defer cancel()
				observer, stop := context.WithTimeout(t.Context(), 5*time.Second)
				defer stop()
				receiptWait := watchedCancellation{Context: observer, watched: make(chan struct{}, 1)}
				recorded, publish := holdProviderStart(t, f, func() bool {
					// Hold the acknowledgement until the caller expires and the
					// independent receipt wait is listening.
					if _, ok := receiveProviderEvent(ctx.Done(), observer.Done()); !ok {
						return false
					}
					_, listening := receiveProviderEvent(receiptWait.watched, observer.Done())
					end, bounded := observer.Deadline()
					return listening && bounded && observer.Err() == nil && time.Now().Before(end)
				})
				planner := New(f.settings)
				result := startProviderCall(ctx, func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
					if tc.dispatchAfterDeadline {
						<-ctx.Done()
					}
					return planner.GenerateTopic(context.WithoutCancel(ctx), req)
				})
				deadline, _ := ctx.Deadline()
				if _, ok := receiveProviderEvent(recorded, receiptWait.Done()); !ok {
					t.Fatal("provider receipt acknowledgement missing")
				}
				problem := providerCancellationProblem(t.Context(), result, f.disconnected, deadline, context.DeadlineExceeded)
				if !strings.Contains(problem, "call did not return") {
					t.Fatalf("lost-context check = %q", problem)
				}
				assertDelayedProviderStart(t, f, deadline, publish, tc.wantProblem)
				f.release()
				select {
				case <-result:
				case <-time.After(5 * time.Second):
					t.Fatal("call still running after releasing the provider fixture")
				}
			})
		}
	})
}
