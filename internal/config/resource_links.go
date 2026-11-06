package config

import (
	"fmt"
	"time"
)

// ResourceLinks controls optional HTTP checks of recommended resources.
type ResourceLinks struct {
	Enabled bool
	Timeout time.Duration
}

func loadResourceLinks(getenv func(string) string, c *Config) error {
	c.ResourceLinks.Timeout = 3 * time.Second
	switch value := getenv("RESOURCE_LINK_CHECK"); value {
	case "", "false":
	case "true":
		c.ResourceLinks.Enabled = true
	default:
		return fmt.Errorf("RESOURCE_LINK_CHECK must be true or false")
	}
	if value := getenv("RESOURCE_LINK_TIMEOUT"); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("RESOURCE_LINK_TIMEOUT must be a positive duration")
		}
		c.ResourceLinks.Timeout = timeout
	}
	return nil
}
