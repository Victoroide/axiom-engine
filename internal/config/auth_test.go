package config

import (
	"maps"
	"strings"
	"testing"
)

func TestLoadHTTPAuth(t *testing.T) {
	shared := strings.Repeat("s", 32)
	previous := strings.Repeat("p", 32)
	verified := map[string]string{
		"AXIOM_AUTH_MODE":     "verified",
		"AXIOM_AUTH_ISSUER":   "https://issuer.example/tenant",
		"AXIOM_AUTH_AUDIENCE": "https://api.example",
		"AXIOM_AUTH_JWKS_URL": "https://issuer.example/keys",
	}
	for _, tc := range []struct {
		name string
		base map[string]string
		set  map[string]string
		want string
	}{
		{name: "default shared", set: map[string]string{"AXIOM_INTERNAL_TOKEN": shared}},
		{name: "explicit shared and rotation", set: map[string]string{"AXIOM_AUTH_MODE": "shared", "AXIOM_INTERNAL_TOKEN": shared, "AXIOM_INTERNAL_TOKEN_PREVIOUS": previous}},
		{name: "legacy disabled", set: map[string]string{"AXIOM_AUTH_DISABLED": "true"}},
		{name: "unknown mode", set: map[string]string{"AXIOM_AUTH_MODE": "unknown", "AXIOM_INTERNAL_TOKEN": shared}, want: "AXIOM_AUTH_MODE"},
		{name: "shared without token", set: map[string]string{"AXIOM_AUTH_MODE": "shared"}, want: "AXIOM_INTERNAL_TOKEN"},
		{name: "shared with disabled", set: map[string]string{"AXIOM_AUTH_MODE": "shared", "AXIOM_AUTH_DISABLED": "true"}, want: "AXIOM_AUTH_DISABLED"},
		{name: "verified", base: verified},
		{name: "verified with explicit false", base: verified, set: map[string]string{"AXIOM_AUTH_DISABLED": "false"}},
		{name: "verified with disabled", base: verified, set: map[string]string{"AXIOM_AUTH_DISABLED": "true"}, want: "AXIOM_AUTH_DISABLED"},
		{name: "verified with current token", base: verified, set: map[string]string{"AXIOM_INTERNAL_TOKEN": shared}, want: "AXIOM_INTERNAL_TOKEN"},
		{name: "verified with previous token", base: verified, set: map[string]string{"AXIOM_INTERNAL_TOKEN_PREVIOUS": previous}, want: "AXIOM_INTERNAL_TOKEN_PREVIOUS"},
		{name: "missing issuer", base: verified, set: map[string]string{"AXIOM_AUTH_ISSUER": ""}, want: "AXIOM_AUTH_ISSUER"},
		{name: "missing audience", base: verified, set: map[string]string{"AXIOM_AUTH_AUDIENCE": ""}, want: "AXIOM_AUTH_AUDIENCE"},
		{name: "missing key endpoint", base: verified, set: map[string]string{"AXIOM_AUTH_JWKS_URL": ""}, want: "AXIOM_AUTH_JWKS_URL"},
		{name: "insecure issuer", base: verified, set: map[string]string{"AXIOM_AUTH_ISSUER": "http://issuer.example"}, want: "invalid issuer"},
		{name: "insecure key endpoint", base: verified, set: map[string]string{"AXIOM_AUTH_JWKS_URL": "http://issuer.example/keys"}, want: "invalid issuer"},
		{name: "allowed algorithms", base: verified, set: map[string]string{"AXIOM_AUTH_ALGORITHMS": "RS256, ES256"}},
		{name: "explicit ES256", base: verified, set: map[string]string{"AXIOM_AUTH_ALGORITHMS": "ES256"}},
		{name: "unsupported algorithm", base: verified, set: map[string]string{"AXIOM_AUTH_ALGORITHMS": "RS256,HS256"}, want: "unsupported signing algorithm"},
		{name: "duplicate algorithm", base: verified, set: map[string]string{"AXIOM_AUTH_ALGORITHMS": "RS256,RS256"}, want: "duplicate signing algorithm"},
		{name: "empty algorithm entry", base: verified, set: map[string]string{"AXIOM_AUTH_ALGORITHMS": "RS256,"}, want: "unsupported signing algorithm"},
		{name: "custom claims and registered clients", base: verified, set: map[string]string{
			"AXIOM_AUTH_TENANT_CLAIM": "org", "AXIOM_AUTH_SCOPE_CLAIM": "permissions",
			"AXIOM_AUTH_CLIENT_ID_CLAIM": "client_id", "AXIOM_AUTH_REGISTERED_CLIENTS": "one,two",
		}},
		{name: "overlapping tenant and scope", base: verified, set: map[string]string{"AXIOM_AUTH_SCOPE_CLAIM": "tenant_id"}, want: "claim mapping"},
		{name: "reserved subject claim", base: verified, set: map[string]string{"AXIOM_AUTH_TENANT_CLAIM": "sub"}, want: "claim mapping"},
		{name: "malformed claim", base: verified, set: map[string]string{"AXIOM_AUTH_TENANT_CLAIM": "tenant id"}, want: "claim mapping"},
		{name: "missing clients", base: verified, set: map[string]string{"AXIOM_AUTH_CLIENT_ID_CLAIM": "client_id"}, want: "registered client mapping"},
		{name: "missing client claim", base: verified, set: map[string]string{"AXIOM_AUTH_REGISTERED_CLIENTS": "one"}, want: "registered client mapping"},
		{name: "duplicate clients", base: verified, set: map[string]string{"AXIOM_AUTH_CLIENT_ID_CLAIM": "client_id", "AXIOM_AUTH_REGISTERED_CLIENTS": "one,one"}, want: "invalid registered client"},
		{name: "invalid client", base: verified, set: map[string]string{"AXIOM_AUTH_CLIENT_ID_CLAIM": "client_id", "AXIOM_AUTH_REGISTERED_CLIENTS": "one,"}, want: "invalid registered client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := make(map[string]string)
			maps.Copy(env, tc.base)
			maps.Copy(env, tc.set)
			_, err := Load(func(key string) string { return env[key] })
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadHTTPAuthRejectsStrayVerifierSettings(t *testing.T) {
	for _, key := range []string{
		"AXIOM_AUTH_ISSUER", "AXIOM_AUTH_AUDIENCE", "AXIOM_AUTH_JWKS_URL", "AXIOM_AUTH_ALGORITHMS",
		"AXIOM_AUTH_TENANT_CLAIM", "AXIOM_AUTH_SCOPE_CLAIM", "AXIOM_AUTH_CLIENT_ID_CLAIM", "AXIOM_AUTH_REGISTERED_CLIENTS",
	} {
		t.Run(key, func(t *testing.T) {
			for _, base := range []map[string]string{
				{"AXIOM_INTERNAL_TOKEN": strings.Repeat("s", 32)},
				{"AXIOM_AUTH_MODE": "shared", "AXIOM_INTERNAL_TOKEN": strings.Repeat("s", 32)},
				{"AXIOM_AUTH_DISABLED": "true"},
			} {
				env := maps.Clone(base)
				env[key] = "stray"
				_, err := Load(func(key string) string { return env[key] })
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Errorf("error=%v, want %s", err, key)
				}
			}
		})
	}
}
