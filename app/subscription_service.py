from __future__ import annotations

from datetime import datetime, timezone

from app.db import (
    activate_subscription,
    count_notification_groups,
    count_user_watch_rules,
    get_active_subscription,
    has_used_plan,
    list_notification_groups,
    list_user_watch_rules,
)
from app.plans import get_plan


def _serialize_plan(plan: dict) -> dict:
    return {**plan, "price": str(plan["price"])}


def _serialize_row(row: dict | None) -> dict | None:
    if not row:
        return None
    result = {}
    for key, value in row.items():
        result[key] = value.isoformat() if hasattr(value, "isoformat") else value
    return result


def _days_remaining(expires_at) -> int:
    if not expires_at:
        return 0
    if isinstance(expires_at, str):
        expires_at = datetime.fromisoformat(expires_at.replace("Z", "+00:00"))
    now = datetime.now(expires_at.tzinfo or timezone.utc)
    seconds = max(0, (expires_at - now).total_seconds())
    return int((seconds + 86399) // 86400)


def ensure_free_trial(debox_user_id: str) -> dict | None:
    active = get_active_subscription(debox_user_id)
    if active:
        return active
    if has_used_plan(debox_user_id, "free"):
        return None
    return activate_subscription(debox_user_id, "free", get_plan("free")["days"])


def entitlement(debox_user_id: str, create_trial: bool = True) -> dict:
    subscription = get_active_subscription(debox_user_id)
    if subscription is None and create_trial:
        subscription = ensure_free_trial(debox_user_id)

    rule_count = count_user_watch_rules(debox_user_id)
    group_count = count_notification_groups(debox_user_id)
    rules = list_user_watch_rules(debox_user_id)
    groups = list_notification_groups(debox_user_id)

    if subscription is None:
        return {
            "subscription": None,
            "plan": None,
            "rule_count": rule_count,
            "group_count": group_count,
            "rules": [_serialize_row(rule) for rule in rules],
            "groups": [_serialize_row(group) for group in groups],
            "days_remaining": 0,
            "can_create_rule": False,
            "can_add_group": False,
            "reason": "当前没有有效订阅。",
        }

    plan = get_plan(subscription["plan_code"])
    return {
        "subscription": _serialize_row(subscription),
        "plan": _serialize_plan(plan),
        "rule_count": rule_count,
        "group_count": group_count,
        "rules": [_serialize_row(rule) for rule in rules],
        "groups": [_serialize_row(group) for group in groups],
        "days_remaining": _days_remaining(subscription.get("expires_at")),
        "can_create_rule": rule_count < plan["rule_limit"],
        "can_add_group": bool(plan["group_notifications"]) and group_count < plan["group_limit"],
        "reason": "",
    }


def validate_plan_purchase(debox_user_id: str, plan_code: str) -> dict:
    plan = get_plan(plan_code)
    if plan_code == "free":
        raise ValueError("免费体验不需要支付。")

    active = get_active_subscription(debox_user_id)
    if not active:
        return {"allowed": True, "mode": "new", "plan": _serialize_plan(plan)}

    active_plan = get_plan(active["plan_code"])
    if active["plan_code"] == plan_code:
        return {"allowed": True, "mode": "renew", "plan": _serialize_plan(plan)}
    if active["plan_code"] == "free":
        return {"allowed": True, "mode": "upgrade", "plan": _serialize_plan(plan)}

    raise ValueError(
        f"你当前是{active_plan['name']}，有效期内不能切换到{plan['name']}。"
        "同套餐可以提前续费，到期时间会自动顺延。"
    )


def require_rule_creation(debox_user_id: str, notification_chat_type: str) -> dict:
    current = entitlement(debox_user_id)
    plan = current["plan"]
    if plan is None:
        raise ValueError("当前没有有效订阅，请先选择套餐。")
    if not current["can_create_rule"]:
        raise ValueError(f"{plan['name']}最多支持 {plan['rule_limit']} 条监控规则。")
    if notification_chat_type == "group" and not plan["group_notifications"]:
        raise ValueError("群通知需要专业订阅。")
    return current


def require_group_slot(debox_user_id: str) -> dict:
    current = entitlement(debox_user_id)
    plan = current["plan"]
    if plan is None or not plan["group_notifications"]:
        raise ValueError("群通知需要专业订阅。")
    if not current["can_add_group"]:
        raise ValueError(f"{plan['name']}最多支持 {plan['group_limit']} 个群通知目标。")
    return current
