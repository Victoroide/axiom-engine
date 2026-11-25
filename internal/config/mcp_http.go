package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Victoroide/axiom-engine/internal/auth"
)

const (
	// MCPEndpointPath is the fixed path of the remote tool endpoint.
	MCPEndpointPath = "/mcp"
	// MCPMetadataPath is where the endpoint's protected resource metadata is served.
	MCPMetadataPath = "/.well-known/oauth-protected-resource" + MCPEndpointPath

	defaultMCPBindAddr = "127.0.0.1:8443"
	maxMCPOrigins      = 32
	maxMCPURLBytes     = 1024
	maxMCPTLSFileBytes = 1 << 20
)

var dnsName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// MCPHTTP holds the settings that only the remote MCP command uses.
type MCPHTTP struct {
	// PublicURL is the exact resource identifier and token audience.
	PublicURL string
	// Issuer is the authorization server advertised in the resource metadata.
	Issuer string
	// BindAddr is a concrete IP address and nonzero port.
	BindAddr string
	// Certificate is the listener's TLS certificate and key.
	Certificate tls.Certificate
	// AllowedOrigins lists exact HTTPS browser origins.
	AllowedOrigins []string
	// Verifier checks access tokens issued for PublicURL.
	Verifier *auth.Verifier
}

// Authority returns the host and any non-default port of PublicURL, as a
// client sends it in the Host header.
func (m MCPHTTP) Authority() string {
	u, err := url.Parse(m.PublicURL)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Host, ":443")
}

// MetadataURL returns the public address of the protected resource metadata.
func (m MCPHTTP) MetadataURL() string {
	return "https://" + m.Authority() + MCPMetadataPath
}

// LoadMCPHTTP loads the settings of the remote MCP command. It requires
// verified authentication, a Redis tenant quota, TLS files and a public URL,
// and returns an error naming the setting that must change before any listener
// opens. Errors for MCP-specific settings omit their values.
func LoadMCPHTTP(getenv func(string) string) (Config, MCPHTTP, error) {
	if getenv("AXIOM_AUTH_MODE") != "verified" {
		return Config{}, MCPHTTP{}, errors.New("mcp-http requires AXIOM_AUTH_MODE=verified")
	}
	c, err := Load(getenv)
	if err != nil {
		return Config{}, MCPHTTP{}, err
	}
	if c.TenantQuota == nil {
		return Config{}, MCPHTTP{}, errors.New("mcp-http requires AXIOM_TENANT_QUOTA_ENABLED=true")
	}
	remote := MCPHTTP{PublicURL: getenv("MCP_PUBLIC_URL"), Issuer: c.verifiedSettings.Issuer, BindAddr: defaultMCPBindAddr}
	public, err := parseMCPPublicURL(remote.PublicURL)
	if err != nil {
		return Config{}, MCPHTTP{}, err
	}
	if remote.PublicURL == c.verifiedSettings.Audience {
		return Config{}, MCPHTTP{}, errors.New("AXIOM_AUTH_AUDIENCE must differ from MCP_PUBLIC_URL so that an API token cannot authorize MCP")
	}
	if value := getenv("MCP_BIND_ADDR"); value != "" {
		if !concreteBindAddr(value) {
			return Config{}, MCPHTTP{}, errors.New("MCP_BIND_ADDR must be a concrete IP address and a port from 1 to 65535, not a wildcard")
		}
		remote.BindAddr = value
	}
	if remote.AllowedOrigins, err = parseMCPOrigins(getenv("MCP_ALLOWED_ORIGINS"), public); err != nil {
		return Config{}, MCPHTTP{}, err
	}
	if remote.Certificate, err = loadMCPCertificate(getenv("MCP_TLS_CERT_FILE"), getenv("MCP_TLS_KEY_FILE")); err != nil {
		return Config{}, MCPHTTP{}, err
	}
	settings := c.verifiedSettings
	settings.Audience = remote.PublicURL
	if remote.Verifier, err = auth.New(settings); err != nil {
		return Config{}, MCPHTTP{}, fmt.Errorf("MCP_PUBLIC_URL: %w", err)
	}
	return c, remote, nil
}

