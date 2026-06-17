from __future__ import annotations

from html import escape
import json
import logging
from pathlib import Path
import time
from typing import Any

import boxbotapi
from boxbotapi import configs as bot_config

from app.config import ROOT_DIR, settings


STATUS_PATH = ROOT_DIR / "data" / "bot_status.json"
LOG_PATH = ROOT_DIR / "data" / "bot.log"
WEBHOOK_EVENTS_PATH = ROOT_DIR / "data" / "webhook-events.jsonl"
PUBLIC_URL_PATH = ROOT_DIR / "data" / "public_url.txt"


def public_app_url() -> str:
    if PUBLIC_URL_PATH.exists():
        url = PUBLIC_URL_PATH.read_text(encoding="utf-8").strip()
        if url.startswith("https://"):
            return url
    return settings.public_app_url


def bot_private_chat_url(bot: boxbotapi.BotAPI | None = None) -> str:
    bot_user_id = settings.debox_bot_user_id
    if not bot_user_id and bot is not None:
        bot_user_id = bot.Self.UserId
    if not bot_user_id and STATUS_PATH.exists():
        try:
            status = json.loads(STATUS_PATH.read_text(encoding="utf-8"))
            bot_user_id = str(status.get("bot_user_id", "") or "").strip()
        except json.JSONDecodeError:
            bot_user_id = ""
    if not bot_user_id:
        return ""
    return f"https://m.debox.pro/user/chat?id={bot_user_id}&start="


def write_status(state: str, detail: str = "", bot: boxbotapi.BotAPI | None = None) -> None:
    STATUS_PATH.parent.mkdir(parents=True, exist_ok=True)
    payload = {
        "state": state,
        "detail": detail,
        "updated_at": int(time.time()),
        "bot_user_id": bot.Self.UserId if bot else "",
        "bot_name": bot.Self.Name if bot else "",
    }
    temporary = STATUS_PATH.with_suffix(".tmp")
    temporary.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
    temporary.replace(STATUS_PATH)


def menu_markup(show_intro: bool = False) -> boxbotapi.InlineKeyboardMarkup:
    app_url = public_app_url()
    rows = []
    if show_intro:
        rows.append(
            boxbotapi.NewInlineKeyboardRow(
                boxbotapi.NewInlineKeyboardButtonData("介绍", "alert:intro"),
            )
        )
    rows.append(
        boxbotapi.NewInlineKeyboardRow(
            boxbotapi.NewInlineKeyboardButtonData("监控能力", "alert:features"),
            boxbotapi.NewInlineKeyboardButtonData("订阅方案", "alert:plans"),
        )
    )
    if app_url:
        rows.append(
            boxbotapi.NewInlineKeyboardRow(
                boxbotapi.NewInlineKeyboardButtonURL("打开监控面板", app_url),
            )
        )
    return boxbotapi.NewInlineKeyboardMarkup(*rows)


def group_entry_markup(bot: boxbotapi.BotAPI | None = None) -> boxbotapi.InlineKeyboardMarkup:
    buttons = []
    private_url = bot_private_chat_url(bot)
    app_url = public_app_url()
    if private_url:
        buttons.append(boxbotapi.NewInlineKeyboardButtonURL("私聊 Bot", private_url))
    if app_url:
        buttons.append(boxbotapi.NewInlineKeyboardButtonURL("个人监控面板", app_url))
    if not buttons:
        return boxbotapi.NewInlineKeyboardMarkup()
    return boxbotapi.NewInlineKeyboardMarkup(boxbotapi.NewInlineKeyboardRow(*buttons))


def menu_text() -> str:
    return (
        "<b>DeBox Asset Alert</b><br/>"
        "订阅钱包地址或代币资产变化，并通过 DeBox 接收实时通知。<br/><br/>"
        "当前支持：创建监控规则、链上余额查询、订阅支付和 Bot 通知。"
    )


def group_entry_text(message: boxbotapi.Message) -> str:
    user_name = ""
    if message.From is not None:
        user_name = message.From.Name or message.From.UserId
    if user_name:
        return (
            f"@{escape(user_name)} 我是 DeBox Asset Alert 链上监控助理，"
            "请私聊 Bot 或打开个人监控面板。"
        )
    return "我是 DeBox Asset Alert 链上监控助理，请私聊 Bot 或打开个人监控面板。"


