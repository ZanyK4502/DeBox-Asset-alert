package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func interactionQuotaPolicy() QuotaPolicy {
	return QuotaPolicy{
		PlanCode: "professional", WalletLimit: 20, RuleLimit: 10,
		InteractionRuleLimit: 1, CombinationRules: true, StageNotifications: true,
		AllowedRuleTypes: []string{"balance_change", "address_interaction"},
	}
}

func interactionQuotaOwner(t *testing.T, ctx context.Context, db *Store, owner string) {
	t.Helper()
	if _, err := db.ActivateSubscription(ctx, owner, "professional", 30); err != nil {
		t.Fatal(err)
	}
}

func interactionQuotaRule(owner string, index int, kind string) CreateWatchRuleParams {
	previous := "9007199254740993"
	p := CreateWatchRuleParams{
		DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56,
		WalletAddress: fmt.Sprintf("0x%040x", index), RuleType: kind, Threshold: "0",
		NotificationChatID: owner, NotificationChatType: "private", NotificationLanguage: "zh",
		DeliveryMode: "stage", CycleType: "fixed", CycleMinutes: 60, TriggerCountThreshold: 3,
		LastValue: &previous,
	}
	if kind == "address_interaction" {
		target := fmt.Sprintf("0x%040x", 10000+index)
		p.TargetAddress, p.TargetLabel = &target, "local interaction target"
	}
	return p
}

func interactionQuotaCombination(owner string, index int, kinds ...string) CreateCombinationRuleParams {
	p := CreateCombinationRuleParams{
		DeBoxUserID: owner, Note: fmt.Sprintf("local combination %d", index),
		CycleType: "fixed", CycleMinutes: 60,
		NotificationChatID: owner, NotificationChatType: "private", NotificationLanguage: "zh",
	}
	for i, kind := range kinds {
		p.Members = append(p.Members, CreateCombinationMemberParams{
			Rule: interactionQuotaRule(owner, index+i, kind), RequiredTriggerCount: int64(i + 2),
		})
	}
	return p
}

func interactionQuotaCreateRule(t *testing.T, ctx context.Context, db *Store, owner string, index int, kind string, policy QuotaPolicy) WatchRule {
	t.Helper()
	rule, err := db.CreateWatchRuleWithinQuota(ctx, interactionQuotaRule(owner, index, kind), policy)
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func interactionQuotaCreateCombination(t *testing.T, ctx context.Context, db *Store, owner string, index int, policy QuotaPolicy) CombinationRule {
	t.Helper()
	combo, err := db.CreateCombinationRuleWithinQuota(ctx, interactionQuotaCombination(owner, index, "address_interaction", "balance_change"), policy)
	if err != nil {
		t.Fatal(err)
	}
	return combo
}

// Seed a stopped rule with meaningful state. A quota refusal must not reset its
// balance/cursor, observation time, notification generation, anchor or window.
func interactionQuotaPauseRule(t *testing.T, ctx context.Context, pool *pgxpool.Pool, rule WatchRule) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET run_status='paused',pause_reason='subscription_expired',
 last_value='9007199254740993',last_checked_at='2026-01-02T03:04:05Z',notification_generation=7,
 aggregation_anchor_at='2026-01-02T02:00:00Z' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	interactionQuotaWindow(t, ctx, pool, rule.DeBoxUserID, "rule", &rule.ID, nil)
}

func interactionQuotaPauseCombination(t *testing.T, ctx context.Context, pool *pgxpool.Pool, combo CombinationRule) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE combination_rules SET run_status='paused',pause_reason='subscription_expired',
 aggregation_anchor_at='2026-01-02T02:00:00Z' WHERE id=$1`, combo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET run_status='paused',pause_reason='subscription_expired',
 last_value='9007199254740993',last_checked_at='2026-01-02T03:04:05Z',notification_generation=9
 WHERE id IN(SELECT watch_rule_id FROM combination_rule_members WHERE combination_rule_id=$1)`, combo.ID); err != nil {
		t.Fatal(err)
	}
	interactionQuotaWindow(t, ctx, pool, combo.DeBoxUserID, "combination", nil, &combo.ID)
}

func interactionQuotaWindow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, source string, ruleID, combinationID *int64) {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO aggregation_windows(debox_user_id,source_type,watch_rule_id,combination_rule_id,
 starts_at,ends_at,total_trigger_count) VALUES($1,$2,$3,$4,'2026-01-02T02:00:00Z','2026-01-02T03:00:00Z',7) RETURNING id`, owner, source, ruleID, combinationID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO aggregate_notifications(debox_user_id,aggregation_window_id,notification_kind,
 notification_chat_id,trigger_count_snapshot,notification_status,notification_attempts,notification_error)
 VALUES($1,$2,CASE WHEN $3='rule' THEN 'stage' ELSE 'combination' END,$1,7,'unknown',2,'preserve original delivery evidence')`, owner, id, source); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_preferences(debox_user_id) VALUES($1) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
}

