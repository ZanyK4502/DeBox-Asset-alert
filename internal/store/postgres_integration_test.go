package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresMigrationContract(t *testing.T) {
	store, pool := openIntegrationStore(t)
	_ = store

	expected := []string{
		"aggregate_notifications",
		"aggregation_window_members",
		"aggregation_windows",
		"alert_events",
		"auth_challenges",
		"auth_sessions",
		"combination_rule_members",
		"combination_rules",
		"complimentary_grants",
		"notification_groups",
		"orders",
		"rule_trigger_events",
		"schema_migrations",
		"subscriptions",
		"user_preferences",
		"watch_rules",
	}
	rows, err := pool.Query(context.Background(), `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		ORDER BY table_name
	`)
	if err != nil {
		t.Fatalf("list migrated tables: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var name string
		err := row.Scan(&name)
		return name, err
	})
	if err != nil {
		t.Fatalf("collect migrated tables: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(expected) {
		t.Fatalf("migrated tables = %v, want %v", got, expected)
	}
}

func TestPostgresConcurrentFreeRuleQuota(t *testing.T) {
	store, _ := openIntegrationStore(t)
	const attempts = 8
	policy := QuotaPolicy{
		PlanCode:         "free",
		WalletLimit:      1,
		RuleLimit:        1,
		AllowedRuleTypes: []string{"balance_change"},
	}
	params := CreateWatchRuleParams{
		DeBoxUserID:          "integration-free-user",
		ChainKey:             "bsc",
		ChainID:              56,
		WalletAddress:        "0x1111111111111111111111111111111111111111",
		RuleType:             "balance_change",
		Threshold:            "0",
		NotificationChatID:   "integration-free-user",
		NotificationChatType: "private",
		NotificationLanguage: "zh",
	}

	var successes atomic.Int32
	errorsSeen := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := store.CreateWatchRuleWithinQuota(context.Background(), params, policy)
			if err == nil {
				successes.Add(1)
				return
			}
			errorsSeen <- err
		}()
	}
	workers.Wait()
	close(errorsSeen)

	if got := successes.Load(); got != 1 {
		t.Fatalf("successful concurrent creates = %d, want 1", got)
	}
	for err := range errorsSeen {
		if !errors.Is(err, ErrRuleLimitReached) {
			t.Fatalf("concurrent create error = %v, want ErrRuleLimitReached", err)
		}
	}
}

