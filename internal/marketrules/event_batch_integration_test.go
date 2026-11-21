package marketrules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func marketEventBatchFixture(t *testing.T, trade bool, count int) (*Service, *store.Store, *pgxpool.Pool, store.MarketRule, store.MarketRuleTarget, []store.MarketEvent) {
	t.Helper()
	ctx := context.Background()
	database, pool := openTradeLaneIntegrationStore(t)
	const owner = "bounded-event-prefix-owner"
	if _, err := database.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	policy := store.QuotaPolicy{PlanCode: plans.Professional, RuleLimit: 20, MarketProjectLimit: 5, MultiPoolMonitoring: true, AllowedMarketRuleTypes: []string{plans.MarketLargeBuy}}
	project, err := database.CreateMarketProjectWithinQuota(ctx, store.CreateMarketProjectParams{DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken, TokenDecimals: 18}, policy)
	if err != nil {
		t.Fatal(err)
	}
	quote, address := "0x"+strings.Repeat("2", 40), "0x"+strings.Repeat("a", 40)
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
		CooldownSeconds: 0, CooldownScope: "chain", NotificationChatID: owner, DeliveryMode: "realtime", DeploymentScope: "all", PoolScope: "all"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	projectPointer, err := database.GetMarketProject(ctx, project.ID, owner)
	if err != nil || projectPointer == nil {
		t.Fatal(projectPointer, err)
	}
	targets, err := database.ListMarketRuleTargets(ctx, rule, *projectPointer)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	service := New(Dependencies{Repository: database}, Settings{TradeFactsEnabled: trade})
	now := time.Now().UTC().Truncate(time.Microsecond)
	service.now = func() time.Time { return now }
	target := targets[0]
	if trade {
		target = MergeTradeRuleTargets(targets)[0]
		if _, err := service.InitializeTradeRuleState(ctx, rule.ID, target, 0); err != nil {
			t.Fatal(err)
		}
	} else if err := database.UpdateMarketRuleTargetState(ctx, rule.ID, target, []byte(`{"last_event_id":0}`), false); err != nil {
		t.Fatal(err)
	}
	var events []store.MarketEvent
	for i := 0; i < count; i++ {
		at := now.Add(time.Duration(i-count) * time.Second)
		block := int64(100 + i)
		hash, blockHash, parentHash := fmt.Sprintf("0x%064x", block), fmt.Sprintf("0x%064x", block+10000), fmt.Sprintf("0x%064x", block+9999)
		if !trade {
			event, _, err := database.CreateMarketEvent(ctx, store.CreateMarketEventParams{MarketPoolID: &marketPool.ID, ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken,
				EventType: "buy", EventKey: fmt.Sprintf("legacy-prefix-%d", i), TokenAmount: stringPointer("10"), Confirmed: true, OccurredAt: at, Source: "local_prefix_test"})
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
			continue
		}
		if _, err := database.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "local_prefix_test", BlockNumber: block, BlockHash: blockHash, ParentHash: parentHash, BlockTimestamp: &at}); err != nil {
			t.Fatal(err)
		}
		receipt, _ := json.Marshal(map[string]any{"transactionHash": hash, "blockHash": blockHash, "blockNumber": fmt.Sprintf("0x%x", block), "transactionIndex": "0x0", "status": "0x1", "logs": []map[string]string{{"logIndex": "0x1"}}})
		parents, err := database.PersistMarketTransaction(ctx, store.MarketTransactionParams{ChainKey: "bsc", ChainID: 56, TransactionHash: hash, BlockNumber: block, BlockHash: blockHash, OccurredAt: at, Confirmed: true, ParserVersion: 1,
			ScopePoolIDs: []int64{marketPool.ID}, ScopeTokenAddresses: []string{tradeFactsTestToken}, ReceiptPayload: receipt,
			Events: []store.MarketTransactionEvent{{Event: store.CreateMarketEventParams{TokenAddress: tradeFactsTestToken, EventType: "buy", EventKey: fmt.Sprintf("prefix-%d", i), Source: "local_prefix_test", TokenAmountRaw: stringPointer("10000000000000000000"), QuoteAmountRaw: stringPointer("1000000000000000000"), TokenAmount: stringPointer("10"), QuoteAmount: stringPointer("1")},
				Legs: []store.MarketTradeLegParams{{PoolID: marketPool.ID, LogIndex: 1, QuoteAddress: quote, TokenAmountRaw: "10000000000000000000", QuoteAmountRaw: "1000000000000000000", TokenAmount: stringPointer("10"), QuoteAmount: stringPointer("1")}}}}})
		if err != nil || len(parents) != 1 {
			t.Fatal(parents, err)
		}
		events = append(events, parents[0])
	}
	return service, database, pool, rule, target, events
}

