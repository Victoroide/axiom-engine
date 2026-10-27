package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type planner func(context.Context, service.TopicRequest) ([]domain.PlanItem, error)

func (f planner) GenerateTopic(ctx context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
	return f(ctx, r)
}

func TestHandlerResponses(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		failure            error
		want               int
	}{
		{"POST", "/api/v1/adaptive-plan", `{`, nil, 400},
		{"POST", "/api/v1/adaptive-plan", `{}`, nil, 400},
		{"POST", "/api/v1/adaptive-plan", `{"vark_profile":"visual","failed_topics":["unknown"]}`, nil, 422},
		{"POST", "/api/v1/adaptive-plan", `{"vark_profile":"visual","failed_topics":["Recursion"]}`, nil, 200},
		{"POST", "/api/v1/adaptive-plan", `{"vark_profile":"visual","failed_topics":["Recursion"]}`, context.DeadlineExceeded, 504},
		{"POST", "/api/v1/adaptive-plan", `{"vark_profile":"visual","failed_topics":["Recursion"]}`, service.ErrCircuitOpen, 503},
		{"POST", "/api/v1/adaptive-plan", `{"vark_profile":"visual","failed_topics":["Recursion"]}`, context.Canceled, 408},
		{"POST", "/api/v1/adaptive-plan", `{"vark_profile":"visual","failed_topics":["Recursion"]}`, errors.New("upstream secret"), 502},
		{"GET", "/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion", "", nil, 200},
		{"GET", "/api/v1/tutor/student/bad%20id/cognitive-graph?topics=Recursion", "", nil, 400},
		{"GET", "/api/v1/tutor/student/student-42/cognitive-graph?topics=", "", nil, 400},
		{"GET", "/api/v1/tutor/student/student-42/cognitive-graph?topics=unknown", "", nil, 422},
		{"GET", "/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion&target_topic=unknown", "", nil, 422},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			cfg, err := config.Load(func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Planner: planner(func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
				return []domain.PlanItem{{Topic: r.Topic}}, tc.failure
			})})
			req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.want {
				data, _ := io.ReadAll(resp.Body)
				t.Fatalf("%d: %s", resp.StatusCode, data)
			}
		})
	}
}
