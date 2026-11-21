package config

import (
	"fmt"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func loadQuotaLimits() (plans.QuotaLimits, error) {
	limits := plans.DefaultQuotaLimits()
	for _, field := range []struct {
		name  string
		value *int
	}{
		{"QUOTA_FREE_RULE_LIMIT", &limits.FreeRuleLimit},
		{"QUOTA_STANDARD_RULE_LIMIT", &limits.StandardRuleLimit},
		{"QUOTA_PROFESSIONAL_RULE_LIMIT", &limits.ProfessionalRuleLimit},
		{"QUOTA_PROFESSIONAL_INTERACTION_RULE_LIMIT", &limits.ProfessionalInteractionRuleLimit},
		{"QUOTA_PROFESSIONAL_MARKET_PROJECT_LIMIT", &limits.ProfessionalMarketProjectLimit},
		{"QUOTA_STANDARD_MARKET_POOL_LIMIT", &limits.StandardMarketPoolLimit},
		{"QUOTA_PROFESSIONAL_MARKET_POOL_LIMIT", &limits.ProfessionalMarketPoolLimit},
	} {
		value, err := parseIntEnv(field.name, *field.value)
		if err != nil {
			return plans.QuotaLimits{}, err
		}
		if value < 1 {
			return plans.QuotaLimits{}, fmt.Errorf("%s must be greater than zero", field.name)
		}
		*field.value = value
	}
	return plans.NormalizeQuotaLimits(limits)
}

func (c Config) EffectiveQuotaLimits() (plans.QuotaLimits, error) {
	return plans.NormalizeQuotaLimits(c.QuotaLimits)
}
