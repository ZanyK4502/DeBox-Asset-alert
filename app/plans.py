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
        "description": "1 条规则，24 小时体验",
    },
    "standard": {
        "code": "standard",
        "name": "标准订阅",
        "price": settings.subscription_price,
        "days": settings.subscription_days,
        "rule_limit": 10,
        "group_notifications": False,
        "description": "10 条规则，30 天",
    },
    "professional": {
        "code": "professional",
        "name": "专业订阅",
        "price": Decimal("25"),
        "days": 30,
        "rule_limit": 50,
        "group_notifications": True,
        "description": "50 条规则、群通知，30 天",
    },
}


def get_plan(plan_code: str) -> dict:
    plan = PLANS.get(plan_code)
    if not plan:
        raise ValueError("Unknown subscription plan")
    return plan


def public_plans() -> list[dict]:
    return [
        {
            **plan,
            "price": str(plan["price"]),
            "asset": settings.subscription_token_symbol,
        }
        for plan in PLANS.values()
    ]
