package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func TestPostgresUnsupportedMarketParametersPreservedAuditedAndDoNotStarveQueue(t *testing.T) {
	database, pool, _ := tradeFactsFixture(t)
	ctx := context.Background()
	old := tradeRuleFixture(t, database)
	state := `{"last_snapshot_id":77,"condition_active":true,"pending_crossing":true}`
	if _, err := pool.Exec(ctx, `UPDATE market_rules SET rule_type=$2,window_minutes=15,threshold_value=3,cycle_minutes=7,
	 delivery_mode='stage',state=$3::jsonb,last_evaluated_at=NULL WHERE id=$1`, old.ID, plans.MarketVolumeAbove, state); err != nil {
		t.Fatal(err)
	}
	params := CreateMarketRuleParams{DeBoxUserID: old.DeBoxUserID, MarketProjectID: old.MarketProjectID, RuleType: plans.MarketPriceAbove, ThresholdValue: "1", WindowMinutes: pointerOf(int32(17))}
	normalizeMarketRuleParams(&params)
	valid, err := createMarketRule(ctx, database.db, params)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := database.ListEnabledMarketRules(ctx, 1)
	if err != nil || len(queue) != 1 || queue[0].ID != valid.ID {
		t.Fatalf("unsupported first rule starved valid rule: %#v,%v", queue, err)
	}
	audit, err := database.AuditMarketRuleParameters(ctx, 10)
	if err != nil || audit.Truncated || len(audit.Rules) != 1 || audit.Rules[0].Rule.ID != old.ID || *audit.Rules[0].Rule.WindowMinutes != 15 || audit.Rules[0].Rule.CycleMinutes != 7 || len(audit.Rules[0].Issues) != 1 {
		t.Fatalf("original impact not preserved: %#v,%v", audit, err)
	}
	if _, err := database.RestoreMarketRuleWithinQuota(ctx, old.ID, old.DeBoxUserID, QuotaPolicy{PlanCode: "free"}); !errors.Is(err, marketwindow.ErrUnsupported) {
		t.Fatalf("restore accepted unsupported saved window: %v", err)
	}
	var window, cycle int32
	var savedState, status string
	if err := pool.QueryRow(ctx, `SELECT window_minutes,cycle_minutes,state::text,run_status FROM market_rules WHERE id=$1`, old.ID).Scan(&window, &cycle, &savedState, &status); err != nil || window != 15 || cycle != 7 || status != "active" {
		t.Fatalf("audit/restore rewrote saved configuration: %d/%d/%s/%s,%v", window, cycle, status, savedState, err)
	}
	var same bool
	if err := pool.QueryRow(ctx, `SELECT state=$2::jsonb FROM market_rules WHERE id=$1`, old.ID, state).Scan(&same); err != nil || !same {
		t.Fatalf("state changed: %s,%v", savedState, err)
	}
	for _, test := range []struct {
		code      string
		window    *int32
		threshold string
	}{
		{plans.MarketVolumeAbove, pointerOf(int32(15)), "1"},
		{plans.MarketPriceIncrease, nil, "1"},
		{plans.MarketHolderRankEntered, nil, "101"},
		{plans.MarketHolderRankExited, nil, "1.5"},
	} {
		params.RuleType, params.WindowMinutes, params.ThresholdValue = test.code, test.window, test.threshold
		if _, err := database.CreateMarketRuleWithinQuota(ctx, params, QuotaPolicy{}); !errors.Is(err, marketwindow.ErrUnsupported) {
			t.Fatalf("create accepted unsupported params %s: %v", test.code, err)
		}
	}
	// SQL filtering uses the same rank and required-period policy as the pure
	// validator, including a non-integral legacy rank and NULL required period.
	for _, test := range []struct {
		code      string
		window    *int32
		threshold string
	}{
		{plans.MarketHolderRankEntered, nil, "100.000"}, {plans.MarketHolderRankEntered, nil, "101"}, {plans.MarketHolderRankExited, nil, "1.5"},
		{plans.MarketPriceIncrease, nil, "1"}, {plans.MarketPriceIncrease, pointerOf(int32(1440)), "1"},
		{plans.MarketVolumeAbove, pointerOf(int32(360)), "1"}, {plans.MarketTradeImbalance, pointerOf(int32(360)), "1"},
		{plans.MarketPriceAbove, pointerOf(int32(17)), "1"},
	} {
		var supported bool
		if err := pool.QueryRow(ctx, `SELECT `+marketRuleSupportedParametersSQL("mr")+` FROM (SELECT $1::text rule_type,$2::int window_minutes,$3::numeric threshold_value) mr`, test.code, test.window, test.threshold).Scan(&supported); err != nil || supported != (len(marketwindow.RuntimeIssues(test.code, test.window, test.threshold)) == 0) {
			t.Fatalf("SQL/shared disagreement %s: %t,%v", test.code, supported, err)
		}
	}
}

func TestPostgresSnapshotBaselineHasNoThousandRowHorizonAndDoesNotSkipMarker(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	targetAt := at.Add(-24 * time.Hour)
	baseline, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0], PriceUSD: pointerOf("1"), Source: "baseline-fixture", CapturedAt: targetAt, QuoteQuality: marketquality.Quote{Status: "live", FetchedAt: &targetAt}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_snapshots(chain_key,chain_id,token_address,market_pool_id,source,captured_at)
	 SELECT 'bsc',56,$1,$2,'baseline-fixture',$3::timestamptz+i*INTERVAL '20 seconds' FROM generate_series(1,4000)i`, tradeFixtureToken, pools[0], targetAt); err != nil {
		t.Fatal(err)
	}
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: &pools[0]}
	got, err := database.MarketSnapshotBaselineForTarget(ctx, target, targetAt)
	if err != nil || got == nil || got.ID != baseline.ID {
		t.Fatalf("24h baseline hidden by newer rows: %#v,%v", got, err)
	}
	marker, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0], PriceUSD: pointerOf("1"), Source: "failure-fixture", CapturedAt: targetAt, QuoteQuality: marketquality.Quote{Status: "stale_fallback", FetchedAt: &targetAt}})
	if err != nil {
		t.Fatal(err)
	}
	got, err = database.MarketSnapshotBaselineForTarget(ctx, target, targetAt)
	if err != nil || got == nil || got.ID != marker.ID || got.QuoteQuality.Status != "stale_fallback" {
		t.Fatalf("bad baseline marker skipped: %#v,%v", got, err)
	}
	target.MarketPoolID = &pools[1]
	got, err = database.MarketSnapshotBaselineForTarget(ctx, target, targetAt)
	if err != nil || got != nil {
		t.Fatalf("cross-pool baseline: %#v,%v", got, err)
	}
}
