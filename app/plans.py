from __future__ import annotations

from copy import deepcopy
from decimal import Decimal

from app.config import settings


ASSET_RULE_TYPES = {
    "balance_change",
    "incoming",
    "outgoing",
    "balance_threshold",
}
APPROVAL_RULE_TYPES = {"approval_change"}
INTERACTION_RULE_TYPES = {"address_interaction"}
ALL_RULE_TYPES = ASSET_RULE_TYPES | APPROVAL_RULE_TYPES | INTERACTION_RULE_TYPES

RULE_TYPE_ORDER = [
    "balance_change",
    "incoming",
    "outgoing",
    "balance_threshold",
    "approval_change",
    "address_interaction",
]

RULE_TYPE_LABELS = {
    "balance_change": "余额变化",
    "incoming": "转入提醒",
    "outgoing": "转出提醒",
    "balance_threshold": "余额阈值",
    "approval_change": "授权 / Approve 监控",
    "address_interaction": "指定地址交互提醒",
}

RULE_TYPE_DESCRIPTIONS = {
    "balance_change": "余额发生任意变化时推送通知。",
    "incoming": "余额增加并达到阈值时推送通知。",
    "outgoing": "余额减少并达到阈值时推送通知。",
    "balance_threshold": "余额达到或低于阈值时推送通知。",
    "approval_change": "钱包对指定合约的代币授权额度发生变化时推送通知。",
    "address_interaction": "钱包与指定地址或合约发生交互时推送通知。",
}

PLAN_ORDER = ["free", "standard", "professional"]

PLANS = {
    "free": {
        "code": "free",
        "name": "免费版",
        "price": Decimal("0"),
        "days": 0,
        "wallet_limit": 1,
        "rule_limit": 1,
        "group_limit": 0,
        "daily_alert_limit": 5,
        "allowed_rule_types": [
            "balance_change",
            "incoming",
            "outgoing",
            "balance_threshold",
        ],
        "private_notification": True,
        "group_notification": False,
        "daily_summary": False,
        "summary_targets": [],
        "description": "1 个钱包、1 条基础规则，每日最多 5 次提醒，仅支持私聊通知。",
    },
    "standard": {
        "code": "standard",
        "name": "标准版",
        "price": settings.subscription_price,
        "days": settings.subscription_days,
        "wallet_limit": 3,
        "rule_limit": 10,
        "group_limit": 0,
        "daily_alert_limit": None,
        "allowed_rule_types": [
            "balance_change",
            "incoming",
            "outgoing",
            "balance_threshold",
            "approval_change",
        ],
        "private_notification": True,
        "group_notification": False,
        "daily_summary": True,
        "summary_targets": ["private"],
        "description": "适合个人监控：3 个钱包、10 条规则，支持资产变化、Approve 监控、私聊通知和每日摘要。",
    },
    "professional": {
        "code": "professional",
        "name": "专业版",
        "price": Decimal("25"),
        "days": 30,
        "wallet_limit": 20,
        "rule_limit": 100,
        "group_limit": 3,
        "daily_alert_limit": None,
        "allowed_rule_types": [
            "balance_change",
            "incoming",
            "outgoing",
            "balance_threshold",
            "approval_change",
            "address_interaction",
        ],
        "private_notification": True,
        "group_notification": True,
        "daily_summary": True,
        "summary_targets": ["private", "group"],
        "description": "适合项目方和社群：20 个钱包、100 条规则，支持群通知、指定地址交互提醒和群每日摘要。",
    },
}


def get_plan(plan_code: str | None) -> dict:
    code = (plan_code or "standard").strip().lower()
    if code not in PLANS:
        raise ValueError(f"不支持的套餐：{code}")
    return deepcopy(PLANS[code])


def public_rule_type(rule_type: str) -> dict:
    if rule_type not in ALL_RULE_TYPES:
        raise ValueError(f"不支持的规则类型：{rule_type}")
    return {
        "code": rule_type,
        "label": RULE_TYPE_LABELS[rule_type],
        "description": RULE_TYPE_DESCRIPTIONS[rule_type],
    }


def public_rule_types() -> list[dict]:
    return [public_rule_type(rule_type) for rule_type in RULE_TYPE_ORDER]


def public_plan(plan_code: str) -> dict:
    plan = get_plan(plan_code)
    plan["price"] = str(plan["price"])
    plan["asset"] = settings.subscription_token_symbol
    plan["allowed_rules"] = [
        public_rule_type(rule_type) for rule_type in plan["allowed_rule_types"]
    ]
    return plan


def public_plans() -> list[dict]:
    return [public_plan(code) for code in PLAN_ORDER]
