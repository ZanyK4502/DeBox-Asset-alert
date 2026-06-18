from decimal import Decimal

from app.chain_service import balance
from app.db import (
    create_alert_event,
    list_enabled_watch_rules,
    update_watch_rule_value,
)
from app.debox_service import send_notification


def notification_reason(rule_type: str, previous: Decimal, current: Decimal, threshold: Decimal) -> str | None:
    delta = current - previous
    if rule_type == "balance_change" and delta != 0 and abs(delta) >= threshold:
        return "余额变化"
    if rule_type == "incoming" and delta > 0 and delta >= threshold:
        return "检测到转入"
    if rule_type == "outgoing" and delta < 0 and abs(delta) >= threshold:
        return "检测到转出"
    if rule_type == "balance_threshold":
        if previous < threshold <= current:
            return "余额向上达到阈值"
        if previous >= threshold > current:
            return "余额向下跌破阈值"
    return None


def check_rule(rule: dict) -> dict:
    current = balance(
        rule["wallet_address"],
        rule["token_address"] or None,
        rule.get("chain_key") or "bsc",
    )
    current_value = current["value"]
    previous_value = rule["last_value"]

    if previous_value is None:
        update_watch_rule_value(rule["id"], current_value)
        return {"rule_id": rule["id"], "status": "baseline", "value": current_value}

    previous_decimal = Decimal(previous_value)
    current_decimal = Decimal(current_value)
    threshold = Decimal(rule["threshold"])
    reason = notification_reason(rule["rule_type"], previous_decimal, current_decimal, threshold)

    if reason is None:
        update_watch_rule_value(rule["id"], current_value)
        return {"rule_id": rule["id"], "status": "unchanged", "value": current_value}

    direction = "增加" if current_decimal > previous_decimal else "减少"
    change_amount = abs(current_decimal - previous_decimal)
    text = (
        f"<b>{reason}</b><br/>"
        f"网络：{current['chain_name']}<br/>"
        f"地址：{current['wallet_address']}<br/>"
        f"资产：{current['symbol']}<br/>"
        f"变化：{previous_value} -> {current_value}（{direction} {change_amount}）<br/>"
        f"规则阈值：{rule['threshold']}"
    )
    message_id = send_notification(
        rule["notification_chat_id"],
        rule["notification_chat_type"],
        text,
    )
    create_alert_event(
        rule["id"],
        rule["rule_type"],
        previous_value,
        current_value,
        message_id,
    )
    update_watch_rule_value(rule["id"], current_value)
    return {"rule_id": rule["id"], "status": "notified", "value": current_value}


def check_all_rules() -> list[dict]:
    results = []
    for rule in list_enabled_watch_rules():
        try:
            results.append(check_rule(rule))
        except Exception as exc:
            results.append({"rule_id": rule["id"], "status": "error", "error": str(exc)})
    return results
