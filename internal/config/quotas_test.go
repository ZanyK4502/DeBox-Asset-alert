package config

import (
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

var quotaEnvNames = []string{"QUOTA_FREE_RULE_LIMIT", "QUOTA_STANDARD_RULE_LIMIT", "QUOTA_PROFESSIONAL_RULE_LIMIT", "QUOTA_PROFESSIONAL_INTERACTION_RULE_LIMIT", "QUOTA_PROFESSIONAL_MARKET_PROJECT_LIMIT", "QUOTA_STANDARD_MARKET_POOL_LIMIT", "QUOTA_PROFESSIONAL_MARKET_POOL_LIMIT"}

func clearQuotaTestEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range quotaEnvNames {
		t.Setenv(name, "")
	}
}

func TestQuotaLimitsDefaultsAndDirectConfigFallback(t *testing.T) {
	clearQuotaTestEnvironment(t)
	loaded, err := loadQuotaLimits()
	if err != nil || loaded != plans.DefaultQuotaLimits() {
		t.Fatalf("default quotas=%+v %v", loaded, err)
	}
	if loaded.ProfessionalMarketProjectLimit != 5 || loaded.ProfessionalMarketPoolLimit != 5 {
		t.Fatalf("professional market defaults=%+v, want 5 projects and 5 pools per project", loaded)
	}
	direct, err := (Config{}).EffectiveQuotaLimits()
	if err != nil || direct != loaded {
		t.Fatalf("direct Config fallback=%+v %v", direct, err)
	}
	if _, err = (Config{QuotaLimits: plans.QuotaLimits{FreeRuleLimit: 1}}).EffectiveQuotaLimits(); err == nil {
		t.Fatal("partial Config silently defaulted")
	}
}

func TestLoadWiresExplicitQuotaLimits(t *testing.T) {
	clearQuotaTestEnvironment(t)
	t.Setenv("STATIC_DIR", testStaticDir(t))
	t.Setenv("MARKET_COLLECTOR_ENABLED", "false")
	t.Setenv("MARKET_RULE_ENGINE_ENABLED", "false")
	t.Setenv("QUOTA_STANDARD_RULE_LIMIT", "7")
	t.Setenv("QUOTA_PROFESSIONAL_RULE_LIMIT", "25")
	t.Setenv("QUOTA_PROFESSIONAL_INTERACTION_RULE_LIMIT", "2")
	t.Setenv("QUOTA_PROFESSIONAL_MARKET_PROJECT_LIMIT", "4")
	t.Setenv("QUOTA_PROFESSIONAL_MARKET_POOL_LIMIT", "7")
	t.Setenv("MARKET_MONITORING_CHAIN_KEYS", "bnb,base,bsc")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	q, err := cfg.EffectiveQuotaLimits()
	if err != nil || q.FreeRuleLimit != 1 || q.StandardRuleLimit != 7 || q.ProfessionalRuleLimit != 25 || q.ProfessionalInteractionRuleLimit != 2 || q.ProfessionalMarketProjectLimit != 4 || q.StandardMarketPoolLimit != 1 || q.ProfessionalMarketPoolLimit != 7 {
		t.Fatalf("configured quotas=%+v %v", q, err)
	}
	keys, err := cfg.EffectiveMarketMonitoringChainKeys()
	if err != nil || len(keys) != 2 || keys[0] != "bsc" || keys[1] != "base" {
		t.Fatalf("loaded monitoring chains=%v %v", keys, err)
	}
}

func TestLoadRejectsExplicitInvalidQuotaLimits(t *testing.T) {
	for _, name := range quotaEnvNames {
		for _, value := range []string{"0", "-1", "1.5", "not-an-integer", "999999999999999999999999999999999"} {
			t.Run(name+"/"+value, func(t *testing.T) {
				clearQuotaTestEnvironment(t)
				t.Setenv(name, value)
				if _, err := Load(); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("invalid explicit quota %s=%s accepted or unlabelled: %v", name, value, err)
				}
			})
		}
	}
	for _, entry := range []struct{ name, value string }{{"QUOTA_FREE_RULE_LIMIT", "2"}, {"QUOTA_PROFESSIONAL_INTERACTION_RULE_LIMIT", "11"}, {"QUOTA_STANDARD_MARKET_POOL_LIMIT", "2"}} {
		t.Run(entry.name+"/inconsistent", func(t *testing.T) {
			clearQuotaTestEnvironment(t)
			t.Setenv(entry.name, entry.value)
			if _, err := Load(); err == nil {
				t.Fatal("inconsistent limits accepted")
			}
		})
	}
}
