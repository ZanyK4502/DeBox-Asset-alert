package marketrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func publishReadinessTrade(t *testing.T, f tradeLifecycleFixture, block int64, at time.Time, options ...string) store.MarketEvent {
	t.Helper()
	ctx := context.Background()
	kind, confirmed := "buy", true
	if len(options) > 0 {
		kind = options[0]
	}
	if len(options) > 1 && options[1] == "pending" {
		confirmed = false
	}
	hash, blockHash, parentHash := fmt.Sprintf("0x%064x", block), fmt.Sprintf("0x%064x", block+10000), fmt.Sprintf("0x%064x", block+9999)
	if _, err := f.db.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "readiness_fixture", BlockNumber: block, BlockHash: blockHash, ParentHash: parentHash, BlockTimestamp: &at}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"transactionHash": hash, "blockHash": blockHash, "blockNumber": fmt.Sprintf("0x%x", block), "transactionIndex": "0x0", "status": "0x1", "logs": []map[string]string{{"logIndex": "0x1"}}})
	parents, err := f.db.PersistMarketTransaction(ctx, store.MarketTransactionParams{ChainKey: "bsc", ChainID: 56, TransactionHash: hash, BlockNumber: block, BlockHash: blockHash, OccurredAt: at, Confirmed: confirmed, ParserVersion: 1,
		ScopePoolIDs: []int64{f.poolIDs[0]}, ScopeTokenAddresses: []string{tradeFactsTestToken}, ReceiptPayload: payload,
		Events: []store.MarketTransactionEvent{{Event: store.CreateMarketEventParams{TokenAddress: tradeFactsTestToken, EventType: kind, EventKey: fmt.Sprintf("readiness-%d", block), Source: "local_fixture", TokenAmountRaw: stringPointer("10000000000000000000"), QuoteAmountRaw: stringPointer("1000000000000000000"), TokenAmount: stringPointer("10"), QuoteAmount: stringPointer("1")},
			Legs: []store.MarketTradeLegParams{{PoolID: f.poolIDs[0], LogIndex: 1, QuoteAddress: "0x" + strings.Repeat("2", 40), TokenAmountRaw: "10000000000000000000", QuoteAmountRaw: "1000000000000000000", TokenAmount: stringPointer("10"), QuoteAmount: stringPointer("1")}}}}})
	if err != nil || len(parents) != 1 {
		t.Fatalf("publish local trade=%+v/%v", parents, err)
	}
	return parents[0]
}

func readinessFixtureCoverage(t *testing.T, f tradeLifecycleFixture, after time.Time, event store.MarketEvent) {
	t.Helper()
	ctx := context.Background()
	hash := "0x" + strings.Repeat("f", 64)
	if _, err := f.db.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "readiness_fixture", BlockNumber: 0, BlockHash: hash, ParentHash: "0x" + strings.Repeat("0", 64), BlockTimestamp: &after}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecordMarketScanCoverage(ctx, store.RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "readiness_fixture", ParserVersion: 1, ScopePoolIDs: []int64{f.poolIDs[0]}, ScopeTokenAddresses: []string{tradeFactsTestToken}, FromBlock: 1, ThroughBlock: *event.BlockNumber, AfterAt: after, ThroughAt: event.OccurredAt, AfterHash: hash, ThroughHash: *event.BlockHash}); err != nil {
		t.Fatal(err)
	}
}

func readTradeReadiness(t *testing.T, f tradeLifecycleFixture) store.MarketTradeReadiness {
	t.Helper()
	states, err := f.db.ListMarketProjectTradeReadiness(context.Background(), f.project.ID, f.rule.DeBoxUserID)
	if err != nil || len(states) != 1 {
		t.Fatalf("readiness states=%+v/%v", states, err)
	}
	return states[0]
}

