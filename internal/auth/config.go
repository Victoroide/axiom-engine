// Package auth verifies signed access tokens for a configured issuer and audience.
package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// ClaimMapping selects the issuer's tenant, scope and caller claims. With no
// ClientID mapping, sub identifies the caller. Client mappings require an
// explicit list of registered IDs and a matching sub if the issuer includes it.
type ClaimMapping struct {
	Tenant            string
	Scope             string // Defaults to scope.
	ClientID          string
	RegisteredClients []string
}

// Config defines the trust boundary for one verifier. The issuer and key
// endpoint must be HTTPS URLs configured by the caller.
type Config struct {
	Issuer     string
	Audience   string
	JWKSURL    string
	Algorithms []string // An explicit subset of RS256 and ES256.
	Claims     ClaimMapping

	ClockSkew       time.Duration // Defaults to zero, at most one minute.
	RefreshTimeout  time.Duration // Defaults to two seconds, at most ten seconds.
	RefreshInterval time.Duration // Defaults to thirty seconds.
	MaxKeyAge       time.Duration // Defaults to ten minutes, at most 24 hours.

	// Transport can supply an issuer-specific TLS trust store.
	Transport http.RoundTripper
}

// New configures a verifier. The first verification that needs a key starts
// the first fetch.
func New(config Config) (*Verifier, error) {
	if !httpsURL(config.Issuer, false) || !httpsURL(config.JWKSURL, true) || !identifier(config.Audience, maxAudienceBytes) {
		return nil, errors.New("auth: invalid issuer, audience or key endpoint")
	}
	algorithms, err := signingAlgorithms(config.Algorithms)
	if err != nil {
		return nil, err
	}
	claims, err := mapClaims(config.Claims)
	if err != nil {
		return nil, err
	}
	if config.RefreshTimeout == 0 {
		config.RefreshTimeout = 2 * time.Second
	}
	if config.RefreshInterval == 0 {
		config.RefreshInterval = 30 * time.Second
	}
	if config.MaxKeyAge == 0 {
		config.MaxKeyAge = 10 * time.Minute
	}
	if config.ClockSkew < 0 || config.ClockSkew > time.Minute ||
		config.RefreshTimeout < time.Millisecond || config.RefreshTimeout > 10*time.Second ||
		config.RefreshInterval < max(time.Second, config.RefreshTimeout) ||
		config.MaxKeyAge < config.RefreshInterval || config.MaxKeyAge > 24*time.Hour {
		return nil, errors.New("auth: invalid key refresh limits or clock skew")
	}
	return &Verifier{
		issuer:     config.Issuer,
		audience:   config.Audience,
		algorithms: algorithms,
		claims:     claims,
		skew:       config.ClockSkew,
		now:        time.Now,
		keys: keyCache{
			url:         config.JWKSURL,
			timeout:     config.RefreshTimeout,
			minInterval: config.RefreshInterval,
			maxAge:      config.MaxKeyAge,
			client: &http.Client{
				Transport: config.Transport,
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
					return http.ErrUseLastResponse
				},
			},
		},
	}, nil
}

func httpsURL(raw string, allowQuery bool) bool {
	if !identifier(raw, maxAudienceBytes) || strings.Contains(raw, "#") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil &&
		(allowQuery || (!parsed.ForceQuery && parsed.RawQuery == ""))
}

func signingAlgorithms(values []string) ([]jose.SignatureAlgorithm, error) {
	if len(values) == 0 || len(values) > 2 {
		return nil, errors.New("auth: configure one or two signing algorithms")
	}
	algorithms := make([]jose.SignatureAlgorithm, 0, len(values))
	for _, value := range values {
		algorithm := jose.SignatureAlgorithm(value)
		if algorithm != jose.RS256 && algorithm != jose.ES256 {
			return nil, errors.New("auth: unsupported signing algorithm")
		}
		if len(algorithms) > 0 && algorithm == algorithms[0] {
			return nil, errors.New("auth: duplicate signing algorithm")
		}
		algorithms = append(algorithms, algorithm)
	}
	return algorithms, nil
}

type claimMapping struct {
	tenant, scope, clientID string
	registeredClients       map[string]struct{}
}

func mapClaims(mapping ClaimMapping) (claimMapping, error) {
	if mapping.Scope == "" {
		mapping.Scope = "scope"
	}
	if !claimName(mapping.Tenant) || !claimName(mapping.Scope) || mapping.Tenant == mapping.Scope ||
		(mapping.ClientID != "" && (!claimName(mapping.ClientID) || mapping.ClientID == mapping.Tenant || mapping.ClientID == mapping.Scope)) {
		return claimMapping{}, errors.New("auth: invalid or overlapping claim mapping")
	}
	if (mapping.ClientID == "") != (len(mapping.RegisteredClients) == 0) || len(mapping.RegisteredClients) > 256 {
		return claimMapping{}, errors.New("auth: invalid registered client mapping")
	}
	clients := make(map[string]struct{}, len(mapping.RegisteredClients))
	for _, client := range mapping.RegisteredClients {
		if _, exists := clients[client]; !identifier(client, maxIdentityBytes) || exists {
			return claimMapping{}, errors.New("auth: invalid registered client")
		}
		clients[client] = struct{}{}
	}
	return claimMapping{mapping.Tenant, mapping.Scope, mapping.ClientID, clients}, nil
}

func claimName(name string) bool {
	switch name {
	case "iss", "aud", "exp", "nbf", "iat", "sub", "jti", "cnf":
		return false
	}
	return identifier(name, maxIdentityBytes)
}
