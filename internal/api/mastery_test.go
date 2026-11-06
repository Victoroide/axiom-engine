package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
)

func TestMasteryHTTPValidationAndStates(t *testing.T) {
	cfg, err := config.Load(func(k string) string {
		switch k {
		case "AXIOM_AUTH_DISABLED":
			return "true"
		case "MAX_MASTERED_TOPICS":
			return "1"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "?topics=Functions&mastered=Variables", "", 200},
		{"GET", "?topics=Functions&mastered=Unknown", "", 422},
		{"GET", "?topics=Functions&mastered=functions", "", 400},
		{"GET", "?topics=Functions&mastered=Variables&mastered=Objects", "", 400},
		{"POST", "", `{"failed_topics":["Recursion"],"mastered_topics":["Unknown"],"vark_profile":"visual"}`, 422},
		{"POST", "", `{"failed_topics":["Recursion"],"mastered_topics":["recursion"],"vark_profile":"visual"}`, 400},
		{"POST", "", `{"failed_topics":["Recursion"],"mastered_topics":["Variables","Objects"],"vark_profile":"visual"}`, 400},
		{"POST", "", `{"failed_topics":["Recursion"],"mastered_topics":[null],"vark_profile":"visual"}`, 400},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			path := "/api/v1/adaptive-plan"
			if tc.method == "GET" {
				path = "/api/v1/tutor/student/student-42/cognitive-graph" + tc.path
			}
			req := httptest.NewRequestWithContext(t.Context(), tc.method, path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.status {
				data, _ := io.ReadAll(resp.Body)
				t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, tc.status, data)
			}
			if tc.status == http.StatusOK {
				var got domain.CognitiveGraphResponse
				if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				states := map[string]string{}
				for _, n := range got.Nodes {
					states[n.ID] = n.CognitiveState
				}
				if states["Functions"] != "failed" || states["Recursion"] != "unassessed" {
					t.Fatalf("states=%v", states)
				}
			}
		})
	}
}

func TestMasteryQueryStates(t *testing.T) {
	cfg, err := config.Load(func(k string) string {
		switch k {
		case "AXIOM_AUTH_DISABLED":
			return "true"
		case "MAX_MASTERED_TOPICS":
			return "2"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	for _, tc := range []struct {
		query, state, details string
		status                int
	}{
		{query: "mastered=Merge%20Sort,Dynamic%20Programming", state: "mastered", status: 200},
		{query: "mastered=", state: "unassessed", status: 200},
		{query: "mastered=,", state: "unassessed", status: 200},
		{query: "mastered=Unknown", details: `mastered contains unknown topics: "Unknown"`, status: 422},
	} {
		t.Run(tc.query, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/tutor/student/student-42/cognitive-graph?topics=Recursion&"+tc.query, nil)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.status {
				data, _ := io.ReadAll(resp.Body)
				t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, tc.status, data)
			}
			if tc.status != http.StatusOK {
				var got domain.ErrorResponse
				if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if got.Error != "validation_error" || got.Details != tc.details {
					t.Fatalf("error=%+v want details=%q", got, tc.details)
				}
				return
			}
			var got domain.CognitiveGraphResponse
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			states := make(map[string]string, len(got.Nodes))
			for _, node := range got.Nodes {
				states[node.ID] = node.CognitiveState
			}
			want := map[string]string{"Recursion": "failed", "Functions": "learning", "Merge Sort": tc.state, "Dynamic Programming": tc.state}
			if !reflect.DeepEqual(states, want) {
				t.Fatalf("states=%v want=%v", states, want)
			}
		})
	}
}
