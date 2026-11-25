package config

import (
	"fmt"
	"strings"
)

// ContentDestinations names the enabled trace receivers allowed to receive
// prompt, completion and input/output content.
type ContentDestinations struct {
	LangSmith bool
	OTLP      bool
}

// Tracing controls the OTLP trace exporter and which receivers may receive content.
type Tracing struct {
	Enabled bool
	// Content lists the receivers granted content. The zero value grants none.
	Content ContentDestinations
}

// CaptureContent reports whether any enabled receiver may receive content.
// When it is false no prompt, completion or input/output value is built.
func (t Tracing) CaptureContent() bool { return t.Content.LangSmith || t.Content.OTLP }

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

// loadTraceContent resolves the content policy once both exporter switches are known.
func loadTraceContent(getenv func(string) string, c *Config) error {
	global := true
	switch getenv("TRACING_CAPTURE_CONTENT") {
	case "", "true":
	case "false":
		global = false
	default:
		return fmt.Errorf("TRACING_CAPTURE_CONTENT must be true or false")
	}
	var granted ContentDestinations
	list := getenv("TRACING_CONTENT_DESTINATIONS")
	if strings.TrimSpace(list) != "" {
		for entry := range strings.SplitSeq(list, ",") {
			switch strings.TrimSpace(entry) {
			case "langsmith":
				if granted.LangSmith {
					return fmt.Errorf("TRACING_CONTENT_DESTINATIONS lists a destination twice")
				}
				if !c.LangSmith.Enabled {
					return fmt.Errorf("TRACING_CONTENT_DESTINATIONS names langsmith, which requires LANGSMITH_TRACING=true")
				}
				granted.LangSmith = true
			case "otlp":
				if granted.OTLP {
					return fmt.Errorf("TRACING_CONTENT_DESTINATIONS lists a destination twice")
				}
				if !c.Tracing.Enabled {
					return fmt.Errorf("TRACING_CONTENT_DESTINATIONS names otlp, which requires TRACING_ENABLED=true")
				}
				granted.OTLP = true
			case "":
				return fmt.Errorf("TRACING_CONTENT_DESTINATIONS contains an empty entry")
			default:
				return fmt.Errorf("TRACING_CONTENT_DESTINATIONS accepts only langsmith and otlp")
			}
		}
	}
	// An explicit false vetoes every destination, and a true switch alone grants none.
	if global {
		c.Tracing.Content = granted
	}
	return nil
}
