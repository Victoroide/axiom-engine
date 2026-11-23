package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const (
	maxJWKSBytes = 64 * 1024
	maxKeys      = 64
)

type verificationKey struct {
	algorithm jose.SignatureAlgorithm
	public    crypto.PublicKey
}

type keyCache struct {
	url                          string
	timeout, minInterval, maxAge time.Duration
	client                       *http.Client

	mu                     sync.Mutex
	keys                   map[string]verificationKey
	expiresAt, nextRefresh time.Time
	inFlight               *keyRefresh
}

type keyRefresh struct {
	done    <-chan struct{}
	cancel  context.CancelFunc
	waiters int
}

func (c *keyCache) lookup(ctx context.Context, now func() time.Time, id, algorithm string) (crypto.PublicKey, error) {
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	start := now()
	if key := c.cachedKey(start, id, algorithm); key != nil {
		c.mu.Unlock()
		return key, nil
	}
	refresh := c.inFlight
	if refresh == nil {
		if start.Before(c.nextRefresh) {
			c.mu.Unlock()
			return nil, ErrInvalidToken
		}
		refresh = c.startRefresh(start, now) //nolint:contextcheck // The shared refresh must discard caller values and deadlines.
	}
	refresh.waiters++
	c.mu.Unlock()

	select {
	case <-ctx.Done():
	case <-refresh.done:
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	refresh.waiters--
	if err := ctx.Err(); err != nil {
		if refresh.waiters == 0 {
			refresh.cancel()
		}
		return nil, err
	}
	key := c.cachedKey(now(), id, algorithm)
	if key == nil {
		return nil, ErrInvalidToken
	}
	return key, nil
}

// The caller holds c.mu.
func (c *keyCache) cachedKey(now time.Time, id, algorithm string) crypto.PublicKey {
	key := c.keys[id]
	if now.Before(c.expiresAt) && string(key.algorithm) == algorithm {
		return key.public
	}
	return nil
}

// The caller holds c.mu.
func (c *keyCache) startRefresh(start time.Time, now func() time.Time) *keyRefresh {
	// The refresh is shared. Its context carries no particular caller's values
	// and is canceled when the last waiting caller leaves.
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	// Waiters observe the deadline even while the transport is cleaning up.
	refresh := &keyRefresh{done: ctx.Done(), cancel: cancel}
	c.inFlight = refresh
	c.nextRefresh = start.Add(c.minInterval)
	go func() {
		defer cancel()
		keys := c.fetch(ctx)
		c.mu.Lock()
		defer c.mu.Unlock()
		if keys != nil && ctx.Err() == nil {
			c.keys = keys
			c.expiresAt = now().Add(c.maxAge)
		}
		c.inFlight = nil
	}()
	return refresh
}

func (c *keyCache) fetch(ctx context.Context) map[string]verificationKey {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/jwk-set+json, application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil || len(data) > maxJWKSBytes {
		return nil
	}
	return parseKeySet(data)
}

func parseKeySet(data []byte) map[string]verificationKey {
	var set struct {
		Keys []jsontext.Value `json:"keys"`
	}
	if json.Unmarshal(data, &set) != nil || set.Keys == nil || len(set.Keys) > maxKeys {
		return nil
	}
	keys := make(map[string]verificationKey, len(set.Keys))
	for _, entry := range set.Keys {
		id, key := parseKey(entry)
		if key.public == nil {
			continue
		}
		if _, duplicate := keys[id]; duplicate {
			return nil
		}
		keys[id] = key
	}
	return keys
}

func parseKey(entry jsontext.Value) (string, verificationKey) {
	var fields map[string]jsontext.Value
	if json.Unmarshal(entry, &fields) != nil {
		return "", verificationKey{}
	}
	id := stringValue(fields["kid"])
	if !identifier(id, maxIdentityBytes) || (fields["use"] != nil && stringValue(fields["use"]) != "sig") {
		return "", verificationKey{}
	}
	if ops := fields["key_ops"]; ops != nil {
		var values []string
		if json.Unmarshal(ops, &values) != nil || len(values) != 1 || values[0] != "verify" {
			return "", verificationKey{}
		}
	}
	// Reject private parameters before decoding the public key.
	for _, field := range []string{"k", "d", "p", "q", "dp", "dq", "qi", "oth", "priv"} {
		if fields[field] != nil {
			return "", verificationKey{}
		}
	}
	keyType := stringValue(fields["kty"])
	if keyType != "RSA" && keyType != "EC" {
		return "", verificationKey{}
	}
	var jwk jose.JSONWebKey
	if jwk.UnmarshalJSON(entry) != nil {
		return "", verificationKey{}
	}
	key := verificationKey{algorithm: keyAlgorithm(jwk.Key), public: jwk.Key}
	if key.algorithm == "" || (fields["alg"] != nil && jwk.Algorithm != string(key.algorithm)) {
		return "", verificationKey{}
	}
	return id, key
}

func keyAlgorithm(public crypto.PublicKey) jose.SignatureAlgorithm {
	switch key := public.(type) {
	case *rsa.PublicKey:
		if bits := key.N.BitLen(); bits >= 2048 && bits <= 4096 {
			return jose.RS256
		}
	case *ecdsa.PublicKey:
		if key.Curve == elliptic.P256() {
			return jose.ES256
		}
	}
	return ""
}