// Empty ID lists mean the entire owner. JSONB retains exact integer state and
// includes pending/unknown delivery records; the helper never interprets them.
func interactionQuotaSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner string, ruleIDs, combinationIDs []int64) string {
	t.Helper()
	if ruleIDs == nil {
		ruleIDs = []int64{}
	}
	if combinationIDs == nil {
		combinationIDs = []int64{}
	}
	var state json.RawMessage
	if err := pool.QueryRow(ctx, `WITH rules AS (
 SELECT * FROM watch_rules WHERE debox_user_id=$1 AND(cardinality($2::bigint[])=0 OR id=ANY($2))
 ), combos AS (
 SELECT * FROM combination_rules WHERE debox_user_id=$1 AND(cardinality($3::bigint[])=0 OR id=ANY($3))
 ), windows AS (
 SELECT * FROM aggregation_windows WHERE debox_user_id=$1
 AND(watch_rule_id IN(SELECT id FROM rules) OR combination_rule_id IN(SELECT id FROM combos))
 ) SELECT jsonb_build_object(
 'rules',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM rules r),'[]'::jsonb),
 'combos',COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM combos c),'[]'::jsonb),
 'members',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM combination_rule_members m WHERE m.combination_rule_id IN(SELECT id FROM combos)),'[]'::jsonb),
 'windows',COALESCE((SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM windows w),'[]'::jsonb),
 'notifications',COALESCE((SELECT jsonb_agg(to_jsonb(n) ORDER BY n.id) FROM aggregate_notifications n WHERE n.aggregation_window_id IN(SELECT id FROM windows)),'[]'::jsonb),
 'events',COALESCE((SELECT jsonb_agg(to_jsonb(e) ORDER BY e.id) FROM alert_events e WHERE e.watch_rule_id IN(SELECT id FROM rules)),'[]'::jsonb),
 'outbox',COALESCE((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.id) FROM wallet_notification_outbox o WHERE o.debox_user_id=$1),'[]'::jsonb),
 'preferences',(SELECT to_jsonb(p) FROM user_preferences p WHERE p.debox_user_id=$1))`, owner, ruleIDs, combinationIDs).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return string(state)
}

func interactionQuotaAssertCount(t *testing.T, ctx context.Context, db *Store, owner string, wantInteractions, wantSlots int64) {
	t.Helper()
	interactions, err := db.CountUserInteractionRules(ctx, owner)
	if err != nil || interactions != wantInteractions {
		t.Fatalf("active interactions=%d want=%d error=%v", interactions, wantInteractions, err)
	}
	slots, err := db.CountUserWatchRules(ctx, owner)
	if err != nil || slots != wantSlots {
		t.Fatalf("shared slots=%d want=%d error=%v", slots, wantSlots, err)
	}
}

func TestPostgresInteractionQuotaSingleAndCombinationRace(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	policy := interactionQuotaPolicy()
	// Three bounded races vary scheduling without depending on which operation
	// wins. Only two concurrent transactions are used for each independent user.
	for round := range 3 {
		owner := fmt.Sprintf("interaction-quota-race-%d", round)
		interactionQuotaOwner(t, ctx, db, owner)
		start := make(chan struct{})
		type outcome struct {
			combination bool
			err         error
		}
		results := make(chan outcome, 2)
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			_, err := db.CreateWatchRuleWithinQuota(ctx, interactionQuotaRule(owner, 1, "address_interaction"), policy)
			results <- outcome{err: err}
		}()
		go func() {
			defer workers.Done()
			<-start
			_, err := db.CreateCombinationRuleWithinQuota(ctx, interactionQuotaCombination(owner, 10, "address_interaction", "balance_change"), policy)
			results <- outcome{combination: true, err: err}
		}()
		close(start)
		workers.Wait()
		close(results)
		var successes int
		var comboWon bool
		for result := range results {
			if result.err == nil {
				successes++
				comboWon = result.combination
			} else if !errors.Is(result.err, ErrInteractionRuleLimitReached) {
				t.Fatalf("unexpected race error: %v", result.err)
			}
		}
		if successes != 1 {
			t.Fatalf("race admitted %d operations, want exactly one", successes)
		}
		var rules, combinations, members int64
		if err := pool.QueryRow(ctx, `SELECT
 (SELECT COUNT(*) FROM watch_rules WHERE debox_user_id=$1),
 (SELECT COUNT(*) FROM combination_rules WHERE debox_user_id=$1),
 (SELECT COUNT(*) FROM combination_rule_members m JOIN combination_rules c ON c.id=m.combination_rule_id WHERE c.debox_user_id=$1)`, owner).Scan(&rules, &combinations, &members); err != nil {
			t.Fatal(err)
		}
		if comboWon {
			if rules != 2 || combinations != 1 || members != 2 {
				t.Fatalf("partial/extra group after race=%d/%d/%d", rules, combinations, members)
			}
			interactionQuotaAssertCount(t, ctx, db, owner, 1, 3)
		} else {
			if rules != 1 || combinations != 0 || members != 0 {
				t.Fatalf("losing group left partial writes=%d/%d/%d", rules, combinations, members)
			}
			interactionQuotaAssertCount(t, ctx, db, owner, 1, 1)
		}
	}
}

