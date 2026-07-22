from __future__ import annotations

from html import escape
import json
import logging
import time
from typing import Any

import boxbotapi
from boxbotapi import configs as bot_config

from app.chain_service import balance
from app.config import ROOT_DIR, settings
from app.db import get_user_preferences, initialize_database, set_bot_language
from app.languages import DEFAULT_LANGUAGE, normalize_language
from app.openapi_service import user_info
from app.subscription_service import entitlement


STATUS_PATH = ROOT_DIR / "data" / "bot_status.json"
LOG_PATH = ROOT_DIR / "data" / "bot.log"
WEBHOOK_EVENTS_PATH = ROOT_DIR / "data" / "webhook-events.jsonl"
PUBLIC_URL_PATH = ROOT_DIR / "data" / "public_url.txt"


def public_app_url() -> str:
    if PUBLIC_URL_PATH.exists():
        url = PUBLIC_URL_PATH.read_text(encoding="utf-8").strip()
        if url.startswith("https://"):
            return url.rstrip("/")
    return settings.public_app_url.rstrip("/")


def bot_private_chat_url(bot: boxbotapi.BotAPI | None = None) -> str:
    bot_user_id = settings.debox_bot_user_id
    if not bot_user_id and bot is not None:
        bot_user_id = getattr(bot.Self, "UserId", "") or ""
    if not bot_user_id and STATUS_PATH.exists():
        try:
            status = json.loads(STATUS_PATH.read_text(encoding="utf-8"))
            bot_user_id = str(status.get("bot_user_id", "") or "").strip()
        except json.JSONDecodeError:
            bot_user_id = ""
    return f"https://m.debox.pro/user/chat?id={bot_user_id}&start=" if bot_user_id else ""


def write_status(state: str, detail: str = "", bot: boxbotapi.BotAPI | None = None) -> None:
    STATUS_PATH.parent.mkdir(parents=True, exist_ok=True)
    self_info = getattr(bot, "Self", None)
    payload = {
        "state": state,
        "detail": detail,
        "updated_at": int(time.time()),
        "bot_user_id": getattr(self_info, "UserId", "") if self_info else "",
        "bot_name": getattr(self_info, "Name", "") if self_info else "",
    }
    temporary = STATUS_PATH.with_suffix(".tmp")
    temporary.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
    temporary.replace(STATUS_PATH)


def _button_data(text: str, data: str) -> Any:
    return boxbotapi.NewInlineKeyboardButtonData(text, data)


def _button_url(text: str, url: str) -> Any:
    return boxbotapi.NewInlineKeyboardButtonURL(text, url)


def _button_chain(text: str, payload: str) -> Any:
    return boxbotapi.NewInlineKeyboardButtonDataWithColor(
        text,
        payload,
        "debox://wallet/request",
        "",
        "#16C784",
    )


def _is_english(language: str) -> bool:
    return normalize_language(language) == "en"


def user_id_from_message(message: boxbotapi.Message) -> str:
    user = getattr(message, "From", None) or getattr(message, "User", None)
    if user is None:
        return ""
    return str(
        getattr(user, "UserId", "")
        or getattr(user, "ID", "")
        or getattr(user, "Id", "")
        or ""
    ).strip()


def text_from_message(message: boxbotapi.Message) -> str:
    return str(
        getattr(message, "Text", "")
        or getattr(message, "TextRaw", "")
        or ""
    ).strip()


def bot_language_for_user(debox_user_id: str) -> str:
    if not debox_user_id:
        return DEFAULT_LANGUAGE
    return normalize_language(get_user_preferences(debox_user_id).get("bot_language"))


def swap_payload() -> str:
    return json.dumps(
        {
            "jsonrpc": "2.0",
            "id": 106,
            "method": "swap",
            "params": [
                {
                    "fromAddress": "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE",
                    "toAddress": settings.subscription_token_address,
                    "fromChainId": "0x38",
                    "toChainId": "0x38",
                }
            ],
        },
        ensure_ascii=False,
        separators=(",", ":"),
    )


