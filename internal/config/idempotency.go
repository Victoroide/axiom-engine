package config

import (
	"fmt"
	"strconv"
	"time"
)

// Idempotency configures retention of successful plan responses.
type Idempotency struct {
	TTL        time.Duration
	MaxEntries int
}

func loadIdempotency(getenv func(string) string, c *Config) error {
	c.Idempotency = Idempotency{TTL: 24 * time.Hour, MaxEntries: 10000}
	if v := getenv("IDEMPOTENCY_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return fmt.Errorf("IDEMPOTENCY_TTL must be a non-negative duration")
		}
		c.Idempotency.TTL = d
	}
	if v := getenv("IDEMPOTENCY_MAX_ENTRIES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("IDEMPOTENCY_MAX_ENTRIES must be a positive integer")
		}
		c.Idempotency.MaxEntries = n
	}
	return nil
}
