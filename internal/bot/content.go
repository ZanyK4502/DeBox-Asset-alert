package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

func menuText(language string) string {
	if normalizeLanguage(language) == "en" {
		return "<b>DeBox Asset Alert</b><br/>" +
			"Monitor on-chain addresses, token balances, approvals, and contract interactions through DeBox Bot.<br/><br/>" +
			"Features include multi-chain monitoring, low and high balance thresholds, real-time and stage alerts, " +
			"Professional combination rules, group alerts, and daily summaries.<br/><br/>" +
			"Open the monitoring dashboard and sign with your wallet to securely sign in. " +
			"Signing sends no transaction and uses no gas."
	}
	return "<b>DeBox Asset Alert</b><br/>" +
		"监控链上地址、代币余额、授权和合约交互，通过 DeBox Bot 接收通知。<br/><br/>" +
		"支持：多链监控、低余额与高余额阈值、实时提醒、阶段提醒、专业版组合规则、群通知和每日摘要等。<br/><br/>" +
		"打开个人监控面板后，通过钱包签名完成安全登录；签名不会发起交易或消耗 Gas。"
}

func featuresText(language string) string {
	if normalizeLanguage(language) == "en" {
		return "<b>Monitoring</b><br/><br/>" +
			"Supported networks: BNB Chain, Ethereum, Base, Polygon, Arbitrum, and Optimism.<br/><br/>" +
			"Monitor native asset balances, or enter an ERC-20 contract to monitor a token balance.<br/><br/>" +
			"- Rule types:<br/>" +
			"• Balance change: alerts when the change reaches the configured amount<br/>" +
			"• Incoming and outgoing transfers: alerts when the transfer reaches the configured amount<br/>" +
			"• Low balance threshold: alerts once when the balance reaches or falls below the threshold; it alerts again only after recovery above it and another drop<br/>" +
			"• High balance threshold: alerts once when the balance reaches or rises above the threshold; it alerts again only after falling below it and another rise<br/>" +
			"• Approval change<br/>" +
			"• Specified address interaction (Professional)<br/><br/>" +
			"- Delivery modes:<br/>" +
			"• Real-time: sends after each trigger<br/>" +
			"• Stage alert (Standard and Professional): counts events in a user-defined cycle, sends once when the configured count is reached, then resets for the next cycle<br/>" +
			"• Combination rule (Professional): uses at least two dedicated member rules; it sends one combined alert after every member reaches its own count in the same cycle. Members do not send individual alerts.<br/><br/>" +
			"Stage and combination events remain available in the dashboard for 30 days.<br/><br/>" +
			"Each summary covers the previous scheduled cutoff through the current cutoff; the first covers the previous 24 hours and includes notification failures.<br/><br/>" +
			"If a summary group is unbound, delivery switches to private chat. If private confirmation fails, the daily summary is turned off."
	}
	return "<b>监控能力</b><br/><br/>" +
		"支持 BNB Chain、Ethereum、Base、Polygon、Arbitrum、Optimism。<br/><br/>" +
		"可监控原生资产余额，也可填写 ERC20 合约监控代币余额。<br/><br/>" +
		"- 规则包括：<br/>" +
		"• 余额变化：变化量达到设定金额时提醒<br/>" +
		"• 转入与转出：金额达到设定值时提醒<br/>" +
		"• 低余额阈值：余额达到或低于阈值时提醒一次；持续低于不重复，恢复至阈值以上后再次跌破才重新提醒<br/>" +
		"• 高余额阈值：余额达到或高于阈值时提醒一次；持续高于不重复，回落至阈值以下后再次突破才重新提醒<br/>" +
		"• 授权变化<br/>" +
		"• 指定地址交互（专业版）<br/><br/>" +
		"- 通知模式：<br/>" +
		"• 实时提醒：每次触发后发送<br/>" +
		"• 阶段提醒（标准版、专业版）：按用户设置的周期累计事件，达到设定次数后发送一次，进入下一周期后重新计数<br/>" +
		"• 组合规则（专业版）：至少包含两条专用成员规则；同一周期内所有成员分别达到设定次数后发送一条总通知，成员不会单独通知<br/><br/>" +
		"阶段提醒和组合规则事件会在个人监控面板保留 30 天。<br/><br/>" +
		"每期摘要统计上一次计划推送时间至本次计划推送时间；首次统计此前 24 小时，并显示本期通知失败次数。<br/><br/>" +
		"解绑摘要群后会自动切回本人私聊；若私聊确认失败，每日摘要会关闭。"
}

