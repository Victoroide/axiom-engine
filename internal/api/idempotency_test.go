package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/idempotency"
)

type cancelOnComplete struct {
	idempotency.Store
	cancel context.CancelFunc
}

func (s cancelOnComplete) Complete(ctx context.Context, key, token string, response idempotency.Response) error {
	s.cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.Complete(ctx, key, token, response)
}

func TestIdempotencyCancellationAtCommit(t *testing.T) {
	for _, tc := range []struct {
		name               string
		cancelBeforeCommit bool
		wantStatus         int
	}{
		{"canceled before commit", true, http.StatusServiceUnavailable},
		{"canceled during commit", false, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			base := idempotency.NewMemory(time.Minute, 2)
			store := cancelOnComplete{Store: base, cancel: cancel}
			app := fiber.New()
			var parent context.Context
			app.Use(func(c fiber.Ctx) error {
				c.SetContext(parent)
				return c.Next()
			})
			var generated int
			app.Post("/", Idempotency(store, func(fiber.Ctx) string { return "test" }, slog.New(slog.DiscardHandler)), func(c fiber.Ctx) error {
				generated++
				if generated == 1 && tc.cancelBeforeCommit {
					cancel()
				}
				return c.JSON(domain.PlanResponse{})
			})
			request := func(requestContext context.Context) (int, string) {
				t.Helper()
				parent = requestContext
				req := httptest.NewRequestWithContext(requestContext, http.MethodPost, "/", strings.NewReader(`{}`))
				req.Header.Set("Idempotency-Key", "key")
				resp, err := app.Test(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					t.Fatal(err)
				}
				return resp.StatusCode, resp.Header.Get("Idempotent-Replayed")
			}
			status, replay := request(ctx)
			if status != tc.wantStatus || replay != "" {
				t.Errorf("first status=%d replay=%q", status, replay)
			}
			status, replay = request(t.Context())
			if tc.cancelBeforeCommit {
				if status != http.StatusOK || replay != "" || generated != 2 {
					t.Fatalf("retry status=%d replay=%q generated=%d", status, replay, generated)
				}
			} else if status != http.StatusOK || replay != "true" || generated != 1 {
				t.Fatalf("replay status=%d replay=%q generated=%d", status, replay, generated)
			}
		})
	}
}
