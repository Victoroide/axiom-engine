package config

import (
	"strings"
	"testing"
)

func TestLangSmithRequiresKey(t *testing.T) {
	_, err := Load(func(k string) string {
		switch k {
		case "AXIOM_AUTH_DISABLED", "LANGSMITH_TRACING":
			return "true"
		}
		return ""
	})
	if err == nil {
		t.Fatal("accepted LangSmith tracing without an API key")
	}
}

func TestLangSmithConfiguration(t *testing.T) {
	for _, endpoint := range []string{"", "https://eu.api.smith.langchain.com/", "https://traces.example.com/api/v1/", "http://127.0.0.1:4318", "http://[::1]:4318", "http://localhost:4318"} {
		t.Run(endpoint, func(t *testing.T) {
			env := map[string]string{"AXIOM_AUTH_DISABLED": "true", "LANGSMITH_TRACING": "true", "LANGSMITH_API_KEY": "test-key", "LANGSMITH_ENDPOINT": endpoint, "LANGSMITH_PROJECT": "test-project"}
			c, err := Load(func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			if !c.LangSmith.Enabled || c.Tracing.Enabled || c.LangSmith.Project != "test-project" || c.LangSmith.APIKey != "test-key" {
				t.Fatalf("configuration=%+v", c)
			}
			want := strings.TrimRight(endpoint, "/")
			if want == "" {
				want = "https://api.smith.langchain.com"
			}
			if c.LangSmith.Endpoint != want {
				t.Fatal(c.LangSmith.Endpoint)
			}
		})
	}
}

func TestLangSmithRejectsInvalidConfiguration(t *testing.T) {
	for _, env := range []map[string]string{
		{"LANGSMITH_TRACING": "yes"},
		{"LANGSMITH_ENDPOINT": "relative/path"}, {"LANGSMITH_ENDPOINT": "/"}, {"LANGSMITH_ENDPOINT": "ftp://example.com"},
		{"LANGSMITH_ENDPOINT": "http://example.com"}, {"LANGSMITH_ENDPOINT": "https://user@example.com"},
		{"LANGSMITH_ENDPOINT": "https://example.com?key=value"}, {"LANGSMITH_ENDPOINT": "https://example.com#fragment"},
		{"LANGSMITH_ENDPOINT": "http://localhost.example.com"},
	} {
		env["AXIOM_AUTH_DISABLED"] = "true"
		if _, err := Load(func(k string) string { return env[k] }); err == nil {
			t.Fatalf("accepted %v", env)
		}
	}
	c, err := Load(func(k string) string {
		if k == "AXIOM_AUTH_DISABLED" {
			return "true"
		}
		return ""
	})
	if err != nil || c.LangSmith.Enabled {
		t.Fatalf("defaults=%+v error=%v", c, err)
	}
}
