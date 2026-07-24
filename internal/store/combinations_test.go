package store

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestAllCombinationMembersReached(t *testing.T) {
	t.Parallel()
	if allCombinationMembersReached([]CombinationMemberProgress{
		{TriggerCount: 2, RequiredTriggerCount: 2},
	}) {
		t.Fatal("one member must not complete a combination")
	}
	if allCombinationMembersReached([]CombinationMemberProgress{
		{TriggerCount: 2, RequiredTriggerCount: 2},
		{TriggerCount: 0, RequiredTriggerCount: 1},
	}) {
		t.Fatal("an unmet member completed the combination")
	}
	if !allCombinationMembersReached([]CombinationMemberProgress{
		{TriggerCount: 3, RequiredTriggerCount: 2},
		{TriggerCount: 1, RequiredTriggerCount: 1},
	}) {
		t.Fatal("all reached members did not complete the combination")
	}
}

func TestCombinationWindowBounds(t *testing.T) {
	t.Parallel()
	anchor := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 24, 10, 37, 0, 0, time.UTC)
	start, end := combinationWindowBounds(combinationTriggerConfiguration{
		CycleType:           "fixed",
		CycleMinutes:        15,
		AggregationAnchorAt: anchor,
	}, now)
	if !start.Equal(time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)) ||
		!end.Equal(time.Date(2026, 7, 24, 10, 45, 0, 0, time.UTC)) {
		t.Fatalf("fixed combination window = %s - %s", start, end)
	}
}

func TestCombinationRuleSlotCostIncludesParent(t *testing.T) {
	t.Parallel()

	if got := combinationRuleSlotCost(2); got != 3 {
		t.Fatalf("combinationRuleSlotCost(2) = %d, want 3", got)
	}
}

func TestCreateCombinationRuleRejectsWhenParentExceedsQuota(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	const userID = "combination-quota-user"
	params := CreateCombinationRuleParams{
		DeBoxUserID:          userID,
		CycleType:            "fixed",
		CycleMinutes:         60,
		NotificationChatID:   userID,
		NotificationChatType: "private",
		Members: []CreateCombinationMemberParams{
			{
				Rule: CreateWatchRuleParams{
					WalletAddress: "0x1111111111111111111111111111111111111111",
					RuleType:      "balance_change",
				},
				RequiredTriggerCount: 1,
			},
			{
				Rule: CreateWatchRuleParams{
					WalletAddress: "0x2222222222222222222222222222222222222222",
					RuleType:      "balance_change",
				},
				RequiredTriggerCount: 1,
			},
		},
	}
	policy := QuotaPolicy{
		PlanCode:         "professional",
		WalletLimit:      20,
		RuleLimit:        9,
		AllowedRuleTypes: []string{"balance_change"},
		CombinationRules: true,
	}

	mock.ExpectBegin()
	expectUserPlan(mock, userID, "professional")
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(7)))
	mock.ExpectRollback()

	_, err = newWithDB(mock).CreateCombinationRuleWithinQuota(
		context.Background(),
		params,
		policy,
	)
	if !errors.Is(err, ErrRuleLimitReached) {
		t.Fatalf("error = %v, want %v", err, ErrRuleLimitReached)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestAttachRecentCombinationEventsGroupsAndLimitsEachMember(t *testing.T) {
	t.Parallel()

	progress := []CombinationMemberProgress{
		{WatchRuleID: 7},
		{WatchRuleID: 8},
	}
	events := []combinationRecentEvent{
		{WatchRuleID: 7, Note: "member 7 event 1"},
		{WatchRuleID: 7, Note: "member 7 event 2"},
		{WatchRuleID: 7, Note: "member 7 event 3"},
		{WatchRuleID: 7, Note: "member 7 event 4"},
		{WatchRuleID: 8, Note: "member 8 event 1"},
		{WatchRuleID: 99, Note: "unknown member"},
	}

	got := attachRecentCombinationEvents(progress, events)
	if len(got[0].RecentNotes) != 3 {
		t.Fatalf("member 7 recent notes = %v, want 3", got[0].RecentNotes)
	}
	if len(got[1].RecentNotes) != 1 || got[1].RecentNotes[0] != "member 8 event 1" {
		t.Fatalf("member 8 recent notes = %v", got[1].RecentNotes)
	}
}
