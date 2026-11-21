package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

type performanceSettings struct {
	fallbacks                                        map[string][]string
	sendLimit, lockConnections, hydrationConcurrency int
}

func loadPerformanceSettings() (performanceSettings, error) {
	var result performanceSettings
	var err error
	result.sendLimit, err = parseIntEnv("DEBOX_SEND_PER_MINUTE", 100)
	if err != nil {
		return result, err
	}
	result.lockConnections, err = parseIntEnv("MARKET_TASK_LOCK_CONNECTIONS", 8)
	if err != nil {
		return result, err
	}
	result.hydrationConcurrency, err = parseIntEnv("MARKET_HYDRATION_CONCURRENCY", 2)
	if err != nil {
		return result, err
	}
	if result.sendLimit != 5 && result.sendLimit != 10 && result.sendLimit != 100 {
		return result, fmt.Errorf("DEBOX_SEND_PER_MINUTE must be 100 for v2, or a conservative local limit of 5 or 10")
	}
	if result.lockConnections < 1 || result.lockConnections > 16 {
		return result, fmt.Errorf("MARKET_TASK_LOCK_CONNECTIONS must be between 1 and 16")
	}
	if result.hydrationConcurrency < 1 || result.hydrationConcurrency > 2 {
		return result, fmt.Errorf("MARKET_HYDRATION_CONCURRENCY must be between 1 and 2")
	}
	raw := strings.TrimSpace(os.Getenv("MARKET_RPC_FALLBACK_URLS_JSON"))
	if len(raw) > 64<<10 {
		return result, fmt.Errorf("MARKET_RPC_FALLBACK_URLS_JSON exceeds bounded capacity")
	}
	var configured map[string][]string
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &configured); err != nil {
			return result, fmt.Errorf("MARKET_RPC_FALLBACK_URLS_JSON must map supported chains to URL arrays")
		}
	}
	result.fallbacks = make(map[string][]string, len(configured)+1)
	for key, endpoints := range configured {
		profile, err := chain.ChainProfile(key, "")
		if err != nil {
			return result, fmt.Errorf("MARKET_RPC_FALLBACK_URLS_JSON has an unsupported chain")
		}
		if _, exists := result.fallbacks[profile.Key]; exists {
			return result, fmt.Errorf("MARKET_RPC_FALLBACK_URLS_JSON repeats a normalized chain")
		}
		result.fallbacks[profile.Key] = endpoints
	}
	// The old variable is BNB-only. An explicitly configured empty BNB array
	// disables that legacy route; never apply it to any of the other chains.
	if legacy := strings.TrimSpace(os.Getenv("CHAIN_RPC_URL")); legacy != "" {
		if _, exists := result.fallbacks["bsc"]; !exists {
			result.fallbacks["bsc"] = []string{legacy}
		}
	}
	if err := validateMarketRPCFallbacks(result.fallbacks); err != nil {
		return result, err
	}
	return result, nil
}

func validateMarketRPCFallbacks(fallbacks map[string][]string) error {
	if len(fallbacks) > 6 {
		return fmt.Errorf("market RPC fallback chain capacity exceeded")
	}
	for key, endpoints := range fallbacks {
		if profile, err := chain.ChainProfile(key, ""); err != nil || profile.Key != key {
			return fmt.Errorf("market RPC fallback chain must be canonical and supported")
		}
		if len(endpoints) > 2 {
			return fmt.Errorf("market RPC fallbacks allow at most two endpoints per chain")
		}
		seen := map[string]bool{}
		for _, endpoint := range endpoints {
			if len(endpoint) == 0 || len(endpoint) > 4096 || endpoint != strings.TrimSpace(endpoint) {
				return fmt.Errorf("market RPC fallback URL is empty or exceeds bounded capacity")
			}
			u, err := url.Parse(endpoint)
			if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("invalid market RPC fallback URL")
			}
			loopback := u.Hostname() == "localhost"
			if ip := net.ParseIP(u.Hostname()); ip != nil {
				loopback = ip.IsLoopback()
			}
			if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
				return fmt.Errorf("market RPC fallback requires HTTPS (HTTP is only allowed on loopback)")
			}
			if seen[endpoint] {
				return fmt.Errorf("market RPC fallback endpoints must be distinct")
			}
			seen[endpoint] = true
		}
	}
	return nil
}

func (c Config) validatePerformanceSettings() error {
	// Zero preserves compatibility for callers constructing Config directly;
	// Load always fills and strictly validates the environment defaults.
	if c.DeBoxSendPerMinute != 0 && c.DeBoxSendPerMinute != 5 && c.DeBoxSendPerMinute != 10 && c.DeBoxSendPerMinute != 100 {
		return fmt.Errorf("DEBOX_SEND_PER_MINUTE must be 100 for v2, or a conservative local limit of 5 or 10")
	}
	if c.MarketTaskLockConnections < 0 || c.MarketTaskLockConnections > 16 {
		return fmt.Errorf("MARKET_TASK_LOCK_CONNECTIONS must be between 1 and 16")
	}
	if c.MarketHydrationConcurrency < 0 || c.MarketHydrationConcurrency > 2 {
		return fmt.Errorf("MARKET_HYDRATION_CONCURRENCY must be between 1 and 2")
	}
	if c.MarketCollectorEnabled && c.MarketScanBatchSize > 1000 {
		return fmt.Errorf("MARKET_SCAN_BATCH_SIZE must not exceed 1000")
	}
	return validateMarketRPCFallbacks(c.MarketRPCFallbackURLs)
}

func (c Config) TaskLockConnections() int32 {
	if c.MarketTaskLockConnections == 0 {
		return 8
	}
	return c.MarketTaskLockConnections
}

func (c Config) NotificationSendLimit() int {
	if c.DeBoxSendPerMinute == 0 {
		return 100
	}
	return c.DeBoxSendPerMinute
}
