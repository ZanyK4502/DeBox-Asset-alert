package app

import (
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
)

func newMarketRuleService(cfg config.Config, monitoringChains []string, dependencies marketrules.Dependencies) *marketrules.Service {
	return marketrules.New(dependencies, marketrules.Settings{
		Enabled:               cfg.MarketRuleEngineEnabled,
		TradeFactsEnabled:     true,
		MonitoringChainKeys:   monitoringChains,
		Interval:              cfg.MarketRuleInterval,
		HolderRefreshInterval: cfg.MarketHolderRefreshInterval,
	})
}
