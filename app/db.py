from __future__ import annotations

from datetime import datetime, timedelta, timezone
from decimal import Decimal
from typing import Any

import psycopg
from psycopg.rows import dict_row

from app.config import settings


UTC = timezone.utc


def now_utc() -> datetime:
    return datetime.now(UTC)


def connect() -> psycopg.Connection:
    if not settings.database_url:
        raise RuntimeError("DATABASE_URL is required. Configure PostgreSQL before starting the app.")
    return psycopg.connect(settings.database_url, row_factory=dict_row)


def _json_value(value: Any) -> Any:
    if isinstance(value, datetime):
        return value.astimezone(UTC).isoformat()
    if isinstance(value, Decimal):
        return str(value)
    return value


def serialize(row: dict | None) -> dict | None:
    if row is None:
        return None
    return {key: _json_value(value) for key, value in dict(row).items()}


def serialize_many(rows: list[dict]) -> list[dict]:
    return [serialize(row) or {} for row in rows]


def initialize_database() -> None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                CREATE TABLE IF NOT EXISTS subscriptions (
                    id SERIAL PRIMARY KEY,
                    debox_user_id TEXT NOT NULL,
                    plan_code TEXT NOT NULL,
                    status TEXT NOT NULL DEFAULT 'active',
                    starts_at TIMESTAMPTZ NOT NULL,
                    expires_at TIMESTAMPTZ NOT NULL,
                    daily_summary_enabled INTEGER NOT NULL DEFAULT 0,
                    daily_summary_time TEXT NOT NULL DEFAULT '20:00',
                    daily_summary_timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
                    daily_summary_chat_type TEXT NOT NULL DEFAULT 'private',
                    daily_summary_chat_id TEXT NOT NULL DEFAULT '',
                    daily_summary_label TEXT NOT NULL DEFAULT '',
                    daily_summary_last_sent_date TEXT NOT NULL DEFAULT '',
                    scheduled_push_last_sent_at TIMESTAMPTZ,
                    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
                )
                """
            )
            cur.execute(
                """
                CREATE TABLE IF NOT EXISTS watch_rules (
                    id SERIAL PRIMARY KEY,
                    debox_user_id TEXT NOT NULL,
                    chain_key TEXT NOT NULL DEFAULT 'bsc',
                    chain_id INTEGER NOT NULL DEFAULT 56,
                    wallet_address TEXT NOT NULL,
                    token_address TEXT,
                    target_address TEXT,
                    target_label TEXT NOT NULL DEFAULT '',
                    rule_type TEXT NOT NULL,
                    threshold NUMERIC NOT NULL DEFAULT 0,
                    notification_chat_id TEXT NOT NULL,
                    notification_chat_type TEXT NOT NULL DEFAULT 'private',
                    notification_label TEXT NOT NULL DEFAULT '',
                    enabled INTEGER NOT NULL DEFAULT 1,
                    last_value TEXT,
                    last_checked_at TIMESTAMPTZ,
                    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
                )
                """
            )
            cur.execute(
                """
                CREATE TABLE IF NOT EXISTS orders (
                    id SERIAL PRIMARY KEY,
                    debox_user_id TEXT NOT NULL,
                    payer_address TEXT NOT NULL,
                    plan_code TEXT NOT NULL DEFAULT 'standard',
                    chain_key TEXT NOT NULL DEFAULT 'bsc',
                    chain_id INTEGER NOT NULL DEFAULT 56,
                    token_address TEXT,
                    token_symbol TEXT NOT NULL DEFAULT 'USDT',
                    token_decimals INTEGER NOT NULL DEFAULT 18,
                    total_amount NUMERIC NOT NULL,
                    recipient_address TEXT NOT NULL,
                    tx_hash TEXT,
                    status TEXT NOT NULL DEFAULT 'pending',
                    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                    expires_at TIMESTAMPTZ NOT NULL,
                    completed_at TIMESTAMPTZ
                )
                """
            )
            cur.execute(
                """
                CREATE TABLE IF NOT EXISTS alert_events (
                    id SERIAL PRIMARY KEY,
                    watch_rule_id INTEGER NOT NULL REFERENCES watch_rules(id) ON DELETE CASCADE,
                    event_type TEXT NOT NULL,
                    previous_value TEXT,
                    current_value TEXT,
                    notification_message_id TEXT,
                    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
                )
                """
            )
            cur.execute(
                """
                CREATE TABLE IF NOT EXISTS notification_groups (
                    id SERIAL PRIMARY KEY,
                    debox_user_id TEXT NOT NULL,
                    gid TEXT NOT NULL,
                    name TEXT NOT NULL DEFAULT '',
                    enabled INTEGER NOT NULL DEFAULT 1,
                    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                    UNIQUE (debox_user_id, gid)
                )
                """
            )
            _migrate(cur)
            cur.execute("CREATE INDEX IF NOT EXISTS idx_watch_rules_user ON watch_rules (debox_user_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_watch_rules_enabled ON watch_rules (enabled)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_subscriptions_user ON subscriptions (debox_user_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_orders_user ON orders (debox_user_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_events_rule ON alert_events (watch_rule_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_groups_user ON notification_groups (debox_user_id)")


def _migrate(cur: psycopg.Cursor) -> None:
    statements = [
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS chain_key TEXT NOT NULL DEFAULT 'bsc'",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS chain_id INTEGER NOT NULL DEFAULT 56",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS target_address TEXT",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS target_label TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS notification_label TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS last_value TEXT",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMPTZ",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_enabled INTEGER NOT NULL DEFAULT 0",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_time TEXT NOT NULL DEFAULT '20:00'",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai'",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_chat_type TEXT NOT NULL DEFAULT 'private'",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_chat_id TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_label TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_last_sent_date TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS scheduled_push_last_sent_at TIMESTAMPTZ",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS plan_code TEXT NOT NULL DEFAULT 'standard'",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS chain_key TEXT NOT NULL DEFAULT 'bsc'",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS chain_id INTEGER NOT NULL DEFAULT 56",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS token_address TEXT",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS token_symbol TEXT NOT NULL DEFAULT 'USDT'",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS token_decimals INTEGER NOT NULL DEFAULT 18",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS recipient_address TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS tx_hash TEXT",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ",
    ]
    for statement in statements:
        cur.execute(statement)


def expire_pending_orders() -> int:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE orders
                SET status = 'expired'
                WHERE status = 'pending' AND expires_at < NOW()
                """
            )
            return cur.rowcount


