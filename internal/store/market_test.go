package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
