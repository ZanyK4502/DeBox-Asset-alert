from __future__ import annotations

from decimal import Decimal

from app.chain_service import (
    amount_to_units,
    chain_profile,
    encode_erc20_transfer,
    transaction_by_hash,
    validate_address,
    validate_transaction_hash,
)
from app.config import settings
from app.db import complete_order, create_order, get_order
from app.plans import get_plan
from app.subscription_service import activate_paid_subscription


def payment_configuration(plan_code: str = "standard") -> dict:
    plan = get_plan(plan_code)
    profile = chain_profile(settings.chain_key)
    missing = []
    if settings.payment_mode == "live" and not settings.payment_recipient_address:
        missing.append("PAYMENT_RECIPIENT_ADDRESS")
    if settings.payment_mode == "live" and not settings.subscription_token_address:
        missing.append("SUBSCRIPTION_TOKEN_ADDRESS")

    return {
        "mode": settings.payment_mode,
        "plan": {**plan, "price": str(plan["price"])},
        "chain": profile,
        "chain_name": profile["name"],
        "chain_id": profile["chain_id"],
        "chain_id_hex": profile["chain_id_hex"],
        "asset": settings.subscription_token_symbol,
        "token_address": settings.subscription_token_address,
        "token_decimals": settings.subscription_token_decimals,
        "total_amount": str(plan["price"]),
        "recipient_address": settings.payment_recipient_address,
        "ready": not missing,
        "missing": missing,
    }


def prepare_payment(payer_address: str, debox_user_id: str, plan_code: str = "standard") -> dict:
    user_id = (debox_user_id or "").strip()
    if not user_id:
        raise ValueError("缺少 DeBox 用户 ID。")
    if plan_code == "free":
        raise ValueError("免费体验无需支付。")
    if settings.payment_mode != "live":
        raise ValueError("当前是预览模式，不会发起真实链上支付。")
    if not settings.payment_recipient_address:
        raise ValueError("尚未配置收款地址 PAYMENT_RECIPIENT_ADDRESS。")
    if not settings.subscription_token_address:
        raise ValueError("尚未配置订阅代币 SUBSCRIPTION_TOKEN_ADDRESS。")

    payer = validate_address(payer_address)
    recipient = validate_address(settings.payment_recipient_address)
    token = validate_address(settings.subscription_token_address)
    plan = get_plan(plan_code)
    profile = chain_profile(settings.chain_key)
    amount_units = amount_to_units(Decimal(str(plan["price"])), settings.subscription_token_decimals)

    order = create_order(
        debox_user_id=user_id,
        payer_address=payer,
        plan_code=plan["code"],
        chain_key=profile["key"],
        chain_id=profile["chain_id"],
        token_address=token,
        token_symbol=settings.subscription_token_symbol,
        token_decimals=settings.subscription_token_decimals,
        total_amount=Decimal(str(plan["price"])),
        recipient_address=recipient,
    )
    request = {
        "from": payer,
        "to": token,
        "data": encode_erc20_transfer(recipient, amount_units),
        "value": "0x0",
        "chainId": profile["chain_id_hex"],
    }
    return {
        "order": order,
        "chain": profile,
        "transaction": request,
        "transactions": [{"request": request}],
        "amount_units": str(amount_units),
        "amount": str(plan["price"]),
        "symbol": settings.subscription_token_symbol,
        "recipient": recipient,
    }


def _find_matching_transfer(tx: dict, order: dict) -> bool:
    text = str(tx).lower()
    required = [
        str(order["payer_address"]).lower(),
        str(order["recipient_address"]).lower(),
        str(order.get("token_address") or "").lower(),
    ]
    return all(item in text for item in required if item)


def verify_payment(order_id: int, tx_hash: str) -> dict:
    order = get_order(order_id)
    if not order:
        raise ValueError("订单不存在。")
    if order["status"] == "paid":
        return {"order": order, "subscription": None}
    if order["status"] != "pending":
        raise ValueError("订单不是待支付状态。")

    clean_hash = validate_transaction_hash(tx_hash)
    tx = transaction_by_hash(clean_hash, order["chain_key"])
    if not _find_matching_transfer(tx, order):
        raise ValueError("没有在交易中识别到匹配的付款记录，请确认交易哈希、付款地址和收款地址。")

    paid_order = complete_order(order_id, clean_hash)
    subscription = activate_paid_subscription(order["debox_user_id"], order["plan_code"])
    return {"order": paid_order, "subscription": subscription}
