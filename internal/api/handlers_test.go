package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
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

func TestCognitiveGraphResolvesTargetTopics(t *testing.T) {
	g, err := graph.Decode(strings.NewReader(`[{"subject":"Recursion","relation":"depends_on","object":"Functions"},{"subject":"Merge Sort","relation":"depends_on","object":"Recursion"},{"subject":"Recursión","relation":"depends_on","object":"Funciones"},{"subject":"Straße","relation":"depends_on","object":"Basis"},{"subject":"ΟΔΟΣ","relation":"depends_on","object":"Basis"}]`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ topics, target, stored string }{
		{"recursion", "recursion", "Recursion"},
		{"Recursion", "recursion", "Recursion"},
		{"merge  sort", "Merge\t\u00a0Sort", "Merge Sort"},
		{"Recursión", "RECURSIO\u0301N", "Recursión"},
		{"Recursion", "Recur\u200bsion", "Recursion"},
		{"STRASSE", "strasse", "Straße"},
		{"ΟΔΟΣ", "οδος", "ΟΔΟΣ"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			cfg, err := config.Load(func(k string) string {
				if k == "AXIOM_AUTH_DISABLED" {
					return "true"
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			app := server.New(cfg, server.Dependencies{Graph: g, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
			query := url.Values{"topics": {tc.topics}, "target_topic": {tc.target}}
			req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/tutor/student/student-42/cognitive-graph?"+query.Encode(), nil)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var got domain.CognitiveGraphResponse
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			want := g.GenerateCognitiveShadow([]string{tc.stored}, tc.stored)
			if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(got, want) {
				t.Fatalf("status = %d, graph = %+v; want %+v", resp.StatusCode, got, want)
			}
		})
	}
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
			cfg, err := config.Load(func(k string) string {
				if k == "AXIOM_AUTH_DISABLED" {
					return "true"
				}
				return ""
			})
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