def menu_markup(language: str = DEFAULT_LANGUAGE, show_intro: bool = False) -> boxbotapi.InlineKeyboardMarkup:
    english = _is_english(language)
    app_url = public_app_url()
    rows = [
        boxbotapi.NewInlineKeyboardRow(
            _button_data("Monitoring" if english else "监控能力", "alert:features"),
            _button_data("Plans" if english else "订阅方案", "alert:plans"),
        ),
        boxbotapi.NewInlineKeyboardRow(
            _button_data("Subscription" if english else "订阅有效期", "alert:subscription"),
            _button_data("Balance" if english else "余额查询", "alert:balance"),
        ),
        boxbotapi.NewInlineKeyboardRow(
            _button_data("Swap" if english else "闪兑", "alert:swap"),
            _button_url("Renew" if english else "快捷续费", f"{app_url}#renew")
            if app_url
            else _button_data("Renew" if english else "快捷续费", "alert:renew"),
        ),
    ]
    if app_url:
        rows.append(
            boxbotapi.NewInlineKeyboardRow(
                _button_url("Monitoring Dashboard" if english else "个人监控面板", app_url)
            )
        )
    rows.append(
        boxbotapi.NewInlineKeyboardRow(
            _button_data("中文" if english else "English", f"alert:language:{'zh' if english else 'en'}")
        )
    )
    if show_intro:
        rows.append(
            boxbotapi.NewInlineKeyboardRow(
                _button_data("Introduction" if english else "介绍", "alert:intro")
            )
        )
    return boxbotapi.NewInlineKeyboardMarkup(*rows)


def back_markup(
    language: str = DEFAULT_LANGUAGE,
    include_panel: bool = True,
) -> boxbotapi.InlineKeyboardMarkup:
    english = _is_english(language)
    app_url = public_app_url()
    buttons = [_button_data("Back to menu" if english else "返回介绍", "alert:intro")]
    if include_panel and app_url:
        buttons.append(_button_url("Monitoring Dashboard" if english else "个人监控面板", app_url))
    return boxbotapi.NewInlineKeyboardMarkup(boxbotapi.NewInlineKeyboardRow(*buttons))


def group_entry_markup(
    bot: boxbotapi.BotAPI | None = None,
    language: str = DEFAULT_LANGUAGE,
) -> boxbotapi.InlineKeyboardMarkup:
    english = _is_english(language)
    buttons = []
    private_url = bot_private_chat_url(bot)
    app_url = public_app_url()
    if private_url:
        buttons.append(_button_url("Message Bot" if english else "私聊 Bot", private_url))
    if app_url:
        buttons.append(_button_url("Monitoring Dashboard" if english else "个人监控面板", app_url))
    if not buttons:
        return boxbotapi.NewInlineKeyboardMarkup()
    return boxbotapi.NewInlineKeyboardMarkup(boxbotapi.NewInlineKeyboardRow(*buttons))


def menu_text(language: str = DEFAULT_LANGUAGE) -> str:
    if _is_english(language):
        return (
            "<b>DeBox Asset Alert</b><br/>"
            "Monitor wallet addresses and token balance changes with real-time alerts from DeBox Bot.<br/><br/>"
            "Features include multi-chain balance monitoring, token detection, private alerts, "
            "group alerts for Professional users, and daily summaries.<br/><br/>"
            "Open the monitoring dashboard and sign with your wallet to securely sign in. "
            "Signing sends no transaction and uses no gas."
        )
    return (
        "<b>DeBox Asset Alert</b><br/>"
        "监控钱包地址或代币资产变化，通过 DeBox Bot 实时推送通知。<br/><br/>"
        "支持：多链余额监控、代币识别、私聊通知、专业版群通知、每日摘要等。<br/><br/>"
        "打开个人监控面板后，通过钱包签名完成安全登录；签名不会发起交易或消耗 Gas。"
    )


def group_entry_text(message: boxbotapi.Message, language: str = DEFAULT_LANGUAGE) -> str:
    user_name = ""
    if message.From is not None:
        user_name = message.From.Name or message.From.UserId
    prefix = f"@{escape(user_name)} " if user_name else ""
    if _is_english(language):
        return f"{prefix}I am the DeBox Asset Alert assistant. Message the Bot or open your monitoring dashboard."
    return f"{prefix}我是 DeBox Asset Alert 链上监控助理，请私聊 Bot 或打开个人监控面板。"


