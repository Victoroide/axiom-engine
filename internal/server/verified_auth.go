package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"

	"github.com/Victoroide/axiom-engine/internal/api"
	"github.com/Victoroide/axiom-engine/internal/auth"
	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
)

type verifiedReplayKey struct{}
type credentialTrailerKey struct{}

// Fiber discards trailers before routing. Capture only credential presence
// from the original request, including a field with an empty value.
func nativeCredentialTrailer(headers *fasthttp.RequestHeader) bool {
	if len(headers.PeekTrailerKeys()) != 0 {
		for line := range bytes.SplitSeq(headers.TrailerHeader(), []byte("\r\n")) {
			name, _, _ := bytes.Cut(line, []byte(":"))
			if bytes.EqualFold(name, []byte("X-Internal-Token")) || bytes.EqualFold(name, []byte("Authorization")) {
				return true
			}
		}
	}
	return false
}

type verifiedAccess struct {
	verifier  *auth.Verifier
	limit     fiber.Handler
	admission *RedisAdmission
	routes    []fiber.Route
}

func (a *verifiedAccess) authorize(c fiber.Ctx) error {
	// The parser seeds its error status before replaying Use middleware.
	parserError := c.Response().StatusCode() >= 400
	endpoint := c.Endpoint()
	if parserError {
		endpoint = a.rejectedOperation(c)
	}
	if endpoint == nil || endpoint.Name == "" {
		return c.Next()
	}
	scope, _, _ := strings.Cut(endpoint.Name, "/")
	ctx := c.Context()
	principal, err := a.verifier.Verify(ctx, verifiedBearer(c), scope)
	if parserError {
		// Charge authorized requests with intact metadata while the parser
		// retains ownership of the error response.
		if err != nil || ctx.Err() != nil {
			return c.Next()
		}
		return a.admit(c, principal, true)
	}
	switch {
	case ctx.Err() != nil:
		return api.WriteRequestCancellation(c, ctx)
	case errors.Is(err, auth.ErrInsufficientScope):
		c.Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+scope+`"`)
		return c.Status(fiber.StatusForbidden).JSON(domain.ErrorResponse{Error: "insufficient_scope", Details: "access token lacks the required scope"})
	case err != nil:
		c.Set("WWW-Authenticate", "Bearer")
		return c.Status(fiber.StatusUnauthorized).JSON(domain.ErrorResponse{Error: "unauthorized", Details: "missing or invalid access token"})
	}
	if len(c.Request().Header.PeekAll("Idempotency-Key")) != 0 {
		c.Locals(verifiedReplayKey{}, verifiedNamespace(principal))
	}
	return a.admit(c, principal, false)
}

func (a *verifiedAccess) admit(c fiber.Ctx, principal auth.Principal, parserError bool) error {
	if a.admission == nil {
		return a.limit(c)
	}
	decision := a.admission.Admit(c.Context(), c.IP(), principal)
	if parserError {
		decision.RetryAfter = 0
		decision.writeHeaders(c)
		return c.Next()
	}
	decision.writeHeaders(c)
	switch decision.Status {
	case fiber.StatusTooManyRequests:
		return c.Status(fiber.StatusTooManyRequests).JSON(domain.ErrorResponse{Error: "rate_limit_exceeded", Details: "too many requests"})
	case fiber.StatusServiceUnavailable:
		return unavailableAdmission(c)
	default:
		return c.Next()
	}
}

func (a *verifiedAccess) rejectedOperation(c fiber.Ctx) *fiber.Route {
	for i := range a.routes {
		route := &a.routes[i]
		methodMatches := route.Method == c.Method() || (c.Method() == fiber.MethodHead && route.Method == fiber.MethodGet)
		if route.Name != "" && methodMatches && fiber.RoutePatternMatch(c.Path(), route.Path, c.App().Config()) {
			return route
		}
	}
	return nil
}

func verifiedBearer(c fiber.Ctx) string {
	headers := &c.Request().Header
	if c.RequestCtx().UserValue(credentialTrailerKey{}) != nil || len(headers.PeekAll("X-Internal-Token")) != 0 {
		return ""
	}
	values := headers.PeekAll("Authorization")
	if len(values) != 1 {
		return ""
	}
	scheme, token, found := strings.Cut(string(values[0]), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.Contains(token, ",") {
		return ""
	}
	return strings.TrimLeft(token, " ")
}

func verifiedNamespace(principal auth.Principal) string {
	return fmt.Sprintf("verified-v1:%x", identityDigest(principal.Issuer(), principal.Tenant(), principal.Caller()))
}

func identityDigest(values ...string) [sha256.Size]byte {
	size := 8 * len(values)
	for _, value := range values {
		size += len(value)
	}
	framed := make([]byte, 0, size)
	for _, value := range values {
		framed = binary.BigEndian.AppendUint64(framed, uint64(len(value)))
		framed = append(framed, value...)
	}
	return sha256.Sum256(framed)
}

func replayNamespace(cfg config.Config) func(fiber.Ctx) string {
	switch {
	case cfg.VerifiedAuth != nil:
		return func(c fiber.Ctx) string { return c.Locals(verifiedReplayKey{}).(string) }
	case cfg.AuthDisabled:
		return func(fiber.Ctx) string { return "anonymous" }
	default:
		return func(c fiber.Ctx) string {
			return fmt.Sprintf("%x", sha256.Sum256([]byte(presentedCredential(c))))
		}
	}
}
