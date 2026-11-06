package server

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	redisstorage "github.com/gofiber/storage/redis/v3"
	"github.com/redis/go-redis/v9"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
)

func openRedis(cfg config.Redis) (*redisstorage.Storage, error) {
	if cfg.URL == "" {
		return nil, nil
	}
	options, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis configuration")
	}
	options.DialTimeout = time.Second
	options.ReadTimeout = 500 * time.Millisecond
	options.WriteTimeout = 500 * time.Millisecond
	options.ContextTimeoutEnabled = true
	options.MaxRetries = -1
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis startup ping failed")
	}
	return redisstorage.NewFromConnectionWithContext(ctx, client), nil
}

type redisWindow struct {
	storage *redisstorage.Storage
	logger  *slog.Logger
}

// Keep the entire sliding-window decision atomic across replicas.
var rateScript = redis.NewScript(`
local now = tonumber(redis.call('TIME')[1])
local window = tonumber(ARGV[1])
local maximum = tonumber(ARGV[2])
local values = redis.call('HMGET', KEYS[1], 'current', 'previous', 'expires')
local current = tonumber(values[1]) or 0
local previous = tonumber(values[2]) or 0
local expires = tonumber(values[3]) or (now + window)
if now >= expires then
 if now - expires >= window then
  previous = 0
  expires = now + window
 else
  previous = current
  expires = expires + window
 end
 current = 0
end
current = current + 1
local reset = expires - now
local remaining = maximum - current - math.ceil(previous * reset / window)
redis.call('HSET', KEYS[1], 'current', current, 'previous', previous, 'expires', expires)
redis.call('EXPIRE', KEYS[1], reset + window)
return {remaining, reset}
`)

func (r redisWindow) New(cfg *limiter.Config) fiber.Handler {
	return func(c fiber.Ctx) error {
		window := max(int64(1), int64(cfg.Expiration/time.Second))
		values, err := rateScript.Run(c.Context(), r.storage.Conn(), []string{"axiom:rate:" + cfg.KeyGenerator(c)}, window, cfg.Max).Int64Slice()
		if err != nil || len(values) != 2 {
			r.logger.ErrorContext(c.Context(), "storage.failed", "operation", "rate_limit.consume")
			c.Set("Retry-After", "1")
			return c.Status(fiber.StatusServiceUnavailable).JSON(domain.ErrorResponse{Error: "service_unavailable", Details: "rate limit storage temporarily unavailable"})
		}
		if values[0] < 0 {
			c.Set("Retry-After", strconv.FormatInt(values[1], 10))
			return cfg.LimitReached(c)
		}
		c.Set("X-RateLimit-Limit", strconv.Itoa(cfg.Max))
		c.Set("X-RateLimit-Remaining", strconv.FormatInt(values[0], 10))
		c.Set("X-RateLimit-Reset", strconv.FormatInt(values[1], 10))
		return c.Next()
	}
}
