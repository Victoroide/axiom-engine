package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
)

func TestTopicRoutes(t *testing.T) {
	cfg, err := config.Load(func(key string) string {
		if key == "AXIOM_AUTH_DISABLED" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	for _, tc := range []struct {
		path   string
		status int
		want   string
	}{
		{"/api/v1/topics?q=" + url.QueryEscape("  mErGe\u00a0 sOrT "), 200, `{"topics":[{"name":"Merge Sort","prerequisites":["Recursion"],"dependents":[]}]}`},
		{"/api/v1/topics?q=absent", 200, `{"topics":[]}`},
		{"/api/v1/topics/" + url.PathEscape(" RECURSION ") + "/path", 200, `{"topic":"Recursion","prerequisite_chain":["Functions"],"dependents":["Dynamic Programming","Merge Sort"]}`},
		{"/api/v1/topics/Unknown/path", 422, `{"error":"validation_error"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var got, want map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d body=%v", resp.StatusCode, tc.status, got)
			}
			if tc.status != 200 {
				if got["error"] != want["error"] {
					t.Fatal(got)
				}
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got=%v want=%v", got, want)
			}
		})
	}
}
