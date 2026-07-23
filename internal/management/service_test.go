package management

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

type fakeRepository struct {
	rules            []store.WatchRule
	groups           map[string]store.NotificationGroup
	deletedRule      [2]any
	deletedPausedFor string
	languageUpdate   struct {
		ruleID   int64
		userID   string
		language string
	}
	summarySettings store.DailySummarySettings
	summaryUserID   string
	groupDeletion   store.GroupDeletion
	deletedGroup    [2]any
	disabledIDs     []int64
}

func (f *fakeRepository) ListUserWatchRules(
	context.Context,
	string,
) ([]store.WatchRule, error) {
	return append([]store.WatchRule(nil), f.rules...), nil
}

func (f *fakeRepository) DeleteWatchRule(
	_ context.Context,
	ruleID int64,
	userID string,
) (bool, error) {
	f.deletedRule = [2]any{ruleID, userID}
	return true, nil
}

func (f *fakeRepository) DeletePausedWatchRules(
	_ context.Context,
	userID string,
) (int64, error) {
	f.deletedPausedFor = userID
	return 2, nil
}

func (f *fakeRepository) UpdateWatchRuleNotificationLanguage(
	_ context.Context,
	ruleID int64,
	userID string,
	language string,
) (store.WatchRule, error) {
	f.languageUpdate.ruleID = ruleID
	f.languageUpdate.userID = userID
	f.languageUpdate.language = language
	return store.WatchRule{ID: ruleID, DeBoxUserID: userID, NotificationLanguage: language}, nil
}

func (f *fakeRepository) GetNotificationGroup(
	_ context.Context,
	userID string,
	gid string,
) (*store.NotificationGroup, error) {
	group, ok := f.groups[userID+"/"+gid]
	if !ok {
		return nil, nil
	}
	copy := group
	return &copy, nil
}

func (f *fakeRepository) ListNotificationGroups(
	_ context.Context,
	userID string,
) ([]store.NotificationGroup, error) {
	result := []store.NotificationGroup{}
	for key, group := range f.groups {
		if strings.HasPrefix(key, userID+"/") {
			result = append(result, group)
		}
	}
	return result, nil
}

func (f *fakeRepository) DeleteNotificationGroup(
	_ context.Context,
	groupID int64,
	userID string,
) (store.GroupDeletion, error) {
	f.deletedGroup = [2]any{groupID, userID}
	return f.groupDeletion, nil
}

func (f *fakeRepository) DisableDailySummaries(
	_ context.Context,
	ids []int64,
	_ string,
) (int64, error) {
	f.disabledIDs = append([]int64(nil), ids...)
	return int64(len(ids)), nil
}

func (f *fakeRepository) UpdateDailySummarySettings(
	_ context.Context,
	userID string,
	settings store.DailySummarySettings,
) (store.Subscription, error) {
	f.summaryUserID = userID
	f.summarySettings = settings
	return store.Subscription{
		ID:                   5,
		DeBoxUserID:          userID,
		DailySummaryEnabled:  1,
		DailySummaryChatType: settings.ChatType,
		DailySummaryChatID:   settings.ChatID,
		DailySummaryLanguage: settings.Language,
		DailySummaryTimezone: settings.TimezoneName,
		DailySummaryTime:     settings.PushTime,
		DailySummaryLabel:    settings.Label,
	}, nil
}

type fakeEntitlements struct {
	plan             plans.Plan
	entitlement      subscription.Entitlement
	createdRuleInput store.CreateWatchRuleParams
	createdGroup     [3]string
	freeRuleID       int64
	restoredRuleID   int64
	summaryChatType  string
}

func (f *fakeEntitlements) Entitlement(
	_ context.Context,
	userID string,
) (subscription.Entitlement, error) {
	value := f.entitlement
	value.DeBoxUserID = userID
	if value.Plan.Code == "" {
		value.Plan = f.plan
	}
	return value, nil
}

func (f *fakeEntitlements) ActivePlan(context.Context, string) (plans.Plan, error) {
	return f.plan, nil
}

func (f *fakeEntitlements) ChooseFreeWatchRule(
	_ context.Context,
	userID string,
	ruleID int64,
) (subscription.Entitlement, error) {
	f.freeRuleID = ruleID
	return subscription.Entitlement{DeBoxUserID: userID}, nil
}

func (f *fakeEntitlements) CreateWatchRule(
	_ context.Context,
	input store.CreateWatchRuleParams,
) (store.WatchRule, error) {
	f.createdRuleInput = input
	return store.WatchRule{
		ID:                   9,
		DeBoxUserID:          input.DeBoxUserID,
		ChainKey:             input.ChainKey,
		WalletAddress:        input.WalletAddress,
		TokenAddress:         input.TokenAddress,
		TargetAddress:        input.TargetAddress,
		RuleType:             input.RuleType,
		Threshold:            input.Threshold,
		NotificationChatID:   input.NotificationChatID,
		NotificationChatType: input.NotificationChatType,
		NotificationLanguage: input.NotificationLanguage,
		LastValue:            input.LastValue,
	}, nil
}

