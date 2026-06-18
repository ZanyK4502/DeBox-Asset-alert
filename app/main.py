from pathlib import Path
import hmac
from decimal import Decimal

from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel
import uvicorn

from app.chain_service import balance, supported_chains
from app.bot_service import (
    handle_webhook_payload,
    public_app_url,
    record_webhook_event,
    write_status,
)
from app.config import ROOT_DIR, settings
from app.db import (
    create_watch_rule,
    expire_pending_orders,
    initialize_database,
)
from app.payment_service import payment_configuration, prepare_payment, verify_payment
from app.plans import public_plans
from app.subscription_service import (
    entitlement,
    ensure_free_trial,
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
    debox_user_id: str = ""
    notification_chat_id: str = ""
    notification_chat_type: str = "private"


class PreparePaymentInput(BaseModel):
    payer_address: str
    debox_user_id: str
    plan_code: str = "standard"


class VerifyPaymentInput(BaseModel):
    order_id: int
    tx_hash: str


@app.on_event("startup")
def startup() -> None:
    initialize_database()
    expire_pending_orders()
    if settings.debox_bot_receive_mode == "webhook":
        write_status(
            "ready" if settings.debox_webhook_key else "setup_required",
            "Webhook endpoint is ready"
            if settings.debox_webhook_key
            else "Set DEBOX_WEBHOOK_KEY after configuring BotMother",
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
        raise HTTPException(status_code=409, detail="Webhook receive mode is disabled")
    if not settings.debox_webhook_key:
        raise HTTPException(status_code=503, detail="DEBOX_WEBHOOK_KEY is not configured")
    if not x_api_key or not hmac.compare_digest(x_api_key, settings.debox_webhook_key):
        raise HTTPException(status_code=401, detail="Invalid webhook key")

    try:
        payload = await request.json()
        if not isinstance(payload, dict):
            raise ValueError("Webhook payload must be a JSON object")
        record_webhook_event(payload)
        result = handle_webhook_payload(payload)
        write_status("running", "Webhook received and processed")
        return result
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    except Exception as exc:
        write_status("degraded", f"Webhook processing error: {exc}")
        raise HTTPException(status_code=500, detail="Webhook processing failed") from exc


@app.get("/api/plans")
def get_plans() -> list[dict]:
    return public_plans()


@app.get("/api/chains")
def get_chains() -> list[dict]:
    return supported_chains()


@app.get("/api/subscription/current")
def current_subscription(debox_user_id: str = "") -> dict:
    return entitlement(debox_user_id or settings.debox_notification_chat_id)


@app.post("/api/subscription/free-trial")
def start_free_trial(debox_user_id: str = "") -> dict:
    user_id = debox_user_id or settings.debox_notification_chat_id
    subscription = ensure_free_trial(user_id)
    if subscription is None:
        active = entitlement(user_id, create_trial=False)
        if active["subscription"]:
            return active
        raise HTTPException(status_code=409, detail="Free trial has already been used")
    return entitlement(user_id, create_trial=False)


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


@app.post("/api/watch-rules")
def post_watch_rule(payload: WatchRuleInput) -> dict:
    try:
        validate_rule_input(payload.rule_type, payload.threshold)
        chat_id = notification_chat_id(payload.notification_chat_type, payload.notification_chat_id)
        user_id = payload.debox_user_id or settings.debox_notification_chat_id
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
        )
        return {"rule": rule, "current_balance": current}
    except Exception as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


def validate_rule_input(rule_type: str, threshold: str) -> None:
    allowed = {"balance_change", "incoming", "outgoing", "balance_threshold"}
    if rule_type not in allowed:
        raise ValueError("Unsupported watch rule type")
    if Decimal(threshold) < 0:
        raise ValueError("Threshold cannot be negative")


def notification_chat_id(chat_type: str, chat_id: str) -> str:
    if chat_type not in {"private", "group"}:
        raise ValueError("Notification target must be private or group")
    resolved = chat_id.strip() or settings.debox_notification_chat_id
    if not resolved:
        raise ValueError("Notification chat ID is required")
    if chat_type == "group" and not chat_id.strip():
        raise ValueError("Group ID is required for group notifications")
    return resolved


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