def features_text(language: str = DEFAULT_LANGUAGE) -> str:
    if _is_english(language):
        return (
            "<b>Monitoring</b><br/><br/>"
            "Supported networks: BNB Chain, Ethereum, Base, Polygon, Arbitrum, and Optimism.<br/><br/>"
            "Monitor native asset balances, or enter an ERC-20 contract to monitor a token balance.<br/><br/>"
            "- Rule types:<br/>"
            "• Balance change<br/>"
            "• Incoming transfer<br/>"
            "• Outgoing transfer<br/>"
            "• Balance threshold: alerts immediately if the balance is already at or below the threshold when created; it does not repeat while below, and alerts again after recovery above the threshold and another drop<br/>"
            "• Approval change<br/>"
            "• Specified address interaction<br/><br/>"
            "<b>Standard</b> includes private alerts and daily summaries.<br/><br/>"
            "<b>Professional</b> includes group alerts and more advanced rules.<br/><br/>"
            "Each summary covers the previous scheduled cutoff through the current cutoff; the first covers the previous 24 hours and includes notification failures.<br/><br/>"
            "If a summary group is unbound, delivery switches to private chat. If private confirmation fails, the daily summary is turned off."
        )
    return (
        "<b>监控能力</b><br/><br/>"
        "支持 BNB Chain、Ethereum、Base、Polygon、Arbitrum、Optimism。<br/><br/>"
        "可监控原生资产余额，也可填写 ERC20 合约监控代币余额。<br/><br/>"
        "- 规则包括：<br/>"
        "• 余额变化<br/>"
        "• 转入<br/>"
        "• 转出<br/>"
        "• 余额阈值：创建规则时余额已达到或低于阈值会立即提醒一次；持续低于不重复，回升至阈值以上后再次跌破会重新提醒<br/>"
        "• 授权变化<br/>"
        "• 指定地址交互<br/><br/>"
        "<b>标准版</b>支持私聊通知和每日摘要；<br/><br/>"
        "<b>专业版</b>支持群通知和更多高级规则。<br/><br/>"
        "每期摘要统计上一次计划推送时间至本次计划推送时间；首次统计此前 24 小时，并显示本期通知失败次数。<br/><br/>"
        "解绑摘要群后会自动切回本人私聊；若私聊确认失败，每日摘要会关闭。"
    )


def plans_text(language: str = DEFAULT_LANGUAGE) -> str:
    if _is_english(language):
        return (
            "<b>Plans</b><br/><br/>"
            "Free: 1 wallet, 1 basic rule, no expiration, up to 5 alerts per day, private alerts only.<br/><br/>"
            f"Standard: {settings.subscription_price} {settings.subscription_token_symbol} / "
            f"{settings.subscription_days} days, 3 wallets, 10 rules, including asset and approval monitoring.<br/><br/>"
            "Professional: 25 USDT / 30 days, 20 wallets, 100 rules, including group alerts and specified address interactions.<br/><br/>"
            "While a paid plan is active, only the same plan can be renewed; choose another plan after it expires.<br/><br/>"
            "Pay with USDT on BNB Chain. The subscription activates after 3 block confirmations; failed verification does not activate it.<br/><br/>"
            "Subscriptions take effect immediately. Digital service purchases are non-refundable, so please review the plan before purchase."
        )
    return (
        "<b>订阅方案</b><br/><br/>"
        "免费版：1 个钱包，1 条基础规则，永久有效，每日最多 5 次提醒，仅私聊通知。<br/><br/>"
        f"标准版：{settings.subscription_price} {settings.subscription_token_symbol} / "
        f"{settings.subscription_days} 天，3 个钱包，10 条规则，支持资产变化和授权监控。<br/><br/>"
        "专业版：25 USDT / 30 天，20 个钱包，100 条规则，支持群通知和指定地址交互。<br/><br/>"
        "付费套餐有效期内只能续费同一套餐并顺延到期时间；套餐到期后才能选择其他套餐。<br/><br/>"
        "使用 BNB Chain USDT 支付，交易达到 3 个区块确认后开通订阅；支付验证失败不会开通。<br/><br/>"
        "订阅开通后立即生效，虚拟服务类权益不支持退款，请确认套餐内容后再购买。"
    )


