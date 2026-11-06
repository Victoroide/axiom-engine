package config

import (
	"fmt"
	"net/url"

	"github.com/redis/go-redis/v9"
)

// Redis configures optional shared request storage.
type Redis struct {
	URL string
}

func loadRedis(getenv func(string) string, c *Config) error {
	c.Redis.URL = getenv("REDIS_URL")
	if c.Redis.URL == "" {
		return nil
	}
	parsed, parseErr := url.Parse(c.Redis.URL)
	if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") {
		return fmt.Errorf("REDIS_URL must be a valid redis:// or rediss:// URL")
	}
	options, err := redis.ParseURL(c.Redis.URL)
	if err != nil || options.Addr == "" {
		return fmt.Errorf("REDIS_URL must be a valid redis:// or rediss:// URL")
	}
	return nil
}
