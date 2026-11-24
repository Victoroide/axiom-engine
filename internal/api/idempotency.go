package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/idempotency"
	"github.com/Victoroide/axiom-engine/internal/service"
)

const reservationTimeout = time.Second

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
		ctx := c.Context()
		if ctx.Err() != nil {
			return canceledReservation(c, ctx)
		}
		// Wait for the reservation result even if the peer leaves during Begin.
		reserveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reservationTimeout)
		reservation, state, err := store.Begin(reserveCtx, key, fingerprint)
		cancel()
		abort := func() error {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reservationTimeout)
			defer cancel()
			return store.Abort(cleanupCtx, key, reservation.Token)
		}
		if err != nil {
			if reservation.Token != "" {
				// Redis may have applied Begin before the transport failed.
				if err := abort(); err != nil {
					logger.ErrorContext(ctx, "storage.failed", "operation", "idempotency.abort")
				}
			}
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
				if err := abort(); err != nil {
					logger.ErrorContext(ctx, "storage.failed", "operation", "idempotency.abort")
				}
			}
		}()
		nextErr := c.Next()
		if nextErr != nil || c.Response().StatusCode() != 200 || ctx.Err() != nil {
			if nextErr == nil && c.Response().StatusCode() == 200 {
				nextErr = canceledReservation(c, ctx)
			}
			abortErr := abort()
			settled = true
			if abortErr != nil {
				if ctx.Err() != nil {
					logger.ErrorContext(ctx, "storage.failed", "operation", "idempotency.abort")
					return nextErr
				}
				return storageUnavailable(c, logger, "idempotency.abort")
			}
			return nextErr
		}
		// The cancellation check above decides whether to publish this success.
		// A later disconnect does not interrupt publication.
		response := idempotency.Response{Status: 200, Body: c.Response().Body(), ContentType: strings.Clone(c.GetRespHeader("Content-Type"))}
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reservationTimeout)
		err = store.Complete(commitCtx, key, reservation.Token, response)
		cancel()
		if err != nil {
			return storageUnavailable(c, logger, "idempotency.complete")
		}
		settled = true
		return nil
	}
}

func canceledReservation(c fiber.Ctx, ctx context.Context) error {
	failure := service.ClassifyError(ctx.Err(), shuttingDown(ctx))
	if failure.RetryAfter > 0 {
		c.Set("Retry-After", strconv.FormatInt(int64(failure.RetryAfter/time.Second), 10))
	}
	return c.Status(failure.Status).JSON(failure.Response)
}

func storageUnavailable(c fiber.Ctx, logger *slog.Logger, operation string) error {
	logger.ErrorContext(c.Context(), "storage.failed", "operation", operation)
	c.Set("Retry-After", "1")
	return c.Status(503).JSON(domain.ErrorResponse{Error: "service_unavailable", Details: "request storage temporarily unavailable"})
}
