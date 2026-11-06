package contract_test

import (
	"net/http"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/Victoroide/axiom-engine/internal/config"
)

func TestRedisFailureOnCurriculumRoutesMatchesContract(t *testing.T) {
	_, router := loadContract(t)
	redis := miniredis.RunT(t)
	app := newApp(t, func(cfg *config.Config) { cfg.Redis.URL = "redis://" + redis.Addr() }, nil)
	t.Cleanup(func() { _ = app.Shutdown() })
	redis.SetError("ERR unavailable")
	for _, path := range []string{"/api/v1/topics", "/api/v1/topics/Recursion/path"} {
		req := request(http.MethodGet, path, "")
		resp := call(t, app, req, http.StatusServiceUnavailable, "service_unavailable")
		validateResponse(t, router, req, resp)
	}
}
