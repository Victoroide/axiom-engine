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
	"sync"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/Victoroide/axiom-reasoning-svc/internal/domain"
	"github.com/Victoroide/axiom-reasoning-svc/internal/graph"
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
	svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) { return nil, nil })
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

func TestHalfOpenSaturation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
		close(entered)
		<-release
		return nil, nil
	})
	svc.breaker = gobreaker.NewCircuitBreaker[[]domain.PlanItem](gobreaker.Settings{MaxRequests: 1, Timeout: time.Nanosecond, ReadyToTrip: func(gobreaker.Counts) bool { return true }})
	_, _ = svc.breaker.Execute(func() ([]domain.PlanItem, error) { return nil, errors.New("failure") })
	done := make(chan error, 1)
	go func() { _, err := svc.GeneratePlan(context.Background(), request("Recursion")); done <- err }()
	<-entered
	if _, err := svc.GeneratePlan(context.Background(), request("Recursion")); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
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
