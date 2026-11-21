package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const walletRPCFixtureToken = "0x2222222222222222222222222222222222222222"

func walletRPCFixture(t *testing.T, plan, mode string) (*Store, *pgxpool.Pool, WatchRule) {
	t.Helper()
	db, pool, rule := walletDeliveryFixture(t, plan, mode)
	if _, err := pool.Exec(context.Background(), `UPDATE watch_rules SET token_address=$2 WHERE id=$1`, rule.ID, walletRPCFixtureToken); err != nil {
		t.Fatal(err)
	}
	return db, pool, walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, plan)
}

func walletRPCObservation(rule WatchRule, value string) RecordWalletObservationParams {
	params := walletTestObservation(rule, value)
	decimals, observed := 18, time.Now().UTC().Truncate(time.Microsecond)
	params.BalanceReadSource, params.BalanceReadDecimals, params.ReadObservedAt = WalletBalanceReadSource, &decimals, &observed
	return params
}

func walletRPCSeedParams(user, value string, decimals int) CreateWatchRuleParams {
	observed := time.Now().UTC().Truncate(time.Microsecond)
	token := walletRPCFixtureToken
	return CreateWatchRuleParams{DeBoxUserID: user, ChainKey: "bsc", ChainID: 56, WalletAddress: "0x3333333333333333333333333333333333333333", TokenAddress: &token, RuleType: "balance_change", Threshold: "0", NotificationChatID: user, NotificationChatType: "private", NotificationLanguage: "zh", DeliveryMode: "realtime", LastValue: &value, BalanceReadSource: WalletBalanceReadSource, BalanceReadDecimals: &decimals, BalanceReadObservedAt: &observed}
}

func walletRPCAssertNoTriggers(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, table := range []string{"alert_events", "rule_trigger_events", "aggregation_window_members", "aggregation_windows", "aggregate_notifications", "wallet_notification_outbox", "notification_detail_snapshots"} {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("source handoff produced %s rows=%d error=%v", table, n, err)
		}
	}
}

func walletRPCAssertProvenance(t *testing.T, pool *pgxpool.Pool, rule WatchRule, value string, decimals int) {
	t.Helper()
	var source, actual string
	var precision int
	var checked time.Time
	if err := pool.QueryRow(context.Background(), `SELECT read_source,decimals,baseline_value,baseline_checked_at FROM wallet_balance_read_states WHERE watch_rule_id=$1`, rule.ID).Scan(&source, &precision, &actual, &checked); err != nil {
		t.Fatal(err)
	}
	if source != WalletBalanceReadSource || precision != decimals || actual != value || rule.LastCheckedAt == nil || !checked.Equal(*rule.LastCheckedAt) {
		t.Fatalf("provenance source=%s precision=%d value=%s checked=%s rule=%+v", source, precision, actual, checked, rule)
	}
}

func TestPostgresWalletRPCSourceRequiresActualCutoverAndSuccessfulRead(t *testing.T) {
	db, pool, rule := walletRPCFixture(t, "standard", "realtime")
	ctx := context.Background()
	params := walletRPCObservation(rule, "12")
	if _, err := db.RecordWalletObservation(ctx, params); !errors.Is(err, ErrWalletBalanceSourceTransitionRequired) {
		t.Fatalf("legacy source silently joined without cutover: %v", err)
	}
	for _, mutation := range []func(*RecordWalletObservationParams){
		func(p *RecordWalletObservationParams) { p.BalanceReadDecimals = nil },
		func(p *RecordWalletObservationParams) { p.ReadObservedAt = nil },
		func(p *RecordWalletObservationParams) { v := time.Now().Add(time.Hour); p.ReadObservedAt = &v },
		func(p *RecordWalletObservationParams) { p.CurrentValue = "not-a-balance" },
		func(p *RecordWalletObservationParams) { p.BalanceReadSource = "unverified" },
	} {
		p := params
		mutation(&p)
		if _, err := db.RecordWalletObservation(ctx, p); !errors.Is(err, ErrWalletBalanceSourceUnavailable) {
			t.Fatalf("invalid RPC evidence committed: %v", err)
		}
	}
	cutoff := time.Now().Add(time.Minute)
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "future boundary must not authorize a past read"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordWalletObservation(ctx, params); !errors.Is(err, ErrWalletBalanceSourceTransitionRequired) {
		t.Fatalf("read made before cutoff silently joined: %v", err)
	}
	actual := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if *actual.LastValue != *rule.LastValue || !actual.LastCheckedAt.Equal(*rule.LastCheckedAt) {
		t.Fatal("blocked observation advanced baseline")
	}
	for _, table := range []string{"wallet_balance_read_states", "wallet_balance_source_transitions"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("blocked observation produced %s=%d error=%v", table, n, err)
		}
	}
	walletRPCAssertNoTriggers(t, pool)
}