func (f *fakeEntitlements) RestorePausedWatchRule(
	_ context.Context,
	userID string,
	ruleID int64,
) (subscription.Entitlement, error) {
	f.restoredRuleID = ruleID
	return subscription.Entitlement{DeBoxUserID: userID}, nil
}

func (f *fakeEntitlements) CreateNotificationGroup(
	_ context.Context,
	userID string,
	gid string,
	name string,
) (store.NotificationGroup, error) {
	f.createdGroup = [3]string{userID, gid, name}
	return store.NotificationGroup{ID: 7, DeBoxUserID: userID, GID: gid, Name: name}, nil
}

func (f *fakeEntitlements) RequireSummaryTarget(
	_ context.Context,
	_ string,
	chatType string,
) (plans.Plan, error) {
	f.summaryChatType = chatType
	if !f.plan.DailySummary || !f.plan.AllowsSummaryTarget(chatType) {
		return plans.Plan{}, errors.New("summary target denied")
	}
	return f.plan, nil
}

type fakeChainService struct {
	balance     chain.BalanceResult
	allowance   chain.AllowanceResult
	interaction chain.InteractionResult
	balanceCall [4]string
}

func (f *fakeChainService) Balance(
	_ context.Context,
	address, tokenAddress, chainKey, fallback string,
) (chain.BalanceResult, error) {
	f.balanceCall = [4]string{address, tokenAddress, chainKey, fallback}
	return f.balance, nil
}

func (f *fakeChainService) TokenAllowance(
	context.Context,
	string,
	string,
	string,
	string,
	string,
) (chain.AllowanceResult, error) {
	return f.allowance, nil
}

func (f *fakeChainService) LatestInteraction(
	context.Context,
	string,
	string,
	string,
	string,
) (chain.InteractionResult, error) {
	return f.interaction, nil
}

type fakeGroupService struct {
	info       map[string]any
	joined     any
	groupID    string
	joinWallet string
}

func (f *fakeGroupService) GroupInfo(
	_ context.Context,
	gid string,
) (map[string]any, error) {
	f.groupID = gid
	return f.info, nil
}

func (f *fakeGroupService) IsGroupJoined(
	_ context.Context,
	gid, wallet string,
) (any, error) {
	f.groupID = gid
	f.joinWallet = wallet
	return f.joined, nil
}

type fakeNotificationService struct {
	chatID   string
	chatType string
	text     string
	err      error
}

func (f *fakeNotificationService) SendNotification(
	chatID, chatType, text string,
) (string, error) {
	f.chatID = chatID
	f.chatType = chatType
	f.text = text
	return "message-1", f.err
}

type fakeInitialChecker struct {
	rule     store.WatchRule
	planCode string
}

func (f *fakeInitialChecker) CheckRule(
	_ context.Context,
	rule store.WatchRule,
	planCode string,
) (any, error) {
	f.rule = rule
	f.planCode = planCode
	return map[string]any{"triggered": true}, nil
}

func TestCreateWatchRuleUsesBaselineAndAuthenticatedIdentity(t *testing.T) {
	repository := &fakeRepository{groups: map[string]store.NotificationGroup{}}
	entitlements := &fakeEntitlements{plan: planForTest(t, plans.Standard)}
	chainService := &fakeChainService{balance: chain.BalanceResult{
		Value:         "12.5",
		Symbol:        "BNB",
		ChainKey:      "bsc",
		ChainID:       56,
		WalletAddress: "0x1111111111111111111111111111111111111111",
	}}
	service := New(Dependencies{
		Repository:      repository,
		Entitlements:    entitlements,
		Chain:           chainService,
		DefaultChainKey: "bsc",
	})
	input := DefaultWatchRuleInput()
	input.WalletAddress = "0x1111111111111111111111111111111111111111"
	input.RuleType = plans.Incoming
	input.Threshold = "1.25"

	result, err := service.CreateWatchRule(context.Background(), "user-1", input)
	if err != nil {
		t.Fatalf("CreateWatchRule() error = %v", err)
	}
	created := entitlements.createdRuleInput
	if created.DeBoxUserID != "user-1" ||
		created.NotificationChatID != "user-1" ||
		created.NotificationChatType != "private" ||
		created.NotificationLabel != "私聊通知" ||
		created.Threshold != "1.25" ||
		created.LastValue == nil || *created.LastValue != "12.5" {
		t.Fatalf("created rule input = %#v", created)
	}
	if result.Rule.ID != 9 || result.InitialCheck != nil ||
		chainService.balanceCall != [4]string{input.WalletAddress, "", "bsc", "bsc"} {
		t.Fatalf("result/call = %#v / %#v", result, chainService.balanceCall)
	}
}

