from __future__ import annotations

from datetime import datetime, timedelta, timezone
from decimal import Decimal
from html import escape
from zoneinfo import ZoneInfo

from app.chain_service import balance, latest_interaction, token_allowance
from app.db import (
    count_daily_alert_events,
    create_alert_event,
    daily_summary_statistics,
    get_notification_group,
    get_scheduled_subscription,
    list_due_scheduled_subscriptions,
    list_enabled_watch_rules,
    list_summary_recent_events,
    mark_scheduled_push_sent,
    scheduled_summary_lock,
    update_alert_event_notification,
    update_watch_rule_value,
)
from app.debox_service import send_notification
from app.languages import normalize_language
from app.plans import ASSET_RULE_TYPES, RULE_TYPE_LABELS, get_plan


FREE_ALERT_TIMEZONE = "Asia/Shanghai"
RULE_TYPE_LABELS_EN = {
    "balance_change": "Balance change",
    "incoming": "Incoming transfer",
    "outgoing": "Outgoing transfer",
    "balance_threshold": "Balance threshold",
    "approval_change": "Approval change",
    "address_interaction": "Specified address interaction",
}


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


def _send_rule_alert(rule: dict, previous: str | None, current: str, note: str) -> str:
    language = normalize_language(rule.get("notification_language"))
    previous_text = previous if previous is not None else "-"
    if language == "en":
        text = (
            "<b>DeBox Asset Alert</b><br/>"
            f"Rule: {escape(RULE_TYPE_LABELS_EN.get(rule['rule_type'], rule['rule_type']))}<br/>"
            f"Network: {escape(str(rule.get('chain_key', '-')))}<br/>"
            f"Wallet: {escape(_short(rule.get('wallet_address')))}<br/>"
            f"Change: {escape(previous_text)} -> {escape(current)}<br/>"
            f"{escape(note)}"
        )
        return send_notification(rule["notification_chat_id"], rule["notification_chat_type"], text)
    text = (
        "<b>DeBox Asset Alert</b><br/>"
        f"规则：{escape(RULE_TYPE_LABELS.get(rule['rule_type'], rule['rule_type']))}<br/>"
        f"网络：{escape(str(rule.get('chain_key', '-')))}<br/>"
        f"钱包：{escape(_short(rule.get('wallet_address')))}<br/>"
        f"变化：{escape(previous_text)} -> {escape(current)}<br/>"
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
        return previous > threshold and current <= threshold
    return False


def _record_and_send_rule_alert(
    rule: dict,
    previous_value: str | None,
    current_value: str,
    note: str,
) -> dict:
    event = create_alert_event(
        watch_rule_id=int(rule["id"]),
        event_type=rule["rule_type"],
        previous_value=previous_value,
        current_value=current_value,
        notification_status="pending",
    )
    try:
        message_id = _send_rule_alert(rule, previous_value, current_value, note)
    except Exception as exc:
        update_alert_event_notification(
            int(event["id"]),
            status="failed",
            error=str(exc),
        )
        raise
    return update_alert_event_notification(
        int(event["id"]),
        status="sent",
        message_id=message_id,
    )


def check_asset_rule(rule: dict) -> dict:
    current = balance(rule["wallet_address"], rule.get("token_address"), rule.get("chain_key"))
    current_value = current["value"]
    previous_value = rule.get("last_value")
    update_watch_rule_value(int(rule["id"]), current_value)
    initial_threshold_match = (
        previous_value is None
        and rule["rule_type"] == "balance_threshold"
        and _decimal(current_value) <= _decimal(rule.get("threshold"))
    )
    if previous_value is None and not initial_threshold_match:
        return {"rule_id": rule["id"], "status": "baseline", "value": current_value}

    previous = _decimal(previous_value)
    now = _decimal(current_value)
    threshold = _decimal(rule.get("threshold"))
    if not initial_threshold_match and not _should_alert_asset(rule["rule_type"], previous, now, threshold):
        return {"rule_id": rule["id"], "status": "no_change", "value": current_value}

    symbol = current.get("symbol", "TOKEN")
    if rule["rule_type"] == "balance_threshold":
        note = (
            f"{symbol} balance reached or fell below the threshold {rule.get('threshold')}."
            if normalize_language(rule.get("notification_language")) == "en"
            else f"{symbol} 余额达到或低于阈值 {rule.get('threshold')}。"
        )
    else:
        note = (
            f"{symbol} balance matched the monitoring condition."
            if normalize_language(rule.get("notification_language")) == "en"
            else f"{symbol} 余额触发监控条件。"
        )
    plan = _plan_for_rule(rule)
    limited = _free_daily_limit_result(rule, plan)
    if limited:
        return {**limited, "value": current_value}
    event = _record_and_send_rule_alert(rule, previous_value, current_value, note)
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

    note = (
        f"Approved spender: {_short(rule.get('target_address'))}."
        if normalize_language(rule.get("notification_language")) == "en"
        else f"授权对象：{_short(rule.get('target_address'))}。"
    )
    plan = _plan_for_rule(rule)
    limited = _free_daily_limit_result(rule, plan)
    if limited:
        return {**limited, "value": current_value}
    event = _record_and_send_rule_alert(rule, previous_value, current_value, note)
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

    note = (
        f"Target address: {_short(rule.get('target_address'))}."
        if normalize_language(rule.get("notification_language")) == "en"
        else f"目标地址：{_short(rule.get('target_address'))}。"
    )
    plan = _plan_for_rule(rule)
    limited = _free_daily_limit_result(rule, plan)
    if limited:
        return {**limited, "value": cursor}
    event = _record_and_send_rule_alert(rule, previous_cursor, cursor, note)
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


def _summary_due(subscription: dict) -> tuple[bool, str, datetime]:
    timezone_name = subscription.get("daily_summary_timezone") or "Asia/Shanghai"
    try:
        zone = ZoneInfo(timezone_name)
    except Exception:
        zone = ZoneInfo("Asia/Shanghai")
    local_now = datetime.now(timezone.utc).astimezone(zone)
    local_date = local_now.date().isoformat()
    push_time = str(subscription.get("daily_summary_time") or "20:00")
    try:
        hour, minute = [int(part) for part in push_time.split(":", 1)]
        if not 0 <= hour <= 23 or not 0 <= minute <= 59:
            raise ValueError("invalid summary time")
    except Exception:
        hour, minute = 20, 0
    period_end = local_now.replace(hour=hour, minute=minute, second=0, microsecond=0)
    if subscription.get("daily_summary_last_sent_date") == local_date:
        return False, local_date, period_end.astimezone(timezone.utc)
    due = local_now >= period_end
    return due, local_date, period_end.astimezone(timezone.utc)


def _as_utc_datetime(value: object) -> datetime | None:
    if isinstance(value, datetime):
        parsed = value
    elif isinstance(value, str) and value.strip():
        try:
            parsed = datetime.fromisoformat(value.strip().replace("Z", "+00:00"))
        except ValueError:
            return None
    else:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)


