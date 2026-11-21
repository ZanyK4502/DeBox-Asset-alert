package monitor

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/debox"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/testdb"
)

// A single bounded timer experiment exercises the actual store, SDK adapter,
// v2 HTTP admission and runner. Every endpoint and business row is synthetic.
func TestPostgresWalletRunnerWakesV2QueueWithoutObservingAgain(t *testing.T) {
	db := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const owner = "queue-wake-local-owner"
	if _, err := db.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/bot/getMe" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"user_id": "queue-wake-bot"}})
			return
		}
		var payload struct {
			ChatID   string `json:"chat_id"`
			ChatType string `json:"chat_type"`
			Content  string `json:"content"`
		}
		nonce, stamp := r.Header.Get("nonce"), r.Header.Get("timestamp")
		expected := sha1.Sum([]byte("local-secret" + nonce + stamp))
		if r.Method != http.MethodPost || r.URL.Path != "/openapi/v2/bot/sendMessage" || len(nonce) != 64 || len(stamp) != 13 || r.Header.Get("signature") != hex.EncodeToString(expected[:]) || r.Header.Get("X-API-KEY") != "local-key" || json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&payload) != nil || payload.ChatID != owner || payload.ChatType != "private" || payload.Content == "" {
			t.Error("invalid local v2 wire request")
			http.Error(w, "invalid local fixture", http.StatusBadRequest)
			return
		}
		id := accepted.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "success": true, "result": map[string]any{"message_id": "local-receipt-" + strconv.FormatInt(id, 10), "chat_id": payload.ChatID, "chat_type": payload.ChatType}})
	}))
	defer server.Close()
	messenger, err := debox.NewMessenger("local-key", "local-secret", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := notificationdelivery.NewAdmission(db, "queue-wake-bot", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := messenger.SetNotificationAdmission(admission); err != nil {
		t.Fatal(err)
	}
	source := &fakeChain{balance: chain.BalanceResult{Value: "1", Symbol: "BNB"}}
	executor := newTestExecutor(t, db, source, messenger)
	var before []store.WatchRule
	for index := range 12 {
		zero := "0"
		rule, err := db.CreateWatchRule(ctx, store.CreateWatchRuleParams{DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56,
			WalletAddress: fmt.Sprintf("0x%040x", index+1), RuleType: plans.BalanceChange, Threshold: "0", LastValue: &zero,
			NotificationChatID: owner, NotificationChatType: "private", DeliveryMode: "realtime"})
		if err != nil {
			t.Fatal(err)
		}
		result := executor.checkRule(context.WithValue(ctx, walletEnqueueOnlyContextKey{}, true), rule, plans.Professional)
		if result.Status != "queued" {
			t.Fatalf("seed observation=%+v", result)
		}
		stored, err := db.GetWatchRule(ctx, rule.ID, owner)
		if err != nil || stored == nil {
			t.Fatal(stored, err)
		}
		before = append(before, *stored)
	}
	if count, err := db.CountDailyAlertEvents(ctx, owner, freeAlertTimezone); err != nil || count != 12 {
		t.Fatalf("initial trigger count=%d/%v", count, err)
	}
	source.calls = nil
	runner := NewRunner(executor, func(ctx context.Context) (Lock, bool, error) { return db.TryMonitorExecutionLock(ctx) }, time.Minute)
	future := time.Now().Add(time.Minute)
	runner.nextObservationAt, runner.nextEntitlementCheckAt, runner.nextCleanupAttemptAt = future, future, future
	started := time.Now()
	wait := runner.runCycle(ctx, discardLogger())
	first := accepted.Load()
	if first == 0 || first >= 12 || wait <= 0 || wait > 2*time.Second {
		t.Fatalf("did not stop at a short due wake: sent=%d wait=%s", first, wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	_ = runner.runCycle(ctx, discardLogger())
	if accepted.Load() != 12 || len(source.calls) != 0 || time.Since(started) >= 5*time.Second {
		t.Fatalf("due queue waited for observations: sent=%d reads=%v elapsed=%s", accepted.Load(), source.calls, time.Since(started))
	}
	if at, err := db.NextWalletNotificationAt(ctx, time.Time{}); err != nil || at != nil {
		t.Fatalf("queue did not drain: next=%v/%v", at, err)
	}
	if count, err := db.CountDailyAlertEvents(ctx, owner, freeAlertTimezone); err != nil || count != 12 {
		t.Fatalf("queue wake recounted triggers=%d/%v", count, err)
	}
	for _, previous := range before {
		now, err := db.GetWatchRule(ctx, previous.ID, owner)
		if err != nil || now == nil || now.LastValue == nil || previous.LastValue == nil || *now.LastValue != *previous.LastValue || now.LastCheckedAt == nil || previous.LastCheckedAt == nil || !now.LastCheckedAt.Equal(*previous.LastCheckedAt) {
			t.Fatalf("queue wake changed balance baseline: before=%+v after=%+v error=%v", previous, now, err)
		}
	}
	events, err := db.ListSummaryRecentEvents(ctx, owner, started.Add(-time.Minute), time.Now().Add(time.Second), 20)
	if err != nil || len(events) != 12 {
		t.Fatalf("final event history=%d/%v", len(events), err)
	}
	for _, event := range events {
		if event.NotificationStatus != "sent" || event.NotificationAttempts != 1 || event.NotificationMessageID == nil {
			t.Fatalf("duplicate attempt or unconfirmed send: %+v", event)
		}
	}
	t.Logf("local timer acceptance: first=%d total=%d elapsed=%s balance_reads_after_seed=%d", first, accepted.Load(), time.Since(started), len(source.calls))
}
