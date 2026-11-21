package marketrules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

type tradeLifecycleFixture struct {
	service *Service
	db      *store.Store
	pool    *pgxpool.Pool
	project store.MarketProject
	rule    store.MarketRule
	target  store.MarketRuleTarget
	policy  store.QuotaPolicy
	poolIDs []int64
}

// Exercise the public creation path, without administrative initialization or
// manufactured latest-ready fences. Every row lives in a private test schema.
func newTradeLifecycleFixture(t *testing.T, kind string, scope ...string) tradeLifecycleFixture {
	t.Helper()
	ctx := context.Background()
	db, pool := openTradeLaneIntegrationStore(t)
	const owner = "trade-lifecycle-local-owner"
	if _, err := db.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	policy := store.QuotaPolicy{PlanCode: plans.Professional, RuleLimit: 100, MarketProjectLimit: 5,
		MultiPoolMonitoring: true, AllowedMarketRuleTypes: []string{plans.MarketLargeBuy, plans.MarketConsecutiveLargeBuy}}
	project, err := db.CreateMarketProjectWithinQuota(ctx, store.CreateMarketProjectParams{
		DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken,
		TokenName: "Lifecycle Fixture", TokenSymbol: "TEST", TokenDecimals: 18,
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	var pools []int64
	for index, digit := range []string{"a", "b"} {
		address := "0x" + strings.Repeat(digit, 40)
		p, err := db.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
			ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address,
			Token0Address: tradeFactsTestToken, Token0Decimals: 18, Token0DecimalsVerified: true,
			Token1Address: "0x" + strings.Repeat("2", 40), Token1Decimals: 18, Token1DecimalsVerified: true,
			SupportsEventParsing: true, ParserAdapter: "pancake_v2", VerificationStatus: "verified",
		})
		if err != nil {
			t.Fatal(err)
		}
		pools = append(pools, p.ID)
		if _, err := db.LinkMarketProjectPoolWithinQuota(ctx, store.LinkMarketProjectPoolParams{
			DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: p.ID,
			Selected: index == 0, IsPrimary: index == 0, DiscoverySource: "local_fixture",
		}, policy); err != nil {
			t.Fatal(err)
		}
	}
	params := store.CreateMarketRuleParams{DeBoxUserID: owner, MarketProjectID: project.ID, RuleType: kind,
		ThresholdUnit: "token", ThresholdValue: "1", PoolScope: "all", DeploymentScope: "all", CooldownScope: "chain",
		CooldownSeconds: 600, NotificationChatID: owner, NotificationChatType: "private", TriggerCountThreshold: 2}
	if len(scope) > 0 {
		params.PoolScope = scope[0]
	}
	if kind == plans.MarketConsecutiveLargeBuy {
		params.WindowMinutes = new(int32(5))
	}
	rule, err := db.CreateMarketRuleWithinQuota(ctx, params, policy)
	if err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{Repository: db}, Settings{TradeFactsEnabled: true})
	targets, err := db.ListMarketRuleTargets(ctx, rule, project)
	if err != nil {
		t.Fatal(err)
	}
	merged := MergeTradeRuleTargets(targets)
	if len(merged) != 1 || len(merged[0].TradePoolIDs) != 1 {
		t.Fatalf("initial selected scope=%#v", merged)
	}
	return tradeLifecycleFixture{service, db, pool, project, rule, merged[0], policy, pools}
}

func assertTradeLifecycleLane(t *testing.T, f tradeLifecycleFixture, ready int64, triggered *time.Time) {
	t.Helper()
	target, exists, err := f.db.LoadMarketRuleTargetState(context.Background(), f.rule.ID, f.target)
	var state tradeLaneState
	if err != nil || !exists || json.Unmarshal(target.State, &state) != nil || state.ActivationFence == nil || *state.ActivationFence != 0 || state.LastTradeReadyID != ready {
		t.Fatalf("trade lane was not activated/preserved: %#v, exists=%v, err=%v", target, exists, err)
	}
	if (triggered == nil) != (target.LastTriggeredAt == nil) || triggered != nil && !triggered.Equal(*target.LastTriggeredAt) {
		t.Fatalf("cooldown changed: got=%v want=%v", target.LastTriggeredAt, triggered)
	}
}