func TestPostgresTradeReadinessWarmupAdvancesWithoutAlertsThenCountsPreservedFacts(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketConsecutiveLargeBuy)
	ctx := context.Background()
	base := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	first := publishReadinessTrade(t, f, 100, base.Add(10*time.Second))
	f.service.now = func() time.Time { return first.OccurredAt.Add(time.Second) }
	if count, err := f.service.evaluateRule(ctx, f.rule); err != nil || count != 0 {
		t.Fatalf("missing first window should warm up=%d/%v", count, err)
	}
	assertTradeLifecycleLane(t, f, first.TradeReadyID, nil)
	if state := readTradeReadiness(t, f); state.Status != "warming" || state.Reason != "scan_coverage_incomplete" {
		t.Fatalf("initial warmup state=%+v", state)
	}
	var skipped, outbox, periods int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1 AND notification_status='skipped'),(SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1 AND notification_status<>'skipped'),(SELECT COUNT(*) FROM market_trade_warmup_periods WHERE rule_id=$1)`, f.rule.ID).Scan(&skipped, &outbox, &periods); err != nil || skipped != 1 || outbox != 0 || periods != 1 {
		t.Fatalf("warmup audit/delivery=%d/%d/%d/%v", skipped, outbox, periods, err)
	}
	// Five minutes of truthful coverage make the next complete window include the
	// first raw warmup fact. It was suppressed only as its own alert candidate.
	fresh := publishReadinessTrade(t, f, 200, base.Add(5*time.Minute+5*time.Second))
	readinessFixtureCoverage(t, f, base, fresh)
	f.service.now = func() time.Time { return fresh.OccurredAt.Add(time.Second) }
	if count, err := f.service.evaluateRule(ctx, f.rule); err != nil || count != 1 {
		t.Fatalf("complete window must recover automatically and count warmup fact=%d/%v", count, err)
	}
	if state := readTradeReadiness(t, f); state.Status != "ready" {
		t.Fatalf("completed window status=%+v", state)
	}
	target, _, err := f.db.LoadMarketRuleTargetState(ctx, f.rule.ID, f.target)
	if err != nil {
		t.Fatal(err)
	}
	triggered := target.LastTriggeredAt
	if triggered == nil {
		t.Fatal("fresh valid alert did not retain cooldown")
	}
	late := publishReadinessTrade(t, f, 102, base.Add(30*time.Second))
	if count, err := f.service.evaluateRule(ctx, f.rule); err != nil || count != 0 {
		t.Fatalf("late warmup source must not send=%d/%v", count, err)
	}
	assertTradeLifecycleLane(t, f, late.TradeReadyID, triggered)
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1 AND notification_status='skipped'),(SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1 AND notification_status<>'skipped'),(SELECT COUNT(*) FROM market_trade_legs)`, f.rule.ID).Scan(&skipped, &outbox, &periods); err != nil || skipped != 2 || outbox != 1 || periods != 3 {
		t.Fatalf("late warmup altered original facts or sent alert=%d/%d/%d/%v", skipped, outbox, periods, err)
	}
	// An even earlier delayed gap must end at the already proven ready instant,
	// rather than reopening warmup across later healthy market time.
	older := publishReadinessTrade(t, f, 101, base.Add(5*time.Second))
	if n, err := f.service.evaluateRule(ctx, f.rule); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	assertTradeLifecycleLane(t, f, older.TradeReadyID, triggered)
	if state := readTradeReadiness(t, f); state.Status != "ready" {
		t.Fatalf("late earlier gap moved current readiness backward: %+v", state)
	}
	var readyFrom time.Time
	if err := f.pool.QueryRow(ctx, `SELECT ready_from_at FROM market_trade_warmup_periods WHERE rule_id=$1 AND from_at=$2`, f.rule.ID, older.OccurredAt).Scan(&readyFrom); err != nil || !readyFrom.Equal(older.OccurredAt.Add(time.Microsecond)) {
		t.Fatalf("late gap must record only its witnessed time point: %v/%v", readyFrom, err)
	}
	future := publishReadinessTrade(t, f, 300, base.Add(6*time.Minute))
	readinessFixtureCoverage(t, f, base, future)
	f.service.now = func() time.Time { return future.OccurredAt.Add(time.Second) }
	if n, err := f.service.evaluateRule(ctx, f.rule); err != nil || n != 0 {
		t.Fatalf("future ready retains existing cooldown=%d/%v", n, err)
	}
	validLate := publishReadinessTrade(t, f, 210, base.Add(5*time.Minute+30*time.Second))
	if n, err := f.service.evaluateRule(ctx, f.rule); err != nil || n != 0 {
		t.Fatalf("late source within formerly ready time=%d/%v", n, err)
	}
	var decisionReason string
	if err := f.pool.QueryRow(ctx, `SELECT notification_error FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$2`, f.rule.ID, validLate.ID).Scan(&decisionReason); err != nil || strings.Contains(decisionReason, "warmup") {
		t.Fatalf("retroactive warmup swallowed valid source after old ready boundary: %q/%v", decisionReason, err)
	}
}

