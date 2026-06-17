from __future__ import annotations

from app.db import (
    activate_subscription,
    count_user_watch_rules,
    get_active_subscription,
    has_used_plan,
)
from app.plans import get_plan


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

    if subscription is None:
        return {
            "subscription": None,
            "plan": None,
            "rule_count": count_user_watch_rules(debox_user_id),
            "can_create_rule": False,
            "reason": "No active subscription",
        }

    plan = get_plan(subscription["plan_code"])
    rule_count = count_user_watch_rules(debox_user_id)
    return {
        "subscription": subscription,
        "plan": {
            **plan,
            "price": str(plan["price"]),
        },
        "rule_count": rule_count,
        "can_create_rule": rule_count < plan["rule_limit"],
        "reason": "",
    }


def require_rule_creation(debox_user_id: str, notification_chat_type: str) -> dict:
    current = entitlement(debox_user_id)
    plan = current["plan"]
    if plan is None:
        raise ValueError("No active subscription. Choose a plan before creating a rule.")
    if not current["can_create_rule"]:
        raise ValueError(
            f"{plan['name']}最多支持 {plan['rule_limit']} 条监控规则。"
        )
    if notification_chat_type == "group" and not plan["group_notifications"]:
        raise ValueError("Group notifications require the Professional plan.")
    return current


def require_group_notification(debox_user_id: str, notification_chat_type: str) -> None:
    if notification_chat_type != "group":
        return
    current = entitlement(debox_user_id)
    if current["plan"] is None or not current["plan"]["group_notifications"]:
        raise ValueError("Group notifications require the Professional plan.")
