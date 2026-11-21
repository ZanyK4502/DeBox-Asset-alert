package store

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestPostgresRuleBackoffPreservesBusinessStateAndDoesNotStarveRule201(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	created := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	triggered := created.Add(time.Minute)
	var projectID int64
	if err := pool.QueryRow(ctx, `INSERT INTO market_projects(debox_user_id,chain_key,chain_id,token_address,token_name,token_symbol,token_decimals)
		VALUES('backoff-owner','bsc',56,$1,'Backoff','BACK',18) RETURNING id`, tradeFixtureToken).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"last_event_id":123,"condition_active":true,"pending_crossing":true}`)
	rows, err := pool.Query(ctx, `INSERT INTO market_rules(debox_user_id,market_project_id,rule_type,threshold_value,threshold_unit,window_minutes,
		notification_chat_id,state,last_triggered_at,created_at)
		SELECT 'backoff-owner',$1,'market_consecutive_large_buy',1000,'usd',15,'fixture-chat',$2,$3,$4::timestamptz+(n*INTERVAL '1 second')
		FROM generate_series(1,201) n RETURNING id`, projectID, state, triggered, created)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := collectInt64Rows(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 201 {
		t.Fatalf("fixture rules=%d", len(ids))
	}
	first, err := database.ListEnabledMarketRules(ctx, 200)
	if err != nil || len(first) != 200 {
		t.Fatalf("first due batch=%d err=%v", len(first), err)
	}
	targetState := json.RawMessage(`{"activation_fence":10,"last_trade_ready_id":99}`)
	if _, err := pool.Exec(ctx, `INSERT INTO market_rule_target_states(market_rule_id,target_key,state,last_triggered_at,last_evaluated_at)
		VALUES($1,'deployment:7/trades:v2',$2,$3,$4)`, ids[0], targetState, triggered, created); err != nil {
		t.Fatal(err)
	}
	var beforeState, beforeTarget []byte
	if err := pool.QueryRow(ctx, "SELECT state FROM market_rules WHERE id=$1", ids[0]).Scan(&beforeState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT state FROM market_rule_target_states WHERE market_rule_id=$1", ids[0]).Scan(&beforeTarget); err != nil {
		t.Fatal(err)
	}
	for _, rule := range first {
		if err := database.DeferMarketRuleEvaluation(ctx, rule.ID, 5*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	next, err := database.ListEnabledMarketRules(ctx, 200)
	if err != nil || len(next) != 1 || next[0].ID != ids[200] {
		t.Fatalf("deferred front 200 still starved other rules: ids=%v err=%v", next, err)
	}
	var afterState, afterTarget []byte
	var lastTriggered, targetTriggered time.Time
	var retrySeconds int64
	var threshold string
	if err := pool.QueryRow(ctx, `SELECT state,last_triggered_at,EXTRACT(EPOCH FROM next_evaluation_at-last_evaluated_at)::bigint,threshold_value::text
		FROM market_rules WHERE id=$1`, ids[0]).Scan(&afterState, &lastTriggered, &retrySeconds, &threshold); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT state,last_triggered_at FROM market_rule_target_states WHERE market_rule_id=$1`, ids[0]).Scan(&afterTarget, &targetTriggered); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeState, afterState) || !bytes.Equal(beforeTarget, afterTarget) || !lastTriggered.Equal(triggered) || !targetTriggered.Equal(triggered) || retrySeconds != 60 || threshold != "1000" {
		t.Fatalf("inspection changed business state: rule=%s target=%s triggered=%v retry=%d", afterState, afterTarget, lastTriggered, retrySeconds)
	}
	// Existing successful-state persistence clears the scheduling delay without
	// altering a cooldown when no trigger was created.
	if _, err := database.UpdateMarketRuleState(ctx, ids[0], state, false); err != nil {
		t.Fatal(err)
	}
	var cleared bool
	if err := pool.QueryRow(ctx, "SELECT next_evaluation_at IS NULL AND last_triggered_at=$2 FROM market_rules WHERE id=$1", ids[0], triggered).Scan(&cleared); err != nil || !cleared {
		t.Fatalf("successful inspection did not clear only its delay: %v %v", cleared, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE market_rules SET next_evaluation_at=NOW()-INTERVAL '1 second' WHERE id=$1", ids[1]); err != nil {
		t.Fatal(err)
	}
	due, err := database.ListEnabledMarketRules(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rule := range due {
		if rule.ID == ids[1] {
			found = true
		}
	}
	if !found {
		t.Fatal("deferred rule never became due again")
	}
}