func TestPostgresTradeReadinessCurrentUnknownCannotBecomeWarmup(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketConsecutiveLargeBuy)
	ctx := context.Background()
	event := publishReadinessTrade(t, f, 100, time.Now().UTC().Add(time.Second).Truncate(time.Microsecond))
	if _, err := f.pool.Exec(ctx, `UPDATE market_trade_pool_summaries SET token_amount=NULL WHERE parent_event_id=$1`, event.ID); err != nil {
		t.Fatal(err)
	}
	f.service.now = func() time.Time { return event.OccurredAt.Add(time.Second) }
	if count, err := f.service.evaluateRule(ctx, f.rule); err == nil || count != 0 {
		t.Fatalf("current unknown was silently skipped=%d/%v", count, err)
	}
	assertTradeLifecycleLane(t, f, 0, nil)
	if state := readTradeReadiness(t, f); state.Status != "blocked" {
		t.Fatalf("current data unavailable presented as warmup/ready=%+v", state)
	}
	var decisions, periods int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1),(SELECT COUNT(*) FROM market_trade_warmup_periods WHERE rule_id=$1)`, f.rule.ID).Scan(&decisions, &periods); err != nil || decisions != 0 || periods != 0 {
		t.Fatalf("blocked source created decisions=%d/%d/%v", decisions, periods, err)
	}
}

func TestPostgresTradeReadinessLegacyLaneNeedsActualCutoffAndPreservesProgress(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketLargeBuy)
	at := seedTradeLifecycleProgress(t, f)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `DELETE FROM market_trade_rule_readiness WHERE rule_id=$1`, f.rule.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE market_rules SET trade_activation_after=NULL WHERE id=$1`, f.rule.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.evaluateRule(ctx, f.rule); !errors.Is(err, store.ErrMarketTradeActivationRequired) {
		t.Fatalf("legacy state guessed old scope provenance=%v", err)
	}
	assertTradeLifecycleLane(t, f, 123, &at)
	cutoff := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := f.db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "isolated reviewed lifecycle test"); err != nil {
		t.Fatal(err)
	}
	if n, err := f.service.evaluateRule(ctx, f.rule); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	assertTradeLifecycleLane(t, f, 123, &at)
	state := readTradeReadiness(t, f)
	if state.ActivationOrigin != "recovery_boundary" || state.EligibilityAfter == nil || !state.EligibilityAfter.Equal(cutoff) {
		t.Fatalf("legacy provenance=%+v", state)
	}
}

