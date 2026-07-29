package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestNormalizeMarketAddress(t *testing.T) {
	t.Parallel()

	got, err := normalizeMarketAddress(" 0xABCDEFabcdefABCDEFabcdefABCDEFabcdefABCD ")
	if err != nil {
		t.Fatalf("normalizeMarketAddress(): %v", err)
	}
	if got != "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd" {
		t.Fatalf("normalized address = %q", got)
	}
	if _, err := normalizeMarketAddress("0x1234"); !errors.Is(err, ErrInvalidMarketAddress) {
		t.Fatalf("invalid address error = %v", err)
	}
}

func TestUpsertMarketAddressLabelRequiresLabel(t *testing.T) {
	t.Parallel()

	_, err := (&Store{}).UpsertMarketAddressLabel(
		context.Background(),
		UpsertMarketAddressLabelParams{
			DeBoxUserID:     "user-1",
			MarketProjectID: 1,
			ChainKey:        "bsc",
			Address:         "0x1111111111111111111111111111111111111111",
			Label:           "   ",
		},
	)
	if !errors.Is(err, ErrInvalidMarketAddressLabel) {
		t.Fatalf("empty label error = %v, want ErrInvalidMarketAddressLabel", err)
	}
}

func TestNormalizeMarketRuleParamsUsesStableDefaults(t *testing.T) {
	t.Parallel()

	params := CreateMarketRuleParams{
		RuleType:             " MARKET_PRICE_ABOVE ",
		ThresholdValue:       "",
		ThresholdUnit:        "not-a-unit",
		Sensitivity:          "",
		CooldownSeconds:      -1,
		DeliveryMode:         "stage",
		CycleType:            "follow",
		NotificationChatType: "anything",
		NotificationLanguage: "EN",
	}
	normalizeMarketRuleParams(&params)

	if params.RuleType != "market_price_above" ||
		params.DeploymentScope != "all" ||
		params.PoolScope != "primary" ||
		params.CooldownScope != "chain" ||
		params.ThresholdValue != "0" ||
		params.ThresholdUnit != "usd" ||
		params.Sensitivity != "balanced" ||
		params.CooldownSeconds != 0 ||
		params.DeliveryMode != "stage" ||
		params.CycleType != "follow" ||
		params.CycleMinutes != 60 ||
		params.TriggerCountThreshold != 1 ||
		params.NotificationChatType != "private" ||
		params.NotificationLanguage != "en" {
		t.Fatalf("normalized market rule params = %+v", params)
	}
}

func TestNormalizeInitialMarketNotificationStatusAllowsAuditSkip(t *testing.T) {
	t.Parallel()

	if status := normalizeInitialMarketNotificationStatus(" skipped "); status != "skipped" {
		t.Fatalf("normalized status = %q, want skipped", status)
	}
	if status := normalizeInitialMarketNotificationStatus("unknown"); status != "pending" {
		t.Fatalf("fallback status = %q, want pending", status)
	}
}

