package config

import (
	"strings"
	"testing"
)

func loadTraceEnv(env map[string]string) (Config, error) {
	values := map[string]string{"AXIOM_AUTH_DISABLED": "true"}
	for key, value := range env {
		values[key] = value
	}
	return Load(func(key string) string { return values[key] })
}

// TestTraceContentDestinationMatrix covers every capture switch and list for each exporter combination.
func TestTraceContentDestinationMatrix(t *testing.T) {
	exporters := []struct {
		name string
		env  map[string]string
	}{
		{"none", nil},
		{"otlp", map[string]string{"TRACING_ENABLED": "true"}},
		{"langsmith", map[string]string{"LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "key"}},
		{"both", map[string]string{"TRACING_ENABLED": "true", "LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "key"}},
	}
	enabled := func(name, destination string) bool {
		return name == "both" || name == destination
	}
	for _, exporter := range exporters {
		for _, capture := range []string{"", "true", "false"} {
			for _, list := range []string{"", " ", "langsmith", "otlp", "langsmith,otlp", "otlp,langsmith", " langsmith , otlp "} {
				t.Run(exporter.name+"/capture="+capture+"/list="+list, func(t *testing.T) {
					env := map[string]string{"TRACING_CAPTURE_CONTENT": capture, "TRACING_CONTENT_DESTINATIONS": list}
					for key, value := range exporter.env {
						env[key] = value
					}
					entries := []string{}
					for entry := range strings.SplitSeq(list, ",") {
						if entry = strings.TrimSpace(entry); entry != "" {
							entries = append(entries, entry)
						}
					}
					valid := true
					for _, entry := range entries {
						valid = valid && enabled(exporter.name, entry)
					}
					c, err := loadTraceEnv(env)
					if !valid {
						if err == nil {
							t.Fatalf("accepted a destination whose exporter is disabled: %+v", c.Tracing)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					wantLangSmith, wantOTLP := false, false
					if capture != "false" {
						for _, entry := range entries {
							wantLangSmith = wantLangSmith || entry == "langsmith"
							wantOTLP = wantOTLP || entry == "otlp"
						}
					}
					if c.Tracing.Content.LangSmith != wantLangSmith || c.Tracing.Content.OTLP != wantOTLP || c.Tracing.CaptureContent() != (wantLangSmith || wantOTLP) {
						t.Fatalf("content=%+v want langsmith=%v otlp=%v", c.Tracing.Content, wantLangSmith, wantOTLP)
					}
				})
			}
		}
	}
}

func TestTraceContentRequiresExplicitDestination(t *testing.T) {
	for _, env := range []map[string]string{
		{"LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "key"},
		{"LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "key", "TRACING_CAPTURE_CONTENT": "true"},
		{"LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "key", "TRACING_CAPTURE_CONTENT": "true", "TRACING_CONTENT_DESTINATIONS": ""},
		{"TRACING_ENABLED": "true", "TRACING_CAPTURE_CONTENT": "true"},
	} {
		c, err := loadTraceEnv(env)
		if err != nil || c.Tracing.CaptureContent() || c.Tracing.Content.LangSmith || c.Tracing.Content.OTLP {
			t.Fatalf("env=%v content=%+v err=%v", env, c.Tracing.Content, err)
		}
	}
}

func TestTraceContentRejectsInvalidDestinations(t *testing.T) {
	both := map[string]string{"TRACING_ENABLED": "true", "LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "key"}
	for _, list := range []string{
		"unknown", "LangSmith", "OTLP", "langsmith,langsmith", "otlp,langsmith,otlp", "langsmith,,otlp", ",langsmith", "langsmith,", ",", "langsmith,unknown",
		"langsmith otlp", "langsmith;otlp", "all", "*",
	} {
		env := map[string]string{"TRACING_CONTENT_DESTINATIONS": list}
		for key, value := range both {
			env[key] = value
		}
		for _, capture := range []string{"", "true", "false"} {
			env["TRACING_CAPTURE_CONTENT"] = capture
			if c, err := loadTraceEnv(env); err == nil {
				t.Errorf("accepted destinations %q with capture %q: %+v", list, capture, c.Tracing.Content)
			} else if strings.Contains(err.Error(), list) && list != "" {
				t.Errorf("error repeats the configured value: %v", err)
			}
		}
	}
	for _, env := range []map[string]string{
		{"TRACING_CONTENT_DESTINATIONS": "otlp"},
		{"TRACING_CONTENT_DESTINATIONS": "langsmith"},
		{"TRACING_CONTENT_DESTINATIONS": "langsmith", "TRACING_ENABLED": "true"},
		{"TRACING_CONTENT_DESTINATIONS": "otlp", "LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "key"},
	} {
		if c, err := loadTraceEnv(env); err == nil {
			t.Errorf("accepted a destination whose exporter is disabled %v: %+v", env, c.Tracing)
		}
	}
}

func TestTraceContentUnsetKeepsStructuralDefaults(t *testing.T) {
	c, err := loadTraceEnv(nil)
	if err != nil || c.Tracing.Enabled || c.LangSmith.Enabled || c.Tracing.CaptureContent() {
		t.Fatalf("defaults=%+v err=%v", c, err)
	}
}
