from __future__ import annotations

from datetime import datetime, timezone
from math import ceil

from app.db import (
    activate_subscription,
    count_notification_groups,
    count_user_wallets,
    count_user_watch_rules,
    get_active_subscription,
    has_used_plan,
    list_notification_groups,
    list_user_watch_rules,
    wallet_is_monitored,
)
from app.plans import get_plan, public_plan


UTC = timezone.utc


def _days_remaining(subscription: dict | None) -> int:
    if not subscription:
        return 0
    raw = subscription.get("expires_at")
    if not raw:
        return 0
    expires_at = datetime.fromisoformat(str(raw).replace("Z", "+00:00"))
    if expires_at.tzinfo is None:
        expires_at = expires_at.replace(tzinfo=UTC)
    seconds = (expires_at.astimezone(UTC) - datetime.now(UTC)).total_seconds()
    return max(0, ceil(seconds / 86400))


def ensure_free_trial(debox_user_id: str) -> dict | None:
    active = get_active_subscription(debox_user_id)
    if active:
        return None
    if has_used_plan(debox_user_id, "free"):
        return None
    return activate_subscription(debox_user_id, "free", 1)


def entitlement(debox_user_id: str, create_trial: bool = True) -> dict:
    if create_trial:
        ensure_free_trial(debox_user_id)
    subscription = get_active_subscription(debox_user_id)
    plan = public_plan(subscription["plan_code"]) if subscription else None
    rules = list_user_watch_rules(debox_user_id)
    groups = list_notification_groups(debox_user_id)
    return {
        "debox_user_id": debox_user_id,
        "subscription": subscription,
        "plan": plan,
        "days_remaining": _days_remaining(subscription),
        "rule_count": count_user_watch_rules(debox_user_id),
        "wallet_count": count_user_wallets(debox_user_id),
        "group_count": count_notification_groups(debox_user_id),
        "rules": rules,
        "groups": groups,
        "summary_settings": {
            "enabled": bool((subscription or {}).get("daily_summary_enabled")),
            "time": (subscription or {}).get("daily_summary_time", "20:00"),
            "timezone": (subscription or {}).get("daily_summary_timezone", "Asia/Shanghai"),
            "chat_type": (subscription or {}).get("daily_summary_chat_type", "private"),
            "chat_id": (subscription or {}).get("daily_summary_chat_id", ""),
            "label": (subscription or {}).get("daily_summary_label", ""),
        },
    }


def active_plan_for_user(debox_user_id: str) -> dict:
    subscription = get_active_subscription(debox_user_id)
    if not subscription:
        subscription = ensure_free_trial(debox_user_id)
    if not subscription:
        raise ValueError("没有有效订阅，请先开通套餐。")
    return get_plan(subscription["plan_code"])


def require_rule_creation(
    debox_user_id: str,
    notification_chat_type: str,
    wallet_address: str,
    rule_type: str,
) -> dict:
    plan = active_plan_for_user(debox_user_id)
    if rule_type not in plan["allowed_rule_types"]:
        raise ValueError(f"当前套餐不支持该规则类型：{rule_type}")
    if notification_chat_type == "group" and not plan["group_notification"]:
        raise ValueError("当前套餐不支持群通知，请升级专业版。")
    if count_user_watch_rules(debox_user_id) >= int(plan["rule_limit"]):
        raise ValueError(f"当前套餐最多支持 {plan['rule_limit']} 条规则。")
    if not wallet_is_monitored(debox_user_id, wallet_address) and count_user_wallets(debox_user_id) >= int(plan["wallet_limit"]):
        raise ValueError(f"当前套餐最多支持 {plan['wallet_limit']} 个钱包。")
    return plan


def require_group_slot(debox_user_id: str) -> dict:
    plan = active_plan_for_user(debox_user_id)
    if not plan["group_notification"]:
        raise ValueError("当前套餐不支持群通知，请升级专业版。")
    if count_notification_groups(debox_user_id) >= int(plan["group_limit"]):
        raise ValueError(f"当前套餐最多绑定 {plan['group_limit']} 个群。")
    return plan


def require_summary_target(debox_user_id: str, chat_type: str) -> dict:
    plan = active_plan_for_user(debox_user_id)
    if not plan["daily_summary"]:
        raise ValueError("当前套餐不支持每日摘要。")
    if chat_type not in plan["summary_targets"]:
        raise ValueError("当前套餐不支持把每日摘要发送到这个目标。")
    return plan


def activate_paid_subscription(debox_user_id: str, plan_code: str) -> dict:
    plan = get_plan(plan_code)
    if plan["code"] == "free":
        raise ValueError("免费体验不能通过支付开通。")
    return activate_subscription(debox_user_id, plan["code"], int(plan["days"]))
