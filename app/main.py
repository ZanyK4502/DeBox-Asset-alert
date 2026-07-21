from __future__ import annotations

from decimal import Decimal
import hmac
import re
from urllib.parse import urlparse, parse_qs

from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel
import uvicorn

from app.bot_service import handle_webhook_payload, public_app_url, record_webhook_event, write_status
from app.chain_service import (
    balance,
    chain_profile,
    latest_interaction,
    supported_chains,
    token_allowance,
)
from app.config import ROOT_DIR, settings
from app.db import (
    create_notification_group,
    create_watch_rule,
    delete_paused_watch_rules,
    delete_notification_group,
    delete_watch_rule,
    expire_pending_orders,
    get_notification_group,
    initialize_database,
    list_notification_groups,
    list_user_watch_rules,
    update_daily_summary_settings,
    update_watch_rule_notification_language,
)
from app.languages import require_language
from app.openapi_service import group_info, is_group_joined, token_info, user_info
from app.payment_service import payment_configuration, prepare_payment, verify_payment
from app.plans import ALL_RULE_TYPES, public_plans, public_rule_types
from app.subscription_service import (
    choose_free_watch_rule,
    entitlement,
    enable_free_plan,
    require_group_slot,
    require_rule_creation,
    require_summary_target,
    restore_paused_watch_rule,
)


STATIC_DIR = ROOT_DIR / "static"
TIME_RE = re.compile(r"^\d{2}:\d{2}$")
ALLOWED_SUMMARY_TIMEZONES = {
    "Asia/Shanghai",
    "Asia/Tokyo",
    "Asia/Bangkok",
    "Asia/Kolkata",
    "Europe/Berlin",
    "Europe/London",
    "America/New_York",
    "America/Los_Angeles",
    "UTC",
}


def parse_debox_group_link(value: str) -> str:
    parsed = urlparse((value or "").strip())
    host = parsed.hostname or ""
    if host.lower() not in {"m.debox.pro", "www.debox.pro", "debox.pro"}:
        return ""
    if parsed.path != "/group":
        return ""
    ids = parse_qs(parsed.query).get("id") or []
    return (ids[0] if ids else "").strip()

app = FastAPI(title=settings.app_name)
app.mount("/static", StaticFiles(directory=STATIC_DIR), name="static")


class WatchRuleInput(BaseModel):
    chain_key: str = "bsc"
    wallet_address: str
    token_address: str | None = None
    target_address: str | None = None
    target_label: str = ""
    rule_type: str = "balance_change"
    threshold: str = "0"
    debox_user_id: str
    notification_chat_id: str = ""
    notification_chat_type: str = "private"
    notification_label: str = ""
    notification_language: str = "zh"


class GroupInput(BaseModel):
    debox_user_id: str
    gid: str
    label: str = ""
    wallet_address: str = ""


class PreparePaymentInput(BaseModel):
    payer_address: str
    debox_user_id: str
    plan_code: str = "standard"


class VerifyPaymentInput(BaseModel):
    order_id: int
    tx_hash: str


class FreeTrialInput(BaseModel):
    debox_user_id: str


class FreeWatchRuleInput(BaseModel):
    debox_user_id: str


class RestoreWatchRuleInput(BaseModel):
    debox_user_id: str


class RuleLanguageInput(BaseModel):
    debox_user_id: str
    language: str


class SummarySettingsInput(BaseModel):
    debox_user_id: str
    enabled: bool = True
    push_time: str = "20:00"
    timezone: str = "Asia/Shanghai"
    chat_type: str = "private"
    chat_id: str = ""
    label: str = ""
    language: str = "zh"


@app.on_event("startup")
def startup() -> None:
    initialize_database()
    expire_pending_orders()
    if settings.debox_bot_receive_mode == "webhook":
        write_status(
            "ready" if settings.debox_webhook_key else "setup_required",
            "Webhook 接收模式已就绪"
            if settings.debox_webhook_key
            else "请先在 BotMother 配置 Webhook，并设置 DEBOX_WEBHOOK_KEY",
        )


@app.get("/")
def index() -> FileResponse:
    return FileResponse(STATIC_DIR / "index.html")


@app.get("/api/health")
def health() -> dict:
    return {
        "ok": True,
        "app": settings.app_name,
        "environment": settings.app_env,
        "receive_mode": settings.debox_bot_receive_mode,
    }


