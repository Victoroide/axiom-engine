package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type resourcePlanner struct{ url string }

func (p resourcePlanner) GenerateTopic(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
	return []domain.PlanItem{{Topic: r.Topic, Resources: []domain.Resource{{Title: "Guide", URL: p.url, ResourceType: "video"}}}}, nil
}

func TestResourceLinkConfigurationControlsOutboundChecks(t *testing.T) {
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(http.StatusOK) }))
	defer remote.Close()
	for _, enabled := range []bool{false, true} {
		cfg, err := config.Load(func(k string) string {
			switch k {
			case "AXIOM_AUTH_DISABLED":
				return "true"
			case "RESOURCE_LINK_CHECK":
				if enabled {
					return "true"
				}
				return "false"
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Planner: resourcePlanner{url: remote.URL}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/adaptive-plan", strings.NewReader(`{"failed_topics":["Recursion"],"vark_profile":"visual"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, data)
		}
		var got domain.PlanResponse
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		want := 1
		if enabled {
			want = 0
		}
		if len(got.Items) != 1 || len(got.Items[0].Resources) != want {
			t.Fatalf("enabled=%v plan=%s", enabled, data)
		}
		var envelope map[string]any
		_ = json.Unmarshal(data, &envelope)
		meta := envelope["_meta"].(map[string]any)
		if enabled && meta["resources_unreachable"] != float64(1) {
			t.Fatalf("metadata=%v", meta)
		}
		if !enabled {
			if _, ok := meta["resources_unreachable"]; ok {
				t.Fatal("disabled check changed metadata")
			}
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("outbound requests=%d", hits.Load())
	}
}
