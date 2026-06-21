from __future__ import annotations

from datetime import datetime, timezone
from math import ceil

from app.db import (
    activate_subscription,
    count_notification_groups,
    count_user_wallets,
    count_user_watch_rules,
    get_active_subscription,
    get_watch_rule,
    get_user_preferences,
    has_paid_subscription_history,
    list_notification_groups,
    list_user_watch_rules,
    pause_user_watch_rules,
    restore_watch_rule,
    set_free_watch_rule,
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


def enable_free_plan(debox_user_id: str) -> dict | None:
    return get_active_subscription(debox_user_id)


def _is_free_eligible(rule: dict, plan: dict) -> bool:
    return (
        int(rule.get("enabled") or 0) == 1
        and rule["rule_type"] in plan["allowed_rule_types"]
        and rule.get("notification_chat_type") == "private"
    )


def _pause_reason(rule: dict, plan: dict, fallback_free: bool, paid_history: bool) -> str:
    if not int(rule.get("enabled") or 0):
        return "规则已关闭。"
    if rule.get("run_status") == "paused":
        return "规则已暂停。"
    if fallback_free and paid_history:
        return "付费套餐已到期，规则已暂停。"
    if rule["rule_type"] not in plan["allowed_rule_types"]:
        return f"{plan['name']}不支持该规则类型。"
    if rule.get("notification_chat_type") == "group" and not plan["group_notification"]:
        return f"{plan['name']}不支持群通知。"
    return ""


def _classified_rules(
    rules: list[dict],
    plan: dict,
    fallback_free: bool,
    paid_history: bool,
    free_watch_rule_id: int | None,
) -> tuple[list[dict], list[dict]]:
    active_rules = []
    paused_rules = []
    active_wallets = set()
    rule_limit = int(plan["rule_limit"])
    wallet_limit = int(plan["wallet_limit"])
    is_free = plan["code"] == "free"

    for rule in rules:
        reason = _pause_reason(rule, plan, fallback_free, paid_history)
        wallet_key = str(rule.get("wallet_address") or "").lower()
        is_new_wallet = wallet_key and wallet_key not in active_wallets
        can_select_free = is_free and _is_free_eligible(rule, plan)

        if is_free and int(rule["id"]) == int(free_watch_rule_id or 0) and can_select_free:
            reason = ""
        elif can_select_free and int(rule["id"]) != int(free_watch_rule_id or 0):
            reason = reason or "请选择这条规则作为免费版监控后继续执行。"

        if not reason and len(active_rules) >= rule_limit:
            reason = f"超出{plan['name']}规则额度。"
        if not reason and is_new_wallet and len(active_wallets) >= wallet_limit:
            reason = f"超出{plan['name']}钱包额度。"

        if reason:
            paused_rules.append({
                **rule,
                "status": "paused",
                "pause_reason": reason,
                "can_select_free": can_select_free,
            })
            continue

        if wallet_key:
            active_wallets.add(wallet_key)
        active_rules.append({
            **rule,
            "status": "active",
            "pause_reason": "",
            "can_select_free": can_select_free,
        })

    return active_rules, paused_rules


def choose_free_watch_rule(debox_user_id: str, rule_id: int) -> dict:
    if active_plan_for_user(debox_user_id)["code"] != "free":
        raise ValueError("当前不是免费版，无需设置免费版监控规则。")
    set_free_watch_rule(debox_user_id, rule_id)
    return entitlement(debox_user_id, create_trial=False)


def restore_paused_watch_rule(debox_user_id: str, rule_id: int) -> dict:
    plan = active_plan_for_user(debox_user_id)
    if plan["code"] == "free":
        return choose_free_watch_rule(debox_user_id, rule_id)

    rule = get_watch_rule(rule_id, debox_user_id)
    if not rule or not int(rule.get("enabled") or 0):
        raise ValueError("规则不存在或已删除。")
    if rule["rule_type"] not in plan["allowed_rule_types"]:
        raise ValueError(f"当前套餐不支持该规则类型：{rule['rule_type']}")
    if rule.get("notification_chat_type") == "group" and not plan["group_notification"]:
        raise ValueError("当前套餐不支持群通知，请升级专业版。")
    if rule.get("run_status") == "active":
        return entitlement(debox_user_id, create_trial=False)
    if count_user_watch_rules(debox_user_id) >= int(plan["rule_limit"]):
        raise ValueError(f"当前套餐最多支持 {plan['rule_limit']} 条运行规则。")
    if not wallet_is_monitored(debox_user_id, rule["wallet_address"]) and count_user_wallets(debox_user_id) >= int(plan["wallet_limit"]):
        raise ValueError(f"当前套餐最多支持 {plan['wallet_limit']} 个运行钱包。")

    restore_watch_rule(rule_id, debox_user_id)
    return entitlement(debox_user_id, create_trial=False)


def entitlement(debox_user_id: str, create_trial: bool = True) -> dict:
    subscription = get_active_subscription(debox_user_id)
    paid_history = has_paid_subscription_history(debox_user_id)
    fallback_free = subscription is None
    preferences = get_user_preferences(debox_user_id)
    if fallback_free and paid_history:
        pause_user_watch_rules(debox_user_id, preferences.get("free_watch_rule_id"))
    plan = public_plan(subscription["plan_code"] if subscription else "free")
    rules = list_user_watch_rules(debox_user_id)
    active_rules, paused_rules = _classified_rules(
        rules,
        plan,
        fallback_free,
        paid_history,
        preferences.get("free_watch_rule_id"),
    )
    groups = list_notification_groups(debox_user_id)
    return {
        "debox_user_id": debox_user_id,
        "subscription": subscription,
        "plan": plan,
        "paid_history": paid_history,
        "fallback_free": fallback_free,
        "preferences": preferences,
        "days_remaining": _days_remaining(subscription),
        "rule_count": count_user_watch_rules(debox_user_id),
        "wallet_count": count_user_wallets(debox_user_id),
        "group_count": count_notification_groups(debox_user_id),
        "rules": rules,
        "active_rules": active_rules,
        "paused_rules": paused_rules,
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
    return get_plan(subscription["plan_code"] if subscription else "free")


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
        raise ValueError("免费版无需支付。")
    return activate_subscription(debox_user_id, plan["code"], int(plan["days"]))
