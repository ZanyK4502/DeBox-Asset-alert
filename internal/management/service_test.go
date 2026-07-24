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
	combinations    []store.CombinationRule
	historyPage     store.AggregationHistoryPage
	historyInput    struct {
		userID   string
		beforeID int64
		limit    int
	}
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

func (f *fakeRepository) ListUserCombinationRules(
	context.Context,
	string,
) ([]store.CombinationRule, error) {
	return append([]store.CombinationRule(nil), f.combinations...), nil
}

func (f *fakeRepository) DeleteCombinationRule(
	_ context.Context,
	_ int64,
	_ string,
) (bool, error) {
	return true, nil
}

func (f *fakeRepository) UpdateCombinationRuleNotificationLanguage(
	_ context.Context,
	combinationID int64,
	userID string,
	language string,
) (store.CombinationRule, error) {
	return store.CombinationRule{
		ID:                   combinationID,
		DeBoxUserID:          userID,
		NotificationLanguage: language,
	}, nil
}

func (f *fakeRepository) ListAggregationEventHistory(
	_ context.Context,
	userID string,
	beforeID int64,
	limit int,
) (store.AggregationHistoryPage, error) {
	f.historyInput.userID = userID
	f.historyInput.beforeID = beforeID
	f.historyInput.limit = limit
	return f.historyPage, nil
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

func TestListAggregationEventHistoryUsesDefaultsAndValidatesPagination(t *testing.T) {
	repository := &fakeRepository{
		historyPage: store.AggregationHistoryPage{
			Events:        []store.AggregationHistoryEvent{},
			RetentionDays: 30,
		},
	}
	service := New(Dependencies{Repository: repository})

	page, err := service.ListAggregationEventHistory(
		context.Background(),
		"user-1",
		0,
		0,
	)
	if err != nil {
		t.Fatalf("ListAggregationEventHistory() error = %v", err)
	}
	if page.RetentionDays != 30 ||
		repository.historyInput.userID != "user-1" ||
		repository.historyInput.beforeID != 0 ||
		repository.historyInput.limit != 50 {
		t.Fatalf(
			"history page/input = %#v / %#v",
			page,
			repository.historyInput,
		)
	}

	for _, test := range []struct {
		name     string
		beforeID int64
		limit    int
	}{
		{name: "negative cursor", beforeID: -1, limit: 20},
		{name: "negative limit", beforeID: 0, limit: -1},
		{name: "oversized limit", beforeID: 0, limit: 101},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.ListAggregationEventHistory(
				context.Background(),
				"user-1",
				test.beforeID,
				test.limit,
			); err == nil {
				t.Fatal("ListAggregationEventHistory() error = nil")
			}
		})
	}
}

