package monitor

import (
	"html"
	"math/big"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type alertRisk int

const (
	alertRiskLow alertRisk = iota
	alertRiskMedium
	alertRiskHigh
)

func alertText(
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	tokenSymbol string,
	note string,
	occurredAt time.Time,
) string {
	english := normalizeLanguage(rule.NotificationLanguage) == "en"
	switch rule.RuleType {
	case plans.BalanceChange:
		return balanceChangeAlertText(rule, previousValue, currentValue, tokenSymbol, occurredAt, english)
	case plans.Incoming:
		return incomingAlertText(rule, previousValue, currentValue, tokenSymbol, occurredAt, english)
	case plans.Outgoing:
		return outgoingAlertText(rule, previousValue, currentValue, tokenSymbol, occurredAt, english)
	case plans.BalanceThreshold:
		return lowBalanceAlertText(rule, currentValue, tokenSymbol, occurredAt, english)
	case plans.HighBalanceThreshold:
		return highBalanceAlertText(rule, currentValue, tokenSymbol, occurredAt, english)
	case plans.ApprovalChange:
		return approvalAlertText(rule, previousValue, currentValue, tokenSymbol, occurredAt, english)
	case plans.AddressInteraction:
		return interactionAlertText(rule, currentValue, occurredAt, english)
	default:
		return fallbackAlertText(rule, currentValue, note, occurredAt, english)
	}
}

func balanceChangeAlertText(
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	tokenSymbol string,
	occurredAt time.Time,
	english bool,
) string {
	wallet := monitoredWallet(rule)
	delta, hasDelta := balanceDelta(previousValue, currentValue)
	title := "🔔 " + wallet + " 余额发生变化"
	risk := alertRiskLow
	if english {
		title = "🔔 Balance changed · " + wallet
	}
	if hasDelta {
		amount := compactRatWithSymbol(absRat(delta), tokenSymbol)
		switch delta.Sign() {
		case 1:
			if english {
				title = "📈 " + wallet + " balance increased by " + amount
			} else {
				title = "📈 " + wallet + " 余额增加 " + amount
			}
		case -1:
			risk = alertRiskMedium
			if english {
				title = "📉 " + wallet + " balance decreased by " + amount
			} else {
				title = "📉 " + wallet + " 余额减少 " + amount
			}
		}
	}

	lines := []string{
		localizedField("当前余额", "Current balance", fullAmountWithSymbol(currentValue, tokenSymbol), english),
		localizedField("变化前", "Previous balance", pointerAmount(previousValue, tokenSymbol), english),
		localizedField("本次变化", "Change this time", signedRatWithSymbol(delta, tokenSymbol, hasDelta), english),
		localizedField("变化幅度", "Percentage change", percentageChange(previousValue, currentValue), english),
		localizedField(
			"触发条件",
			"Trigger condition",
			localizedCondition(
				"余额变化 ≥ "+fullAmountWithSymbol(rule.Threshold, tokenSymbol),
				"balance change ≥ "+fullAmountWithSymbol(rule.Threshold, tokenSymbol),
				english,
			),
			english,
		),
		riskLine(risk, english),
		contextLine(rule, occurredAt, english),
	}
	return buildAlert(title, lines...)
}

func incomingAlertText(
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	tokenSymbol string,
	occurredAt time.Time,
	english bool,
) string {
	wallet := monitoredWallet(rule)
	delta, hasDelta := balanceDelta(previousValue, currentValue)
	amount := compactRatWithSymbol(absRat(delta), tokenSymbol)
	title := "💰 " + wallet + " 收到转入 " + amount
	if !hasDelta {
		title = "💰 " + wallet + " 收到一笔转入"
	}
	if english {
		title = "💰 " + wallet + " received " + amount
		if !hasDelta {
			title = "💰 Incoming transfer · " + wallet
		}
	}
	lines := []string{
		localizedField("当前余额", "Current balance", fullAmountWithSymbol(currentValue, tokenSymbol), english),
		localizedField("转入前", "Balance before", pointerAmount(previousValue, tokenSymbol), english),
		localizedField("余额增幅", "Balance increase", percentageChange(previousValue, currentValue), english),
		localizedField(
			"触发条件",
			"Trigger condition",
			localizedCondition(
				"单笔转入 ≥ "+fullAmountWithSymbol(rule.Threshold, tokenSymbol),
				"incoming amount ≥ "+fullAmountWithSymbol(rule.Threshold, tokenSymbol),
				english,
			),
			english,
		),
		riskLine(alertRiskLow, english),
		contextLine(rule, occurredAt, english),
	}
	return buildAlert(title, lines...)
}

func outgoingAlertText(
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	tokenSymbol string,
	occurredAt time.Time,
	english bool,
) string {
	wallet := monitoredWallet(rule)
	delta, hasDelta := balanceDelta(previousValue, currentValue)
	amount := compactRatWithSymbol(absRat(delta), tokenSymbol)
	title := "💸 " + wallet + " 转出 " + amount
	if !hasDelta {
		title = "💸 检测到钱包转出 · " + wallet
	}
	if english {
		title = "💸 " + wallet + " sent " + amount
		if !hasDelta {
			title = "💸 Outgoing transfer · " + wallet
		}
	}
	explanation := "请确认这笔资金变化是否为本人操作。"
	if english {
		explanation = "Please confirm that you recognize this balance change."
	}
	lines := []string{
		localizedField("当前余额", "Current balance", fullAmountWithSymbol(currentValue, tokenSymbol), english),
		localizedField("转出前", "Balance before", pointerAmount(previousValue, tokenSymbol), english),
		localizedField("余额降幅", "Balance decrease", percentageChange(previousValue, currentValue), english),
		localizedField(
			"触发条件",
			"Trigger condition",
			localizedCondition(
				"单笔转出 ≥ "+fullAmountWithSymbol(rule.Threshold, tokenSymbol),
				"outgoing amount ≥ "+fullAmountWithSymbol(rule.Threshold, tokenSymbol),
				english,
			),
			english,
		),
		riskLine(alertRiskMedium, english),
		explanation,
		contextLine(rule, occurredAt, english),
	}
	return buildAlert(title, lines...)
}

func lowBalanceAlertText(
	rule store.WatchRule,
	currentValue string,
	tokenSymbol string,
	occurredAt time.Time,
	english bool,
) string {
	wallet := monitoredWallet(rule)
	threshold := fullAmountWithSymbol(rule.Threshold, tokenSymbol)
	title := "⚠️ " + wallet + " 余额低于 " + threshold
	if english {
		title = "⚠️ " + wallet + " balance fell below " + threshold
	}
	if current, currentOK := decimalRat(currentValue); currentOK {
		if configured, configuredOK := decimalRat(rule.Threshold); configuredOK &&
			current.Cmp(configured) == 0 {
			title = "⚠️ " + wallet + " 余额达到低余额阈值 " + threshold
			if english {
				title = "⚠️ " + wallet + " reached the low-balance threshold " + threshold
			}
		}
	}
	explanation := "余额不足可能影响后续转账或合约操作。"
	if english {
		explanation = "A low balance may prevent future transfers or contract actions."
	}
	lines := []string{
		localizedField("当前余额", "Current balance", fullAmountWithSymbol(currentValue, tokenSymbol), english),
		localizedField("设置阈值", "Configured threshold", threshold, english),
		localizedField(
			"低于阈值",
			"Below threshold by",
			thresholdDistance(rule.Threshold, currentValue, tokenSymbol),
			english,
		),
		riskLine(alertRiskHigh, english),
		explanation,
		contextLine(rule, occurredAt, english),
	}
	return buildAlert(title, lines...)
}

func highBalanceAlertText(
	rule store.WatchRule,
	currentValue string,
	tokenSymbol string,
	occurredAt time.Time,
	english bool,
) string {
	wallet := monitoredWallet(rule)
	threshold := fullAmountWithSymbol(rule.Threshold, tokenSymbol)
	title := "📈 " + wallet + " 余额高于 " + threshold
	if english {
		title = "📈 " + wallet + " balance rose above " + threshold
	}
	if current, currentOK := decimalRat(currentValue); currentOK {
		if configured, configuredOK := decimalRat(rule.Threshold); configuredOK &&
			current.Cmp(configured) == 0 {
			title = "📈 " + wallet + " 余额达到高余额阈值 " + threshold
			if english {
				title = "📈 " + wallet + " reached the high-balance threshold " + threshold
			}
		}
	}
	explanation := "余额已达到你设置的高余额提醒条件。"
	if english {
		explanation = "The balance has reached your configured high-balance condition."
	}
	lines := []string{
		localizedField("当前余额", "Current balance", fullAmountWithSymbol(currentValue, tokenSymbol), english),
		localizedField("设置阈值", "Configured threshold", threshold, english),
		localizedField(
			"超过阈值",
			"Above threshold by",
			thresholdDistance(currentValue, rule.Threshold, tokenSymbol),
			english,
		),
		riskLine(alertRiskLow, english),
		explanation,
		contextLine(rule, occurredAt, english),
	}
	return buildAlert(title, lines...)
}

func approvalAlertText(
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	tokenSymbol string,
	occurredAt time.Time,
	english bool,
) string {
	wallet := monitoredWallet(rule)
	target := targetName(rule)
	targetDetails := targetDescription(rule)
	current, currentOK := decimalRat(currentValue)
	previous, previousOK := pointerRat(previousValue)
	revoked := currentOK && current.Sign() == 0 && previousOK && previous.Sign() > 0
	reduced := currentOK && previousOK && current.Sign() > 0 && current.Cmp(previous) < 0
	allowance := allowanceAmount(currentValue, tokenSymbol, english)

	title := "🚨 授权风险 · " + target
	body := target + " 当前最多可以使用 " + allowance + "。"
	explanation := "这意味着该地址可以从钱包转走相应代币，请确认是否为本人操作。"
	risk := alertRiskHigh
	if english {
		title = "🚨 Approval risk · " + target
		body = target + " can now use up to " + allowance + " from this wallet."
		explanation = "This address may transfer the approved tokens from the wallet. Confirm that you authorized it."
	}
	if revoked {
		risk = alertRiskLow
		title = "✅ 授权已取消 · " + target
		body = target + " 已不能再使用该钱包中的 " + tokenName(tokenSymbol, false) + "。"
		explanation = "该授权额度现已归零。"
		if english {
			title = "✅ Approval revoked · " + target
			body = target + " can no longer use this wallet's " + fallbackSymbol(tokenSymbol) + "."
			explanation = "The approved allowance is now zero."
		}
	} else if reduced {
		risk = alertRiskMedium
		title = "⚠️ 授权限额已降低 · " + target
		body = target + " 当前最多可以使用 " + allowance + "。"
		explanation = "授权仍然有效，请确认剩余额度符合预期。"
		if english {
			title = "⚠️ Approval reduced · " + target
			body = target + " can now use up to " + allowance + "."
			explanation = "The approval is still active. Confirm that the remaining allowance is expected."
		}
	}

	lines := []string{
		body,
		localizedField("当前授权", "Current allowance", allowance, english),
		localizedField("授权对象", "Approved address", targetDetails, english),
		localizedField("钱包", "Wallet", wallet, english),
		riskLine(risk, english),
		explanation,
		contextLine(rule, occurredAt, english),
	}
	return buildAlert(title, lines...)
}

func interactionAlertText(
	rule store.WatchRule,
	transaction string,
	occurredAt time.Time,
	english bool,
) string {
	target := targetName(rule)
	title := "⚠️ 指定地址交互 · " + target
	body := "监控钱包刚刚与该地址发生了一笔链上交易。"
	explanation := "请确认这次交互是否符合预期。"
	if english {
		title = "⚠️ Watched-address interaction · " + target
		body = "The monitored wallet just made an on-chain transaction with this address."
		explanation = "Please confirm that you recognize this interaction."
	}
	transaction = strings.TrimSpace(transaction)
	if transaction == "matched" || transaction == "none" {
		transaction = ""
	}
	lines := []string{
		body,
		localizedField("目标地址", "Target address", targetDescription(rule), english),
		localizedField(
			"交易",
			"Transaction",
			notificationfmt.ShortIdentifier(transaction),
			english,
		),
		localizedField("监控钱包", "Monitored wallet", monitoredWallet(rule), english),
		riskLine(alertRiskMedium, english),
		explanation,
		contextLine(rule, occurredAt, english),
	}
	return buildAlert(title, lines...)
}

func fallbackAlertText(
	rule store.WatchRule,
	currentValue string,
	note string,
	occurredAt time.Time,
	english bool,
) string {
	label := ruleTypeLabels[normalizeLanguage(rule.NotificationLanguage)][rule.RuleType]
	if label == "" {
		label = rule.RuleType
	}
	title := "🔔 地址监控提醒"
	if english {
		title = "🔔 Address monitoring alert"
	}
	return buildAlert(
		title,
		localizedField("规则", "Rule", label, english),
		localizedField("监控钱包", "Monitored wallet", monitoredWallet(rule), english),
		localizedField("当前值", "Current value", currentValue, english),
		note,
		contextLine(rule, occurredAt, english),
	)
}

func buildAlert(title string, lines ...string) string {
	output := make([]string, 0, len(lines)+1)
	output = append(output, "<b>"+html.EscapeString(strings.TrimSpace(title))+"</b>")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			if strings.HasPrefix(line, formattedAlertFieldPrefix) {
				output = append(output, strings.TrimPrefix(line, formattedAlertFieldPrefix))
			} else {
				output = append(output, html.EscapeString(line))
			}
		}
	}
	return notificationfmt.JoinBlocks(output...)
}