func TestPostgresInteractionQuotaRejectsWholeGroupZeroAndDeniedType(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, test := range []struct {
		name    string
		limit   int
		allowed []string
		group   bool
		two     bool
		want    error
	}{
		{"two_interaction_members", 1, []string{"balance_change", "address_interaction"}, true, true, ErrInteractionRuleLimitReached},
		{"zero_single", 0, []string{"balance_change", "address_interaction"}, false, false, ErrInteractionRuleLimitReached},
		{"zero_group", 0, []string{"balance_change", "address_interaction"}, true, false, ErrInteractionRuleLimitReached},
		{"type_single", 1, []string{"balance_change"}, false, false, ErrRuleTypeDenied},
		{"type_group", 1, []string{"balance_change"}, true, false, ErrRuleTypeDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := "interaction-quota-denied-" + test.name
			interactionQuotaOwner(t, ctx, db, owner)
			policy := interactionQuotaPolicy()
			policy.InteractionRuleLimit, policy.AllowedRuleTypes = test.limit, test.allowed
			before := interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil)
			var err error
			if test.group {
				second := "balance_change"
				if test.two {
					second = "address_interaction"
				}
				_, err = db.CreateCombinationRuleWithinQuota(ctx, interactionQuotaCombination(owner, 1, "address_interaction", second), policy)
			} else {
				_, err = db.CreateWatchRuleWithinQuota(ctx, interactionQuotaRule(owner, 1, "address_interaction"), policy)
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("refusal=%v want=%v", err, test.want)
			}
			if after := interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil); after != before {
				t.Fatalf("refused operation changed owner state\nbefore=%s\nafter=%s", before, after)
			}
			interactionQuotaAssertCount(t, ctx, db, owner, 0, 0)
		})
	}
}

func TestPostgresInteractionQuotaRestoreRefusalAndActiveIdempotence(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, combination := range []bool{false, true} {
		name := "single"
		if combination {
			name = "combination"
		}
		t.Run(name, func(t *testing.T) {
			owner := "interaction-quota-restore-" + name
			interactionQuotaOwner(t, ctx, db, owner)
			policy := interactionQuotaPolicy()
			var candidate WatchRule
			var combo CombinationRule
			if combination {
				combo = interactionQuotaCreateCombination(t, ctx, db, owner, 10, policy)
				interactionQuotaPauseCombination(t, ctx, pool, combo)
			} else {
				candidate = interactionQuotaCreateRule(t, ctx, db, owner, 10, "address_interaction", policy)
				interactionQuotaPauseRule(t, ctx, pool, candidate)
			}
			active := interactionQuotaCreateRule(t, ctx, db, owner, 1, "address_interaction", policy)
			before := interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil)
			var err error
			if combination {
				_, err = db.RestoreCombinationRuleWithinQuota(ctx, combo.ID, owner, policy)
			} else {
				_, err = db.RestoreWatchRuleWithinQuota(ctx, candidate.ID, owner, policy)
			}
			if !errors.Is(err, ErrInteractionRuleLimitReached) {
				t.Fatalf("restore refusal=%v", err)
			}
			if _, err := db.RestoreWatchRuleWithinQuota(ctx, active.ID, owner, policy); err != nil {
				t.Fatalf("already-active single was not idempotent: %v", err)
			}
			if after := interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil); after != before {
				t.Fatalf("denied/idempotent restore changed baseline, window or notification state\nbefore=%s\nafter=%s", before, after)
			}
			interactionQuotaAssertCount(t, ctx, db, owner, 1, 1)
			// Free the one occupied slot in the fixture, then check the positive
			// restore and a second idempotent call do not charge it twice.
			if _, err := pool.Exec(ctx, `UPDATE watch_rules SET run_status='paused',pause_reason='manual' WHERE id=$1`, active.ID); err != nil {
				t.Fatal(err)
			}
			if combination {
				_, err = db.RestoreCombinationRuleWithinQuota(ctx, combo.ID, owner, policy)
			} else {
				_, err = db.RestoreWatchRuleWithinQuota(ctx, candidate.ID, owner, policy)
			}
			if err != nil {
				t.Fatalf("restore after capacity freed: %v", err)
			}
			restored := interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil)
			if combination {
				_, err = db.RestoreCombinationRuleWithinQuota(ctx, combo.ID, owner, policy)
			} else {
				_, err = db.RestoreWatchRuleWithinQuota(ctx, candidate.ID, owner, policy)
			}
			if err != nil || interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil) != restored {
				t.Fatalf("active restore changed state or recounted its slot: %v", err)
			}
			wantSlots := int64(1)
			if combination {
				wantSlots = 3
			}
			interactionQuotaAssertCount(t, ctx, db, owner, 1, wantSlots)
		})
	}
}

