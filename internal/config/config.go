// Package config loads validated process configuration.
package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config contains startup configuration for the service.
type Config struct {
	Port               string
	AuthDisabled       bool
	InternalToken      string
	CORSAllowedOrigins []string
	RateLimitMax       int
	RateLimitWindow    time.Duration
	TrustedProxies     []string
	RequestTimeout     time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	LLMTimeout         time.Duration
	LLMMaxConcurrency  int
	LLMQueueTimeout    time.Duration
	MaxFailedTopics    int
	GraphFile          string
	LogLevel           slog.Level
	ShutdownTimeout    time.Duration
}

func defaults() Config {
	return Config{
		Port:              "8080",
		RateLimitMax:      50,
		RateLimitWindow:   time.Minute,
		RequestTimeout:    20 * time.Second,
		LLMTimeout:        15 * time.Second,
		LLMMaxConcurrency: 16,
		LLMQueueTimeout:   2 * time.Second,
		MaxFailedTopics:   10,
		LogLevel:          slog.LevelInfo,
	}
}

// Load parses environment values without modifying process state.
func Load(getenv func(string) string) (Config, error) {
	c := defaults()
	c.InternalToken = getenv("AXIOM_INTERNAL_TOKEN")
	c.GraphFile = getenv("GRAPH_FILE")
	for _, item := range []struct {
		key string
		dst *bool
	}{
		{"AXIOM_AUTH_DISABLED", &c.AuthDisabled},
	} {
		if v := getenv(item.key); v != "" {
			switch v {
			case "true":
				*item.dst = true
			case "false":
				*item.dst = false
			default:
				return Config{}, fmt.Errorf("%s must be true or false", item.key)
			}
		}
	}
	if c.InternalToken == "" && !c.AuthDisabled {
		return Config{}, fmt.Errorf("AXIOM_INTERNAL_TOKEN is required unless AXIOM_AUTH_DISABLED=true")
	}
	if c.InternalToken != "" && (len(c.InternalToken) < 32 || strings.ContainsFunc(c.InternalToken, func(r rune) bool { return r < 0x21 || r > 0x7e })) {
		return Config{}, fmt.Errorf("AXIOM_INTERNAL_TOKEN must be at least 32 visible ASCII characters without whitespace")
	}
	if v := getenv("PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("PORT must be an integer between 1 and 65535")
		}
		c.Port = v
	}
	for _, item := range []struct {
		key string
		dst *int
	}{
		{"LLM_MAX_CONCURRENCY", &c.LLMMaxConcurrency},
		{"RATE_LIMIT_MAX", &c.RateLimitMax},
		{"MAX_FAILED_TOPICS", &c.MaxFailedTopics},
	} {
		if v := getenv(item.key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive integer", item.key)
			}
			*item.dst = n
		}
	}
	for _, item := range []struct {
		key string
		dst *time.Duration
	}{
		{"RATE_LIMIT_WINDOW", &c.RateLimitWindow},
		{"REQUEST_TIMEOUT", &c.RequestTimeout},
		{"LLM_TIMEOUT", &c.LLMTimeout},
		{"LLM_QUEUE_TIMEOUT", &c.LLMQueueTimeout},
		{"SHUTDOWN_TIMEOUT", &c.ShutdownTimeout},
	} {
		if v := getenv(item.key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", item.key)
			}
			*item.dst = d
		}
	}
	if c.LLMMaxConcurrency > 1024 {
		return Config{}, fmt.Errorf("LLM_MAX_CONCURRENCY must be between 1 and 1024")
	}
	if getenv("LLM_QUEUE_TIMEOUT") == "" {
		c.LLMQueueTimeout = min(2*time.Second, c.LLMTimeout/2)
	}
	if c.LLMQueueTimeout <= 0 {
		return Config{}, fmt.Errorf("LLM_TIMEOUT must allow a positive queue timeout")
	}
	if c.LLMQueueTimeout >= c.LLMTimeout {
		return Config{}, fmt.Errorf("LLM_QUEUE_TIMEOUT must be less than LLM_TIMEOUT")
	}
	c.ReadTimeout = 10 * time.Second
	c.IdleTimeout = time.Minute
	if c.RequestTimeout > time.Duration(1<<63-1)-c.ReadTimeout {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT is too large for the HTTP write deadline")
	}
	c.WriteTimeout = c.RequestTimeout + c.ReadTimeout
	if getenv("SHUTDOWN_TIMEOUT") == "" {
		c.ShutdownTimeout = max(c.RequestTimeout+5*time.Second, c.ReadTimeout+time.Second)
	}
	if c.ShutdownTimeout < c.ReadTimeout+time.Second {
		return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be at least 11s")
	}
	if c.LLMTimeout > c.RequestTimeout {
		return Config{}, fmt.Errorf("LLM_TIMEOUT must not exceed REQUEST_TIMEOUT")
	}
	if v := getenv("LOG_LEVEL"); v != "" {
		switch strings.ToLower(v) {
		case "debug":
			c.LogLevel = slog.LevelDebug
		case "info":
			c.LogLevel = slog.LevelInfo
		case "warn":
			c.LogLevel = slog.LevelWarn
		case "error":
			c.LogLevel = slog.LevelError
		default:
			return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error")
		}
	}
	if v := getenv("TRUSTED_PROXIES"); v != "" {
		for _, raw := range strings.Split(v, ",") {
			proxy := strings.TrimSpace(raw)
			if _, err := netip.ParseAddr(proxy); err != nil {
				if _, err := netip.ParsePrefix(proxy); err != nil {
					return Config{}, fmt.Errorf("TRUSTED_PROXIES contains an invalid IP or CIDR: %q", proxy)
				}
			}
			c.TrustedProxies = append(c.TrustedProxies, proxy)
		}
	}
	if v := getenv("CORS_ALLOWED_ORIGINS"); v != "" {
		for _, raw := range strings.Split(v, ",") {
			origin := strings.TrimSpace(raw)
			if origin == "*" {
				if strings.TrimSpace(v) != "*" {
					return Config{}, fmt.Errorf("wildcard CORS origin must be used alone")
				}
			} else {
				u, err := url.Parse(origin)
				if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
					return Config{}, fmt.Errorf("CORS_ALLOWED_ORIGINS contains an invalid origin: %q", origin)
				}
			}
			c.CORSAllowedOrigins = append(c.CORSAllowedOrigins, origin)
		}
	}
	if c.AuthDisabled && c.InternalToken != "" {
		return Config{}, fmt.Errorf("AXIOM_AUTH_DISABLED must be false when AXIOM_INTERNAL_TOKEN is configured")
	}
	return c, nil
}