func TestPostgresStageTriggerSendsOncePerWindow(t *testing.T) {
	store, pool := openIntegrationStore(t)
	ctx := context.Background()
	rule, err := store.CreateWatchRule(ctx, CreateWatchRuleParams{
		DeBoxUserID:           "integration-stage-user",
		ChainKey:              "bsc",
		ChainID:               56,
		WalletAddress:         "0x1111111111111111111111111111111111111111",
		RuleType:              "balance_change",
		Threshold:             "0",
		NotificationChatID:    "integration-stage-user",
		NotificationChatType:  "private",
		NotificationLanguage:  "zh",
		DeliveryMode:          "stage",
		CycleType:             "fixed",
		CycleMinutes:          60,
		TriggerCountThreshold: 2,
	})
	if err != nil {
		t.Fatalf("create stage rule: %v", err)
	}

	for index, wantDue := range []bool{false, true, false} {
		previous := fmt.Sprint(index)
		current := fmt.Sprint(index + 1)
		result, err := store.RecordStageTrigger(ctx, RecordStageTriggerParams{
			WatchRuleID:   rule.ID,
			DeBoxUserID:   rule.DeBoxUserID,
			PreviousValue: &previous,
			CurrentValue:  &current,
			Note:          "integration event",
		})
		if err != nil {
			t.Fatalf("record stage trigger %d: %v", index+1, err)
		}
		if result.TotalTriggerCount != int64(index+1) ||
			result.NotificationDue != wantDue {
			t.Fatalf("stage trigger %d = %#v", index+1, result)
		}
	}

	var eventCount, notificationCount int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM rule_trigger_events WHERE watch_rule_id = $1),
			(SELECT COUNT(*)
			 FROM aggregate_notifications an
			 JOIN aggregation_windows aw ON aw.id = an.aggregation_window_id
			 WHERE aw.watch_rule_id = $1)
	`, rule.ID).Scan(&eventCount, &notificationCount); err != nil {
		t.Fatalf("count stage records: %v", err)
	}
	if eventCount != 3 || notificationCount != 1 {
		t.Fatalf("stage records = events:%d notifications:%d", eventCount, notificationCount)
	}
}

func TestPostgresCombinationSendsAfterEveryMemberReachesThreshold(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	userID := "integration-combination-user"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'professional', 'active', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create professional subscription: %v", err)
	}
	memberRule := func(wallet string) CreateCombinationMemberParams {
		return CreateCombinationMemberParams{
			Rule: CreateWatchRuleParams{
				DeBoxUserID:   userID,
				ChainKey:      "bsc",
				ChainID:       56,
				WalletAddress: wallet,
				RuleType:      "balance_change",
				Threshold:     "0",
			},
			RequiredTriggerCount: 1,
		}
	}
	first := memberRule("0x1111111111111111111111111111111111111111")
	first.RequiredTriggerCount = 2
	combination, err := database.CreateCombinationRuleWithinQuota(
		ctx,
		CreateCombinationRuleParams{
			DeBoxUserID:          userID,
			Note:                 "integration combination",
			CycleType:            "fixed",
			CycleMinutes:         60,
			NotificationChatID:   userID,
			NotificationChatType: "private",
			NotificationLanguage: "en",
			Members: []CreateCombinationMemberParams{
				first,
				memberRule("0x2222222222222222222222222222222222222222"),
			},
		},
		QuotaPolicy{
			PlanCode:          "professional",
			WalletLimit:       20,
			RuleLimit:         100,
			AllowedRuleTypes:  []string{"balance_change"},
			GroupNotification: true,
			CombinationRules:  true,
		},
	)
	if err != nil {
		t.Fatalf("create combination rule: %v", err)
	}
	if len(combination.Members) != 2 {
		t.Fatalf("combination members = %d, want 2", len(combination.Members))
	}

	sequence := []struct {
		memberIndex int
		wantDue     bool
	}{
		{memberIndex: 0, wantDue: false},
		{memberIndex: 0, wantDue: false},
		{memberIndex: 1, wantDue: true},
		{memberIndex: 1, wantDue: false},
	}
	for index, item := range sequence {
		current := fmt.Sprint(index + 1)
		result, err := database.RecordCombinationTrigger(ctx, RecordCombinationTriggerParams{
			WatchRuleID:  combination.Members[item.memberIndex].WatchRuleID,
			DeBoxUserID:  userID,
			CurrentValue: &current,
			Note:         "combination event",
		})
		if err != nil {
			t.Fatalf("record combination trigger %d: %v", index+1, err)
		}
		if result.NotificationDue != item.wantDue {
			t.Fatalf("trigger %d due = %v, want %v", index+1, result.NotificationDue, item.wantDue)
		}
	}

	var eventCount, notificationCount int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*)
			 FROM rule_trigger_events rte
			 JOIN aggregation_windows aw ON aw.id = rte.aggregation_window_id
			 WHERE aw.combination_rule_id = $1),
			(SELECT COUNT(*)
			 FROM aggregate_notifications an
			 JOIN aggregation_windows aw ON aw.id = an.aggregation_window_id
			 WHERE aw.combination_rule_id = $1)
	`, combination.ID).Scan(&eventCount, &notificationCount); err != nil {
		t.Fatalf("count combination records: %v", err)
	}
	if eventCount != 4 || notificationCount != 1 {
		t.Fatalf("combination records = events:%d notifications:%d", eventCount, notificationCount)
	}

	history, err := database.ListAggregationEventHistory(ctx, userID, 0, 10)
	if err != nil {
		t.Fatalf("list combination history: %v", err)
	}
	if len(history.Events) != 4 ||
		history.Stats.EventCount != 4 ||
		history.Stats.CombinationEventCount != 4 ||
		history.Stats.StageEventCount != 0 {
		t.Fatalf("combination history = %#v", history)
	}
	for _, event := range history.Events {
		if event.SourceType != "combination" ||
			event.CombinationRuleID == nil ||
			*event.CombinationRuleID != combination.ID ||
			event.CombinationNote != combination.Note ||
			event.NotificationKind != "combination" {
			t.Fatalf("combination history event = %#v", event)
		}
	}
}

