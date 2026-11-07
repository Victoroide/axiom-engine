package config

import "testing"

func TestTracingRejectsInvalidBoolean(t *testing.T) {
	_, err := Load(func(k string) string {
		switch k {
		case "AXIOM_AUTH_DISABLED":
			return "true"
		case "TRACING_ENABLED":
			return "yes"
		}
		return ""
	})
	if err == nil {
		t.Fatal("accepted TRACING_ENABLED=yes")
	}
}

func TestTracingDefaults(t *testing.T) {
	for _, value := range []string{"", "false", "true"} {
		c, err := Load(func(k string) string {
			switch k {
			case "AXIOM_AUTH_DISABLED":
				return "true"
			case "TRACING_ENABLED":
				return value
			}
			return ""
		})
		if err != nil || c.Tracing.Enabled != (value == "true") {
			t.Fatalf("value=%q enabled=%v err=%v", value, c.Tracing.Enabled, err)
		}
	}
}
