from __future__ import annotations

from collections import Counter
from datetime import datetime, timezone
from decimal import Decimal
from html import escape
from zoneinfo import ZoneInfo

from app.chain_service import balance, latest_interaction, token_allowance
from app.db import (
    count_daily_alert_events,
    create_alert_event,
    list_due_scheduled_subscriptions,
    list_enabled_watch_rules,
    list_recent_alert_events,
    list_user_watch_rules,
    mark_scheduled_push_sent,
    update_watch_rule_value,
)
from app.debox_service import send_notification
from app.plans import ASSET_RULE_TYPES, RULE_TYPE_LABELS, get_plan


FREE_ALERT_TIMEZONE = "Asia/Shanghai"


def _decimal(value: str | None) -> Decimal:
    try:
        return Decimal(str(value or "0"))
    except Exception:
        return Decimal("0")


def _short(address: str | None) -> str:
    if not address:
        return "-"
    value = str(address)
    return f"{value[:8]}...{value[-6:]}" if len(value) > 16 else value


def _send_rule_alert(rule: dict, previous: str, current: str, note: str) -> str:
    text = (
        "<b>DeBox Asset Alert</b><br/>"
        f"规则：{escape(RULE_TYPE_LABELS.get(rule['rule_type'], rule['rule_type']))}<br/>"
        f"网络：{escape(str(rule.get('chain_key', '-')))}<br/>"
        f"钱包：{escape(_short(rule.get('wallet_address')))}<br/>"
        f"变化：{escape(previous)} -> {escape(current)}<br/>"
        f"{escape(note)}"
    )
    return send_notification(rule["notification_chat_id"], rule["notification_chat_type"], text)


def _plan_for_rule(rule: dict) -> dict:
    return get_plan(rule.get("effective_plan_code") or "free")


def _rule_allowed_by_plan(rule: dict, plan: dict) -> dict | None:
    if rule["rule_type"] not in plan["allowed_rule_types"]:
        return {
            "rule_id": rule["id"],
            "status": "plan_limited",
            "reason": "rule_type",
            "plan": plan["code"],
        }
    if rule.get("notification_chat_type") == "group" and not plan["group_notification"]:
        return {
            "rule_id": rule["id"],
            "status": "plan_limited",
            "reason": "group_notification",
            "plan": plan["code"],
        }
    return None


def _free_daily_limit_result(rule: dict, plan: dict) -> dict | None:
    if plan["code"] != "free":
        return None
    limit = int(plan.get("daily_alert_limit") or 0)
    if limit <= 0:
        return None
    used = count_daily_alert_events(rule["debox_user_id"], FREE_ALERT_TIMEZONE)
    if used < limit:
        return None
    return {
        "rule_id": rule["id"],
        "status": "daily_limit",
        "limit": limit,
        "used": used,
    }


def _should_alert_asset(rule_type: str, previous: Decimal, current: Decimal, threshold: Decimal) -> bool:
    delta = current - previous
    absolute_delta = abs(delta)
    if rule_type == "balance_change":
        return delta != 0 and (threshold <= 0 or absolute_delta >= threshold)
    if rule_type == "incoming":
        return delta > 0 and (threshold <= 0 or delta >= threshold)
    if rule_type == "outgoing":
        return delta < 0 and (threshold <= 0 or abs(delta) >= threshold)
    if rule_type == "balance_threshold":
        return current <= threshold
    return False