def user_id_from_query(query: boxbotapi.CallbackQuery) -> str:
    user = getattr(query, "From", None) or getattr(query, "User", None)
    if user is None:
        return ""
    return str(
        getattr(user, "UserId", "")
        or getattr(user, "ID", "")
        or getattr(user, "Id", "")
        or ""
    ).strip()


def _extract_address(payload: dict) -> str:
    candidates = [payload]
    data = payload.get("data")
    if isinstance(data, dict):
        candidates.append(data)
    for item in candidates:
        for key in ("address", "walletAddress", "wallet_address"):
            value = item.get(key)
            if value:
                return str(value)
    return ""


def _plan_name(plan: dict, language: str) -> str:
    if not _is_english(language):
        return str(plan.get("name") or "未开通")
    return {
        "free": "Free",
        "standard": "Standard",
        "professional": "Professional",
    }.get(str(plan.get("code") or ""), str(plan.get("name") or "Not active"))


def subscription_text(debox_user_id: str, language: str = DEFAULT_LANGUAGE) -> str:
    english = _is_english(language)
    if not debox_user_id:
        return (
            "We could not identify your DeBox User ID. Open the monitoring dashboard to view your subscription."
            if english
            else "暂时无法识别你的 DeBox 用户 ID，请打开个人监控面板查看订阅。"
        )
    current = entitlement(debox_user_id)
    plan = current.get("plan") or {}
    subscription = current.get("subscription") or {}
    plan_name = escape(_plan_name(plan, language))
    if plan.get("code") == "free":
        if english:
            return (
                "<b>Subscription</b><br/>"
                f"Current plan: {plan_name}<br/>"
                "Valid through: No expiration<br/>"
                f"Monitoring rules: {current.get('rule_count', 0)} / {plan.get('rule_limit', 0)}<br/>"
                f"Group alerts: {current.get('group_count', 0)} / {plan.get('group_limit', 0)}"
            )
        return (
            "<b>订阅有效期</b><br/>"
            f"当前方案：{plan_name}<br/>"
            "有效期：永久有效<br/>"
            f"监控规则：{current.get('rule_count', 0)} / {plan.get('rule_limit', 0)}<br/>"
            f"群通知：{current.get('group_count', 0)} / {plan.get('group_limit', 0)}"
        )
    if english:
        return (
            "<b>Subscription</b><br/>"
            f"Current plan: {plan_name}<br/>"
            f"Days remaining: {escape(str(current.get('days_remaining', '0')))}<br/>"
            f"Expires at: {escape(str(subscription.get('expires_at', '-')))}<br/>"
            f"Monitoring rules: {current.get('rule_count', 0)} / {plan.get('rule_limit', 0)}<br/>"
            f"Group alerts: {current.get('group_count', 0)} / {plan.get('group_limit', 0)}"
        )
    return (
        "<b>订阅有效期</b><br/>"
        f"当前方案：{plan_name}<br/>"
        f"剩余天数：{escape(str(current.get('days_remaining', '0')))} 天<br/>"
        f"到期时间：{escape(str(subscription.get('expires_at', '-')))}<br/>"
        f"监控规则：{current.get('rule_count', 0)} / {plan.get('rule_limit', 0)}<br/>"
        f"群通知：{current.get('group_count', 0)} / {plan.get('group_limit', 0)}"
    )


def balance_text(debox_user_id: str, language: str = DEFAULT_LANGUAGE) -> str:
    english = _is_english(language)
    if not debox_user_id:
        return (
            "We could not identify your DeBox User ID. Open the monitoring dashboard to check your balance."
            if english
            else "暂时无法识别你的 DeBox 用户 ID，请打开个人监控面板查询余额。"
        )
    profile = user_info(user_id=debox_user_id)
    address = _extract_address(profile).strip()
    if not address:
        return (
            "No wallet address was found in your DeBox profile. Connect a wallet in the monitoring dashboard first."
            if english
            else "没有从 DeBox 用户资料中识别到钱包地址，请在个人监控面板连接钱包后查询。"
        )
    current = balance(address, settings.subscription_token_address, "bsc")
    gas = balance(address, None, "bsc")
    if english:
        return (
            "<b>Balance</b><br/>"
            f"Wallet: {escape(address[:8])}...{escape(address[-6:])}<br/>"
            f"Network: {escape(current['chain_name'])}<br/>"
            f"Balance: {escape(current['value'])} {escape(current['symbol'])}<br/>"
            f"Gas balance: {escape(gas['value'])} {escape(gas['symbol'])}"
        )
    return (
        "<b>余额查询</b><br/>"
        f"钱包：{escape(address[:8])}...{escape(address[-6:])}<br/>"
        f"网络：{escape(current['chain_name'])}<br/>"
        f"余额：{escape(current['value'])} {escape(current['symbol'])}<br/>"
        f"Gas 费余额：{escape(gas['value'])} {escape(gas['symbol'])}"
    )


