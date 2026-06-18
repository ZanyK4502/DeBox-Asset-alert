from __future__ import annotations

from decimal import Decimal
import hmac

from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel
import uvicorn

from app.bot_service import (
    handle_webhook_payload,
    public_app_url,
    record_webhook_event,
    write_status,
)
from app.chain_service import balance, chain_profile, supported_chains
from app.config import ROOT_DIR, settings
from app.db import (
    create_notification_group,
    create_watch_rule,
    delete_notification_group,
    delete_watch_rule,
    expire_pending_orders,
    get_notification_group,
    initialize_database,
    list_notification_groups,
    list_user_watch_rules,
)
from app.openapi_service import group_info, is_group_joined, token_info, user_info
from app.payment_service import payment_configuration, prepare_payment, verify_payment
from app.plans import public_plans
from app.subscription_service import (
    entitlement,
    ensure_free_trial,
    require_group_slot,
    require_rule_creation,
)


STATIC_DIR = ROOT_DIR / "static"
app = FastAPI(title=settings.app_name)
app.mount("/static", StaticFiles(directory=STATIC_DIR), name="static")


class WatchRuleInput(BaseModel):
    chain_key: str = "bsc"
    wallet_address: str
    token_address: str | None = None
    rule_type: str = "balance_change"
    threshold: str = "0"
    debox_user_id: str
    notification_chat_id: str = ""
    notification_chat_type: str = "private"
    notification_label: str = ""


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
        raise HTTPException(status_code=409, detail="当前未启用 Webhook 接收模式")
    if not settings.debox_webhook_key:
        raise HTTPException(status_code=503, detail="尚未配置 DEBOX_WEBHOOK_KEY")
    if not x_api_key or not hmac.compare_digest(x_api_key, settings.debox_webhook_key):
        raise HTTPException(status_code=401, detail="Webhook 密钥不正确")

    try:
        payload = await request.json()
        if not isinstance(payload, dict):
            raise ValueError("Webhook 请求体必须是 JSON 对象")
        record_webhook_event(payload)
        result = handle_webhook_payload(payload)
        write_status("running", "Webhook 已接收并处理")
        return result
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    except Exception as exc:
        write_status("degraded", f"Webhook 处理失败: {exc}")
        raise HTTPException(status_code=500, detail="Webhook 处理失败") from exc


@app.get("/api/plans")
def get_plans() -> list[dict]:
    return public_plans()


@app.get("/api/chains")
def get_chains() -> list[dict]:
    return supported_chains()


@app.get("/api/subscription/current")
def current_subscription(debox_user_id: str) -> dict:
    if not debox_user_id:
        raise HTTPException(status_code=400, detail="缺少 debox_user_id")
    return entitlement(debox_user_id)


@app.post("/api/subscription/free-trial")
def start_free_trial(payload: FreeTrialInput) -> dict:
    subscription = ensure_free_trial(payload.debox_user_id)
    if subscription is None:
        active = entitlement(payload.debox_user_id, create_trial=False)
        if active["subscription"]:
            return active
        raise HTTPException(status_code=409, detail="免费体验已经使用过。")
    return entitlement(payload.debox_user_id, create_trial=False)


@app.get("/api/watch-rules")
def get_watch_rules(debox_user_id: str) -> dict:
    if not debox_user_id:
        raise HTTPException(status_code=400, detail="缺少 debox_user_id")
    return {"rules": list_user_watch_rules(debox_user_id)}


@app.post("/api/watch-rules")
def post_watch_rule(payload: WatchRuleInput) -> dict:
    try:
        validate_rule_input(payload.rule_type, payload.threshold)
        user_id = payload.debox_user_id.strip()
        if not user_id:
            raise ValueError("请先连接 DeBox 钱包。")
        chat_id, label = notification_target(payload)
        require_rule_creation(user_id, payload.notification_chat_type)
        current = balance(payload.wallet_address, payload.token_address, payload.chain_key)
        rule = create_watch_rule(
            debox_user_id=user_id,
            chain_key=current["chain_key"],
            chain_id=current["chain_id"],
            wallet_address=current["wallet_address"],
            token_address=current["token_address"],
            rule_type=payload.rule_type,
            threshold=payload.threshold,
            notification_chat_id=chat_id,
            notification_chat_type=payload.notification_chat_type,
            notification_label=label,
        )
        return {"rule": rule, "current_balance": current, "entitlement": entitlement(user_id)}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.delete("/api/watch-rules/{rule_id}")
def remove_watch_rule(rule_id: int, debox_user_id: str) -> dict:
    try:
        if not debox_user_id:
            raise ValueError("缺少 debox_user_id")
        delete_watch_rule(debox_user_id, rule_id)
        return {"ok": True, "entitlement": entitlement(debox_user_id)}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


def validate_rule_input(rule_type: str, threshold: str) -> None:
    allowed = {"balance_change", "incoming", "outgoing", "balance_threshold"}
    if rule_type not in allowed:
        raise ValueError("不支持的监控类型。")
    if Decimal(threshold) < 0:
        raise ValueError("金额阈值不能小于 0。")


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
def get_balance(
    address: str,
    token_address: str | None = None,
    chain_key: str = "bsc",
) -> dict:
    try:
        return balance(address, token_address, chain_key)
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.get("/api/debox/user")
def get_debox_user(user_id: str = "", wallet_address: str = "") -> dict:
    try:
        if not user_id and not wallet_address:
            raise ValueError("需要提供 user_id 或 wallet_address")
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
        raise HTTPException(status_code=400, detail="缺少 debox_user_id")
    return {"groups": list_notification_groups(debox_user_id)}


@app.post("/api/notification-groups")
def post_group(payload: GroupInput) -> dict:
    try:
        user_id = payload.debox_user_id.strip()
        gid = payload.gid.strip()
        if not user_id:
            raise ValueError("请先连接 DeBox 钱包。")
        if not gid:
            raise ValueError("请输入 DeBox 群 ID。")
        existing = get_notification_group(user_id, gid)
        if existing:
            return {"group": existing, "already_exists": True, "entitlement": entitlement(user_id)}
        require_group_slot(user_id)
        group = group_info(gid)
        if payload.wallet_address.strip():
            joined = is_group_joined(gid, payload.wallet_address.strip())
            if not group_joined(joined):
                raise ValueError("当前钱包似乎不是该群成员，请确认后再绑定。")
        saved = create_notification_group(user_id, gid, payload.label.strip() or group_name(group, gid))
        return {"group": saved, "source": group, "entitlement": entitlement(user_id)}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@app.delete("/api/notification-groups/{group_id}")
def delete_group(group_id: int, debox_user_id: str) -> dict:
    try:
        if not debox_user_id:
            raise ValueError("缺少 debox_user_id")
        delete_notification_group(debox_user_id, group_id)
        return {"ok": True, "entitlement": entitlement(debox_user_id)}
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
