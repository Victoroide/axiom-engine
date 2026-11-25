package config

import (
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"
)

func TestLoadTenantQuota(t *testing.T) {
	verified := map[string]string{
		"AXIOM_AUTH_MODE": "verified", "AXIOM_AUTH_ISSUER": "https://issuer.example",
		"AXIOM_AUTH_AUDIENCE": "https://api.example", "AXIOM_AUTH_JWKS_URL": "https://issuer.example/keys",
		"REDIS_URL": "redis://localhost:6379", "AXIOM_TENANT_QUOTA_ENABLED": "true",
	}
	entries := make([]string, 257)
	for i := range entries {
		entries[i] = fmt.Sprintf("%q:1", fmt.Sprintf("tenant-%d", i))
	}
	const enabled = "AXIOM_TENANT_QUOTA_ENABLED"
	const maximum = "AXIOM_TENANT_QUOTA_MAX"
	const window = "AXIOM_TENANT_QUOTA_WINDOW"
	const overrides = "AXIOM_TENANT_QUOTA_OVERRIDES"
	for _, tc := range []struct {
		name, key, value, want string
	}{
		{name: "enabled defaults"},
		{name: "off explicitly", key: enabled, value: "false"},
		{name: "off by omission", key: enabled},
		{name: "invalid flag", key: enabled, value: "TRUE", want: enabled},
		{name: "missing Redis", key: "REDIS_URL", want: "REDIS_URL"},
		{name: "minimum maximum", key: maximum, value: "1"},
		{name: "maximum maximum", key: maximum, value: "100000"},
		{name: "zero maximum", key: maximum, value: "0", want: maximum},
		{name: "overflow maximum", key: maximum, value: "100001", want: maximum},
		{name: "fractional maximum", key: maximum, value: "1.5", want: maximum},
		{name: "minimum window", key: window, value: "1s"},
		{name: "maximum window", key: window, value: "1h"},
		{name: "whole seconds in milliseconds", key: window, value: "2000ms"},
		{name: "short window", key: window, value: "999ms", want: window},
		{name: "partial second", key: window, value: "1s1ms", want: window},
		{name: "long window", key: window, value: "1h1s", want: window},
		{name: "valid overrides", key: overrides, value: `{"tenant-1":1,"Tenant-1":100000}`},
		{name: "duplicate overrides", key: overrides, value: `{"private-tenant":1,"private-tenant":2}`, want: overrides},
		{name: "duplicate escaped key", key: overrides, value: `{"private-tenant":1,"private-\u0074enant":2}`, want: overrides},
		{name: "blank tenant", key: overrides, value: `{"":1}`, want: overrides},
		{name: "whitespace in tenant", key: overrides, value: `{"private-tenant abc":1}`, want: overrides},
		{name: "non ASCII tenant", key: overrides, value: `{"private-tenant-é":1}`, want: overrides},
		{name: "tenant too long", key: overrides, value: `{"` + strings.Repeat("a", 129) + `":1}`, want: overrides},
		{name: "null policy", key: overrides, value: "null", want: overrides},
		{name: "array policy", key: overrides, value: "[]", want: overrides},
		{name: "string policy", key: overrides, value: `"{}"`, want: overrides},
		{name: "multiple objects", key: overrides, value: "{}{}", want: overrides},
		{name: "invalid encoding", key: overrides, value: "{\"\xff\":1}", want: overrides},
		{name: "null maximum", key: overrides, value: `{"private-tenant":null}`, want: overrides},
		{name: "nested maximum", key: overrides, value: `{"private-tenant":{"max":1}}`, want: overrides},
		{name: "string maximum", key: overrides, value: `{"private-tenant":"1"}`, want: overrides},
		{name: "fractional override", key: overrides, value: `{"private-tenant":1.5}`, want: overrides},
		{name: "zero override", key: overrides, value: `{"private-tenant":0}`, want: overrides},
		{name: "oversize override", key: overrides, value: `{"private-tenant":100001}`, want: overrides},
		{name: "maximum entries", key: overrides, value: "{" + strings.Join(entries[:256], ",") + "}"},
		{name: "too many entries", key: overrides, value: "{" + strings.Join(entries, ",") + "}", want: overrides},
		{name: "maximum bytes", key: overrides, value: "{}" + strings.Repeat(" ", 65534)},
		{name: "too many bytes", key: overrides, value: "{}" + strings.Repeat(" ", 65535), want: overrides},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := maps.Clone(verified)
			env[tc.key] = tc.value
			cfg, err := Load(func(key string) string { return env[key] })
			assertQuotaConfigError(t, err, tc.want)
			if err != nil {
				return
			}
			if env[enabled] != "true" {
				if cfg.TenantQuota != nil {
					t.Fatal("tenant quota should be disabled")
				}
				return
			}
			switch tc.name {
			case "enabled defaults":
				if cfg.TenantQuota.Window() != time.Minute || cfg.TenantQuota.Maximum("unlisted") != 50 {
					t.Fatal("expected the default of 50 actions per minute")
				}
			case "valid overrides":
				if cfg.TenantQuota.Maximum("tenant-1") != 1 || cfg.TenantQuota.Maximum("Tenant-1") != 100_000 ||
					cfg.TenantQuota.Maximum("TENANT-1") != 50 {
					t.Fatal("expected exact tenant lookup with the default for unlisted tenants")
				}
			}
		})
	}
	for _, setting := range []string{maximum, window, overrides} {
		t.Run("disabled with "+setting, func(t *testing.T) {
			env := maps.Clone(verified)
			env[enabled], env[setting] = "false", "1"
			_, err := Load(func(key string) string { return env[key] })
			assertQuotaConfigError(t, err, setting)
			delete(env, enabled)
			_, err = Load(func(key string) string { return env[key] })
			assertQuotaConfigError(t, err, setting)
		})
	}
	for _, identity := range []map[string]string{
		{"AXIOM_AUTH_DISABLED": "true"},
		{"AXIOM_INTERNAL_TOKEN": strings.Repeat("s", 32)},
	} {
		identity[enabled], identity["REDIS_URL"] = "true", "redis://localhost:6379"
		_, err := Load(func(key string) string { return identity[key] })
		assertQuotaConfigError(t, err, "AXIOM_AUTH_MODE")
	}
}

func assertQuotaConfigError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatal(err)
		}
	} else if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "private-tenant") {
		t.Errorf("error=%v, want a redacted error naming %s", err, want)
	}
}