@app.get("/api/bot/webhook-status")
def webhook_status() -> dict:
    base_url = public_app_url()
    return {
        "mode": settings.debox_bot_receive_mode,
        "configured": bool(settings.debox_webhook_key),
        "webhook_url": f"{base_url}/bot/webhook" if base_url else "/bot/webhook",
    }


@app.post("/bot/webhook")
async def bot_webhook(
    request: Request,
    x_api_key: str | None = Header(default=None, alias="X-API-KEY"),
) -> dict:
    if settings.debox_bot_receive_mode != "webhook":
        raise HTTPException(status_code=409, detail="当前未启用 Webhook 接收模式。")
    if not settings.debox_webhook_key:
        raise HTTPException(status_code=503, detail="尚未配置 DEBOX_WEBHOOK_KEY。")
    if not x_api_key or not hmac.compare_digest(x_api_key, settings.debox_webhook_key):
        raise HTTPException(status_code=401, detail="Webhook 密钥不正确。")

    try:
        payload = await request.json()
        if not isinstance(payload, dict):
            raise ValueError("Webhook 请求体必须是 JSON 对象。")
        record_webhook_event(payload)
        result = handle_webhook_payload(payload)
        write_status("running", "Webhook 已接收并处理。")
        return result
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    except Exception as exc:
        write_status("degraded", f"Webhook 处理失败：{exc}")
        raise HTTPException(status_code=500, detail="Webhook 处理失败。") from exc


@app.get("/api/plans")
def get_plans() -> dict:
    return {"plans": public_plans(), "rule_types": public_rule_types()}


@app.get("/api/chains")
def get_chains() -> list[dict]:
    return supported_chains()


@app.get("/api/subscription/current")
def current_subscription(debox_user_id: str) -> dict:
    if not debox_user_id:
        raise HTTPException(status_code=400, detail="缺少 debox_user_id。")
    return entitlement(debox_user_id)


@app.post("/api/subscription/free-trial")
def enable_free_plan_endpoint(payload: FreeTrialInput) -> dict:
    enable_free_plan(payload.debox_user_id)
    return entitlement(payload.debox_user_id, create_trial=False)


@app.post("/api/subscription/summary-settings")
def save_summary_settings(payload: SummarySettingsInput) -> dict:
    try:
        user_id = payload.debox_user_id.strip()
        if not user_id:
            raise ValueError("请先连接 DeBox 钱包。")
        chat_type = payload.chat_type.strip().lower()
        if chat_type not in {"private", "group"}:
            raise ValueError("每日摘要推送对象只能是私聊或群聊。")
        require_summary_target(user_id, chat_type)
        language = require_language(payload.language)
        validate_push_time(payload.push_time)
        timezone_name = validate_summary_timezone(payload.timezone)
        chat_id = user_id if chat_type == "private" else payload.chat_id.strip()
        if chat_type == "group" and not get_notification_group(user_id, chat_id):
            raise ValueError("请先绑定这个群，再设置群每日摘要。")
        subscription = update_daily_summary_settings(
            debox_user_id=user_id,
            enabled=payload.enabled,
            push_time=payload.push_time,
            timezone_name=timezone_name,
            chat_type=chat_type,
            chat_id=chat_id,
            label=payload.label.strip() or ("私聊摘要" if chat_type == "private" else chat_id),
            language=language,
        )
        return {"subscription": subscription, "entitlement": entitlement(user_id, create_trial=False)}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


def validate_push_time(value: str) -> None:
    if not TIME_RE.fullmatch(value):
        raise ValueError("推送时间格式应为 HH:MM。")
    hour, minute = [int(part) for part in value.split(":", 1)]
    if hour > 23 or minute > 59:
        raise ValueError("推送时间必须在 00:00 到 23:59 之间。")


def validate_summary_timezone(value: str) -> str:
    timezone_name = (value or "Asia/Shanghai").strip()
    if timezone_name not in ALLOWED_SUMMARY_TIMEZONES:
        raise ValueError("每日摘要时区不在支持范围内。")
    return timezone_name


@app.get("/api/watch-rules")
def get_watch_rules(debox_user_id: str) -> dict:
    if not debox_user_id:
        raise HTTPException(status_code=400, detail="缺少 debox_user_id。")
    return {"rules": list_user_watch_rules(debox_user_id)}