func TestBalanceThresholdRunsConfiguredInitialChecker(t *testing.T) {
	entitlements := &fakeEntitlements{plan: planForTest(t, plans.Standard)}
	checker := &fakeInitialChecker{}
	service := New(Dependencies{
		Repository:   &fakeRepository{},
		Entitlements: entitlements,
		Chain: &fakeChainService{balance: chain.BalanceResult{
			Value:         "0.5",
			WalletAddress: "0x1111111111111111111111111111111111111111",
			ChainKey:      "bsc",
			ChainID:       56,
		}},
		InitialChecker: checker,
	})
	input := DefaultWatchRuleInput()
	input.WalletAddress = "0x1111111111111111111111111111111111111111"
	input.RuleType = plans.BalanceThreshold
	input.Threshold = "1"

	result, err := service.CreateWatchRule(context.Background(), "user-1", input)
	if err != nil {
		t.Fatalf("CreateWatchRule() error = %v", err)
	}
	if entitlements.createdRuleInput.LastValue != nil ||
		checker.rule.ID != result.Rule.ID ||
		checker.planCode != plans.Standard ||
		result.InitialCheck == nil {
		t.Fatalf("threshold result/checker = %#v / %#v", result, checker)
	}
}

func TestWatchRuleValidationRejectsInvalidInputs(t *testing.T) {
	base := DefaultWatchRuleInput()
	base.WalletAddress = "0x1111111111111111111111111111111111111111"
	tests := []struct {
		name  string
		edit  func(*WatchRuleInput)
		match string
	}{
		{name: "language", edit: func(v *WatchRuleInput) { v.NotificationLanguage = "fr" }, match: "zh 或 en"},
		{name: "rule type", edit: func(v *WatchRuleInput) { v.RuleType = "unknown" }, match: "不支持"},
		{name: "number", edit: func(v *WatchRuleInput) { v.Threshold = "NaN" }, match: "有效数字"},
		{name: "negative", edit: func(v *WatchRuleInput) { v.Threshold = "-1" }, match: "不能小于"},
		{name: "approval fields", edit: func(v *WatchRuleInput) { v.RuleType = plans.ApprovalChange }, match: "代币合约"},
		{name: "interaction target", edit: func(v *WatchRuleInput) { v.RuleType = plans.AddressInteraction }, match: "目标地址"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			if _, err := validateWatchRuleInput(input); err == nil ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("validateWatchRuleInput() error = %v", err)
			}
		})
	}
}

func TestSaveSummarySettingsUsesBoundGroup(t *testing.T) {
	repository := &fakeRepository{groups: map[string]store.NotificationGroup{
		"user-1/group-1": {ID: 3, DeBoxUserID: "user-1", GID: "group-1", Name: "Builders"},
	}}
	entitlements := &fakeEntitlements{plan: planForTest(t, plans.Professional)}
	service := New(Dependencies{Repository: repository, Entitlements: entitlements})
	input := DefaultSummarySettingsInput()
	input.ChatType = "group"
	input.ChatID = " group-1 "
	input.Timezone = "America/New_York"
	input.PushTime = "09:30"
	input.Language = "en"

	result, err := service.SaveSummarySettings(context.Background(), "user-1", input)
	if err != nil {
		t.Fatalf("SaveSummarySettings() error = %v", err)
	}
	if repository.summaryUserID != "user-1" ||
		repository.summarySettings.ChatID != "group-1" ||
		repository.summarySettings.Label != "group-1" ||
		repository.summarySettings.TimezoneName != "America/New_York" ||
		entitlements.summaryChatType != "group" ||
		result.Subscription.ID != 5 {
		t.Fatalf("summary settings/result = %#v / %#v", repository.summarySettings, result)
	}
	input.Timezone = "Mars/Olympus"
	if _, err := service.SaveSummarySettings(context.Background(), "user-1", input); err == nil ||
		!strings.Contains(err.Error(), "时区") {
		t.Fatalf("invalid timezone error = %v", err)
	}
}

