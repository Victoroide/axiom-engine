package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

func loadLangSmith(getenv func(string) string, c *Config) error {
	switch getenv("LANGSMITH_TRACING") {
	case "", "false":
	case "true":
		c.LangSmith.Enabled = true
	default:
		return fmt.Errorf("LANGSMITH_TRACING must be true or false")
	}
	c.LangSmith.APIKey = getenv("LANGSMITH_API_KEY")
	c.LangSmith.Project = getenv("LANGSMITH_PROJECT")
	c.LangSmith.Endpoint = getenv("LANGSMITH_ENDPOINT")
	if c.LangSmith.Endpoint == "" {
		c.LangSmith.Endpoint = "https://api.smith.langchain.com"
	}
	c.LangSmith.Endpoint = strings.TrimRight(c.LangSmith.Endpoint, "/")
	u, err := url.Parse(c.LangSmith.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("LANGSMITH_ENDPOINT must be an absolute HTTPS URL without credentials, query or fragment")
	}
	loopback := strings.EqualFold(u.Hostname(), "localhost")
	if addr, err := netip.ParseAddr(u.Hostname()); err == nil {
		loopback = addr.IsLoopback()
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return fmt.Errorf("LANGSMITH_ENDPOINT requires HTTPS except on loopback")
	}
	if c.LangSmith.Enabled && strings.TrimSpace(c.LangSmith.APIKey) == "" {
		return fmt.Errorf("LANGSMITH_API_KEY is required when LANGSMITH_TRACING=true")
	}
	return nil
}
