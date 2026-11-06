package config

import (
	"fmt"
	"strconv"
)

// Mastery limits the learner history accepted by the service.
type Mastery struct {
	MaxTopics int
}

func loadMastery(getenv func(string) string, c *Config) error {
	c.Mastery.MaxTopics = 100
	if value := getenv("MAX_MASTERED_TOPICS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("MAX_MASTERED_TOPICS must be a positive integer")
		}
		c.Mastery.MaxTopics = n
	}
	return nil
}