def callback_text(data: str) -> str:
    if data == "alert:intro":
        return menu_text()
    if data == "alert:features":
        return (
            "<b>监控能力</b><br/>"
            "- 钱包原生币余额变化<br/>"
            "- ERC20 余额变化<br/>"
            "- 转入、转出与金额阈值<br/>"
            "- 私聊通知"
        )
    if data == "alert:plans":
        return (
            "<b>标准订阅</b><br/>"
            f"{settings.subscription_price} {settings.subscription_token_symbol} / "
            f"{settings.subscription_days} 天<br/>"
            "支付完成并通过链上验证后，订阅自动生效。"
        )
    return menu_text()


def callback_markup(data: str) -> boxbotapi.InlineKeyboardMarkup:
    return menu_markup(show_intro=data in {"alert:features", "alert:plans"})


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
        callback_text(query.Data),
        callback_markup(query.Data),
    )
    message.ParseMode = boxbotapi.ModeHTML
    bot.Send(message)


def handle_webhook_payload(payload: dict[str, Any]) -> dict:
    bot = boxbotapi.NewBotAPI(settings.debox_bot_api_key, settings.debox_bot_api_secret)

    update = boxbotapi.Update.from_dict(payload)
    if update is not None and (update.Message is not None or update.CallbackQuery is not None):
        if update.Message is not None:
            handle_message(bot, update.Message)
            return {"ok": True, "kind": "message", "update_id": update.Id}
        handle_callback(bot, update.CallbackQuery)
        return {"ok": True, "kind": "callback", "update_id": update.Id}

    callback_data = str(
        payload.get("callback_data")
        or payload.get("data")
        or payload.get("callback")
        or ""
    ).strip()
    message_id = str(payload.get("message_id", "") or "").strip()
    sender_id = str(payload.get("from_user_id", "") or "").strip()
    group_id = str(payload.get("group_id", "") or "").strip()
    if callback_data and message_id and (group_id or sender_id):
        chat_id = group_id or sender_id
        chat_type = "group" if group_id else "private"
        message = boxbotapi.NewEditMessageTextAndMarkup(
            chat_id,
            chat_type,
            message_id,
            callback_text(callback_data),
            callback_markup(callback_data),
        )
        message.ParseMode = boxbotapi.ModeHTML
        bot.Send(message)
        return {"ok": True, "kind": "flat_callback", "data": callback_data}

    text = str(payload.get("message", "") or "").strip()
    if not text or not sender_id:
        return {"ok": True, "kind": "ignored"}

    if text.lower() in {"/start", "start", "菜单", "menu"}:
        if group_id:
            pseudo_message = boxbotapi.Message(
                Chat=boxbotapi.Chat(ID=group_id, Type="group"),
                From=boxbotapi.User(
                    UserId=sender_id,
                    Name=str(payload.get("from_name", "") or payload.get("sender_name", "") or ""),
                ),
            )
            send_group_entry(bot, pseudo_message)
        else:
            send_menu(bot, sender_id, "private")
        return {"ok": True, "kind": "message", "command": text.lower()}

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
        raise RuntimeError(
            "Long Polling is disabled. Run the FastAPI service and configure /bot/webhook."
        )

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
                        logging.info(
                            "Received message update=%s chat=%s type=%s text=%r",
                            update.Id,
                            update.Message.Chat.ID if update.Message.Chat else "",
                            update.Message.Chat.Type if update.Message.Chat else "",
                            update.Message.Text,
                        )
                        handle_message(bot, update.Message)
                    elif update.CallbackQuery is not None:
                        logging.info(
                            "Received callback update=%s data=%r",
                            update.Id,
                            update.CallbackQuery.Data,
                        )
                        handle_callback(bot, update.CallbackQuery)
                except Exception:
                    logging.exception("Failed to handle update=%s", update.Id)
    except KeyboardInterrupt:
        write_status("stopped", "Stopped by user")
    except Exception as exc:
        write_status("error", str(exc))
        raise
