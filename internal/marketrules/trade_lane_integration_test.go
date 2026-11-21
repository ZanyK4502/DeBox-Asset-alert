package marketrules

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openTradeLaneIntegrationStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	if os.Getenv("RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("set RUN_POSTGRES_INTEGRATION=1 for isolated local trade-lane integration tests")
	}
	config, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil || os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("an explicit local TEST_DATABASE_URL is required")
	}
	host := config.ConnConfig.Host
	if host != "localhost" && !net.ParseIP(host).IsLoopback() && !filepath.IsAbs(host) {
		t.Fatal("trade-lane integration tests require a local PostgreSQL endpoint")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatalf("open local test admin: %v", err)
	}
	schema := fmt.Sprintf("trade_lane_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatalf("create isolated trade schema: %v", err)
	}
	var pool *pgxpool.Pool
	var database *store.Store
	t.Cleanup(func() {
		if database != nil {
			database.Close()
		}
		if pool != nil {
			pool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop isolated trade schema: %v", err)
		}
		admin.Close()
	})
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated trade pool: %v", err)
	}
	isolatedURL, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse isolated trade URL: %v", err)
	}
	query := isolatedURL.Query()
	query.Set("search_path", schema)
	isolatedURL.RawQuery = query.Encode()
	database, err = store.Open(ctx, isolatedURL.String())
	if err != nil {
		t.Fatalf("open isolated trade store: %v", err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate isolated trade schema: %v", err)
	}
	return database, pool
}