func TestPostgresWalletRPCSourceCutoverSuppressesFirstAlertAndPreservesSubsequentRules(t *testing.T) {
	for _, mode := range []string{"realtime", "stage"} {
		t.Run(mode, func(t *testing.T) {
			plan := "free"
			if mode == "stage" {
				plan = "professional"
			}
			db, pool, rule := walletRPCFixture(t, plan, mode)
			ctx := context.Background()
			cutoff := time.Now().UTC().Add(-30 * time.Second).Truncate(time.Microsecond)
			if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "explicit isolated cutover"); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_checked_at=$2 WHERE id=$1`, rule.ID, cutoff.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, plan)
			params := walletRPCObservation(rule, "12")
			limit := int64(1)
			params.DailyAlertLimit = &limit
			first, err := db.RecordWalletObservation(ctx, params)
			if err != nil || first.Status != "source_rebased" || first.Event != nil || first.Stage != nil || first.Outbox != nil {
				t.Fatalf("first=%+v err=%v", first, err)
			}
			walletRPCAssertNoTriggers(t, pool)
			used, err := db.CountDailyAlertEvents(ctx, rule.DeBoxUserID, "Asia/Shanghai")
			if err != nil || used != 0 {
				t.Fatalf("source handoff consumed free quota=%d error=%v", used, err)
			}
			var previousSource, previousValue, nextSource, nextValue string
			var precision int
			if err := pool.QueryRow(ctx, `SELECT previous_source,previous_value,next_source,next_value,next_decimals FROM wallet_balance_source_transitions WHERE watch_rule_id=$1`, rule.ID).Scan(&previousSource, &previousValue, &nextSource, &nextValue, &precision); err != nil || previousSource != "legacy_unverified" || previousValue != "10" || nextSource != WalletBalanceReadSource || nextValue != "12" || precision != 18 {
				t.Fatalf("source audit old=%s/%s new=%s/%s decimals=%d error=%v", previousSource, previousValue, nextSource, nextValue, precision, err)
			}
			next := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, plan)
			walletRPCAssertProvenance(t, pool, next, "12", 18)
			if _, err := db.RecordWalletObservation(ctx, params); !errors.Is(err, ErrWalletObservationStale) {
				t.Fatalf("same cutover snapshot repeated: %v", err)
			}
			second := walletRPCObservation(next, "13")
			second.DailyAlertLimit = &limit
			result, err := db.RecordWalletObservation(ctx, second)
			if err != nil || result.Status == "source_rebased" || result.Outbox == nil {
				t.Fatalf("normal follow-up changed=%+v error=%v", result, err)
			}
			latest := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, plan)
			walletRPCAssertProvenance(t, pool, latest, "13", 18)
			badPrecision := walletRPCObservation(latest, "14")
			changed := 6
			badPrecision.BalanceReadDecimals = &changed
			if _, err := db.RecordWalletObservation(ctx, badPrecision); !errors.Is(err, ErrWalletBalanceSourceTransitionRequired) {
				t.Fatalf("precision silently changed after cutover: %v", err)
			}
			if _, err := db.RecordWalletObservation(ctx, walletTestObservation(latest, "14")); !errors.Is(err, ErrWalletBalanceSourceUnavailable) {
				t.Fatalf("source evidence disappeared silently: %v", err)
			}
		})
	}
}

func TestPostgresWalletRPCSourceSeedAndInitialThresholdRemainDistinct(t *testing.T) {
	db, pool, existing := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	seed := walletRPCSeedParams(existing.DeBoxUserID, "42", 0)
	rule, err := db.CreateWatchRule(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	rule.EffectivePlanCode = "standard"
	walletRPCAssertProvenance(t, pool, rule, "42", 0)
	params := walletRPCObservation(rule, "43")
	zero := 0
	params.BalanceReadDecimals = &zero
	normal, err := db.RecordWalletObservation(ctx, params)
	if err != nil || normal.Outbox == nil || normal.Status == "source_rebased" {
		t.Fatalf("new RPC-created rule treated as legacy=%+v error=%v", normal, err)
	}
	var frozen WalletNotificationPayload
	if err := json.Unmarshal(normal.Outbox.Payload, &frozen); err != nil || frozen.BalanceReadSource != WalletBalanceReadSource || frozen.BalanceReadDecimals == nil || *frozen.BalanceReadDecimals != 0 || frozen.Validate() != nil {
		t.Fatalf("new zero-decimal source evidence missing from frozen payload=%+v error=%v", frozen, err)
	}
	threshold := walletRPCSeedParams(existing.DeBoxUserID, "0", 18)
	threshold.RuleType, threshold.Threshold, threshold.LastValue = "balance_threshold", "10", nil
	thresholdRule, err := db.CreateWatchRule(ctx, threshold)
	if err != nil {
		t.Fatal(err)
	}
	thresholdRule.EffectivePlanCode = "standard"
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM wallet_balance_read_states WHERE watch_rule_id=$1`, thresholdRule.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("nil threshold baseline preseeded=%d error=%v", n, err)
	}
	initial, err := db.RecordWalletObservation(ctx, walletRPCObservation(thresholdRule, "4"))
	if err != nil || initial.Outbox == nil || initial.Status == "source_rebased" {
		t.Fatalf("initial matching threshold no longer alerts=%+v error=%v", initial, err)
	}
}

func TestPostgresWalletRPCSourceProvenanceAndObservationRollbackTogether(t *testing.T) {
	db, pool, existing := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	rule, err := db.CreateWatchRule(ctx, walletRPCSeedParams(existing.DeBoxUserID, "10", 18))
	if err != nil {
		t.Fatal(err)
	}
	rule.EffectivePlanCode = "standard"
	params := walletRPCObservation(rule, "12")
	params.BuildNotification = func(WalletObservationResult) (CreateNotificationDetailSnapshotParams, WalletNotificationPayload, error) {
		return CreateNotificationDetailSnapshotParams{}, WalletNotificationPayload{}, errors.New("injected notification build failure")
	}
	if _, err := db.RecordWalletObservation(ctx, params); err == nil {
		t.Fatal("notification build failure committed")
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_wallet_rpc_state() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected provenance write failure'; END $$; CREATE TRIGGER reject_wallet_rpc_state BEFORE UPDATE ON wallet_balance_read_states FOR EACH ROW EXECUTE FUNCTION reject_wallet_rpc_state()`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordWalletObservation(ctx, walletRPCObservation(rule, "12")); err == nil {
		t.Fatal("provenance failure committed observation")
	}
	actual := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if *actual.LastValue != "10" || !actual.LastCheckedAt.Equal(*rule.LastCheckedAt) {
		t.Fatal("failed provenance transaction advanced watch baseline")
	}
	walletRPCAssertProvenance(t, pool, actual, "10", 18)
	walletRPCAssertNoTriggers(t, pool)
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_wallet_rpc_state ON wallet_balance_read_states`); err != nil {
		t.Fatal(err)
	}
	// A writer from before the source-aware version must not leave a baseline
	// falsely labeled RPC. Its next observation is blocked rather than alerted.
	if err := db.UpdateWatchRuleValue(ctx, rule.ID, "99"); err != nil {
		t.Fatal(err)
	}
	mixed := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if _, err := db.RecordWalletObservation(ctx, walletRPCObservation(mixed, "12")); !errors.Is(err, ErrWalletBalanceSourceTransitionRequired) {
		t.Fatalf("mixed-version baseline silently trusted=%v", err)
	}
}

func TestPostgresWalletRPCSourceCreationRollsBackOnMissingEvidenceAndStateFailure(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	params := walletRPCSeedParams("source-create", "10", 18)
	params.BalanceReadObservedAt = nil
	if _, err := db.CreateWatchRule(ctx, params); !errors.Is(err, ErrWalletBalanceSourceUnavailable) {
		t.Fatalf("missing read evidence accepted=%v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_wallet_rpc_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected source seed failure'; END $$; CREATE TRIGGER reject_wallet_rpc_insert BEFORE INSERT ON wallet_balance_read_states FOR EACH ROW EXECUTE FUNCTION reject_wallet_rpc_insert()`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateWatchRule(ctx, walletRPCSeedParams("source-create", "10", 18)); err == nil {
		t.Fatal("source seed failure committed rule")
	}
	var rules, states int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM watch_rules),(SELECT COUNT(*) FROM wallet_balance_read_states)`).Scan(&rules, &states); err != nil || rules != 0 || states != 0 {
		t.Fatalf("partial create rules=%d states=%d error=%v", rules, states, err)
	}
}

func TestPostgresWalletRPCSourceConcurrentCutoverHasOneCommittedTransition(t *testing.T) {
	db, pool, rule := walletRPCFixture(t, "standard", "realtime")
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-time.Minute)
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "one-time parallel source handoff"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_checked_at=$2 WHERE id=$1`, rule.ID, cutoff.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, current := range []string{"12", "13"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			_, err := db.RecordWalletObservation(ctx, walletRPCObservation(rule, value))
			errs <- err
		}(current)
	}
	wg.Wait()
	close(errs)
	success, stale := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrWalletObservationStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	var transitions, states int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM wallet_balance_source_transitions),(SELECT COUNT(*) FROM wallet_balance_read_states)`).Scan(&transitions, &states); err != nil || success != 1 || stale != 1 || transitions != 1 || states != 1 {
		t.Fatalf("success=%d stale=%d transitions=%d states=%d error=%v", success, stale, transitions, states, err)
	}
	walletRPCAssertNoTriggers(t, pool)
}

func TestPostgresWalletRPCSourceFailedHandoffRollsBackAuditAndBaseline(t *testing.T) {
	db, pool, rule := walletRPCFixture(t, "standard", "realtime")
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-time.Minute)
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "failed handoff isolation"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_checked_at=$2 WHERE id=$1`, rule.ID, cutoff.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_rpc_handoff() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected handoff provenance failure'; END $$; CREATE TRIGGER reject_rpc_handoff BEFORE INSERT ON wallet_balance_read_states FOR EACH ROW EXECUTE FUNCTION reject_rpc_handoff()`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordWalletObservation(ctx, walletRPCObservation(rule, "12")); err == nil {
		t.Fatal("failed handoff state committed")
	}
	actual := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if *actual.LastValue != *rule.LastValue || !actual.LastCheckedAt.Equal(*rule.LastCheckedAt) {
		t.Fatal("failed handoff changed baseline")
	}
	for _, table := range []string{"wallet_balance_source_transitions", "wallet_balance_read_states", "wallet_trigger_recovery_decisions"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("failed handoff left %s=%d error=%v", table, n, err)
		}
	}
	walletRPCAssertNoTriggers(t, pool)
}