func (s *Service) plansText(language string) string {
	standard, _ := s.deps.Catalog.Get(plans.Standard)
	professional, _ := s.deps.Catalog.Get(plans.Professional)
	if normalizeLanguage(language) == "en" {
		return "<b>Plans</b><br/><br/>" +
			"Free: 1 wallet, 1 basic real-time rule, no expiration, up to 5 alerts per day, private alerts only.<br/><br/>" +
			fmt.Sprintf(
				"Standard: %s %s / %d days, 3 wallets, 10 rules, asset and approval monitoring, real-time or stage alerts, private delivery, and private daily summaries.<br/><br/>",
				standard.Price,
				standard.Asset,
				standard.Days,
			) +
			fmt.Sprintf(
				"Professional: %s %s / %d days, 20 wallets, 100 rules, all rule types, stage alerts, combination rules, group delivery, and private or group daily summaries. Combination members use the rule quota.<br/><br/>",
				professional.Price,
				professional.Asset,
				professional.Days,
			) +
			"While a paid plan is active, only the same plan can be renewed; choose another plan after it expires.<br/><br/>" +
			"Pay with USDT on BNB Chain. The subscription activates after 3 block confirmations; failed verification does not activate it.<br/><br/>" +
			"Subscriptions take effect immediately. Digital service purchases are non-refundable, so please review the plan before purchase."
	}
	return "<b>订阅方案</b><br/><br/>" +
		"免费版：1 个钱包，1 条基础实时规则，永久有效，每日最多 5 次提醒，仅私聊通知。<br/><br/>" +
		fmt.Sprintf(
			"标准版：%s %s / %d 天，3 个钱包，10 条规则，支持资产变化、授权监控、实时或阶段提醒、私聊通知和本人私聊每日摘要。<br/><br/>",
			standard.Price,
			standard.Asset,
			standard.Days,
		) +
		fmt.Sprintf(
			"专业版：%s %s / %d 天，20 个钱包，100 条规则，支持全部规则类型、阶段提醒、组合规则、群通知，以及本人私聊或群每日摘要；组合成员会占用规则额度。<br/><br/>",
			professional.Price,
			professional.Asset,
			professional.Days,
		) +
		"付费套餐有效期内只能续费同一套餐并顺延到期时间；套餐到期后才能选择其他套餐。<br/><br/>" +
		"使用 BNB Chain USDT 支付，交易达到 3 个区块确认后开通订阅；支付验证失败不会开通。<br/><br/>" +
		"订阅开通后立即生效，虚拟服务类权益不支持退款，请确认套餐内容后再购买。"
}

func groupEntryText(message *boxbotapi.Message) string {
	userName := ""
	if message != nil && message.From != nil {
		userName = firstNonEmpty(message.From.Name, message.From.UserId)
	}
	prefix := ""
	if userName != "" {
		prefix = "@" + escapeText(userName) + " "
	}
	return prefix +
		"我是 DeBox Asset Alert 链上监控助理，请私聊 Bot 或打开个人监控面板。<br/><br/>" +
		"I'm the DeBox Asset Alert monitoring assistant. " +
		"Message the Bot or open your monitoring dashboard."
}