const formattedAlertFieldPrefix = "\x00notification-field:"

func localizedField(chineseLabel, englishLabel, value string, english bool) string {
	value = html.EscapeString(strings.TrimSpace(value))
	if english {
		return formattedAlertFieldPrefix + notificationfmt.KeyValue(englishLabel, value, true)
	}
	return formattedAlertFieldPrefix + notificationfmt.KeyValue(chineseLabel, value, false)
}

func localizedCondition(chinese, englishText string, english bool) string {
	if english {
		return englishText
	}
	return chinese
}

func monitoredWallet(rule store.WatchRule) string {
	return notificationfmt.ShortIdentifier(rule.WalletAddress)
}

func targetName(rule store.WatchRule) string {
	if label := strings.TrimSpace(rule.TargetLabel); label != "" {
		return label
	}
	value := notificationfmt.ShortIdentifier(stringValue(rule.TargetAddress))
	if value == "" {
		return "指定地址"
	}
	return value
}

func targetDescription(rule store.WatchRule) string {
	address := notificationfmt.ShortIdentifier(stringValue(rule.TargetAddress))
	label := strings.TrimSpace(rule.TargetLabel)
	switch {
	case label != "" && address != "":
		return label + " · " + address
	case label != "":
		return label
	default:
		return address
	}
}

