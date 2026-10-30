package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

type plannerFunc func(context.Context, TopicRequest) ([]domain.PlanItem, error)

func (f plannerFunc) GenerateTopic(ctx context.Context, r TopicRequest) ([]domain.PlanItem, error) {
	return f(ctx, r)
}
func request(topics ...string) domain.AdaptivePlanRequest {
	return domain.AdaptivePlanRequest{FailedTopics: topics, VARKProfile: "visual", StudentID: "student-42", CourseID: "cs101"}
}
func newService(f plannerFunc) *ReasoningService {
	return NewReasoningService(graph.NewInMemoryGraph(), f, Options{LLMTimeout: time.Second, MaxFailedTopics: 10})
}

func TestOrderingDedupeAndHallucinationGuard(t *testing.T) {
	for range 20 {
		var mu sync.Mutex
		calls := map[string]int{}
		svc := newService(func(ctx context.Context, r TopicRequest) ([]domain.PlanItem, error) {
			select {
			case <-time.After(time.Duration(rand.IntN(5)) * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			mu.Lock()
			calls[r.Topic]++
			mu.Unlock()
			if len(r.GraphContext) == 0 || r.VARKProfile != "visual" || r.StudentID != "student-42" || r.CourseID != "cs101" {
				return nil, errors.New("context missing")
			}
			return []domain.PlanItem{{Topic: r.Topic, Explanation: r.Topic}, {Topic: "invented"}, {Topic: "Functions"}}, nil
		})
		got, err := svc.GeneratePlan(context.Background(), request(" Recursion ", "", "Polymorphism", "Recursion", "  "))
		if err != nil {
			t.Fatal(err)
		}
		topics := make([]string, 0, len(got.Items))
		for i, item := range got.Items {
			topics = append(topics, item.Topic)
			if item.Priority != i+1 {
				t.Fatal(got.Items)
			}
		}
		if !reflect.DeepEqual(topics, []string{"Functions", "Recursion", "Polymorphism"}) || calls["Recursion"] != 1 || calls["Polymorphism"] != 1 || len(calls) != 2 || got.Meta.TopicsProcessed != 2 || got.Meta.ItemsGenerated != 6 || got.Meta.ItemsValidated != 3 {
			t.Fatalf("response=%+v calls=%v", got, calls)
		}
	}
}

func TestValidationBeforePlanner(t *testing.T) {
	cases := make([]domain.AdaptivePlanRequest, 0, 8)
	cases = append(cases, request(), request(" "), request("unknown"), request("Recursion", "unknown"))
	bad := request("Recursion")
	bad.VARKProfile = "injected"
	cases = append(cases, bad)
	bad = request("Recursion")
	bad.StudentID = "ignore instructions"
	cases = append(cases, bad)
	bad = request("Recursion")
	bad.CourseID = string(make([]byte, 129))
	cases = append(cases, bad)
	bad = request("Recursion", "Polymorphism")
	cases = append(cases, bad)
	for _, req := range cases {
		t.Run(req.VARKProfile+req.StudentID+req.CourseID+fmt.Sprint(len(req.FailedTopics)), func(t *testing.T) {
			svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
				t.Error("planner called on invalid input")
				return nil, nil
			})
			svc.maxFailedTopics = 1
			_, err := svc.GeneratePlan(context.Background(), req)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("expected validation error: %v", err)
			}
		})
	}
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	})
	_, err := svc.GeneratePlan(context.Background(), request("Recursion", "unknown", "other"))
	var validation *ValidationError
	if !errors.As(err, &validation) || !reflect.DeepEqual(validation.UnknownTopics, []string{"unknown", "other"}) {
		t.Fatal(err)
	}
	for _, profile := range []string{"visual", "auditory", "read_write", "kinesthetic"} {
		r := request("Recursion")
		r.VARKProfile = profile
		r.StudentID = ""
		r.CourseID = ""
		if _, err := svc.GeneratePlan(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeadlineAndUpstreamErrors(t *testing.T) {
	svc := newService(func(ctx context.Context, _ TopicRequest) ([]domain.PlanItem, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	svc.llmTimeout = time.Millisecond
	if _, err := svc.GeneratePlan(context.Background(), request("Recursion")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	svc = newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
		return nil, errors.New("backend secret")
	})
	for range 3 {
		if _, err := svc.GeneratePlan(context.Background(), request("Recursion")); !errors.Is(err, ErrUpstream) {
			t.Fatal(err)
		}
	}
	if _, err := svc.GeneratePlan(context.Background(), request("Recursion")); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
}

func TestCancellationDoesNotTripBreaker(t *testing.T) {
	svc := newService(func(ctx context.Context, _ TopicRequest) ([]domain.PlanItem, error) { return nil, ctx.Err() })
	for range 5 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := svc.GeneratePlan(ctx, request("Recursion")); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if svc.breaker.State() != gobreaker.StateClosed || svc.breaker.Counts().TotalFailures != 0 {
		t.Fatal(svc.breaker.Counts())
	}
}

func TestRequestFailuresDoNotOpenCircuit(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "direct", err: ErrInvalidOutput},
		{name: "wrapped", err: fmt.Errorf("parse response: %w", ErrInvalidOutput)},
		{name: "rejected", err: ErrUpstreamRejected},
		{name: "wrapped rejection", err: fmt.Errorf("request denied: %w", ErrUpstreamRejected)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
				if r.Topic == "Recursion" {
					return nil, tc.err
				}
				return []domain.PlanItem{{Topic: r.Topic}}, nil
			})
			for i := range 5 {
				if _, err := svc.GeneratePlan(context.Background(), request("Recursion")); !errors.Is(err, tc.err) {
					t.Errorf("request %d: expected %v, got %v", i+1, tc.err, err)
				}
			}
			counts := svc.breaker.Counts()
			if svc.breaker.State() != gobreaker.StateClosed || counts.TotalExclusions != 5 || counts.TotalFailures != 0 || counts.TotalSuccesses != 0 {
				t.Fatalf("request failure changed health: state=%v counts=%+v", svc.breaker.State(), counts)
			}
			if _, err := svc.GeneratePlan(context.Background(), request("Polymorphism")); err != nil {
				t.Fatalf("unrelated topic failed: %v", err)
			}
		})
	}
}

