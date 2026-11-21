package config

import (
	"fmt"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func parseMarketMonitoringChainKeys(value string) ([]string, error) {
	var keys []string
	if strings.TrimSpace(value) != "" {
		keys = strings.Split(value, ",")
	}
	result, err := plans.NormalizeMarketMonitoringChainKeys(keys)
	if err != nil {
		return nil, fmt.Errorf("MARKET_MONITORING_CHAIN_KEYS: %w", err)
	}
	return result, nil
}

func (c Config) EffectiveMarketMonitoringChainKeys() ([]string, error) {
	return plans.NormalizeMarketMonitoringChainKeys(c.MarketMonitoringChainKeys)
}