def _summary_period(subscription: dict, period_end: datetime) -> tuple[datetime, datetime]:
    previous_end = _as_utc_datetime(subscription.get("daily_summary_last_period_end_at"))
    if previous_end is None or previous_end >= period_end:
        previous_end = period_end - timedelta(hours=24)
    return previous_end, period_end


def _period_label(period_start: datetime, period_end: datetime, timezone_name: str, english: bool) -> str:
    try:
        zone = ZoneInfo(timezone_name)
    except Exception:
        timezone_name = "Asia/Shanghai"
        zone = ZoneInfo(timezone_name)
    start_text = period_start.astimezone(zone).strftime("%Y-%m-%d %H:%M")
    end_text = period_end.astimezone(zone).strftime("%Y-%m-%d %H:%M")
    if english:
        return f"{start_text} to {end_text} ({timezone_name})"
    return f"{start_text} 至 {end_text}（{timezone_name}）"


def _summary_text(
    subscription: dict,
    period_start: datetime,
    period_end: datetime,
) -> str:
    user_id = subscription["debox_user_id"]
    language = normalize_language(subscription.get("daily_summary_language"))
    english = language == "en"
    stats = daily_summary_statistics(user_id, period_start, period_end)
    events = list_summary_recent_events(user_id, period_start, period_end, limit=5)
    timezone_name = subscription.get("daily_summary_timezone") or "Asia/Shanghai"
    period_text = _period_label(period_start, period_end, timezone_name, english)
    summary_label = str(subscription.get("daily_summary_label") or "").strip()

    labels = RULE_TYPE_LABELS_EN if english else RULE_TYPE_LABELS
    separator = ": " if english else "："
    recent_lines = []
    for event in events[:5]:
        label = labels.get(event["event_type"], event["event_type"])
        recent_lines.append(
            f"- {escape(label)}{separator}{escape(_short(event.get('wallet_address')))} "
            f"{escape(str(event.get('previous_value') or '-'))} -> {escape(str(event.get('current_value') or '-'))}"
        )
    recent_text = "<br/>".join(recent_lines) if recent_lines else (
        "No alerts were triggered this period." if english else "本期暂无触发事件。"
    )

    alert_hint = "None" if english else "无"
    if stats["event_count"]:
        alert_hint = (
            f"{stats['event_count']} alerts were triggered this period. Review the recent events below."
            if english
            else f"本期共触发 {stats['event_count']} 次提醒，请查看下方最近事件。"
        )

    if english:
        title = "DeBox Asset Alert Daily Summary"
        if summary_label:
            title = f"{title} · {escape(summary_label)}"
        return (
            f"<b>{title}</b><br/>"
            f"Period: {escape(period_text)}<br/>"
            f"Alerts this period: {stats['event_count']}<br/>"
            f"Notification failures: {stats['failed_notification_count']}<br/>"
            f"Monitored wallets: {stats['wallet_count']}<br/>"
            f"Running rules: {stats['rule_count']}<br/>"
            f"Rules: Assets {stats['asset_rule_count']}, "
            f"approvals {stats['approval_rule_count']}, "
            f"interactions {stats['interaction_rule_count']}<br/>"
            f"Events: Assets {stats['asset_event_count']}, "
            f"approvals {stats['approval_event_count']}, "
            f"interactions {stats['interaction_event_count']}<br/>"
            f"Risk notice: {escape(alert_hint)}<br/><br/>"
            f"<b>Recent events</b><br/>{recent_text}"
        )

    title = "DeBox Asset Alert 每日摘要"
    if summary_label:
        title = f"{title} · {escape(summary_label)}"
    return (
        f"<b>{title}</b><br/>"
        f"统计周期：{escape(period_text)}<br/>"
        f"本期触发次数：{stats['event_count']}<br/>"
        f"通知失败次数：{stats['failed_notification_count']}<br/>"
        f"监控钱包数：{stats['wallet_count']}<br/>"
        f"运行规则数：{stats['rule_count']}<br/>"
        f"资产规则：{stats['asset_rule_count']}，"
        f"授权规则：{stats['approval_rule_count']}，"
        f"交互规则：{stats['interaction_rule_count']}<br/>"
        f"事件概览：资产 {stats['asset_event_count']}，"
        f"授权 {stats['approval_event_count']}，"
        f"交互 {stats['interaction_event_count']}<br/>"
        f"异常提醒：{escape(alert_hint)}<br/><br/>"
        f"<b>最近事件</b><br/>{recent_text}"
    )


