package store

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestCreateWatchRuleWithinQuotaCommitsCheckAndInsertTogether(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	params := CreateWatchRuleParams{
		DeBoxUserID:          "user-1",
		ChainKey:             "bsc",
		ChainID:              56,
		WalletAddress:        "0x1111111111111111111111111111111111111111",
		RuleType:             "balance_change",
		Threshold:            "0",
		NotificationChatID:   "user-1",
		NotificationChatType: "private",
		NotificationLanguage: "zh",
	}
	policy := QuotaPolicy{
		PlanCode:          "standard",
		WalletLimit:       3,
		RuleLimit:         10,
		AllowedRuleTypes:  []string{"balance_change"},
		GroupNotification: false,
	}
	now := time.Now().UTC()

	mock.ExpectBegin()
	expectUserPlan(mock, "user-1", "standard")
	mock.ExpectQuery("SELECT COUNT").
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("user-1", params.WalletAddress).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("INSERT INTO watch_rules").
		WithArgs(
			params.DeBoxUserID,
			params.ChainKey,
			params.ChainID,
			params.WalletAddress,
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"",
			params.RuleType,
			params.Threshold,
			params.NotificationChatID,
			params.NotificationChatType,
			"",
			"zh",
			"standalone",
			"realtime",
			"fixed",
			int32(60),
			int64(1),
			pgxmock.AnyArg(),
		).
		WillReturnRows(watchRuleRows().AddRow(
			int64(21),
			params.DeBoxUserID,
			params.ChainKey,
			params.ChainID,
			params.WalletAddress,
			nil,
			nil,
			"",
			params.RuleType,
			params.Threshold,
			params.NotificationChatID,
			params.NotificationChatType,
			"",
			"zh",
			"standalone",
			"realtime",
			"fixed",
			int32(60),
			int64(1),
			nil,
			int32(1),
			"active",
			nil,
			nil,
			now,
		))
	mock.ExpectCommit()

	rule, err := newWithDB(mock).CreateWatchRuleWithinQuota(
		context.Background(),
		params,
		policy,
	)
	if err != nil {
		t.Fatalf("CreateWatchRuleWithinQuota(): %v", err)
	}
	if rule.ID != 21 || rule.RunStatus != "active" {
		t.Fatalf("created rule = %+v", rule)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCreateWatchRuleWithinQuotaRollsBackAtRuleLimit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectUserPlan(mock, "user-1", "free")
	mock.ExpectQuery("SELECT COUNT").
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectRollback()

	_, err = newWithDB(mock).CreateWatchRuleWithinQuota(
		context.Background(),
		CreateWatchRuleParams{
			DeBoxUserID:          "user-1",
			WalletAddress:        "0x1111111111111111111111111111111111111111",
			RuleType:             "balance_change",
			NotificationChatType: "private",
		},
		QuotaPolicy{
			PlanCode:         "free",
			WalletLimit:      1,
			RuleLimit:        1,
			AllowedRuleTypes: []string{"balance_change"},
		},
	)
	if !errors.Is(err, ErrRuleLimitReached) {
		t.Fatalf("error = %v, want %v", err, ErrRuleLimitReached)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCreateNotificationGroupWithinQuotaRejectsFourthGroup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectUserPlan(mock, "user-1", "professional")
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("user-1", "group-4").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(3)))
	mock.ExpectRollback()

	_, err = newWithDB(mock).CreateNotificationGroupWithinQuota(
		context.Background(),
		"user-1",
		"group-4",
		"Group 4",
		QuotaPolicy{
			PlanCode:          "professional",
			GroupLimit:        3,
			GroupNotification: true,
		},
	)
	if !errors.Is(err, ErrGroupLimitReached) {
		t.Fatalf("error = %v, want %v", err, ErrGroupLimitReached)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRestoreWatchRuleWithinQuotaRollsBackAtRuleLimit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	now := time.Now().UTC()
	wallet := "0x1111111111111111111111111111111111111111"
	mock.ExpectBegin()
	expectUserPlan(mock, "user-1", "standard")
	mock.ExpectQuery("FROM watch_rules").
		WithArgs(int64(17), "user-1").
		WillReturnRows(watchRuleRows().AddRow(
			int64(17),
			"user-1",
			"bsc",
			int32(56),
			wallet,
			nil,
			nil,
			"",
			"balance_change",
			"0",
			"user-1",
			"private",
			"",
			"zh",
			"standalone",
			"realtime",
			"fixed",
			int32(60),
			int64(1),
			nil,
			int32(1),
			"paused",
			nil,
			nil,
			now,
		))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(10)))
	mock.ExpectRollback()

	_, err = newWithDB(mock).RestoreWatchRuleWithinQuota(
		context.Background(),
		17,
		"user-1",
		QuotaPolicy{
			PlanCode:         "standard",
			WalletLimit:      3,
			RuleLimit:        10,
			AllowedRuleTypes: []string{"balance_change"},
		},
	)
	if !errors.Is(err, ErrRuleLimitReached) {
		t.Fatalf("error = %v, want %v", err, ErrRuleLimitReached)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCreateWithinQuotaDetectsSubscriptionChange(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectUserPlan(mock, "user-1", "professional")
	mock.ExpectRollback()

	_, err = newWithDB(mock).CreateWatchRuleWithinQuota(
		context.Background(),
		CreateWatchRuleParams{DeBoxUserID: "user-1"},
		QuotaPolicy{PlanCode: "standard"},
	)
	if !errors.Is(err, ErrSubscriptionChanged) {
		t.Fatalf("error = %v, want %v", err, ErrSubscriptionChanged)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectUserPlan(mock pgxmock.PgxPoolIface, deboxUserID string, planCode string) {
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(deboxUserID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("UPDATE subscriptions").
		WithArgs(deboxUserID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(deboxUserID).
		WillReturnRows(pgxmock.NewRows([]string{"plan_code"}).AddRow(planCode))
}

func watchRuleRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id",
		"debox_user_id",
		"chain_key",
		"chain_id",
		"wallet_address",
		"token_address",
		"target_address",
		"target_label",
		"rule_type",
		"threshold",
		"notification_chat_id",
		"notification_chat_type",
		"notification_label",
		"notification_language",
		"rule_scope",
		"delivery_mode",
		"cycle_type",
		"cycle_minutes",
		"trigger_count_threshold",
		"aggregation_anchor_at",
		"enabled",
		"run_status",
		"last_value",
		"last_checked_at",
		"created_at",
	})
}
