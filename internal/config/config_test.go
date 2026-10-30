package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	c, err := loadWithoutAuth(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != "8080" || c.RateLimitMax != 50 || c.RateLimitWindow != time.Minute || c.RequestTimeout != 20*time.Second || c.LLMTimeout != 15*time.Second || c.MaxFailedTopics != 10 || c.ShutdownTimeout != 10*time.Second || c.LogLevel != slog.LevelInfo || len(c.CORSAllowedOrigins) != 0 || len(c.TrustedProxies) != 0 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{"PORT": "9090", "AXIOM_INTERNAL_TOKEN": "secret", "CORS_ALLOWED_ORIGINS": "https://example.com, http://localhost:3000", "RATE_LIMIT_MAX": "2", "RATE_LIMIT_WINDOW": "2m", "TRUSTED_PROXIES": "127.0.0.1,10.0.0.0/8,::1", "REQUEST_TIMEOUT": "5s", "LLM_TIMEOUT": "3s", "MAX_FAILED_TOPICS": "4", "GRAPH_FILE": "graph.json", "LOG_LEVEL": "debug", "SHUTDOWN_TIMEOUT": "1s"}
	c, err := loadWithoutAuth(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != "9090" || c.InternalToken != "secret" || len(c.CORSAllowedOrigins) != 2 || len(c.TrustedProxies) != 3 || c.LogLevel != slog.LevelDebug || c.MaxFailedTopics != 4 || c.GraphFile != "graph.json" || c.LLMTimeout != 3*time.Second || c.RateLimitMax != 2 || c.RateLimitWindow != 2*time.Minute || c.ShutdownTimeout != time.Second {
		t.Fatalf("overrides: %+v", c)
	}
	for _, level := range []string{"info", "warn", "error"} {
		if _, err := loadWithoutAuth(func(k string) string {
			if k == "LOG_LEVEL" {
				return level
			}
			return ""
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadWithoutAuth(func(k string) string {
		if k == "CORS_ALLOWED_ORIGINS" {
			return "*"
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string][]string{
		"PORT": {"0", "65536", "abc", "-1"}, "RATE_LIMIT_MAX": {"0", "-1", "x"}, "MAX_FAILED_TOPICS": {"0", "-1", "x"},
		"RATE_LIMIT_WINDOW": {"0s", "-1s", "bad"}, "REQUEST_TIMEOUT": {"0s", "bad", "1s"}, "LLM_TIMEOUT": {"-1s", "bad", "21s"}, "SHUTDOWN_TIMEOUT": {"0s", "bad"},
		"TRUSTED_PROXIES": {"localhost", "127.0.0.1,", "10.0.0.0/99"}, "CORS_ALLOWED_ORIGINS": {"example.com", "https://example.com/path", "https://example.com,", "*,https://example.com", "https://user:pass@example.com"}, "LOG_LEVEL": {"trace"},
	}
	for key, values := range cases {
		for _, value := range values {
			t.Run(key+"="+value, func(t *testing.T) {
				_, err := loadWithoutAuth(func(k string) string {
					if k == key {
						return value
					}
					return ""
				})
				if err == nil {
					t.Fatal("expected invalid configuration")
				}
			})
		}
	}
}

func TestHTTPTimeouts(t *testing.T) {
	for _, requestTimeout := range []string{"", "35s"} {
		cfg, err := loadWithoutAuth(func(k string) string {
			if k == "REQUEST_TIMEOUT" {
				return requestTimeout
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReadTimeout != 10*time.Second || cfg.IdleTimeout != time.Minute || cfg.WriteTimeout != cfg.RequestTimeout+10*time.Second || cfg.WriteTimeout <= cfg.RequestTimeout {
			t.Fatalf("invalid HTTP timeout derivation: %+v", cfg)
		}
	}
	if _, err := loadWithoutAuth(func(k string) string {
		if k == "REQUEST_TIMEOUT" {
			return "2562047h47m16.854775807s"
		}
		return ""
	}); err == nil {
		t.Fatal("write timeout overflow accepted")
	}
}

func TestLoadRequiresTokenUnlessAuthDisabled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       map[string]string
		wantError bool
	}{
		{"missing token", nil, true},
		{"explicit opt out", map[string]string{"AXIOM_AUTH_DISABLED": "true"}, false},
		{"invalid opt out", map[string]string{"AXIOM_AUTH_DISABLED": "yes"}, true},
		{"explicit enabled", map[string]string{"AXIOM_AUTH_DISABLED": "false"}, true},
		{"configured token", map[string]string{"AXIOM_INTERNAL_TOKEN": "0123456789abcdef0123456789abcdef"}, false},
		{"token with opt out", map[string]string{"AXIOM_INTERNAL_TOKEN": "0123456789abcdef0123456789abcdef", "AXIOM_AUTH_DISABLED": "true"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(func(k string) string { return tc.env[k] })
			if (err != nil) != tc.wantError {
				t.Fatalf("Load error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}

func loadWithoutAuth(getenv func(string) string) (Config, error) {
	return Load(func(k string) string {
		if k == "AXIOM_AUTH_DISABLED" {
			if getenv("AXIOM_INTERNAL_TOKEN") != "" {
				return "false"
			}
			return "true"
		}
		return getenv(k)
	})
}
