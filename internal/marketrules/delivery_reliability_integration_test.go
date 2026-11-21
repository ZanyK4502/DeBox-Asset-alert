package marketrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestPostgresLatePreCutoffTradeReadyAuditsWithoutConsumingFreshCooldown(t *testing.T) {
	runLatePreCutoffTradeCooldown(t, false, false)
}
func TestPostgresMixedPageOldTradeCannotSuppressFreshTradeCooldown(t *testing.T) {
	runLatePreCutoffTradeCooldown(t, true, false)
}
func TestPostgresOldCumulativeReadyConsumesWithoutDiscardedWindowCoverage(t *testing.T) {
	runLatePreCutoffTradeCooldown(t, false, true)
}
func TestPostgresReplayedSentParentDoesNotConsumeFreshSamePageCooldown(t *testing.T) {
	runLatePreCutoffTradeCooldown(t, false, false, true)
}
func runLatePreCutoffTradeCooldown(t *testing.T, mixed bool, cumulativeOldOnly bool, replay ...bool) {
	database, pool := openTradeLaneIntegrationStore(t)
	ctx := context.Background()
	const owner = "late-source-cooldown-owner"
	if _, err := database.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	policy := store.QuotaPolicy{PlanCode: plans.Professional, RuleLimit: 20, MarketProjectLimit: 5, MultiPoolMonitoring: true, AllowedMarketRuleTypes: []string{plans.MarketLargeBuy}}
	project, err := database.CreateMarketProjectWithinQuota(ctx, store.CreateMarketProjectParams{DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken, TokenDecimals: 18, TokenSymbol: "TEST"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	quote := "0x" + strings.Repeat("2", 40)
	address := "0x" + strings.Repeat("a", 40)
	marketPool, err := database.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address,
		Token0Address: tradeFactsTestToken, Token1Address: quote, Token0Decimals: 18, Token1Decimals: 18, Token0DecimalsVerified: true, Token1DecimalsVerified: true,
		SupportsEventParsing: true, ParserAdapter: "pancake_v2", VerificationStatus: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.LinkMarketProjectPoolWithinQuota(ctx, store.LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: marketPool.ID, Selected: true, IsPrimary: true}, policy); err != nil {
		t.Fatal(err)
	}
	rule, err := database.CreateMarketRuleWithinQuota(ctx, store.CreateMarketRuleParams{DeBoxUserID: owner, MarketProjectID: project.ID, RuleType: plans.MarketLargeBuy, ThresholdValue: "1", ThresholdUnit: "token",
		CooldownSeconds: 3600, CooldownScope: "chain", NotificationChatID: owner, DeliveryMode: "realtime", DeploymentScope: "all", PoolScope: "all"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if cumulativeOldOnly {
		minutes := int32(5)
		rule.RuleType = plans.MarketConsecutiveLargeBuy
		rule.WindowMinutes = &minutes
		if _, err := pool.Exec(ctx, `UPDATE market_rules SET rule_type=$2,window_minutes=$3 WHERE id=$1`, rule.ID, rule.RuleType, minutes); err != nil {
			t.Fatal(err)
		}
	}
	projectPointer, err := database.GetMarketProject(ctx, project.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	oldTargets, err := database.ListMarketRuleTargets(ctx, rule, *projectPointer)
	if err != nil {
		t.Fatal(err)
	}
	targets := MergeTradeRuleTargets(oldTargets)
	if len(targets) != 1 {
		t.Fatal(targets)
	}
	service := New(Dependencies{Repository: database}, Settings{TradeFactsEnabled: true})
	if _, err := service.InitializeTradeRuleState(ctx, rule.ID, targets[0], 0); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := database.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "local late-source fixture"); err != nil {
		t.Fatal(err)
	}
	publish := func(block int64, at time.Time, revision ...int64) store.MarketEvent {
		t.Helper()
		hash := fmt.Sprintf("0x%064x", block)
		salt := int64(0)
		if len(revision) > 0 {
			salt = revision[0]
		}
		blockHash := fmt.Sprintf("0x%064x", block+10000+salt)
		parentHash := fmt.Sprintf("0x%064x", block+9999)
		if _, err := database.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "local_test", BlockNumber: block, BlockHash: blockHash, ParentHash: parentHash, BlockTimestamp: &at}); err != nil {
			t.Fatal(err)
		}
		receipt, _ := json.Marshal(map[string]any{"transactionHash": hash, "blockHash": blockHash, "blockNumber": fmt.Sprintf("0x%x", block), "transactionIndex": "0x0", "status": "0x1", "logs": []map[string]string{{"logIndex": "0x1"}}})
		parents, err := database.PersistMarketTransaction(ctx, store.MarketTransactionParams{ChainKey: "bsc", ChainID: 56, TransactionHash: hash, BlockNumber: block, BlockHash: blockHash, OccurredAt: at, Confirmed: true, ParserVersion: 2,
			ScopePoolIDs: []int64{marketPool.ID}, ScopeTokenAddresses: []string{tradeFactsTestToken}, ReceiptPayload: receipt,
			Events: []store.MarketTransactionEvent{{Event: store.CreateMarketEventParams{TokenAddress: tradeFactsTestToken, EventType: "buy", EventKey: fmt.Sprintf("trade-%d", block), Source: "local_test", TokenAmountRaw: stringPointer("10000000000000000000"), TokenAmount: stringPointer("10"), QuoteAmountRaw: stringPointer("1000000000000000000"), QuoteAmount: stringPointer("1")},
				Legs: []store.MarketTradeLegParams{{PoolID: marketPool.ID, LogIndex: 1, QuoteAddress: quote, TokenAmountRaw: "10000000000000000000", QuoteAmountRaw: "1000000000000000000", TokenAmount: stringPointer("10"), QuoteAmount: stringPointer("1")}}}}})
		if err != nil || len(parents) != 1 {
			t.Fatal(parents, err)
		}
		return parents[0]
	}
	staleCallback := false
	if err := service.withEvaluationWrite(ctx, rule.ID, nil, func(writer *Service) error { staleCallback = true; return nil }); !errors.Is(err, store.ErrMarketRuleEvaluationStale) || staleCallback {
		t.Fatalf("fence change was not revalidated: called=%t/%v", staleCallback, err)
	}
	if len(replay) > 0 && replay[0] {
		at := time.Now().UTC().Truncate(time.Microsecond)
		old := publish(100, at)
		if n, err := service.evaluateRule(ctx, rule); err != nil || n != 1 {
			t.Fatal(n, err)
		}
		claims, err := database.ClaimMarketDeliveries(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatal(claims, err)
		}
		claim := claims[0]
		snapshot, err := database.CreateNotificationDetailSnapshot(ctx, store.CreateNotificationDetailSnapshotParams{SourceKey: fmt.Sprintf("market_realtime:%d", claim.ID), NotificationKind: store.NotificationKindMarketRealtime,
			SourceType: "market_notification_delivery", SourceID: &claim.ID, DeBoxUserID: owner, NotificationChatID: owner, NotificationChatType: "private", NotificationText: "original verified send", Details: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.PrepareMarketDelivery(ctx, claim, store.MarketDeliveryPayload{SnapshotID: snapshot.ID, Payload: notificationdelivery.Payload{ChatID: owner, ChatType: "private", Text: snapshot.NotificationText}}); err != nil {
			t.Fatal(err)
		}
		if err := database.BeginMarketDeliverySend(ctx, claim); err != nil {
			t.Fatal(err)
		}
		message := "verified-original"
		if err := database.CompleteMarketDeliveryResult(ctx, claim, &message, nil); err != nil {
			t.Fatal(err)
		}
		newHash := fmt.Sprintf("0x%064x", int64(10101))
		if _, err := database.MarkMarketBlockReorged(ctx, 56, 100, newHash); err != nil {
			t.Fatal(err)
		}
		publish(100, at, 1)
		fresh := publish(101, time.Now().UTC().Truncate(time.Microsecond))
		current, err := database.GetMarketRule(ctx, rule.ID, owner)
		if err != nil {
			t.Fatal(err)
		}
		service.now = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }
		if n, err := service.evaluateRule(ctx, *current); err != nil || n != 1 {
			t.Fatalf("sent replay occupied private cooldown before fresh source: %d/%v", n, err)
		}
		var oldSent, freshPending, unchanged bool
		var rows int
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$2 AND notification_status='sent' AND notification_message_id='verified-original'),
   EXISTS(SELECT 1 FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$3 AND notification_status='pending'),
   EXISTS(SELECT 1 FROM notification_detail_snapshots WHERE id=$4 AND notification_text='original verified send'),(SELECT COUNT(*)FROM market_rule_events WHERE market_rule_id=$1)`, rule.ID, old.ID, fresh.ID, snapshot.ID).Scan(&oldSent, &freshPending, &unchanged, &rows); err != nil || !oldSent || !freshPending || !unchanged || rows != 2 {
			t.Fatalf("logical replay changed evidence or lost fresh: %t/%t/%t/%d/%v", oldSent, freshPending, unchanged, rows, err)
		}
		return
	}
	old := publish(100, cutoff.Add(-time.Second))
	if mixed {
		fresh := publish(101, time.Now().UTC().Truncate(time.Microsecond))
		if created, err := service.evaluateRule(ctx, rule); err != nil || created != 1 {
			t.Fatalf("old and fresh in one page lost fresh alert: %d/%v", created, err)
		}
		var oldAudited, freshPending, triggered bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$2 AND notification_status='skipped'),
   EXISTS(SELECT 1 FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$3 AND notification_status='pending'),last_triggered_at IS NOT NULL
   FROM market_rules WHERE id=$1`, rule.ID, old.ID, fresh.ID).Scan(&oldAudited, &freshPending, &triggered); err != nil || !oldAudited || !freshPending || !triggered {
			t.Fatalf("mixed page eligibility: old=%t fresh=%t cooldown=%t err=%v", oldAudited, freshPending, triggered, err)
		}
		return
	}
	if created, err := service.evaluateRule(ctx, rule); err != nil || created != 0 {
		t.Fatalf("late old source consumed a trigger: %d/%v", created, err)
	}
	var clean bool
	var cursor int64
	if err := pool.QueryRow(ctx, `SELECT mr.last_triggered_at IS NULL AND s.last_triggered_at IS NULL,(s.state->>'last_trade_ready_id')::bigint
  FROM market_rules mr JOIN market_rule_target_states s ON s.market_rule_id=mr.id WHERE mr.id=$1 AND s.target_key=$2`, rule.ID, targets[0].TargetKey).Scan(&clean, &cursor); err != nil || !clean || cursor < 1 {
		t.Fatalf("old source advanced cooldown or failed to progress state: %t/%d/%v", clean, cursor, err)
	}
	var audited bool
	if err := pool.QueryRow(ctx, `SELECT notification_status='skipped' AND delivery_outcome='cancelled' FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$2`, rule.ID, old.ID).Scan(&audited); err != nil || !audited {
		t.Fatal(audited, err)
	}
	if cumulativeOldOnly {
		return
	}
	fresh := publish(101, time.Now().UTC().Truncate(time.Microsecond))
	current, err := database.GetMarketRule(ctx, rule.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := service.evaluateRule(ctx, *current); err != nil || created != 1 {
		t.Fatalf("old source suppressed the fresh event: %d/%v", created, err)
	}
	var freshPending bool
	if err := pool.QueryRow(ctx, `SELECT notification_status='pending' AND delivery_outcome='not_attempted' FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$2`, rule.ID, fresh.ID).Scan(&freshPending); err != nil || !freshPending {
		t.Fatal(freshPending, err)
	}
	claims, err := database.ClaimMarketDeliveries(ctx, 200)
	if err != nil || len(claims) != 1 {
		t.Fatalf("unexpected deliverable sources: %#v/%v", claims, err)
	}
}
