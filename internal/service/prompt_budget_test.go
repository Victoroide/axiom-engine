package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

func budgetFixture(t *testing.T, edges []domain.GraphTuple, budget config.PromptBudget, planner Planner) *ReasoningService {
	t.Helper()
	data, err := json.Marshal(edges)
	if err != nil {
		t.Fatal(err)
	}
	g, err := graph.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return NewReasoningService(g, planner, Options{PromptBudget: budget, MaxFailedTopics: 10, LLMTimeout: time.Second})
}

func TestPromptBudgetCountsUniqueBoundaryEdges(t *testing.T) {
	edges := make([]domain.GraphTuple, 0, 259)
	for i := range 16 {
		hub := fmt.Sprintf("Hub%02d", i)
		edges = append(edges, domain.GraphTuple{Subject: hub, Relation: "depends_on", Object: "Root"})
		for j := range 15 {
			edges = append(edges, domain.GraphTuple{Subject: fmt.Sprintf("Leaf%02d", j), Relation: "is_a", Object: hub})
		}
	}
	edges = append(edges, edges[0], domain.GraphTuple{Subject: "Beyond", Relation: "is_a", Object: "Far"})
	for _, over := range []bool{false, true} {
		t.Run(fmt.Sprintf("over=%t", over), func(t *testing.T) {
			if over {
				edges = append(edges, domain.GraphTuple{Subject: "Far", Relation: "is_a", Object: "Leaf00"})
			}
			var calls atomic.Int32
			svc := budgetFixture(t, edges, config.DefaultPromptBudget(), plannerFunc(func(_ context.Context, req TopicRequest) ([]domain.PlanItem, error) {
				calls.Add(1)
				if len(req.GraphContext) != 256 || req.PrerequisiteOrder == nil || len(req.PrerequisiteOrder) != 0 {
					t.Errorf("tuples = %d, prerequisites = %#v", len(req.GraphContext), req.PrerequisiteOrder)
				}
				return []domain.PlanItem{{Topic: req.Topic}}, nil
			}))
			out, err := svc.GeneratePlan(t.Context(), domain.AdaptivePlanRequest{FailedTopics: []string{"Root"}, VARKProfile: "visual"})
			if over {
				requirePromptRejection(t, err, calls.Load())
				return
			}
			if err != nil || calls.Load() != 1 || out.Meta.SubgraphTuples != 256 || len(out.Items) != 1 || out.Items[0].Priority != 1 {
				t.Fatalf("plan = %+v, error = %v, calls = %d", out, err, calls.Load())
			}
		})
	}
}

func TestPromptBudgetCountsPrerequisitesBeyondSubgraph(t *testing.T) {
	edges := make([]domain.GraphTuple, 13)
	for i := range edges {
		edges[i] = domain.GraphTuple{Subject: fmt.Sprintf("Node%02d", i), Relation: "depends_on", Object: fmt.Sprintf("Node%02d", i+1)}
	}
	for _, tc := range []struct {
		name     string
		edges    []domain.GraphTuple
		mastered []string
		reject   bool
	}{
		{name: "at cap", edges: edges[:12]},
		{name: "one over", edges: edges, reject: true},
		{name: "mastered entry excluded", edges: edges, mastered: []string{" node13 "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			svc := budgetFixture(t, tc.edges, config.DefaultPromptBudget(), plannerFunc(func(_ context.Context, req TopicRequest) ([]domain.PlanItem, error) {
				calls.Add(1)
				want := make([]string, 12)
				for i := range want {
					want[i] = fmt.Sprintf("Node%02d", 12-i)
				}
				if len(req.GraphContext) != 3 || !reflect.DeepEqual(req.PrerequisiteOrder, want) {
					t.Errorf("graph = %v, prerequisites = %v, want %v", req.GraphContext, req.PrerequisiteOrder, want)
				}
				return []domain.PlanItem{{Topic: req.Topic}, {Topic: "Node01"}}, nil
			}))
			out, err := svc.GeneratePlan(t.Context(), domain.AdaptivePlanRequest{FailedTopics: []string{"Node00"}, MasteredTopics: tc.mastered, VARKProfile: "visual"})
			if tc.reject {
				requirePromptRejection(t, err, calls.Load())
				return
			}
			if err != nil || calls.Load() != 1 || len(out.Items) != 2 || out.Meta.ItemsSkippedMastered != len(tc.mastered) {
				t.Fatalf("plan = %+v, error = %v, calls = %d", out, err, calls.Load())
			}
			first, last := out.Items[0], out.Items[1]
			if first.Topic != "Node01" || first.Priority != 1 || last.Topic != "Node00" || last.Priority != 2 || len(last.PrerequisiteChain) != len(tc.edges) {
				t.Fatalf("unexpected ranks or full chain: %+v", out.Items)
			}
		})
	}
}