func TestPostgresInteractionQuotaAutomaticReconcileSkipsFullCandidates(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, test := range []struct {
		name, active string
		limit        int
	}{
		{"active_single", "single", 1},
		{"active_combination", "combination", 1},
		{"one_slot_for_paused_single_and_group", "", 1},
		{"zero_still_restores_normal", "", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := "interaction-quota-auto-" + test.name
			interactionQuotaOwner(t, ctx, db, owner)
			fixturePolicy := interactionQuotaPolicy()
			fixturePolicy.InteractionRuleLimit = 5 // only constructing legacy stopped fixtures
			if test.active == "single" {
				interactionQuotaCreateRule(t, ctx, db, owner, 1, "address_interaction", fixturePolicy)
			} else if test.active == "combination" {
				interactionQuotaCreateCombination(t, ctx, db, owner, 1, fixturePolicy)
			}
			paused := interactionQuotaCreateRule(t, ctx, db, owner, 10, "address_interaction", fixturePolicy)
			interactionQuotaPauseRule(t, ctx, pool, paused)
			normal := interactionQuotaCreateRule(t, ctx, db, owner, 20, "balance_change", fixturePolicy)
			interactionQuotaPauseRule(t, ctx, pool, normal)
			combo := interactionQuotaCreateCombination(t, ctx, db, owner, 30, fixturePolicy)
			interactionQuotaPauseCombination(t, ctx, pool, combo)
			protectedRuleIDs := []int64{paused.ID}
			if test.active == "" && test.limit == 1 {
				// The first paused single may occupy the vacant slot. The later
				// group must observe that consumption and remain wholly stopped.
				protectedRuleIDs = []int64{combo.Members[0].Rule.ID, combo.Members[1].Rule.ID}
			} else {
				for _, member := range combo.Members {
					protectedRuleIDs = append(protectedRuleIDs, member.Rule.ID)
				}
			}
			before := interactionQuotaSnapshot(t, ctx, pool, owner, protectedRuleIDs, []int64{combo.ID})
			policy := interactionQuotaPolicy()
			policy.InteractionRuleLimit = test.limit
			result, err := db.ReconcileUserEntitlements(ctx, owner, policy)
			wantRestored := int64(1)
			if test.active == "" && test.limit == 1 {
				wantRestored = 2
			}
			if err != nil || result.WatchRulesRestored != wantRestored || result.CombinationRulesRestored != 0 {
				t.Fatalf("normal quota exhaustion became reconciliation failure: result=%+v error=%v", result, err)
			}
			if after := interactionQuotaSnapshot(t, ctx, pool, owner, protectedRuleIDs, []int64{combo.ID}); after != before {
				t.Fatalf("skipped candidates changed their state\nbefore=%s\nafter=%s", before, after)
			}
			stored, err := db.GetWatchRule(ctx, normal.ID, owner)
			if err != nil || stored == nil || stored.RunStatus != "active" || stored.LastValue == nil || *stored.LastValue != "9007199254740993" || stored.NotificationGeneration != 7 {
				t.Fatalf("ordinary rule not restored with original baseline/generation: %+v/%v", stored, err)
			}
			wantSlots := int64(2)
			if test.active == "combination" {
				wantSlots = 4
			} else if test.limit == 0 {
				wantSlots = 1
			}
			wantInteractions := int64(test.limit)
			interactionQuotaAssertCount(t, ctx, db, owner, wantInteractions, wantSlots)
			settled := interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil)
			again, err := db.ReconcileUserEntitlements(ctx, owner, policy)
			if err != nil || again.WatchRulesRestored != 0 || again.CombinationRulesRestored != 0 || interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil) != settled {
				t.Fatalf("repeat reconciliation changed existing active state: %+v/%v", again, err)
			}
		})
	}
}
