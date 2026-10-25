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
	MaxFailedTopics    int
	GraphFile          string
	LogLevel           slog.Level
	ShutdownTimeout    time.Duration
}

// Load parses environment values without modifying process state.
func Load(getenv func(string) string) (Config, error) {
	c := Config{Port: "8080", InternalToken: getenv("AXIOM_INTERNAL_TOKEN"), GraphFile: getenv("GRAPH_FILE"), RateLimitMax: 50, RateLimitWindow: time.Minute, RequestTimeout: 20 * time.Second, LLMTimeout: 15 * time.Second, MaxFailedTopics: 10, ShutdownTimeout: 10 * time.Second, LogLevel: slog.LevelInfo}
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
	}{{"RATE_LIMIT_MAX", &c.RateLimitMax}, {"MAX_FAILED_TOPICS", &c.MaxFailedTopics}} {
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
	}{{"RATE_LIMIT_WINDOW", &c.RateLimitWindow}, {"REQUEST_TIMEOUT", &c.RequestTimeout}, {"LLM_TIMEOUT", &c.LLMTimeout}, {"SHUTDOWN_TIMEOUT", &c.ShutdownTimeout}} {
		if v := getenv(item.key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", item.key)
			}
			*item.dst = d
		}
	}
	c.ReadTimeout = 10 * time.Second
	c.IdleTimeout = time.Minute
	if c.RequestTimeout > time.Duration(1<<63-1)-c.ReadTimeout {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT is too large for the HTTP write deadline")
	}
	c.WriteTimeout = c.RequestTimeout + c.ReadTimeout
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
	return c, nil
}
