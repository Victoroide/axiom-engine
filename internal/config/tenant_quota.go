package config

import (
	json "encoding/json/v2"
	"fmt"
	"strconv"
	"time"

	"github.com/Victoroide/axiom-engine/internal/auth"
)

const (
	tenantQuotaEnabled   = "AXIOM_TENANT_QUOTA_ENABLED"
	tenantQuotaMax       = "AXIOM_TENANT_QUOTA_MAX"
	tenantQuotaWindow    = "AXIOM_TENANT_QUOTA_WINDOW"
	tenantQuotaOverrides = "AXIOM_TENANT_QUOTA_OVERRIDES"
	maxTenantQuota       = 100_000
)

// TenantQuota is an immutable policy for shared tenant admission.
type TenantQuota struct {
	maximum   int
	window    time.Duration
	overrides map[string]int
}

// Maximum returns the override for tenant, or the policy default.
func (p *TenantQuota) Maximum(tenant string) int {
	if maximum, ok := p.overrides[tenant]; ok {
		return maximum
	}
	return p.maximum
}

// Window returns the policy's whole-second sliding window duration.
func (p *TenantQuota) Window() time.Duration { return p.window }

func loadTenantQuota(getenv func(string) string, c *Config) error {
	switch getenv(tenantQuotaEnabled) {
	case "", "false":
		for _, key := range [...]string{tenantQuotaMax, tenantQuotaWindow, tenantQuotaOverrides} {
			if getenv(key) != "" {
				return fmt.Errorf("%s requires %s=true", key, tenantQuotaEnabled)
			}
		}
		return nil
	case "true":
	default:
		return fmt.Errorf("%s must be true or false", tenantQuotaEnabled)
	}
	if c.VerifiedAuth == nil {
		return fmt.Errorf("%s requires AXIOM_AUTH_MODE=verified", tenantQuotaEnabled)
	}
	if c.Redis.URL == "" {
		return fmt.Errorf("%s requires REDIS_URL", tenantQuotaEnabled)
	}

	policy := &TenantQuota{maximum: 50, window: time.Minute}
	if value := getenv(tenantQuotaMax); value != "" {
		maximum, err := strconv.Atoi(value)
		if err != nil || maximum < 1 || maximum > maxTenantQuota {
			return fmt.Errorf("%s must be an integer between 1 and %d", tenantQuotaMax, maxTenantQuota)
		}
		policy.maximum = maximum
	}
	if value := getenv(tenantQuotaWindow); value != "" {
		window, err := time.ParseDuration(value)
		if err != nil || window < time.Second || window > time.Hour || window%time.Second != 0 {
			return fmt.Errorf("%s must be a whole-second duration from 1s to 1h", tenantQuotaWindow)
		}
		policy.window = window
	}
	if value := getenv(tenantQuotaOverrides); value != "" {
		if len(value) > 64*1024 {
			return fmt.Errorf("%s must be at most 64 KiB", tenantQuotaOverrides)
		}
		// JSON v2 rejects duplicate member names, including escaped equivalents.
		if json.Unmarshal([]byte(value), &policy.overrides) != nil || policy.overrides == nil {
			return fmt.Errorf("%s must be a JSON object with unique tenant names and integer values", tenantQuotaOverrides)
		}
		if len(policy.overrides) > 256 {
			return fmt.Errorf("%s must contain at most 256 tenants", tenantQuotaOverrides)
		}
		for tenant, maximum := range policy.overrides {
			if !auth.ValidTenant(tenant) || maximum < 1 || maximum > maxTenantQuota {
				return fmt.Errorf("%s must map valid tenant names to integers between 1 and %d", tenantQuotaOverrides, maxTenantQuota)
			}
		}
	}
	c.TenantQuota = policy
	return nil
}