def check_asset_rule(rule: dict) -> dict:
    current = balance(rule["wallet_address"], rule.get("token_address"), rule.get("chain_key"))
    current_value = current["value"]
    previous_value = rule.get("last_value")
    update_watch_rule_value(int(rule["id"]), current_value)
    if previous_value is None:
        return {"rule_id": rule["id"], "status": "baseline", "value": current_value}

    previous = _decimal(previous_value)
    now = _decimal(current_value)
    threshold = _decimal(rule.get("threshold"))
    if not _should_alert_asset(rule["rule_type"], previous, now, threshold):
        return {"rule_id": rule["id"], "status": "no_change", "value": current_value}

    symbol = current.get("symbol", "TOKEN")
    note = f"{symbol} 余额触发监控条件。"
    plan = _plan_for_rule(rule)
    limited = _free_daily_limit_result(rule, plan)
    if limited:
        return {**limited, "value": current_value}
    message_id = _send_rule_alert(rule, previous_value, current_value, note)
    event = create_alert_event(
        watch_rule_id=int(rule["id"]),
        event_type=rule["rule_type"],
        previous_value=previous_value,
        current_value=current_value,
        notification_message_id=message_id,
    )
    return {"rule_id": rule["id"], "status": "alerted", "event": event}


def check_approval_rule(rule: dict) -> dict:
    if not rule.get("token_address") or not rule.get("target_address"):
        return {"rule_id": rule["id"], "status": "invalid", "reason": "missing token or spender"}
    current = token_allowance(
        rule["wallet_address"],
        rule["token_address"],
        rule["target_address"],
        rule.get("chain_key"),
    )
    current_value = current["value"]
    previous_value = rule.get("last_value")
    update_watch_rule_value(int(rule["id"]), current_value)
    if previous_value is None:
        return {"rule_id": rule["id"], "status": "baseline", "value": current_value}
    if _decimal(previous_value) == _decimal(current_value):
        return {"rule_id": rule["id"], "status": "no_change", "value": current_value}

    note = f"授权对象：{_short(rule.get('target_address'))}。"
    plan = _plan_for_rule(rule)
    limited = _free_daily_limit_result(rule, plan)
    if limited:
        return {**limited, "value": current_value}
    message_id = _send_rule_alert(rule, previous_value, current_value, note)
    event = create_alert_event(
        watch_rule_id=int(rule["id"]),
        event_type="approval_change",
        previous_value=previous_value,
        current_value=current_value,
        notification_message_id=message_id,
    )
    return {"rule_id": rule["id"], "status": "alerted", "event": event}


def check_interaction_rule(rule: dict) -> dict:
    if not rule.get("target_address"):
        return {"rule_id": rule["id"], "status": "invalid", "reason": "missing target address"}
    current = latest_interaction(rule["wallet_address"], rule["target_address"], rule.get("chain_key"))
    cursor = current["cursor"]
    previous_cursor = rule.get("last_value")
    update_watch_rule_value(int(rule["id"]), cursor)
    if previous_cursor is None:
        return {"rule_id": rule["id"], "status": "baseline", "value": cursor}
    if cursor == previous_cursor or not current.get("matched"):
        return {"rule_id": rule["id"], "status": "no_change", "value": cursor}

    note = f"目标地址：{_short(rule.get('target_address'))}。"
    plan = _plan_for_rule(rule)
    limited = _free_daily_limit_result(rule, plan)
    if limited:
        return {**limited, "value": cursor}
    message_id = _send_rule_alert(rule, previous_cursor, cursor, note)
    event = create_alert_event(
        watch_rule_id=int(rule["id"]),
        event_type="address_interaction",
        previous_value=previous_cursor,
        current_value=cursor,
        notification_message_id=message_id,
    )
    return {"rule_id": rule["id"], "status": "alerted", "event": event}


def check_rule(rule: dict) -> dict:
    try:
        plan = _plan_for_rule(rule)
        limited = _rule_allowed_by_plan(rule, plan)
        if limited:
            return limited
        if rule["rule_type"] in ASSET_RULE_TYPES:
            return check_asset_rule(rule)
        if rule["rule_type"] == "approval_change":
            return check_approval_rule(rule)
        if rule["rule_type"] == "address_interaction":
            return check_interaction_rule(rule)
        return {"rule_id": rule["id"], "status": "unsupported", "rule_type": rule["rule_type"]}
    except Exception as exc:
        return {"rule_id": rule.get("id"), "status": "error", "error": str(exc)}


