package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

func TestItemsOutsideRequestedClosureAreDropped(t *testing.T) {
	var logs bytes.Buffer
	svc := newService(func(context.Context, TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: "Recursion"}, {Topic: "Hash Tables"}, {Topic: "functions"}}, nil
	})
	svc.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	got, err := svc.GeneratePlan(t.Context(), request("Recursion"))
	if err != nil {
		t.Fatal(err)
	}
	topics := make([]string, 0, len(got.Items))
	for _, item := range got.Items {
		topics = append(topics, item.Topic)
	}
	if !reflect.DeepEqual(topics, []string{"Functions", "Recursion"}) {
		t.Fatalf("topics=%v", topics)
	}
	data, err := json.Marshal(got.Meta)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["items_dropped"] != float64(1) || meta["items_after_validation"] != float64(2) {
		t.Fatalf("metadata=%s", data)
	}
	if !strings.Contains(logs.String(), `"items_dropped":1`) {
		t.Fatal(logs.String())
	}
}

func TestRequestedClosureIncludesAllPrerequisites(t *testing.T) {
	g, err := graph.Decode(strings.NewReader(`[{"subject":"A","relation":"depends_on","object":"B"},{"subject":"B","relation":"depends_on","object":"C"},{"subject":"C","relation":"depends_on","object":"D"},{"subject":"Other","relation":"depends_on","object":"Root"}]`))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewReasoningService(g, plannerFunc(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic}, {Topic: "b"}, {Topic: "c"}, {Topic: "d"}, {Topic: "Other"}}, nil
	}), Options{LLMTimeout: time.Second, MaxFailedTopics: 10})
	got, err := svc.GeneratePlan(t.Context(), request("a"))
	if err != nil {
		t.Fatal(err)
	}
	topics := make([]string, 0, len(got.Items))
	for _, item := range got.Items {
		topics = append(topics, item.Topic)
	}
	if !reflect.DeepEqual(topics, []string{"D", "C", "B", "A"}) {
		t.Fatalf("topics=%v", topics)
	}
}