func (s *Service) subscriptionText(
	ctx context.Context,
	userID string,
	language string,
) (string, error) {
	english := normalizeLanguage(language) == "en"
	if strings.TrimSpace(userID) == "" {
		if english {
			return "We could not identify your DeBox User ID. Open the monitoring dashboard to view your subscription.", nil
		}
		return "暂时无法识别你的 DeBox 用户 ID，请打开个人监控面板查看订阅。", nil
	}
	current, err := s.deps.Subscriptions.Entitlement(ctx, userID)
	if err != nil {
		return "", err
	}
	planName := escapeText(localizedPlanName(current, language))
	if current.Plan.Code == plans.Free {
		if english {
			return fmt.Sprintf(
				"<b>Subscription</b><br/>"+
					"Current plan: %s<br/>"+
					"Valid through: No expiration<br/>"+
					"Monitoring rules: %d / %d<br/>"+
					"Group alerts: %d / %d",
				planName,
				current.RuleCount,
				current.Plan.RuleLimit,
				current.GroupCount,
				current.Plan.GroupLimit,
			), nil
		}
		return fmt.Sprintf(
			"<b>订阅有效期</b><br/>"+
				"当前方案：%s<br/>"+
				"有效期：永久有效<br/>"+
				"监控规则：%d / %d<br/>"+
				"群通知：%d / %d",
			planName,
			current.RuleCount,
			current.Plan.RuleLimit,
			current.GroupCount,
			current.Plan.GroupLimit,
		), nil
	}

	expiresAt := "-"
	if current.Subscription != nil {
		expiresAt = current.Subscription.ExpiresAt.Format(time.RFC3339)
	}
	if english {
		return fmt.Sprintf(
			"<b>Subscription</b><br/>"+
				"Current plan: %s<br/>"+
				"Days remaining: %d<br/>"+
				"Expires at: %s<br/>"+
				"Monitoring rules: %d / %d<br/>"+
				"Group alerts: %d / %d",
			planName,
			current.DaysRemaining,
			escapeText(expiresAt),
			current.RuleCount,
			current.Plan.RuleLimit,
			current.GroupCount,
			current.Plan.GroupLimit,
		), nil
	}
	return fmt.Sprintf(
		"<b>订阅有效期</b><br/>"+
			"当前方案：%s<br/>"+
			"剩余天数：%d 天<br/>"+
			"到期时间：%s<br/>"+
			"监控规则：%d / %d<br/>"+
			"群通知：%d / %d",
		planName,
		current.DaysRemaining,
		escapeText(expiresAt),
		current.RuleCount,
		current.Plan.RuleLimit,
		current.GroupCount,
		current.Plan.GroupLimit,
	), nil
}

func localizedPlanName(current subscription.Entitlement, language string) string {
	if normalizeLanguage(language) != "en" {
		if current.Plan.Name == "" {
			return "未开通"
		}
		return current.Plan.Name
	}
	switch current.Plan.Code {
	case plans.Free:
		return "Free"
	case plans.Standard:
		return "Standard"
	case plans.Professional:
		return "Professional"
	case "":
		return "Not active"
	default:
		return current.Plan.Name
	}
}

func (s *Service) balanceText(
	ctx context.Context,
	userID string,
	language string,
) (string, error) {
	english := normalizeLanguage(language) == "en"
	if strings.TrimSpace(userID) == "" {
		if english {
			return "We could not identify your DeBox User ID. Open the monitoring dashboard to check your balance.", nil
		}
		return "暂时无法识别你的 DeBox 用户 ID，请打开个人监控面板查询余额。", nil
	}
	profile, err := s.deps.DeBox.UserInfo(ctx, userID, "")
	if err != nil {
		return "", err
	}
	address := strings.TrimSpace(extractAddress(profile))
	if address == "" {
		if english {
			return "No wallet address was found in your DeBox profile. Connect a wallet in the monitoring dashboard first.", nil
		}
		return "没有从 DeBox 用户资料中识别到钱包地址，请在个人监控面板连接钱包后查询。", nil
	}
	token, err := s.deps.Chain.Balance(
		ctx,
		address,
		s.deps.Settings.SubscriptionTokenAddress,
		"bsc",
		s.deps.Settings.DefaultChainKey,
	)
	if err != nil {
		return "", err
	}
	gas, err := s.deps.Chain.Balance(
		ctx,
		address,
		"",
		"bsc",
		s.deps.Settings.DefaultChainKey,
	)
	if err != nil {
		return "", err
	}
	wallet := shortAddress(address)
	if english {
		return fmt.Sprintf(
			"<b>Balance</b><br/>"+
				"Wallet: %s<br/>"+
				"Network: %s<br/>"+
				"Balance: %s %s<br/>"+
				"Gas balance: %s %s",
			escapeText(wallet),
			escapeText(token.ChainName),
			escapeText(token.Value),
			escapeText(token.Symbol),
			escapeText(gas.Value),
			escapeText(gas.Symbol),
		), nil
	}
	return fmt.Sprintf(
		"<b>余额查询</b><br/>"+
			"钱包：%s<br/>"+
			"网络：%s<br/>"+
			"余额：%s %s<br/>"+
			"Gas 费余额：%s %s",
		escapeText(wallet),
		escapeText(token.ChainName),
		escapeText(token.Value),
		escapeText(token.Symbol),
		escapeText(gas.Value),
		escapeText(gas.Symbol),
	), nil
}

