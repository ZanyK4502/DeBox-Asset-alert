from __future__ import annotations

from decimal import Decimal

from app.chain_service import (
    amount_to_units,
    chain_profile,
    encode_erc20_transfer,
    latest_block_number,
    rpc_transaction_by_hash,
    transaction_receipt,
    validate_address,
    validate_transaction_hash,
)
from app.config import settings
from app.db import (
    claim_order_transaction,
    create_order,
    expire_pending_orders,
    finalize_paid_order,
    get_active_subscription,
    list_confirming_orders,
    update_order_verification,
)
from app.plans import get_plan


PAYMENT_CHAIN_KEY = "bsc"
PAYMENT_CHAIN_ID = 56
BSC_USDT_ADDRESS = "0x55d398326f99059ff775485246999027b3197955"
REQUIRED_CONFIRMATIONS = 3


def payment_configuration(plan_code: str = "standard") -> dict:
    plan = get_plan(plan_code)
    profile = chain_profile(PAYMENT_CHAIN_KEY)
    missing = []
    recipient = ""
    token = ""
    try:
        recipient = validate_address(settings.payment_recipient_address)
    except ValueError:
        if settings.payment_mode == "live":
            missing.append("PAYMENT_RECIPIENT_ADDRESS")
    try:
        token = validate_address(settings.subscription_token_address)
    except ValueError:
        if settings.payment_mode == "live":
            missing.append("SUBSCRIPTION_TOKEN_ADDRESS")
    if settings.payment_mode == "live" and token and token != BSC_USDT_ADDRESS:
        missing.append("SUBSCRIPTION_TOKEN_ADDRESS must be BSC USDT")
    if settings.payment_mode == "live" and settings.subscription_token_decimals != 18:
        missing.append("SUBSCRIPTION_TOKEN_DECIMALS must be 18")

    return {
        "mode": settings.payment_mode,
        "plan": {**plan, "price": str(plan["price"])},
        "chain": profile,
        "chain_name": profile["name"],
        "chain_id": profile["chain_id"],
        "chain_id_hex": profile["chain_id_hex"],
        "asset": settings.subscription_token_symbol,
        "token_address": token or settings.subscription_token_address,
        "token_decimals": settings.subscription_token_decimals,
        "total_amount": str(plan["price"]),
        "recipient_address": recipient or settings.payment_recipient_address,
        "required_confirmations": REQUIRED_CONFIRMATIONS,
        "ready": not missing,
        "missing": missing,
    }


def _require_live_configuration() -> tuple[dict, str, str]:
    configuration = payment_configuration("standard")
    if settings.payment_mode != "live":
        raise ValueError("当前是预览模式，不会发起真实链上支付。")
    if not configuration["ready"]:
        raise ValueError(f"支付配置不完整：{', '.join(configuration['missing'])}")
    return (
        configuration["chain"],
        validate_address(configuration["recipient_address"]),
        validate_address(configuration["token_address"]),
    )


def prepare_payment(debox_user_id: str, payer_address: str, plan_code: str = "standard") -> dict:
    user_id = (debox_user_id or "").strip()
    if not user_id:
        raise ValueError("缺少 DeBox 用户身份。")
    plan = get_plan(plan_code)
    if plan["code"] == "free":
        raise ValueError("免费版无需支付。")

    profile, recipient, token = _require_live_configuration()
    payer = validate_address(payer_address)
    active = get_active_subscription(user_id)
    if active and active["plan_code"] not in {"free", plan["code"]}:
        raise ValueError("当前付费套餐未到期，只能续费同一套餐；到期后才能选择其他套餐。")

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
        "required_confirmations": REQUIRED_CONFIRMATIONS,
    }


def _hex_quantity(value: object, field_name: str) -> int:
    if isinstance(value, int):
        return value
    if isinstance(value, str) and value.startswith("0x"):
        try:
            return int(value, 16)
        except ValueError:
            pass
    raise ValueError(f"交易字段 {field_name} 无效。")


def _decode_erc20_transfer(data: object) -> tuple[str, int]:
    value = str(data or "").strip().lower()
    if len(value) != 138 or not value.startswith("0xa9059cbb"):
        raise ValueError("交易不是标准 ERC-20 Transfer。")
    recipient_word = value[10:74]
    amount_word = value[74:138]
    if recipient_word[:24] != "0" * 24:
        raise ValueError("交易收款地址编码无效。")
    try:
        recipient = validate_address(f"0x{recipient_word[-40:]}")
        amount = int(amount_word, 16)
    except ValueError as exc:
        raise ValueError("交易 Transfer 参数无效。") from exc
    return recipient, amount


def _confirming_result(order: dict, block_number: int | None, confirmations: int) -> dict:
    updated = update_order_verification(
        int(order["id"]),
        status="confirming",
        block_number=block_number,
        confirmations=confirmations,
    )
    return {
        "payment_status": "confirming",
        "order": updated,
        "confirmations": confirmations,
        "required_confirmations": REQUIRED_CONFIRMATIONS,
    }


