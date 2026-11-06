package config

import (
	"fmt"
	"time"
)

// LoadMCP loads shared runtime settings without validating HTTP-only settings.
func LoadMCP(getenv func(string) string) (Config, error) {
	c, err := Load(func(key string) string {
		switch key {
		case "AXIOM_AUTH_DISABLED":
			return "true"
		case "REDIS_URL", "IDEMPOTENCY_TTL", "IDEMPOTENCY_MAX_ENTRIES", "AXIOM_INTERNAL_TOKEN", "AXIOM_INTERNAL_TOKEN_PREVIOUS", "PORT", "CORS_ALLOWED_ORIGINS", "TRUSTED_PROXIES", "RATE_LIMIT_MAX", "RATE_LIMIT_WINDOW", "SWAGGER_ENABLED", "METRICS_ENABLED", "SHUTDOWN_DRAIN_DELAY", "SHUTDOWN_TIMEOUT":
			return ""
		default:
			return getenv(key)
		}
	})
	if err != nil {
		return Config{}, err
	}
	if value := getenv("SHUTDOWN_TIMEOUT"); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be a positive duration")
		}
		c.ShutdownTimeout = timeout
	}
	return c, nil
}
