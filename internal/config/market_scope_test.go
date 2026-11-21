package config

import (
	"reflect"
	"testing"
)

func TestMarketMonitoringScopeDefaultsAndValidation(t *testing.T) {
	defaultKeys := []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}
	keys, err := parseMarketMonitoringChainKeys("")
	if err != nil || !reflect.DeepEqual(keys, defaultKeys) {
		t.Fatalf("default scope=%v %v", keys, err)
	}
	for _, input := range []string{"bsc", " BNB , bsc "} {
		keys, err := parseMarketMonitoringChainKeys(input)
		if err != nil || len(keys) != 1 || keys[0] != "bsc" {
			t.Fatalf("scope %q=%v %v", input, keys, err)
		}
	}
	for _, input := range []string{",", "bsc,", "solana", "ethereum,,bsc"} {
		if _, err := parseMarketMonitoringChainKeys(input); err == nil {
			t.Fatalf("invalid scope accepted: %q", input)
		}
	}
	keys, err = (Config{}).EffectiveMarketMonitoringChainKeys()
	if err != nil || !reflect.DeepEqual(keys, defaultKeys) {
		t.Fatal(keys, err)
	}
	for _, input := range [][]string{{}, {""}, {"unsupported"}} {
		if _, err := (Config{MarketMonitoringChainKeys: input}).EffectiveMarketMonitoringChainKeys(); err == nil {
			t.Fatalf("invalid direct scope accepted: %#v", input)
		}
	}
	cfg := Config{MarketMonitoringChainKeys: []string{"bsc"}}
	keys, err = cfg.EffectiveMarketMonitoringChainKeys()
	if err != nil {
		t.Fatal(err)
	}
	keys[0] = "base"
	if cfg.MarketMonitoringChainKeys[0] != "bsc" {
		t.Fatal("effective getter leaked config scope backing array")
	}
}
