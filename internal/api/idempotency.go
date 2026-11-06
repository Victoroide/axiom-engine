package api

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/idempotency"
)

// Idempotency replays successful responses within a credential scope.
func Idempotency(store idempotency.Store, scope func(fiber.Ctx) string, logger *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		values := c.Request().Header.PeekAll("Idempotency-Key")
		if len(values) == 0 {
			return c.Next()
		}
		key := string(values[0])
		if len(values) != 1 || len(key) < 1 || len(key) > 255 || strings.ContainsFunc(key, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
			return c.Status(400).JSON(domain.ErrorResponse{Error: "invalid_idempotency_key", Details: "Idempotency-Key must contain 1 to 255 visible ASCII characters"})
		}
		key = scope(c) + ":" + key
		fingerprint := fmt.Sprintf("%x", sha256.Sum256(c.Body()))
		reservation, state, err := store.Begin(c.Context(), key, fingerprint)
		if err != nil {
			return storageUnavailable(c, logger, "idempotency.begin")
		}
		switch state {
		case idempotency.Replay:
			c.Set("Content-Type", reservation.Response.ContentType)
			c.Set("Idempotent-Replayed", "true")
			return c.Status(reservation.Response.Status).Send(reservation.Response.Body)
		case idempotency.Mismatch:
			return c.Status(422).JSON(domain.ErrorResponse{Error: "idempotency_key_mismatch", Details: "Idempotency-Key was used with a different request body"})
		case idempotency.InProgress:
			c.Set("Retry-After", "1")
			return c.Status(409).JSON(domain.ErrorResponse{Error: "idempotency_key_in_progress", Details: "request with this Idempotency-Key is still in progress"})
		}
		settled := false
		defer func() {
			if !settled {
				if err := store.Abort(c.Context(), key, reservation.Token); err != nil {
					logger.ErrorContext(c.Context(), "storage.failed", "operation", "idempotency.abort")
				}
			}
		}()
		nextErr := c.Next()
		if nextErr != nil || c.Response().StatusCode() != 200 {
			abortErr := store.Abort(c.Context(), key, reservation.Token)
			settled = true
			if abortErr != nil {
				return storageUnavailable(c, logger, "idempotency.abort")
			}
			return nextErr
		}
		response := idempotency.Response{Status: 200, Body: c.Response().Body(), ContentType: strings.Clone(c.GetRespHeader("Content-Type"))}
		if err := store.Complete(c.Context(), key, reservation.Token, response); err != nil {
			if abortErr := store.Abort(c.Context(), key, reservation.Token); abortErr != nil {
				logger.ErrorContext(c.Context(), "storage.failed", "operation", "idempotency.abort")
			}
			settled = true
			return storageUnavailable(c, logger, "idempotency.complete")
		}
		settled = true
		return nil
	}
}

func storageUnavailable(c fiber.Ctx, logger *slog.Logger, operation string) error {
	logger.ErrorContext(c.Context(), "storage.failed", "operation", operation)
	c.Set("Retry-After", "1")
	return c.Status(503).JSON(domain.ErrorResponse{Error: "service_unavailable", Details: "request storage temporarily unavailable"})
}
