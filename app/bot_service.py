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


def menu_markup(show_intro: bool = False) -> boxbotapi.InlineKeyboardMarkup:
    app_url = public_app_url()
    rows = [
        boxbotapi.NewInlineKeyboardRow(
            _button_data("监控能力", "alert:features"),
            _button_data("订阅方案", "alert:plans"),
        ),
        boxbotapi.NewInlineKeyboardRow(
            _button_data("订阅有效期", "alert:subscription"),
            _button_data("余额查询", "alert:balance"),
        ),
        boxbotapi.NewInlineKeyboardRow(
            _button_data("闪兑", "alert:swap"),
            _button_url("快捷续费", f"{app_url}#renew")
            if app_url
            else _button_data("快捷续费", "alert:renew"),
        ),
    ]
    if app_url:
        rows.append(boxbotapi.NewInlineKeyboardRow(_button_url("个人监控面板", app_url)))
    if show_intro:
        rows.append(boxbotapi.NewInlineKeyboardRow(_button_data("介绍", "alert:intro")))
    return boxbotapi.NewInlineKeyboardMarkup(*rows)


def back_markup(include_panel: bool = True) -> boxbotapi.InlineKeyboardMarkup:
    app_url = public_app_url()
    buttons = [_button_data("返回介绍", "alert:intro")]
    if include_panel and app_url:
        buttons.append(_button_url("个人监控面板", app_url))
    return boxbotapi.NewInlineKeyboardMarkup(boxbotapi.NewInlineKeyboardRow(*buttons))


def group_entry_markup(bot: boxbotapi.BotAPI | None = None) -> boxbotapi.InlineKeyboardMarkup:
    buttons = []
    private_url = bot_private_chat_url(bot)
    app_url = public_app_url()
    if private_url:
        buttons.append(_button_url("私聊 Bot", private_url))
    if app_url:
        buttons.append(_button_url("个人监控面板", app_url))
    if not buttons:
        return boxbotapi.NewInlineKeyboardMarkup()
    return boxbotapi.NewInlineKeyboardMarkup(boxbotapi.NewInlineKeyboardRow(*buttons))


def menu_text() -> str:
    return (
        "<b>DeBox Asset Alert</b><br/>"
        "监控钱包地址或代币资产变化，通过 DeBox Bot 实时推送通知。<br/><br/>"
        "支持：多链余额监控、代币识别、私聊通知、专业版群通知、每日摘要等。"
    )


def group_entry_text(message: boxbotapi.Message) -> str:
    user_name = ""
    if message.From is not None:
        user_name = message.From.Name or message.From.UserId
    prefix = f"@{escape(user_name)} " if user_name else ""
    return f"{prefix}我是 DeBox Asset Alert 链上监控助理，请私聊 Bot 或打开个人监控面板。"


def features_text() -> str:
    return (
        "<b>监控能力</b><br/><br/>"
        "支持 BNB Chain、Ethereum、Base、Polygon、Arbitrum、Optimism。<br/><br/>"
        "可监控原生资产余额，也可填写 ERC20 合约监控代币余额。<br/><br/>"
        "- 规则包括：<br/>"
        "• 余额变化<br/>"
        "• 转入<br/>"
        "• 转出<br/>"
        "• 余额阈值<br/>"
        "• 授权变化<br/>"
        "• 指定地址交互<br/><br/>"
        "<b>标准版</b>支持私聊通知和每日摘要；<br/><br/>"
        "<b>专业版</b>支持群通知和更多高级规则。"
    )


