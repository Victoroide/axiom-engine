package llm

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Victoroide/axiom-engine/baml_client/types"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type cancelOnSpanStart struct {
	*tracetest.SpanRecorder
	cancel context.CancelFunc
}

func (p cancelOnSpanStart) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	p.SpanRecorder.OnStart(ctx, span)
	p.cancel()
}

type watchedCancellation struct {
	context.Context
	watched chan struct{}
}

func (c watchedCancellation) Done() <-chan struct{} {
	select {
	case c.watched <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func TestBAMLCancellationDuringSetupSkipsNativeCall(t *testing.T) {
	f := newProviderFixture(t, "openai", "ok")
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := watchedCancellation{Context: parent, watched: make(chan struct{}, 1)}
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(cancelOnSpanStart{recorder, cancel}))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = tp.Shutdown(t.Context()) }()

	items, err := New(f.settings).GenerateTopic(ctx, topicRequest())
	if items != nil || !errors.Is(err, context.Canceled) || f.count() != 0 {
		t.Errorf("items = %v, error = %v, attempts = %d; want cancellation before dispatch", items, err, f.count())
	}
	// The native client starts a goroutine watching Done whenever it is invoked.
	select {
	case <-ctx.watched:
		t.Error("native client watched the canceled context")
	default:
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Status().Code != codes.Error || spans[0].Status().Description != "canceled" {
		t.Fatalf("expected one canceled provider span, got %v", spans)
	}
}

func TestBAMLDoesNotDispatchAfterContextDone(t *testing.T) {
	adapter := &BAML{generate: func(context.Context, []types.GraphTuple, string, []string, string) (types.AdaptiveStudyPlan, error) {
		t.Fatal("planner called after context ended")
		return types.AdaptiveStudyPlan{}, nil
	}}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelExpired()
	for _, ctx := range []context.Context{canceled, expired} {
		items, err := adapter.GenerateTopic(ctx, topicRequest())
		if items != nil || !errors.Is(err, ctx.Err()) {
			t.Fatalf("items = %v, error = %v; want %v", items, err, ctx.Err())
		}
	}
}

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