type fakeEntitlements struct {
	plan             plans.Plan
	entitlement      subscription.Entitlement
	createdRuleInput store.CreateWatchRuleParams
	createdGroup     [3]string
	freeRuleID       int64
	restoredRuleID   int64
	summaryChatType  string
	combinationInput store.CreateCombinationRuleParams
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
		ID:                    9,
		DeBoxUserID:           input.DeBoxUserID,
		ChainKey:              input.ChainKey,
		WalletAddress:         input.WalletAddress,
		TokenAddress:          input.TokenAddress,
		TargetAddress:         input.TargetAddress,
		RuleType:              input.RuleType,
		Threshold:             input.Threshold,
		NotificationChatID:    input.NotificationChatID,
		NotificationChatType:  input.NotificationChatType,
		NotificationLanguage:  input.NotificationLanguage,
		RuleScope:             input.RuleScope,
		DeliveryMode:          input.DeliveryMode,
		CycleType:             input.CycleType,
		CycleMinutes:          input.CycleMinutes,
		TriggerCountThreshold: input.TriggerCountThreshold,
		LastValue:             input.LastValue,
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

func (f *fakeEntitlements) CreateCombinationRule(
	_ context.Context,
	input store.CreateCombinationRuleParams,
) (store.CombinationRule, error) {
	f.combinationInput = input
	return store.CombinationRule{
		ID:           21,
		DeBoxUserID:  input.DeBoxUserID,
		Note:         input.Note,
		CycleType:    input.CycleType,
		CycleMinutes: input.CycleMinutes,
	}, nil
}

func (f *fakeEntitlements) RestoreCombinationRule(
	_ context.Context,
	userID string,
	combinationID int64,
) (store.CombinationRule, error) {
	return store.CombinationRule{ID: combinationID, DeBoxUserID: userID}, nil
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
	tests := []struct {
		name      string
		ruleType  string
		balance   string
		threshold string
	}{
		{name: "low balance", ruleType: plans.BalanceThreshold, balance: "0.5", threshold: "1"},
		{name: "high balance", ruleType: plans.HighBalanceThreshold, balance: "1.5", threshold: "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entitlements := &fakeEntitlements{plan: planForTest(t, plans.Standard)}
			checker := &fakeInitialChecker{}
			service := New(Dependencies{
				Repository:   &fakeRepository{},
				Entitlements: entitlements,
				Chain: &fakeChainService{balance: chain.BalanceResult{
					Value:         test.balance,
					WalletAddress: "0x1111111111111111111111111111111111111111",
					ChainKey:      "bsc",
					ChainID:       56,
				}},
				InitialChecker: checker,
			})
			input := DefaultWatchRuleInput()
			input.WalletAddress = "0x1111111111111111111111111111111111111111"
			input.RuleType = test.ruleType
			input.Threshold = test.threshold

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
		})
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
		{name: "zero high threshold", edit: func(v *WatchRuleInput) {
			v.RuleType = plans.HighBalanceThreshold
			v.Threshold = "0"
		}, match: "必须大于 0"},
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

func TestWatchRuleValidationThresholdSemantics(t *testing.T) {
	tests := []struct {
		name      string
		ruleType  string
		threshold string
		token     string
		target    string
		want      string
	}{
		{name: "balance change allows zero", ruleType: plans.BalanceChange, threshold: "0", want: "0"},
		{name: "incoming allows zero", ruleType: plans.Incoming, threshold: "0", want: "0"},
		{name: "outgoing allows zero", ruleType: plans.Outgoing, threshold: "0", want: "0"},
		{name: "low balance allows zero", ruleType: plans.BalanceThreshold, threshold: "0", want: "0"},
		{name: "high balance is positive", ruleType: plans.HighBalanceThreshold, threshold: "1", want: "1"},
		{
			name:      "approval ignores threshold",
			ruleType:  plans.ApprovalChange,
			threshold: "not-used",
			token:     "0x2222222222222222222222222222222222222222",
			target:    "0x3333333333333333333333333333333333333333",
			want:      "0",
		},
		{
			name:      "interaction ignores threshold",
			ruleType:  plans.AddressInteraction,
			threshold: "not-used",
			target:    "0x3333333333333333333333333333333333333333",
			want:      "0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := DefaultWatchRuleInput()
			input.RuleType = test.ruleType
			input.Threshold = test.threshold
			input.TokenAddress = test.token
			input.TargetAddress = test.target

			got, err := validateWatchRuleInput(input)
			if err != nil {
				t.Fatalf("validateWatchRuleInput() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("threshold = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWatchRuleValidationRejectsInvalidStageSettings(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*WatchRuleInput)
		match string
	}{
		{
			name: "delivery mode",
			edit: func(input *WatchRuleInput) {
				input.DeliveryMode = "later"
			},
			match: "通知模式",
		},
		{
			name: "cycle type",
			edit: func(input *WatchRuleInput) {
				input.DeliveryMode = "stage"
				input.CycleType = "rolling"
			},
			match: "周期只能",
		},
		{
			name: "cycle minutes",
			edit: func(input *WatchRuleInput) {
				input.DeliveryMode = "stage"
				input.CycleMinutes = 0
			},
			match: "分钟数必须大于 0",
		},
		{
			name: "trigger count",
			edit: func(input *WatchRuleInput) {
				input.DeliveryMode = "stage"
				input.TriggerCountThreshold = 0
			},
			match: "触发次数必须大于 0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := DefaultWatchRuleInput()
			test.edit(&input)
			if _, err := validateWatchRuleInput(input); err == nil ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("validateWatchRuleInput() error = %v", err)
			}
		})
	}
}

func TestCreateStageRuleStoresCycleSettings(t *testing.T) {
	entitlements := &fakeEntitlements{plan: planForTest(t, plans.Standard)}
	service := New(Dependencies{
		Repository:   &fakeRepository{},
		Entitlements: entitlements,
		Chain: &fakeChainService{balance: chain.BalanceResult{
			Value:         "12",
			WalletAddress: "0x1111111111111111111111111111111111111111",
			ChainKey:      "bsc",
			ChainID:       56,
		}},
	})
	input := DefaultWatchRuleInput()
	input.WalletAddress = "0x1111111111111111111111111111111111111111"
	input.DeliveryMode = "stage"
	input.CycleType = "follow"
	input.CycleMinutes = 30
	input.TriggerCountThreshold = 4

	result, err := service.CreateWatchRule(context.Background(), "user-1", input)
	if err != nil {
		t.Fatalf("CreateWatchRule() error = %v", err)
	}
	created := entitlements.createdRuleInput
	if created.RuleScope != "standalone" ||
		created.DeliveryMode != "stage" ||
		created.CycleType != "follow" ||
		created.CycleMinutes != 30 ||
		created.TriggerCountThreshold != 4 {
		t.Fatalf("created stage settings = %#v", created)
	}
	if result.Rule.DeliveryMode != "stage" || result.Rule.CycleType != "follow" {
		t.Fatalf("created stage rule = %#v", result.Rule)
	}
}

func TestCreateStageRuleRejectsFreePlan(t *testing.T) {
	service := New(Dependencies{
		Repository:   &fakeRepository{},
		Entitlements: &fakeEntitlements{plan: planForTest(t, plans.Free)},
	})
	input := DefaultWatchRuleInput()
	input.WalletAddress = "0x1111111111111111111111111111111111111111"
	input.DeliveryMode = "stage"

	_, err := service.CreateWatchRule(context.Background(), "user-1", input)
	if err == nil || !strings.Contains(err.Error(), "标准版和专业版") {
		t.Fatalf("CreateWatchRule() error = %v", err)
	}
}

func TestCreateCombinationRulePreparesMembersAndUsesProfessionalPlan(t *testing.T) {
	t.Parallel()
	repository := &fakeRepository{groups: map[string]store.NotificationGroup{}}
	entitlements := &fakeEntitlements{plan: planForTest(t, plans.Professional)}
	chainService := &fakeChainService{balance: chain.BalanceResult{
		WalletAddress: "0x1111111111111111111111111111111111111111",
		Value:         "10",
		Symbol:        "BNB",
		ChainKey:      "bsc",
	}}
	service := New(Dependencies{
		Repository:   repository,
		Entitlements: entitlements,
		Chain:        chainService,
	})
	member := func(ruleType string, count int64) CombinationMemberInput {
		rule := DefaultWatchRuleInput()
		rule.WalletAddress = "0x1111111111111111111111111111111111111111"
		rule.RuleType = ruleType
		return CombinationMemberInput{Rule: rule, RequiredTriggerCount: count}
	}
	result, err := service.CreateCombinationRule(
		context.Background(),
		"user-1",
		CombinationRuleInput{
			Note:                 " treasury linkage ",
			CycleType:            "follow",
			CycleMinutes:         30,
			NotificationChatType: "private",
			NotificationLanguage: "en",
			Members: []CombinationMemberInput{
				member(plans.BalanceChange, 2),
				member(plans.Outgoing, 3),
			},
		},
	)
	if err != nil {
		t.Fatalf("CreateCombinationRule() error = %v", err)
	}
	input := entitlements.combinationInput
	if result.Combination.ID != 21 ||
		len(result.Baselines) != 2 ||
		input.DeBoxUserID != "user-1" ||
		input.Note != "treasury linkage" ||
		input.CycleType != "follow" ||
		input.CycleMinutes != 30 ||
		input.NotificationChatID != "user-1" ||
		len(input.Members) != 2 ||
		input.Members[0].RequiredTriggerCount != 2 ||
		input.Members[1].RequiredTriggerCount != 3 {
		t.Fatalf("combination creation/input = %#v / %#v", result, input)
	}
}

func TestCombinationRuleRequiresProfessionalAndAtLeastTwoMembers(t *testing.T) {
	t.Parallel()
	service := New(Dependencies{
		Repository:   &fakeRepository{},
		Entitlements: &fakeEntitlements{plan: planForTest(t, plans.Standard)},
	})
	input := DefaultCombinationRuleInput()
	input.CycleMinutes = 30
	input.Members = []CombinationMemberInput{
		{Rule: DefaultWatchRuleInput(), RequiredTriggerCount: 1},
		{Rule: DefaultWatchRuleInput(), RequiredTriggerCount: 1},
	}
	if _, err := service.CreateCombinationRule(
		context.Background(),
		"user-1",
		input,
	); err == nil || !strings.Contains(err.Error(), "专业版") {
		t.Fatalf("standard combination error = %v", err)
	}

	input.Members = input.Members[:1]
	if _, err := service.CreateCombinationRule(
		context.Background(),
		"user-1",
		input,
	); err == nil || !strings.Contains(err.Error(), "至少需要两条") {
		t.Fatalf("single member error = %v", err)
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
