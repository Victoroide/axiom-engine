package config

import (
	"strings"
	"testing"
	"time"
)

func FuzzLoad(f *testing.F) {
	for _, pair := range [][2]string{
		{"", ""}, {"PORT", "65535"}, {"PORT", "0"},
		{"AXIOM_AUTH_DISABLED", "true"}, {"AXIOM_INTERNAL_TOKEN", ""},
		{"AXIOM_INTERNAL_TOKEN_PREVIOUS", strings.Repeat("p", 32)},
		{"LLM_QUEUE_TIMEOUT", "14s"}, {"LLM_QUEUE_TIMEOUT", "15s"},
		{"LLM_TIMEOUT", "1ns"}, {"LLM_TIMEOUT", "20s"},
		{"REQUEST_TIMEOUT", "2562047h47m16.854775807s"},
		{"REQUEST_TIMEOUT", "2562047h47m2s"},
		{"SHUTDOWN_TIMEOUT", "10s"}, {"SHUTDOWN_TIMEOUT", "11s"},
		{"SHUTDOWN_DRAIN_DELAY", "0"}, {"RATE_LIMIT_WINDOW", "-1s"},
		{"LLM_MAX_CONCURRENCY", "1025"}, {"MAX_FAILED_TOPICS", "1"},
		{"TRUSTED_PROXIES", "127.0.0.1,::1"}, {"CORS_ALLOWED_ORIGINS", "https://example.com"},
		{"METRICS_ENABLED", "false"}, {"SWAGGER_ENABLED", "true"},
		{"LOG_LEVEL", "debug"}, {"GRAPH_FILE", "curriculum.json"},
	} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, key, value string) {
		env := map[string]string{"AXIOM_INTERNAL_TOKEN": strings.Repeat("t", 32), key: value}
		c, err := Load(func(name string) string { return env[name] })
		if err != nil {
			return
		}
		if c.LLMQueueTimeout <= 0 || c.LLMQueueTimeout >= c.LLMTimeout || c.LLMTimeout > c.RequestTimeout {
			t.Fatalf("invalid timeout ordering: queue=%s llm=%s request=%s", c.LLMQueueTimeout, c.LLMTimeout, c.RequestTimeout)
		}
		if c.ShutdownTimeout < 11*time.Second {
			t.Fatalf("shutdown timeout is too short: %s", c.ShutdownTimeout)
		}
		if c.WriteTimeout != c.RequestTimeout+10*time.Second {
			t.Fatalf("write timeout=%s request timeout=%s", c.WriteTimeout, c.RequestTimeout)
		}
	})
}