def check_all_rules(limit: int = 200) -> dict:
    results = [check_rule(rule) for rule in list_enabled_watch_rules(limit)]
    return {
        "checked": len(results),
        "alerted": sum(1 for item in results if item.get("status") == "alerted"),
        "errors": [item for item in results if item.get("status") == "error"],
        "results": results,
    }


def _summary_due(subscription: dict) -> tuple[bool, str]:
    timezone_name = subscription.get("daily_summary_timezone") or "Asia/Shanghai"
    try:
        zone = ZoneInfo(timezone_name)
    except Exception:
        zone = ZoneInfo("Asia/Shanghai")
    local_now = datetime.now(timezone.utc).astimezone(zone)
    local_date = local_now.date().isoformat()
    if subscription.get("daily_summary_last_sent_date") == local_date:
        return False, local_date

    push_time = str(subscription.get("daily_summary_time") or "20:00")
    try:
        hour, minute = [int(part) for part in push_time.split(":", 1)]
    except Exception:
        hour, minute = 20, 0
    return (local_now.hour, local_now.minute) >= (hour, minute), local_date


def _summary_text(subscription: dict) -> str:
    user_id = subscription["debox_user_id"]
    rules = list_user_watch_rules(user_id)
    events = list_recent_alert_events(user_id, hours=24, limit=80)
    rule_count_by_type = Counter(rule["rule_type"] for rule in rules)
    event_count_by_type = Counter(event["event_type"] for event in events)
    wallets = {str(rule["wallet_address"]).lower() for rule in rules}

    recent_lines = []
    for event in events[:5]:
        label = RULE_TYPE_LABELS.get(event["event_type"], event["event_type"])
        recent_lines.append(
            f"- {escape(label)}：{escape(_short(event.get('wallet_address')))} "
            f"{escape(str(event.get('previous_value') or '-'))} -> {escape(str(event.get('current_value') or '-'))}"
        )
    recent_text = "<br/>".join(recent_lines) if recent_lines else "今日暂无触发事件。"

    asset_rule_count = sum(rule_count_by_type[key] for key in ASSET_RULE_TYPES)
    asset_event_count = sum(event_count_by_type[key] for key in ASSET_RULE_TYPES)
    alert_hint = "无"
    if events:
        alert_hint = f"有 {len(events)} 条规则在过去 24 小时内触发，请查看下方最近事件。"

    return (
        "<b>DeBox Asset Alert 每日摘要</b><br/>"
        f"统计范围：过去 24 小时<br/>"
        f"今日触发次数：{len(events)}<br/>"
        f"监控钱包数：{len(wallets)}<br/>"
        f"当前规则数：{len(rules)}<br/>"
        f"资产规则：{asset_rule_count}，"
        f"授权规则：{rule_count_by_type['approval_change']}，"
        f"交互规则：{rule_count_by_type['address_interaction']}<br/>"
        f"事件概览：资产 {asset_event_count}，"
        f"授权 {event_count_by_type['approval_change']}，"
        f"交互 {event_count_by_type['address_interaction']}<br/>"
        f"异常提醒：{escape(alert_hint)}<br/><br/>"
        f"{recent_text}"
    )


def send_due_scheduled_reports(limit: int = 100) -> dict:
    sent = 0
    skipped = 0
    errors = []
    for subscription in list_due_scheduled_subscriptions(limit):
        due, local_date = _summary_due(subscription)
        if not due:
            skipped += 1
            continue
        chat_type = subscription.get("daily_summary_chat_type") or "private"
        chat_id = subscription.get("daily_summary_chat_id") or subscription["debox_user_id"]
        if chat_type == "private":
            chat_id = subscription["debox_user_id"]
        try:
            send_notification(chat_id, chat_type, _summary_text(subscription))
            mark_scheduled_push_sent(int(subscription["id"]), local_date)
            sent += 1
        except Exception as exc:
            errors.append({"subscription_id": subscription["id"], "error": str(exc)})
    return {"sent": sent, "skipped": skipped, "errors": errors}