func contextLine(rule store.WatchRule, occurredAt time.Time, english bool) string {
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	chainName := notificationfmt.ChainName(rule.ChainKey)
	clock := notificationfmt.LocalTime(occurredAt, freeAlertTimezone, "15:04")
	if english {
		return localizedField("网络", "Network", chainName+" · Today "+clock, true)
	}
	return localizedField("网络", "Network", chainName+" · 今天 "+clock, false)
}

func riskLine(risk alertRisk, english bool) string {
	labels := []string{"🟢", "🟠", "🔴"}
	valuesZH := []string{"低", "中", "高"}
	valuesEN := []string{"Low", "Medium", "High"}
	if english {
		return localizedField(labels[risk]+" 风险等级", labels[risk]+" Risk level", valuesEN[risk], true)
	}
	return localizedField(labels[risk]+" 风险等级", labels[risk]+" Risk level", valuesZH[risk], false)
}

func fullAmountWithSymbol(value, symbol string) string {
	value = notificationfmt.FullTokenAmount(value)
	if value == "" {
		return ""
	}
	return strings.TrimSpace(value + " " + strings.TrimSpace(symbol))
}

func pointerAmount(value *string, symbol string) string {
	if value == nil {
		return ""
	}
	return fullAmountWithSymbol(*value, symbol)
}

