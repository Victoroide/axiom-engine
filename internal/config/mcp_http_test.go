package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	mcpTestURL    = "https://mcp.example.test/mcp"
	mcpTestIssuer = "https://issuer.example.test/tenant"
)

type mcpTLSFiles struct{ cert, key string }

func writeMCPTLS(t *testing.T, notAfter time.Time) mcpTLSFiles {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mcp.example.test"},
		DNSNames:  []string{"mcp.example.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := mcpTLSFiles{cert: filepath.Join(dir, "tls.crt"), key: filepath.Join(dir, "tls.key")}
	if err := os.WriteFile(files.cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return files
}

func mcpHTTPEnv(t *testing.T) map[string]string {
	t.Helper()
	files := writeMCPTLS(t, time.Now().Add(time.Hour))
	return map[string]string{
		"AXIOM_AUTH_MODE": "verified", "AXIOM_AUTH_ISSUER": mcpTestIssuer,
		"AXIOM_AUTH_AUDIENCE": "https://api.example.test/axiom", "AXIOM_AUTH_JWKS_URL": "https://issuer.example.test/keys",
		"AXIOM_TENANT_QUOTA_ENABLED": "true", "REDIS_URL": "redis://localhost:6379",
		"MCP_PUBLIC_URL": mcpTestURL, "MCP_TLS_CERT_FILE": files.cert, "MCP_TLS_KEY_FILE": files.key,
	}
}

func loadMCPHTTP(env map[string]string) (Config, MCPHTTP, error) {
	return LoadMCPHTTP(func(key string) string { return env[key] })
}

func TestLoadMCPHTTPDefaults(t *testing.T) {
	cfg, remote, err := loadMCPHTTP(mcpHTTPEnv(t))
	if err != nil {
		t.Fatal(err)
	}
	if remote.PublicURL != mcpTestURL || remote.Issuer != mcpTestIssuer || remote.BindAddr != "127.0.0.1:8443" ||
		len(remote.AllowedOrigins) != 1 || remote.AllowedOrigins[0] != "https://mcp.example.test" ||
		remote.Verifier == nil || cfg.VerifiedAuth == nil || remote.Verifier == cfg.VerifiedAuth ||
		len(remote.Certificate.Certificate) != 1 || cfg.TenantQuota == nil {
		t.Fatalf("remote=%+v cfg.TenantQuota=%v", remote, cfg.TenantQuota)
	}
	if got := remote.MetadataURL(); got != "https://mcp.example.test/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("metadata URL = %s", got)
	}
	if remote.Authority() != "mcp.example.test" {
		t.Fatalf("authority = %s", remote.Authority())
	}
}

func TestLoadMCPHTTPRequirements(t *testing.T) {
	for _, tc := range []struct {
		name, key, value, want string
	}{
		{name: "shared mode", key: "AXIOM_AUTH_MODE", value: "shared", want: "AXIOM_AUTH_MODE=verified"},
		{name: "unset mode", key: "AXIOM_AUTH_MODE", want: "AXIOM_AUTH_MODE=verified"},
		{name: "legacy disabled", key: "AXIOM_AUTH_DISABLED", value: "true", want: "AXIOM_AUTH_DISABLED"},
		{name: "quota off", key: "AXIOM_TENANT_QUOTA_ENABLED", value: "false", want: "AXIOM_TENANT_QUOTA_ENABLED=true"},
		{name: "quota unset", key: "AXIOM_TENANT_QUOTA_ENABLED", want: "AXIOM_TENANT_QUOTA_ENABLED=true"},
		{name: "no Redis", key: "REDIS_URL", want: "REDIS_URL"},
		{name: "no issuer", key: "AXIOM_AUTH_ISSUER", want: "AXIOM_AUTH_ISSUER"},
		{name: "insecure key set", key: "AXIOM_AUTH_JWKS_URL", value: "http://issuer.example.test/keys", want: "invalid issuer"},
		{name: "invalid claim mapping", key: "AXIOM_AUTH_SCOPE_CLAIM", value: "tenant_id", want: "claim mapping"},
		{name: "missing public URL", key: "MCP_PUBLIC_URL", want: "MCP_PUBLIC_URL"},
		{name: "REST audience reused", key: "AXIOM_AUTH_AUDIENCE", value: mcpTestURL, want: "AXIOM_AUTH_AUDIENCE must differ"},
		{name: "missing certificate", key: "MCP_TLS_CERT_FILE", want: "plaintext"},
		{name: "missing key", key: "MCP_TLS_KEY_FILE", want: "plaintext"},
		{name: "unreadable certificate", key: "MCP_TLS_CERT_FILE", value: "/nonexistent/tls.crt", want: "MCP_TLS_CERT_FILE"},
		{name: "unreadable key", key: "MCP_TLS_KEY_FILE", value: "/nonexistent/tls.key", want: "MCP_TLS_KEY_FILE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := mcpHTTPEnv(t)
			env[tc.key] = tc.value
			_, _, err := loadMCPHTTP(env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadMCPHTTPPublicURL(t *testing.T) {
	for _, tc := range []struct {
		name, value, authority string
		ok                     bool
	}{
		{name: "host", value: "https://mcp.example.test/mcp", authority: "mcp.example.test", ok: true},
		{name: "explicit port", value: "https://mcp.example.test:9443/mcp", authority: "mcp.example.test:9443", ok: true},
		{name: "default port", value: "https://mcp.example.test:443/mcp", authority: "mcp.example.test", ok: true},
		{name: "IPv4 literal", value: "https://192.0.2.10:8443/mcp", authority: "192.0.2.10:8443", ok: true},
		{name: "IPv6 literal", value: "https://[2001:db8::1]:8443/mcp", authority: "[2001:db8::1]:8443", ok: true},
		{name: "plain HTTP", value: "http://mcp.example.test/mcp"},
		{name: "relative", value: "/mcp"},
		{name: "no host", value: "https:///mcp"},
		{name: "other path", value: "https://mcp.example.test/api"},
		{name: "root path", value: "https://mcp.example.test/"},
		{name: "no path", value: "https://mcp.example.test"},
		{name: "trailing slash", value: "https://mcp.example.test/mcp/"},
		{name: "nested path", value: "https://mcp.example.test/v1/mcp"},
		{name: "encoded path", value: "https://mcp.example.test/m%63p"},
		{name: "dot segment", value: "https://mcp.example.test/./mcp"},
		{name: "credentials", value: "https://canary-user@mcp.example.test/mcp"},
		{name: "query", value: "https://mcp.example.test/mcp?a=b"},
		{name: "empty query", value: "https://mcp.example.test/mcp?"},
		{name: "fragment", value: "https://mcp.example.test/mcp#x"},
		{name: "empty fragment", value: "https://mcp.example.test/mcp#"},
		{name: "uppercase host", value: "https://MCP.example.test/mcp"},
		{name: "uppercase scheme", value: "HTTPS://mcp.example.test/mcp"},
		{name: "trailing dot host", value: "https://mcp.example.test./mcp"},
		{name: "port zero", value: "https://mcp.example.test:0/mcp"},
		{name: "port too large", value: "https://mcp.example.test:65536/mcp"},
		{name: "padded port", value: "https://mcp.example.test:08443/mcp"},
		{name: "empty port", value: "https://mcp.example.test:/mcp"},
		{name: "space", value: "https://mcp.example.test/mcp "},
		{name: "unicode host", value: "https://mcp.exämple.test/mcp"},
		{name: "wildcard host", value: "https://*.example.test/mcp"},
		{name: "underscore host", value: "https://mcp_host.example.test/mcp"},
		{name: "oversize", value: "https://" + strings.Repeat("a", 1020) + ".test/mcp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := mcpHTTPEnv(t)
			env["MCP_PUBLIC_URL"] = tc.value
			_, remote, err := loadMCPHTTP(env)
			if !tc.ok {
				if err == nil || !strings.Contains(err.Error(), "MCP_PUBLIC_URL") || strings.Contains(err.Error(), "canary-user") {
					t.Fatalf("error = %v, want a redacted MCP_PUBLIC_URL error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if remote.PublicURL != tc.value || remote.Authority() != tc.authority {
				t.Fatalf("URL=%s authority=%s", remote.PublicURL, remote.Authority())
			}
		})
	}
}

func TestLoadMCPHTTPBindAddress(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		ok          bool
	}{
		{name: "loopback", value: "127.0.0.1:9443", ok: true},
		{name: "explicit remote address", value: "192.0.2.10:8443", ok: true},
		{name: "IPv6 address", value: "[2001:db8::1]:8443", ok: true},
		{name: "IPv4 wildcard", value: "0.0.0.0:8443"},
		{name: "IPv6 wildcard", value: "[::]:8443"},
		{name: "zoned IPv6 wildcard", value: "[::%lo0]:8443"},
		{name: "mapped wildcard", value: "[::ffff:0.0.0.0]:8443"},
		{name: "empty host", value: ":8443"},
		{name: "host name", value: "localhost:8443"},
		{name: "no port", value: "127.0.0.1"},
		{name: "port only", value: "8443"},
		{name: "port zero", value: "127.0.0.1:0"},
		{name: "port too large", value: "127.0.0.1:65536"},
		{name: "multicast", value: "224.0.0.1:8443"},
		{name: "whitespace", value: " 127.0.0.1:8443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := mcpHTTPEnv(t)
			env["MCP_BIND_ADDR"] = tc.value
			_, remote, err := loadMCPHTTP(env)
			if !tc.ok {
				if err == nil || !strings.Contains(err.Error(), "MCP_BIND_ADDR") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || remote.BindAddr != tc.value {
				t.Fatalf("bind=%q err=%v", remote.BindAddr, err)
			}
		})
	}
}

func TestLoadMCPHTTPAllowedOrigins(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        []string
	}{
		{name: "default is the public origin", want: []string{"https://mcp.example.test"}},
		{name: "explicit list replaces the default", value: "https://app.example.test, https://admin.example.test:8443", want: []string{"https://app.example.test", "https://admin.example.test:8443"}},
		{name: "wildcard", value: "*"},
		{name: "wildcard among origins", value: "https://app.example.test,*"},
		{name: "null", value: "null"},
		{name: "plain HTTP", value: "http://app.example.test"},
		{name: "path", value: "https://app.example.test/app"},
		{name: "trailing slash", value: "https://app.example.test/"},
		{name: "query", value: "https://app.example.test?a=b"},
		{name: "fragment", value: "https://app.example.test#x"},
		{name: "credentials", value: "https://canary-user@app.example.test"},
		{name: "uppercase host", value: "https://APP.example.test"},
		{name: "default port", value: "https://app.example.test:443"},
		{name: "subdomain wildcard", value: "https://*.example.test"},
		{name: "empty entry", value: "https://app.example.test,,https://admin.example.test"},
		{name: "duplicate", value: "https://app.example.test,https://app.example.test"},
		{name: "bare host", value: "app.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := mcpHTTPEnv(t)
			if tc.value != "" {
				env["MCP_ALLOWED_ORIGINS"] = tc.value
			}
			_, remote, err := loadMCPHTTP(env)
			if tc.want == nil {
				if err == nil || !strings.Contains(err.Error(), "MCP_ALLOWED_ORIGINS") || strings.Contains(err.Error(), "canary-user") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || !maps.Equal(setOf(remote.AllowedOrigins), setOf(tc.want)) {
				t.Fatalf("origins = %v err = %v", remote.AllowedOrigins, err)
			}
		})
	}
	t.Run("too many", func(t *testing.T) {
		env := mcpHTTPEnv(t)
		origins := make([]string, 33)
		for i := range origins {
			origins[i] = "https://app" + strings.Repeat("a", i) + ".example.test"
		}
		env["MCP_ALLOWED_ORIGINS"] = strings.Join(origins, ",")
		if _, _, err := loadMCPHTTP(env); err == nil || !strings.Contains(err.Error(), "MCP_ALLOWED_ORIGINS") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("default keeps a non-default port", func(t *testing.T) {
		env := mcpHTTPEnv(t)
		env["MCP_PUBLIC_URL"] = "https://mcp.example.test:9443/mcp"
		_, remote, err := loadMCPHTTP(env)
		if err != nil || len(remote.AllowedOrigins) != 1 || remote.AllowedOrigins[0] != "https://mcp.example.test:9443" {
			t.Fatalf("origins = %v err = %v", remote.AllowedOrigins, err)
		}
	})
	t.Run("default drops the default port", func(t *testing.T) {
		env := mcpHTTPEnv(t)
		env["MCP_PUBLIC_URL"] = "https://mcp.example.test:443/mcp"
		_, remote, err := loadMCPHTTP(env)
		if err != nil || len(remote.AllowedOrigins) != 1 || remote.AllowedOrigins[0] != "https://mcp.example.test" {
			t.Fatalf("origins = %v err = %v", remote.AllowedOrigins, err)
		}
	})
}

func setOf(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func TestLoadMCPHTTPCertificate(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		env := mcpHTTPEnv(t)
		files := writeMCPTLS(t, time.Now().Add(-time.Minute))
		env["MCP_TLS_CERT_FILE"], env["MCP_TLS_KEY_FILE"] = files.cert, files.key
		if _, _, err := loadMCPHTTP(env); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("mismatched key", func(t *testing.T) {
		env := mcpHTTPEnv(t)
		env["MCP_TLS_KEY_FILE"] = writeMCPTLS(t, time.Now().Add(time.Hour)).key
		if _, _, err := loadMCPHTTP(env); err == nil || !strings.Contains(err.Error(), "match") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("not a certificate", func(t *testing.T) {
		env := mcpHTTPEnv(t)
		env["MCP_TLS_CERT_FILE"] = env["MCP_TLS_KEY_FILE"]
		if _, _, err := loadMCPHTTP(env); err == nil || !strings.Contains(err.Error(), "MCP_TLS") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("errors omit key material", func(t *testing.T) {
		env := mcpHTTPEnv(t)
		data, err := os.ReadFile(env["MCP_TLS_KEY_FILE"])
		if err != nil {
			t.Fatal(err)
		}
		env["MCP_TLS_CERT_FILE"] = env["MCP_TLS_KEY_FILE"]
		_, _, err = loadMCPHTTP(env)
		if err == nil || strings.Contains(err.Error(), strings.TrimSpace(string(data))[40:80]) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestMCPHTTPSettingsDoNotAffectOtherCommands(t *testing.T) {
	for _, load := range []struct {
		name string
		fn   func(func(string) string) (Config, error)
	}{{"http", Load}, {"stdio", LoadMCP}} {
		t.Run(load.name, func(t *testing.T) {
			env := map[string]string{
				"AXIOM_AUTH_DISABLED": "true", "MCP_PUBLIC_URL": "not a url", "MCP_BIND_ADDR": "0.0.0.0:0",
				"MCP_TLS_CERT_FILE": "/nonexistent", "MCP_TLS_KEY_FILE": "/nonexistent", "MCP_ALLOWED_ORIGINS": "*",
			}
			if _, err := load.fn(func(key string) string { return env[key] }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadMCPHTTPUsesTheHTTPSettings(t *testing.T) {
	env := mcpHTTPEnv(t)
	env["REQUEST_TIMEOUT"], env["TRUSTED_PROXIES"], env["LLM_MAX_CONCURRENCY"] = "30s", "10.0.0.0/8", "3"
	cfg, _, err := loadMCPHTTP(env)
	if err != nil || cfg.RequestTimeout != 30*time.Second || cfg.LLMMaxConcurrency != 3 || len(cfg.TrustedProxies) != 1 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	env["REQUEST_TIMEOUT"] = "invalid"
	if _, _, err := loadMCPHTTP(env); err == nil || !strings.Contains(err.Error(), "REQUEST_TIMEOUT") {
		t.Fatalf("error = %v", err)
	}
}
