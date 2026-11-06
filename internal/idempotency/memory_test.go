package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
)

func TestMemoryExpiryAndOldestEviction(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := NewMemory(time.Hour, 2)
	store.now = func() time.Time { return now }
	first, _, _ := store.Begin(ctx, "first", "body")
	if err := store.Complete(ctx, "first", first.Token, Response{Status: 200, Body: []byte("ok")}); err != nil {
		t.Fatal(err)
	}
	second, _, _ := store.Begin(ctx, "second", "body")
	if err := store.Complete(ctx, "second", second.Token, Response{Status: 200}); err != nil {
		t.Fatal(err)
	}
	third, _, _ := store.Begin(ctx, "third", "body")
	if err := store.Complete(ctx, "third", third.Token, Response{Status: 200}); err != nil {
		t.Fatal(err)
	}
	if len(store.entries) != 2 {
		t.Fatal("entry bound exceeded")
	}
	if _, ok := store.entries["first"]; ok {
		t.Fatal("oldest entry retained")
	}
	now = now.Add(time.Hour)
	_, state, _ := store.Begin(ctx, "second", "body")
	if state != Acquired || len(store.entries) != 1 {
		t.Fatalf("state=%d entries=%d", state, len(store.entries))
	}
}

func TestMemoryOwnershipAndResponseCopies(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(time.Hour, 1)
	old, _, _ := store.Begin(ctx, "key", "body")
	if err := store.Abort(ctx, "key", old.Token); err != nil {
		t.Fatal(err)
	}
	current, _, _ := store.Begin(ctx, "key", "body")
	if err := store.Complete(ctx, "key", old.Token, Response{Status: 200}); err != nil {
		t.Fatal(err)
	}
	_, state, _ := store.Begin(ctx, "key", "body")
	if state != InProgress {
		t.Fatal("stale owner completed a newer reservation")
	}
	body := []byte("ok")
	if err := store.Complete(ctx, "key", current.Token, Response{Status: 200, Body: body}); err != nil {
		t.Fatal(err)
	}
	body[0] = 'x'
	replay, state, _ := store.Begin(ctx, "key", "body")
	if state != Replay || string(replay.Response.Body) != "ok" {
		t.Fatal("response was not copied")
	}
	replay.Response.Body[0] = 'x'
	replay, _, _ = store.Begin(ctx, "key", "body")
	if string(replay.Response.Body) != "ok" {
		t.Fatal("replay mutated storage")
	}
}

func TestMemoryInFlightDoesNotExpireWithResponseTTL(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := NewMemory(time.Second, 10)
	store.now = func() time.Time { return now }
	owner, _, _ := store.Begin(ctx, "key", "body")
	now = now.Add(2 * time.Second)
	_, state, err := store.Begin(ctx, "key", "body")
	if err != nil || state != InProgress {
		t.Fatalf("running reservation expired: state=%d err=%v", state, err)
	}
	if err := store.Complete(ctx, "key", owner.Token, Response{Status: 200}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	_, state, err = store.Begin(ctx, "key", "body")
	if err != nil || state != Acquired {
		t.Fatalf("successful response did not expire: state=%d err=%v", state, err)
	}
}

func TestMemoryOwnsResponseContentType(t *testing.T) {
	app := fiber.New()
	c := app.AcquireCtx(&fasthttp.RequestCtx{})
	defer app.ReleaseCtx(c)
	c.Set("Content-Type", "application/json; charset=utf-8")
	store := NewMemory(time.Hour, 1)
	owner, _, err := store.Begin(c.Context(), "key", "body")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(c.Context(), "key", owner.Token, Response{Status: 200, ContentType: c.GetRespHeader("Content-Type")}); err != nil {
		t.Fatal(err)
	}
	c.Set("Content-Type", "text/html; charset=utf-8")
	replay, state, err := store.Begin(c.Context(), "key", "body")
	if err != nil || state != Replay || replay.Response.ContentType != "application/json; charset=utf-8" {
		t.Fatalf("state=%d content_type=%q err=%v", state, replay.Response.ContentType, err)
	}
}
