package marketcollector

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

type localTradeNotifier struct{ messages []string }

func (n *localTradeNotifier) SendNotification(_, _, text string) (string, error) {
	n.messages = append(n.messages, text)
	return fmt.Sprintf("local-%d", len(n.messages)), nil
}

func localTradeStore(t *testing.T) *store.Store {
	t.Helper()
	if os.Getenv("RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("requires explicitly configured isolated local PostgreSQL")
	}
	configured := os.Getenv("TEST_DATABASE_URL")
	cfg, err := pgxpool.ParseConfig(configured)
	if err != nil {
		t.Fatal(err)
	}
	host := cfg.ConnConfig.Host
	if host != "localhost" && !net.ParseIP(host).IsLoopback() && !filepath.IsAbs(host) {
		t.Fatal("trade acceptance requires a local PostgreSQL connection")
	}
	parsed, err := url.Parse(configured)
	if err != nil {
		t.Fatal(err)
	}
	// pgx uses the private socket in ?host=. A localhost authority also keeps
	// the existing acceptance fixture's local-host validation intact.
	if parsed.Hostname() == "" && filepath.IsAbs(host) {
		parsed.Host = "localhost"
	}
	t.Setenv("TEST_DATABASE_URL", parsed.String())
	t.Setenv("RUN_ACCEPTANCE", "1")
	return testdb.Open(t)
}