func TestListMarketRuleEventHistoryReturnsOnlyRuleAuditRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	now := time.Now().UTC()
	currentValue := "750"
	poolID := int64(4)
	transactionHash := "0xabc"
	tokenAmount := "100"
	usdValue := "750"
	mock.ExpectQuery("FROM market_rule_events mre").
		WithArgs(int64(7), "user-7", int64(0), "bsc", "market_large_buy", int64(0), "", 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "market_rule_id", "market_event_id", "rule_type",
			"threshold_value", "threshold_unit", "previous_value", "current_value",
			"note", "notification_status", "notification_error",
			"notification_sent_at", "created_at", "market_pool_id", "chain_key",
			"event_type", "transaction_hash", "wallet_address", "token_amount",
			"usd_value", "source", "occurred_at", "notification_successful",
			"address_label", "address_excluded", "combination_notes",
		}).AddRow(
			int64(12), int64(8), int64(9), "market_large_buy",
			"500", "usd", nil, &currentValue, "large buy", "sent", "",
			&now, now, &poolID, "bsc", "buy", &transactionHash, nil, &tokenAmount,
			&usdValue, "nodit", now, true, "项目方金库", false,
			[]string{"大额异动组合", "金库风险组合"},
		))

	events, err := newWithDB(mock).ListMarketRuleEventHistory(
		context.Background(),
		7,
		"user-7",
		MarketEventFilter{Limit: 50, ChainKey: " BSC ", RuleType: " MARKET_LARGE_BUY "},
	)
	if err != nil {
		t.Fatalf("ListMarketRuleEventHistory(): %v", err)
	}
	if len(events) != 1 || !events[0].NotificationSuccessful ||
		events[0].RuleType != "market_large_buy" ||
		events[0].AddressLabel != "项目方金库" ||
		len(events[0].CombinationNotes) != 2 ||
		events[0].CombinationNotes[0] != "大额异动组合" ||
		events[0].CombinationNotes[1] != "金库风险组合" ||
		events[0].CurrentValue == nil || *events[0].CurrentValue != "750" {
		t.Fatalf("unexpected history rows: %#v", events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCreateMarketProjectWithinQuotaIsAtomic(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	const userID = "market-project-user"
	mock.ExpectBegin()
	expectUserPlan(mock, userID, "standard")
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(
			userID,
			int64(56),
			"0x1111111111111111111111111111111111111111",
		).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectRollback()

	_, err = newWithDB(mock).CreateMarketProjectWithinQuota(
		context.Background(),
		CreateMarketProjectParams{
			DeBoxUserID:   userID,
			ChainKey:      "bsc",
			ChainID:       56,
			TokenAddress:  "0x1111111111111111111111111111111111111111",
			TokenDecimals: 18,
		},
		QuotaPolicy{
			PlanCode:           "standard",
			MarketProjectLimit: 1,
		},
	)
	if !errors.Is(err, ErrMarketProjectLimitReached) {
		t.Fatalf("error = %v, want ErrMarketProjectLimitReached", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestDeleteArchivedMarketProjectDeletesOwnedArchive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	const userID = "delete-market-project-user"
	const projectID int64 = 41
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM market_projects").
		WithArgs(projectID, userID).
		WillReturnRows(marketProjectRowsForDelete(projectID, userID, "archived"))
	mock.ExpectExec("DELETE FROM market_combination_rules").
		WithArgs(projectID, userID).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectExec("DELETE FROM market_projects").
		WithArgs(projectID, userID).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	if err := newWithDB(mock).DeleteArchivedMarketProject(
		context.Background(), projectID, userID,
	); err != nil {
		t.Fatalf("DeleteArchivedMarketProject(): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestDeleteArchivedMarketProjectRejectsActiveProject(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	const userID = "active-market-project-user"
	const projectID int64 = 42
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM market_projects").
		WithArgs(projectID, userID).
		WillReturnRows(marketProjectRowsForDelete(projectID, userID, "active"))
	mock.ExpectRollback()

	err = newWithDB(mock).DeleteArchivedMarketProject(
		context.Background(), projectID, userID,
	)
	if !errors.Is(err, ErrMarketProjectNotArchived) {
		t.Fatalf("error = %v, want ErrMarketProjectNotArchived", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func marketProjectRowsForDelete(projectID int64, userID, status string) *pgxmock.Rows {
	now := time.Now().UTC()
	return pgxmock.NewRows([]string{
		"id", "debox_user_id", "market_asset_id", "chain_key", "chain_id",
		"token_address", "token_name", "token_symbol", "token_decimals",
		"total_supply_raw", "status", "pause_reason", "four_meme_status",
		"main_pool_id", "metadata", "last_discovered_at", "created_at", "updated_at",
	}).AddRow(
		projectID, userID, nil, "bsc", int64(56),
		"0x1111111111111111111111111111111111111111", "Token", "TKN", int32(18),
		nil, status, "", "not_applicable", nil, []byte(`{}`), nil, now, now,
	)
}

func TestListMarketProjectsIncludesCanonicalIdentityForDuplicateDetection(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	const userID = "market-list-user"
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT listed\\.\\*").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "debox_user_id", "market_asset_id", "chain_key", "chain_id",
			"token_address", "token_name", "token_symbol", "token_decimals",
			"total_supply_raw", "status", "pause_reason", "four_meme_status",
			"main_pool_id", "metadata", "last_discovered_at", "created_at", "updated_at",
			"identity_source", "canonical_asset_id",
		}).AddRow(
			int64(9), userID, nil, "bsc", int64(56),
			"0x1111111111111111111111111111111111111111", "Token", "TKN", int32(18),
			nil, "archived", "", "not_applicable", nil, []byte(`{}`), nil, now, now,
			"coingecko", "token-id",
		))

	projects, err := newWithDB(mock).ListMarketProjects(context.Background(), userID, true)
	if err != nil {
		t.Fatalf("ListMarketProjects(): %v", err)
	}
	if len(projects) != 1 || projects[0].IdentitySource != "coingecko" ||
		projects[0].CanonicalAssetID != "token-id" || projects[0].Status != "archived" {
		t.Fatalf("projects = %+v", projects)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestNormalizedJSONRejectsInvalidPayload(t *testing.T) {
	t.Parallel()

	if got := string(normalizedJSON(json.RawMessage(`{"ok":true}`))); got != `{"ok":true}` {
		t.Fatalf("valid JSON = %s", got)
	}
	if got := string(normalizedJSON(json.RawMessage(`{`))); got != `{}` {
		t.Fatalf("invalid JSON = %s", got)
	}
	if got := nullableJSON(json.RawMessage(`{`)); got != nil {
		t.Fatalf("invalid nullable JSON = %#v", got)
	}
}
