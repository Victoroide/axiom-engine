package config

import (
	"testing"
	"time"
)

func TestResourceLinkSettingsRejectInvalidValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"RESOURCE_LINK_CHECK", "yes"}, {"RESOURCE_LINK_CHECK", "TRUE"}, {"RESOURCE_LINK_TIMEOUT", "0s"}, {"RESOURCE_LINK_TIMEOUT", "-1s"}, {"RESOURCE_LINK_TIMEOUT", "soon"}} {
		if _, err := loadWithoutAuth(func(k string) string {
			if k == tc.key {
				return tc.value
			}
			return ""
		}); err == nil {
			t.Errorf("accepted %s=%q", tc.key, tc.value)
		}
	}
}

func TestResourceLinkDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		enabled, timeout string
		wantEnabled      bool
		wantTimeout      time.Duration
	}{{"", "", false, 3 * time.Second}, {"true", "10ms", true, 10 * time.Millisecond}} {
		got, err := loadWithoutAuth(func(k string) string {
			switch k {
			case "RESOURCE_LINK_CHECK":
				return tc.enabled
			case "RESOURCE_LINK_TIMEOUT":
				return tc.timeout
			}
			return ""
		})
		if err != nil || got.ResourceLinks.Enabled != tc.wantEnabled || got.ResourceLinks.Timeout != tc.wantTimeout {
			t.Fatalf("config=%+v error=%v", got.ResourceLinks, err)
		}
	}
}
