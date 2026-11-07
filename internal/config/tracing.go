package config

import "fmt"

// Tracing controls the OTLP trace exporter.
type Tracing struct {
	Enabled        bool
	CaptureContent bool
}

func loadTracing(getenv func(string) string, c *Config) error {
	switch getenv("TRACING_ENABLED") {
	case "", "false":
	case "true":
		c.Tracing.Enabled = true
	default:
		return fmt.Errorf("TRACING_ENABLED must be true or false")
	}
	return nil
}