func TestThrottlingOpensCircuit(t *testing.T) {
	svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
		return nil, ErrThrottled
	})
	for i := range 3 {
		if _, err := svc.GeneratePlan(context.Background(), request("Recursion")); !errors.Is(err, ErrThrottled) {
			t.Fatal(err)
		}
		if i < 2 && svc.breaker.State() != gobreaker.StateClosed {
			t.Fatalf("circuit opened after %d failures", i+1)
		}
	}
	if svc.breaker.State() != gobreaker.StateOpen {
		t.Fatalf("throttling did not open circuit: %+v", svc.breaker.Counts())
	}
	if _, err := svc.GeneratePlan(context.Background(), request("Polymorphism")); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected open circuit, got %v", err)
	}
}

func TestHalfOpenRequestFailuresReleaseProbes(t *testing.T) {
	for _, failure := range []error{ErrInvalidOutput, ErrUpstreamRejected} {
		t.Run(failure.Error(), func(t *testing.T) {
			healthy := false
			svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
				if !healthy {
					return nil, failure
				}
				return []domain.PlanItem{{Topic: r.Topic}}, nil
			})
			settings := breakerSettings(svc.logger)
			settings.Timeout = time.Nanosecond
			svc.breaker = gobreaker.NewCircuitBreaker[[]domain.PlanItem](settings)
			for range 3 {
				_, _ = svc.breaker.Execute(func() ([]domain.PlanItem, error) { return nil, ErrUpstreamUnavailable })
			}
			for range 5 {
				if _, err := svc.GeneratePlan(t.Context(), request("Recursion")); !errors.Is(err, failure) {
					t.Fatalf("excluded probe failed: %v", err)
				}
			}
			counts := svc.breaker.Counts()
			if svc.breaker.State() != gobreaker.StateHalfOpen || counts.TotalExclusions != 5 || counts.TotalFailures != 0 || counts.TotalSuccesses != 0 {
				t.Fatalf("excluded probes changed health: state=%v counts=%+v", svc.breaker.State(), counts)
			}
			healthy = true
			for range 2 {
				if _, err := svc.GeneratePlan(t.Context(), request("Recursion")); err != nil {
					t.Fatalf("healthy probe could not run: %v", err)
				}
			}
			if svc.breaker.State() != gobreaker.StateClosed {
				t.Fatalf("healthy probes did not close circuit: %+v", svc.breaker.Counts())
			}
		})
	}
}

