from __future__ import annotations

from decimal import Decimal

from app.chain_service import (
    amount_to_units,
    encode_erc20_transfer,
    transaction_by_hash,
    validate_address,
)
from app.config import settings
from app.db import activate_subscription, complete_order, create_order, get_order
from app.plans import get_plan
from app.subscription_service import validate_plan_purchase


def payment_configuration(plan_code: str = "standard") -> dict:
    plan = get_plan(plan_code)
    missing = []
    if not settings.payment_recipient_address:
        missing.append("PAYMENT_RECIPIENT_ADDRESS")
    if settings.subscription_token_symbol != "BNB" and not settings.subscription_token_address:
        missing.append("SUBSCRIPTION_TOKEN_ADDRESS")
    if Decimal(plan["price"]) <= 0:
        missing.append("valid payment amount")

    return {
        "ready": not missing,
        "mode": settings.payment_mode,
        "payment_enabled": settings.payment_mode == "live",
        "plan_code": plan_code,
        "plan_name": plan["name"],
        "missing": missing,
        "chain_key": settings.chain_key,
        "chain_id": settings.chain_id,
        "chain_id_hex": hex(settings.chain_id),
        "chain_name": settings.chain_name,
        "asset": settings.subscription_token_symbol,
        "total_amount": str(plan["price"]),
        "days": plan["days"],
        "recipient_address": settings.payment_recipient_address,
        "token_address": settings.subscription_token_address or None,
    }


def _require_configuration(plan_code: str) -> dict:
    config = payment_configuration(plan_code)
    if not config["ready"]:
        raise ValueError(f"支付配置不完整：{', '.join(config['missing'])}")
    return config


def prepare_payment(payer_address: str, debox_user_id: str, plan_code: str) -> dict:
    if settings.payment_mode != "live":
        raise ValueError("当前是预览模式，不会发起真实链上支付。")

    validate_plan_purchase(debox_user_id, plan_code)
    config = _require_configuration(plan_code)
    total_amount = Decimal(config["total_amount"])
    payer = validate_address(payer_address)
    recipient = validate_address(config["recipient_address"])

    if config["token_address"]:
        token_address = validate_address(config["token_address"])
        total_units = amount_to_units(total_amount, settings.subscription_token_decimals)
        transaction = {
            "kind": "payment",
            "label": f"支付 {config['total_amount']} {config['asset']}",
            "request": {
                "from": payer,
                "to": token_address,
                "data": encode_erc20_transfer(recipient, total_units),
                "value": "0x0",
            },
        }
        payment_contract_address = token_address
    else:
        token_address = None
        total_units = amount_to_units(total_amount, 18)
        transaction = {
            "kind": "payment",
            "label": f"支付 {config['total_amount']} {config['asset']}",
            "request": {
                "from": payer,
                "to": recipient,
                "data": "0x",
                "value": hex(total_units),
            },
        }
        payment_contract_address = recipient

    order = create_order(
        debox_user_id=debox_user_id,
        payer_address=payer,
        token_address=token_address,
        recipient_address=recipient,
        payment_contract_address=payment_contract_address,
        total_amount=str(total_amount),
        plan_code=plan_code,
    )
    return {"order": order, "plan": config, "transactions": [transaction]}


def _field(data: dict, *names: str):
    for name in names:
        if name in data and data[name] is not None:
            return data[name]
    return None


def _decode_transfer_input(data: str) -> tuple[str, int]:
    value = (data or "").lower()
    if not value.startswith("0xa9059cbb") or len(value) < 138:
        raise ValueError("支付交易方法不匹配。")
    recipient = "0x" + value[34:74]
    amount = int(value[74:138], 16)
    return validate_address(recipient), amount


def verify_payment(order_id: int, tx_hash: str) -> dict:
    order = get_order(order_id)
    if not order:
        raise ValueError("订单不存在。")
    if order["status"] == "paid":
        return {"order": order, "already_verified": True}

    transaction = transaction_by_hash(tx_hash, settings.chain_key)
    if transaction.get("success") is False or transaction.get("status") in {0, "0", "failed"}:
        raise ValueError("链上交易失败。")

    payer = _field(transaction, "from", "fromAddress", "sender", "senderAddress")
    if not payer:
        raise ValueError("Nodit 返回数据中没有交易发起方。")
    if validate_address(payer) != validate_address(order["payer_address"]):
        raise ValueError("付款地址与订单不一致。")

    token_address = order["token_address"]
    recipient = validate_address(order["recipient_address"])
    tx_to = _field(transaction, "to", "toAddress", "recipient", "recipientAddress")
    tx_input = _field(transaction, "input", "data", "inputData") or "0x"

    if token_address:
        token_address = validate_address(token_address)
        if not tx_to or validate_address(tx_to) != token_address:
            raise ValueError("支付代币合约与订单不一致。")
        expected_total = amount_to_units(
            Decimal(order["total_amount"]),
            settings.subscription_token_decimals,
        )
        decoded_recipient, decoded_amount = _decode_transfer_input(tx_input)
        if decoded_recipient != recipient:
            raise ValueError("收款地址与订单不一致。")
        if decoded_amount != expected_total:
            raise ValueError("支付金额与订单不一致。")
    else:
        expected_total = amount_to_units(Decimal(order["total_amount"]), 18)
        tx_value = int(str(_field(transaction, "value", "amount", "nativeValue") or "0"), 0)
        if not tx_to or validate_address(tx_to) != recipient:
            raise ValueError("收款地址与订单不一致。")
        if tx_value != expected_total:
            raise ValueError("支付金额与订单不一致。")

    paid_order = complete_order(order_id, tx_hash)
    subscription = activate_subscription(
        paid_order["debox_user_id"],
        paid_order["plan_code"],
        get_plan(paid_order["plan_code"])["days"],
    )
    return {"order": paid_order, "subscription": subscription, "already_verified": False}
