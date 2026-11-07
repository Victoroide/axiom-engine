package config

import (
	"fmt"
	"net/url"
	"strings"
)

// LLM selects the provider and generation model used by the planner.
type LLM struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   string `json:"-"`
}

// DefaultLLM returns the planner settings used when no selection is provided.
func DefaultLLM() LLM { return LLM{Provider: "aws-bedrock", Model: "amazon.nova-micro-v1:0"} }

func loadLLM(getenv func(string) string, c *Config) error {
	defaults := DefaultLLM()
	p := getenv("LLM_PROVIDER")
	if p == "" {
		p = defaults.Provider
	}
	l := LLM{Provider: p, Model: getenv("LLM_MODEL"), BaseURL: getenv("LLM_BASE_URL")}
	key := ""
	switch p {
	case "aws-bedrock":
		if l.Model == "" {
			l.Model = defaults.Model
		}
	case "openai":
		key = "OPENAI_API_KEY"
	case "anthropic":
		key = "ANTHROPIC_API_KEY"
	case "google-ai":
		key = "GOOGLE_API_KEY"
	case "ollama":
		if l.BaseURL == "" {
			l.BaseURL = "http://localhost:11434/v1"
		}
	case "openai-generic":
		if l.BaseURL == "" {
			return fmt.Errorf("LLM_BASE_URL is required for openai-generic")
		}
		l.APIKey = getenv("LLM_API_KEY")
	default:
		return fmt.Errorf("LLM_PROVIDER must be aws-bedrock, openai, anthropic, google-ai, ollama or openai-generic")
	}
	if strings.TrimSpace(l.Model) == "" || strings.TrimSpace(l.Model) != l.Model {
		return fmt.Errorf("LLM_MODEL is required and must have no surrounding whitespace")
	}
	if key != "" {
		l.APIKey = getenv(key)
		if strings.TrimSpace(l.APIKey) == "" {
			return fmt.Errorf("%s is required for %s", key, p)
		}
	}
	if l.BaseURL != "" {
		if p != "openai" && p != "ollama" && p != "openai-generic" {
			return fmt.Errorf("LLM_BASE_URL is supported only for openai, ollama or openai-generic")
		}
		u, err := url.Parse(l.BaseURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("LLM_BASE_URL must be an absolute HTTP or HTTPS URL without credentials, query or fragment")
		}
		l.BaseURL = strings.TrimRight(l.BaseURL, "/")
	}
	c.LLM = l
	return nil
}