func TestPostgresTradeLaneFreezesSelectedPoolsThroughDeliveryLoading(t *testing.T) {
	for _, mode := range []string{"realtime", "stage"} {
		t.Run(mode, func(t *testing.T) {
			database, pool := openTradeLaneIntegrationStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			const owner = "trade-lane-integration-owner"
			if _, err := database.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
				t.Fatal(err)
			}
			policy := store.QuotaPolicy{PlanCode: plans.Professional, RuleLimit: 20, MarketProjectLimit: 5,
				MultiPoolMonitoring: true, StageNotifications: true, AllowedMarketRuleTypes: []string{plans.MarketLargeBuy}}
			project, err := database.CreateMarketProjectWithinQuota(ctx, store.CreateMarketProjectParams{
				DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken,
				TokenName: "Trade Fixture", TokenSymbol: "TEST", TokenDecimals: 6,
			}, policy)
			if err != nil {
				t.Fatal(err)
			}
			quote := "0x2222222222222222222222222222222222222222"
			var poolIDs []int64
			for index, hexDigit := range []string{"a", "b", "c"} {
				address := "0x" + strings.Repeat(hexDigit, 40)
				marketPool, err := database.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
					ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address,
					Token0Address: tradeFactsTestToken, Token0Decimals: 6, Token0DecimalsVerified: true,
					Token1Address: quote, Token1Decimals: 18, Token1DecimalsVerified: true,
					LiquidityUSD: "1000000", SupportsEventParsing: true, ParserAdapter: marketparse.AdapterV2,
					VerificationStatus: "verified", SeenAt: now,
				})
				if err != nil {
					t.Fatal(err)
				}
				poolIDs = append(poolIDs, marketPool.ID)
				if _, err := database.LinkMarketProjectPoolWithinQuota(ctx, store.LinkMarketProjectPoolParams{
					DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: marketPool.ID,
					Selected: index < 2, IsPrimary: index == 0, DiscoverySource: "local_test",
				}, policy); err != nil {
					t.Fatal(err)
				}
				if _, err := database.CreateMarketSnapshot(ctx, store.CreateMarketSnapshotParams{
					ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken, MarketPoolID: marketPool.ID,
					PriceUSD: stringPointer("1000"), LiquidityUSD: stringPointer("1000000"), Source: "local_test", CapturedAt: now, QuoteQuality: liveQuoteFixture(now),
				}); err != nil {
					t.Fatal(err)
				}
			}
			rule, err := database.CreateMarketRuleWithinQuota(ctx, store.CreateMarketRuleParams{
				DeBoxUserID: owner, MarketProjectID: project.ID, RuleType: plans.MarketLargeBuy,
				ThresholdUnit: "usd", ThresholdValue: "10000", PoolScope: "all", DeploymentScope: "all", CooldownScope: "chain",
				NotificationChatID: owner, DeliveryMode: mode, CycleType: "fixed", CycleMinutes: 60, TriggerCountThreshold: 1,
			}, policy)
			if err != nil {
				t.Fatal(err)
			}
			projectPointer, err := database.GetMarketProject(ctx, project.ID, owner)
			if err != nil {
				t.Fatal(err)
			}
			legacyTargets, err := database.ListMarketRuleTargets(ctx, rule, *projectPointer)
			if err != nil {
				t.Fatal(err)
			}
			targets := MergeTradeRuleTargets(legacyTargets)
			if len(targets) != 1 || len(targets[0].TradePoolIDs) != 2 {
				t.Fatalf("selected-pool scope = %#v", targets)
			}
			service := New(Dependencies{Repository: database}, Settings{TradeFactsEnabled: true})
			if _, err := service.InitializeTradeRuleState(ctx, rule.ID, targets[0], 0); err != nil {
				t.Fatal(err)
			}
			hash, blockHash, parentHash := "0x"+strings.Repeat("d", 64), "0x"+strings.Repeat("e", 64), "0x"+strings.Repeat("f", 64)
			if _, err := database.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{
				ChainKey: "bsc", ChainID: 56, CursorKey: "local_test", BlockNumber: 100, BlockHash: blockHash, ParentHash: parentHash,
				BlockTimestamp: &now, ScannedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"transactionHash": hash, "blockHash": blockHash, "blockNumber": "0x64", "transactionIndex": "0x0",
				"logs": []map[string]string{{"logIndex": "0x1"}, {"logIndex": "0x2"}, {"logIndex": "0x3"}}})
			var legs []store.MarketTradeLegParams
			valuationProof, _ := json.Marshal(valuationProofFixture(now))
			for index, amount := range []string{"4", "8", "8"} {
				usd := "8000"
				if index == 0 {
					usd = "4000"
				}
				legs = append(legs, store.MarketTradeLegParams{PoolID: poolIDs[index], LogIndex: int64(index + 1), QuoteAddress: quote,
					TokenAmountRaw: amount + "000000", QuoteAmountRaw: "1", TokenAmount: stringPointer(amount),
					USDValue: stringPointer(usd), PriceUSD: stringPointer("1000"), Metadata: valuationProof})
			}
			parents, err := database.PersistMarketTransaction(ctx, store.MarketTransactionParams{
				ChainKey: "bsc", ChainID: 56, TransactionHash: hash, BlockNumber: 100, BlockHash: blockHash,
				OccurredAt: now, Confirmed: true, ParserVersion: 2, ScopePoolIDs: poolIDs, ScopeTokenAddresses: []string{tradeFactsTestToken},
				ReceiptPayload: payload, Events: []store.MarketTransactionEvent{{Event: store.CreateMarketEventParams{
					TokenAddress: tradeFactsTestToken, EventType: marketparse.EventBuy, EventKey: "local_test_trade",
					TokenAmountRaw: stringPointer("20000000"), TokenAmount: stringPointer("20"), USDValue: stringPointer("20000"), PriceUSD: stringPointer("1000"),
					Source: "local_test", Confidence: "1", Metadata: json.RawMessage(`{"protocol":"pancakeswap"}`),
				}, Legs: legs}},
			})
			if err != nil || len(parents) != 1 {
				t.Fatalf("atomic receipt = %#v, %v", parents, err)
			}
			created, err := service.evaluateRule(ctx, rule)
			if err != nil || created != 1 {
				t.Fatalf("real selected-pool lane created=%d, err=%v", created, err)
			}
			assertFrozenHistory := func() store.MarketRuleEventHistory {
				t.Helper()
				history, err := database.ListMarketRuleEventHistory(ctx, project.ID, owner, store.MarketEventFilter{Limit: 20})
				if err != nil || len(history) != 1 || history[0].TokenAmount == nil || *history[0].TokenAmount != "12" ||
					history[0].USDValue == nil || *history[0].USDValue != "12000" || history[0].MarketPoolID != nil ||
					!reflect.DeepEqual(history[0].TradePoolIDs, poolIDs[:2]) {
					t.Fatalf("history did not preserve selected-pool projection: %#v, %v", history, err)
				}
				for index, poolID := range poolIDs {
					filtered, err := database.ListMarketRuleEventHistory(ctx, project.ID, owner, store.MarketEventFilter{Limit: 20, MarketPoolID: poolID})
					want := 1
					if index == 2 {
						want = 0
					}
					if err != nil || len(filtered) != want {
						t.Fatalf("frozen pool history filter %d: rows=%d, want=%d, err=%v", poolID, len(filtered), want, err)
					}
				}
				return history[0]
			}
			historyItem := assertFrozenHistory()
			claims, err := database.ClaimMarketDeliveries(ctx, 10)
			if err != nil || len(claims) != 1 || claims[0].Kind != mode {
				t.Fatalf("claim selected-pool notification = %#v, %v", claims, err)
			}
			before, err := database.LoadMarketDelivery(ctx, claims[0])
			if err != nil {
				t.Fatal(err)
			}
			var beforeEvent store.MarketEvent
			var beforeSnapshot *store.MarketSnapshot
			if mode == "stage" {
				if len(before.StageEvents) != 1 {
					t.Fatalf("stage counted pool legs instead of one parent: %#v", before.StageEvents)
				}
				beforeEvent, beforeSnapshot = before.StageEvents[0].Event, before.StageEvents[0].Snapshot
			} else {
				beforeEvent, beforeSnapshot = *before.Event, before.Snapshot
			}
			if beforeEvent.TokenAmount == nil || *beforeEvent.TokenAmount != "12" || beforeEvent.USDValue == nil || *beforeEvent.USDValue != "12000" ||
				!reflect.DeepEqual(beforeEvent.TradePoolIDs, poolIDs[:2]) || beforeSnapshot == nil || beforeSnapshot.LiquidityUSD == nil || *beforeSnapshot.LiquidityUSD != "2000000" {
				t.Fatalf("delivery used global/unselected totals: %#v, %#v", beforeEvent, beforeSnapshot)
			}
			// Simulate later pool selection, quote changes, and a parent correction.
			if _, err := database.LinkMarketProjectPoolWithinQuota(ctx, store.LinkMarketProjectPoolParams{
				DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: poolIDs[2], Selected: true, IsPrimary: true,
			}, policy); err != nil {
				t.Fatal(err)
			}
			if _, err := database.CreateMarketSnapshot(ctx, store.CreateMarketSnapshotParams{
				ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken, MarketPoolID: poolIDs[0],
				PriceUSD: stringPointer("999999"), LiquidityUSD: stringPointer("1"), Source: "local_test", CapturedAt: now.Add(time.Minute), QuoteQuality: liveQuoteFixture(now.Add(time.Minute)),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "UPDATE market_events SET token_amount=999,usd_value=999999 WHERE id=$1", parents[0].ID); err != nil {
				t.Fatal(err)
			}
			after, err := database.LoadMarketDelivery(ctx, claims[0])
			if err != nil {
				t.Fatal(err)
			}
			if mode == "stage" {
				if !reflect.DeepEqual(before.StageEvents[0].Event, after.StageEvents[0].Event) || !reflect.DeepEqual(before.StageEvents[0].Snapshot, after.StageEvents[0].Snapshot) {
					t.Fatal("stage delivery recomputed a previously frozen trade")
				}
			} else if !reflect.DeepEqual(before.Event, after.Event) || !reflect.DeepEqual(before.Snapshot, after.Snapshot) {
				t.Fatal("realtime delivery recomputed a previously frozen trade")
			}
			assertFrozenHistory()
			// A recognized projection with missing amounts must remain unknown,
			// while an actual legacy record retains its original parent fallback.
			if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET details='{"trade_projection":{}}'::jsonb WHERE id=$1`, historyItem.ID); err != nil {
				t.Fatal(err)
			}
			missing, err := database.ListMarketRuleEventHistory(ctx, project.ID, owner, store.MarketEventFilter{Limit: 20})
			if err != nil || len(missing) != 1 || missing[0].TokenAmount != nil || missing[0].USDValue != nil || missing[0].MarketPoolID != nil {
				t.Fatalf("missing frozen fields fell back to global totals: %#v, %v", missing, err)
			}
			if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET details='{}'::jsonb WHERE id=$1`, historyItem.ID); err != nil {
				t.Fatal(err)
			}
			legacy, err := database.ListMarketRuleEventHistory(ctx, project.ID, owner, store.MarketEventFilter{Limit: 20})
			if err != nil || len(legacy) != 1 || legacy[0].TokenAmount == nil || *legacy[0].TokenAmount != "999" ||
				legacy[0].USDValue == nil || *legacy[0].USDValue != "999999" || len(legacy[0].TradePoolIDs) != 0 {
				t.Fatalf("legacy history behavior changed: %#v, %v", legacy, err)
			}
		})
	}
}
