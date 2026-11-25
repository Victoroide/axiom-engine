package config

import (
	"fmt"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/auth"
)

var verifiedAuthSettings = [...]string{
	"AXIOM_AUTH_ISSUER",
	"AXIOM_AUTH_AUDIENCE",
	"AXIOM_AUTH_JWKS_URL",
	"AXIOM_AUTH_ALGORITHMS",
	"AXIOM_AUTH_TENANT_CLAIM",
	"AXIOM_AUTH_SCOPE_CLAIM",
	"AXIOM_AUTH_CLIENT_ID_CLAIM",
	"AXIOM_AUTH_REGISTERED_CLIENTS",
}

func loadHTTPAuth(getenv func(string) string, c *Config) error {
	mode := getenv("AXIOM_AUTH_MODE")
	switch mode {
	case "", "shared", "verified":
	default:
		return fmt.Errorf("AXIOM_AUTH_MODE must be shared or verified")
	}
	switch getenv("AXIOM_AUTH_DISABLED") {
	case "", "false":
	case "true":
		c.AuthDisabled = true
		if mode != "" {
			return fmt.Errorf("AXIOM_AUTH_DISABLED=true requires AXIOM_AUTH_MODE to be unset")
		}
	default:
		return fmt.Errorf("AXIOM_AUTH_DISABLED must be true or false")
	}
	if mode == "verified" {
		return loadVerifiedAuth(getenv, c)
	}
	for _, key := range verifiedAuthSettings {
		if getenv(key) != "" {
			return fmt.Errorf("%s requires AXIOM_AUTH_MODE=verified", key)
		}
	}
	return loadSharedAuth(getenv, c)
}

func loadSharedAuth(getenv func(string) string, c *Config) error {
	c.InternalToken = getenv("AXIOM_INTERNAL_TOKEN")
	c.InternalTokenPrevious = getenv("AXIOM_INTERNAL_TOKEN_PREVIOUS")
	if c.InternalToken == "" && !c.AuthDisabled {
		return fmt.Errorf("AXIOM_INTERNAL_TOKEN is required unless AXIOM_AUTH_DISABLED=true")
	}
	for _, token := range []struct{ key, value string }{
		{"AXIOM_INTERNAL_TOKEN", c.InternalToken},
		{"AXIOM_INTERNAL_TOKEN_PREVIOUS", c.InternalTokenPrevious},
	} {
		if token.value != "" && (len(token.value) < 32 || strings.ContainsFunc(token.value, func(r rune) bool { return r < 0x21 || r > 0x7e })) {
			return fmt.Errorf("%s must be at least 32 visible ASCII characters without whitespace", token.key)
		}
	}
	if c.InternalTokenPrevious != "" && c.InternalTokenPrevious == c.InternalToken {
		return fmt.Errorf("AXIOM_INTERNAL_TOKEN_PREVIOUS must differ from AXIOM_INTERNAL_TOKEN")
	}
	if c.AuthDisabled && c.InternalTokenPrevious != "" {
		return fmt.Errorf("AXIOM_AUTH_DISABLED must be false when AXIOM_INTERNAL_TOKEN_PREVIOUS is configured")
	}
	if c.AuthDisabled && c.InternalToken != "" {
		return fmt.Errorf("AXIOM_AUTH_DISABLED must be false when AXIOM_INTERNAL_TOKEN is configured")
	}
	return nil
}

func loadVerifiedAuth(getenv func(string) string, c *Config) error {
	for _, key := range []string{"AXIOM_INTERNAL_TOKEN", "AXIOM_INTERNAL_TOKEN_PREVIOUS"} {
		if getenv(key) != "" {
			return fmt.Errorf("%s cannot be used with AXIOM_AUTH_MODE=verified", key)
		}
	}
	settings := auth.Config{
		Issuer:     getenv("AXIOM_AUTH_ISSUER"),
		Audience:   getenv("AXIOM_AUTH_AUDIENCE"),
		JWKSURL:    getenv("AXIOM_AUTH_JWKS_URL"),
		Algorithms: authCSV(getenv("AXIOM_AUTH_ALGORITHMS")),
		Claims: auth.ClaimMapping{
			Tenant:            getenv("AXIOM_AUTH_TENANT_CLAIM"),
			Scope:             getenv("AXIOM_AUTH_SCOPE_CLAIM"),
			ClientID:          getenv("AXIOM_AUTH_CLIENT_ID_CLAIM"),
			RegisteredClients: authCSV(getenv("AXIOM_AUTH_REGISTERED_CLIENTS")),
		},
	}
	for _, setting := range []struct{ key, value string }{
		{"AXIOM_AUTH_ISSUER", settings.Issuer},
		{"AXIOM_AUTH_AUDIENCE", settings.Audience},
		{"AXIOM_AUTH_JWKS_URL", settings.JWKSURL},
	} {
		if setting.value == "" {
			return fmt.Errorf("%s is required with AXIOM_AUTH_MODE=verified", setting.key)
		}
	}
	if settings.Claims.Tenant == "" {
		settings.Claims.Tenant = "tenant_id"
	}
	if len(settings.Algorithms) == 0 {
		settings.Algorithms = []string{"RS256"}
	}
	verifier, err := auth.New(settings)
	if err != nil {
		return fmt.Errorf("AXIOM_AUTH_MODE=verified: %w", err)
	}
	c.VerifiedAuth, c.verifiedSettings = verifier, settings
	return nil
}

func authCSV(value string) []string {
	if value == "" {
		return nil
	}
	fields := strings.Split(value, ",")
	for i, field := range fields {
		fields[i] = strings.TrimSpace(field)
	}
	return fields
}
