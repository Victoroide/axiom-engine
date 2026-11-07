package config

import (
	"strings"
	"testing"
)

func TestLLMRejectsInvalidConfiguration(t *testing.T) {
	for _, env := range []map[string]string{
		{"LLM_PROVIDER": "unknown"},
		{"LLM_PROVIDER": "openai"},
		{"LLM_PROVIDER": "openai", "LLM_MODEL": "small"},
		{"LLM_PROVIDER": "anthropic", "LLM_MODEL": "small"},
		{"LLM_PROVIDER": "google-ai", "LLM_MODEL": "small"},
		{"LLM_PROVIDER": "ollama"},
		{"LLM_PROVIDER": "openai-generic", "LLM_MODEL": "small"},
		{"LLM_PROVIDER": "openai-generic", "LLM_MODEL": "small", "LLM_BASE_URL": "file:///tmp/api"},
		{"LLM_PROVIDER": "openai-generic", "LLM_MODEL": "small", "LLM_BASE_URL": "https://user@example.com/v1"},
		{"LLM_MODEL": " "},
		{"LLM_BASE_URL": "http://example.com/v1"},
	} {
		t.Run(strings.Join(mapKeys(env), "/"), func(t *testing.T) {
			env["AXIOM_AUTH_DISABLED"] = "true"
			if _, err := Load(func(k string) string { return env[k] }); err == nil {
				t.Fatalf("accepted invalid configuration: %v", env)
			}
		})
	}
}

func mapKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k, v := range env {
		keys = append(keys, k+"="+v)
	}
	return keys
}

func TestLLMDefaultsAndProviders(t *testing.T) {
	for _, provider := range []string{"", "aws-bedrock", "openai", "anthropic", "google-ai", "ollama", "openai-generic"} {
		t.Run(provider, func(t *testing.T) {
			env := map[string]string{"AXIOM_AUTH_DISABLED": "true", "LLM_PROVIDER": provider}
			if provider != "" && provider != "aws-bedrock" {
				env["LLM_MODEL"] = "chosen"
			}
			for _, k := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "LLM_API_KEY"} {
				env[k] = "test-key"
			}
			if provider == "openai-generic" {
				env["LLM_BASE_URL"] = "https://example.com/v1/"
			}
			c, err := Load(func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			if provider == "" || provider == "aws-bedrock" {
				if c.LLM.Provider != "aws-bedrock" || c.LLM.Model != "amazon.nova-micro-v1:0" {
					t.Fatalf("defaults=%+v", c.LLM)
				}
			} else if c.LLM.Provider != provider || c.LLM.Model != "chosen" {
				t.Fatalf("selection=%+v", c.LLM)
			}
			if provider == "ollama" && c.LLM.BaseURL != "http://localhost:11434/v1" {
				t.Fatal(c.LLM.BaseURL)
			}
			if provider == "openai-generic" && (c.LLM.BaseURL != "https://example.com/v1" || c.LLM.APIKey != "test-key") {
				t.Fatal("generic settings")
			}
		})
	}
}

func TestDefaultLLMMatchesLoadedConfiguration(t *testing.T) {
	c, err := Load(func(k string) string {
		if k == "AXIOM_AUTH_DISABLED" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM != DefaultLLM() {
		t.Fatalf("loaded=%+v default=%+v", c.LLM, DefaultLLM())
	}
}