func TestPostgresTradeReadinessCommitCASPreservesLargeIntegerAndRollsBack(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketLargeBuy)
	ctx := context.Background()
	if _, err := f.service.evaluateRule(ctx, f.rule); err != nil {
		t.Fatal(err)
	}
	state := readTradeReadiness(t, f)
	expected := json.RawMessage(`{"activation_fence":0,"last_trade_ready_id":9007199254740992}`)
	if _, err := f.pool.Exec(ctx, `UPDATE market_rule_target_states SET state='{"activation_fence":0,"last_trade_ready_id":9007199254740993}' WHERE market_rule_id=$1 AND target_key=$2`, f.rule.ID, f.target.TargetKey); err != nil {
		t.Fatal(err)
	}
	err := f.db.WithMarketRuleEvaluationWrite(ctx, f.rule.ID, func(writer *store.Store) error {
		return writer.CommitMarketTradeReadiness(ctx, f.rule, f.project, f.target, state, expected, nil, "ready", "")
	})
	if !errors.Is(err, store.ErrMarketRuleEvaluationStale) {
		t.Fatalf("CAS lost int64 precision=%v", err)
	}
	actual, _, err := f.db.LoadMarketRuleTargetState(ctx, f.rule.ID, f.target)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("rollback after readiness before outbox")
	err = f.db.WithMarketRuleEvaluationWrite(ctx, f.rule.ID, func(writer *store.Store) error {
		if err := writer.CommitMarketTradeReadiness(ctx, f.rule, f.project, f.target, state, actual.State, []store.MarketTradeReadinessDecision{{OccurredAt: time.Now().UTC(), WarmupReason: "scan_coverage_incomplete"}}, "warming", "scan_coverage_incomplete"); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	after := readTradeReadiness(t, f)
	if after.Version != state.Version || after.Status != state.Status {
		t.Fatalf("failed evaluation committed readiness=%+v/%+v", state, after)
	}
	var periods int
	if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_trade_warmup_periods WHERE rule_id=$1`, f.rule.ID).Scan(&periods); err != nil || periods != 0 {
		t.Fatalf("failed evaluation retained warmup period=%d/%v", periods, err)
	}
	// A status update must invalidate an older successful batch's CAS too.
	if err := f.db.MarkMarketTradeReadinessBlocked(ctx, state, "current data unavailable"); err != nil {
		t.Fatal(err)
	}
	err = f.db.WithMarketRuleEvaluationWrite(ctx, f.rule.ID, func(writer *store.Store) error {
		return writer.CommitMarketTradeReadiness(ctx, f.rule, f.project, f.target, state, actual.State, nil, "ready", "")
	})
	if !errors.Is(err, store.ErrMarketRuleEvaluationStale) {
		t.Fatalf("stale batch overwrote a newer blocked state=%v", err)
	}
}

func TestPostgresTradeReadinessNewDeploymentActivatesWithoutResettingOtherChain(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketLargeBuy)
	ctx := context.Background()
	var asset, bscAsset, bscProject int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO market_assets(canonical_name,symbol,identity_source,canonical_asset_id,verification_status)
 VALUES('Local multichain','LOCAL','local_fixture','readiness-multichain','verified') RETURNING id`).Scan(&asset); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO market_asset_deployments(market_asset_id,chain_key,chain_id,token_address,token_decimals,verification_status,verification_source)
 VALUES($1,'bsc',56,$2,18,'verified','local_fixture') RETURNING id`, asset, tradeFactsTestToken).Scan(&bscAsset); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE market_projects SET market_asset_id=$2 WHERE id=$1`, f.project.ID, asset); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO market_project_deployments(market_project_id,market_asset_id,market_asset_deployment_id)
 VALUES($1,$2,$3) RETURNING id`, f.project.ID, asset, bscAsset).Scan(&bscProject); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.LinkMarketProjectPoolWithinQuota(ctx, store.LinkMarketProjectPoolParams{DeBoxUserID: f.rule.DeBoxUserID, MarketProjectID: f.project.ID, MarketPoolID: f.poolIDs[0], Selected: true, IsPrimary: true}, f.policy); err != nil {
		t.Fatal(err)
	}
	targets, err := f.db.ListMarketRuleTargets(ctx, f.rule, f.project)
	if err != nil {
		t.Fatal(err)
	}
	merged := MergeTradeRuleTargets(targets)
	if len(merged) != 1 {
		t.Fatal(merged)
	}
	f.target = merged[0]
	at := seedTradeLifecycleProgress(t, f)
	var baseAsset, baseProject int64
	var created time.Time
	baseToken := "0x" + strings.Repeat("3", 40)
	if err := f.pool.QueryRow(ctx, `INSERT INTO market_asset_deployments(market_asset_id,chain_key,chain_id,token_address,token_decimals,verification_status,verification_source)
 VALUES($1,'base',8453,$2,18,'verified','local_fixture') RETURNING id`, asset, baseToken).Scan(&baseAsset); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO market_project_deployments(market_project_id,market_asset_id,market_asset_deployment_id)
 VALUES($1,$2,$3) RETURNING id,created_at`, f.project.ID, asset, baseAsset).Scan(&baseProject, &created); err != nil {
		t.Fatal(err)
	}
	address := "0x" + strings.Repeat("e", 40)
	p, err := f.db.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{ChainKey: "base", ChainID: 8453, Protocol: "uniswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address,
		Token0Address: baseToken, Token1Address: "0x" + strings.Repeat("2", 40), Token0Decimals: 18, Token1Decimals: 18, Token0DecimalsVerified: true, Token1DecimalsVerified: true, SupportsEventParsing: true, ParserAdapter: "uniswap_v2", VerificationStatus: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.LinkMarketProjectPoolWithinQuota(ctx, store.LinkMarketProjectPoolParams{DeBoxUserID: f.rule.DeBoxUserID, MarketProjectID: f.project.ID, MarketPoolID: p.ID, Selected: true, IsPrimary: true}, f.policy); err != nil {
		t.Fatal(err)
	}
	if n, err := f.service.evaluateRule(ctx, f.rule); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	assertTradeLifecycleLane(t, f, 123, &at)
	states, err := f.db.ListMarketProjectTradeReadiness(ctx, f.project.ID, f.rule.DeBoxUserID)
	if err != nil || len(states) != 2 {
		t.Fatal(states, err)
	}
	var sourceID int64
	var eligibility time.Time
	if err := f.pool.QueryRow(ctx, `SELECT (lane.state->>'last_trade_ready_id')::bigint,r.eligibility_after FROM market_rule_target_states lane
 JOIN market_trade_rule_readiness r ON r.rule_id=lane.market_rule_id AND r.target_key=lane.target_key WHERE lane.market_rule_id=$1 AND lane.market_project_deployment_id=$2`, f.rule.ID, baseProject).Scan(&sourceID, &eligibility); err != nil || sourceID != 0 || eligibility.Before(created) {
		t.Fatalf("new chain guessed progress or historical eligibility=%d/%v/%v", sourceID, eligibility, err)
	}
}

