package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	c, err := loadWithoutAuth(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMMaxConcurrency != 16 || c.LLMQueueTimeout != 2*time.Second || c.Port != "8080" || c.RateLimitMax != 50 || c.RateLimitWindow != time.Minute || c.RequestTimeout != 20*time.Second || c.LLMTimeout != 15*time.Second || c.MaxFailedTopics != 10 || c.ShutdownTimeout != 25*time.Second || c.LogLevel != slog.LevelInfo || len(c.CORSAllowedOrigins) != 0 || len(c.TrustedProxies) != 0 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{"PORT": "9090", "AXIOM_INTERNAL_TOKEN": "0123456789abcdef0123456789abcdef", "CORS_ALLOWED_ORIGINS": "https://example.com, http://localhost:3000", "RATE_LIMIT_MAX": "2", "RATE_LIMIT_WINDOW": "2m", "TRUSTED_PROXIES": "127.0.0.1,10.0.0.0/8,::1", "REQUEST_TIMEOUT": "5s", "LLM_TIMEOUT": "3s", "MAX_FAILED_TOPICS": "4", "GRAPH_FILE": "graph.json", "LOG_LEVEL": "debug", "SHUTDOWN_TIMEOUT": "11s"}
	c, err := loadWithoutAuth(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != "9090" || c.InternalToken != "0123456789abcdef0123456789abcdef" || len(c.CORSAllowedOrigins) != 2 || len(c.TrustedProxies) != 3 || c.LogLevel != slog.LevelDebug || c.MaxFailedTopics != 4 || c.GraphFile != "graph.json" || c.LLMTimeout != 3*time.Second || c.RateLimitMax != 2 || c.RateLimitWindow != 2*time.Minute || c.ShutdownTimeout != 11*time.Second {
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

func TestShutdownTimeoutDefaultsAboveRequestTimeout(t *testing.T) {
	for _, tc := range []struct {
		request, shutdown string
		want              time.Duration
	}{
		{"", "", 25 * time.Second},
		{"35s", "", 40 * time.Second},
		{"35s", "11s", 11 * time.Second},
	} {
		cfg, err := loadWithoutAuth(func(k string) string {
			switch k {
			case "REQUEST_TIMEOUT":
				return tc.request
			case "SHUTDOWN_TIMEOUT":
				return tc.shutdown
			default:
				return ""
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ShutdownTimeout != tc.want {
			t.Errorf("REQUEST_TIMEOUT=%q SHUTDOWN_TIMEOUT=%q: got %s, want %s", tc.request, tc.shutdown, cfg.ShutdownTimeout, tc.want)
		}
	}
}

func TestShutdownTimeoutRejectsInsufficientBudget(t *testing.T) {
	for _, value := range []string{"1s", "2s", "10s", "10999ms"} {
		_, err := loadWithoutAuth(func(k string) string {
			if k == "SHUTDOWN_TIMEOUT" {
				return value
			}
			return ""
		})
		if err == nil {
			t.Errorf("SHUTDOWN_TIMEOUT=%q accepted", value)
		}
	}
}

func TestShutdownDrainDelay(t *testing.T) {
	for _, tc := range []struct {
		value     string
		wantError bool
	}{
		{"", false},
		{"0s", false},
		{"250ms", false},
		{"1s", false},
		{"-1s", true},
		{"invalid", true},
		{"2562047h47m16.854775808s", true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			_, err := loadWithoutAuth(func(k string) string {
				if k == "SHUTDOWN_DRAIN_DELAY" {
					return tc.value
				}
				return ""
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("SHUTDOWN_DRAIN_DELAY=%q: error=%v, want error=%v", tc.value, err, tc.wantError)
			}
		})
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

func TestLoadRejectsWeakOrPaddedTokens(t *testing.T) {
	for _, tc := range []struct {
		name      string
		token     string
		wantError bool
	}{
		{"one byte", "x", true},
		{"31 bytes", strings.Repeat("x", 31), true},
		{"leading space", " " + strings.Repeat("x", 32), true},
		{"trailing space", strings.Repeat("x", 32) + " ", true},
		{"embedded space", strings.Repeat("x", 16) + " " + strings.Repeat("x", 16), true},
		{"newline", strings.Repeat("x", 32) + "\n", true},
		{"tab", strings.Repeat("x", 32) + "\t", true},
		{"non ASCII", strings.Repeat("x", 32) + "é", true},
		{"delete", strings.Repeat("x", 32) + "\x7f", true},
		{"32 bytes", "0123456789abcdef0123456789abcdef", false},
		{"visible boundaries", strings.Repeat("!~", 16), false},
		{"longer token", strings.Repeat("x", 64), false},
	} {
		for _, disabled := range []string{"true", "false"} {
			t.Run(tc.name+"/disabled="+disabled, func(t *testing.T) {
				_, err := Load(func(k string) string {
					switch k {
					case "AXIOM_INTERNAL_TOKEN":
						return tc.token
					case "AXIOM_AUTH_DISABLED":
						return disabled
					default:
						return ""
					}
				})
				switch {
				case tc.wantError:
					if err == nil || err.Error() != "AXIOM_INTERNAL_TOKEN must be at least 32 visible ASCII characters without whitespace" {
						t.Fatalf("expected token validation error, got %v", err)
					}
				case disabled == "true":
					if err == nil || err.Error() != "AXIOM_AUTH_DISABLED must be false when AXIOM_INTERNAL_TOKEN is configured" {
						t.Fatalf("expected conflicting authentication settings to fail, got %v", err)
					}
				case err != nil:
					t.Fatal(err)
				}
			})
		}
	}
}

func TestConcurrencyConfigBounds(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		invalid    bool
	}{
		{"LLM_MAX_CONCURRENCY", "0", true}, {"LLM_MAX_CONCURRENCY", "-1", true}, {"LLM_MAX_CONCURRENCY", "1025", true}, {"LLM_MAX_CONCURRENCY", "bad", true},
		{"LLM_MAX_CONCURRENCY", "1", false}, {"LLM_MAX_CONCURRENCY", "1024", false},
		{"LLM_QUEUE_TIMEOUT", "0s", true}, {"LLM_QUEUE_TIMEOUT", "-1s", true}, {"LLM_QUEUE_TIMEOUT", "bad", true}, {"LLM_QUEUE_TIMEOUT", "15s", true}, {"LLM_QUEUE_TIMEOUT", "16s", true}, {"LLM_QUEUE_TIMEOUT", "1s", false},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			_, err := loadWithoutAuth(func(key string) string {
				if key == tc.key {
					return tc.value
				}
				return ""
			})
			if (err != nil) != tc.invalid {
				t.Fatalf("error=%v, want invalid=%v", err, tc.invalid)
			}
		})
	}
}

func TestQueueTimeoutAdaptsToCallDeadline(t *testing.T) {
	for _, tc := range []struct {
		llm, queue string
		want       time.Duration
		wantError  bool
	}{
		{"1s", "", 500 * time.Millisecond, false},
		{"2s", "", time.Second, false},
		{"15s", "", 2 * time.Second, false},
		{"1s", "250ms", 250 * time.Millisecond, false},
		{"1s", "1s", 0, true},
		{"1s", "2s", 0, true},
	} {
		t.Run(tc.llm+"/"+tc.queue, func(t *testing.T) {
			cfg, err := loadWithoutAuth(func(key string) string {
				switch key {
				case "LLM_TIMEOUT":
					return tc.llm
				case "LLM_QUEUE_TIMEOUT":
					return tc.queue
				default:
					return ""
				}
			})
			if (err != nil) != tc.wantError || (err == nil && cfg.LLMQueueTimeout != tc.want) {
				t.Fatalf("queue=%s error=%v, want %s error=%v", cfg.LLMQueueTimeout, err, tc.want, tc.wantError)
			}
		})
	}
}

func TestConcurrencyConfigOverrides(t *testing.T) {
	c, err := loadWithoutAuth(func(key string) string {
		switch key {
		case "LLM_MAX_CONCURRENCY":
			return "7"
		case "LLM_QUEUE_TIMEOUT":
			return "125ms"
		default:
			return ""
		}
	})
	if err != nil || c.LLMMaxConcurrency != 7 || c.LLMQueueTimeout != 125*time.Millisecond {
		t.Fatalf("config=%+v error=%v", c, err)
	}
}

func TestMetricsEnabledValidation(t *testing.T) {
	for _, value := range []string{"yes", "1", "TRUE", " false", "invalid"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadWithoutAuth(func(key string) string {
				if key == "METRICS_ENABLED" {
					return value
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), "METRICS_ENABLED") {
				t.Fatalf("METRICS_ENABLED=%q error=%v", value, err)
			}
		})
	}
}

func TestPreviousTokenMustDiffer(t *testing.T) {
	token := strings.Repeat("a", 32)
	_, err := Load(func(key string) string {
		if key == "AXIOM_INTERNAL_TOKEN" || key == "AXIOM_INTERNAL_TOKEN_PREVIOUS" {
			return token
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("expected distinct token validation, got %v", err)
	}
}

func TestPreviousTokenValidation(t *testing.T) {
	for _, previous := range []string{"short", strings.Repeat("b", 31), " " + strings.Repeat("b", 32), strings.Repeat("b", 32) + " ", strings.Repeat("b", 32) + "é", strings.Repeat("b", 32) + "\x7f"} {
		t.Run(previous, func(t *testing.T) {
			_, err := Load(func(key string) string {
				switch key {
				case "AXIOM_INTERNAL_TOKEN":
					return strings.Repeat("a", 32)
				case "AXIOM_INTERNAL_TOKEN_PREVIOUS":
					return previous
				default:
					return ""
				}
			})
			if err == nil || !strings.Contains(err.Error(), "AXIOM_INTERNAL_TOKEN_PREVIOUS") {
				t.Fatalf("expected previous token validation, got %v", err)
			}
		})
	}
}

func TestPreviousTokenRequiresAuthentication(t *testing.T) {
	_, err := Load(func(key string) string {
		switch key {
		case "AXIOM_AUTH_DISABLED":
			return "true"
		case "AXIOM_INTERNAL_TOKEN_PREVIOUS":
			return strings.Repeat("b", 32)
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("previous token accepted with authentication disabled")
	}
}

func TestSwaggerEnabledValidation(t *testing.T) {
	for _, value := range []string{"yes", "1", "TRUE", " false", "invalid"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadWithoutAuth(func(key string) string {
				if key == "SWAGGER_ENABLED" {
					return value
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), "SWAGGER_ENABLED") {
				t.Fatalf("SWAGGER_ENABLED=%q error=%v", value, err)
			}
		})
	}
}
