package store

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func summaryMaintenanceIntegrationFixture(t *testing.T) (*Store, *pgxpool.Pool, Subscription, []DailySummaryTarget) {
	t.Helper()
	if os.Getenv("RUN_POSTGRES_INTEGRATION") == "1" {
		config, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Fatalf("parse isolated summary test database: %v", err)
		}
		host := config.ConnConfig.Host
		if host != "localhost" && !net.ParseIP(host).IsLoopback() && !filepath.IsAbs(host) {
			t.Fatal("summary maintenance integration tests require a local database")
		}
	}
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	subscription, err := database.ActivateSubscription(ctx, "summary-maintenance-owner", "professional", 30)
	if err != nil {
		t.Fatalf("activate isolated subscription: %v", err)
	}
	if _, err := database.UpdateDailySummarySettings(ctx, subscription.DeBoxUserID, DailySummarySettings{
		Enabled: true, PushTime: "07:30", TimezoneName: "UTC", ChatType: "group",
		ChatID: "existing-group", Label: "Legacy summary label", Language: "en",
		Targets: []DailySummaryTarget{
			{ChatType: "private", ChatID: subscription.DeBoxUserID, Enabled: 1, PushTime: "18:15", Timezone: "Asia/Shanghai", Label: "Private target", Language: "zh"},
			{ChatType: "group", ChatID: "existing-group", Enabled: 1, PushTime: "07:30", Timezone: "UTC", Label: "Group target", Language: "en"},
			{ChatType: "group", ChatID: "already-disabled", Enabled: 0, PushTime: "09:45", Timezone: "Asia/Tokyo", Label: "Disabled target", Language: "en"},
		},
	}); err != nil {
		t.Fatalf("create isolated existing summary configuration: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE subscriptions
		SET daily_summary_last_sent_date = '2026-11-13',
		    scheduled_push_last_sent_at = '2026-11-13T07:31:00Z',
		    daily_summary_last_period_end_at = '2026-11-13T07:30:00Z'
		WHERE id = $1
	`, subscription.ID); err != nil {
		t.Fatalf("seed legacy summary progress: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE daily_summary_targets
		SET last_sent_date = '2026-11-13',
		    last_sent_at = '2026-11-13T07:31:00Z',
		    last_period_end_at = '2026-11-13T07:30:00Z'
		WHERE subscription_id = $1
	`, subscription.ID); err != nil {
		t.Fatalf("seed target summary progress: %v", err)
	}
	subscription, targets := readSummaryMaintenanceState(t, database, pool, subscription.ID)
	return database, pool, subscription, targets
}

func readSummaryMaintenanceState(t *testing.T, database *Store, pool *pgxpool.Pool, id int64) (Subscription, []DailySummaryTarget) {
	t.Helper()
	subscription, err := collectOne[Subscription](context.Background(), pool,
		"SELECT "+subscriptionColumns+" FROM subscriptions WHERE id = $1", id)
	if err != nil {
		t.Fatalf("read isolated subscription: %v", err)
	}
	targets, err := database.ListDailySummaryTargets(context.Background(), id)
	if err != nil {
		t.Fatalf("read isolated summary targets: %v", err)
	}
	return subscription, targets
}

func TestPostgresDisableDailySummarySettingsPreservesConfigurationAndProgress(t *testing.T) {
	database, pool, before, beforeTargets := summaryMaintenanceIntegrationFixture(t)
	ctx := context.Background()
	other, err := database.ActivateSubscription(ctx, "summary-maintenance-other", "standard", 30)
	if err != nil {
		t.Fatalf("activate other isolated subscription: %v", err)
	}
	if _, err := database.UpdateDailySummarySettings(ctx, other.DeBoxUserID, DailySummarySettings{
		Enabled: true, PushTime: "20:00", TimezoneName: "Asia/Shanghai", ChatType: "private",
		ChatID: other.DeBoxUserID, Label: "Other owner", Language: "zh",
	}); err != nil {
		t.Fatalf("set other isolated subscription summary: %v", err)
	}
	other, otherTargets := readSummaryMaintenanceState(t, database, pool, other.ID)
	got, err := database.DisableDailySummarySettings(ctx, before.DeBoxUserID, before.ID)
	if err != nil {
		t.Fatalf("disable existing summaries: %v", err)
	}
	want := before
	want.DailySummaryEnabled = 0
	wantTargets := append([]DailySummaryTarget(nil), beforeTargets...)
	for index := range wantTargets {
		wantTargets[index].Enabled = 0
	}
	after, afterTargets := readSummaryMaintenanceState(t, database, pool, before.ID)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(after, want) || !reflect.DeepEqual(afterTargets, wantTargets) {
		t.Fatalf("stop changed data beyond enabled flags:\nsubscription=%#v\ntargets=%#v", after, afterTargets)
	}
	otherAfter, otherTargetsAfter := readSummaryMaintenanceState(t, database, pool, other.ID)
	if !reflect.DeepEqual(otherAfter, other) || !reflect.DeepEqual(otherTargetsAfter, otherTargets) {
		t.Fatal("stop touched another owner's subscription or targets")
	}
	if _, err := database.DisableDailySummarySettings(ctx, before.DeBoxUserID, before.ID); err != nil {
		t.Fatalf("repeat stop of already disabled summaries: %v", err)
	}
	repeated, repeatedTargets := readSummaryMaintenanceState(t, database, pool, before.ID)
	if !reflect.DeepEqual(repeated, want) || !reflect.DeepEqual(repeatedTargets, wantTargets) {
		t.Fatal("repeat stop changed preserved configuration or delivery progress")
	}
}

