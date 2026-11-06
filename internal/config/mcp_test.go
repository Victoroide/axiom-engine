package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadMCP(t *testing.T) {
	env := map[string]string{"IDEMPOTENCY_TTL": "invalid", "IDEMPOTENCY_MAX_ENTRIES": "invalid", "REDIS_URL": "invalid", "PORT": "bad", "AXIOM_INTERNAL_TOKEN": "short", "AXIOM_INTERNAL_TOKEN_PREVIOUS": "short", "AXIOM_AUTH_DISABLED": "invalid", "CORS_ALLOWED_ORIGINS": "bad", "TRUSTED_PROXIES": "bad", "RATE_LIMIT_MAX": "bad", "RATE_LIMIT_WINDOW": "bad", "SWAGGER_ENABLED": "bad", "METRICS_ENABLED": "bad", "SHUTDOWN_DRAIN_DELAY": "bad", "SHUTDOWN_TIMEOUT": "1s", "GRAPH_FILE": "curriculum.json", "LLM_MAX_CONCURRENCY": "3"}
	c, err := LoadMCP(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if c.GraphFile != "curriculum.json" || c.LLMMaxConcurrency != 3 || c.ShutdownTimeout != time.Second {
		t.Fatalf("config=%+v", c)
	}
	for _, key := range []string{"LLM_TIMEOUT", "LLM_MAX_CONCURRENCY", "LLM_QUEUE_TIMEOUT", "MAX_FAILED_TOPICS", "MAX_MASTERED_TOPICS", "RESOURCE_LINK_CHECK", "RESOURCE_LINK_TIMEOUT", "REQUEST_TIMEOUT", "SHUTDOWN_TIMEOUT", "LOG_LEVEL"} {
		t.Run(key, func(t *testing.T) {
			_, err := LoadMCP(func(k string) string {
				if k == key {
					return "invalid"
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := Load(func(string) string { return "" }); err == nil {
		t.Fatal("HTTP accepted missing token")
	}
}