func parseMCPPublicURL(raw string) (*url.URL, error) {
	bad := errors.New("MCP_PUBLIC_URL must be an absolute https URL with a lowercase host, no credentials, query or fragment, and the path " + MCPEndpointPath)
	if raw == "" {
		return nil, errors.New("MCP_PUBLIC_URL is required")
	}
	if len(raw) > maxMCPURLBytes || !visibleASCII(raw) || strings.ContainsAny(raw, "?#") || !strings.HasPrefix(raw, "https://") {
		return nil, bad
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Path != MCPEndpointPath || u.RawPath != "" {
		return nil, bad
	}
	if _, _, ok := splitAuthority(u.Host); !ok {
		return nil, bad
	}
	return u, nil
}

func parseMCPOrigins(raw string, public *url.URL) ([]string, error) {
	if raw == "" {
		return []string{"https://" + strings.TrimSuffix(public.Host, ":443")}, nil
	}
	bad := errors.New("MCP_ALLOWED_ORIGINS must list distinct exact https origins (scheme, lowercase host and optional non-default port), at most 32, with no wildcard, null, path, query or credentials")
	entries := strings.Split(raw, ",")
	if len(entries) > maxMCPOrigins {
		return nil, bad
	}
	origins := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		origin := strings.TrimSpace(entry)
		authority, found := strings.CutPrefix(origin, "https://")
		if !found || !visibleASCII(origin) {
			return nil, bad
		}
		_, port, ok := splitAuthority(authority)
		if _, duplicate := seen[origin]; !ok || port == "443" || duplicate {
			return nil, bad
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins, nil
}

// splitAuthority validates a canonical lowercase host with an optional port.
func splitAuthority(authority string) (host, port string, ok bool) {
	rest := authority
	switch {
	case strings.HasPrefix(authority, "["):
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", "", false
		}
		host, rest = authority[1:end], authority[end+1:]
		address, err := netip.ParseAddr(host)
		if err != nil || !address.Is6() || address.Zone() != "" || address.String() != host {
			return "", "", false
		}
		if rest != "" && !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
	default:
		host, rest, _ = strings.Cut(authority, ":")
		if rest != "" {
			rest = ":" + rest
		} else if strings.HasSuffix(authority, ":") {
			return "", "", false
		}
		if address, err := netip.ParseAddr(host); err == nil {
			if !address.Is4() || address.String() != host {
				return "", "", false
			}
		} else if len(host) > 253 || !dnsName.MatchString(host) {
			return "", "", false
		}
	}
	if rest == "" {
		return host, "", true
	}
	port = rest[1:]
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", "", false
	}
	return host, port, true
}

func concreteBindAddr(value string) bool {
	address, err := netip.ParseAddrPort(value)
	if err != nil || address.Port() == 0 {
		return false
	}
	ip := address.Addr().WithZone("").Unmap()
	return !ip.IsUnspecified() && !ip.IsMulticast()
}

func loadMCPCertificate(certFile, keyFile string) (tls.Certificate, error) {
	if certFile == "" || keyFile == "" {
		return tls.Certificate{}, errors.New("MCP_TLS_CERT_FILE and MCP_TLS_KEY_FILE are required; plaintext listeners and proxy-terminated TLS are not supported")
	}
	certPEM, err := readMCPTLSFile("MCP_TLS_CERT_FILE", certFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM, err := readMCPTLSFile("MCP_TLS_KEY_FILE", keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, errors.New("MCP_TLS_CERT_FILE and MCP_TLS_KEY_FILE must hold a matching PEM certificate and private key")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return tls.Certificate{}, errors.New("MCP_TLS_CERT_FILE must hold a valid certificate")
	}
	if !time.Now().Before(leaf.NotAfter) {
		return tls.Certificate{}, errors.New("MCP_TLS_CERT_FILE certificate has expired")
	}
	return certificate, nil
}

func readMCPTLSFile(setting, path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // The operator names the TLS files.
	if err != nil || len(data) > maxMCPTLSFileBytes {
		return nil, fmt.Errorf("%s must name a readable file of at most 1 MiB", setting)
	}
	return data, nil
}

func visibleASCII(value string) bool {
	for i := range len(value) {
		if value[i] < '!' || value[i] > '~' {
			return false
		}
	}
	return true
}