func TestPostgresDisableDailySummarySettingsRejectsWrongOwnerAndInactiveSubscription(t *testing.T) {
	database, pool, before, beforeTargets := summaryMaintenanceIntegrationFixture(t)
	ctx := context.Background()
	for _, request := range []struct {
		userID string
		id     int64
	}{
		{"another-owner", before.ID},
		{before.DeBoxUserID, before.ID + 9999},
		{"", before.ID},
		{before.DeBoxUserID, 0},
	} {
		if _, err := database.DisableDailySummarySettings(ctx, request.userID, request.id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid owner/subscription error = %v, want ErrNotFound", err)
		}
	}
	after, afterTargets := readSummaryMaintenanceState(t, database, pool, before.ID)
	if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterTargets, beforeTargets) {
		t.Fatal("rejected stop changed existing summary data")
	}
	if _, err := pool.Exec(ctx, "UPDATE subscriptions SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1", before.ID); err != nil {
		t.Fatalf("expire isolated subscription: %v", err)
	}
	expired, expiredTargets := readSummaryMaintenanceState(t, database, pool, before.ID)
	if _, err := database.DisableDailySummarySettings(ctx, before.DeBoxUserID, before.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired subscription error = %v, want ErrNotFound", err)
	}
	after, afterTargets = readSummaryMaintenanceState(t, database, pool, before.ID)
	if !reflect.DeepEqual(after, expired) || !reflect.DeepEqual(afterTargets, expiredTargets) {
		t.Fatal("expired stop performed lifecycle changes or reset targets")
	}
}

func TestPostgresDisableDailySummarySettingsDoesNotCreateMissingTargets(t *testing.T) {
	database, pool, before, _ := summaryMaintenanceIntegrationFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "DELETE FROM daily_summary_targets WHERE subscription_id = $1", before.ID); err != nil {
		t.Fatalf("seed legacy-only isolated summary: %v", err)
	}
	if _, err := database.DisableDailySummarySettings(ctx, before.DeBoxUserID, before.ID); err != nil {
		t.Fatalf("stop legacy-only summary: %v", err)
	}
	after, targets := readSummaryMaintenanceState(t, database, pool, before.ID)
	want := before
	want.DailySummaryEnabled = 0
	if !reflect.DeepEqual(after, want) || len(targets) != 0 {
		t.Fatal("stop created targets or altered legacy configuration")
	}
}

func TestPostgresDisableDailySummarySettingsRollsBackOnTargetFailure(t *testing.T) {
	database, pool, before, beforeTargets := summaryMaintenanceIntegrationFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION reject_summary_maintenance_stop() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'isolated target update failure';
		END;
		$$
	`); err != nil {
		t.Fatalf("create isolated failure function: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER reject_summary_maintenance_stop
		BEFORE UPDATE OF enabled ON daily_summary_targets
		FOR EACH ROW EXECUTE FUNCTION reject_summary_maintenance_stop()
	`); err != nil {
		t.Fatalf("create isolated failure trigger: %v", err)
	}
	if _, err := database.DisableDailySummarySettings(ctx, before.DeBoxUserID, before.ID); err == nil {
		t.Fatal("target update failure was swallowed")
	}
	after, afterTargets := readSummaryMaintenanceState(t, database, pool, before.ID)
	if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterTargets, beforeTargets) {
		t.Fatal("failed transaction partially disabled subscription or changed targets")
	}
}