func TestPostgresWalletRPCSourceCombinationSeedsAreAtomicAndHandoffDoesNotCount(t *testing.T) {
	db, pool, existing := walletDeliveryFixture(t, "professional", "realtime")
	ctx := context.Background()
	first, second := walletRPCSeedParams(existing.DeBoxUserID, "10", 18), walletRPCSeedParams(existing.DeBoxUserID, "10", 18)
	second.WalletAddress = "0x4444444444444444444444444444444444444444"
	params := CreateCombinationRuleParams{DeBoxUserID: existing.DeBoxUserID, CycleType: "fixed", CycleMinutes: 60, NotificationChatID: existing.DeBoxUserID, NotificationChatType: "private", Members: []CreateCombinationMemberParams{{Rule: first, RequiredTriggerCount: 1}, {Rule: second, RequiredTriggerCount: 1}}}
	policy := QuotaPolicy{PlanCode: "professional", WalletLimit: 20, RuleLimit: 100, AllowedRuleTypes: []string{"balance_change"}, CombinationRules: true}
	params.Members[1].Rule.BalanceReadObservedAt = nil
	if _, err := db.CreateCombinationRuleWithinQuota(ctx, params, policy); !errors.Is(err, ErrWalletBalanceSourceUnavailable) {
		t.Fatalf("combination missing evidence accepted: %v", err)
	}
	var rules, combinations, states int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM watch_rules),(SELECT COUNT(*) FROM combination_rules),(SELECT COUNT(*) FROM wallet_balance_read_states)`).Scan(&rules, &combinations, &states); err != nil || rules != 1 || combinations != 0 || states != 0 {
		t.Fatalf("partial combination seeds rules=%d combos=%d states=%d error=%v", rules, combinations, states, err)
	}
	params.Members[1].Rule = second
	combination, err := db.CreateCombinationRuleWithinQuota(ctx, params, policy)
	if err != nil || len(combination.Members) != 2 {
		t.Fatalf("combination=%+v error=%v", combination, err)
	}
	for _, member := range combination.Members {
		rule := walletDeliveryRule(t, db, member.Rule.ID, existing.DeBoxUserID, "professional")
		walletRPCAssertProvenance(t, pool, rule, "10", 18)
	}
	// A second, legacy-source combination exercises the source handoff before
	// any member progress is counted.
	params.Members[0].Rule.WalletAddress = "0x5555555555555555555555555555555555555555"
	params.Members[1].Rule.WalletAddress = "0x6666666666666666666666666666666666666666"
	for i := range params.Members {
		params.Members[i].Rule.BalanceReadSource = ""
		params.Members[i].Rule.BalanceReadDecimals = nil
		params.Members[i].Rule.BalanceReadObservedAt = nil
	}
	legacy, err := db.CreateCombinationRuleWithinQuota(ctx, params, policy)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-time.Minute)
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "combination source handoff"); err != nil {
		t.Fatal(err)
	}
	id := legacy.Members[0].Rule.ID
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_checked_at=$2 WHERE id=$1`, id, cutoff.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	rule := walletDeliveryRule(t, db, id, existing.DeBoxUserID, "professional")
	result, err := db.RecordWalletObservation(ctx, walletRPCObservation(rule, "12"))
	if err != nil || result.Status != "source_rebased" || result.Combination != nil || result.Outbox != nil {
		t.Fatalf("combination handoff counted=%+v error=%v", result, err)
	}
	walletRPCAssertNoTriggers(t, pool)
}
