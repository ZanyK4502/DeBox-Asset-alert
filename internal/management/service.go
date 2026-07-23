package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

var (
	decimalPattern = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$`)
	timePattern    = regexp.MustCompile(`^\d{2}:\d{2}$`)
)

var allowedSummaryTimezones = map[string]struct{}{
	"Asia/Shanghai":       {},
	"Asia/Tokyo":          {},
	"Asia/Bangkok":        {},
	"Asia/Kolkata":        {},
	"Europe/Berlin":       {},
	"Europe/London":       {},
	"America/New_York":    {},
	"America/Los_Angeles": {},
	"UTC":                 {},
}

type Repository interface {
	ListUserWatchRules(context.Context, string) ([]store.WatchRule, error)
	DeleteWatchRule(context.Context, int64, string) (bool, error)
	DeletePausedWatchRules(context.Context, string) (int64, error)
	UpdateWatchRuleNotificationLanguage(context.Context, int64, string, string) (store.WatchRule, error)
	GetNotificationGroup(context.Context, string, string) (*store.NotificationGroup, error)
	ListNotificationGroups(context.Context, string) ([]store.NotificationGroup, error)
	DeleteNotificationGroup(context.Context, int64, string) (store.GroupDeletion, error)
	DisableDailySummaries(context.Context, []int64, string) (int64, error)
	UpdateDailySummarySettings(context.Context, string, store.DailySummarySettings) (store.Subscription, error)
}

type EntitlementService interface {
	Entitlement(context.Context, string) (subscription.Entitlement, error)
	ActivePlan(context.Context, string) (plans.Plan, error)
	ChooseFreeWatchRule(context.Context, string, int64) (subscription.Entitlement, error)
	CreateWatchRule(context.Context, store.CreateWatchRuleParams) (store.WatchRule, error)
	RestorePausedWatchRule(context.Context, string, int64) (subscription.Entitlement, error)
	CreateNotificationGroup(context.Context, string, string, string) (store.NotificationGroup, error)
	RequireSummaryTarget(context.Context, string, string) (plans.Plan, error)
}

type ChainService interface {
	Balance(context.Context, string, string, string, string) (chain.BalanceResult, error)
	TokenAllowance(context.Context, string, string, string, string, string) (chain.AllowanceResult, error)
	LatestInteraction(context.Context, string, string, string, string) (chain.InteractionResult, error)
}

type GroupService interface {
	GroupInfo(context.Context, string) (map[string]any, error)
	IsGroupJoined(context.Context, string, string) (any, error)
}

type NotificationService interface {
	SendNotification(string, string, string) (string, error)
}

type InitialRuleChecker interface {
	CheckRule(context.Context, store.WatchRule, string) (any, error)
}

type Dependencies struct {
	Repository      Repository
	Entitlements    EntitlementService
	Chain           ChainService
	Groups          GroupService
	Notifications   NotificationService
	InitialChecker  InitialRuleChecker
	DefaultChainKey string
}

type Service struct {
	deps Dependencies
}

func New(dependencies Dependencies) *Service {
	dependencies.DefaultChainKey = strings.ToLower(strings.TrimSpace(dependencies.DefaultChainKey))
	if dependencies.DefaultChainKey == "" {
		dependencies.DefaultChainKey = "bsc"
	}
	return &Service{deps: dependencies}
}

type WatchRuleInput struct {
	ChainKey             string `json:"chain_key"`
	WalletAddress        string `json:"wallet_address"`
	TokenAddress         string `json:"token_address"`
	TargetAddress        string `json:"target_address"`
	TargetLabel          string `json:"target_label"`
	RuleType             string `json:"rule_type"`
	Threshold            string `json:"threshold"`
	NotificationChatID   string `json:"notification_chat_id"`
	NotificationChatType string `json:"notification_chat_type"`
	NotificationLabel    string `json:"notification_label"`
	NotificationLanguage string `json:"notification_language"`
}

func DefaultWatchRuleInput() WatchRuleInput {
	return WatchRuleInput{
		ChainKey:             "bsc",
		RuleType:             plans.BalanceChange,
		Threshold:            "0",
		NotificationChatType: "private",
		NotificationLanguage: "zh",
	}
}

type WatchRuleCreation struct {
	Rule         store.WatchRule          `json:"rule"`
	Baseline     any                      `json:"baseline"`
	InitialCheck any                      `json:"initial_check"`
	Entitlement  subscription.Entitlement `json:"entitlement"`
}

func (s *Service) ListWatchRules(
	ctx context.Context,
	deboxUserID string,
) ([]store.WatchRule, error) {
	return s.deps.Repository.ListUserWatchRules(ctx, deboxUserID)
}

func (s *Service) CreateWatchRule(
	ctx context.Context,
	deboxUserID string,
	input WatchRuleInput,
) (WatchRuleCreation, error) {
	input = normalizeWatchRuleInput(input, s.deps.DefaultChainKey)
	threshold, err := validateWatchRuleInput(input)
	if err != nil {
		return WatchRuleCreation{}, err
	}
	profile, err := chain.ChainProfile(input.ChainKey, s.deps.DefaultChainKey)
	if err != nil {
		return WatchRuleCreation{}, err
	}
	plan, err := s.deps.Entitlements.ActivePlan(ctx, deboxUserID)
	if err != nil {
		return WatchRuleCreation{}, err
	}
	if !plan.AllowsRuleType(input.RuleType) {
		return WatchRuleCreation{}, fmt.Errorf("当前套餐不支持该规则类型：%s", input.RuleType)
	}
	if input.NotificationChatType == "group" && !plan.GroupNotification {
		return WatchRuleCreation{}, errors.New("当前套餐不支持群通知，请升级专业版。")
	}
	chatID, notificationLabel, err := s.notificationTarget(ctx, deboxUserID, input)
	if err != nil {
		return WatchRuleCreation{}, err
	}

	walletAddress := input.WalletAddress
	tokenAddress := optionalString(input.TokenAddress)
	targetAddress := optionalString(input.TargetAddress)
	var lastValue *string
	var baseline any

	switch input.RuleType {
	case plans.ApprovalChange:
		value, err := s.deps.Chain.TokenAllowance(
			ctx,
			walletAddress,
			input.TokenAddress,
			input.TargetAddress,
			profile.Key,
			s.deps.DefaultChainKey,
		)
		if err != nil {
			return WatchRuleCreation{}, err
		}
		walletAddress = value.WalletAddress
		tokenAddress = stringPointer(value.TokenAddress)
		targetAddress = stringPointer(value.SpenderAddress)
		lastValue = stringPointer(value.Value)
		baseline = value
	case plans.AddressInteraction:
		value, err := s.deps.Chain.LatestInteraction(
			ctx,
			walletAddress,
			input.TargetAddress,
			profile.Key,
			s.deps.DefaultChainKey,
		)
		if err != nil {
			return WatchRuleCreation{}, err
		}
		walletAddress = value.WalletAddress
		targetAddress = stringPointer(value.TargetAddress)
		lastValue = stringPointer(value.Cursor)
		baseline = value
	default:
		value, err := s.deps.Chain.Balance(
			ctx,
			walletAddress,
			input.TokenAddress,
			profile.Key,
			s.deps.DefaultChainKey,
		)
		if err != nil {
			return WatchRuleCreation{}, err
		}
		walletAddress = value.WalletAddress
		tokenAddress = value.TokenAddress
		if input.RuleType != plans.BalanceThreshold {
			lastValue = stringPointer(value.Value)
		}
		baseline = value
	}

	rule, err := s.deps.Entitlements.CreateWatchRule(ctx, store.CreateWatchRuleParams{
		DeBoxUserID:          deboxUserID,
		ChainKey:             profile.Key,
		ChainID:              int32(profile.ChainID),
		WalletAddress:        walletAddress,
		TokenAddress:         tokenAddress,
		TargetAddress:        targetAddress,
		TargetLabel:          strings.TrimSpace(input.TargetLabel),
		RuleType:             input.RuleType,
		Threshold:            threshold,
		NotificationChatID:   chatID,
		NotificationChatType: input.NotificationChatType,
		NotificationLabel:    notificationLabel,
		NotificationLanguage: input.NotificationLanguage,
		LastValue:            lastValue,
	})
	if err != nil {
		return WatchRuleCreation{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return WatchRuleCreation{}, err
	}

	var initialCheck any
	if input.RuleType == plans.BalanceThreshold && s.deps.InitialChecker != nil {
		initialCheck, err = s.deps.InitialChecker.CheckRule(ctx, rule, entitlement.Plan.Code)
		if err != nil {
			return WatchRuleCreation{}, err
		}
	}
	return WatchRuleCreation{
		Rule:         rule,
		Baseline:     baseline,
		InitialCheck: initialCheck,
		Entitlement:  entitlement,
	}, nil
}

type EntitlementResult struct {
	OK          bool                     `json:"ok"`
	Deleted     *int64                   `json:"deleted,omitempty"`
	Entitlement subscription.Entitlement `json:"entitlement"`
}

func (s *Service) DeletePausedWatchRules(
	ctx context.Context,
	deboxUserID string,
) (EntitlementResult, error) {
	deleted, err := s.deps.Repository.DeletePausedWatchRules(ctx, deboxUserID)
	if err != nil {
		return EntitlementResult{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return EntitlementResult{}, err
	}
	return EntitlementResult{OK: true, Deleted: &deleted, Entitlement: entitlement}, nil
}

func (s *Service) DeleteWatchRule(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
) (EntitlementResult, error) {
	if _, err := s.deps.Repository.DeleteWatchRule(ctx, ruleID, deboxUserID); err != nil {
		return EntitlementResult{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return EntitlementResult{}, err
	}
	return EntitlementResult{OK: true, Entitlement: entitlement}, nil
}

func (s *Service) ChooseFreeWatchRule(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
) (subscription.Entitlement, error) {
	return s.deps.Entitlements.ChooseFreeWatchRule(ctx, deboxUserID, ruleID)
}

func (s *Service) RestoreWatchRule(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
) (subscription.Entitlement, error) {
	return s.deps.Entitlements.RestorePausedWatchRule(ctx, deboxUserID, ruleID)
}

type WatchRuleUpdate struct {
	Rule        store.WatchRule          `json:"rule"`
	Entitlement subscription.Entitlement `json:"entitlement"`
}

func (s *Service) UpdateWatchRuleLanguage(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
	language string,
) (WatchRuleUpdate, error) {
	language, err := requireLanguage(language)
	if err != nil {
		return WatchRuleUpdate{}, err
	}
	rule, err := s.deps.Repository.UpdateWatchRuleNotificationLanguage(
		ctx,
		ruleID,
		deboxUserID,
		language,
	)
	if err != nil {
		return WatchRuleUpdate{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return WatchRuleUpdate{}, err
	}
	return WatchRuleUpdate{Rule: rule, Entitlement: entitlement}, nil
}

type SummarySettingsInput struct {
	Enabled  bool   `json:"enabled"`
	PushTime string `json:"push_time"`
	Timezone string `json:"timezone"`
	ChatType string `json:"chat_type"`
	ChatID   string `json:"chat_id"`
	Label    string `json:"label"`
	Language string `json:"language"`
}

func DefaultSummarySettingsInput() SummarySettingsInput {
	return SummarySettingsInput{
		Enabled:  true,
		PushTime: "20:00",
		Timezone: "Asia/Shanghai",
		ChatType: "private",
		Language: "zh",
	}
}

type SummarySettingsResult struct {
	Subscription store.Subscription       `json:"subscription"`
	Entitlement  subscription.Entitlement `json:"entitlement"`
}

func (s *Service) SaveSummarySettings(
	ctx context.Context,
	deboxUserID string,
	input SummarySettingsInput,
) (SummarySettingsResult, error) {
	chatType := strings.ToLower(strings.TrimSpace(input.ChatType))
	if chatType != "private" && chatType != "group" {
		return SummarySettingsResult{}, errors.New("每日摘要推送对象只能是私聊或群聊。")
	}
	if _, err := s.deps.Entitlements.RequireSummaryTarget(ctx, deboxUserID, chatType); err != nil {
		return SummarySettingsResult{}, err
	}
	language, err := requireLanguage(input.Language)
	if err != nil {
		return SummarySettingsResult{}, err
	}
	pushTime, err := validatePushTime(input.PushTime)
	if err != nil {
		return SummarySettingsResult{}, err
	}
	timezoneName, err := validateSummaryTimezone(input.Timezone)
	if err != nil {
		return SummarySettingsResult{}, err
	}
	chatID := deboxUserID
	if chatType == "group" {
		chatID = strings.TrimSpace(input.ChatID)
		group, err := s.deps.Repository.GetNotificationGroup(ctx, deboxUserID, chatID)
		if err != nil {
			return SummarySettingsResult{}, err
		}
		if group == nil {
			return SummarySettingsResult{}, errors.New("请先绑定这个群，再设置群每日摘要。")
		}
	}
	label := strings.TrimSpace(input.Label)
	if label == "" {
		if chatType == "private" {
			label = "私聊摘要"
		} else {
			label = chatID
		}
	}
	updated, err := s.deps.Repository.UpdateDailySummarySettings(
		ctx,
		deboxUserID,
		store.DailySummarySettings{
			Enabled:      input.Enabled,
			PushTime:     pushTime,
			TimezoneName: timezoneName,
			ChatType:     chatType,
			ChatID:       chatID,
			Label:        label,
			Language:     language,
		},
	)
	if err != nil {
		return SummarySettingsResult{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return SummarySettingsResult{}, err
	}
	return SummarySettingsResult{Subscription: updated, Entitlement: entitlement}, nil
}

func (s *Service) ListNotificationGroups(
	ctx context.Context,
	deboxUserID string,
) ([]store.NotificationGroup, error) {
	return s.deps.Repository.ListNotificationGroups(ctx, deboxUserID)
}

type NotificationGroupInput struct {
	Link  string `json:"gid"`
	Label string `json:"label"`
}

type NotificationGroupCreation struct {
	Group         store.NotificationGroup  `json:"group"`
	AlreadyExists bool                     `json:"already_exists,omitempty"`
	Source        map[string]any           `json:"source,omitempty"`
	Entitlement   subscription.Entitlement `json:"entitlement"`
}

func (s *Service) CreateNotificationGroup(
	ctx context.Context,
	deboxUserID string,
	walletAddress string,
	input NotificationGroupInput,
) (NotificationGroupCreation, error) {
	gid := parseDeBoxGroupLink(input.Link)
	if gid == "" {
		return NotificationGroupCreation{}, errors.New("请输入正确的 DeBox 群链接。")
	}
	existing, err := s.deps.Repository.GetNotificationGroup(ctx, deboxUserID, gid)
	if err != nil {
		return NotificationGroupCreation{}, err
	}
	if existing != nil {
		entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
		if err != nil {
			return NotificationGroupCreation{}, err
		}
		return NotificationGroupCreation{
			Group:         *existing,
			AlreadyExists: true,
			Entitlement:   entitlement,
		}, nil
	}
	plan, err := s.deps.Entitlements.ActivePlan(ctx, deboxUserID)
	if err != nil {
		return NotificationGroupCreation{}, err
	}
	if !plan.GroupNotification {
		return NotificationGroupCreation{}, errors.New("当前套餐不支持群通知，请升级专业版。")
	}
	source, err := s.deps.Groups.GroupInfo(ctx, gid)
	if err != nil {
		return NotificationGroupCreation{}, err
	}
	joined, err := s.deps.Groups.IsGroupJoined(ctx, gid, walletAddress)
	if err != nil {
		return NotificationGroupCreation{}, err
	}
	if !groupJoined(joined) {
		return NotificationGroupCreation{}, errors.New("当前钱包似乎不是该群成员，请确认后再绑定。")
	}
	name := strings.TrimSpace(input.Label)
	if name == "" {
		name = groupName(source, gid)
	}
	group, err := s.deps.Entitlements.CreateNotificationGroup(ctx, deboxUserID, gid, name)
	if err != nil {
		return NotificationGroupCreation{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return NotificationGroupCreation{}, err
	}
	return NotificationGroupCreation{
		Group:       group,
		Source:      source,
		Entitlement: entitlement,
	}, nil
}

type NotificationGroupDeletion struct {
	OK                      bool                     `json:"ok"`
	SummaryTargetChanged    bool                     `json:"summary_target_changed"`
	SummaryConfirmationSent bool                     `json:"summary_confirmation_sent"`
	SummaryDisabled         bool                     `json:"summary_disabled"`
	Entitlement             subscription.Entitlement `json:"entitlement"`
}

func (s *Service) DeleteNotificationGroup(
	ctx context.Context,
	deboxUserID string,
	groupID int64,
) (NotificationGroupDeletion, error) {
	deletion, err := s.deps.Repository.DeleteNotificationGroup(ctx, groupID, deboxUserID)
	if err != nil {
		return NotificationGroupDeletion{}, err
	}
	enabledFallbacks := make([]store.Subscription, 0, len(deletion.SummaryFallbacks))
	for _, fallback := range deletion.SummaryFallbacks {
		if fallback.DailySummaryEnabled == 1 {
			enabledFallbacks = append(enabledFallbacks, fallback)
		}
	}

	confirmationSent := false
	summaryDisabled := false
	if len(enabledFallbacks) > 0 {
		language := normalizeLanguage(enabledFallbacks[0].DailySummaryLanguage)
		text := "<b>每日摘要</b><br/>原群已解绑，每日摘要已自动切换为本人私聊。"
		if language == "en" {
			text = "<b>Daily summary</b><br/>The group was unbound. Your daily summary now goes to this private chat."
		}
		if s.deps.Notifications != nil {
			_, err = s.deps.Notifications.SendNotification(deboxUserID, "private", text)
		} else {
			err = errors.New("notification service is unavailable")
		}
		if err == nil {
			confirmationSent = true
		} else {
			ids := make([]int64, 0, len(enabledFallbacks))
			for _, fallback := range enabledFallbacks {
				ids = append(ids, fallback.ID)
			}
			if _, disableErr := s.deps.Repository.DisableDailySummaries(ctx, ids, deboxUserID); disableErr != nil {
				return NotificationGroupDeletion{}, disableErr
			}
			summaryDisabled = true
		}
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return NotificationGroupDeletion{}, err
	}
	return NotificationGroupDeletion{
		OK:                      true,
		SummaryTargetChanged:    len(deletion.SummaryFallbacks) > 0,
		SummaryConfirmationSent: confirmationSent,
		SummaryDisabled:         summaryDisabled,
		Entitlement:             entitlement,
	}, nil
}

func (s *Service) notificationTarget(
	ctx context.Context,
	deboxUserID string,
	input WatchRuleInput,
) (string, string, error) {
	if input.NotificationChatType == "private" {
		return deboxUserID, "私聊通知", nil
	}
	if input.NotificationChatType != "group" {
		return "", "", errors.New("通知目标只能是 private 或 group。")
	}
	gid := strings.TrimSpace(input.NotificationChatID)
	if gid == "" {
		return "", "", errors.New("专业版群通知需要选择一个已绑定的群。")
	}
	group, err := s.deps.Repository.GetNotificationGroup(ctx, deboxUserID, gid)
	if err != nil {
		return "", "", err
	}
	if group == nil {
		return "", "", errors.New("这个群还没有绑定，请先在群通知设置中添加。")
	}
	label := strings.TrimSpace(input.NotificationLabel)
	if label == "" {
		label = group.Name
	}
	if label == "" {
		label = gid
	}
	return gid, label, nil
}

func normalizeWatchRuleInput(input WatchRuleInput, fallbackChain string) WatchRuleInput {
	input.ChainKey = strings.ToLower(strings.TrimSpace(input.ChainKey))
	if input.ChainKey == "" {
		input.ChainKey = fallbackChain
	}
	input.WalletAddress = strings.TrimSpace(input.WalletAddress)
	input.TokenAddress = strings.TrimSpace(input.TokenAddress)
	input.TargetAddress = strings.TrimSpace(input.TargetAddress)
	input.RuleType = strings.ToLower(strings.TrimSpace(input.RuleType))
	if input.RuleType == "" {
		input.RuleType = plans.BalanceChange
	}
	input.Threshold = strings.TrimSpace(input.Threshold)
	if input.Threshold == "" {
		input.Threshold = "0"
	}
	input.NotificationChatType = strings.ToLower(strings.TrimSpace(input.NotificationChatType))
	if input.NotificationChatType == "" {
		input.NotificationChatType = "private"
	}
	input.NotificationLanguage = strings.ToLower(strings.TrimSpace(input.NotificationLanguage))
	if input.NotificationLanguage == "" {
		input.NotificationLanguage = "zh"
	}
	return input
}

func validateWatchRuleInput(input WatchRuleInput) (string, error) {
	if _, err := requireLanguage(input.NotificationLanguage); err != nil {
		return "", err
	}
	supported := map[string]struct{}{
		plans.BalanceChange:      {},
		plans.Incoming:           {},
		plans.Outgoing:           {},
		plans.BalanceThreshold:   {},
		plans.ApprovalChange:     {},
		plans.AddressInteraction: {},
	}
	if _, ok := supported[input.RuleType]; !ok {
		return "", errors.New("不支持的监控类型。")
	}
	if !decimalPattern.MatchString(input.Threshold) {
		return "", errors.New("金额阈值必须是有效数字。")
	}
	number, _, err := big.ParseFloat(input.Threshold, 10, 256, big.ToNearestEven)
	if err != nil {
		return "", errors.New("金额阈值必须是有效数字。")
	}
	if number.Sign() < 0 {
		return "", errors.New("金额阈值不能小于 0。")
	}
	if input.RuleType == plans.ApprovalChange &&
		(input.TokenAddress == "" || input.TargetAddress == "") {
		return "", errors.New("授权监控需要填写代币合约和授权对象地址。")
	}
	if input.RuleType == plans.AddressInteraction && input.TargetAddress == "" {
		return "", errors.New("指定地址交互提醒需要填写目标地址或合约。")
	}
	return input.Threshold, nil
}

func requireLanguage(value string) (string, error) {
	language := strings.ToLower(strings.TrimSpace(value))
	if language != "zh" && language != "en" {
		return "", errors.New("语言只能是 zh 或 en。")
	}
	return language, nil
}

func normalizeLanguage(value string) string {
	language, err := requireLanguage(value)
	if err != nil {
		return "zh"
	}
	return language
}

func validatePushTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !timePattern.MatchString(value) {
		return "", errors.New("推送时间格式应为 HH:MM。")
	}
	hour, _ := strconv.Atoi(value[:2])
	minute, _ := strconv.Atoi(value[3:])
	if hour > 23 || minute > 59 {
		return "", errors.New("推送时间必须在 00:00 到 23:59 之间。")
	}
	return value, nil
}

func validateSummaryTimezone(value string) (string, error) {
	timezoneName := strings.TrimSpace(value)
	if timezoneName == "" {
		timezoneName = "Asia/Shanghai"
	}
	if _, ok := allowedSummaryTimezones[timezoneName]; !ok {
		return "", errors.New("每日摘要时区不在支持范围内。")
	}
	return timezoneName, nil
}

func parseDeBoxGroupLink(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "m.debox.pro" && host != "www.debox.pro" && host != "debox.pro" {
		return ""
	}
	if parsed.Path != "/group" {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("id"))
}

func groupName(group map[string]any, fallback string) string {
	for _, key := range []string{"group_name", "name", "title", "nickname"} {
		if value := strings.TrimSpace(fmt.Sprint(group[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	if nested, ok := group["data"].(map[string]any); ok {
		return groupName(nested, fallback)
	}
	return fallback
}

func groupJoined(payload any) bool {
	switch value := payload.(type) {
	case bool:
		return value
	case map[string]any:
		for _, key := range []string{"is_join", "isJoin", "joined", "success", "data"} {
			if nested, ok := value[key]; ok {
				return groupJoined(nested)
			}
		}
	case string:
		normalized := strings.ToLower(strings.TrimSpace(value))
		return normalized == "1" || normalized == "true" || normalized == "yes"
	case json.Number:
		return value.String() == "1"
	case int:
		return value == 1
	case int64:
		return value == 1
	case float64:
		return value == 1
	}
	return false
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func stringPointer(value string) *string {
	return &value
}