func TestPostgresTradeReadinessOppositeDirectionDoesNotRequireUnrelatedOldWindow(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketConsecutiveLargeBuy)
	ctx := context.Background()
	base := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	publishReadinessTrade(t, f, 50, base.Add(time.Second), "buy", "pending")
	reverse := publishReadinessTrade(t, f, 100, base.Add(10*time.Second), "sell")
	first := publishReadinessTrade(t, f, 190, base.Add(5*time.Minute+30*time.Second))
	second := publishReadinessTrade(t, f, 200, base.Add(6*time.Minute))
	after := base.Add(29 * time.Second)
	afterHash := fmt.Sprintf("0x%064x", 10110)
	if _, err := f.db.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "readiness_fixture", BlockNumber: 110, BlockHash: afterHash, ParentHash: fmt.Sprintf("0x%064x", 10109), BlockTimestamp: &after}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecordMarketScanCoverage(ctx, store.RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "readiness_fixture", ParserVersion: 1, ScopePoolIDs: []int64{f.poolIDs[0]}, ScopeTokenAddresses: []string{tradeFactsTestToken}, FromBlock: 111, ThroughBlock: *second.BlockNumber, AfterAt: after, ThroughAt: second.OccurredAt, AfterHash: afterHash, ThroughHash: *second.BlockHash}); err != nil {
		t.Fatal(err)
	}
	f.service.now = func() time.Time { return second.OccurredAt.Add(time.Second) }
	if n, err := f.service.evaluateRule(ctx, f.rule); err != nil || n != 1 {
		t.Fatalf("irrelevant reverse window blocked fresh complete buys=%d/%v", n, err)
	}
	lane, _, err := f.db.LoadMarketRuleTargetState(ctx, f.rule.ID, f.target)
	if err != nil {
		t.Fatal(err)
	}
	var state tradeLaneState
	if json.Unmarshal(lane.State, &state) != nil || state.LastTradeReadyID != second.TradeReadyID || reverse.TradeReadyID >= first.TradeReadyID {
		t.Fatalf("reverse changed count/cursor=%+v", state)
	}
	if readiness := readTradeReadiness(t, f); readiness.Status != "ready" {
		t.Fatalf("complete buy window status=%+v", readiness)
	}
	var count int64
	if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$2 AND notification_status<>'skipped'`, f.rule.ID, second.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("reverse reset cumulative buys or duplicated delivery=%d/%v", count, err)
	}
}

func TestPostgresTradeReadinessLateGapWithoutPriorWarmupCannotSwallowReadyInterval(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketConsecutiveLargeBuy)
	ctx := context.Background()
	base := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	publishReadinessTrade(t, f, 200, base.Add(6*time.Minute))
	latest := publishReadinessTrade(t, f, 300, base.Add(7*time.Minute))
	readinessFixtureCoverage(t, f, base, latest)
	f.service.now = func() time.Time { return latest.OccurredAt.Add(time.Second) }
	if n, err := f.service.evaluateRule(ctx, f.rule); err != nil || n != 1 {
		t.Fatalf("initial ready evaluations=%d/%v", n, err)
	}
	var periods int
	if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_trade_warmup_periods WHERE rule_id=$1`, f.rule.ID).Scan(&periods); err != nil || periods != 0 {
		t.Fatalf("fixture already had warmup periods=%d/%v", periods, err)
	}
	lateGap := publishReadinessTrade(t, f, 100, base.Add(10*time.Second))
	if n, err := f.service.evaluateRule(ctx, f.rule); err != nil || n != 0 {
		t.Fatalf("late gap result=%d/%v", n, err)
	}
	var from, through, end time.Time
	if err := f.pool.QueryRow(ctx, `SELECT from_at,through_at,ready_from_at FROM market_trade_warmup_periods WHERE rule_id=$1`, f.rule.ID).Scan(&from, &through, &end); err != nil || !from.Equal(lateGap.OccurredAt) || !through.Equal(from) || !end.Equal(from.Add(time.Microsecond)) {
		t.Fatalf("late gap inferred an unproven interval=%v/%v/%v/%v", from, through, end, err)
	}
	valid := publishReadinessTrade(t, f, 250, base.Add(6*time.Minute+30*time.Second))
	if n, err := f.service.evaluateRule(ctx, f.rule); err != nil || n != 0 {
		t.Fatalf("late healthy source result=%d/%v", n, err)
	}
	var reason string
	if err := f.pool.QueryRow(ctx, `SELECT notification_error FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$2`, f.rule.ID, valid.ID).Scan(&reason); err != nil || strings.Contains(reason, "warmup") {
		t.Fatalf("late healthy event swallowed by retroactive warmup=%q/%v", reason, err)
	}
	if state := readTradeReadiness(t, f); state.Status != "ready" {
		t.Fatalf("historical gap moved current status backward=%+v", state)
	}
}

