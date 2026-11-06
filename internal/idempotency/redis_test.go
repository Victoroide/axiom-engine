package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisReservationsAreAtomicAndOwned(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedis(client, time.Second, 3*time.Second)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan Reservation, 20)
	for range 20 {
		go func() {
			<-start
			reservation, state, err := store.Begin(ctx, "key", "body")
			if err != nil {
				t.Error(err)
			}
			if state != Acquired {
				reservation.Token = ""
			}
			results <- reservation
		}()
	}
	close(start)
	count := 0
	var owner Reservation
	for range 20 {
		r := <-results
		if r.Token != "" {
			count++
			owner = r
		}
	}
	if count != 1 {
		t.Fatalf("owners=%d", count)
	}
	_, state, err := store.Begin(ctx, "key", "different")
	if err != nil || state != Mismatch {
		t.Fatalf("state=%d err=%v", state, err)
	}
	if err := store.Complete(ctx, "key", owner.Token, Response{Status: 200, Body: []byte("ok"), ContentType: "application/json"}); err != nil {
		t.Fatal(err)
	}
	replay, state, err := store.Begin(ctx, "key", "body")
	if err != nil || state != Replay || string(replay.Response.Body) != "ok" || replay.Response.ContentType != "application/json" {
		t.Fatalf("replay=%+v state=%d err=%v", replay, state, err)
	}
	server.FastForward(time.Second)
	newer, state, err := store.Begin(ctx, "key", "body")
	if err != nil || state != Acquired {
		t.Fatalf("state=%d err=%v", state, err)
	}
	if err := store.Abort(ctx, "key", owner.Token); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, "key", owner.Token, Response{Status: 200}); err != nil {
		t.Fatal(err)
	}
	_, state, err = store.Begin(ctx, "key", "body")
	if err != nil || state != InProgress {
		t.Fatalf("stale owner changed reservation: state=%d err=%v", state, err)
	}
	if err := store.Abort(ctx, "key", newer.Token); err != nil {
		t.Fatal(err)
	}
	_, state, err = store.Begin(ctx, "key", "body")
	if err != nil || state != Acquired {
		t.Fatalf("aborted reservation retained: state=%d err=%v", state, err)
	}
}
func TestRedisInFlightOutlivesResponseTTL(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedis(client, time.Second, 3*time.Second)
	ctx := context.Background()
	_, _, err := store.Begin(ctx, "slow", "body")
	if err != nil {
		t.Fatal(err)
	}
	server.FastForward(2 * time.Second)
	_, state, err := store.Begin(ctx, "slow", "body")
	if err != nil || state != InProgress {
		t.Fatalf("running reservation expired: state=%d err=%v", state, err)
	}
	server.FastForward(time.Second)
	_, state, err = store.Begin(ctx, "slow", "body")
	if err != nil || state != Acquired {
		t.Fatalf("abandoned reservation did not expire: state=%d err=%v", state, err)
	}

}