@app.post("/api/watch-rules")
def post_watch_rule(payload: WatchRuleInput) -> dict:
    try:
        validate_rule_input(payload)
        user_id = payload.debox_user_id.strip()
        if not user_id:
            raise ValueError("请先连接 DeBox 钱包。")

        profile = chain_profile(payload.chain_key)
        wallet_address = payload.wallet_address.strip()
        plan = require_rule_creation(user_id, payload.notification_chat_type.strip().lower(), wallet_address, payload.rule_type)
        chat_id, label = notification_target(payload)

        token_address = (payload.token_address or "").strip() or None
        target_address = (payload.target_address or "").strip() or None
        if payload.rule_type == "approval_change":
            baseline = token_allowance(wallet_address, token_address or "", target_address or "", profile["key"])
            last_value = baseline["value"]
            baseline_payload = baseline
        elif payload.rule_type == "address_interaction":
            baseline = latest_interaction(wallet_address, target_address or "", profile["key"])
            last_value = baseline["cursor"]
            baseline_payload = baseline
        else:
            baseline = balance(wallet_address, token_address, profile["key"])
            wallet_address = baseline["wallet_address"]
            token_address = baseline["token_address"]
            last_value = baseline["value"]
            baseline_payload = baseline

        rule = create_watch_rule(
            debox_user_id=user_id,
            chain_key=profile["key"],
            chain_id=profile["chain_id"],
            wallet_address=wallet_address,
            token_address=token_address,
            target_address=target_address,
            target_label=payload.target_label.strip(),
            rule_type=payload.rule_type,
            threshold=Decimal(payload.threshold),
            notification_chat_id=chat_id,
            notification_chat_type=payload.notification_chat_type.strip().lower(),
            notification_label=label,
            notification_language=require_language(payload.notification_language),
            last_value=last_value,
        )
        current_entitlement = (
            choose_free_watch_rule(user_id, int(rule["id"]))
            if plan["code"] == "free"
            else entitlement(user_id, create_trial=False)
        )
        return {"rule": rule, "baseline": baseline_payload, "entitlement": current_entitlement}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.delete("/api/watch-rules/paused")
def remove_paused_watch_rules(debox_user_id: str) -> dict:
    try:
        if not debox_user_id:
            raise ValueError("缺少 debox_user_id。")
        deleted = delete_paused_watch_rules(debox_user_id)
        return {"ok": True, "deleted": deleted, "entitlement": entitlement(debox_user_id, create_trial=False)}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.delete("/api/watch-rules/{rule_id}")
def remove_watch_rule(rule_id: int, debox_user_id: str) -> dict:
    try:
        if not debox_user_id:
            raise ValueError("缺少 debox_user_id。")
        delete_watch_rule(rule_id, debox_user_id)
        return {"ok": True, "entitlement": entitlement(debox_user_id, create_trial=False)}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.post("/api/watch-rules/{rule_id}/free-monitor")
def set_free_monitor_rule(rule_id: int, payload: FreeWatchRuleInput) -> dict:
    try:
        if not payload.debox_user_id:
            raise ValueError("缺少 debox_user_id。")
        return choose_free_watch_rule(payload.debox_user_id, rule_id)
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.post("/api/watch-rules/{rule_id}/restore")
def restore_monitor_rule(rule_id: int, payload: RestoreWatchRuleInput) -> dict:
    try:
        if not payload.debox_user_id:
            raise ValueError("缺少 debox_user_id。")
        return restore_paused_watch_rule(payload.debox_user_id, rule_id)
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.patch("/api/watch-rules/{rule_id}/notification-language")
def change_rule_notification_language(rule_id: int, payload: RuleLanguageInput) -> dict:
    try:
        user_id = payload.debox_user_id.strip()
        if not user_id:
            raise ValueError("缺少 debox_user_id。")
        rule = update_watch_rule_notification_language(
            rule_id,
            user_id,
            require_language(payload.language),
        )
        return {
            "rule": rule,
            "entitlement": entitlement(user_id, create_trial=False),
        }
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


def validate_rule_input(payload: WatchRuleInput) -> None:
    require_language(payload.notification_language)
    if payload.rule_type not in ALL_RULE_TYPES:
        raise ValueError("不支持的监控类型。")
    if Decimal(payload.threshold) < 0:
        raise ValueError("金额阈值不能小于 0。")
    if payload.rule_type == "approval_change":
        if not payload.token_address or not payload.target_address:
            raise ValueError("授权监控需要填写代币合约和授权对象地址。")
    if payload.rule_type == "address_interaction" and not payload.target_address:
        raise ValueError("指定地址交互提醒需要填写目标地址或合约。")