def plans_text() -> str:
    return (
        "<b>订阅方案</b><br/><br/>"
        "免费体验：1 个钱包，1 条规则，24 小时，仅私聊通知。<br/><br/>"
        f"标准版：{settings.subscription_price} {settings.subscription_token_symbol} / "
        f"{settings.subscription_days} 天，3 个钱包，10 条规则，支持资产变化和授权监控。<br/><br/>"
        "专业版：25 USDT / 30 天，20 个钱包，100 条规则，支持群通知和指定地址交互。<br/><br/>"
        "同一时间只能有一个有效付费套餐；同套餐可提前续费并顺延到期时间。"
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


def subscription_text(debox_user_id: str) -> str:
    if not debox_user_id:
        return "暂时无法识别你的 DeBox 用户 ID，请打开个人监控面板查看订阅。"
    current = entitlement(debox_user_id)
    plan = current.get("plan") or {}
    subscription = current.get("subscription") or {}
    return (
        "<b>订阅有效期</b><br/>"
        f"当前方案：{escape(plan.get('name', '未开通'))}<br/>"
        f"剩余天数：{escape(str(current.get('days_remaining', '0')))} 天<br/>"
        f"到期时间：{escape(str(subscription.get('expires_at', '-')))}<br/>"
        f"监控规则：{current.get('rule_count', 0)} / {plan.get('rule_limit', 0)}<br/>"
        f"群通知：{current.get('group_count', 0)} / {plan.get('group_limit', 0)}"
    )


def balance_text(debox_user_id: str) -> str:
    if not debox_user_id:
        return "暂时无法识别你的 DeBox 用户 ID，请打开个人监控面板查询余额。"
    profile = user_info(user_id=debox_user_id)
    address = _extract_address(profile).strip()
    if not address:
        return "没有从 DeBox 用户资料中识别到钱包地址，请在个人监控面板连接钱包后查询。"
    current = balance(address, settings.subscription_token_address, "bsc")
    gas = balance(address, None, "bsc")
    return (
        "<b>余额查询</b><br/>"
        f"钱包：{escape(address[:8])}...{escape(address[-6:])}<br/>"
        f"网络：{escape(current['chain_name'])}<br/>"
        f"余额：{escape(current['value'])} {escape(current['symbol'])}<br/>"
        f"Gas 费余额：{escape(gas['value'])} {escape(gas['symbol'])}"
    )


def swap_text() -> str:
    return "<b>闪兑</b><br/>将资产兑换为 BSC 链 USDT"


def swap_markup() -> boxbotapi.InlineKeyboardMarkup:
    return boxbotapi.NewInlineKeyboardMarkup(
        boxbotapi.NewInlineKeyboardRow(
            _button_chain("开始兑换", swap_payload()),
            _button_data("返回", "alert:intro"),
        )
    )


def callback_text(data: str, debox_user_id: str = "") -> str:
    try:
        if data == "alert:intro":
            return menu_text()
        if data == "alert:features":
            return features_text()
        if data == "alert:plans":
            return plans_text()
        if data == "alert:subscription":
            return subscription_text(debox_user_id)
        if data == "alert:balance":
            return balance_text(debox_user_id)
        if data == "alert:swap":
            return swap_text()
        if data == "alert:renew":
            app_url = public_app_url()
            return f"请打开个人监控面板续费：{escape(app_url)}" if app_url else "请在 H5 中续费。"
    except Exception as exc:
        return f"操作失败：{escape(str(exc))}"
    return menu_text()


def callback_markup(data: str) -> boxbotapi.InlineKeyboardMarkup:
    if data == "alert:intro":
        return menu_markup()
    if data == "alert:swap":
        return swap_markup()
    return back_markup()


def send_menu(bot: boxbotapi.BotAPI, chat_id: str, chat_type: str) -> None:
    message = boxbotapi.NewMessage(chat_id, chat_type, menu_text())
    message.ParseMode = boxbotapi.ModeHTML
    message.ReplyMarkup = menu_markup()
    bot.Send(message)


def send_group_entry(bot: boxbotapi.BotAPI, message: boxbotapi.Message) -> None:
    if message.Chat is None:
        return
    response = boxbotapi.NewMessage(message.Chat.ID, message.Chat.Type, group_entry_text(message))
    response.ParseMode = boxbotapi.ModeHTML
    response.ReplyMarkup = group_entry_markup(bot)
    bot.Send(response)


def handle_message(bot: boxbotapi.BotAPI, message: boxbotapi.Message) -> None:
    if message.Chat is None:
        return
    text = (message.Text or "").strip().lower()
    if text in {"/start", "start", "菜单", "menu"}:
        if message.Chat.Type == "group":
            send_group_entry(bot, message)
            return
        send_menu(bot, message.Chat.ID, message.Chat.Type)


def handle_callback(bot: boxbotapi.BotAPI, query: boxbotapi.CallbackQuery) -> None:
    if query.Message is None or query.Message.Chat is None:
        return
    message = boxbotapi.NewEditMessageTextAndMarkup(
        query.Message.Chat.ID,
        query.Message.Chat.Type,
        query.Message.MessageID,
        callback_text(query.Data, user_id_from_query(query)),
        callback_markup(query.Data),
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

    bot_config.Debug = False
    bot_config.MessageListener = True
    write_status("starting")

    try:
        bot = boxbotapi.NewBotAPI(settings.debox_bot_api_key, settings.debox_bot_api_secret)
        write_status("running", "Long Polling is active", bot)
        logging.info("Bot listener started for %s (%s)", bot.Self.Name, bot.Self.UserId)

        update_config = boxbotapi.NewUpdate(0)
        update_config.Timeout = 10

        while True:
            try:
                updates = bot.GetUpdates(update_config)
            except Exception as exc:
                logging.exception("Long Polling request failed")
                write_status("degraded", f"Long Polling error: {exc}", bot)
                time.sleep(2)
                continue

            write_status("running", "Long Polling is active", bot)
            for update in updates:
                update_config.Offset = max(update_config.Offset, update.Id + 1)
                try:
                    if update.Message is not None:
                        handle_message(bot, update.Message)
                    elif update.CallbackQuery is not None:
                        handle_callback(bot, update.CallbackQuery)
                except Exception:
                    logging.exception("Failed to handle update=%s", update.Id)
    except KeyboardInterrupt:
        write_status("stopped", "Stopped by user")
    except Exception as exc:
        write_status("error", str(exc))
        raise
