package llm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestBedrockAttemptsPerCall(t *testing.T) {
	for _, tt := range []struct {
		name, mode, maxAttempts string
		attempts                int
		class                   error
	}{
		{"throttle", "throttle", "3", 3, service.ErrThrottled},
		{"denied", "denied", "3", 1, service.ErrUpstreamRejected},
		{"defaults", "throttle", "", 3, service.ErrThrottled},
		{"one attempt", "throttle", "1", 1, service.ErrThrottled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := bedrockEndpoint(t, tt.mode)
			if tt.maxAttempts == "" {
				for _, key := range []string{"AWS_MAX_ATTEMPTS", "AWS_RETRY_MODE"} {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				t.Setenv("AWS_MAX_ATTEMPTS", tt.maxAttempts)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			start := time.Now()
			_, err := New().GenerateTopic(ctx, topicRequest())
			elapsed := time.Since(start)
			if !errors.Is(err, tt.class) {
				t.Fatalf("error = %v, want %v", err, tt.class)
			}
			if s.Count() != tt.attempts || elapsed > 6*time.Second {
				t.Fatalf("requests = %d, elapsed = %s; want %d requests within 6s", s.Count(), elapsed, tt.attempts)
			}
		})
	}
}
