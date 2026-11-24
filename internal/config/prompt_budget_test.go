package config

import (
	"strings"
	"testing"
)

func TestPromptBudgetConfiguration(t *testing.T) {
	for name, load := range map[string]func(func(string) string) (Config, error){"HTTP": Load, "MCP": LoadMCP} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				key, value, errorKey string
			}{
				{"LLM_MAX_TOPIC_INPUT_BYTES", "3072", "LLM_MAX_TOPIC_INPUT_BYTES"},
				{"LLM_MAX_TOPIC_INPUT_BYTES", "3093", "LLM_MAX_TOPIC_INPUT_BYTES must be at least 3094 bytes"},
				{"LLM_MAX_TOPIC_INPUT_BYTES", "9223372036854775808", "LLM_MAX_TOPIC_INPUT_BYTES"},
				{"LLM_MAX_REQUEST_INPUT_BYTES", "16383", "LLM_MAX_REQUEST_INPUT_BYTES"},
				{"LLM_MAX_CONTEXT_EDGES", "0", "LLM_MAX_CONTEXT_EDGES"},
				{"LLM_MAX_PREREQUISITES", "-1", "LLM_MAX_PREREQUISITES"},
				{"LLM_MAX_PREREQUISITES", "unbounded", "LLM_MAX_PREREQUISITES"},
			} {
				env := map[string]string{"AXIOM_AUTH_DISABLED": "true", tc.key: tc.value}
				if _, err := load(func(key string) string { return env[key] }); err == nil || !strings.Contains(err.Error(), tc.errorKey) {
					t.Errorf("%s=%q: got %v", tc.key, tc.value, err)
				}
			}
			env := map[string]string{"AXIOM_AUTH_DISABLED": "true"}
			cfg, err := load(func(key string) string { return env[key] })
			if err != nil || cfg.PromptBudget != DefaultPromptBudget() {
				t.Fatalf("default budget = %+v, error = %v", cfg.PromptBudget, err)
			}
			env["LLM_MAX_TOPIC_INPUT_BYTES"] = "3094"
			env["LLM_MAX_REQUEST_INPUT_BYTES"] = "3094"
			cfg, err = load(func(key string) string { return env[key] })
			if err != nil || cfg.PromptBudget.MaxTopicBytes != 3094 || cfg.PromptBudget.MaxRequestBytes != 3094 {
				t.Fatalf("smallest budget = %+v, error = %v", cfg.PromptBudget, err)
			}
			env["LLM_MAX_TOPIC_INPUT_BYTES"] = "4096"
			env["LLM_MAX_REQUEST_INPUT_BYTES"] = "4096"
			env["LLM_MAX_CONTEXT_EDGES"] = "20"
			env["LLM_MAX_PREREQUISITES"] = "3"
			cfg, err = load(func(key string) string { return env[key] })
			if want := (PromptBudget{MaxTopicBytes: 4096, MaxRequestBytes: 4096, MaxEdges: 20, MaxPrerequisites: 3}); err != nil || cfg.PromptBudget != want {
				t.Fatalf("configured budget = %+v, error = %v", cfg.PromptBudget, err)
			}
		})
	}
}

func TestPromptBudgetOllamaContextConfiguration(t *testing.T) {
	for name, load := range map[string]func(func(string) string) (Config, error){"HTTP": Load, "MCP": LoadMCP} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, context, cap, wantError string
				wantBytes                     int
			}{
				{name: "default", wantBytes: 4480},
				{name: "exact ceiling", cap: "4480", wantBytes: 4480},
				{name: "over ceiling", cap: "4481", wantError: "LLM_MAX_TOPIC_INPUT_BYTES must be at most 4480"},
				{name: "context too small", context: "2709", wantError: "LLM_OLLAMA_CONTEXT_TOKENS must be at least 2710"},
				{name: "minimum context", context: "2710", wantBytes: 3094},
				{name: "malformed context", context: "4K", wantError: "LLM_OLLAMA_CONTEXT_TOKENS must be an integer"},
				{name: "larger context", context: "8192", wantBytes: 8576},
				{name: "larger context and override", context: "8192", cap: "8576", wantBytes: 8576},
				{name: "maximum integer context", context: "9223372036854775807", wantBytes: 16384},
			} {
				t.Run(tc.name, func(t *testing.T) {
					env := map[string]string{
						"AXIOM_AUTH_DISABLED": "true", "LLM_PROVIDER": "ollama", "LLM_MODEL": "mistral:v0.3",
						"LLM_OLLAMA_CONTEXT_TOKENS": tc.context, "LLM_MAX_TOPIC_INPUT_BYTES": tc.cap,
					}
					cfg, err := load(func(key string) string { return env[key] })
					if tc.wantError != "" {
						if err == nil || !strings.Contains(err.Error(), tc.wantError) {
							t.Fatalf("error = %v, want %q", err, tc.wantError)
						}
						return
					}
					if err != nil || cfg.PromptBudget.MaxTopicBytes != tc.wantBytes {
						t.Fatalf("budget = %+v, error = %v, want %d bytes", cfg.PromptBudget, err, tc.wantBytes)
					}
				})
			}
		})
	}
}