func TestPostgresCollectorToSelectedPoolRulesAndFrozenDelivery(t *testing.T) {
	database := localTradeStore(t)
	ctx := context.Background()
	eventTime := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	const user = "local-trade-scope-owner"
	const token = "0x1111111111111111111111111111111111111111"
	const quote = "0x2222222222222222222222222222222222222222"
	if _, err := database.ActivateSubscription(ctx, user, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	policy := store.QuotaPolicy{PlanCode: plans.Professional, MarketProjectLimit: 10, RuleLimit: 100,
		AllowedMarketRuleTypes: []string{plans.MarketLargeBuy}, MultiPoolMonitoring: true, StageNotifications: true}
	project, err := database.CreateMarketProjectWithinQuota(ctx, store.CreateMarketProjectParams{
		DeBoxUserID: user, ChainKey: "bsc", ChainID: 56, TokenAddress: token, TokenName: "Local fixture", TokenSymbol: "FIX", TokenDecimals: 0,
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	for index, address := range []string{"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		pool, err := database.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
			ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address,
			Token0Address: quote, Token1Address: token, Token0Decimals: 0, Token1Decimals: 0,
			Token0DecimalsVerified: true, Token1DecimalsVerified: true,
			LiquidityUSD: "1000000", SupportsEventParsing: true, ParserAdapter: "evm_v2", VerificationStatus: "verified",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.LinkMarketProjectPool(ctx, store.LinkMarketProjectPoolParams{
			DeBoxUserID: user, MarketProjectID: project.ID, MarketPoolID: pool.ID, Selected: true, IsPrimary: index == 0,
		}); err != nil {
			t.Fatal(err)
		}
		price, liquidity := "1", "1000000"
		quoteAt := eventTime.Add(-30 * time.Second)
		if _, err := database.CreateMarketSnapshot(ctx, store.CreateMarketSnapshotParams{
			MarketPoolID: pool.ID, ChainKey: "bsc", ChainID: 56, TokenAddress: token,
			PriceUSD: &price, LiquidityUSD: &liquidity, Source: "local_fixture", CapturedAt: quoteAt,
			QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &quoteAt},
		}); err != nil {
			t.Fatal(err)
		}
		// Today's/latest price differs by 100x. The older chain transaction
		// must use its then-available quote, while live percent denominators
		// still use the latest valid liquidity observation.
		currentAt, currentPrice := time.Now().UTC().Add(-10*time.Second), "100"
		if _, err := database.CreateMarketSnapshot(ctx, store.CreateMarketSnapshotParams{
			MarketPoolID: pool.ID, ChainKey: "bsc", ChainID: 56, TokenAddress: token,
			PriceUSD: &currentPrice, LiquidityUSD: &liquidity, Source: "local_fixture", CapturedAt: currentAt,
			QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &currentAt},
		}); err != nil {
			t.Fatal(err)
		}
	}
	notifier := &localTradeNotifier{}
	engine := marketrules.New(marketrules.Dependencies{Repository: database, Notifications: notifier, PublicAppURL: "https://example.invalid"},
		marketrules.Settings{Enabled: true, TradeFactsEnabled: true})
	ruleIDs := make(map[string]int64)
	for _, spec := range []struct{ name, scope, unit, threshold string }{
		{"single", "primary", "usd", "10000"},
		{"both", "all", "usd", "10000"},
		{"percent_no", "all", "percent", "1"},
		{"percent_yes", "all", "percent", "0.5"},
	} {
		rule, err := database.CreateMarketRuleWithinQuota(ctx, store.CreateMarketRuleParams{
			DeBoxUserID: user, MarketProjectID: project.ID, RuleType: plans.MarketLargeBuy,
			PoolScope: spec.scope, DeploymentScope: "all", CooldownScope: "chain",
			ThresholdUnit: spec.unit, ThresholdValue: spec.threshold, NotificationChatID: user, NotificationChatType: "private",
		}, policy)
		if err != nil {
			t.Fatal(err)
		}
		ruleIDs[spec.name] = rule.ID
		targets, err := database.ListMarketRuleTargets(ctx, rule, project)
		if err != nil {
			t.Fatal(err)
		}
		merged := marketrules.MergeTradeRuleTargets(targets)
		if len(merged) != 1 {
			t.Fatalf("one chain must resolve to one trade lane: %+v", merged)
		}
		if created, err := engine.InitializeTradeRuleState(ctx, rule.ID, merged[0], 0); err != nil || !created {
			t.Fatalf("explicit local activation = %v/%v", created, err)
		}
	}
	collector, _, client, parserCtx := receiptFactFixture(t)
	collector.repository = database
	// Read the real pool IDs and correct token precision through the production
	// resolver. The receipt itself remains an entirely local RPC fixture.
	targets, err := database.ListMarketProjectPools(ctx, project.ID, user)
	if err != nil || len(targets) != 2 {
		t.Fatalf("real selected pools = %v/%v", targets, err)
	}
	for id := range parserCtx.poolByID {
		delete(parserCtx.poolByID, id)
	}
	// Rebuild the parser using database identities, rather than the unit
	// fixture's deliberately unrelated IDs.
	pools := make([]marketparse.Pool, 0, len(targets))
	for _, pool := range targets {
		pools = append(pools, marketparse.Pool{ID: pool.ID, ChainID: 56, Protocol: pool.Protocol, Version: "v2", Adapter: "evm_v2",
			PoolKey: pool.PoolKey, LogAddress: *pool.PoolAddress,
			Token0: marketparse.Token{Address: quote}, Token1: marketparse.Token{Address: token}})
		parserCtx.poolByID[pool.ID] = store.MarketCollectionTarget{MarketPoolID: pool.ID}
	}
	parserCtx.parser, err = marketparse.NewParser(pools, nil)
	parserCtx.pools = pools
	if err != nil {
		t.Fatal(err)
	}
	client.blocks[100]["timestamp"] = fmt.Sprintf("0x%x", eventTime.Unix())
	stamp := eventTime
	if _, err := database.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{
		ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, BlockNumber: 100, BlockHash: testHashB, ParentHash: testHashC, BlockTimestamp: &stamp,
	}); err != nil {
		t.Fatal(err)
	}
	if err := collector.hydrateParseAndPersist(ctx, testHashA, parserCtx, "nodit_log_scanner", true); err != nil {
		t.Fatal(err)
	}
	result, err := engine.RunCycle(ctx)
	if err != nil || result.TriggersCreated != 2 || result.DeliveriesSent != 2 || len(notifier.messages) != 2 {
		t.Fatalf("integrated scope evaluation = %+v, err=%v, messages=%v", result, err, notifier.messages)
	}
	for _, message := range notifier.messages {
		if strings.Contains(message, "6000") && !strings.Contains(message, "12000") {
			t.Fatalf("delivery fell back to a single pool: %s", message)
		}
	}
	// A duplicate webhook read and a second rule cycle must not count a new
	// transaction or send again, even after the whole receipt is already ready.
	if err := collector.hydrateParseAndPersist(ctx, testHashA, parserCtx, "webhook", false); err != nil {
		t.Fatal(err)
	}
	result, err = engine.RunCycle(ctx)
	if err != nil || result.TriggersCreated != 0 || result.DeliveriesSent != 0 || len(notifier.messages) != 2 {
		t.Fatalf("duplicate receipt replayed: %+v, %v", result, err)
	}
	history, err := database.ListMarketRuleEventHistory(ctx, project.ID, user, store.MarketEventFilter{Limit: 100})
	if err != nil || len(history) != 2 {
		t.Fatalf("trigger history = %+v, %v", history, err)
	}
	for _, event := range history {
		if event.MarketRuleID != ruleIDs["both"] && event.MarketRuleID != ruleIDs["percent_yes"] {
			t.Fatalf("an unqualified single-pool/1%% rule triggered: %+v", event)
		}
	}
}
