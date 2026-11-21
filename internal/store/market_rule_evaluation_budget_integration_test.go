package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Cancellable serialized SQL delay is a local regression model, not a measured
// production RTT. The tracer exists only in this test; production has no hook.
type marketEvaluationLagTrace struct {
	mu     sync.Mutex
	active bool
	calls  int
}

func (trace *marketEvaluationLagTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	trace.mu.Lock()
	active := trace.active
	if active {
		trace.calls++
	}
	trace.mu.Unlock()
	if active {
		timer := time.NewTimer(7 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	return ctx
}
func (*marketEvaluationLagTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestPostgresMarketEvaluationSmallPrefixesFitUnchangedBudgetWithSQLLag(t *testing.T) {
	database, verify, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	trace := &marketEvaluationLagTrace{}
	cfg := verify.Config()
	cfg.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	database.db = traced
	var events []MarketEvent
	for i := 0; i < 30; i++ {
		events = append(events, marketDeliveryFact(t, database, fmt.Sprintf("lag-model-%d", i), time.Now().UTC()))
	}
	target := MarketRuleTarget{TargetKey: "local-budget-prefix"}
	if err := database.UpdateMarketRuleTargetState(ctx, rule.ID, target, []byte(`{"last_event_id":0}`), false); err != nil {
		t.Fatal(err)
	}
	writePage := func(page []MarketEvent) error {
		t.Helper()
		trace.mu.Lock()
		trace.active, trace.calls = true, 0
		trace.mu.Unlock()
		start := time.Now()
		err := database.WithMarketRuleEvaluationWrite(ctx, rule.ID, func(writer *Store) error {
			for _, event := range page {
				if _, _, err := writer.CreateMarketRuleEventAndEnqueue(ctx, CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: event.ID, TriggerKey: event.EventKey, NotificationStatus: "pending"}); err != nil {
					return err
				}
			}
			state, _ := json.Marshal(map[string]int64{"last_event_id": page[len(page)-1].ID})
			if err := writer.UpdateMarketRuleTargetState(ctx, rule.ID, target, state, true); err != nil {
				return err
			}
			_, err := writer.UpdateMarketRuleState(ctx, rule.ID, state, true)
			return err
		})
		trace.mu.Lock()
		trace.active = false
		calls := trace.calls
		trace.mu.Unlock()
		t.Logf("local 7ms/SQL model: parents=%d calls=%d wall=%s budget_error=%t", len(page), calls, time.Since(start), errors.Is(err, ErrMarketDeliveryBudget))
		return err
	}
	if err := writePage(events); !errors.Is(err, ErrMarketDeliveryBudget) {
		t.Fatalf("old 30-parent control must retain its 2s failure: %v", err)
	}
	var clean bool
	if err := verify.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM market_rule_events) AND r.state='{}'::jsonb AND r.last_triggered_at IS NULL AND s.state='{"last_event_id":0}'::jsonb AND s.last_triggered_at IS NULL FROM market_rules r JOIN market_rule_target_states s ON s.market_rule_id=r.id WHERE r.id=$1 AND s.target_key=$2`, rule.ID, target.TargetKey).Scan(&clean); err != nil || !clean {
		t.Fatalf("large failed page partially committed source/state/cooldown: %t/%v", clean, err)
	}
	for _, page := range [][]MarketEvent{events[:MarketRuleCurrentEventLimit], events[MarketRuleCurrentEventLimit:]} {
		if err := writePage(page); err != nil {
			t.Fatalf("complete small prefix cannot make progress within original 2s: %v", err)
		}
	}
	var count int
	var cursor int64
	if err := verify.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_rule_events WHERE market_rule_id=$1),(state->>'last_event_id')::bigint FROM market_rule_target_states WHERE market_rule_id=$1 AND target_key=$2`, rule.ID, target.TargetKey).Scan(&count, &cursor); err != nil || count != len(events) || cursor != events[len(events)-1].ID {
		t.Fatalf("small committed prefixes lost or duplicated facts: %d/%d/%v", count, cursor, err)
	}
}
