from __future__ import annotations

from decimal import Decimal
from html import escape

from app.chain_service import balance
from app.db import (
    create_alert_event,
    list_due_scheduled_subscriptions,
    list_enabled_watch_rules,
    list_user_watch_rules,
    mark_scheduled_push_sent,
    update_watch_rule_value,
)
from app.debox_service import send_notification
from app.plans import get_plan


def notification_reason(
    rule_type: str,
    previous: Decimal,
    current: Decimal,
    threshold: Decimal,
) -> str | None:
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


def _rule_title(rule: dict, current: dict) -> str:
    label = (rule.get("notification_label") or "").strip()
    asset = current["symbol"]
    chain = current["chain_name"]
    return f"{label} {asset} / {chain}".strip() if label else f"{asset} / {chain}"


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
        f"<b>{escape(reason)}</b><br/>"
        f"监控：{escape(_rule_title(rule, current))}<br/>"
        f"地址：{escape(current['wallet_address'])}<br/>"
        f"变化：{escape(previous_value)} -> {escape(current_value)}"
        f"（{escape(direction)} {escape(str(change_amount))}）<br/>"
        f"阈值：{escape(rule['threshold'])}"
    )
    message_id = send_notification(rule["notification_chat_id"], rule["notification_chat_type"], text)
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


def scheduled_summary_text(subscription: dict, rules: list[dict]) -> str:
    plan = get_plan(subscription["plan_code"])
    enabled_rules = [rule for rule in rules if int(rule.get("enabled") or 0) == 1]
    lines = [
        "<b>DeBox Asset Alert 每日摘要</b>",
        f"套餐：{escape(plan['name'])}",
        f"有效期至：{escape(str(subscription['expires_at']))}",
        f"当前监控规则：{len(enabled_rules)} 条",
    ]
    for rule in enabled_rules[:8]:
        symbol = "原生资产" if not rule.get("token_address") else "代币"
        label = rule.get("notification_label") or rule.get("wallet_address")
        value = rule.get("last_value") or "未建立基线"
        lines.append(f"- {escape(str(label))}：{escape(str(value))} {escape(symbol)}")
    if len(enabled_rules) > 8:
        lines.append(f"...还有 {len(enabled_rules) - 8} 条规则")
    return "<br/>".join(lines)


def send_due_scheduled_reports(limit: int = 20) -> list[dict]:
    results = []
    for subscription in list_due_scheduled_subscriptions()[:limit]:
        try:
            plan = get_plan(subscription["plan_code"])
            if not plan["scheduled_push"]:
                continue
            rules = list_user_watch_rules(subscription["debox_user_id"])
            if not rules:
                mark_scheduled_push_sent(subscription["id"])
                results.append({"subscription_id": subscription["id"], "status": "skipped"})
                continue
            send_notification(subscription["debox_user_id"], "private", scheduled_summary_text(subscription, rules))
            mark_scheduled_push_sent(subscription["id"])
            results.append({"subscription_id": subscription["id"], "status": "sent"})
        except Exception as exc:
            results.append({"subscription_id": subscription["id"], "status": "error", "error": str(exc)})
    return results