func TestPostgresMarketEventPagesCommitOnlySuccessfulPrefixesAndResume(t *testing.T) {
	for _, trade := range []bool{false, true} {
		t.Run(fmt.Sprintf("trade_facts_%t", trade), func(t *testing.T) {
			service, _, pool, rule, target, events := marketEventBatchFixture(t, trade, 45)
			ctx := context.Background()
			if created, err := service.evaluateRule(ctx, rule); err != nil || created != 16 {
				t.Fatalf("first bounded prefix: %d/%v", created, err)
			}
			stateKey := "last_event_id"
			readyAt := func(index int) int64 { return events[index].ID }
			if trade {
				stateKey = "last_trade_ready_id"
				readyAt = func(index int) int64 { return events[index].TradeReadyID }
			}
			assertPrefix := func(wantCount, lastIndex int) {
				t.Helper()
				var count int
				var cursor int64
				if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1),(state->>$3)::bigint FROM market_rule_target_states WHERE market_rule_id=$1 AND target_key=$2`, rule.ID, target.TargetKey, stateKey).Scan(&count, &cursor); err != nil || count != wantCount || cursor != readyAt(lastIndex) {
					t.Fatalf("committed prefix count/cursor=%d/%d want=%d/%d err=%v", count, cursor, wantCount, readyAt(lastIndex), err)
				}
			}
			assertPrefix(16, 15)
			// Fail after the first few writes of the second page. Its source,
			// notification links and watermark must all roll back together.
			if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION reject_prefix_source() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.market_event_id=%d THEN RAISE EXCEPTION 'local prefix failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_prefix_source BEFORE INSERT ON market_rule_events FOR EACH ROW EXECUTE FUNCTION reject_prefix_source()`, events[20].ID)); err != nil {
				t.Fatal(err)
			}
			if _, err := service.evaluateRule(ctx, rule); err == nil {
				t.Fatal("injected page failure was ignored")
			}
			assertPrefix(16, 15)
			if _, err := pool.Exec(ctx, "DROP TRIGGER reject_prefix_source ON market_rule_events"); err != nil {
				t.Fatal(err)
			}
			if created, err := service.evaluateRule(ctx, rule); err != nil || created != 16 {
				t.Fatalf("retry did not resume failed prefix: %d/%v", created, err)
			}
			assertPrefix(32, 31)
			if created, err := service.evaluateRule(ctx, rule); err != nil || created != 13 {
				t.Fatalf("last prefix: %d/%v", created, err)
			}
			assertPrefix(45, 44)
			if created, err := service.evaluateRule(ctx, rule); err != nil || created != 0 {
				t.Fatalf("completed page replayed sources: %d/%v", created, err)
			}
			assertPrefix(45, 44)
		})
	}
}

func TestPostgresSmallTradePagesKeepTheCompleteCumulativeWindow(t *testing.T) {
	service, database, pool, rule, target, events := marketEventBatchFixture(t, true, 45)
	ctx := context.Background()
	window := int32(1)
	rule.RuleType, rule.WindowMinutes, rule.TriggerCountThreshold = plans.MarketConsecutiveLargeBuy, &window, 20
	if _, err := pool.Exec(ctx, `UPDATE market_rules SET rule_type=$2,window_minutes=$3,trigger_count_threshold=$4 WHERE id=$1`, rule.ID, rule.RuleType, window, rule.TriggerCountThreshold); err != nil {
		t.Fatal(err)
	}
	after := service.now().Add(-2 * time.Minute)
	afterHash := fmt.Sprintf("0x%064x", int64(10099))
	if _, err := database.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "local_prefix_test", BlockNumber: 99, BlockHash: afterHash, ParentHash: fmt.Sprintf("0x%064x", int64(10098)), BlockTimestamp: &after}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordMarketScanCoverage(ctx, store.RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "local_prefix_test", ParserVersion: 1,
		ScopePoolIDs: target.TradePoolIDs, ScopeTokenAddresses: []string{tradeFactsTestToken}, FromBlock: 100, ThroughBlock: 144,
		AfterAt: after, ThroughAt: events[44].OccurredAt, AfterHash: afterHash, ThroughHash: fmt.Sprintf("0x%064x", int64(10144))}); err != nil {
		t.Fatal(err)
	}
	for index, want := range []int64{0, 13, 13, 0} {
		if created, err := service.evaluateRule(ctx, rule); err != nil || created != want {
			t.Fatalf("cumulative page %d: created=%d want=%d err=%v", index, created, want, err)
		}
	}
	var count, accepted int64
	if err := pool.QueryRow(ctx, `SELECT (details#>>'{trade_window,count}')::bigint,(SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1 AND notification_status='pending') FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$2`, rule.ID, events[44].ID).Scan(&count, &accepted); err != nil || count != 45 || accepted != 26 {
		t.Fatalf("current-page cap truncated historical counting: last_count=%d accepted=%d err=%v", count, accepted, err)
	}
}

func TestPostgresSmallEventPagesPreserveCooldownAcrossCommittedPrefixes(t *testing.T) {
	for _, trade := range []bool{false, true} {
		t.Run(fmt.Sprintf("trade_facts_%t", trade), func(t *testing.T) {
			service, _, pool, rule, _, _ := marketEventBatchFixture(t, trade, 35)
			ctx := context.Background()
			rule.CooldownSeconds = 600
			if _, err := pool.Exec(ctx, `UPDATE market_rules SET cooldown_seconds=600 WHERE id=$1`, rule.ID); err != nil {
				t.Fatal(err)
			}
			for index, want := range []int64{1, 0, 0, 0} {
				if created, err := service.evaluateRule(ctx, rule); err != nil || created != want {
					t.Fatalf("cooldown page %d created=%d want=%d err=%v", index, created, want, err)
				}
			}
			var pending, cooled int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE notification_status='pending'),COUNT(*) FILTER(WHERE notification_status='skipped' AND notification_error='cooldown_active') FROM market_rule_events WHERE market_rule_id=$1`, rule.ID).Scan(&pending, &cooled); err != nil || pending != 1 || cooled != 34 {
				t.Fatalf("private/persisted cooldown changed at page boundary: pending=%d cooled=%d err=%v", pending, cooled, err)
			}
		})
	}
}
