package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestStrictJSONDecoder(t *testing.T) {
	for _, tc := range []struct {
		name, body, detail string
	}{
		{"unknown field", `{"failed_topics":["Recursion"],"vark_profile":"visual","admin":true}`, `unknown field "admin"`},
		{"duplicate key", `{"failed_topics":["Nope"],"failed_topics":["Recursion"],"vark_profile":"visual"}`, `duplicate field "failed_topics"`},
		{"escaped duplicate key", `{"failed_topics":["Nope"],"failed_\u0074opics":["Recursion"],"vark_profile":"visual"}`, `duplicate field "failed_topics"`},
		{"nested duplicate key", `{"failed_topics":["Recursion"],"vark_profile":"visual","telemetry":{"session_id":"first","session_id":"last"}}`, `duplicate field "session_id"`},
		{"case mismatch", `{"FAILED_TOPICS":["Recursion"],"vark_profile":"visual"}`, `unknown field "FAILED_TOPICS"`},
		{"nested case mismatch", `{"failed_topics":["Recursion"],"vark_profile":"visual","telemetry":{"Session_ID":"x"}}`, `unknown field "Session_ID"`},
		{"nested unknown field", `{"failed_topics":["Recursion"],"vark_profile":"visual","telemetry":{"admin":true}}`, `unknown field "admin"`},
		{"null element", `{"failed_topics":[null,"Recursion"],"vark_profile":"visual"}`, "failed_topics must not contain null elements"},
		{"trailing object", `{"failed_topics":["Recursion"],"vark_profile":"visual"} {}`, "request body must contain exactly one JSON value"},
		{"trailing data", `{"failed_topics":["Recursion"],"vark_profile":"visual"} junk`, "request body must contain exactly one JSON value"},
		{"null body", `null`, "request body must be a JSON object"},
		{"wrong type", `{"failed_topics":"Recursion","vark_profile":"visual"}`, "request body must match the JSON schema"},
		{"long field name", `{"` + strings.Repeat("界", 1000) + `":true}`, `unknown field "` + strings.Repeat("界", 64) + `…"`},
		{"out of range number", `{"failed_topics":["Recursion"],"vark_profile":"visual","telemetry":{"timestamp_unix":1e400}}`, "request body must match the JSON schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(func(k string) string {
				if k == "AXIOM_AUTH_DISABLED" {
					return "true"
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			app := server.New(cfg, server.Dependencies{Planner: planner(func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
				return []domain.PlanItem{{Topic: r.Topic}}, nil
			}), Graph: graph.NewInMemoryGraph(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
			req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/adaptive-plan", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var body domain.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusBadRequest || body.Error != "invalid_request_body" || body.Details != tc.detail {
				t.Fatalf("got %d %+v, want 400 invalid_request_body: %s", resp.StatusCode, body, tc.detail)
			}
		})
	}
}