def swap_text(language: str = DEFAULT_LANGUAGE) -> str:
    if _is_english(language):
        return "<b>Swap</b><br/>Swap assets for USDT on BSC"
    return "<b>闪兑</b><br/>将资产兑换为 BSC 链 USDT"


def swap_markup(language: str = DEFAULT_LANGUAGE) -> boxbotapi.InlineKeyboardMarkup:
    english = _is_english(language)
    return boxbotapi.NewInlineKeyboardMarkup(
        boxbotapi.NewInlineKeyboardRow(
            _button_chain("Start swap" if english else "开始兑换", swap_payload()),
            _button_data("Back" if english else "返回", "alert:intro"),
        )
    )


def callback_text(
    data: str,
    debox_user_id: str = "",
    language: str = DEFAULT_LANGUAGE,
) -> str:
    english = _is_english(language)
    try:
        if data == "alert:intro" or data.startswith("alert:language:"):
            return menu_text(language)
        if data == "alert:features":
            return features_text(language)
        if data == "alert:plans":
            return plans_text(language)
        if data == "alert:subscription":
            return subscription_text(debox_user_id, language)
        if data == "alert:balance":
            return balance_text(debox_user_id, language)
        if data == "alert:swap":
            return swap_text(language)
        if data == "alert:renew":
            app_url = public_app_url()
            if english:
                return (
                    f"Open the monitoring dashboard to renew: {escape(app_url)}"
                    if app_url
                    else "Please renew in the H5 app."
                )
            return f"请打开个人监控面板续费：{escape(app_url)}" if app_url else "请在 H5 中续费。"
    except Exception as exc:
        return "Operation failed. Please try again later." if english else f"操作失败：{escape(str(exc))}"
    return menu_text(language)


def callback_markup(data: str, language: str = DEFAULT_LANGUAGE) -> boxbotapi.InlineKeyboardMarkup:
    if data == "alert:intro" or data.startswith("alert:language:"):
        return menu_markup(language)
    if data == "alert:swap":
        return swap_markup(language)
    return back_markup(language)


def send_menu(
    bot: boxbotapi.BotAPI,
    chat_id: str,
    chat_type: str,
    language: str = DEFAULT_LANGUAGE,
) -> None:
    message = boxbotapi.NewMessage(chat_id, chat_type, menu_text(language))
    message.ParseMode = boxbotapi.ModeHTML
    message.ReplyMarkup = menu_markup(language)
    bot.Send(message)


def send_group_entry(bot: boxbotapi.BotAPI, message: boxbotapi.Message) -> None:
    if message.Chat is None:
        return
    language = bot_language_for_user(user_id_from_message(message))
    response = boxbotapi.NewMessage(
        message.Chat.ID,
        message.Chat.Type,
        group_entry_text(message, language),
    )
    response.ParseMode = boxbotapi.ModeHTML
    response.ReplyMarkup = group_entry_markup(bot, language)
    bot.Send(response)


def handle_message(bot: boxbotapi.BotAPI, message: boxbotapi.Message) -> None:
    if message.Chat is None:
        return
    text = text_from_message(message).lower()
    if text in {"/start", "start", "菜单", "menu"}:
        if message.Chat.Type == "group":
            send_group_entry(bot, message)
            return
        language = bot_language_for_user(user_id_from_message(message))
        send_menu(bot, message.Chat.ID, message.Chat.Type, language)


