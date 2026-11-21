package marketrules

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/debox"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// This is a correctness fixture, not a provider, scheduler or capacity model.
// The collector's real receipt hydration entry points have their own named
// integration test. Here a complete local receipt exercises the atomic fact,
// projection, rule, frozen outbox and signed HTTP boundaries at the 5x5 scope.
func TestPostgresPro5x5TradeIsolationAndV2Delivery(t *testing.T) {
	db, pool := openTradeLaneIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const owner = "pro5x5-local-owner"
	if _, err := db.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	policy := store.QuotaPolicy{PlanCode: plans.Professional, RuleLimit: 10, MarketProjectLimit: 5,
		MarketPoolLimit: 5, AllowedMarketChainKeys: []string{"bsc"}, MultiPoolMonitoring: true,
		AllowedMarketRuleTypes: []string{plans.MarketLargeBuy, plans.MarketLargeSell}}
	const quote = "0xffffffffffffffffffffffffffffffffffffffff"
	var projects []store.MarketProject
	var poolIDs [5][]int64
	var allPoolIDs []int64
	var tokens []string
	snapshotAt := time.Now().UTC().Truncate(time.Microsecond)
	for projectIndex := 0; projectIndex < 5; projectIndex++ {
		token := fmt.Sprintf("0x%040x", projectIndex+1)
		tokens = append(tokens, token)
		project, err := db.CreateMarketProjectWithinQuota(ctx, store.CreateMarketProjectParams{
			DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56, TokenAddress: token,
			TokenName: fmt.Sprintf("Local project %d", projectIndex), TokenSymbol: fmt.Sprintf("FIX%d", projectIndex), TokenDecimals: 6,
		}, policy)
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, project)
		for poolIndex := 0; poolIndex < 5; poolIndex++ {
			address := fmt.Sprintf("0x%040x", 100+projectIndex*5+poolIndex)
			other := quote
			// Both monitored coins occur on both sides of the same physical
			// swaps, but each project selects its own five different pools.
			if projectIndex < 2 {
				other = fmt.Sprintf("0x%040x", 2-projectIndex)
			}
			marketPool, err := db.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
				ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address,
				Token0Address: token, Token0Decimals: 6, Token0DecimalsVerified: true,
				Token1Address: other, Token1Decimals: 6, Token1DecimalsVerified: true,
				LiquidityUSD: "1000000", SupportsEventParsing: true, ParserAdapter: marketparse.AdapterV2, VerificationStatus: "verified",
			})
			if err != nil {
				t.Fatal(err)
			}
			poolIDs[projectIndex] = append(poolIDs[projectIndex], marketPool.ID)
			allPoolIDs = append(allPoolIDs, marketPool.ID)
			if _, err := db.LinkMarketProjectPoolWithinQuota(ctx, store.LinkMarketProjectPoolParams{
				DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: marketPool.ID,
				Selected: true, IsPrimary: poolIndex == 0, DiscoverySource: "local_5x5_fixture",
			}, policy); err != nil {
				t.Fatal(err)
			}
			if _, err := db.CreateMarketSnapshot(ctx, store.CreateMarketSnapshotParams{
				ChainKey: "bsc", ChainID: 56, TokenAddress: token, MarketPoolID: marketPool.ID,
				PriceUSD: stringPointer("1000"), LiquidityUSD: stringPointer("1000000"), Source: "local_5x5_fixture",
				CapturedAt: snapshotAt, QuoteQuality: liveQuoteFixture(snapshotAt),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	engine := New(Dependencies{Repository: db, PublicAppURL: "https://local.invalid"}, Settings{TradeFactsEnabled: true})
	type ruleCase struct {
		project                      int
		kind, unit, threshold, scope string
		want                         int64
	}
	specs := []ruleCase{
		{0, plans.MarketLargeBuy, "usd", "10000", "all", 1},
		{0, plans.MarketLargeBuy, "percent", "0.5", "all", 1},
		{0, plans.MarketLargeBuy, "percent", "0.7", "all", 0},
		{0, plans.MarketLargeBuy, "usd", "10000", "primary", 0},
		{0, plans.MarketLargeSell, "usd", "10000", "all", 1},
		{1, plans.MarketLargeBuy, "usd", "10000", "all", 1},
		{1, plans.MarketLargeSell, "usd", "10000", "all", 1},
		{2, plans.MarketLargeBuy, "usd", "10000", "all", 1},
		{3, plans.MarketLargeBuy, "usd", "10000", "all", 1},
		{4, plans.MarketLargeBuy, "usd", "10000", "all", 1},
	}
	var rules []store.MarketRule
	for _, spec := range specs {
		rule, err := db.CreateMarketRuleWithinQuota(ctx, store.CreateMarketRuleParams{
			DeBoxUserID: owner, MarketProjectID: projects[spec.project].ID, RuleType: spec.kind,
			PoolScope: spec.scope, DeploymentScope: "all", CooldownScope: "chain",
			ThresholdUnit: spec.unit, ThresholdValue: spec.threshold,
			NotificationChatID: owner, NotificationChatType: "private", NotificationLanguage: "en",
		}, policy)
		if err != nil {
			t.Fatal(err)
		}
		rules = append(rules, rule)
		targets, err := db.ListMarketRuleTargets(ctx, rule, projects[spec.project])
		merged := MergeTradeRuleTargets(targets)
		wantPools := 5
		if spec.scope == "primary" {
			wantPools = 1
		}
		if err != nil || len(merged) != 1 || len(merged[0].TradePoolIDs) != wantPools {
			t.Fatalf("project %d target escaped selected scope: %#v/%v", spec.project, merged, err)
		}
		// An explicit local review captures this fixture's current scope.
		// It is not an invented production cutoff or a cursor change.
		if _, err := engine.InitializeTradeRuleState(ctx, rule.ID, merged[0], 0); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	hash, blockHash, parentHash := "0x"+strings.Repeat("a", 64), "0x"+strings.Repeat("b", 64), "0x"+strings.Repeat("c", 64)
	if _, err := db.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{
		ChainKey: "bsc", ChainID: 56, CursorKey: "local_5x5_fixture", BlockNumber: 100,
		BlockHash: blockHash, ParentHash: parentHash, BlockTimestamp: &at, ScannedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	var logs []map[string]string
	for index := 1; index <= 50; index++ {
		logs = append(logs, map[string]string{"logIndex": fmt.Sprintf("0x%x", index)})
	}
	receipt, _ := json.Marshal(map[string]any{"transactionHash": hash, "blockHash": blockHash, "blockNumber": "0x64", "transactionIndex": "0x0", "logs": logs})
	proof, _ := json.Marshal(valuationProofFixture(at))
	var events []store.MarketTransactionEvent
	for projectIndex := 0; projectIndex < 5; projectIndex++ {
		directions := []string{marketparse.EventBuy}
		if projectIndex < 2 {
			directions = append(directions, marketparse.EventSell)
		}
		for _, direction := range directions {
			selected := poolIDs[projectIndex]
			perPool := int64(6)
			if projectIndex < 2 {
				selected = allPoolIDs[:10]
			}
			if projectIndex >= 2 {
				perPool = []int64{4, 8, 10}[projectIndex-2]
			}
			var legs []store.MarketTradeLegParams
			for _, poolID := range selected {
				globalIndex := 0
				for index, id := range allPoolIDs {
					if id == poolID {
						globalIndex = index
						break
					}
				}
				logIndex := int64(globalIndex*2 + 1)
				if projectIndex < 2 && (projectIndex == 0 && direction == marketparse.EventSell || projectIndex == 1 && direction == marketparse.EventBuy) {
					logIndex++
				}
				other := quote
				if projectIndex < 2 {
					other = tokens[1-projectIndex]
				}
				legs = append(legs, store.MarketTradeLegParams{PoolID: poolID, LogIndex: logIndex, QuoteAddress: other,
					TokenAmountRaw: fmt.Sprint(perPool * 1000000), QuoteAmountRaw: "6000000", TokenAmount: stringPointer(fmt.Sprint(perPool)),
					USDValue: stringPointer(fmt.Sprint(perPool * 1000)), PriceUSD: stringPointer("1000"), Metadata: proof})
			}
			total := perPool * int64(len(selected))
			events = append(events, store.MarketTransactionEvent{Event: store.CreateMarketEventParams{
				TokenAddress: tokens[projectIndex], EventType: direction, EventKey: fmt.Sprintf("local_5x5_%d_%s", projectIndex, direction),
				TokenAmountRaw: stringPointer(fmt.Sprint(total * 1000000)), TokenAmount: stringPointer(fmt.Sprint(total)),
				USDValue: stringPointer(fmt.Sprint(total * 1000)), PriceUSD: stringPointer("1000"), Source: "nodit_log_scanner", Confidence: "1", Metadata: json.RawMessage(`{"protocol":"pancakeswap"}`),
			}, Legs: legs})
		}
	}
	params := store.MarketTransactionParams{ChainKey: "bsc", ChainID: 56, TransactionHash: hash, BlockNumber: 100, BlockHash: blockHash,
		OccurredAt: at, Confirmed: true, ParserVersion: 2, ScopePoolIDs: allPoolIDs, ScopeTokenAddresses: tokens, ReceiptPayload: receipt, Events: events}
	parents, err := db.PersistMarketTransaction(ctx, params)
	if err != nil || len(parents) != 7 {
		t.Fatalf("complete dual-coin/direction receipt: parents=%d/%v", len(parents), err)
	}
	for index, rule := range rules {
		created, err := engine.evaluateRule(ctx, rule)
		if err != nil || created != specs[index].want {
			t.Fatalf("rule[%d] projected result=%d want=%d err=%v", index, created, specs[index].want, err)
		}
	}
	wantHistory := []int{3, 2, 1, 1, 1}
	wantUSD := []string{"30000", "30000", "20000", "40000", "50000"}
	for index, project := range projects {
		history, err := db.ListMarketRuleEventHistory(ctx, project.ID, owner, store.MarketEventFilter{Limit: 20})
		if err != nil || len(history) != wantHistory[index] {
			t.Fatalf("project %d trigger count=%d want=%d/%v", index, len(history), wantHistory[index], err)
		}
		for _, event := range history {
			if !reflect.DeepEqual(event.TradePoolIDs, poolIDs[index]) || event.USDValue == nil || *event.USDValue != wantUSD[index] || event.MarketPoolID != nil {
				t.Fatalf("project %d mixed another pool/token/direction or lost selected sum: %#v", index, event)
			}
		}
	}
	// Same hash and complete parser scope entering through the other source
	// must preserve seven parents, 55 legs, seven ready records and eight MREs.
	for index := range params.Events {
		params.Events[index].Event.Source = "webhook"
	}
	if _, err := db.PersistMarketTransaction(ctx, params); err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if count, err := engine.evaluateRule(ctx, rule); err != nil || count != 0 {
			t.Fatalf("duplicate counted again: %d/%v", count, err)
		}
	}
	var parentCount, legCount, readyCount, triggerCount int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_events),(SELECT COUNT(*) FROM market_trade_legs),(SELECT COUNT(*) FROM market_trade_ready_events WHERE valid),(SELECT COUNT(*) FROM market_rule_events)`).Scan(&parentCount, &legCount, &readyCount, &triggerCount); err != nil || parentCount != 7 || legCount != 55 || readyCount != 7 || triggerCount != 8 {
		t.Fatalf("receipt replay changed facts/outbox: %d/%d/%d/%d err=%v", parentCount, legCount, readyCount, triggerCount, err)
	}
	claims, err := db.ClaimMarketDeliveries(ctx, 20)
	if err != nil || len(claims) != 8 {
		t.Fatalf("eight actual rule notifications were not claimable: %#v/%v", claims, err)
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].ID < claims[j].ID })
	// Deliberately send three claims, not eight: four actual HTTP attempts fit
	// the shared five/minute admission, including the one definite retry.
	for _, claim := range claims[:3] {
		delivery, err := db.LoadMarketDelivery(ctx, claim)
		if err != nil || delivery.Event == nil || delivery.Snapshot == nil || delivery.Event.USDValue == nil || *delivery.Event.USDValue != "30000" || len(delivery.Event.TradePoolIDs) != 5 || delivery.Snapshot.LiquidityUSD == nil || *delivery.Snapshot.LiquidityUSD != "5000000" {
			t.Fatalf("5-pool frozen delivery projection invalid: %#v/%v", delivery, err)
		}
	}
	var httpMu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/bot/getMe" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"local-5x5-bot"}}`)
			return
		}
		if r.URL.Path != "/openapi/v2/bot/sendMessage" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.Error(w, "unexpected", 404)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			t.Error(err)
		}
		stamp, nonce := r.Header.Get("timestamp"), r.Header.Get("nonce")
		sig := sha1.Sum([]byte("local-test-secret" + nonce + stamp))
		if r.Header.Get("signature") != hex.EncodeToString(sig[:]) || len(nonce) != 64 || r.Header.Get("X-API-KEY") != "local-test-key" {
			t.Error("unsigned/malformed v2 request")
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body["chat_id"] != owner || body["chat_type"] != "private" || !strings.Contains(strings.ReplaceAll(fmt.Sprint(body["content"]), ",", ""), "30000") {
			t.Errorf("frozen destination/selected amount changed: %s", raw)
		}
		httpMu.Lock()
		bodies = append(bodies, append([]byte(nil), raw...))
		call := len(bodies)
		httpMu.Unlock()
		switch call {
		case 2:
			_, _ = io.WriteString(w, `{"ok":false,"success":false,"message":"Energy account is not ready"}`)
		case 3:
			_, _ = io.WriteString(w, `{"ok":true,"success":true,"result":{}}`)
		default:
			_, _ = fmt.Fprintf(w, `{"ok":true,"success":true,"result":{"message_id":"local-accepted-%d"}}`, call)
		}
	}))
	defer server.Close()
	messenger, err := debox.NewMessenger("local-test-key", "local-test-secret", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := notificationdelivery.NewAdmission(db, messenger.Self().UserId, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := messenger.SetNotificationAdmission(admission); err != nil {
		t.Fatal(err)
	}
	engine.notifications = messenger
	for index, claim := range claims[:3] {
		sent, err := engine.deliver(ctx, claim)
		if index == 0 && (!sent || err != nil) || index > 0 && (sent || err == nil) {
			t.Fatalf("delivery[%d] sent=%v err=%v", index, sent, err)
		}
	}
	assertOutcome := func(id int64, wantStatus, wantOutcome string, wantAttempts int) {
		t.Helper()
		var status, outcome string
		var attempts int
		if err := pool.QueryRow(ctx, `SELECT notification_status,delivery_outcome,notification_attempts FROM market_rule_events WHERE id=$1`, id).Scan(&status, &outcome, &attempts); err != nil || status != wantStatus || outcome != wantOutcome || attempts != wantAttempts {
			t.Fatalf("outbox %d outcome=%s/%s attempts=%d want=%s/%s/%d err=%v", id, status, outcome, attempts, wantStatus, wantOutcome, wantAttempts, err)
		}
	}
	assertOutcome(claims[0].ID, "sent", "sent", 1)
	assertOutcome(claims[1].ID, "failed", notificationdelivery.Retryable, 1)
	assertOutcome(claims[2].ID, "skipped", notificationdelivery.Unknown, 1)
	// Expire only the private retry's clock, not an admission token or a lease.
	// Its real next_attempt_at was first verified to be in the future.
	var future bool
	if err := pool.QueryRow(ctx, `SELECT next_attempt_at>NOW() FROM market_rule_events WHERE id=$1`, claims[1].ID).Scan(&future); err != nil || !future {
		t.Fatalf("temporary failure did not back off: %v/%v", future, err)
	}
	if more, err := db.ClaimMarketDeliveries(ctx, 20); err != nil || len(more) != 0 {
		t.Fatalf("early retry or sent/unknown reclaim: %#v/%v", more, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET next_attempt_at=NOW()-INTERVAL '1 second' WHERE id=$1`, claims[1].ID); err != nil {
		t.Fatal(err)
	}
	retry, err := db.ClaimMarketDeliveries(ctx, 20)
	if err != nil || len(retry) != 1 || retry[0].ID != claims[1].ID || retry[0].LeaseToken <= claims[1].LeaseToken {
		t.Fatalf("finite retry/unknown selection incorrect: %#v/%v", retry, err)
	}
	if sent, err := engine.deliver(ctx, retry[0]); err != nil || !sent {
		t.Fatalf("explicit failed retry not sent: %v/%v", sent, err)
	}
	assertOutcome(claims[1].ID, "sent", "sent", 2)
	assertOutcome(claims[2].ID, "skipped", notificationdelivery.Unknown, 1)
	if more, err := db.ClaimMarketDeliveries(ctx, 20); err != nil || len(more) != 0 {
		t.Fatalf("unknown automatically resent: %#v/%v", more, err)
	}
	httpMu.Lock()
	defer httpMu.Unlock()
	if len(bodies) != 4 || !bytes.Equal(bodies[1], bodies[3]) {
		t.Fatalf("retry rebuilt frozen payload or unknown entered HTTP again: calls=%d", len(bodies))
	}
	t.Log("5 projects / 25 distinct selected pools / 10 rules; 7 logical parents / 55 legs / 8 triggers; 4 signed v2 HTTP attempts: 2 confirmed, 1 definite failure retried, 1 unknown held; 5 other claimed notifications intentionally not sent (not a throughput test)")
}
