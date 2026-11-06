package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestPlanStreamHonorsMasteredTopicsLikeJSON(t *testing.T) {
	cfg := defaultConfig(t)
	cfg.Mastery.MaxTopics = 1
	var calls atomic.Int32
	app := newTestApp(t, cfg, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		calls.Add(1)
		if len(r.PrerequisiteOrder) != 0 {
			t.Errorf("mastered prerequisites reached planner: %v", r.PrerequisiteOrder)
		}
		return []domain.PlanItem{{Topic: r.Topic}, {Topic: "Functions"}}, nil
	}, nil)
	body := `{"student_id":"student-42","failed_topics":["Recursion"],"mastered_topics":[" functions ","Functions"],"vark_profile":"visual"}`
	jsonResp, jsonBody := call(t, app, http.MethodPost, "/api/v1/adaptive-plan", body, nil)
	streamResp, streamBody := call(t, app, http.MethodPost, "/api/v1/adaptive-plan/stream", body, nil)
	if jsonResp.StatusCode != http.StatusOK || streamResp.StatusCode != http.StatusOK {
		t.Fatalf("JSON=%d %s stream=%d %s", jsonResp.StatusCode, jsonBody, streamResp.StatusCode, streamBody)
	}
	events := parseEvents(t, streamBody)
	if len(events) != 2 || events[0].name != "topic" || events[1].name != "plan" {
		t.Fatalf("events=%+v", events)
	}
	var expected, got domain.PlanResponse
	if err := json.Unmarshal([]byte(jsonBody), &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(events[1].data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Topic != "Recursion" || got.Meta.ItemsSkippedMastered != 1 || got.Meta.ItemsDropped != 1 {
		t.Fatalf("plan=%+v", got)
	}
	expected.Meta.LLMLatencyMs = 0
	expected.Meta.TotalLatencyMs = 0
	got.Meta.LLMLatencyMs = 0
	got.Meta.TotalLatencyMs = 0
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("stream plan=%+v JSON plan=%+v", got, expected)
	}
	for _, tc := range []struct {
		mastered string
		status   int
	}{
		{`["Unknown"]`, http.StatusUnprocessableEntity},
		{`["recursion"]`, http.StatusBadRequest},
		{`["Functions","Variables"]`, http.StatusBadRequest},
	} {
		invalid := `{"failed_topics":["Recursion"],"mastered_topics":` + tc.mastered + `,"vark_profile":"visual"}`
		expected, expectedBody := call(t, app, http.MethodPost, "/api/v1/adaptive-plan", invalid, nil)
		actual, actualBody := call(t, app, http.MethodPost, "/api/v1/adaptive-plan/stream", invalid, nil)
		if expected.StatusCode != tc.status || actual.StatusCode != tc.status || expectedBody != actualBody || !strings.HasPrefix(actual.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("mastered=%s JSON=%d %s stream=%d %s", tc.mastered, expected.StatusCode, expectedBody, actual.StatusCode, actualBody)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("invalid mastery reached planner: calls=%d", calls.Load())
	}
}