def _failed_result(order: dict, message: str, block_number: int | None = None) -> dict:
    updated = update_order_verification(
        int(order["id"]),
        status="failed",
        block_number=block_number,
        confirmations=0,
        error=message,
    )
    return {
        "payment_status": "failed",
        "order": updated,
        "confirmations": 0,
        "required_confirmations": REQUIRED_CONFIRMATIONS,
        "error": message,
    }


def _verify_claimed_order(order: dict) -> dict:
    tx_hash = validate_transaction_hash(str(order.get("tx_hash") or ""))
    if order["chain_key"] != PAYMENT_CHAIN_KEY or int(order["chain_id"]) != PAYMENT_CHAIN_ID:
        return _failed_result(order, "订单支付网络不是 BNB Chain。")
    if validate_address(str(order.get("token_address") or "")) != BSC_USDT_ADDRESS:
        return _failed_result(order, "订单支付代币不是 BSC USDT。")

    transaction = rpc_transaction_by_hash(tx_hash, PAYMENT_CHAIN_KEY)
    receipt = transaction_receipt(tx_hash, PAYMENT_CHAIN_KEY)
    if transaction is None or receipt is None:
        return _confirming_result(order, None, 0)

    block_number = _hex_quantity(receipt.get("blockNumber"), "blockNumber")
    try:
        if validate_transaction_hash(str(transaction.get("hash") or "")) != tx_hash:
            return _failed_result(order, "链上交易哈希不匹配。", block_number)
        if validate_transaction_hash(str(receipt.get("transactionHash") or "")) != tx_hash:
            return _failed_result(order, "链上交易回执哈希不匹配。", block_number)
        transaction_block = _hex_quantity(transaction.get("blockNumber"), "blockNumber")
        if transaction_block != block_number:
            return _failed_result(order, "链上交易与回执区块不一致。", block_number)
        if validate_address(str(transaction.get("from") or "")) != validate_address(order["payer_address"]):
            return _failed_result(order, "实际付款钱包与订单钱包不一致。", block_number)
        if validate_address(str(transaction.get("to") or "")) != validate_address(order["token_address"]):
            return _failed_result(order, "交易未发送到指定 USDT 合约。", block_number)
        if _hex_quantity(transaction.get("value", "0x0"), "value") != 0:
            return _failed_result(order, "USDT 支付交易不应附带 BNB 金额。", block_number)
        recipient, amount_units = _decode_erc20_transfer(transaction.get("input"))
        if _hex_quantity(receipt.get("status"), "status") != 1:
            return _failed_result(order, "链上支付交易执行失败。", block_number)
    except ValueError as exc:
        return _failed_result(order, str(exc), block_number)

    expected_amount = amount_to_units(
        Decimal(str(order["total_amount"])),
        int(order["token_decimals"]),
    )
    if recipient != validate_address(order["recipient_address"]):
        return _failed_result(order, "链上 USDT 收款地址与订单不一致。", block_number)
    if amount_units != expected_amount:
        return _failed_result(order, "链上 USDT 支付金额与订单不一致。", block_number)
    confirmations = max(0, latest_block_number(PAYMENT_CHAIN_KEY) - block_number + 1)
    if confirmations < REQUIRED_CONFIRMATIONS:
        return _confirming_result(order, block_number, confirmations)

    plan = get_plan(order["plan_code"])
    paid_order, subscription = finalize_paid_order(
        int(order["id"]),
        tx_hash,
        block_number,
        confirmations,
        int(plan["days"]),
    )
    return {
        "payment_status": "paid",
        "order": paid_order,
        "subscription": subscription,
        "confirmations": confirmations,
        "required_confirmations": REQUIRED_CONFIRMATIONS,
    }


def verify_payment(
    order_id: int,
    tx_hash: str,
    debox_user_id: str,
    payer_address: str,
) -> dict:
    clean_hash = validate_transaction_hash(tx_hash)
    order = claim_order_transaction(
        order_id,
        (debox_user_id or "").strip(),
        validate_address(payer_address),
        clean_hash,
    )
    if order["status"] == "paid":
        return {
            "payment_status": "paid",
            "order": order,
            "subscription": None,
            "confirmations": int(order.get("tx_confirmations") or REQUIRED_CONFIRMATIONS),
            "required_confirmations": REQUIRED_CONFIRMATIONS,
        }
    return _verify_claimed_order(order)


def reconcile_confirming_payments(limit: int = 50) -> dict:
    expired = expire_pending_orders()
    results = []
    errors = []
    for order in list_confirming_orders(limit):
        try:
            results.append(_verify_claimed_order(order))
        except Exception as exc:
            update_order_verification(
                int(order["id"]),
                status="confirming",
                block_number=order.get("tx_block_number"),
                confirmations=int(order.get("tx_confirmations") or 0),
                error=str(exc),
            )
            errors.append({"order_id": order["id"], "error": str(exc)})
    return {
        "checked": len(results) + len(errors),
        "expired": expired,
        "paid": sum(1 for result in results if result["payment_status"] == "paid"),
        "confirming": sum(1 for result in results if result["payment_status"] == "confirming"),
        "failed": sum(1 for result in results if result["payment_status"] == "failed"),
        "errors": errors,
    }