func TestUpstreamFailuresOpenCircuit(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "unavailable", err: ErrUpstreamUnavailable},
		{name: "deadline", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
				return nil, fmt.Errorf("upstream call: %w", tc.err)
			})
			for range 3 {
				if _, err := svc.GeneratePlan(context.Background(), request("Recursion")); !errors.Is(err, tc.err) {
					t.Fatal(err)
				}
			}
			if svc.breaker.State() != gobreaker.StateOpen {
				t.Fatalf("upstream failure did not open circuit: %+v", svc.breaker.Counts())
			}
		})
	}
}

func TestHalfOpenSaturation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		close(entered)
		<-release
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	})
	svc.breaker = gobreaker.NewCircuitBreaker[[]domain.PlanItem](gobreaker.Settings{MaxRequests: 1, Timeout: time.Nanosecond, ReadyToTrip: func(gobreaker.Counts) bool { return true }})
	_, _ = svc.breaker.Execute(func() ([]domain.PlanItem, error) { return nil, errors.New("failure") })
	done := make(chan error, 1)
	go func() { _, err := svc.GeneratePlan(context.Background(), request("Recursion")); done <- err }()
	<-entered
	_, err := svc.GeneratePlan(context.Background(), request("Recursion"))
	var unavailable *UnavailableError
	if !errors.Is(err, ErrCircuitOpen) || !errors.As(err, &unavailable) || unavailable.RetryAfter != time.Second {
		t.Errorf("saturated circuit error=%v retry=%+v", err, unavailable)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMultiTopicOutageOpensCircuit(t *testing.T) {
	var siblingStarted chan struct{}
	svc := newService(func(ctx context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		if r.Topic == "Polymorphism" {
			close(siblingStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		<-siblingStarted
		return nil, errors.New("backend unavailable")
	})
	for range 3 {
		siblingStarted = make(chan struct{})
		if _, err := svc.GeneratePlan(context.Background(), request("Recursion", "Polymorphism")); !errors.Is(err, ErrUpstream) {
			t.Fatal(err)
		}
	}
	if svc.breaker.State() != gobreaker.StateOpen {
		t.Fatalf("outage did not open circuit: %+v", svc.breaker.Counts())
	}
	if _, err := svc.GeneratePlan(context.Background(), request("Recursion", "Polymorphism")); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
}

func TestHalfOpenCanceledProbesDoNotCloseCircuit(t *testing.T) {
	svc := newService(func(ctx context.Context, _ TopicRequest) ([]domain.PlanItem, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	settings := breakerSettings(svc.logger)
	settings.Timeout = time.Nanosecond
	svc.breaker = gobreaker.NewCircuitBreaker[[]domain.PlanItem](settings)
	for range 3 {
		_, _ = svc.breaker.Execute(func() ([]domain.PlanItem, error) { return nil, errors.New("backend unavailable") })
	}
	if _, err := svc.GeneratePlan(context.Background(), request("Recursion", "Polymorphism", "Arrays")); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
	if svc.breaker.State() != gobreaker.StateHalfOpen || svc.breaker.Counts().TotalSuccesses != 0 || svc.breaker.Counts().TotalExclusions != 2 {
		t.Fatalf("canceled probes changed health: state=%v counts=%+v", svc.breaker.State(), svc.breaker.Counts())
	}
}

func TestPrerequisiteBeforeEarlierRequestedDependent(t *testing.T) {
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	})
	got, err := svc.GeneratePlan(context.Background(), request("Polymorphism", "Inheritance", "Recursion"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Inheritance", "Polymorphism", "Recursion"}
	for i, item := range got.Items {
		if item.Topic != want[i] || item.Priority != i+1 {
			t.Fatalf("not prerequisite ordered: %+v", got.Items)
		}
	}
	if len(got.Items) != len(want) {
		t.Fatalf("missing items: %+v", got.Items)
	}
}

func TestReplaceHallucinatedPrerequisiteChains(t *testing.T) {
	svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: "Polymorphism", PrerequisiteChain: []string{"invented", "Recursion"}}, {Topic: "Inheritance", PrerequisiteChain: []string{"invented"}}, {Topic: "Variables", PrerequisiteChain: []string{"Polymorphism"}}}, nil
	})
	got, err := svc.GeneratePlan(context.Background(), request("Polymorphism"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Variables", "Inheritance", "Polymorphism"}
	if len(got.Items) != len(want) {
		t.Fatalf("missing items: %+v", got.Items)
	}
	for i, item := range got.Items {
		if item.Topic != want[i] || item.Priority != i+1 || !reflect.DeepEqual(item.PrerequisiteChain, svc.graph.GetPrerequisiteChain(item.Topic)) {
			t.Fatalf("untrusted chain or order: %+v", got.Items)
		}
	}
}

func TestHallucinationDropWarning(t *testing.T) {
	for _, dropped := range []bool{false, true} {
		t.Run(fmt.Sprint(dropped), func(t *testing.T) {
			var logs bytes.Buffer
			svc := NewReasoningService(graph.NewInMemoryGraph(), plannerFunc(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
				items := []domain.PlanItem{{Topic: "Recursion"}}
				if dropped {
					items = append(items, domain.PlanItem{Topic: "invented"})
				}
				return items, nil
			}), Options{LLMTimeout: time.Second, MaxFailedTopics: 10, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
			_, err := svc.GeneratePlan(WithRequestID(context.Background(), "validation-42"), request("Recursion"))
			if err != nil {
				t.Fatal(err)
			}
			if !dropped {
				if logs.Len() != 0 {
					t.Fatal(logs.String())
				}
				return
			}
			var event map[string]any
			if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if event["msg"] != "stage.validation" || event["level"] != "WARN" || event["request_id"] != "validation-42" || event["items_dropped"] != float64(1) {
				t.Fatal(event)
			}
		})
	}
}

func TestModelCaseVariantKept(t *testing.T) {
	svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: "recursion"}}, nil
	})
	got, err := svc.GeneratePlan(t.Context(), request("Recursion"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Topic != "Recursion" || !reflect.DeepEqual(got.Items[0].PrerequisiteChain, []string{"Functions"}) {
		t.Fatalf("expected one canonical topic, got %+v", got.Items)
	}
	if got.Meta.ItemsValidated != 1 {
		t.Fatal(got.Meta)
	}
}

func TestRequestCanonicalTopicsDeduplicated(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		mu.Lock()
		calls = append(calls, r.Topic)
		mu.Unlock()
		return []domain.PlanItem{{Topic: r.Topic}}, nil
	})
	got, err := svc.GeneratePlan(t.Context(), request("recursion", " Recursion ", "RECURSION"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"Recursion"}) || got.Meta.TopicsProcessed != 1 || len(got.Items) != 1 || got.Items[0].Topic != "Recursion" {
		t.Fatalf("calls=%v response=%+v", calls, got)
	}
}