def handle_callback(bot: boxbotapi.BotAPI, query: boxbotapi.CallbackQuery) -> None:
    if query.Message is None or query.Message.Chat is None:
        return
    debox_user_id = user_id_from_query(query)
    data = str(query.Data or "")
    language = bot_language_for_user(debox_user_id)
    if data.startswith("alert:language:"):
        language = normalize_language(data.rsplit(":", 1)[-1])
        if debox_user_id:
            set_bot_language(debox_user_id, language)
    message = boxbotapi.NewEditMessageTextAndMarkup(
        query.Message.Chat.ID,
        query.Message.Chat.Type,
        query.Message.MessageID,
        callback_text(data, debox_user_id, language),
        callback_markup(data, language),
    )
    message.ParseMode = boxbotapi.ModeHTML
    bot.Send(message)


def handle_webhook_payload(payload: dict[str, Any]) -> dict:
    bot = boxbotapi.NewBotAPI(settings.debox_bot_api_key, settings.debox_bot_api_secret)
    factory = getattr(boxbotapi.Update, "from_dict", None)
    if factory is None:
        return {"ok": True, "kind": "unsupported"}
    update = factory(payload)
    if update is not None and (update.Message is not None or update.CallbackQuery is not None):
        if update.Message is not None:
            handle_message(bot, update.Message)
            return {"ok": True, "kind": "message", "update_id": update.Id}
        handle_callback(bot, update.CallbackQuery)
        return {"ok": True, "kind": "callback", "update_id": update.Id}
    return {"ok": True, "kind": "ignored"}


def record_webhook_event(payload: dict[str, Any]) -> None:
    WEBHOOK_EVENTS_PATH.parent.mkdir(parents=True, exist_ok=True)
    record = {"received_at": int(time.time()), "payload": payload}
    with WEBHOOK_EVENTS_PATH.open("a", encoding="utf-8") as output:
        output.write(json.dumps(record, ensure_ascii=False) + "\n")


def run() -> None:
    LOG_PATH.parent.mkdir(parents=True, exist_ok=True)
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
        handlers=[
            logging.FileHandler(LOG_PATH, encoding="utf-8"),
            logging.StreamHandler(),
        ],
    )
    if not settings.debox_bot_api_key:
        raise RuntimeError("DEBOX_BOT_API_KEY is required")
    if settings.debox_bot_receive_mode != "polling":
        raise RuntimeError("Long Polling is disabled. Run the FastAPI service and configure /bot/webhook.")

    initialize_database()
    bot_config.Debug = False
    bot_config.MessageListener = True
    write_status("starting")

    try:
        bot = boxbotapi.NewBotAPI(settings.debox_bot_api_key, settings.debox_bot_api_secret)
        write_status("running", "Long Polling is active", bot)
        logging.info("Bot listener started for %s (%s)", bot.Self.Name, bot.Self.UserId)

        update_config = boxbotapi.NewUpdate(0)
        update_config.Timeout = 10
        last_health_log = 0.0

        while True:
            try:
                updates = bot.GetUpdates(update_config)
            except Exception as exc:
                logging.exception("Long Polling request failed")
                write_status("degraded", f"Long Polling error: {exc}", bot)
                time.sleep(2)
                continue

            write_status("running", "Long Polling is active", bot)
            now = time.monotonic()
            if updates or now - last_health_log >= 60:
                logging.info(
                    "Bot polling healthy: updates=%s offset=%s",
                    len(updates),
                    update_config.Offset,
                )
                last_health_log = now
            for update in updates:
                update_config.Offset = max(update_config.Offset, update.Id + 1)
                try:
                    if update.Message is not None:
                        message_text = text_from_message(update.Message).lower()
                        logging.info(
                            "Received bot message: update=%s has_chat=%s chat_type=%s "
                            "has_text=%s is_start=%s",
                            update.Id,
                            update.Message.Chat is not None,
                            getattr(update.Message.Chat, "Type", ""),
                            bool(message_text),
                            message_text in {"/start", "start", "菜单", "menu"},
                        )
                        handle_message(bot, update.Message)
                    elif update.CallbackQuery is not None:
                        logging.info("Received bot callback: update=%s", update.Id)
                        handle_callback(bot, update.CallbackQuery)
                except Exception:
                    logging.exception("Failed to handle update=%s", update.Id)
    except KeyboardInterrupt:
        write_status("stopped", "Stopped by user")
    except Exception as exc:
        write_status("error", str(exc))
        raise