func (s *Service) callbackText(
	ctx context.Context,
	data string,
	userID string,
	language string,
) (string, error) {
	switch {
	case data == "alert:intro", strings.HasPrefix(data, "alert:language:"):
		return menuText(language), nil
	case data == "alert:features":
		return featuresText(language), nil
	case data == "alert:plans":
		return s.plansText(language), nil
	case data == "alert:subscription":
		return s.subscriptionText(ctx, userID, language)
	case data == "alert:balance":
		return s.balanceText(ctx, userID, language)
	case data == "alert:swap":
		if normalizeLanguage(language) == "en" {
			return "<b>Swap</b><br/>Swap assets for USDT on BSC", nil
		}
		return "<b>闪兑</b><br/>将资产兑换为 BSC 链 USDT", nil
	case data == "alert:renew":
		if normalizeLanguage(language) == "en" {
			if s.publicAppURL != "" {
				return "Open the monitoring dashboard to renew: " + escapeText(s.publicAppURL), nil
			}
			return "Please renew in the H5 app.", nil
		}
		if s.publicAppURL != "" {
			return "请打开个人监控面板续费：" + escapeText(s.publicAppURL), nil
		}
		return "请在 H5 中续费。", nil
	default:
		return menuText(language), nil
	}
}

func (s *Service) menuMarkup(language string) boxbotapi.InlineKeyboardMarkup {
	english := normalizeLanguage(language) == "en"
	rows := [][]boxbotapi.InlineKeyboardButton{
		boxbotapi.NewInlineKeyboardRow(
			buttonData(choice(english, "Monitoring", "监控能力"), "alert:features"),
			buttonData(choice(english, "Plans", "订阅方案"), "alert:plans"),
		),
		boxbotapi.NewInlineKeyboardRow(
			buttonData(choice(english, "Subscription", "订阅有效期"), "alert:subscription"),
			buttonData(choice(english, "Balance", "余额查询"), "alert:balance"),
		),
	}
	renewButton := buttonData(choice(english, "Renew", "快捷续费"), "alert:renew")
	if s.publicAppURL != "" {
		renewButton = buttonURL(choice(english, "Renew", "快捷续费"), s.publicAppURL+"#renew")
	}
	rows = append(rows, boxbotapi.NewInlineKeyboardRow(
		buttonData(choice(english, "Swap", "闪兑"), "alert:swap"),
		renewButton,
	))
	if s.publicAppURL != "" {
		rows = append(rows, boxbotapi.NewInlineKeyboardRow(
			buttonURL(
				choice(english, "Monitoring Dashboard", "个人监控面板"),
				s.publicAppURL,
			),
			buttonURL(
				choice(english, "Aggregate Events", "汇总通知事件"),
				s.publicAppURL+"#aggregateEventsSection",
			),
		))
	}
	if english {
		rows = append(rows, boxbotapi.NewInlineKeyboardRow(
			buttonData("中文", "alert:language:zh"),
		))
	} else {
		rows = append(rows, boxbotapi.NewInlineKeyboardRow(
			buttonData("English", "alert:language:en"),
		))
	}
	return boxbotapi.NewInlineKeyboardMarkup(rows...)
}

