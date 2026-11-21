package marketrules

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

// An explicit administrative initialization may deliberately review history
// before this rule/selection was created. Its captured source generation must
// not reapply those already reviewed lifecycle boundaries on the first run.
// A global notification recovery cutoff remains an independent protection;
// this isolated schema has none, and this test never bypasses one.
func TestPostgresTradeScopeExplicitReviewKeepsApprovedHistoricalStart(t *testing.T) {
	f := newTradeLifecycleFixture(t, plans.MarketLargeBuy)
	ctx := context.Background()
	var created time.Time
	if err := f.pool.QueryRow(ctx, `SELECT created_at FROM market_rules WHERE id=$1`, f.rule.ID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	cutoff, err := f.db.GetNotificationSourceCutoff(ctx)
	if err != nil || cutoff != nil {
		t.Fatalf("review fixture unexpectedly has a global notification cutoff: %v/%v", cutoff, err)
	}
	// Publish now, with a truthful historical source time; do not backdate the
	// rule or manufacture a future pool timestamp to make this case pass.
	occurred := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if !occurred.Before(created) {
		t.Fatalf("historical fixture time %s is not before creation %s", occurred, created)
	}
	event := publishReadinessTrade(t, f, 451, occurred)
	initialized, err := f.db.InitializeTradeRuleState(ctx, f.rule.ID, f.target,
		json.RawMessage(`{"activation_fence":0,"last_trade_ready_id":0}`))
	if err != nil || !initialized {
		t.Fatalf("explicit history review initialization=%v/%v", initialized, err)
	}
	f.service.now = func() time.Time { return time.Now().UTC() }
	if count, err := f.service.evaluateRule(ctx, f.rule); err != nil || count != 1 {
		t.Fatalf("approved historical trade was blocked by a prior scope/creation boundary: %d/%v", count, err)
	}
	state := readTradeReadiness(t, f)
	if state.ActivationOrigin != "explicit_review" || state.EligibilityAfter != nil {
		t.Fatalf("reviewed historical start was silently narrowed: %#v", state)
	}
	target, exists, err := f.db.LoadMarketRuleTargetState(ctx, f.rule.ID, f.target)
	var lane tradeLaneState
	if err != nil || !exists || json.Unmarshal(target.State, &lane) != nil || lane.ActivationFence == nil || *lane.ActivationFence != 0 || lane.LastTradeReadyID != event.TradeReadyID {
		t.Fatalf("reviewed trade did not advance its existing lane: %#v/%v/%v", target, exists, err)
	}
	var recorded, notified int
	if err := f.pool.QueryRow(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE notification_status<>'skipped') FROM market_rule_events WHERE market_rule_id=$1 AND market_event_id=$2`, f.rule.ID, event.ID).Scan(&recorded, &notified); err != nil || recorded != 1 || notified != 1 {
		t.Fatalf("reviewed historical trade audit/delivery=%d/%d/%v", recorded, notified, err)
	}
	if count, err := f.service.evaluateRule(ctx, f.rule); err != nil || count != 0 {
		t.Fatalf("reviewed historical trade was processed twice: %d/%v", count, err)
	}
}