func TestPromptBudgetCountsEscapedUTF8(t *testing.T) {
	name := strings.Repeat("𐐷界\\\"<&", 20) + "δ"
	const nameBytes = 20*(4+3+2+2+6+6) + 2
	const size = config.PromptScaffoldingBytes + len("visualRoot") + len(`Root -[depends_on]-> \n`) + len(`1. \n`) + 2*nameBytes
	edge := domain.GraphTuple{Subject: "Root", Relation: "depends_on", Object: name}
	for _, cap := range []int{size, size - 1} {
		t.Run(fmt.Sprint(cap), func(t *testing.T) {
			var calls atomic.Int32
			budget := config.DefaultPromptBudget()
			budget.MaxTopicBytes = cap
			svc := budgetFixture(t, []domain.GraphTuple{edge}, budget, plannerFunc(func(_ context.Context, req TopicRequest) ([]domain.PlanItem, error) {
				calls.Add(1)
				if !reflect.DeepEqual(req.GraphContext, []domain.GraphTuple{edge}) || !reflect.DeepEqual(req.PrerequisiteOrder, []string{name}) {
					t.Error("the curriculum bytes changed")
				}
				return []domain.PlanItem{{Topic: req.Topic}}, nil
			}))
			_, err := svc.GeneratePlan(t.Context(), domain.AdaptivePlanRequest{FailedTopics: []string{"Root"}, VARKProfile: "visual"})
			if cap < size {
				requirePromptRejection(t, err, calls.Load())
			} else if err != nil || calls.Load() != 1 {
				t.Fatalf("error = %v, calls = %d", err, calls.Load())
			}
		})
	}
}

func TestPromptBudgetSmallestRequest(t *testing.T) {
	env := map[string]string{"AXIOM_AUTH_DISABLED": "true", "LLM_MAX_TOPIC_INPUT_BYTES": "3094", "LLM_MAX_REQUEST_INPUT_BYTES": "3094"}
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	planner := plannerFunc(func(_ context.Context, req TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		return []domain.PlanItem{{Topic: req.Topic}}, nil
	})
	edge := domain.GraphTuple{Subject: "B", Relation: "is_a", Object: "A"}
	svc := budgetFixture(t, []domain.GraphTuple{edge}, cfg.PromptBudget, planner)
	if _, err := svc.GeneratePlan(t.Context(), request("A")); err != nil || calls.Load() != 1 {
		t.Fatalf("smallest request: error = %v, calls = %d", err, calls.Load())
	}
	calls.Store(0)
	edge.Subject += "C"
	svc = budgetFixture(t, []domain.GraphTuple{edge}, cfg.PromptBudget, planner)
	_, err = svc.GeneratePlan(t.Context(), request("A"))
	requirePromptRejection(t, err, calls.Load())
}

func TestPromptBudgetExampleCurriculum(t *testing.T) {
	g, err := graph.Load("../../examples/graphs/computer-science.json")
	if err != nil {
		t.Fatal(err)
	}
	topics := g.Topics("")
	if len(topics) != 130 {
		t.Fatalf("example topics = %d, want 130", len(topics))
	}
	defaults := config.DefaultPromptBudget()
	for _, tc := range []struct{ cap, accepted int }{{defaults.MaxPrerequisites, 113}, {24, 130}} {
		budget := defaults
		budget.MaxPrerequisites = tc.cap
		svc := NewReasoningService(g, nil, Options{PromptBudget: budget, MaxFailedTopics: 10})
		accepted := 0
		for _, topic := range topics {
			entries := len(g.GetPrerequisiteChain(topic.Name))
			_, err := svc.PreparePlan(t.Context(), request(topic.Name))
			switch {
			case entries > tc.cap:
				requirePromptRejection(t, err, 0)
			case err != nil:
				t.Fatalf("topic %q with %d prerequisites: %v", topic.Name, entries, err)
			default:
				accepted++
			}
		}
		if accepted != tc.accepted {
			t.Fatalf("prerequisite cap %d: admitted %d, want %d", tc.cap, accepted, tc.accepted)
		}
		t.Logf("%d of %d example topics admitted with a %d-entry prerequisite cap", accepted, len(topics), tc.cap)
	}
	for topic, want := range map[string]int{"Write Ahead Logging": 13, "Sharding": 24} {
		if got := len(g.GetPrerequisiteChain(topic)); got != want {
			t.Fatalf("%s prerequisites = %d, want %d", topic, got, want)
		}
	}
}