func (s *Service) backMarkup(language string) boxbotapi.InlineKeyboardMarkup {
	english := normalizeLanguage(language) == "en"
	buttons := []boxbotapi.InlineKeyboardButton{
		buttonData(choice(english, "Back to menu", "返回介绍"), "alert:intro"),
	}
	if s.publicAppURL != "" {
		buttons = append(buttons, buttonURL(
			choice(english, "Monitoring Dashboard", "个人监控面板"),
			s.publicAppURL,
		))
	}
	return boxbotapi.NewInlineKeyboardMarkup(
		boxbotapi.NewInlineKeyboardRow(buttons...),
	)
}

func (s *Service) groupEntryMarkup(_ string) boxbotapi.InlineKeyboardMarkup {
	buttons := make([]boxbotapi.InlineKeyboardButton, 0, 2)
	if privateURL := s.botPrivateChatURL(); privateURL != "" {
		buttons = append(buttons, buttonURL("私聊 Bot / Message Bot", privateURL))
	}
	if s.publicAppURL != "" {
		buttons = append(buttons, buttonURL("监控面板 / Dashboard", s.publicAppURL))
	}
	if len(buttons) == 0 {
		return boxbotapi.NewInlineKeyboardMarkup()
	}
	return boxbotapi.NewInlineKeyboardMarkup(
		boxbotapi.NewInlineKeyboardRow(buttons...),
	)
}

func (s *Service) swapMarkup(language string) boxbotapi.InlineKeyboardMarkup {
	english := normalizeLanguage(language) == "en"
	return boxbotapi.NewInlineKeyboardMarkup(
		boxbotapi.NewInlineKeyboardRow(
			buttonChain(
				choice(english, "Start swap", "开始兑换"),
				swapPayload(s.deps.Settings.SubscriptionTokenAddress),
			),
			buttonData(choice(english, "Back", "返回"), "alert:intro"),
		),
	)
}

func (s *Service) callbackMarkup(
	data string,
	language string,
) boxbotapi.InlineKeyboardMarkup {
	if data == "alert:intro" || strings.HasPrefix(data, "alert:language:") {
		return s.menuMarkup(language)
	}
	if data == "alert:swap" {
		return s.swapMarkup(language)
	}
	return s.backMarkup(language)
}

func buttonData(text, data string) boxbotapi.InlineKeyboardButton {
	return boxbotapi.NewInlineKeyboardButtonData(text, data)
}

func buttonURL(text, target string) boxbotapi.InlineKeyboardButton {
	return boxbotapi.NewInlineKeyboardButtonURL(text, target)
}

func buttonChain(text, payload string) boxbotapi.InlineKeyboardButton {
	return boxbotapi.NewInlineKeyboardButtonDataWithColor(
		text,
		payload,
		"debox://wallet/request",
		"",
		"#16C784",
	)
}

func swapPayload(tokenAddress string) string {
	payload := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []struct {
			FromAddress string `json:"fromAddress"`
			ToAddress   string `json:"toAddress"`
			FromChainID string `json:"fromChainId"`
			ToChainID   string `json:"toChainId"`
		} `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      106,
		Method:  "swap",
	}
	payload.Params = append(payload.Params, struct {
		FromAddress string `json:"fromAddress"`
		ToAddress   string `json:"toAddress"`
		FromChainID string `json:"fromChainId"`
		ToChainID   string `json:"toChainId"`
	}{
		FromAddress: "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE",
		ToAddress:   tokenAddress,
		FromChainID: "0x38",
		ToChainID:   "0x38",
	})
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func extractAddress(payload map[string]any) string {
	candidates := []map[string]any{payload}
	if nested, ok := payload["data"].(map[string]any); ok {
		candidates = append(candidates, nested)
	}
	for _, candidate := range candidates {
		for _, key := range []string{"address", "walletAddress", "wallet_address"} {
			if value := strings.TrimSpace(fmt.Sprint(candidate[key])); value != "" &&
				value != "<nil>" {
				return value
			}
		}
	}
	return ""
}

func escapeText(value string) string {
	return html.EscapeString(value)
}

func shortAddress(value string) string {
	if len(value) <= 16 {
		return value
	}
	return value[:8] + "..." + value[len(value)-6:]
}

func choice(english bool, englishText, chineseText string) string {
	if english {
		return englishText
	}
	return chineseText
}
