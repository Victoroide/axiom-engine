package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviousTokenTelemetry(t *testing.T) {
	original := previousTokenWarnings
	previousTokenWarnings = &previousTokenLogGate{}
	t.Cleanup(func() { previousTokenWarnings = original })
	cfg := metricsConfig(t)
	cfg.AuthDisabled = false
	cfg.InternalToken = strings.Repeat("a", 32)
	cfg.InternalTokenPrevious = strings.Repeat("b", 32)
	var logs bytes.Buffer
	app := newTestApp(t, cfg, nil, &logs)
	for _, headers := range []map[string]string{
		{"X-Internal-Token": cfg.InternalToken},
		{"X-Internal-Token": "invalid"},
		{"X-Internal-Token": "invalid", "Authorization": "Bearer " + cfg.InternalTokenPrevious},
		nil,
	} {
		call(t, app, "POST", "/api/v1/adaptive-plan", validBody, headers)
	}
	if strings.Contains(logs.String(), "auth.previous_token_used") {
		t.Fatal("previous-token warning emitted without a previous-token authentication")
	}
	_, body := call(t, app, "GET", "/metrics", "", nil)
	if !strings.Contains(body, "axiom_auth_previous_token_total 0\n") {
		t.Error("previous-token counter must start at zero")
	}
	for _, headers := range []map[string]string{
		{"X-Internal-Token": cfg.InternalTokenPrevious, "X-Request-ID": "rotation.first"},
		{"Authorization": "Bearer " + cfg.InternalTokenPrevious, "X-Request-ID": "rotation.second"},
	} {
		resp, body := call(t, app, "POST", "/api/v1/adaptive-plan", validBody, headers)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("previous-token status=%d body=%s", resp.StatusCode, body)
		}
	}
	_, body = call(t, app, "GET", "/metrics", "", nil)
	if !strings.Contains(body, "axiom_auth_previous_token_total 2\n") {
		t.Error("previous-token counter must include both accepted credentials")
	}
	// A second application shares the process warning limit without metrics.
	cfg.MetricsEnabled = false
	second := newTestApp(t, cfg, nil, &logs)
	resp, body := call(t, second, "POST", "/api/v1/adaptive-plan", validBody, map[string]string{"X-Internal-Token": cfg.InternalTokenPrevious})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics disabled: status=%d body=%s", resp.StatusCode, body)
	}
	var warnings int
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == "auth.previous_token_used" {
			warnings++
			if record["level"] != "WARN" || record["request_id"] != "rotation.first" {
				t.Errorf("warning=%v", record)
			}
		}
	}
	if warnings != 1 {
		t.Errorf("got %d previous-token warnings, want one per process", warnings)
	}
	if strings.Contains(logs.String(), cfg.InternalToken) || strings.Contains(logs.String(), cfg.InternalTokenPrevious) {
		t.Error("credentials appeared in logs")
	}
}

func TestPreviousTokenWarningInterval(t *testing.T) {
	gate := &previousTokenLogGate{}
	start := time.Now()
	for _, tc := range []struct {
		elapsed time.Duration
		want    bool
	}{
		{0, true},
		{time.Minute - time.Nanosecond, false},
		{time.Minute, true},
		{2*time.Minute - time.Nanosecond, false},
		{2 * time.Minute, true},
	} {
		if got := gate.allow(start.Add(tc.elapsed)); got != tc.want {
			t.Errorf("after %s: allow=%v want=%v", tc.elapsed, got, tc.want)
		}
	}
}

func TestPreviousTokenWarningConcurrent(t *testing.T) {
	gate := &previousTokenLogGate{}
	start := time.Now()
	var allowed atomic.Int32
	var group sync.WaitGroup
	for range 64 {
		group.Go(func() {
			if gate.allow(start) {
				allowed.Add(1)
			}
		})
	}
	group.Wait()
	if got := allowed.Load(); got != 1 {
		t.Errorf("allowed %d concurrent warnings, want 1", got)
	}
}
