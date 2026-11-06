package contract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestMasteryContract(t *testing.T) {
	_, router := loadContract(t)
	app := newApp(t, nil, func(_ context.Context, r service.TopicRequest) ([]domain.PlanItem, error) {
		return []domain.PlanItem{{Topic: r.Topic}, {Topic: "Functions"}}, nil
	})
	req := request(http.MethodPost, planPath, `{"failed_topics":["Recursion"],"mastered_topics":["Functions"],"vark_profile":"visual"}`)
	resp := call(t, app, req, 200, "")
	validateResponse(t, router, req, resp)
	var got map[string]any
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got["_meta"].(map[string]any)["items_skipped_mastered"] != float64(1) || len(got["items"].([]any)) != 1 {
		t.Fatalf("body=%s", resp.Body)
	}
	req = request(http.MethodGet, graphPath+"&mastered=Functions", "")
	resp = call(t, app, req, 200, "")
	validateResponse(t, router, req, resp)
}
