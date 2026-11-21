package app

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type sixChainStartupRepository struct {
	startupMarketRuleRepository
	projects               map[int64]store.MarketProject
	snapshotReadsByProject map[int64]int
}

func (r *sixChainStartupRepository) GetMarketProject(_ context.Context, id int64, owner string) (*store.MarketProject, error) {
	project, exists := r.projects[id]
	if !exists || project.DeBoxUserID != owner {
		return nil, fmt.Errorf("fixture project does not belong to the requested owner")
	}
	return &project, nil
}

func (r *sixChainStartupRepository) ListMarketSnapshots(_ context.Context, id int64, owner string, _ int) ([]store.MarketSnapshot, error) {
	project, exists := r.projects[id]
	if !exists || project.DeBoxUserID != owner {
		return nil, fmt.Errorf("snapshot lookup crossed fixture owners")
	}
	r.snapshotReadsByProject[id]++
	return nil, nil
}

// Exercise the real startup constructor with all six chains in one cycle.
// Identical token addresses on different chains must retain separate projects,
// while restoring scope must not re-enable the legacy trade-event fallback.
func TestSixChainStartupEvaluatesEachChainAndRetainsTradeFactsProtection(t *testing.T) {
	keys := []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}
	cfg := config.Config{MarketCollectorEnabled: true, MarketRuleEngineEnabled: true, MarketChainKeys: keys}
	monitoring, err := cfg.EffectiveMarketMonitoringChainKeys()
	if err != nil || !reflect.DeepEqual(monitoring, keys) {
		t.Fatalf("default monitoring scope = %v, error = %v", monitoring, err)
	}
	collectors, err := marketCollectorChainKeys(cfg, monitoring)
	if err != nil || !reflect.DeepEqual(collectors, keys) {
		t.Fatalf("collector scope = %v, error = %v", collectors, err)
	}
	catalog, err := plans.NewCatalogWithMarketScope("5", 30, "USDT", plans.QuotaLimits{}, monitoring)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{plans.Standard, plans.Professional} {
		if got := marketScopePolicies(catalog)[code].AllowedMarketChainKeys; !reflect.DeepEqual(got, keys) {
			t.Fatalf("%s admission scope = %v", code, got)
		}
	}

	repository := &sixChainStartupRepository{
		projects:               map[int64]store.MarketProject{},
		snapshotReadsByProject: map[int64]int{},
	}
	chainIDs := []int64{56, 1, 8453, 137, 42161, 10}
	var wantUpdated []int64
	for index, key := range keys {
		projectID := int64(index + 1)
		owner := fmt.Sprintf("six-chain-startup-%d", index)
		repository.projects[projectID] = store.MarketProject{
			ID: projectID, DeBoxUserID: owner, Status: "active",
			ChainKey: key, ChainID: chainIDs[index],
			TokenAddress: "0x1111111111111111111111111111111111111111",
		}
		priceID := projectID * 10
		repository.rules = append(repository.rules,
			store.MarketRule{ID: priceID, MarketProjectID: projectID, DeBoxUserID: owner, RuleType: plans.MarketPriceAbove, ThresholdValue: "1"},
			store.MarketRule{ID: priceID + 1, MarketProjectID: projectID, DeBoxUserID: owner, RuleType: plans.MarketLargeBuy, ThresholdValue: "10000"},
		)
		wantUpdated = append(wantUpdated, priceID)
	}
	notifications := &startupMarketNotifications{}
	service := newMarketRuleService(cfg, monitoring, marketrules.Dependencies{Repository: repository, Notifications: notifications})
	result, err := service.RunCycle(context.Background())
	if err == nil || strings.Count(err.Error(), "market trade facts repository is unavailable") != len(keys) {
		t.Fatalf("trade facts were not required independently for all six chains: result=%+v err=%v", result, err)
	}
	if result.RulesEvaluated != int64(len(keys)) || result.TriggersCreated != 0 || notifications.calls != 0 || repository.legacyReads != 0 {
		t.Fatalf("six-chain startup skipped scope or used legacy delivery: result=%+v sends=%d legacy_reads=%d", result, notifications.calls, repository.legacyReads)
	}
	if !reflect.DeepEqual(repository.updatedRules, wantUpdated) {
		t.Fatalf("per-project price states = %v, want %v", repository.updatedRules, wantUpdated)
	}
	for projectID := range repository.projects {
		if reads := repository.snapshotReadsByProject[projectID]; reads != 1 {
			t.Fatalf("project %d snapshot reads = %d, want 1", projectID, reads)
		}
	}
}
