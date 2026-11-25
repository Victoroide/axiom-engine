// Package mcpfixture provides a local HTTPS issuer, signing key, request-budget
// storage and synthetic access tokens for checks of the remote MCP endpoint.
package mcpfixture

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/alicebob/miniredis/v2"
	jose "github.com/go-jose/go-jose/v4"
)

// Options configures a fixture.
type Options struct {
	// Dir receives ca.pem, tls.crt and tls.key, readable by any user.
	Dir string
	// IssuerHosts are the names and addresses the certificate must cover, in
	// addition to the MCP host.
	IssuerHosts []string
	// MCPHost is the public host name of the MCP endpoint.
	MCPHost string
	// ListenHost is the interface the issuer and Redis listen on.
	ListenHost string
}

// Fixture is a running issuer and Redis.
type Fixture struct {
	IssuerPort, RedisPort string
	keys                  []byte
	signer                jose.Signer
	issuer                string
	server                *http.Server
	redis                 *miniredis.Miniredis
	opts                  Options
}

// New starts the fixture. Call Close to stop it.
func New(ctx context.Context, opts Options) (*Fixture, error) {
	certPEM, keyPEM, err := certificate(opts.MCPHost, opts.IssuerHosts)
	if err != nil {
		return nil, err
	}
	for name, data := range map[string][]byte{"ca.pem": certPEM, "tls.crt": certPEM, "tls.key": keyPEM} {
		if err := os.WriteFile(filepath.Join(opts.Dir, name), data, 0o644); err != nil { //nolint:gosec // Synthetic material for local checks, read by a container user.
			return nil, err
		}
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	signingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	jwk := jose.JSONWebKey{Key: signingKey, KeyID: "fixture-key", Algorithm: string(jose.ES256), Use: "sig"}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jwk}, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		return nil, err
	}
	keys, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
	if err != nil {
		return nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(opts.ListenHost, "0"))
	if err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	redis := miniredis.NewMiniRedis()
	if err := redis.StartAddr(net.JoinHostPort(opts.ListenHost, "0")); err != nil {
		_ = ln.Close()
		return nil, err
	}
	_, redisPort, _ := net.SplitHostPort(redis.Addr())
	f := &Fixture{IssuerPort: port, RedisPort: redisPort, keys: keys, signer: signer, redis: redis, opts: opts}
	f.issuer = "https://" + net.JoinHostPort(opts.IssuerHosts[0], port) + "/issuer"
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(f.keys) })
	f.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}}
	go func() { _ = f.server.ServeTLS(ln, "", "") }()
	return f, nil
}

// Issuer returns the issuer identifier, reachable at IssuerHosts[0].
func (f *Fixture) Issuer() string { return f.issuer }

// KeysURL returns the key set address.
func (f *Fixture) KeysURL() string {
	return "https://" + net.JoinHostPort(f.opts.IssuerHosts[0], f.IssuerPort) + "/keys"
}

// Token returns an access token for audience, tenant, caller and scopes.
func (f *Fixture) Token(audience, tenant, caller, scope string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"iss": f.issuer, "aud": audience, "exp": time.Now().Add(15 * time.Minute).Unix(),
		"tenant_id": tenant, "sub": caller, "scope": scope,
	})
	if err != nil {
		return "", err
	}
	signed, err := f.signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return signed.CompactSerialize()
}

// Close stops the issuer and Redis.
func (f *Fixture) Close() error {
	f.redis.Close()
	return f.server.Close()
}

func certificate(hosts string, issuerHosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: hosts},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	for _, host := range append([]string{hosts}, issuerHosts...) {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}
