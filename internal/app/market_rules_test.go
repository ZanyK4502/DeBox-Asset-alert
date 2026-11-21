package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// This adapter deliberately lacks the trade-facts interface. Startup must
// reject that dependency for trade rules instead of silently using old events.
type startupMarketRuleRepository struct {
	marketrules.Repository
	rules         []store.MarketRule
	project       store.MarketProject
	snapshotReads int
	legacyReads   int
	updatedRules  []int64
}

func (r *startupMarketRuleRepository) ApplyExpiredEntitlementFallbacks(context.Context) (int64, error) {
	return 0, nil
}
func (r *startupMarketRuleRepository) ReconcileMarketNotificationSources(context.Context) error {
	return nil
}
func (r *startupMarketRuleRepository) RecoverStaleMarketDeliveries(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (r *startupMarketRuleRepository) ProcessPendingWatchCombinationTriggers(context.Context, int) (int64, error) {
	return 0, nil
}
func (r *startupMarketRuleRepository) ListEnabledMarketRules(context.Context, int) ([]store.MarketRule, error) {
	return r.rules, nil
}
func (r *startupMarketRuleRepository) GetMarketProject(context.Context, int64, string) (*store.MarketProject, error) {
	return &r.project, nil
}
func (r *startupMarketRuleRepository) ListMarketSnapshots(context.Context, int64, string, int) ([]store.MarketSnapshot, error) {
	r.snapshotReads++
	return nil, nil
}
func (r *startupMarketRuleRepository) ListMarketEvents(context.Context, int64, string, int64, int) ([]store.MarketEvent, error) {
	r.legacyReads++
	return nil, errors.New("startup unexpectedly read legacy trade events")
}
func (r *startupMarketRuleRepository) UpdateMarketRuleState(_ context.Context, id int64, state json.RawMessage, _ bool) (store.MarketRule, error) {
	r.updatedRules = append(r.updatedRules, id)
	return store.MarketRule{ID: id, State: state}, nil
}
func (r *startupMarketRuleRepository) ClaimMarketDeliveries(context.Context, int) ([]store.MarketDeliveryClaim, error) {
	return nil, nil
}

type startupMarketNotifications struct{ calls int }

func (n *startupMarketNotifications) SendNotification(string, string, string) (string, error) {
	n.calls++
	return "", errors.New("startup fixture must not send notifications")
}

func TestMarketRuleStartupUsesFactsForAllFourTradeRulesAndKeepsPriceRoute(t *testing.T) {
	window := int32(15)
	repository := &startupMarketRuleRepository{
		project: store.MarketProject{ID: 1, Status: "active", ChainKey: "bsc", ChainID: 56},
	}
	for i, kind := range []string{
		plans.MarketLargeBuy, plans.MarketLargeSell,
		plans.MarketConsecutiveLargeBuy, plans.MarketConsecutiveLargeSell,
		plans.MarketPriceAbove,
	} {
		repository.rules = append(repository.rules, store.MarketRule{
			ID: int64(i + 1), MarketProjectID: 1, RuleType: kind,
			ThresholdValue: "10000", WindowMinutes: &window,
		})
	}
	notifications := &startupMarketNotifications{}
	service := newMarketRuleService(
		config.Config{MarketRuleEngineEnabled: true}, []string{"bsc"},
		marketrules.Dependencies{Repository: repository, Notifications: notifications},
	)
	result, err := service.RunCycle(context.Background())
	if err == nil || strings.Count(err.Error(), "market trade facts repository is unavailable") != 4 {
		t.Fatalf("actual startup constructor did not require trade facts for all four rules: result=%+v err=%v", result, err)
	}
	if result.RulesEvaluated != 1 || repository.snapshotReads != 1 || repository.legacyReads != 0 ||
		!reflect.DeepEqual(repository.updatedRules, []int64{5}) || result.TriggersCreated != 0 || notifications.calls != 0 {
		t.Fatalf("trade startup fell back or interfered with the price rule: result=%+v repository=%+v sends=%d", result, repository, notifications.calls)
	}
}

func TestMarketRuleStartupPreservesDisabledEngine(t *testing.T) {
	service := newMarketRuleService(config.Config{}, []string{"bsc"}, marketrules.Dependencies{})
	result, err := service.RunCycle(context.Background())
	if service.Enabled() || err != nil || !reflect.DeepEqual(result, marketrules.CycleResult{}) {
		t.Fatalf("trade facts wiring enabled a disabled engine: enabled=%v result=%+v err=%v", service.Enabled(), result, err)
	}
}

func TestMarketRuleStartupPreservesMonitoringChainScope(t *testing.T) {
	repository := &startupMarketRuleRepository{
		project: store.MarketProject{ID: 1, Status: "active", ChainKey: "ethereum", ChainID: 1},
		rules:   []store.MarketRule{{ID: 1, MarketProjectID: 1, RuleType: plans.MarketPriceAbove, ThresholdValue: "1"}},
	}
	notifications := &startupMarketNotifications{}
	service := newMarketRuleService(
		config.Config{MarketRuleEngineEnabled: true}, []string{"bsc"},
		marketrules.Dependencies{Repository: repository, Notifications: notifications},
	)
	result, err := service.RunCycle(context.Background())
	if err != nil || repository.snapshotReads != 0 || len(repository.updatedRules) != 0 || notifications.calls != 0 || result.TriggersCreated != 0 {
		t.Fatalf("startup expanded monitoring outside BSC: result=%+v repository=%+v sends=%d err=%v", result, repository, notifications.calls, err)
	}
}
