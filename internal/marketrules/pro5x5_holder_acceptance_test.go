package marketrules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// Real holder Data decoding/attempt observer, leased scheduler and atomic PG
// apply are used. Only HTTP and advancing the isolated scheduler minute are
// fixtures; this is a cost/count sample, not a provider throughput benchmark.
func TestPostgresPro5HolderFullSampleFailureAndRecoveryCost(t *testing.T) {
	db, pool := openTradeLaneIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	const owner = "pro5-holder-cost"
	if _, err := db.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	policy := store.QuotaPolicy{PlanCode: plans.Professional, RuleLimit: 10, WalletLimit: 20, MarketProjectLimit: 5, MarketPoolLimit: 5,
		MultiPoolMonitoring: true, AllowedMarketChainKeys: []string{"bsc"}, AllowedMarketRuleTypes: []string{plans.MarketHolderRankEntered}}
	var tokens []string
	for index := range 5 {
		token := fmt.Sprintf("0x%040x", 5000+index)
		tokens = append(tokens, token)
		project, err := db.CreateMarketProjectWithinQuota(ctx, store.CreateMarketProjectParams{DeBoxUserID: owner,
			ChainKey: "bsc", ChainID: 56, TokenAddress: token, TokenDecimals: 0, TokenSymbol: "HOLDER"}, policy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.CreateMarketRuleWithinQuota(ctx, store.CreateMarketRuleParams{DeBoxUserID: owner, MarketProjectID: project.ID,
			RuleType: plans.MarketHolderRankEntered, ThresholdValue: "100", ThresholdUnit: "count", NotificationChatID: owner}, policy); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	counts := map[string]int{}
	observedCalls, observedCU := int64(0), int64(0)
	failLastPage := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Token     string `json:"contractAddress"`
			Cursor    string `json:"cursor"`
			RPP       int    `json:"rpp"`
			WithCount bool   `json:"withCount"`
		}
		if r.URL.Path != "/bnb/mainnet/token/getTokenHoldersByContract" || json.NewDecoder(r.Body).Decode(&payload) != nil || payload.RPP != 100 || !payload.WithCount {
			t.Error("unexpected holder Data wire request")
			http.Error(w, "unexpected fixture request", http.StatusBadRequest)
			return
		}
		page := 0
		if payload.Cursor != "" {
			var err error
			page, err = strconv.Atoi(strings.TrimPrefix(payload.Cursor, "page-"))
			if err != nil || page < 1 || page > 19 {
				t.Error("invalid fixture cursor", payload.Cursor)
				http.Error(w, "invalid cursor", http.StatusBadRequest)
				return
			}
		}
		mu.Lock()
		counts[payload.Token]++
		fail := failLastPage && payload.Token == tokens[0] && page == 19
		mu.Unlock()
		if fail {
			http.Error(w, "bounded last-page failure", http.StatusServiceUnavailable)
			return
		}
		items := make([]map[string]string, 100)
		for index := range items {
			n := page*100 + index
			items[index] = map[string]string{"ownerAddress": completeHolderAddress(n), "balance": fmt.Sprint(n + 1)}
		}
		next := ""
		if page < 19 {
			next = fmt.Sprintf("page-%d", page+1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": 2000, "rpp": 100, "cursor": next, "items": items})
	}))
	defer server.Close()
	client, err := chain.NewClient("local-fixture", server.URL, chain.WithHTTPClient(server.Client()), chain.WithCURateLimit(1_000_000),
		chain.WithUsageObserver(func(usage chain.NoditUsage) {
			mu.Lock()
			defer mu.Unlock()
			observedCalls++
			observedCU += usage.CU
		}))
	if err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{Repository: db, Holders: client}, Settings{Enabled: true, MonitoringChainKeys: []string{"bsc"}})
	runner, err := NewHolderRefreshRunner(service, HolderRefreshSettings{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	advanceBudgetMinute := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_scheduler SET window_started_at=date_trunc('minute',clock_timestamp())-INTERVAL '1 minute'`); err != nil {
			t.Fatal(err)
		}
	}
	for index := range 5 {
		if index == 3 {
			// Continue in another isolated budget minute after sixty pages;
			// no real fifteen-minute refresh or one-minute budget wait is needed.
			advanceBudgetMinute()
		}
		result, err := runner.RunOnce(ctx)
		if err != nil || result.Refreshed != 1 || result.PagesAdmitted != 20 {
			t.Fatalf("complete five-token round %d: %+v %v", index, result, err)
		}
	}
	for _, token := range tokens {
		state, err := db.GetMarketHolderRefreshState(ctx, 56, token)
		if err != nil || state == nil || state.LastSuccessAt == nil || state.SourceItemCount != 2000 || len(state.RankBaseline) != 100 || state.RankBaseline[0].BalanceRaw != "2000" {
			t.Fatalf("incomplete token baseline %s: %+v %v", token, state, err)
		}
	}
	mu.Lock()
	initialCalls, initialCU := observedCalls, observedCU
	mu.Unlock()
	if initialCalls != 100 || initialCU != 15_000 {
		t.Fatalf("complete five-token costs: calls=%d CU=%d", initialCalls, initialCU)
	}
	before, err := db.GetMarketHolderRefreshState(ctx, 56, tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	var snapshotCount int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holder_snapshots`).Scan(&snapshotCount); err != nil || snapshotCount != 100 {
		t.Fatal(snapshotCount, err)
	}
	makeFirstDue := func() {
		t.Helper()
		advanceBudgetMinute()
		if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_states SET next_due_at=clock_timestamp()-INTERVAL '1 second',next_attempt_at=clock_timestamp()-INTERVAL '1 second' WHERE token_address=$1`, tokens[0]); err != nil {
			t.Fatal(err)
		}
	}
	makeFirstDue()
	mu.Lock()
	failLastPage = true
	mu.Unlock()
	failed, err := runner.RunOnce(ctx)
	if err == nil || failed.Refreshed != 0 || failed.PagesAdmitted != 20 || failed.FailureCode != "source_error" {
		t.Fatalf("failed final page committed: %+v %v", failed, err)
	}
	afterFailure, err := db.GetMarketHolderRefreshState(ctx, 56, tokens[0])
	if err != nil || afterFailure.LastSuccessAt == nil || !afterFailure.LastSuccessAt.Equal(*before.LastSuccessAt) || afterFailure.RankBaseline[0].BalanceRaw != before.RankBaseline[0].BalanceRaw {
		t.Fatalf("failure advanced baseline: %+v %v", afterFailure, err)
	}
	var afterFailureSnapshots int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holder_snapshots`).Scan(&afterFailureSnapshots); err != nil || afterFailureSnapshots != snapshotCount {
		t.Fatalf("failure changed snapshots: %d %v", afterFailureSnapshots, err)
	}
	mu.Lock()
	failedCalls, failedCU := observedCalls-initialCalls, observedCU-initialCU
	failLastPage = false
	mu.Unlock()
	if failedCalls != 22 || failedCU != 3300 {
		t.Fatalf("failed logical page retry attempts missing: calls=%d CU=%d", failedCalls, failedCU)
	}
	makeFirstDue()
	idle, err := runner.RunOnce(ctx)
	if err != nil || !idle.Idle || idle.Claimed != 0 || idle.PagesAdmitted != 0 {
		t.Fatalf("full rolling budget admitted recovery prematurely: %+v %v", idle, err)
	}
	// Expire only the oldest round of pages and keep the other admissions.
	tag, err := pool.Exec(ctx, `UPDATE market_holder_refresh_scheduler
 SET recent_page_admissions=array_fill(clock_timestamp()-INTERVAL '16 minutes',ARRAY[20])
     || recent_page_admissions[21:120]
 WHERE id=1 AND cardinality(recent_page_admissions)=120`)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("could not advance the rolling budget clock: %v", err)
	}
	recovered, err := runner.RunOnce(ctx)
	if err != nil || recovered.Refreshed != 1 || recovered.PagesAdmitted != 20 {
		t.Fatalf("bounded recovery failed: %+v %v", recovered, err)
	}
	afterRecovery, err := db.GetMarketHolderRefreshState(ctx, 56, tokens[0])
	if err != nil || afterRecovery.LastSuccessAt == nil || !afterRecovery.LastSuccessAt.After(*before.LastSuccessAt) {
		t.Fatalf("recovery did not advance success: %+v %v", afterRecovery, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if observedCalls != 142 || observedCU != 21_300 {
		t.Fatalf("attempt cost total wrong: %d/%d", observedCalls, observedCU)
	}
	report, _ := json.Marshal(map[string]any{"tokens": 5, "holders_per_token": 2000, "successful_pages": 100, "initial_attempts": initialCalls,
		"initial_cu": initialCU, "failed_round_logical_pages": 20, "failed_round_http_attempts": failedCalls, "failed_round_cu": failedCU,
		"recovery_attempts": 20, "recovery_cu": 3000, "total_attempts": observedCalls, "total_cu": observedCU,
		"provider_rate_fixture": 1_000_000, "throughput_claim": false, "minute_page_budget": 60, "rolling_page_budget": 120, "cadence_minutes": 15})
	t.Logf("PRO5_HOLDER_COST %s", report)
}
