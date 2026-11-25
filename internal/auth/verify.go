package auth

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const (
	maxTokenBytes    = 8 * 1024
	maxAudienceBytes = 1024
	maxIdentityBytes = 128
	maxAudiences     = 16
	maxScopes        = 32
	maxScopeBytes    = 128
	maxUnixTime      = 253402300799 // End of year 9999.
)

var (
	// ErrInvalidToken covers malformed, untrusted and expired access tokens.
	ErrInvalidToken = errors.New("auth: invalid access token")
	// ErrInsufficientScope means a verified token lacks a required scope.
	ErrInsufficientScope = errors.New("auth: insufficient scope")
)

// Principal contains only the identities selected by the issuer mapping.
// Keep these values within the authorization boundary.
type Principal struct {
	issuer, tenant, caller string
}

// Issuer returns the configured issuer that authenticated the caller.
func (p Principal) Issuer() string { return p.issuer }

// Tenant returns the authenticated tenant identifier from the mapped claim.
func (p Principal) Tenant() string { return p.tenant }

// Caller returns the verified subject or registered client identifier.
func (p Principal) Caller() string { return p.caller }

// String redacts a principal formatted directly with %v or %+v.
func (Principal) String() string { return "[verified principal]" }

// GoString redacts a principal formatted directly with %#v.
func (p Principal) GoString() string { return p.String() }

// MarshalJSON omits principal fields from serialized diagnostics.
func (Principal) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Verifier authenticates signed access tokens for one issuer and resource
// audience. Reuse it across requests to share the key cache.
type Verifier struct {
	issuer, audience string
	algorithms       []jose.SignatureAlgorithm
	claims           claimMapping
	skew             time.Duration
	now              func() time.Time
	keys             keyCache
}

// Verify checks a compact signed access token and every required scope. Any
// error returns a zero principal. Verify can be called concurrently.
func (v *Verifier) Verify(ctx context.Context, compact string, requiredScopes ...string) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	if len(compact) > maxTokenBytes || strings.ContainsAny(compact, " \t\r\n") || !accessTokenHeader(compact) {
		return Principal{}, ErrInvalidToken
	}
	signed, err := jose.ParseSignedCompact(compact, v.algorithms)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	header := signed.Signatures[0].Protected
	if !identifier(header.KeyID, maxIdentityBytes) {
		return Principal{}, ErrInvalidToken
	}
	key, err := v.keys.lookup(ctx, v.now, header.KeyID, header.Algorithm)
	if err != nil {
		return Principal{}, err
	}
	payload, err := signed.Verify(key)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	return v.verifyClaims(payload, requiredScopes)
}

func accessTokenHeader(compact string) bool {
	encoded, _, ok := strings.Cut(compact, ".")
	if !ok {
		return false
	}
	headerJSON, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return false
	}
	var header map[string]jsontext.Value
	if json.Unmarshal(headerJSON, &header) != nil {
		return false
	}
	for _, name := range []string{"jku", "jwk", "x5u", "x5c", "crit", "b64"} {
		if _, present := header[name]; present {
			return false
		}
	}
	typ := stringValue(header["typ"])
	return strings.EqualFold(typ, "at+jwt") || strings.EqualFold(typ, "application/at+jwt")
}

func (v *Verifier) verifyClaims(payload []byte, requiredScopes []string) (Principal, error) {
	var claims map[string]jsontext.Value
	if json.Unmarshal(payload, &claims) != nil || stringValue(claims["iss"]) != v.issuer ||
		claims["cnf"] != nil || !validAudience(claims["aud"], v.audience) || !validTime(claims, v.now(), v.skew) {
		return Principal{}, ErrInvalidToken
	}
	tenant := stringValue(claims[v.claims.tenant])
	caller := v.claims.caller(claims)
	if !ValidTenant(tenant) || !identifier(caller, maxIdentityBytes) {
		return Principal{}, ErrInvalidToken
	}
	scopes := tokenScopes(claims[v.claims.scope])
	if scopes == nil {
		return Principal{}, ErrInvalidToken
	}
	for _, scope := range requiredScopes {
		if _, allowed := scopes[scope]; !allowed {
			return Principal{}, ErrInsufficientScope
		}
	}
	return Principal{issuer: v.issuer, tenant: tenant, caller: caller}, nil
}

func (mapping claimMapping) caller(claims map[string]jsontext.Value) string {
	subject := stringValue(claims["sub"])
	if mapping.clientID == "" {
		return subject
	}
	client := stringValue(claims[mapping.clientID])
	_, registered := mapping.registeredClients[client]
	if !registered || (claims["sub"] != nil && subject != client) {
		return ""
	}
	return client
}

func validAudience(raw jsontext.Value, audience string) bool {
	if raw.Kind() == '"' {
		return stringValue(raw) == audience
	}
	var audiences []string
	if json.Unmarshal(raw, &audiences) != nil || len(audiences) > maxAudiences {
		return false
	}
	seen := make(map[string]struct{}, len(audiences))
	for _, candidate := range audiences {
		if _, duplicate := seen[candidate]; duplicate || !identifier(candidate, maxAudienceBytes) {
			return false
		}
		seen[candidate] = struct{}{}
	}
	_, matched := seen[audience]
	return matched
}

func validTime(claims map[string]jsontext.Value, now time.Time, skew time.Duration) bool {
	expiry, ok := numericDate(claims["exp"])
	if !ok || !now.Before(expiry.Add(skew)) {
		return false
	}
	for _, name := range []string{"nbf", "iat"} {
		if raw := claims[name]; raw != nil {
			start, ok := numericDate(raw)
			if !ok || start.After(now.Add(skew)) || !start.Before(expiry) {
				return false
			}
		}
	}
	return true
}

func numericDate(raw jsontext.Value) (time.Time, bool) {
	var seconds int64
	if json.Unmarshal(raw, &seconds) != nil || seconds <= 0 || seconds > maxUnixTime {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

func tokenScopes(raw jsontext.Value) map[string]struct{} {
	list := stringValue(raw)
	if len(list) > maxScopes*(maxScopeBytes+1)-1 {
		return nil
	}
	scopes := make(map[string]struct{})
	for scope := range strings.SplitSeq(list, " ") {
		if _, duplicate := scopes[scope]; duplicate || len(scopes) == maxScopes ||
			!identifier(scope, maxScopeBytes) || strings.ContainsAny(scope, `"\`) {
			return nil
		}
		scopes[scope] = struct{}{}
	}
	return scopes
}

func stringValue(raw jsontext.Value) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// ValidTenant reports whether a value can identify a tenant in a verified claim.
func ValidTenant(value string) bool { return identifier(value, maxIdentityBytes) }

func identifier(value string, maxBytes int) bool {
	if len(value) == 0 || len(value) > maxBytes {
		return false
	}
	for i := range len(value) {
		if value[i] < '!' || value[i] > '~' {
			return false
		}
	}
	return true
}
