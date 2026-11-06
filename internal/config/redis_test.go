package config

import "testing"

func TestRedisConfiguration(t *testing.T) {
	for _, tc := range []struct {
		url   string
		valid bool
	}{
		{"", true}, {"redis://localhost:6379/0", true}, {"rediss://user:secret@localhost:6379/1", true},
		{"http://localhost:6379", false}, {"unix:///tmp/redis.sock", false}, {"redis://", false}, {"redis://localhost/bad", false}, {"bad", false},
	} {
		cfg, err := Load(func(key string) string {
			if key == "AXIOM_AUTH_DISABLED" {
				return "true"
			}
			if key == "REDIS_URL" {
				return tc.url
			}
			return ""
		})
		if (err == nil) != tc.valid {
			t.Errorf("valid=%v err=%v", tc.valid, err)
		}
		if err == nil && cfg.Redis.URL != tc.url {
			t.Error("URL changed")
		}
	}
}