def expire_active_subscriptions() -> int:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE subscriptions
                SET status = 'expired'
                WHERE status = 'active' AND expires_at < NOW()
                """
            )
            return cur.rowcount


def get_active_subscription(debox_user_id: str) -> dict | None:
    expire_active_subscriptions()
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT *
                FROM subscriptions
                WHERE debox_user_id = %s AND status = 'active' AND expires_at > NOW()
                ORDER BY expires_at DESC
                LIMIT 1
                """,
                (debox_user_id,),
            )
            return serialize(cur.fetchone())


def has_used_plan(debox_user_id: str, plan_code: str) -> bool:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                "SELECT 1 FROM subscriptions WHERE debox_user_id = %s AND plan_code = %s LIMIT 1",
                (debox_user_id, plan_code),
            )
            return cur.fetchone() is not None


def activate_subscription(debox_user_id: str, plan_code: str, days: int) -> dict:
    start = now_utc()
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE subscriptions
                SET status = 'expired'
                WHERE debox_user_id = %s AND status = 'active' AND expires_at < NOW()
                """,
                (debox_user_id,),
            )
            cur.execute(
                """
                SELECT *
                FROM subscriptions
                WHERE debox_user_id = %s AND status = 'active' AND expires_at > NOW()
                ORDER BY expires_at DESC
                LIMIT 1
                """,
                (debox_user_id,),
            )
            active = cur.fetchone()
            if active and active["plan_code"] == plan_code:
                cur.execute(
                    """
                    UPDATE subscriptions
                    SET expires_at = expires_at + (%s || ' days')::interval
                    WHERE id = %s
                    RETURNING *
                    """,
                    (days, active["id"]),
                )
                return serialize(cur.fetchone()) or {}
            if active and active["plan_code"] == "free" and plan_code != "free":
                cur.execute("UPDATE subscriptions SET status = 'upgraded' WHERE id = %s", (active["id"],))
            elif active:
                raise ValueError("当前订阅未到期，暂时不能切换套餐；同套餐可以提前续费。")

            daily_summary = 0 if plan_code == "free" else 1
            cur.execute(
                """
                INSERT INTO subscriptions (
                    debox_user_id, plan_code, status, starts_at, expires_at,
                    daily_summary_enabled, daily_summary_chat_type,
                    daily_summary_chat_id, daily_summary_label
                )
                VALUES (%s, %s, 'active', %s, %s, %s, 'private', %s, '私聊摘要')
                RETURNING *
                """,
                (debox_user_id, plan_code, start, start + timedelta(days=days), daily_summary, debox_user_id),
            )
            return serialize(cur.fetchone()) or {}


def create_order(
    *,
    debox_user_id: str,
    payer_address: str,
    plan_code: str,
    chain_key: str,
    chain_id: int,
    token_address: str | None,
    token_symbol: str,
    token_decimals: int,
    total_amount: Decimal,
    recipient_address: str,
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO orders (
                    debox_user_id, payer_address, plan_code, chain_key, chain_id,
                    token_address, token_symbol, token_decimals, total_amount,
                    recipient_address, expires_at
                )
                VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
                RETURNING *
                """,
                (
                    debox_user_id,
                    payer_address,
                    plan_code,
                    chain_key,
                    chain_id,
                    token_address,
                    token_symbol,
                    token_decimals,
                    total_amount,
                    recipient_address,
                    now_utc() + timedelta(minutes=20),
                ),
            )
            return serialize(cur.fetchone()) or {}


