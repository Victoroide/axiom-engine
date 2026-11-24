package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
const replayPlanFormat = "plan-v1"

// Idempotency replays successful plan responses within an authorization namespace.
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
		requestBody := c.Body()
		fingerprint := fmt.Sprintf("%x", sha256.Sum256(requestBody))
		ctx := c.Context()
		if ctx.Err() != nil {
			return WriteRequestCancellation(c, ctx)
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
			body, err := restoreReplayBody(reservation.Response, requestBody)
			if err != nil {
				return storageUnavailable(c, logger, "idempotency.begin")
			}
			c.Set("Content-Type", reservation.Response.ContentType)
			c.Set("Idempotent-Replayed", "true")
			return c.Status(reservation.Response.Status).Send(body)
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
				nextErr = WriteRequestCancellation(c, ctx)
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
		body, err := storeReplayBody(c.Response().Body())
		if err != nil {
			return storageUnavailable(c, logger, "idempotency.complete")
		}
		response := idempotency.Response{Status: 200, Body: body, ContentType: strings.Clone(c.GetRespHeader("Content-Type")), Format: replayPlanFormat}
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

// replayPlan contains just the generated fields. Raw messages preserve their
// JSON encoding across replay, including fields added to items or metadata.
type replayPlan struct {
	Items json.RawMessage `json:"items"`
	Meta  json.RawMessage `json:"_meta"`
}

type replayPlanIDs struct {
	StudentID string `json:"student_id"`
	CourseID  string `json:"course_id"`
}

func storeReplayBody(body []byte) ([]byte, error) {
	var plan replayPlan
	if err := plan.decode(body); err != nil {
		return nil, err
	}
	return json.Marshal(plan)
}

func restoreReplayBody(stored idempotency.Response, requestBody []byte) ([]byte, error) {
	if stored.Format == "" {
		// Earlier records contain the original HTTP body in its entirety.
		return stored.Body, nil
	}
	if stored.Format != replayPlanFormat {
		return nil, errors.New("unsupported replay format")
	}
	// Begin matched the exact request bytes that the plan handler accepted.
	// Only these two echoed fields are needed from the retry.
	var ids replayPlanIDs
	if err := json.Unmarshal(requestBody, &ids); err != nil {
		return nil, err
	}
	var plan replayPlan
	if err := plan.decode(stored.Body); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		replayPlanIDs
		replayPlan
	}{ids, plan})
}

func (plan *replayPlan) decode(body []byte) error {
	if err := json.Unmarshal(body, plan); err != nil {
		return err
	}
	if plan.Items == nil || plan.Meta == nil {
		return errors.New("incomplete replay plan")
	}
	return nil
}

// WriteRequestCancellation writes the client-safe response for an ended request context.
func WriteRequestCancellation(c fiber.Ctx, ctx context.Context) error {
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
