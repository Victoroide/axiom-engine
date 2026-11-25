package mcpfixture

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/auth"
)

func TestTokensVerifyAgainstTheIssuerOnlyForTheirAudience(t *testing.T) {
	dir := t.TempDir()
	f, err := New(t.Context(), Options{
		Dir: dir, IssuerHosts: []string{"localhost"}, MCPHost: "mcp.example.test", ListenHost: "127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	ca, err := os.ReadFile(filepath.Join(dir, "ca.pem")) //nolint:gosec // The path is inside the test's temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("the CA file holds no certificate")
	}
	const audience = "https://mcp.example.test/mcp"
	verifier, err := auth.New(auth.Config{
		Issuer: f.Issuer(), Audience: audience, JWKSURL: f.KeysURL(), Algorithms: []string{"ES256"},
		Claims:    auth.ClaimMapping{Tenant: "tenant_id"},
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.Token(audience, "tenant", "caller", "graph:read")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := verifier.Verify(t.Context(), token, "graph:read")
	if err != nil || principal.Tenant() != "tenant" || !principal.HasScope("graph:read") {
		t.Fatalf("principal=%v err=%v", principal, err)
	}
	other, err := f.Token("https://api.example.test/axiom", "tenant", "caller", "graph:read")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(t.Context(), other); err == nil {
		t.Fatal("a token for another audience verified")
	}
}