def get_order(order_id: int) -> dict | None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT * FROM orders WHERE id = %s", (order_id,))
            return serialize(cur.fetchone())


def complete_order(order_id: int, tx_hash: str) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE orders
                SET status = 'paid', tx_hash = %s, completed_at = NOW()
                WHERE id = %s AND status IN ('pending', 'preview')
                RETURNING *
                """,
                (tx_hash, order_id),
            )
            row = cur.fetchone()
            if not row:
                raise ValueError("订单不存在或已经处理。")
            return serialize(row) or {}


def create_watch_rule(
    *,
    debox_user_id: str,
    chain_key: str,
    chain_id: int,
    wallet_address: str,
    token_address: str | None,
    target_address: str | None,
    target_label: str,
    rule_type: str,
    threshold: Decimal,
    notification_chat_id: str,
    notification_chat_type: str,
    notification_label: str,
    last_value: str | None,
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO watch_rules (
                    debox_user_id, chain_key, chain_id, wallet_address,
                    token_address, target_address, target_label, rule_type,
                    threshold, notification_chat_id, notification_chat_type,
                    notification_label, last_value, last_checked_at
                )
                VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, NOW())
                RETURNING *
                """,
                (
                    debox_user_id,
                    chain_key,
                    chain_id,
                    wallet_address,
                    token_address,
                    target_address,
                    target_label,
                    rule_type,
                    threshold,
                    notification_chat_id,
                    notification_chat_type,
                    notification_label,
                    last_value,
                ),
            )
            return serialize(cur.fetchone()) or {}


def delete_watch_rule(rule_id: int, debox_user_id: str) -> bool:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                "DELETE FROM watch_rules WHERE id = %s AND debox_user_id = %s",
                (rule_id, debox_user_id),
            )
            return cur.rowcount > 0


def count_user_watch_rules(debox_user_id: str) -> int:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                "SELECT COUNT(*) AS count FROM watch_rules WHERE debox_user_id = %s AND enabled = 1",
                (debox_user_id,),
            )
            return int(cur.fetchone()["count"])


def count_user_wallets(debox_user_id: str) -> int:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT COUNT(DISTINCT LOWER(wallet_address)) AS count
                FROM watch_rules
                WHERE debox_user_id = %s AND enabled = 1
                """,
                (debox_user_id,),
            )
            return int(cur.fetchone()["count"])


def wallet_is_monitored(debox_user_id: str, wallet_address: str) -> bool:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT 1 FROM watch_rules
                WHERE debox_user_id = %s AND LOWER(wallet_address) = LOWER(%s) AND enabled = 1
                LIMIT 1
                """,
                (debox_user_id, wallet_address),
            )
            return cur.fetchone() is not None


def list_user_watch_rules(debox_user_id: str) -> list[dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT *
                FROM watch_rules
                WHERE debox_user_id = %s
                ORDER BY created_at DESC
                """,
                (debox_user_id,),
            )
            return serialize_many(cur.fetchall())


def list_enabled_watch_rules(limit: int = 200) -> list[dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT wr.*
                FROM watch_rules wr
                WHERE wr.enabled = 1
                  AND EXISTS (
                      SELECT 1 FROM subscriptions s
                      WHERE s.debox_user_id = wr.debox_user_id
                        AND s.status = 'active'
                        AND s.expires_at > NOW()
                  )
                ORDER BY wr.last_checked_at NULLS FIRST, wr.id ASC
                LIMIT %s
                """,
                (max(1, min(int(limit), 1000)),),
            )
            return serialize_many(cur.fetchall())