func TestPostgresGroupDeletionFallsBackAllRuleTargetsToPrivate(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	userID := "integration-group-fallback-user"
	groupID := "integration-group"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'professional', 'active', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create professional subscription: %v", err)
	}
	group, err := database.CreateNotificationGroup(ctx, userID, groupID, "Integration Group")
	if err != nil {
		t.Fatalf("create notification group: %v", err)
	}
	standalone, err := database.CreateWatchRule(ctx, CreateWatchRuleParams{
		DeBoxUserID:          userID,
		ChainKey:             "bsc",
		ChainID:              56,
		WalletAddress:        "0x1111111111111111111111111111111111111111",
		RuleType:             "balance_change",
		Threshold:            "0",
		NotificationChatID:   groupID,
		NotificationChatType: "group",
		NotificationLabel:    group.Name,
	})
	if err != nil {
		t.Fatalf("create standalone rule: %v", err)
	}
	member := func(wallet string) CreateCombinationMemberParams {
		return CreateCombinationMemberParams{
			Rule: CreateWatchRuleParams{
				DeBoxUserID:   userID,
				ChainKey:      "bsc",
				ChainID:       56,
				WalletAddress: wallet,
				RuleType:      "balance_change",
				Threshold:     "0",
			},
			RequiredTriggerCount: 1,
		}
	}
	combination, err := database.CreateCombinationRuleWithinQuota(
		ctx,
		CreateCombinationRuleParams{
			DeBoxUserID:          userID,
			Note:                 "group fallback combination",
			CycleType:            "fixed",
			CycleMinutes:         60,
			NotificationChatID:   groupID,
			NotificationChatType: "group",
			NotificationLabel:    group.Name,
			Members: []CreateCombinationMemberParams{
				member("0x2222222222222222222222222222222222222222"),
				member("0x3333333333333333333333333333333333333333"),
			},
		},
		QuotaPolicy{
			PlanCode:          "professional",
			WalletLimit:       20,
			RuleLimit:         100,
			AllowedRuleTypes:  []string{"balance_change"},
			GroupNotification: true,
			CombinationRules:  true,
		},
	)
	if err != nil {
		t.Fatalf("create combination rule: %v", err)
	}

	if _, err := database.DeleteNotificationGroup(ctx, group.ID, userID); err != nil {
		t.Fatalf("delete notification group: %v", err)
	}

	var standaloneChatID, standaloneChatType, standaloneLabel string
	if err := pool.QueryRow(ctx, `
		SELECT notification_chat_id, notification_chat_type, notification_label
		FROM watch_rules
		WHERE id = $1
	`, standalone.ID).Scan(
		&standaloneChatID,
		&standaloneChatType,
		&standaloneLabel,
	); err != nil {
		t.Fatalf("read standalone fallback: %v", err)
	}
	if standaloneChatID != userID ||
		standaloneChatType != "private" ||
		standaloneLabel != "私聊通知" {
		t.Fatalf(
			"standalone fallback = %q/%q/%q",
			standaloneChatID,
			standaloneChatType,
			standaloneLabel,
		)
	}

	var combinationChatID, combinationChatType, combinationLabel string
	if err := pool.QueryRow(ctx, `
		SELECT notification_chat_id, notification_chat_type, notification_label
		FROM combination_rules
		WHERE id = $1
	`, combination.ID).Scan(
		&combinationChatID,
		&combinationChatType,
		&combinationLabel,
	); err != nil {
		t.Fatalf("read combination fallback: %v", err)
	}
	if combinationChatID != userID ||
		combinationChatType != "private" ||
		combinationLabel != "私聊通知" {
		t.Fatalf(
			"combination fallback = %q/%q/%q",
			combinationChatID,
			combinationChatType,
			combinationLabel,
		)
	}
	for _, combinationMember := range combination.Members {
		var memberChatID, memberChatType, memberLabel string
		if err := pool.QueryRow(ctx, `
			SELECT notification_chat_id, notification_chat_type, notification_label
			FROM watch_rules
			WHERE id = $1
		`, combinationMember.WatchRuleID).Scan(
			&memberChatID,
			&memberChatType,
			&memberLabel,
		); err != nil {
			t.Fatalf("read combination member fallback: %v", err)
		}
		if memberChatID != userID ||
			memberChatType != "private" ||
			memberLabel != "私聊通知" {
			t.Fatalf(
				"combination member fallback = %q/%q/%q",
				memberChatID,
				memberChatType,
				memberLabel,
			)
		}
	}
}

