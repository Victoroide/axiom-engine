package contract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestResourceLinkMetadataContract(t *testing.T) {
	_, router := loadContract(t)
	app := newApp(t, func(c *config.Config) { c.ResourceLinks.Enabled = true }, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic, Resources: []domain.Resource{{Title: "Private guide", URL: "http://127.0.0.1/private", ResourceType: "video"}}}}, nil
	})
	req := request(http.MethodPost, planPath, validBody)
	resp := call(t, app, req, 200, "")
	validateResponse(t, router, req, resp)
	var got domain.PlanResponse
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Meta.ResourcesUnreachable != 1 || len(got.Items) != 1 || len(got.Items[0].Resources) != 0 {
		t.Fatalf("response=%s", resp.Body)
	}
}