def notification_target(payload: WatchRuleInput) -> tuple[str, str]:
    chat_type = payload.notification_chat_type.strip().lower()
    if chat_type not in {"private", "group"}:
        raise ValueError("通知目标只能是 private 或 group。")
    if chat_type == "private":
        return payload.debox_user_id.strip(), "私聊通知"

    gid = payload.notification_chat_id.strip()
    if not gid:
        raise ValueError("专业版群通知需要选择一个已绑定的群。")
    group = get_notification_group(payload.debox_user_id, gid)
    if group is None:
        raise ValueError("这个群还没有绑定，请先在群通知设置中添加。")
    return gid, payload.notification_label.strip() or group.get("name") or gid


@app.get("/api/chain/balance")
def get_balance(address: str, token_address: str | None = None, chain_key: str = "bsc") -> dict:
    try:
        return balance(address, token_address, chain_key)
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.get("/api/debox/user")
def get_debox_user(user_id: str = "", wallet_address: str = "") -> dict:
    try:
        if not user_id and not wallet_address:
            raise ValueError("需要提供 user_id 或 wallet_address。")
        return user_info(user_id=user_id, wallet_address=wallet_address)
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.get("/api/debox/token")
def get_debox_token(contract_address: str, chain_key: str = "bsc") -> dict:
    try:
        profile = chain_profile(chain_key)
        return token_info(contract_address, profile["chain_id"])
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.get("/api/notification-groups")
def get_groups(debox_user_id: str) -> dict:
    if not debox_user_id:
        raise HTTPException(status_code=400, detail="缺少 debox_user_id。")
    return {"groups": list_notification_groups(debox_user_id)}


@app.post("/api/notification-groups")
def post_group(payload: GroupInput) -> dict:
    try:
        user_id = payload.debox_user_id.strip()
        gid = parse_debox_group_link(payload.gid)
        if not user_id:
            raise ValueError("请先连接 DeBox 钱包。")
        if not gid:
            raise ValueError("请输入正确的 DeBox 群链接。")
        existing = get_notification_group(user_id, gid)
        if existing:
            return {"group": existing, "already_exists": True, "entitlement": entitlement(user_id, create_trial=False)}
        require_group_slot(user_id)
        group = group_info(gid)
        if payload.wallet_address.strip():
            joined = is_group_joined(gid, payload.wallet_address.strip())
            if not group_joined(joined):
                raise ValueError("当前钱包似乎不是该群成员，请确认后再绑定。")
        saved = create_notification_group(user_id, gid, payload.label.strip() or group_name(group, gid))
        return {"group": saved, "source": group, "entitlement": entitlement(user_id, create_trial=False)}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.delete("/api/notification-groups/{group_id}")
def delete_group(group_id: int, debox_user_id: str) -> dict:
    try:
        if not debox_user_id:
            raise ValueError("缺少 debox_user_id。")
        delete_notification_group(group_id, debox_user_id)
        return {"ok": True, "entitlement": entitlement(debox_user_id, create_trial=False)}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


def group_name(group: dict, fallback: str) -> str:
    for key in ("group_name", "name", "title", "nickname"):
        value = group.get(key)
        if value:
            return str(value)
    data = group.get("data")
    if isinstance(data, dict):
        return group_name(data, fallback)
    return fallback


def group_joined(payload: dict) -> bool:
    for key in ("is_join", "isJoin", "joined", "success", "data"):
        value = payload.get(key)
        if isinstance(value, bool):
            return value
        if isinstance(value, dict):
            return group_joined(value)
        if isinstance(value, (str, int)):
            return str(value).lower() in {"1", "true", "yes"}
    return False


@app.get("/api/payment/config")
def get_payment_config(plan_code: str = "standard") -> dict:
    return payment_configuration(plan_code)


@app.post("/api/payment/prepare")
def post_prepare_payment(payload: PreparePaymentInput) -> dict:
    try:
        return prepare_payment(payload.payer_address, payload.debox_user_id, payload.plan_code)
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.post("/api/payment/verify")
def post_verify_payment(payload: VerifyPaymentInput) -> dict:
    try:
        return verify_payment(payload.order_id, payload.tx_hash)
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


def run() -> None:
    uvicorn.run(
        "app.main:app",
        host=settings.app_host,
        port=settings.app_port,
        reload=settings.app_env == "development",
    )
