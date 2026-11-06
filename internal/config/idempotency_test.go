package config

import (
	"strings"
	"testing"
	"time"
)

func TestIdempotencyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		invalid    bool
	}{
		{"", "", false}, {"IDEMPOTENCY_TTL", "0s", false}, {"IDEMPOTENCY_TTL", "5m", false},
		{"IDEMPOTENCY_TTL", "-1s", true}, {"IDEMPOTENCY_TTL", "bad", true},
		{"IDEMPOTENCY_MAX_ENTRIES", "1", false}, {"IDEMPOTENCY_MAX_ENTRIES", "0", true}, {"IDEMPOTENCY_MAX_ENTRIES", "bad", true},
	} {
		cfg, err := Load(func(key string) string {
			if key == "AXIOM_AUTH_DISABLED" {
				return "true"
			}
			if key == tc.key {
				return tc.value
			}
			return ""
		})
		if tc.invalid {
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("%s=%s err=%v", tc.key, tc.value, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if tc.key == "" && (cfg.Idempotency.TTL != 24*time.Hour || cfg.Idempotency.MaxEntries != 10000) {
			t.Fatalf("defaults=%+v", cfg.Idempotency)
		}
	}
}
