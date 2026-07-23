package plans

import (
	"fmt"
	"strings"
)

const (
	Free         = "free"
	Standard     = "standard"
	Professional = "professional"

	BalanceChange      = "balance_change"
	Incoming           = "incoming"
	Outgoing           = "outgoing"
	BalanceThreshold   = "balance_threshold"
	ApprovalChange     = "approval_change"
	AddressInteraction = "address_interaction"
)

var planOrder = []string{Free, Standard, Professional}

var ruleTypes = []RuleType{
	{Code: BalanceChange, Label: "余额变化", Description: "余额发生任意变化时推送通知。"},
	{Code: Incoming, Label: "转入提醒", Description: "余额增加并达到阈值时推送通知。"},
	{Code: Outgoing, Label: "转出提醒", Description: "余额减少并达到阈值时推送通知。"},
	{Code: BalanceThreshold, Label: "余额阈值", Description: "余额首次达到或低于阈值时提醒一次；持续低于不重复，恢复后再次跌破会重新提醒。"},
	{Code: ApprovalChange, Label: "授权 / Approve 监控", Description: "钱包对指定合约的代币授权额度发生变化时推送通知。"},
	{Code: AddressInteraction, Label: "指定地址交互提醒", Description: "钱包与指定地址或合约发生交互时推送通知。"},
}

type RuleType struct {
	Code        string `json:"code"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type Plan struct {
	Code                string     `json:"code"`
	Name                string     `json:"name"`
	Price               string     `json:"price"`
	Asset               string     `json:"asset"`
	Days                int        `json:"days"`
	WalletLimit         int        `json:"wallet_limit"`
	RuleLimit           int        `json:"rule_limit"`
	GroupLimit          int        `json:"group_limit"`
	DailyAlertLimit     *int       `json:"daily_alert_limit"`
	AllowedRuleTypes    []string   `json:"allowed_rule_types"`
	AllowedRules        []RuleType `json:"allowed_rules"`
	PrivateNotification bool       `json:"private_notification"`
	GroupNotification   bool       `json:"group_notification"`
	DailySummary        bool       `json:"daily_summary"`
	SummaryTargets      []string   `json:"summary_targets"`
	Description         string     `json:"description"`
}

func (p Plan) AllowsRuleType(ruleType string) bool {
	for _, allowed := range p.AllowedRuleTypes {
		if allowed == ruleType {
			return true
		}
	}
	return false
}

func (p Plan) AllowsSummaryTarget(chatType string) bool {
	for _, target := range p.SummaryTargets {
		if target == chatType {
			return true
		}
	}
	return false
}

type Catalog struct {
	plans map[string]Plan
}

func NewCatalog(standardPrice string, standardDays int, asset string) (*Catalog, error) {
	standardPrice = strings.TrimSpace(standardPrice)
	asset = strings.TrimSpace(asset)
	if standardPrice == "" {
		return nil, fmt.Errorf("standard plan price must not be empty")
	}
	if standardDays < 1 {
		return nil, fmt.Errorf("standard plan days must be greater than zero")
	}
	if asset == "" {
		return nil, fmt.Errorf("subscription asset must not be empty")
	}

	dailyLimit := 5
	catalog := &Catalog{plans: map[string]Plan{
		Free: makePlan(
			Free,
			"免费版",
			"0",
			asset,
			0,
			1,
			1,
			0,
			&dailyLimit,
			[]string{BalanceChange, Incoming, Outgoing, BalanceThreshold},
			false,
			false,
			nil,
			"1 个钱包、1 条基础规则，每日最多 5 次提醒，仅支持私聊通知。",
		),
		Standard: makePlan(
			Standard,
			"标准版",
			standardPrice,
			asset,
			standardDays,
			3,
			10,
			0,
			nil,
			[]string{BalanceChange, Incoming, Outgoing, BalanceThreshold, ApprovalChange},
			false,
			true,
			[]string{"private"},
			"适合个人监控：3 个钱包、10 条规则，支持资产变化、Approve 监控、私聊通知和每日摘要。",
		),
		Professional: makePlan(
			Professional,
			"专业版",
			"25",
			asset,
			30,
			20,
			100,
			3,
			nil,
			[]string{BalanceChange, Incoming, Outgoing, BalanceThreshold, ApprovalChange, AddressInteraction},
			true,
			true,
			[]string{"private", "group"},
			"适合项目方和社群：20 个钱包、100 条规则，支持群通知、指定地址交互提醒和群每日摘要。",
		),
	}}
	return catalog, nil
}

func (c *Catalog) Get(code string) (Plan, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		code = Standard
	}
	plan, ok := c.plans[code]
	if !ok {
		return Plan{}, fmt.Errorf("不支持的套餐：%s", code)
	}
	return clonePlan(plan), nil
}

func (c *Catalog) PublicPlans() []Plan {
	result := make([]Plan, 0, len(planOrder))
	for _, code := range planOrder {
		plan, _ := c.Get(code)
		result = append(result, plan)
	}
	return result
}

func PublicRuleTypes() []RuleType {
	return append([]RuleType(nil), ruleTypes...)
}

func makePlan(
	code string,
	name string,
	price string,
	asset string,
	days int,
	walletLimit int,
	ruleLimit int,
	groupLimit int,
	dailyAlertLimit *int,
	allowedRuleTypes []string,
	groupNotification bool,
	dailySummary bool,
	summaryTargets []string,
	description string,
) Plan {
	allowedRules := make([]RuleType, 0, len(allowedRuleTypes))
	for _, code := range allowedRuleTypes {
		for _, ruleType := range ruleTypes {
			if ruleType.Code == code {
				allowedRules = append(allowedRules, ruleType)
				break
			}
		}
	}
	return Plan{
		Code:                code,
		Name:                name,
		Price:               price,
		Asset:               asset,
		Days:                days,
		WalletLimit:         walletLimit,
		RuleLimit:           ruleLimit,
		GroupLimit:          groupLimit,
		DailyAlertLimit:     dailyAlertLimit,
		AllowedRuleTypes:    append([]string(nil), allowedRuleTypes...),
		AllowedRules:        allowedRules,
		PrivateNotification: true,
		GroupNotification:   groupNotification,
		DailySummary:        dailySummary,
		SummaryTargets:      append([]string(nil), summaryTargets...),
		Description:         description,
	}
}

func clonePlan(plan Plan) Plan {
	plan.AllowedRuleTypes = append([]string(nil), plan.AllowedRuleTypes...)
	plan.AllowedRules = append([]RuleType(nil), plan.AllowedRules...)
	plan.SummaryTargets = append([]string(nil), plan.SummaryTargets...)
	if plan.DailyAlertLimit != nil {
		value := *plan.DailyAlertLimit
		plan.DailyAlertLimit = &value
	}
	return plan
}