func compactRatWithSymbol(value *big.Rat, symbol string) string {
	if value == nil {
		return ""
	}
	amount := notificationfmt.TokenAmount(value.RatString())
	return strings.TrimSpace(amount + " " + strings.TrimSpace(symbol))
}

func signedRatWithSymbol(value *big.Rat, symbol string, available bool) string {
	if !available || value == nil {
		return ""
	}
	sign := ""
	if value.Sign() > 0 {
		sign = "+"
	} else if value.Sign() < 0 {
		sign = "-"
	}
	return sign + compactRatWithSymbol(absRat(value), symbol)
}

func percentageChange(previousValue *string, currentValue string) string {
	previous, previousOK := pointerRat(previousValue)
	current, currentOK := decimalRat(currentValue)
	if !previousOK || !currentOK || previous.Sign() == 0 {
		return ""
	}
	change := new(big.Rat).Sub(current, previous)
	denominator := absRat(previous)
	change.Quo(change, denominator)
	change.Mul(change, big.NewRat(100, 1))
	sign := ""
	if change.Sign() > 0 {
		sign = "+"
	} else if change.Sign() < 0 {
		sign = "-"
	}
	return sign + notificationfmt.Percentage(absRat(change).RatString()) + "%"
}

func thresholdDistance(largerValue, smallerValue, symbol string) string {
	larger, largerOK := decimalRat(largerValue)
	smaller, smallerOK := decimalRat(smallerValue)
	if !largerOK || !smallerOK {
		return ""
	}
	distance := new(big.Rat).Sub(larger, smaller)
	if distance.Sign() <= 0 {
		return ""
	}
	return fullAmountWithSymbol(distance.RatString(), symbol)
}

