package management

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

// Keep the existing repository fake compatible without editing its source file.
func (f *fakeRepository) DisableDailySummarySettings(context.Context, string, int64) (store.Subscription, error) {
	return store.Subscription{}, errors.New("unexpected maintenance summary stop")
}

type summaryMaintenanceRepository struct {
	*fakeRepository
	userID string
	id     int64
	calls  int
	value  store.Subscription
	err    error
}

func (f *summaryMaintenanceRepository) DisableDailySummarySettings(_ context.Context, userID string, id int64) (store.Subscription, error) {
	f.userID, f.id = userID, id
	f.calls++
	return f.value, f.err
}

type summaryMaintenanceEntitlements struct {
	*fakeEntitlements
	before, after subscription.Entitlement
	firstErr      error
	secondErr     error
	calls         int
}

func (f *summaryMaintenanceEntitlements) Entitlement(context.Context, string) (subscription.Entitlement, error) {
	f.calls++
	if f.calls == 1 {
		return f.before, f.firstErr
	}
	return f.after, f.secondErr
}

func summaryMaintenanceFixture() (*Service, *summaryMaintenanceRepository, *summaryMaintenanceEntitlements) {
	last := time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)
	before := store.Subscription{
		ID: 17, DeBoxUserID: "summary-owner", PlanCode: plans.Standard,
		DailySummaryEnabled: 1, DailySummaryTime: "07:30", DailySummaryTimezone: "UTC",
		DailySummaryChatType: "group", DailySummaryChatID: "existing-group",
		DailySummaryLabel: "Existing legacy label", DailySummaryLanguage: "en",
		DailySummaryLastSentDate: "2026-11-13", DailySummaryLastPeriodEndAt: &last,
	}
	targets := []store.DailySummaryTarget{
		{ID: 31, SubscriptionID: before.ID, ChatType: "private", ChatID: before.DeBoxUserID, Enabled: 1,
			PushTime: "18:15", Timezone: "Asia/Shanghai", Label: "Private target", Language: "zh", LastSentDate: "2026-11-13", LastPeriodEndAt: &last},
		{ID: 32, SubscriptionID: before.ID, ChatType: "group", ChatID: "existing-group", Enabled: 1,
			PushTime: "07:30", Timezone: "UTC", Label: "Group target", Language: "en", LastSentDate: "2026-11-13", LastPeriodEndAt: &last},
	}
	after := before
	after.DailySummaryEnabled = 0
	afterTargets := append([]store.DailySummaryTarget(nil), targets...)
	for index := range afterTargets {
		afterTargets[index].Enabled = 0
	}
	repository := &summaryMaintenanceRepository{fakeRepository: &fakeRepository{}, value: after}
	entitlements := &summaryMaintenanceEntitlements{
		fakeEntitlements: &fakeEntitlements{},
		// A stop must not depend on the current plan supporting summaries.
		before: subscription.Entitlement{Subscription: &before, Plan: plans.Plan{Code: plans.Free}, SummarySettings: subscription.SummarySettings{Targets: targets}},
		after:  subscription.Entitlement{Subscription: &after, SummarySettings: subscription.SummarySettings{Targets: afterTargets}},
	}
	return New(Dependencies{Repository: repository, Entitlements: entitlements}), repository, entitlements
}

func TestDisableSummarySettingsPreservesExistingConfiguration(t *testing.T) {
	for _, withFields := range []bool{false, true} {
		t.Run(map[bool]string{false: "minimal stop", true: "matching existing fields"}[withFields], func(t *testing.T) {
			service, repository, entitlements := summaryMaintenanceFixture()
			input := SummarySettingsInput{}
			if withFields {
				existing := entitlements.before.Subscription
				input.PushTime, input.Timezone = existing.DailySummaryTime, existing.DailySummaryTimezone
				input.ChatType, input.ChatID = existing.DailySummaryChatType, existing.DailySummaryChatID
				input.Label, input.Language = existing.DailySummaryLabel, existing.DailySummaryLanguage
				for _, target := range entitlements.before.SummarySettings.Targets {
					disabled := false
					input.Targets = append(input.Targets, SummaryTargetInput{
						ChatType: target.ChatType, ChatID: target.ChatID, Enabled: &disabled,
						PushTime: target.PushTime, Timezone: target.Timezone, Label: target.Label, Language: target.Language,
					})
				}
			}
			result, err := service.DisableSummarySettings(context.Background(), "summary-owner", input)
			if err != nil {
				t.Fatalf("DisableSummarySettings: %v", err)
			}
			if repository.calls != 1 || repository.userID != "summary-owner" || repository.id != 17 {
				t.Fatalf("unexpected stop scope: %#v", repository)
			}
			if !reflect.DeepEqual(result.Subscription, repository.value) || !reflect.DeepEqual(result.Entitlement, entitlements.after) {
				t.Fatalf("stop did not return the preserved subscription/updated entitlement: %#v", result)
			}
			if entitlements.calls != 2 {
				t.Fatalf("entitlement calls = %d, want 2", entitlements.calls)
			}
		})
	}
}

