from __future__ import annotations

from decimal import Decimal

from app.chain_service import ERC20_ABI, validate_address, web3
from app.config import settings
from app.db import activate_subscription, complete_order, create_order, get_order
from app.plans import get_plan


TRANSFER_ABI = [
    *ERC20_ABI,
    {
        "type": "function",
        "name": "transfer",
        "inputs": [
            {"name": "recipient", "type": "address"},
            {"name": "amount", "type": "uint256"},
        ],
        "outputs": [{"name": "", "type": "bool"}],
        "stateMutability": "nonpayable",
    },
]


def payment_configuration(plan_code: str = "standard") -> dict:
    plan = get_plan(plan_code)
    missing = []
    if not settings.payment_recipient_address:
        missing.append("PAYMENT_RECIPIENT_ADDRESS")
    if settings.subscription_token_symbol != "BNB" and not settings.subscription_token_address:
        missing.append("SUBSCRIPTION_TOKEN_ADDRESS")
    if plan["price"] <= 0:
        missing.append("valid payment amount")
    return {
        "ready": not missing,
        "mode": settings.payment_mode,
        "payment_enabled": settings.payment_mode == "live",
        "plan_code": plan_code,
        "plan_name": plan["name"],
        "missing": missing,
        "chain_id": settings.chain_id,
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
        raise ValueError(f"Payment configuration incomplete: {', '.join(config['missing'])}")
    return config


def _units(value: Decimal, decimals: int) -> int:
    units = value * (Decimal(10) ** decimals)
    if units != units.to_integral_value():
        raise ValueError("Payment amount has too many decimal places")
    return int(units)


def prepare_payment(payer_address: str, debox_user_id: str, plan_code: str) -> dict:
    if settings.payment_mode != "live":
        raise ValueError(
            "Payment is in preview mode. Real on-chain payment is disabled."
        )
    config = _require_configuration(plan_code)
    total_amount = Decimal(config["total_amount"])
    client = web3()
    payer = validate_address(payer_address)
    recipient = validate_address(config["recipient_address"])

    if config["token_address"]:
        token_address = validate_address(config["token_address"])
        token = client.eth.contract(address=token_address, abi=TRANSFER_ABI)
        decimals = int(token.functions.decimals().call())
        total_units = _units(total_amount, decimals)
        transaction = {
            "kind": "payment",
            "label": f"支付 {config['asset']}",
            "request": {
                "from": payer,
                "to": token_address,
                "data": token.functions.transfer(
                    recipient, total_units
                )._encode_transaction_data(),
                "value": "0x0",
            },
        }
        payment_contract_address = token_address
    else:
        token_address = None
        total_units = _units(total_amount, 18)
        transaction = {
            "kind": "payment",
            "label": f"支付 {config['asset']}",
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


def verify_payment(order_id: int, tx_hash: str) -> dict:
    order = get_order(order_id)
    if not order:
        raise ValueError("Order not found")
    if order["status"] == "paid":
        return {"order": order, "already_verified": True}

    client = web3()
    receipt = client.eth.wait_for_transaction_receipt(
        tx_hash, timeout=120, poll_latency=2
    )
    transaction = client.eth.get_transaction(tx_hash)
    if receipt["status"] != 1:
        raise ValueError("Transaction failed")
    if validate_address(transaction["from"]) != validate_address(order["payer_address"]):
        raise ValueError("Transaction payer does not match order")

    token_address = order["token_address"]
    recipient = validate_address(order["recipient_address"])
    if token_address:
        token_address = validate_address(token_address)
        if validate_address(transaction["to"]) != token_address:
            raise ValueError("Payment token does not match order")
        token = client.eth.contract(address=token_address, abi=TRANSFER_ABI)
        decimals = int(token.functions.decimals().call())
        expected_total = _units(Decimal(order["total_amount"]), decimals)
        function, params = token.decode_function_input(transaction["input"])
        if function.fn_name != "transfer":
            raise ValueError("Unexpected payment method")
        if validate_address(params["recipient"]) != recipient:
            raise ValueError("Payment recipient does not match order")
        if params["amount"] != expected_total:
            raise ValueError("Payment amount does not match order")
    else:
        expected_total = _units(Decimal(order["total_amount"]), 18)
        if validate_address(transaction["to"]) != recipient:
            raise ValueError("Payment recipient does not match order")
        if transaction["value"] != expected_total:
            raise ValueError("Payment amount does not match order")

    paid_order = complete_order(order_id, tx_hash)
    subscription = activate_subscription(
        paid_order["debox_user_id"],
        paid_order["plan_code"],
        get_plan(paid_order["plan_code"])["days"],
    )
    return {"order": paid_order, "subscription": subscription, "already_verified": False}