def update_watch_rule_value(rule_id: int, value: str) -> None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE watch_rules
                SET last_value = %s, last_checked_at = NOW()
                WHERE id = %s
                """,
                (value, rule_id),
            )


def create_alert_event(
    watch_rule_id: int,
    event_type: str,
    previous_value: str | None,
    current_value: str | None,
    notification_message_id: str | None = None,
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO alert_events (
                    watch_rule_id, event_type, previous_value, current_value, notification_message_id
                )
                VALUES (%s, %s, %s, %s, %s)
                RETURNING *
                """,
                (watch_rule_id, event_type, previous_value, current_value, notification_message_id),
            )
            return serialize(cur.fetchone()) or {}


def list_recent_alert_events(debox_user_id: str, hours: int = 24, limit: int = 50) -> list[dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT ae.*, wr.chain_key, wr.wallet_address, wr.token_address, wr.rule_type, wr.target_address
                FROM alert_events ae
                JOIN watch_rules wr ON wr.id = ae.watch_rule_id
                WHERE wr.debox_user_id = %s
                  AND ae.created_at >= NOW() - (%s || ' hours')::interval
                ORDER BY ae.created_at DESC
                LIMIT %s
                """,
                (debox_user_id, hours, max(1, min(int(limit), 500))),
            )
            return serialize_many(cur.fetchall())


def create_notification_group(debox_user_id: str, gid: str, name: str = "") -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO notification_groups (debox_user_id, gid, name, enabled)
                VALUES (%s, %s, %s, 1)
                ON CONFLICT (debox_user_id, gid)
                DO UPDATE SET name = EXCLUDED.name, enabled = 1
                RETURNING *
                """,
                (debox_user_id, gid, name),
            )
            return serialize(cur.fetchone()) or {}


def get_notification_group(debox_user_id: str, gid: str) -> dict | None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT * FROM notification_groups
                WHERE debox_user_id = %s AND gid = %s AND enabled = 1
                LIMIT 1
                """,
                (debox_user_id, gid),
            )
            return serialize(cur.fetchone())


def list_notification_groups(debox_user_id: str) -> list[dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT * FROM notification_groups
                WHERE debox_user_id = %s AND enabled = 1
                ORDER BY created_at DESC
                """,
                (debox_user_id,),
            )
            return serialize_many(cur.fetchall())


def count_notification_groups(debox_user_id: str) -> int:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT COUNT(*) AS count FROM notification_groups
                WHERE debox_user_id = %s AND enabled = 1
                """,
                (debox_user_id,),
            )
            return int(cur.fetchone()["count"])


def delete_notification_group(group_id: int, debox_user_id: str) -> bool:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE notification_groups
                SET enabled = 0
                WHERE id = %s AND debox_user_id = %s
                """,
                (group_id, debox_user_id),
            )
            return cur.rowcount > 0


def update_daily_summary_settings(
    *,
    debox_user_id: str,
    enabled: bool,
    push_time: str,
    timezone_name: str,
    chat_type: str,
    chat_id: str,
    label: str,
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE subscriptions
                SET daily_summary_enabled = %s,
                    daily_summary_time = %s,
                    daily_summary_timezone = %s,
                    daily_summary_chat_type = %s,
                    daily_summary_chat_id = %s,
                    daily_summary_label = %s
                WHERE debox_user_id = %s AND status = 'active' AND expires_at > NOW()
                RETURNING *
                """,
                (
                    1 if enabled else 0,
                    push_time,
                    timezone_name,
                    chat_type,
                    chat_id,
                    label,
                    debox_user_id,
                ),
            )
            row = cur.fetchone()
            if not row:
                raise ValueError("没有可更新的有效订阅。")
            return serialize(row) or {}


def list_due_scheduled_subscriptions(limit: int = 100) -> list[dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT *
                FROM subscriptions
                WHERE status = 'active'
                  AND expires_at > NOW()
                  AND daily_summary_enabled = 1
                ORDER BY daily_summary_time ASC
                LIMIT %s
                """,
                (max(1, min(int(limit), 1000)),),
            )
            return serialize_many(cur.fetchall())


def mark_scheduled_push_sent(subscription_id: int, sent_date: str) -> None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE subscriptions
                SET daily_summary_last_sent_date = %s,
                    scheduled_push_last_sent_at = NOW()
                WHERE id = %s
                """,
                (sent_date, subscription_id),
            )