def _summary_target(subscription: dict) -> tuple[str, str]:
    user_id = str(subscription.get("debox_user_id") or "").strip()
    if not user_id:
        raise ValueError("摘要订阅缺少 DeBox 用户 ID。")

    chat_type = str(subscription.get("daily_summary_chat_type") or "private").strip().lower()
    if chat_type == "private":
        return user_id, "private"
    if chat_type != "group":
        raise ValueError("摘要通知目标类型无效。")

    chat_id = str(subscription.get("daily_summary_chat_id") or "").strip()
    if not chat_id or get_notification_group(user_id, chat_id) is None:
        raise ValueError("摘要目标群已解绑或不可用。")
    return chat_id, "group"


def send_due_scheduled_reports(limit: int = 100) -> dict:
    sent = 0
    skipped = 0
    locked = 0
    errors = []
    after_id = 0
    page_size = max(1, min(int(limit), 1000))

    while True:
        subscriptions = list_due_scheduled_subscriptions(after_id=after_id, limit=page_size)
        if not subscriptions:
            break
        after_id = int(subscriptions[-1]["id"])

        for candidate in subscriptions:
            subscription_id = int(candidate["id"])
            try:
                with scheduled_summary_lock(subscription_id) as acquired:
                    if not acquired:
                        locked += 1
                        continue

                    subscription = get_scheduled_subscription(subscription_id)
                    if subscription is None:
                        skipped += 1
                        continue
                    due, local_date, period_end = _summary_due(subscription)
                    if not due:
                        skipped += 1
                        continue

                    chat_id, chat_type = _summary_target(subscription)
                    period_start, period_end = _summary_period(subscription, period_end)
                    send_notification(
                        chat_id,
                        chat_type,
                        _summary_text(subscription, period_start, period_end),
                    )
                    mark_scheduled_push_sent(subscription_id, local_date, period_end)
                    sent += 1
            except Exception as exc:
                errors.append({"subscription_id": subscription_id, "error": str(exc)})

    return {"sent": sent, "skipped": skipped, "locked": locked, "errors": errors}
