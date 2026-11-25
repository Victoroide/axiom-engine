package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	redisstorage "github.com/gofiber/storage/redis/v3"

	"github.com/Victoroide/axiom-engine/internal/auth"
	"github.com/Victoroide/axiom-engine/internal/config"
)

const tenantRatePrefix = "axiom:tenant-rate:v1:"

// AdmissionDecision reports whether a request may proceed. The IP header
// values are present if the IP admitted the request. Delays are in seconds.
type AdmissionDecision struct {
	Status      int
	IPLimit     int
	IPRemaining int64
	IPReset     int64
	RetryAfter  int64
}

// RedisAdmission applies the same shared budgets across HTTP transports.
type RedisAdmission struct {
	redisWindow
	ipMaximum int
	ipWindow  time.Duration
	tenant    *config.TenantQuota
}

// NewRedisAdmission opens shared request storage and checks that it is reachable.
// It returns nil when Redis is unconfigured and tenant quotas are disabled.
func NewRedisAdmission(cfg config.Config, logger *slog.Logger) (*RedisAdmission, error) {
	if cfg.TenantQuota != nil && (cfg.AuthDisabled || cfg.VerifiedAuth == nil || cfg.Redis.URL == "" || cfg.TenantQuota.Window() == 0) {
		return nil, fmt.Errorf("tenant quotas require a valid policy, verified authentication and Redis")
	}
	storage, err := openRedis(cfg.Redis)
	if err != nil || storage == nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return newRedisAdmission(cfg, logger, storage), nil
}

func newRedisAdmission(cfg config.Config, logger *slog.Logger, storage *redisstorage.Storage) *RedisAdmission {
	return &RedisAdmission{
		redisWindow: redisWindow{storage: storage, logger: logger},
		ipMaximum:   cfg.RateLimitMax, ipWindow: cfg.RateLimitWindow, tenant: cfg.TenantQuota,
	}
}

// Close releases the shared storage connection.
func (a *RedisAdmission) Close() error {
	return errors.Join(a.storage.Close(), a.storage.Conn().Close())
}

// Admit checks the IP and then the optional tenant budget for one action.
// The caller must authenticate, check scope and resolve the trusted client IP
// before calling Admit. Use it once before replay or dispatch.
func (a *RedisAdmission) Admit(ctx context.Context, ip string, principal auth.Principal) AdmissionDecision {
	var tenant rateBucket
	if a.tenant != nil {
		if principal.Issuer() == "" || principal.Tenant() == "" {
			return AdmissionDecision{Status: http.StatusServiceUnavailable, RetryAfter: 1}
		}
		tenant = rateBucket{
			key:     fmt.Sprintf("%s%x", tenantRatePrefix, identityDigest(principal.Issuer(), principal.Tenant())),
			window:  int64(a.tenant.Window() / time.Second),
			maximum: a.tenant.Maximum(principal.Tenant()),
		}
	}
	return a.admit(ctx, ipRateBucket(normalizeRateLimitIP(ip), a.ipMaximum, a.ipWindow), tenant)
}
