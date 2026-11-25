package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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

const ipRatePrefix = "axiom:rate:"

type rateBucket struct {
	key     string
	window  int64
	maximum int
}

func ipRateBucket(ipKey string, maximum int, window time.Duration) rateBucket {
	return rateBucket{key: ipRatePrefix + ipKey, window: max(1, int64(window/time.Second)), maximum: maximum}
}

// Apply IP and tenant decisions in order within one Redis operation.
var rateScript = redis.NewScript(`
local now = tonumber(redis.call('TIME')[1])
local function consume(key, window, maximum, admittedOnly)
 local values = redis.call('HMGET', key, 'current', 'previous', 'expires', 'window')
 local current = tonumber(values[1]) or 0
 local previous = tonumber(values[2]) or 0
 local expires = tonumber(values[3]) or (now + window)
 -- Reset tenant counts when their stored duration differs or is missing.
 if admittedOnly and tonumber(values[4]) ~= window then
  current, previous, expires = 0, 0, now + window
 end
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
 local reset = expires - now
 local next = current + 1
 local remaining = maximum - next - math.ceil(previous * reset / window)
 -- Tenant buckets count only admissions, so their counters stay bounded and
 -- repeated denials cannot extend their expiry.
 if remaining >= 0 or not admittedOnly then
  if admittedOnly then
   redis.call('HSET', key, 'current', next, 'previous', previous, 'expires', expires, 'window', window)
  else
   redis.call('HSET', key, 'current', next, 'previous', previous, 'expires', expires)
  end
  redis.call('EXPIRE', key, reset + window)
 end
 return remaining, reset
end

local remaining, reset = consume(KEYS[1], tonumber(ARGV[1]), tonumber(ARGV[2]), false)
if remaining < 0 or #KEYS == 1 then
 return {remaining, reset}
end
local ok, tenantRemaining, tenantReset = pcall(consume, KEYS[2], tonumber(ARGV[3]), tonumber(ARGV[4]), true)
if not ok then
 -- A zero reset signals a tenant storage failure after IP admission.
 return {remaining, reset, 0, 0}
end
return {remaining, reset, tenantRemaining, tenantReset}
`)

func (r redisWindow) New(cfg *limiter.Config) fiber.Handler {
	return func(c fiber.Ctx) error {
		decision := r.admit(c.Context(), ipRateBucket(cfg.KeyGenerator(c), cfg.Max, cfg.Expiration), rateBucket{})
		decision.writeHeaders(c)
		switch decision.Status {
		case http.StatusTooManyRequests:
			return cfg.LimitReached(c)
		case http.StatusServiceUnavailable:
			return unavailableAdmission(c)
		default:
			return c.Next()
		}
	}
}

func (r redisWindow) admit(ctx context.Context, ip, tenant rateBucket) AdmissionDecision {
	keys, args := []string{ip.key}, []any{ip.window, ip.maximum}
	if tenant.key != "" {
		keys = append(keys, tenant.key)
		args = append(args, tenant.window, tenant.maximum)
	}
	values, err := rateScript.Run(ctx, r.storage.Conn(), keys, args...).Int64Slice()
	if err != nil || (len(values) != 2 && len(values) != 4) {
		r.logger.ErrorContext(ctx, "storage.failed", "operation", "rate_limit.consume")
		return AdmissionDecision{Status: http.StatusServiceUnavailable, RetryAfter: 1}
	}
	if values[0] < 0 {
		return AdmissionDecision{Status: http.StatusTooManyRequests, RetryAfter: values[1]}
	}
	decision := AdmissionDecision{Status: http.StatusOK, IPLimit: ip.maximum, IPRemaining: values[0], IPReset: values[1]}
	if tenant.key != "" {
		switch {
		case len(values) != 4 || values[3] <= 0:
			r.logger.ErrorContext(ctx, "storage.failed", "operation", "tenant_quota.consume")
			decision.Status, decision.RetryAfter = http.StatusServiceUnavailable, 1
		case values[2] < 0:
			decision.Status, decision.RetryAfter = http.StatusTooManyRequests, values[3]
		}
	}
	return decision
}

func unavailableAdmission(c fiber.Ctx) error {
	return c.Status(fiber.StatusServiceUnavailable).JSON(domain.ErrorResponse{Error: "service_unavailable", Details: "rate limit storage temporarily unavailable"})
}

func (d AdmissionDecision) writeHeaders(c fiber.Ctx) {
	d.setHeaders(c.Set)
}

func (d AdmissionDecision) setHeaders(set func(key, value string)) {
	if d.IPLimit > 0 {
		set("X-RateLimit-Limit", strconv.Itoa(d.IPLimit))
		set("X-RateLimit-Remaining", strconv.FormatInt(d.IPRemaining, 10))
		set("X-RateLimit-Reset", strconv.FormatInt(d.IPReset, 10))
	}
	if d.RetryAfter > 0 {
		set("Retry-After", strconv.FormatInt(d.RetryAfter, 10))
	}
}