func TestDisableSummarySettingsRejectsConfigurationChanges(t *testing.T) {
	disabled, enabled := false, true
	tests := []struct {
		name  string
		input SummarySettingsInput
	}{
		{"top level enable", SummarySettingsInput{Enabled: true}},
		{"nested enable", SummarySettingsInput{Targets: []SummaryTargetInput{{ChatType: "private", Enabled: &enabled}}}},
		{"new target", SummarySettingsInput{Targets: []SummaryTargetInput{{ChatType: "group", ChatID: "new-group", Enabled: &disabled}}}},
		{"foreign private target", SummarySettingsInput{Targets: []SummaryTargetInput{{ChatType: "private", ChatID: "another-owner", Enabled: &disabled}}}},
		{"top time", SummarySettingsInput{PushTime: "20:00"}},
		{"top timezone", SummarySettingsInput{Timezone: "Asia/Tokyo"}},
		{"top type", SummarySettingsInput{ChatType: "private"}},
		{"top destination", SummarySettingsInput{ChatID: "new-group"}},
		{"top label", SummarySettingsInput{Label: "New label"}},
		{"top language", SummarySettingsInput{Language: "zh"}},
		{"target time", SummarySettingsInput{Targets: []SummaryTargetInput{{ChatType: "private", PushTime: "20:00"}}}},
		{"target timezone", SummarySettingsInput{Targets: []SummaryTargetInput{{ChatType: "private", Timezone: "UTC"}}}},
		{"target label", SummarySettingsInput{Targets: []SummaryTargetInput{{ChatType: "private", Label: "New label"}}}},
		{"target language", SummarySettingsInput{Targets: []SummaryTargetInput{{ChatType: "private", Language: "en"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repository, _ := summaryMaintenanceFixture()
			_, err := service.DisableSummarySettings(context.Background(), "summary-owner", test.input)
			if !errors.Is(err, ErrSummaryMaintenanceChange) {
				t.Fatalf("error = %v, want ErrSummaryMaintenanceChange", err)
			}
			if repository.calls != 0 {
				t.Fatal("invalid request wrote summary settings")
			}
		})
	}
}

func TestDisableSummarySettingsPropagatesErrorsAndRequiresOwnedSubscription(t *testing.T) {
	for _, test := range []string{"entitlement", "missing subscription", "foreign subscription", "repository", "refreshed entitlement"} {
		t.Run(test, func(t *testing.T) {
			service, repository, entitlements := summaryMaintenanceFixture()
			want := errors.New("isolated failure")
			wantWrites := 0
			switch test {
			case "entitlement":
				entitlements.firstErr = want
			case "missing subscription":
				entitlements.before.Subscription = nil
				want = store.ErrNotFound
			case "foreign subscription":
				entitlements.before.Subscription.DeBoxUserID = "another-owner"
				want = store.ErrNotFound
			case "repository":
				repository.err = want
				wantWrites = 1
			case "refreshed entitlement":
				entitlements.secondErr = want
				wantWrites = 1
			}
			_, err := service.DisableSummarySettings(context.Background(), "summary-owner", SummarySettingsInput{})
			if !errors.Is(err, want) || repository.calls != wantWrites {
				t.Fatalf("error = %v, writes = %d; want %v, %d", err, repository.calls, want, wantWrites)
			}
		})
	}
}