func TestPostgresTradeLifecycleNewRulesActivateWithoutAdministrativeFence(t *testing.T) {
	for _, kind := range []string{plans.MarketLargeBuy, plans.MarketConsecutiveLargeBuy} {
		t.Run(kind, func(t *testing.T) {
			f := newTradeLifecycleFixture(t, kind)
			var activation *time.Time
			if err := f.pool.QueryRow(context.Background(), `SELECT trade_activation_after FROM market_rules WHERE id=$1`, f.rule.ID).Scan(&activation); err != nil || activation == nil {
				t.Fatalf("creation did not record a real operation boundary: %v/%v", activation, err)
			}
			// Discovery may link another, unselected pool before the rule's
			// first cycle. That unrelated row must not move its start time.
			address := "0x" + strings.Repeat("c", 40)
			unselected, err := f.db.UpsertMarketPool(context.Background(), store.UpsertMarketPoolParams{
				ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address,
				Token0Address: tradeFactsTestToken, Token1Address: "0x" + strings.Repeat("2", 40),
				Token0Decimals: 18, Token1Decimals: 18, Token0DecimalsVerified: true, Token1DecimalsVerified: true,
				SupportsEventParsing: true, ParserAdapter: "pancake_v2", VerificationStatus: "verified",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.LinkMarketProjectPoolWithinQuota(context.Background(), store.LinkMarketProjectPoolParams{
				DeBoxUserID: f.rule.DeBoxUserID, MarketProjectID: f.project.ID, MarketPoolID: unselected.ID, Selected: false, DiscoverySource: "later_discovery",
			}, f.policy); err != nil {
				t.Fatal(err)
			}
			if count, err := f.service.evaluateRule(context.Background(), f.rule); err != nil || count != 0 {
				t.Fatalf("new rule requires manual initialization: %d/%v", count, err)
			}
			assertTradeLifecycleLane(t, f, 0, nil)
			var eligible *time.Time
			if err := f.pool.QueryRow(context.Background(), `SELECT eligibility_after FROM market_trade_rule_readiness WHERE rule_id=$1 AND target_key=$2`, f.rule.ID, f.target.TargetKey).Scan(&eligible); err != nil || eligible == nil || !eligible.Equal(*activation) {
				t.Fatalf("unselected discovery changed the rule's start: creation=%v eligibility=%v error=%v", activation, eligible, err)
			}
			var count int
			if err := f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1`, f.rule.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("activation generated notifications: %d/%v", count, err)
			}
		})
	}
}

func seedTradeLifecycleProgress(t *testing.T, f tradeLifecycleFixture) time.Time {
	t.Helper()
	ctx := context.Background()
	if _, err := f.service.evaluateRule(ctx, f.rule); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := f.pool.Exec(ctx, `UPDATE market_rule_target_states SET state='{"activation_fence":0,"last_trade_ready_id":123}'::jsonb,last_triggered_at=$3
		WHERE market_rule_id=$1 AND target_key=$2`, f.rule.ID, f.target.TargetKey, at); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE market_rules SET last_triggered_at=$2 WHERE id=$1`, f.rule.ID, at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestPostgresTradeLifecycleRestorePreservesValidProgressAndCooldown(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketLargeBuy)
	at := seedTradeLifecycleProgress(t, f)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE market_rules SET run_status='paused',pause_reason='rule_limit' WHERE id=$1`, f.rule.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := f.db.RestoreMarketRuleWithinQuota(ctx, f.rule.ID, f.rule.DeBoxUserID, f.policy)
	if err != nil || restored.RunStatus != "active" {
		t.Fatalf("restore=%#v/%v", restored, err)
	}
	if count, err := f.service.evaluateRule(ctx, restored); err != nil || count != 0 {
		t.Fatalf("restored lane=%d/%v", count, err)
	}
	assertTradeLifecycleLane(t, f, 123, &at)
}

func TestPostgresTradeLifecyclePoolChangesPreserveProgressAndIgnoreUnchangedRelink(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketLargeBuy)
	at := seedTradeLifecycleProgress(t, f)
	ctx := context.Background()
	type scope struct {
		fingerprint string
		revision    int64
		eligible    time.Time
	}
	readScope := func() scope {
		t.Helper()
		var result scope
		if err := f.pool.QueryRow(ctx, `SELECT scope_fingerprint,scope_revision,eligibility_after FROM market_trade_rule_readiness WHERE rule_id=$1 AND target_key=$2`, f.rule.ID, f.target.TargetKey).
			Scan(&result.fingerprint, &result.revision, &result.eligible); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := readScope()
	link := func(index int, primary bool) {
		t.Helper()
		if _, err := f.db.LinkMarketProjectPoolWithinQuota(ctx, store.LinkMarketProjectPoolParams{
			DeBoxUserID: f.rule.DeBoxUserID, MarketProjectID: f.project.ID, MarketPoolID: f.poolIDs[index], Selected: true,
			IsPrimary: primary, DiscoverySource: "local_fixture",
		}, f.policy); err != nil {
			t.Fatal(err)
		}
		if count, err := f.service.evaluateRule(ctx, f.rule); err != nil || count != 0 {
			t.Fatalf("scope reconciliation=%d/%v", count, err)
		}
	}
	link(0, true)
	if unchanged := readScope(); unchanged != before {
		t.Fatalf("unchanged relink reset scope: before=%#v after=%#v", before, unchanged)
	}
	link(1, false)
	after := readScope()
	if after.fingerprint == before.fingerprint || after.revision <= before.revision || after.eligible.Before(before.eligible) {
		t.Fatalf("pool scope change has no truthful new boundary: before=%#v after=%#v", before, after)
	}
	assertTradeLifecycleLane(t, f, 123, &at)
}

type tradeLifecycleScope struct {
	fingerprint string
	revision    int64
	eligible    time.Time
}

func readTradeLifecycleScope(t *testing.T, f tradeLifecycleFixture) tradeLifecycleScope {
	t.Helper()
	var value tradeLifecycleScope
	if err := f.pool.QueryRow(context.Background(), `SELECT scope_fingerprint,scope_revision,eligibility_after FROM market_trade_rule_readiness WHERE rule_id=$1 AND target_key=$2`, f.rule.ID, f.target.TargetKey).
		Scan(&value.fingerprint, &value.revision, &value.eligible); err != nil {
		t.Fatal(err)
	}
	return value
}

func linkTradeLifecyclePool(t *testing.T, f tradeLifecycleFixture, index int, selected, primary bool) {
	t.Helper()
	if _, err := f.db.LinkMarketProjectPoolWithinQuota(context.Background(), store.LinkMarketProjectPoolParams{
		DeBoxUserID: f.rule.DeBoxUserID, MarketProjectID: f.project.ID, MarketPoolID: f.poolIDs[index],
		Selected: selected, IsPrimary: primary,
	}, f.policy); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresTradeLifecycleReselectSamePoolsRebindsWithoutResettingProgress(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketLargeBuy)
	at := seedTradeLifecycleProgress(t, f)
	before := readTradeLifecycleScope(t, f)
	// Both operations happen before the next evaluation; comparing only the
	// final pool IDs cannot reveal this genuine period with no selected pool.
	linkTradeLifecyclePool(t, f, 0, false, false)
	linkTradeLifecyclePool(t, f, 0, true, true)
	if count, err := f.service.evaluateRule(context.Background(), f.rule); err != nil || count != 0 {
		t.Fatalf("same-pool reactivation=%d/%v", count, err)
	}
	after := readTradeLifecycleScope(t, f)
	if after.fingerprint != before.fingerprint || after.revision <= before.revision || !after.eligible.After(before.eligible) {
		t.Fatalf("same IDs hid a new selection period: before=%#v after=%#v", before, after)
	}
	assertTradeLifecycleLane(t, f, 123, &at)
}

func TestPostgresTradeLifecyclePrimaryChangesRespectActualRulePoolScope(t *testing.T) {
	for _, poolScope := range []string{"all", "primary"} {
		t.Run(poolScope, func(t *testing.T) {
			f := newTradeLifecycleFixture(t, plans.MarketLargeBuy, poolScope)
			linkTradeLifecyclePool(t, f, 1, true, false)
			at := seedTradeLifecycleProgress(t, f)
			before := readTradeLifecycleScope(t, f)
			linkTradeLifecyclePool(t, f, 1, true, true)
			linkTradeLifecyclePool(t, f, 0, true, true)
			if count, err := f.service.evaluateRule(context.Background(), f.rule); err != nil || count != 0 {
				t.Fatalf("primary selection reconciliation=%d/%v", count, err)
			}
			after := readTradeLifecycleScope(t, f)
			if poolScope == "all" {
				if before != after {
					t.Fatalf("primary-only edit reset all-pools monitoring: before=%#v after=%#v", before, after)
				}
			} else if after.fingerprint != before.fingerprint || after.revision <= before.revision || !after.eligible.After(before.eligible) {
				t.Fatalf("returning to the same primary hid a new period: before=%#v after=%#v", before, after)
			}
			assertTradeLifecycleLane(t, f, 123, &at)
		})
	}
}

func TestPostgresTradeLifecycleInFlightReselectionInvalidatesOldEvaluation(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketLargeBuy)
	at := seedTradeLifecycleProgress(t, f)
	ctx := context.Background()
	readiness, err := f.db.ListMarketProjectTradeReadiness(ctx, f.project.ID, f.rule.DeBoxUserID)
	if err != nil || len(readiness) != 1 {
		t.Fatalf("read expected scope evidence=%#v/%v", readiness, err)
	}
	target, exists, err := f.db.LoadMarketRuleTargetState(ctx, f.rule.ID, f.target)
	if err != nil || !exists {
		t.Fatalf("read expected lane=%#v/%v", target, err)
	}
	linkTradeLifecyclePool(t, f, 0, false, false)
	linkTradeLifecyclePool(t, f, 0, true, true)
	err = f.db.WithMarketRuleEvaluationWrite(ctx, f.rule.ID, func(writer *store.Store) error {
		return writer.CommitMarketTradeReadiness(ctx, f.rule, f.project, target, readiness[0], target.State, nil, "ready", "")
	})
	if !errors.Is(err, store.ErrMarketRuleEvaluationStale) {
		t.Fatalf("in-flight evaluation crossed a lifecycle change with the same final IDs: %v", err)
	}
	assertTradeLifecycleLane(t, f, 123, &at)
}