func balanceDelta(previousValue *string, currentValue string) (*big.Rat, bool) {
	previous, previousOK := pointerRat(previousValue)
	current, currentOK := decimalRat(currentValue)
	if !previousOK || !currentOK {
		return nil, false
	}
	return new(big.Rat).Sub(current, previous), true
}

func decimalRat(value string) (*big.Rat, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	number, ok := new(big.Rat).SetString(value)
	return number, ok
}

func pointerRat(value *string) (*big.Rat, bool) {
	if value == nil {
		return nil, false
	}
	return decimalRat(*value)
}

func absRat(value *big.Rat) *big.Rat {
	if value == nil {
		return nil
	}
	return new(big.Rat).Abs(new(big.Rat).Set(value))
}

func allowanceAmount(value, tokenSymbol string, english bool) string {
	number, ok := decimalRat(value)
	if ok && isEffectivelyUnlimited(number) {
		if english {
			return "Unlimited " + fallbackSymbol(tokenSymbol)
		}
		return strings.TrimSpace("无限额度 " + strings.TrimSpace(tokenSymbol))
	}
	return fullAmountWithSymbol(value, tokenSymbol)
}

func isEffectivelyUnlimited(value *big.Rat) bool {
	if value == nil {
		return false
	}
	limit, _ := new(big.Int).SetString("1000000000000000000000000000000", 10)
	return value.Cmp(new(big.Rat).SetInt(limit)) >= 0
}

func fallbackSymbol(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "tokens"
	}
	return value
}

func tokenName(value string, english bool) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	if english {
		return "tokens"
	}
	return "代币"
}
