package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/test/resourcelinkfixture"
)

type resourcePlanner struct {
	resources []domain.Resource
	delay     time.Duration
}

func (p resourcePlanner) GenerateTopic(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
	time.Sleep(p.delay)
	return []domain.PlanItem{{Topic: r.Topic, Resources: p.resources}}, nil
}

func TestResourceLinkConfigurationControlsOutboundChecks(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enabled       bool
		empty         bool
		defaultPolicy bool
		kept          int
		dropped       int
		wantChecks    int32
	}{
		{name: "disabled", kept: 2},
		{name: "enabled", enabled: true, dropped: 2, wantChecks: 1},
		{name: "default-policy", enabled: true, defaultPolicy: true, dropped: 1},
		{name: "empty", enabled: true, empty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := resourcelinkfixture.New(t)
			cfg, err := config.Load(func(k string) string {
				switch k {
				case "AXIOM_AUTH_DISABLED":
					return "true"
				case "RESOURCE_LINK_CHECK":
					if tc.enabled {
						return "true"
					}
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			planner := resourcePlanner{}
			if !tc.empty {
				planner.resources = fixture.Resources()
			}
			var policy service.ResourceLinkPolicy = fixture
			if tc.defaultPolicy {
				planner.resources = planner.resources[:1]
				policy = nil
			}
			if !tc.enabled {
				planner.delay = 6 * time.Second
			}
			app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Planner: planner, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), ResourceLinkPolicy: policy})
			exercise := func(t *testing.T) {
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/adaptive-plan", strings.NewReader(`{"failed_topics":["Recursion"],"vark_profile":"visual"}`))
				req.Header.Set("Content-Type", "application/json")
				resp, err := app.Test(req, fiber.TestConfig{})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				data, err := io.ReadAll(resp.Body)
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
				if len(got.Items) != 1 || got.Items[0].Topic != "Recursion" || got.Items[0].Resources == nil || len(got.Items[0].Resources) != tc.kept || got.Meta.ResourcesUnreachable != tc.dropped {
					t.Fatalf("plan=%s", data)
				}
				if fixture.Hits() != tc.wantChecks {
					t.Errorf("outbound requests=%d, want %d", fixture.Hits(), tc.wantChecks)
				}
				if !tc.enabled {
					stable := resourcelinkfixture.NormalizePlanLatency(string(data))
					want := fmt.Sprintf(`{"student_id":"","course_id":"","items":[{"topic":"Recursion","priority":1,"prerequisite_chain":["Functions"],"explanation":"","resources":[`+
						`{"title":"First","url":%[1]q,"resource_type":"video"},{"title":"Second","url":%[1]q,"resource_type":"diagram"}]}],`+
						`"_meta":{"subgraph_tuples":3,"topics_processed":1,"items_generated":1,"items_after_validation":1,"items_dropped":0,"llm_latency_ms":0,"total_latency_ms":0}}`, fixture.URL)
					if stable != want {
						t.Errorf("disabled response=%s\nwant=%s", stable, want)
					}
				}
			}
			if tc.enabled {
				exercise(t)
				return
			}
			// Start the HTTP server's background Date updater outside synthetic time.
			startup, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil), fiber.TestConfig{})
			if err != nil {
				t.Fatal(err)
			}
			_ = startup.Body.Close()
			synctest.Test(t, exercise)
		})
	}
}
