package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Redis retains reservations and responses shared by application replicas.
type Redis struct {
	client     redis.UniversalClient
	ttl        time.Duration
	pendingTTL time.Duration
}

// NewRedis constructs response storage on an existing Redis client.
// Successful responses use ttl; pendingTTL bounds abandoned reservations.
func NewRedis(client redis.UniversalClient, ttl, pendingTTL time.Duration) *Redis {
	return &Redis{client: client, ttl: ttl, pendingTTL: pendingTTL}
}

func redisKey(key string) string {
	return fmt.Sprintf("axiom:idempotency:%x", sha256.Sum256([]byte(key)))
}

var beginScript = redis.NewScript(`
local existing = redis.call('HGET', KEYS[1], 'fingerprint')
if existing then
 if existing ~= ARGV[1] then return {2, ''} end
 local response = redis.call('HGET', KEYS[1], 'response')
 if response then return {1, response} end
 return {3, ''}
end
redis.call('HSET', KEYS[1], 'fingerprint', ARGV[1], 'token', ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return {0, ''}
`)

// Begin atomically reserves a key or reads its current state.
func (s *Redis) Begin(ctx context.Context, key, fingerprint string) (Reservation, State, error) {
	token := uuid.NewString()
	values, err := beginScript.Run(ctx, s.client, []string{redisKey(key)}, fingerprint, token, max(1, s.pendingTTL.Milliseconds())).Slice()
	if err != nil {
		return Reservation{}, Acquired, err
	}
	if len(values) != 2 {
		return Reservation{}, Acquired, fmt.Errorf("invalid reservation result")
	}
	n, ok := values[0].(int64)
	if !ok || n < 0 || n > 3 {
		return Reservation{}, Acquired, fmt.Errorf("invalid reservation state")
	}
	reservation := Reservation{Token: token}
	if State(n) == Replay {
		encoded, ok := values[1].(string)
		if !ok {
			return Reservation{}, Acquired, fmt.Errorf("invalid stored response")
		}
		if err := json.Unmarshal([]byte(encoded), &reservation.Response); err != nil {
			return Reservation{}, Acquired, err
		}
	}
	return reservation, State(n), nil
}

var completeScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'token') == ARGV[1] then
 redis.call('HSET', KEYS[1], 'response', ARGV[2])
 redis.call('PEXPIRE', KEYS[1], ARGV[3])
end
return 1
`)

// Complete publishes a successful response only for its reservation owner.
func (s *Redis) Complete(ctx context.Context, key, token string, response Response) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return completeScript.Run(ctx, s.client, []string{redisKey(key)}, token, string(encoded), max(1, s.ttl.Milliseconds())).Err()
}

var abortScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'token') == ARGV[1] then redis.call('DEL', KEYS[1]) end
return 1
`)

// Abort releases a failed reservation without removing a newer one.
func (s *Redis) Abort(ctx context.Context, key, token string) error {
	return abortScript.Run(ctx, s.client, []string{redisKey(key)}, token).Err()
}