func TestCreateNotificationGroupParsesLinkAndChecksMembership(t *testing.T) {
	repository := &fakeRepository{groups: map[string]store.NotificationGroup{}}
	entitlements := &fakeEntitlements{plan: planForTest(t, plans.Professional)}
	groups := &fakeGroupService{
		info:   map[string]any{"data": map[string]any{"group_name": "Builders"}},
		joined: map[string]any{"data": true},
	}
	service := New(Dependencies{
		Repository:   repository,
		Entitlements: entitlements,
		Groups:       groups,
	})
	result, err := service.CreateNotificationGroup(
		context.Background(),
		"user-1",
		"0x1111111111111111111111111111111111111111",
		NotificationGroupInput{
			Link: "https://m.debox.pro/group?id=group-1&code=user-1",
		},
	)
	if err != nil {
		t.Fatalf("CreateNotificationGroup() error = %v", err)
	}
	if entitlements.createdGroup != [3]string{"user-1", "group-1", "Builders"} ||
		groups.joinWallet != "0x1111111111111111111111111111111111111111" ||
		result.Group.GID != "group-1" {
		t.Fatalf("group creation = %#v / %#v / %#v", entitlements.createdGroup, groups, result)
	}
	if parseDeBoxGroupLink("https://example.com/group?id=group-1") != "" {
		t.Fatal("parseDeBoxGroupLink() accepted a foreign host")
	}
	if groupJoined(map[string]any{"is_join": false, "data": true}) {
		t.Fatal("groupJoined() ignored the first supported field")
	}
}

func TestDeleteNotificationGroupFallsBackOrDisablesSummary(t *testing.T) {
	fallback := store.Subscription{
		ID:                       11,
		DeBoxUserID:              "user-1",
		DailySummaryEnabled:      1,
		DailySummaryLanguage:     "en",
		DailySummaryChatType:     "private",
		DailySummaryChatID:       "user-1",
		DailySummaryLastSentDate: time.Now().Format("2006-01-02"),
	}
	t.Run("confirmation succeeds", func(t *testing.T) {
		repository := &fakeRepository{groupDeletion: store.GroupDeletion{
			SummaryFallbacks: []store.Subscription{fallback},
		}}
		notifier := &fakeNotificationService{}
		service := New(Dependencies{
			Repository:    repository,
			Entitlements:  &fakeEntitlements{},
			Notifications: notifier,
		})
		result, err := service.DeleteNotificationGroup(context.Background(), "user-1", 3)
		if err != nil {
			t.Fatalf("DeleteNotificationGroup() error = %v", err)
		}
		if !result.SummaryTargetChanged || !result.SummaryConfirmationSent ||
			result.SummaryDisabled || notifier.chatID != "user-1" ||
			notifier.chatType != "private" || !strings.Contains(notifier.text, "Daily summary") {
			t.Fatalf("deletion/notifier = %#v / %#v", result, notifier)
		}
	})
	t.Run("confirmation fails", func(t *testing.T) {
		repository := &fakeRepository{groupDeletion: store.GroupDeletion{
			SummaryFallbacks: []store.Subscription{fallback},
		}}
		service := New(Dependencies{
			Repository:   repository,
			Entitlements: &fakeEntitlements{},
			Notifications: &fakeNotificationService{
				err: errors.New("send failed"),
			},
		})
		result, err := service.DeleteNotificationGroup(context.Background(), "user-1", 3)
		if err != nil {
			t.Fatalf("DeleteNotificationGroup() error = %v", err)
		}
		if !result.SummaryDisabled || result.SummaryConfirmationSent ||
			!reflect.DeepEqual(repository.disabledIDs, []int64{11}) {
			t.Fatalf("deletion/disabled = %#v / %#v", result, repository.disabledIDs)
		}
	})
}

func TestRuleMutationsUseAuthenticatedUser(t *testing.T) {
	repository := &fakeRepository{}
	entitlements := &fakeEntitlements{}
	service := New(Dependencies{Repository: repository, Entitlements: entitlements})
	if _, err := service.DeleteWatchRule(context.Background(), "user-1", 4); err != nil {
		t.Fatalf("DeleteWatchRule() error = %v", err)
	}
	paused, err := service.DeletePausedWatchRules(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("DeletePausedWatchRules() error = %v", err)
	}
	if paused.Deleted == nil || *paused.Deleted != 2 {
		t.Fatalf("DeletePausedWatchRules() = %#v", paused)
	}
	if _, err := service.UpdateWatchRuleLanguage(context.Background(), "user-1", 5, "en"); err != nil {
		t.Fatalf("UpdateWatchRuleLanguage() error = %v", err)
	}
	if repository.deletedRule != [2]any{int64(4), "user-1"} ||
		repository.deletedPausedFor != "user-1" ||
		repository.languageUpdate.userID != "user-1" {
		t.Fatalf("mutation identity = %#v / %q / %#v",
			repository.deletedRule,
			repository.deletedPausedFor,
			repository.languageUpdate,
		)
	}
}

func planForTest(t *testing.T, code string) plans.Plan {
	t.Helper()
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	plan, err := catalog.Get(code)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", code, err)
	}
	return plan
}
