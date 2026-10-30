package main

import (
	"os"
	"strings"
	"testing"
)

func TestRunRejectsInvalidRetrySettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"AWS_MAX_ATTEMPTS", ""}, {"AWS_MAX_ATTEMPTS", "abc"}, {"AWS_MAX_ATTEMPTS", "0"},
		{"AWS_MAX_ATTEMPTS", "-1"}, {"AWS_MAX_ATTEMPTS", "1.5"}, {"AWS_MAX_ATTEMPTS", " 3"},
		{"AWS_MAX_ATTEMPTS", "4294967296"},
		{"AWS_RETRY_MODE", ""}, {"AWS_RETRY_MODE", "legacy"}, {"AWS_RETRY_MODE", "bogus"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv("AXIOM_AUTH_DISABLED", "true")
			t.Setenv("AXIOM_INTERNAL_TOKEN", "")
			t.Setenv("AWS_MAX_ATTEMPTS", "3")
			t.Setenv("AWS_RETRY_MODE", "standard")
			t.Setenv(tc.key, tc.value)
			if err := run([]string{"unexpected"}); err == nil || !strings.Contains(err.Error(), tc.key+" must be") {
				t.Fatalf("expected retry configuration error before startup, got %v", err)
			}
		})
	}
}

func TestRunAcceptsRetrySettings(t *testing.T) {
	for _, tc := range []struct{ attempts, mode string }{
		{"unset", "unset"}, {"1", "standard"}, {"5", "adaptive"}, {"4294967295", "standard"},
	} {
		t.Run(tc.attempts+"/"+tc.mode, func(t *testing.T) {
			t.Setenv("AXIOM_AUTH_DISABLED", "true")
			t.Setenv("AXIOM_INTERNAL_TOKEN", "")
			for key, value := range map[string]string{"AWS_MAX_ATTEMPTS": tc.attempts, "AWS_RETRY_MODE": tc.mode} {
				t.Setenv(key, value)
				if value == "unset" {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := run([]string{"unexpected"}); err == nil || err.Error() != "usage: axiom-server [healthcheck]" {
				t.Fatalf("expected argument validation after configuration, got %v", err)
			}
		})
	}
}
