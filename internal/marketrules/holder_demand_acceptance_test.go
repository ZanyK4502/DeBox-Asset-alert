package marketrules

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// Exercise the real public single-chain creation path (without deployment
// rows), real demand scheduling and atomic DB apply with a paginated provider.
func TestPostgresHolderDemandPublicProjectsShareComplete2000Sample(t *testing.T) {
	db, pool := openTradeLaneIntegrationStore(t)
	ctx := context.Background()
	supply := "1000000000"
	policy := store.QuotaPolicy{PlanCode: plans.Professional, RuleLimit: 10, WalletLimit: 20, MarketProjectLimit: 1, MarketPoolLimit: 2, MultiPoolMonitoring: true, AllowedMarketChainKeys: []string{"bsc"}, AllowedMarketRuleTypes: []string{plans.MarketHolderRankEntered}}
	projects := make([]store.MarketProject, 0, 3)
	for _, owner := range []string{"holder-no-rule", "holder-owner-a", "holder-owner-b"} {
		if _, err := db.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
			t.Fatal(err)
		}
		project, err := db.CreateMarketProjectWithinQuota(ctx, store.CreateMarketProjectParams{DeBoxUserID: owner, ChainID: 56, ChainKey: "bsc", TokenAddress: tradeFactsTestToken, TokenSymbol: "SHARED", TokenDecimals: 0, TotalSupplyRaw: &supply}, policy)
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, project)
	}
	count := int64(store.MarketHolderRefreshMaxHolders)
	pages := make([]chain.TokenHoldersPage, store.MarketHolderRefreshMaxPages)
	for page := range pages {
		items := make([]chain.TokenHolder, store.MarketHolderRefreshPageSize)
		for i := range items {
			index := page*store.MarketHolderRefreshPageSize + i
			items[i] = chain.TokenHolder{OwnerAddress: fmt.Sprintf("0x%040x", index+100), BalanceRaw: fmt.Sprint(index + 1)}
		}
		cursor := ""
		if page+1 < len(pages) {
			cursor = fmt.Sprintf("cursor-%d", page+1)
		}
		pages[page] = chain.TokenHoldersPage{Count: &count, RPP: store.MarketHolderRefreshPageSize, Cursor: cursor, Items: items}
	}
	provider := &holderSampleTestProvider{pages: pages}
	service := New(Dependencies{Repository: db, Holders: provider}, Settings{Enabled: true, MonitoringChainKeys: []string{"bsc"}})
	runner, err := NewHolderRefreshRunner(service, HolderRefreshSettings{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	// A project alone does not generate a source request or initialize an idle scheduler.
	if result, err := runner.RunOnce(ctx); err != nil || result.Claimed != 0 || len(provider.options) != 0 {
		t.Fatalf("no-demand project bought pages: %+v %v", result, err)
	}
	var schedulers int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holder_refresh_scheduler`).Scan(&schedulers); err != nil || schedulers != 0 {
		t.Fatal(schedulers, err)
	}
	rules := make([]store.MarketRule, 0, 2)
	for _, project := range projects[1:] {
		rule, err := db.CreateMarketRuleWithinQuota(ctx, store.CreateMarketRuleParams{DeBoxUserID: project.DeBoxUserID, MarketProjectID: project.ID, RuleType: plans.MarketHolderRankEntered, ThresholdValue: "100", ThresholdUnit: "count", NotificationChatID: project.DeBoxUserID}, policy)
		if err != nil {
			t.Fatal(err)
		}
		rules = append(rules, rule)
	}
	result, err := runner.RunOnce(ctx)
	if err != nil || result.Refreshed != 1 || result.PagesAdmitted != 20 || len(provider.options) != 20 {
		t.Fatalf("complete shared refresh %+v calls=%d %v", result, len(provider.options), err)
	}
	state, err := db.GetMarketHolderRefreshState(ctx, 56, tradeFactsTestToken)
	if err != nil || state == nil || state.LastSuccessAt == nil || state.SourceItemCount != 2000 || state.CoveredTopN != 100 || len(state.RankBaseline) != 100 {
		t.Fatalf("state=%+v %v", state, err)
	}
	if state.RankBaseline[0].BalanceRaw != "2000" {
		t.Fatalf("late-page largest holder omitted: %+v", state.RankBaseline[0])
	}
	var states, tracked, history, events int
	if err = pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_holder_refresh_states),(SELECT COUNT(*) FROM market_holders),(SELECT COUNT(*) FROM market_holder_snapshots),(SELECT COUNT(*) FROM market_events)`).Scan(&states, &tracked, &history, &events); err != nil || states != 1 || tracked != 20 || history != 20 || events != 0 {
		t.Fatalf("sharing/initial baseline changed %d/%d/%d/%d %v", states, tracked, history, events, err)
	}
	var due, started time.Time
	if err = pool.QueryRow(ctx, `SELECT next_due_at,refresh_started_at FROM market_holder_refresh_states WHERE chain_id=56 AND token_address=$1`, tradeFactsTestToken).Scan(&due, &started); err != nil || due.Sub(started) != 15*time.Minute {
		t.Fatalf("refresh cadence changed: %s %v", due, err)
	}
	if result, err = runner.RunOnce(ctx); err != nil || result.Claimed != 0 || len(provider.options) != 20 {
		t.Fatalf("second owner duplicated refresh: %+v %v", result, err)
	}
	for _, rule := range rules {
		if deleted, err := db.DeleteMarketRule(ctx, rule.ID, rule.DeBoxUserID); err != nil || !deleted {
			t.Fatal(deleted, err)
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE market_holder_refresh_states SET next_due_at=NOW()-INTERVAL '1 second',next_attempt_at=NOW()-INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	if result, err = runner.RunOnce(ctx); err != nil || result.Claimed != 0 || len(provider.options) != 20 {
		t.Fatalf("stopped demand still sampled: %+v %v", result, err)
	}
	after, err := db.GetMarketHolderRefreshState(ctx, 56, tradeFactsTestToken)
	if err != nil || after.LastSuccessAt == nil || !after.LastSuccessAt.Equal(*state.LastSuccessAt) || after.SourceItemCount != 2000 || after.RankBaseline[0].BalanceRaw != "2000" {
		t.Fatalf("stopped demand changed saved baseline: %+v %v", after, err)
	}
}