func TestPostgresTradeReadinessScopeBoundaryMapPrunesDeselectedPools(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketLargeBuy)
	ctx := context.Background()
	change := func(index int, selected bool) {
		t.Helper()
		if _, err := f.db.LinkMarketProjectPoolWithinQuota(ctx, store.LinkMarketProjectPoolParams{DeBoxUserID: f.rule.DeBoxUserID, MarketProjectID: f.project.ID, MarketPoolID: f.poolIDs[index], Selected: selected}, f.policy); err != nil {
			t.Fatal(err)
		}
	}
	change(1, true)
	change(0, false)
	var keys int
	var removed, kept bool
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM jsonb_object_keys(pool_boundaries)),pool_boundaries ? $2,pool_boundaries ? $3
 FROM market_project_trade_scope_changes WHERE project_id=$1`, f.project.ID, fmt.Sprint(f.poolIDs[0]), fmt.Sprint(f.poolIDs[1])).Scan(&keys, &removed, &kept); err != nil || keys != 1 || removed || !kept {
		t.Fatalf("scope provenance retained an unbounded past selection history=%d/%t/%t/%v", keys, removed, kept, err)
	}
	change(0, true)
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM jsonb_object_keys(pool_boundaries)),pool_boundaries ? $2
 FROM market_project_trade_scope_changes WHERE project_id=$1`, f.project.ID, fmt.Sprint(f.poolIDs[0])).Scan(&keys, &kept); err != nil || keys != 2 || !kept {
		t.Fatalf("reselection lost new truthful boundary=%d/%t/%v", keys, kept, err)
	}
}