func TestPromptBudgetCountsDefaultFanOut(t *testing.T) {
	topics := []string{"Root00X", "Root01X", "Root02", "Root03", "Root04", "Root05", "Root06", "Root07", "Root08", "Root09"}
	// The ten topic names add 62 bytes after the shared graph and scaffolding.
	const aggregate = 131_072
	shared := (aggregate - 62) / len(topics)
	remaining := shared - config.PromptScaffoldingBytes - len("visual")
	prefix := strings.Repeat("界\\\"", 24)
	edges := make([]domain.GraphTuple, 50)
	for i := range edges {
		topic := topics[i%len(topics)]
		edges[i] = domain.GraphTuple{Subject: prefix + fmt.Sprintf("B%04d", i), Relation: "depends_on", Object: topic}
		remaining -= 24*7 + 5 + len(` -[depends_on]-> \n`) + len(topic)
	}
	for i := range edges {
		padding := min(remaining, 50)
		edges[i].Subject += strings.Repeat("x", padding)
		remaining -= padding
	}
	if remaining != 0 {
		t.Fatalf("fixture missed the cap by %d bytes per topic", remaining)
	}
	for _, atCap := range []bool{true, false} {
		t.Run(fmt.Sprintf("at_cap=%t", atCap), func(t *testing.T) {
			budget := config.DefaultPromptBudget()
			if !atCap {
				budget.MaxRequestBytes--
			}
			var calls atomic.Int32
			svc := budgetFixture(t, edges, budget, plannerFunc(func(_ context.Context, req TopicRequest) ([]domain.PlanItem, error) {
				calls.Add(1)
				if len(req.GraphContext) != len(edges) || len(req.PrerequisiteOrder) != 0 {
					t.Errorf("graph = %d edges, prerequisites = %v", len(req.GraphContext), req.PrerequisiteOrder)
				}
				return []domain.PlanItem{{Topic: req.Topic}}, nil
			}))
			out, err := svc.GeneratePlan(t.Context(), request(topics...))
			if !atCap {
				requirePromptRejection(t, err, calls.Load())
				return
			}
			if err != nil || calls.Load() != 10 || len(out.Items) != 10 {
				t.Fatalf("plan = %+v, error = %v, calls = %d", out, err, calls.Load())
			}
			t.Logf("ten topics accepted at %d aggregate input bytes", aggregate)
		})
	}
}

func TestPromptBudgetPreservesValidationAndCancellation(t *testing.T) {
	budget := config.DefaultPromptBudget()
	budget.MaxEdges = 1
	svc := NewReasoningService(graph.NewInMemoryGraph(), plannerFunc(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
		t.Error("planner called before accepting input")
		return nil, nil
	}), Options{MaxFailedTopics: 10, PromptBudget: budget})
	for _, topic := range []string{"unknown", ""} {
		_, err := svc.GeneratePlan(t.Context(), request("Recursion", topic, "Polymorphism"))
		if topic == "" {
			requirePromptRejection(t, err, 0)
			continue
		}
		var validation *ValidationError
		if !errors.As(err, &validation) || !reflect.DeepEqual(validation.UnknownTopics, []string{"unknown"}) {
			t.Fatalf("validation error = %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := svc.GeneratePlan(ctx, request("Recursion")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func requirePromptRejection(t *testing.T, err error, calls int32) {
	t.Helper()
	var budget *PromptBudgetError
	if !errors.As(err, &budget) || calls != 0 {
		t.Fatalf("error = %v, calls = %d, want a prompt budget error and no calls", err, calls)
	}
}
