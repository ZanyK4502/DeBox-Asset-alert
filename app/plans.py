from __future__ import annotations

from decimal import Decimal

from app.config import settings


PLANS = {
    "free": {
        "code": "free",
        "name": "免费体验",
        "price": Decimal("0"),
        "days": 1,
        "rule_limit": 1,
        "group_notifications": False,
        "group_limit": 0,
        "scheduled_push": False,
        "description": "1 条监控规则，24 小时体验，仅支持私聊通知。",
    },
    "standard": {
        "code": "standard",
        "name": "标准订阅",
        "price": settings.subscription_price,
        "days": settings.subscription_days,
        "rule_limit": 10,
        "group_notifications": False,
        "group_limit": 0,
        "scheduled_push": True,
        "description": "最多 10 条监控规则，支持私聊通知和每日资产摘要。",
    },
    "professional": {
        "code": "professional",
        "name": "专业订阅",
        "price": Decimal("25"),
        "days": 30,
        "rule_limit": 50,
        "group_notifications": True,
        "group_limit": 3,
        "scheduled_push": True,
        "description": "最多 50 条监控规则，可绑定 3 个 DeBox 群作为通知目标。",
    },
}


def get_plan(plan_code: str) -> dict:
    plan = PLANS.get(plan_code)
    if not plan:
        raise ValueError("未知订阅套餐")
    return dict(plan)


def public_plans() -> list[dict]:
    return [
        {
            **plan,
            "price": str(plan["price"]),
            "asset": settings.subscription_token_symbol,
        }
        for plan in PLANS.values()
    ]
