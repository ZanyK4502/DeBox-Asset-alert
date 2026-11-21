package marketrules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestMarketScopeRuntimeSkipsExcludedTradeTargetBeforeStateOrReadiness(t *testing.T) {
	service, repo, rule := newTradeLaneTestService(true)
	merged := MergeTradeRuleTargets(repo.targets)
	if _, err := service.InitializeTradeRuleState(context.Background(), rule.ID, merged[0], 100); err != nil {
		t.Fatal(err)
	}
	eth := repo.targets[0]
	eth.TargetKey = "deployment:8/pool:3"
	eth.ChainID = 1
	eth.ChainKey = "ethereum"
	id := int64(8)
	eth.MarketProjectDeploymentID = &id
	repo.targets = append(repo.targets, eth)
	repo.initializations = 0
	repo.loadedKeys = nil
	service.settings.MonitoringChainKeys = []string{"bsc"}
	if _, err := service.evaluateRule(context.Background(), rule); err != nil {
		t.Fatalf("excluded uninitialized chain blocked allowed target: %v", err)
	}
	for _, key := range repo.loadedKeys {
		if strings.Contains(key, "deployment:8/") {
			t.Fatalf("excluded target state loaded: %v", repo.loadedKeys)
		}
	}
	if _, ok := repo.states["deployment:8/trades:v2"]; ok || repo.initializations != 0 {
		t.Fatal("excluded target was initialized")
	}
}

func TestMarketScopeRuntimeSkipsSnapshotChainWithoutChangingItsState(t *testing.T) {
	service, repo, rule := newTradeLaneTestService(false)
	target := repo.targets[0]
	target.ChainKey = "ethereum"
	target.ChainID = 1
	target.TargetKey = "excluded-price"
	target.State = json.RawMessage(`{"condition_active":true,"last_event_id":4321}`)
	repo.targets = []store.MarketRuleTarget{target}
	repo.states[target.TargetKey] = target
	rule.RuleType = plans.MarketPriceAbove
	service.settings.MonitoringChainKeys = []string{"bsc"}
	if n, err := service.evaluateRule(context.Background(), rule); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if len(repo.loadedKeys) != 0 || len(repo.changedTargets) != 0 || string(repo.states[target.TargetKey].State) != string(target.State) {
		t.Fatal("excluded chain baseline/cooldown was touched")
	}
}

func TestMarketScopeRuntimeCopiesExplicitScopeAndKeepsLegacyAdapter(t *testing.T) {
	keys := []string{"bsc"}
	s := New(Dependencies{}, Settings{MonitoringChainKeys: keys})
	keys[0] = "ethereum"
	if !s.marketChainAllowed("bsc", 56) || s.marketChainAllowed("ethereum", 1) || s.marketChainAllowed("bsc", 1) {
		t.Fatal("runtime chain scope was bypassed or mutated")
	}
	if !New(Dependencies{}, Settings{}).marketChainAllowed("ethereum", 1) {
		t.Fatal("legacy adapter implicitly restricted")
	}
	if New(Dependencies{}, Settings{MonitoringChainKeys: []string{}}).marketChainAllowed("bsc", 56) {
		t.Fatal("explicit empty scope opened every chain")
	}
}