func TestModelCanonicalTopicsDeduplicated(t *testing.T) {
	var logs bytes.Buffer
	svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: "recursion"}, {Topic: " RECURSION "}, {Topic: "Recursion"}}, nil
	})
	svc.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	got, err := svc.GeneratePlan(t.Context(), request("Recursion"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Topic != "Recursion" || logs.Len() != 0 {
		t.Fatalf("items=%+v logs=%s", got.Items, logs.String())
	}
}

func TestGlobalConcurrencyBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var active, peak atomic.Int32
		release := make(chan struct{})
		svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			<-release
			return []domain.PlanItem{{Topic: r.Topic}}, nil
		})
		topics := []string{"Variables", "Functions", "Recursion", "Arrays", "Hash Tables", "Objects", "Classes", "Inheritance", "Polymorphism", "Merge Sort"}
		done := make(chan error, 30)
		for range 30 {
			go func() { _, err := svc.GeneratePlan(t.Context(), request(topics...)); done <- err }()
		}
		synctest.Wait()
		got := peak.Load()
		close(release)
		for range 30 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if got != 16 {
			t.Fatalf("peak concurrent calls = %d, want 16", got)
		}
	})
}

func TestQueueTimeoutReturnsUnavailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release, entered := make(chan struct{}), make(chan struct{})
		svc := NewReasoningService(graph.NewInMemoryGraph(), plannerFunc(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
			close(entered)
			<-release
			return []domain.PlanItem{{Topic: r.Topic}}, nil
		}), Options{LLMTimeout: time.Second, MaxFailedTopics: 10, MaxConcurrency: 1, QueueTimeout: 50 * time.Millisecond})
		done := make(chan error, 1)
		go func() { _, err := svc.GeneratePlan(t.Context(), request("Recursion")); done <- err }()
		<-entered
		start := time.Now()
		_, err := svc.GeneratePlan(t.Context(), request("Recursion"))
		close(release)
		if firstErr := <-done; firstErr != nil {
			t.Fatal(firstErr)
		}
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) || unavailable.RetryAfter != time.Second || errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 50*time.Millisecond {
			t.Fatalf("error=%v elapsed=%v", err, time.Since(start))
		}
		if svc.breaker.Counts().Requests != 1 {
			t.Fatalf("queue wait reached breaker: %+v", svc.breaker.Counts())
		}
	})
}

func TestCanceledQueueWaitPreservesCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release, entered := make(chan struct{}), make(chan struct{})
		svc := NewReasoningService(graph.NewInMemoryGraph(), plannerFunc(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
			close(entered)
			<-release
			return []domain.PlanItem{{Topic: r.Topic}}, nil
		}), Options{LLMTimeout: time.Second, MaxFailedTopics: 10, MaxConcurrency: 1, QueueTimeout: 50 * time.Millisecond})
		done := make(chan error, 1)
		go func() { _, err := svc.GeneratePlan(t.Context(), request("Recursion")); done <- err }()
		<-entered
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := svc.GeneratePlan(ctx, request("Recursion"))
		close(release)
		if firstErr := <-done; firstErr != nil {
			t.Fatal(firstErr)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestPlannerIgnoringContextStillTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finished := make(chan struct{})
		var finishedOnce sync.Once
		svc := NewReasoningService(graph.NewInMemoryGraph(), plannerFunc(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
			time.Sleep(2 * time.Second)
			finishedOnce.Do(func() { close(finished) })
			return []domain.PlanItem{{Topic: r.Topic}}, nil
		}), Options{LLMTimeout: 50 * time.Millisecond, MaxFailedTopics: 10, MaxConcurrency: 1, QueueTimeout: 10 * time.Millisecond})
		start := time.Now()
		_, err := svc.GeneratePlan(t.Context(), request("Recursion"))
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 50*time.Millisecond {
			t.Errorf("error=%v elapsed=%v", err, time.Since(start))
		}
		_, err = svc.GeneratePlan(t.Context(), request("Recursion"))
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) || unavailable.RetryAfter != time.Second {
			t.Errorf("blocked call released capacity: %v", err)
		}
		<-finished
		synctest.Wait()
		if !svc.slots.TryAcquire(1) {
			t.Fatal("finished call retained capacity")
		}
		svc.slots.Release(1)
	})
}

func TestMissingTopicItemFailsAsInvalidOutput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []domain.PlanItem
	}{
		{"empty", nil}, {"prerequisite only", []domain.PlanItem{{Topic: "Functions"}}}, {"unrelated", []domain.PlanItem{{Topic: "Hash Tables"}}}, {"unknown", []domain.PlanItem{{Topic: "invented"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) { return tc.items, nil })
			svc.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			for range 5 {
				_, err := svc.GeneratePlan(WithRequestID(t.Context(), "missing-42"), request("Recursion"))
				var topicErr *TopicError
				if !errors.Is(err, ErrInvalidOutput) || !errors.As(err, &topicErr) || topicErr.Topic != "Recursion" {
					t.Fatalf("error=%v", err)
				}
			}
			counts := svc.breaker.Counts()
			if counts.TotalExclusions != 5 || counts.TotalFailures != 0 || counts.TotalSuccesses != 0 || svc.breaker.State() != gobreaker.StateClosed {
				t.Fatalf("counts=%+v state=%v", counts, svc.breaker.State())
			}
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var event map[string]any
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				if event["msg"] != "stage.validation" || event["level"] != "WARN" || event["topic"] != "Recursion" || event["request_id"] != "missing-42" {
					t.Fatalf("event=%v", event)
				}
			}
		})
	}
}

func TestIncompleteRequestedTopicsFail(t *testing.T) {
	svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		if r.Topic == "Recursion" {
			return []domain.PlanItem{{Topic: r.Topic}}, nil
		}
		return []domain.PlanItem{{Topic: "Functions"}}, nil
	})
	_, err := svc.GeneratePlan(t.Context(), request("Recursion", "Polymorphism"))
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("error=%v", err)
	}
}