func TestPostgresAggregationHistoryIsScopedPaginatedAndLimitedToThirtyDays(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	userID := "integration-history-user"
	rule, err := database.CreateWatchRule(ctx, CreateWatchRuleParams{
		DeBoxUserID:           userID,
		ChainKey:              "bsc",
		ChainID:               56,
		WalletAddress:         "0x1111111111111111111111111111111111111111",
		RuleType:              "balance_change",
		Threshold:             "0",
		NotificationChatID:    userID,
		NotificationChatType:  "private",
		NotificationLanguage:  "zh",
		DeliveryMode:          "stage",
		CycleType:             "fixed",
		CycleMinutes:          60,
		TriggerCountThreshold: 2,
	})
	if err != nil {
		t.Fatalf("create history stage rule: %v", err)
	}

	eventIDs := make([]int64, 0, 4)
	for index := range 4 {
		previous := fmt.Sprint(index)
		current := fmt.Sprint(index + 1)
		result, err := database.RecordStageTrigger(ctx, RecordStageTriggerParams{
			WatchRuleID:   rule.ID,
			DeBoxUserID:   userID,
			PreviousValue: &previous,
			CurrentValue:  &current,
			Note:          fmt.Sprintf("history event %d", index+1),
		})
		if err != nil {
			t.Fatalf("record history event %d: %v", index+1, err)
		}
		eventIDs = append(eventIDs, result.TriggerEventID)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE rule_trigger_events
		SET created_at = NOW() - INTERVAL '31 days',
		    detected_at = NOW() - INTERVAL '31 days'
		WHERE id = $1
	`, eventIDs[0]); err != nil {
		t.Fatalf("age first history event: %v", err)
	}

	firstPage, err := database.ListAggregationEventHistory(ctx, userID, 0, 2)
	if err != nil {
		t.Fatalf("list first history page: %v", err)
	}
	if len(firstPage.Events) != 2 ||
		!firstPage.HasMore ||
		firstPage.NextBeforeID == nil ||
		firstPage.RetentionDays != 30 {
		t.Fatalf("first history page = %#v", firstPage)
	}
	if firstPage.Stats.EventCount != 3 ||
		firstPage.Stats.StageEventCount != 3 ||
		firstPage.Stats.CombinationEventCount != 0 ||
		firstPage.Stats.NotificationCount != 1 {
		t.Fatalf("history stats = %#v", firstPage.Stats)
	}
	for _, event := range firstPage.Events {
		if event.SourceType != "rule" ||
			event.WatchRuleID != rule.ID ||
			event.NotificationKind != "stage" ||
			event.Note == "" {
			t.Fatalf("history event = %#v", event)
		}
	}

	secondPage, err := database.ListAggregationEventHistory(
		ctx,
		userID,
		*firstPage.NextBeforeID,
		2,
	)
	if err != nil {
		t.Fatalf("list second history page: %v", err)
	}
	if len(secondPage.Events) != 1 ||
		secondPage.HasMore ||
		secondPage.NextBeforeID != nil {
		t.Fatalf("second history page = %#v", secondPage)
	}

	otherUserPage, err := database.ListAggregationEventHistory(
		ctx,
		"integration-other-user",
		0,
		50,
	)
	if err != nil {
		t.Fatalf("list other user history: %v", err)
	}
	if len(otherUserPage.Events) != 0 ||
		otherUserPage.Stats.EventCount != 0 {
		t.Fatalf("other user history = %#v", otherUserPage)
	}
}

func TestPostgresTransactionHashIsGloballyUnique(t *testing.T) {
	store, _ := openIntegrationStore(t)
	token := "0x55d398326f99059ff775485246999027b3197955"
	first := createIntegrationOrder(t, store, "integration-user-1", token)
	second := createIntegrationOrder(t, store, "integration-user-2", token)
	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if _, err := store.ClaimOrderTransaction(
		context.Background(), first.ID, first.DeBoxUserID, first.PayerAddress, txHash,
	); err != nil {
		t.Fatalf("claim first transaction: %v", err)
	}
	if _, err := store.ClaimOrderTransaction(
		context.Background(), second.ID, second.DeBoxUserID, second.PayerAddress, txHash,
	); !errors.Is(err, ErrOrderTransactionUsed) {
		t.Fatalf("claim duplicate transaction error = %v, want ErrOrderTransactionUsed", err)
	}
}

func openIntegrationStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	if os.Getenv("RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("set RUN_POSTGRES_INTEGRATION=1 to run PostgreSQL integration tests")
	}
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required; production DATABASE_URL is intentionally ignored")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("open integration admin pool: %v", err)
	}
	t.Cleanup(adminPool.Close)
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatalf("ping integration database: %v", err)
	}

	schema := fmt.Sprintf("go_contract_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create integration schema: %v", err)
	}

	schemaConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse schema pool config: %v", err)
	}
	if schemaConfig.ConnConfig.RuntimeParams == nil {
		schemaConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	schemaConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, schemaConfig)
	if err != nil {
		t.Fatalf("open integration schema pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = adminPool.Exec(dropCtx, "DROP SCHEMA "+quotedSchema+" CASCADE")
	})

	store := &Store{db: pool, pool: pool}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate integration schema: %v", err)
	}
	return store, pool
}

func createIntegrationOrder(t *testing.T, store *Store, userID, token string) Order {
	t.Helper()
	order, err := store.CreateOrder(context.Background(), CreateOrderParams{
		DeBoxUserID:      userID,
		PayerAddress:     "0x2222222222222222222222222222222222222222",
		PlanCode:         "standard",
		ChainKey:         "bsc",
		ChainID:          56,
		TokenAddress:     &token,
		TokenSymbol:      "USDT",
		TokenDecimals:    18,
		TotalAmount:      "10",
		RecipientAddress: "0x3333333333333333333333333333333333333333",
	})
	if err != nil {
		t.Fatalf("create order for %s: %v", userID, err)
	}
	return order
}
