from __future__ import annotations

from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
from decimal import Decimal
from typing import Any, Iterator

import psycopg
from psycopg.rows import dict_row

from app.config import settings
from app.languages import DEFAULT_LANGUAGE, normalize_language


UTC = timezone.utc
SUMMARY_LOCK_NAMESPACE = 7_220_026
DATABASE_INIT_LOCK_NAMESPACE = 7_220_026_001


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
            cur.execute("SELECT pg_advisory_xact_lock(%s)", (DATABASE_INIT_LOCK_NAMESPACE,))
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
                    daily_summary_language TEXT NOT NULL DEFAULT 'zh',
                    daily_summary_last_sent_date TEXT NOT NULL DEFAULT '',
                    scheduled_push_last_sent_at TIMESTAMPTZ,
                    daily_summary_last_period_end_at TIMESTAMPTZ,
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
                    notification_language TEXT NOT NULL DEFAULT 'zh',
                    enabled INTEGER NOT NULL DEFAULT 1,
                    run_status TEXT NOT NULL DEFAULT 'active',
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
                    completed_at TIMESTAMPTZ,
                    tx_block_number BIGINT,
                    tx_confirmations INTEGER NOT NULL DEFAULT 0,
                    verified_at TIMESTAMPTZ,
                    verification_error TEXT NOT NULL DEFAULT ''
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
                    notification_status TEXT NOT NULL DEFAULT 'sent',
                    notification_error TEXT NOT NULL DEFAULT '',
                    notification_attempts INTEGER NOT NULL DEFAULT 0,
                    notification_attempted_at TIMESTAMPTZ,
                    notification_sent_at TIMESTAMPTZ,
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
            cur.execute(
                """
                CREATE TABLE IF NOT EXISTS user_preferences (
                    debox_user_id TEXT PRIMARY KEY,
                    free_watch_rule_id INTEGER REFERENCES watch_rules(id) ON DELETE SET NULL,
                    bot_language TEXT NOT NULL DEFAULT 'zh',
                    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
                )
                """
            )
            cur.execute(
                """
                CREATE TABLE IF NOT EXISTS auth_challenges (
                    challenge_id TEXT PRIMARY KEY,
                    wallet_address TEXT NOT NULL,
                    nonce_hash TEXT NOT NULL UNIQUE,
                    message TEXT NOT NULL,
                    expires_at TIMESTAMPTZ NOT NULL,
                    used_at TIMESTAMPTZ,
                    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
                )
                """
            )
            cur.execute(
                """
                CREATE TABLE IF NOT EXISTS auth_sessions (
                    token_hash TEXT PRIMARY KEY,
                    debox_user_id TEXT NOT NULL,
                    wallet_address TEXT NOT NULL,
                    expires_at TIMESTAMPTZ NOT NULL,
                    revoked_at TIMESTAMPTZ,
                    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
                )
                """
            )
            cur.execute(
                """
                CREATE TABLE IF NOT EXISTS complimentary_grants (
                    wallet_address TEXT PRIMARY KEY,
                    debox_user_id TEXT NOT NULL,
                    plan_code TEXT NOT NULL,
                    starts_at TIMESTAMPTZ NOT NULL,
                    expires_at TIMESTAMPTZ NOT NULL,
                    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
                )
                """
            )
            _migrate(cur)
            cur.execute("CREATE INDEX IF NOT EXISTS idx_watch_rules_user ON watch_rules (debox_user_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_watch_rules_enabled ON watch_rules (enabled)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_watch_rules_run_status ON watch_rules (run_status)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_subscriptions_user ON subscriptions (debox_user_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_orders_user ON orders (debox_user_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_events_rule ON alert_events (watch_rule_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_events_created ON alert_events (created_at)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_groups_user ON notification_groups (debox_user_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_auth_challenges_expiry ON auth_challenges (expires_at)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions (debox_user_id)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_auth_sessions_expiry ON auth_sessions (expires_at)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_complimentary_grants_user ON complimentary_grants (debox_user_id)")
            cur.execute(
                """
                CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_tx_hash_unique
                ON orders (LOWER(tx_hash))
                WHERE tx_hash IS NOT NULL AND tx_hash <> ''
                """
            )
            cur.execute(
                """
                WITH ranked_open_orders AS (
                    SELECT id,
                           ROW_NUMBER() OVER (
                               PARTITION BY debox_user_id
                               ORDER BY
                                   CASE WHEN status = 'confirming' THEN 0 ELSE 1 END,
                                   created_at DESC,
                                   id DESC
                           ) AS position
                    FROM orders
                    WHERE status IN ('pending', 'confirming')
                )
                UPDATE orders
                SET status = 'expired',
                    verification_error = 'superseded during payment migration'
                FROM ranked_open_orders
                WHERE orders.id = ranked_open_orders.id
                  AND ranked_open_orders.position > 1
                """
            )
            cur.execute(
                """
                CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_one_open_per_user
                ON orders (debox_user_id)
                WHERE status IN ('pending', 'confirming')
                """
            )


def _migrate(cur: psycopg.Cursor) -> None:
    statements = [
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS chain_key TEXT NOT NULL DEFAULT 'bsc'",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS chain_id INTEGER NOT NULL DEFAULT 56",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS target_address TEXT",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS target_label TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS notification_label TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS notification_language TEXT NOT NULL DEFAULT 'zh'",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS run_status TEXT NOT NULL DEFAULT 'active'",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS last_value TEXT",
        "ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMPTZ",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_enabled INTEGER NOT NULL DEFAULT 0",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_time TEXT NOT NULL DEFAULT '20:00'",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai'",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_chat_type TEXT NOT NULL DEFAULT 'private'",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_chat_id TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_label TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_language TEXT NOT NULL DEFAULT 'zh'",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_last_sent_date TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS scheduled_push_last_sent_at TIMESTAMPTZ",
        "ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_last_period_end_at TIMESTAMPTZ",
        "ALTER TABLE user_preferences ADD COLUMN IF NOT EXISTS bot_language TEXT NOT NULL DEFAULT 'zh'",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS plan_code TEXT NOT NULL DEFAULT 'standard'",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS chain_key TEXT NOT NULL DEFAULT 'bsc'",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS chain_id INTEGER NOT NULL DEFAULT 56",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS token_address TEXT",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS token_symbol TEXT NOT NULL DEFAULT 'USDT'",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS token_decimals INTEGER NOT NULL DEFAULT 18",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS recipient_address TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS tx_hash TEXT",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS status TEXT",
        "UPDATE orders SET status = 'pending' WHERE status IS NULL",
        "ALTER TABLE orders ALTER COLUMN status SET DEFAULT 'pending'",
        "ALTER TABLE orders ALTER COLUMN status SET NOT NULL",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ",
        "UPDATE orders SET created_at = NOW() WHERE created_at IS NULL",
        "ALTER TABLE orders ALTER COLUMN created_at SET DEFAULT NOW()",
        "ALTER TABLE orders ALTER COLUMN created_at SET NOT NULL",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ",
        "UPDATE orders SET expires_at = created_at + INTERVAL '20 minutes' WHERE expires_at IS NULL",
        "ALTER TABLE orders ALTER COLUMN expires_at SET DEFAULT (NOW() + INTERVAL '20 minutes')",
        "ALTER TABLE orders ALTER COLUMN expires_at SET NOT NULL",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS tx_block_number BIGINT",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS tx_confirmations INTEGER NOT NULL DEFAULT 0",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ",
        "ALTER TABLE orders ADD COLUMN IF NOT EXISTS verification_error TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_status TEXT NOT NULL DEFAULT 'sent'",
        "ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_error TEXT NOT NULL DEFAULT ''",
        "ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_attempts INTEGER NOT NULL DEFAULT 0",
        "ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_attempted_at TIMESTAMPTZ",
        "ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_sent_at TIMESTAMPTZ",
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


def cleanup_auth_records() -> None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                DELETE FROM auth_challenges
                WHERE expires_at < NOW() - INTERVAL '1 day'
                   OR used_at < NOW() - INTERVAL '1 day'
                """
            )
            cur.execute(
                """
                DELETE FROM auth_sessions
                WHERE expires_at < NOW() - INTERVAL '30 days'
                   OR revoked_at < NOW() - INTERVAL '30 days'
                """
            )


def create_auth_challenge(
    challenge_id: str,
    wallet_address: str,
    nonce_hash: str,
    message: str,
    expires_at: datetime,
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO auth_challenges (
                    challenge_id, wallet_address, nonce_hash, message, expires_at
                )
                VALUES (%s, %s, %s, %s, %s)
                RETURNING *
                """,
                (challenge_id, wallet_address, nonce_hash, message, expires_at),
            )
            return serialize(cur.fetchone()) or {}


def get_active_auth_challenge(challenge_id: str, wallet_address: str) -> dict | None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT *
                FROM auth_challenges
                WHERE challenge_id = %s
                  AND LOWER(wallet_address) = LOWER(%s)
                  AND used_at IS NULL
                  AND expires_at > NOW()
                """,
                (challenge_id, wallet_address),
            )
            return serialize(cur.fetchone())


def consume_auth_challenge(challenge_id: str, wallet_address: str) -> bool:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE auth_challenges
                SET used_at = NOW()
                WHERE challenge_id = %s
                  AND LOWER(wallet_address) = LOWER(%s)
                  AND used_at IS NULL
                  AND expires_at > NOW()
                """,
                (challenge_id, wallet_address),
            )
            return cur.rowcount == 1


def create_auth_session(
    token_hash: str,
    debox_user_id: str,
    wallet_address: str,
    expires_at: datetime,
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO auth_sessions (
                    token_hash, debox_user_id, wallet_address, expires_at
                )
                VALUES (%s, %s, %s, %s)
                RETURNING *
                """,
                (token_hash, debox_user_id, wallet_address, expires_at),
            )
            return serialize(cur.fetchone()) or {}


def get_active_auth_session(token_hash: str) -> dict | None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE auth_sessions
                SET last_seen_at = NOW()
                WHERE token_hash = %s
                  AND revoked_at IS NULL
                  AND expires_at > NOW()
                RETURNING *
                """,
                (token_hash,),
            )
            return serialize(cur.fetchone())


def revoke_auth_session(token_hash: str) -> bool:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE auth_sessions
                SET revoked_at = NOW()
                WHERE token_hash = %s AND revoked_at IS NULL
                """,
                (token_hash,),
            )
            return cur.rowcount == 1


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


def has_paid_subscription_history(debox_user_id: str) -> bool:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT 1
                FROM subscriptions
                WHERE debox_user_id = %s
                  AND plan_code <> 'free'
                LIMIT 1
                """,
                (debox_user_id,),
            )
            return cur.fetchone() is not None


def activate_subscription(debox_user_id: str, plan_code: str, days: int) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            return _activate_subscription(cur, debox_user_id, plan_code, days)


def _activate_subscription(
    cur: psycopg.Cursor,
    debox_user_id: str,
    plan_code: str,
    days: int,
    *,
    allow_renewal: bool = True,
) -> dict:
    start = now_utc()
    cur.execute("SELECT pg_advisory_xact_lock(hashtext(%s))", (debox_user_id,))
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
        FOR UPDATE
        """,
        (debox_user_id,),
    )
    active = cur.fetchone()
    if active and active["plan_code"] == plan_code and allow_renewal:
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
        (debox_user_id, plan_code, start, start + timedelta(days=days), 0, debox_user_id),
    )
    return serialize(cur.fetchone()) or {}


def get_complimentary_grant(wallet_address: str) -> dict | None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT *
                FROM complimentary_grants
                WHERE LOWER(wallet_address) = LOWER(%s)
                """,
                (wallet_address,),
            )
            return serialize(cur.fetchone())


def activate_complimentary_subscription(
    debox_user_id: str,
    wallet_address: str,
    plan_code: str,
    days: int,
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                "SELECT pg_advisory_xact_lock(hashtext(%s))",
                (f"complimentary:{wallet_address.lower()}",),
            )
            cur.execute(
                """
                SELECT 1
                FROM complimentary_grants
                WHERE LOWER(wallet_address) = LOWER(%s)
                """,
                (wallet_address,),
            )
            if cur.fetchone():
                raise ValueError("该白名单钱包已经领取过免费套餐。")

            subscription = _activate_subscription(
                cur,
                debox_user_id,
                plan_code,
                days,
                allow_renewal=False,
            )
            cur.execute(
                """
                INSERT INTO complimentary_grants (
                    wallet_address, debox_user_id, plan_code, starts_at, expires_at
                )
                VALUES (%s, %s, %s, %s, %s)
                RETURNING *
                """,
                (
                    wallet_address.lower(),
                    debox_user_id,
                    plan_code,
                    subscription["starts_at"],
                    subscription["expires_at"],
                ),
            )
            grant = serialize(cur.fetchone()) or {}
            return {"subscription": subscription, "grant": grant}


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
            cur.execute("SELECT pg_advisory_xact_lock(hashtext(%s))", (debox_user_id,))
            cur.execute(
                """
                SELECT plan_code
                FROM subscriptions
                WHERE debox_user_id = %s AND status = 'active' AND expires_at > NOW()
                ORDER BY expires_at DESC
                LIMIT 1
                """,
                (debox_user_id,),
            )
            active = cur.fetchone()
            if active and active["plan_code"] not in {"free", plan_code}:
                raise ValueError("当前付费套餐未到期，只能续费同一套餐；到期后才能选择其他套餐。")
            cur.execute(
                """
                SELECT 1 FROM orders
                WHERE debox_user_id = %s AND status = 'confirming'
                LIMIT 1
                """,
                (debox_user_id,),
            )
            if cur.fetchone():
                raise ValueError("已有一笔支付正在链上确认，请等待确认完成后再操作。")
            cur.execute(
                """
                UPDATE orders
                SET status = 'expired', verification_error = 'replaced by a newer payment order'
                WHERE debox_user_id = %s AND status = 'pending'
                """,
                (debox_user_id,),
            )
            try:
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
            except psycopg.errors.UniqueViolation as exc:
                raise ValueError("已有一笔支付正在处理，请勿重复提交。") from exc
            return serialize(cur.fetchone()) or {}


def get_order(order_id: int) -> dict | None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT * FROM orders WHERE id = %s", (order_id,))
            return serialize(cur.fetchone())


def claim_order_transaction(
    order_id: int,
    debox_user_id: str,
    payer_address: str,
    tx_hash: str,
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            try:
                cur.execute(
                    """
                    UPDATE orders
                    SET status = 'confirming',
                        tx_hash = %s,
                        verified_at = NOW(),
                        verification_error = ''
                    WHERE id = %s
                      AND debox_user_id = %s
                      AND LOWER(payer_address) = LOWER(%s)
                      AND (
                          (status = 'pending' AND expires_at > NOW())
                          OR status = 'confirming'
                      )
                      AND (tx_hash IS NULL OR tx_hash = '' OR LOWER(tx_hash) = LOWER(%s))
                    RETURNING *
                    """,
                    (tx_hash, order_id, debox_user_id, payer_address, tx_hash),
                )
            except psycopg.errors.UniqueViolation as exc:
                raise ValueError("这笔链上交易已经用于其他订单。") from exc
            row = cur.fetchone()
            if not row:
                cur.execute("SELECT * FROM orders WHERE id = %s", (order_id,))
                existing = cur.fetchone()
                if existing and existing["status"] == "paid" and str(existing.get("tx_hash") or "").lower() == tx_hash.lower():
                    return serialize(existing) or {}
                raise ValueError("订单不存在、已失效，或不属于当前登录钱包。")
            return serialize(row) or {}


def update_order_verification(
    order_id: int,
    *,
    status: str,
    block_number: int | None = None,
    confirmations: int = 0,
    error: str = "",
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE orders
                SET status = %s,
                    tx_block_number = %s,
                    tx_confirmations = %s,
                    verified_at = NOW(),
                    verification_error = %s
                WHERE id = %s AND status <> 'paid'
                RETURNING *
                """,
                (status, block_number, max(0, confirmations), error[:500], order_id),
            )
            row = cur.fetchone()
            if not row:
                cur.execute("SELECT * FROM orders WHERE id = %s", (order_id,))
                row = cur.fetchone()
            if not row:
                raise ValueError("订单不存在。")
            return serialize(row) or {}


def list_confirming_orders(limit: int = 50) -> list[dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT *
                FROM orders
                WHERE status = 'confirming' AND tx_hash IS NOT NULL AND tx_hash <> ''
                ORDER BY verified_at ASC NULLS FIRST, id ASC
                LIMIT %s
                """,
                (max(1, min(int(limit), 200)),),
            )
            return serialize_many(cur.fetchall())


def finalize_paid_order(
    order_id: int,
    tx_hash: str,
    block_number: int,
    confirmations: int,
    subscription_days: int,
) -> tuple[dict, dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT * FROM orders WHERE id = %s FOR UPDATE", (order_id,))
            order = cur.fetchone()
            if not order:
                raise ValueError("订单不存在。")
            if order["status"] == "paid":
                if str(order.get("tx_hash") or "").lower() != tx_hash.lower():
                    raise ValueError("订单已由其他交易完成。")
                cur.execute(
                    """
                    SELECT * FROM subscriptions
                    WHERE debox_user_id = %s AND status = 'active' AND expires_at > NOW()
                    ORDER BY expires_at DESC LIMIT 1
                    """,
                    (order["debox_user_id"],),
                )
                return serialize(order) or {}, serialize(cur.fetchone()) or {}
            if order["status"] != "confirming" or str(order.get("tx_hash") or "").lower() != tx_hash.lower():
                raise ValueError("订单不在可完成状态。")

            subscription = _activate_subscription(
                cur,
                order["debox_user_id"],
                order["plan_code"],
                subscription_days,
            )
            cur.execute(
                """
                UPDATE orders
                SET status = 'paid',
                    tx_block_number = %s,
                    tx_confirmations = %s,
                    verified_at = NOW(),
                    verification_error = '',
                    completed_at = NOW()
                WHERE id = %s AND status = 'confirming'
                RETURNING *
                """,
                (block_number, confirmations, order_id),
            )
            paid_order = cur.fetchone()
            if not paid_order:
                raise ValueError("订单已被其他请求处理。")
            return serialize(paid_order) or {}, subscription


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
    notification_language: str,
    last_value: str | None,
) -> dict:
    notification_language = normalize_language(notification_language)
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO watch_rules (
                    debox_user_id, chain_key, chain_id, wallet_address,
                    token_address, target_address, target_label, rule_type,
                    threshold, notification_chat_id, notification_chat_type,
                    notification_label, notification_language, run_status,
                    last_value, last_checked_at
                )
                VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, 'active', %s, NOW())
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
                    notification_language,
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


def delete_paused_watch_rules(debox_user_id: str) -> int:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                DELETE FROM watch_rules
                WHERE debox_user_id = %s
                  AND run_status = 'paused'
                """,
                (debox_user_id,),
            )
            return cur.rowcount


def get_watch_rule(rule_id: int, debox_user_id: str) -> dict | None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                "SELECT * FROM watch_rules WHERE id = %s AND debox_user_id = %s",
                (rule_id, debox_user_id),
            )
            return serialize(cur.fetchone())


def update_watch_rule_notification_language(
    rule_id: int,
    debox_user_id: str,
    language: str,
) -> dict:
    language = normalize_language(language)
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE watch_rules
                SET notification_language = %s
                WHERE id = %s AND debox_user_id = %s
                RETURNING *
                """,
                (language, rule_id, debox_user_id),
            )
            row = cur.fetchone()
            if row is None:
                raise ValueError("规则不存在或已删除。")
            return serialize(row) or {}


def restore_watch_rule(rule_id: int, debox_user_id: str) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE watch_rules
                SET run_status = 'active'
                WHERE id = %s
                  AND debox_user_id = %s
                  AND enabled = 1
                RETURNING *
                """,
                (rule_id, debox_user_id),
            )
            row = cur.fetchone()
            if row is None:
                raise ValueError("规则不存在或已删除。")
            return serialize(row) or {}


def count_user_watch_rules(debox_user_id: str) -> int:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT COUNT(*) AS count
                FROM watch_rules
                WHERE debox_user_id = %s
                  AND enabled = 1
                  AND run_status = 'active'
                """,
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
                WHERE debox_user_id = %s
                  AND enabled = 1
                  AND run_status = 'active'
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
                WHERE debox_user_id = %s
                  AND LOWER(wallet_address) = LOWER(%s)
                  AND enabled = 1
                  AND run_status = 'active'
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


def get_user_preferences(debox_user_id: str) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT * FROM user_preferences WHERE debox_user_id = %s", (debox_user_id,))
            preferences = serialize(cur.fetchone()) or {
                "debox_user_id": debox_user_id,
                "free_watch_rule_id": None,
                "bot_language": DEFAULT_LANGUAGE,
            }
            preferences["bot_language"] = normalize_language(preferences.get("bot_language"))
            return preferences


def set_bot_language(debox_user_id: str, language: str) -> dict:
    language = normalize_language(language)
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO user_preferences (debox_user_id, bot_language, updated_at)
                VALUES (%s, %s, NOW())
                ON CONFLICT (debox_user_id)
                DO UPDATE SET bot_language = EXCLUDED.bot_language, updated_at = NOW()
                RETURNING *
                """,
                (debox_user_id, language),
            )
            return serialize(cur.fetchone()) or {}


def set_free_watch_rule(debox_user_id: str, rule_id: int) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT 1
                FROM watch_rules
                WHERE id = %s
                  AND debox_user_id = %s
                  AND enabled = 1
                  AND notification_chat_type = 'private'
                  AND rule_type IN (
                    'balance_change',
                    'incoming',
                    'outgoing',
                    'balance_threshold'
                  )
                LIMIT 1
                """,
                (rule_id, debox_user_id),
            )
            if cur.fetchone() is None:
                raise ValueError("这条规则不能设为免费版监控。")
            cur.execute(
                """
                UPDATE watch_rules
                SET run_status = CASE WHEN id = %s THEN 'active' ELSE 'paused' END
                WHERE debox_user_id = %s
                  AND enabled = 1
                """,
                (rule_id, debox_user_id),
            )
            cur.execute(
                """
                INSERT INTO user_preferences (debox_user_id, free_watch_rule_id, updated_at)
                VALUES (%s, %s, NOW())
                ON CONFLICT (debox_user_id)
                DO UPDATE SET free_watch_rule_id = EXCLUDED.free_watch_rule_id, updated_at = NOW()
                RETURNING *
                """,
                (debox_user_id, rule_id),
            )
            return serialize(cur.fetchone()) or {}


def pause_user_watch_rules(debox_user_id: str, except_rule_id: int | None = None) -> int:
    with connect() as conn:
        with conn.cursor() as cur:
            params: tuple = (debox_user_id,)
            keep_clause = ""
            if except_rule_id:
                keep_clause = "AND id <> %s"
                params = (debox_user_id, except_rule_id)
            cur.execute(
                f"""
                UPDATE watch_rules
                SET run_status = 'paused'
                WHERE debox_user_id = %s
                  AND enabled = 1
                  AND run_status = 'active'
                  {keep_clause}
                """,
                params,
            )
            return cur.rowcount


def list_enabled_watch_rules(limit: int = 200) -> list[dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT wr.*, COALESCE(active_subscription.plan_code, 'free') AS effective_plan_code
                FROM watch_rules wr
                LEFT JOIN LATERAL (
                    SELECT s.plan_code
                    FROM subscriptions s
                    WHERE s.debox_user_id = wr.debox_user_id
                      AND s.status = 'active'
                      AND s.expires_at > NOW()
                    ORDER BY s.expires_at DESC
                    LIMIT 1
                ) active_subscription ON TRUE
                WHERE wr.enabled = 1
                  AND wr.run_status = 'active'
                  AND (
                    active_subscription.plan_code <> 'free'
                    OR (
                      (
                        active_subscription.plan_code = 'free'
                        OR NOT EXISTS (
                          SELECT 1 FROM subscriptions s
                          WHERE s.debox_user_id = wr.debox_user_id
                            AND s.plan_code <> 'free'
                        )
                      )
                      AND EXISTS (
                        SELECT 1
                        FROM user_preferences up
                        WHERE up.debox_user_id = wr.debox_user_id
                          AND up.free_watch_rule_id = wr.id
                      )
                    )
                  )
                ORDER BY wr.last_checked_at NULLS FIRST, wr.id ASC
                LIMIT %s
                """,
                (max(1, min(int(limit), 1000)),),
            )
            return serialize_many(cur.fetchall())


def count_daily_alert_events(debox_user_id: str, timezone_name: str = "Asia/Shanghai") -> int:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT COUNT(*) AS count
                FROM alert_events ae
                JOIN watch_rules wr ON wr.id = ae.watch_rule_id
                WHERE wr.debox_user_id = %s
                  AND (ae.created_at AT TIME ZONE %s)::date = (NOW() AT TIME ZONE %s)::date
                """,
                (debox_user_id, timezone_name, timezone_name),
            )
            return int(cur.fetchone()["count"])


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
    notification_status: str = "pending",
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO alert_events (
                    watch_rule_id, event_type, previous_value, current_value,
                    notification_message_id, notification_status
                )
                VALUES (%s, %s, %s, %s, %s, %s)
                RETURNING *
                """,
                (
                    watch_rule_id,
                    event_type,
                    previous_value,
                    current_value,
                    notification_message_id,
                    notification_status,
                ),
            )
            return serialize(cur.fetchone()) or {}


def update_alert_event_notification(
    event_id: int,
    *,
    status: str,
    message_id: str | None = None,
    error: str = "",
) -> dict:
    if status not in {"sent", "failed"}:
        raise ValueError("通知状态无效。")
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE alert_events
                SET notification_message_id = %s,
                    notification_status = %s,
                    notification_error = %s,
                    notification_attempts = notification_attempts + 1,
                    notification_attempted_at = NOW(),
                    notification_sent_at = CASE WHEN %s = 'sent' THEN NOW() ELSE NULL END
                WHERE id = %s
                RETURNING *
                """,
                (message_id, status, error[:500], status, event_id),
            )
            row = cur.fetchone()
            if row is None:
                raise ValueError("提醒事件不存在。")
            return serialize(row) or {}


def daily_summary_statistics(
    debox_user_id: str,
    period_start: datetime,
    period_end: datetime,
) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                WITH active_rules AS MATERIALIZED (
                    SELECT id, wallet_address, rule_type
                    FROM watch_rules
                    WHERE debox_user_id = %s
                      AND enabled = 1
                      AND run_status = 'active'
                ),
                rule_stats AS (
                    SELECT
                        COUNT(*) AS rule_count,
                        COUNT(DISTINCT LOWER(wallet_address)) AS wallet_count,
                        COUNT(*) FILTER (
                            WHERE rule_type IN (
                                'balance_change', 'incoming', 'outgoing', 'balance_threshold'
                            )
                        ) AS asset_rule_count,
                        COUNT(*) FILTER (WHERE rule_type = 'approval_change') AS approval_rule_count,
                        COUNT(*) FILTER (WHERE rule_type = 'address_interaction') AS interaction_rule_count
                    FROM active_rules
                ),
                event_stats AS (
                    SELECT
                        COUNT(*) AS event_count,
                        COUNT(*) FILTER (
                            WHERE ae.event_type IN (
                                'balance_change', 'incoming', 'outgoing', 'balance_threshold'
                            )
                        ) AS asset_event_count,
                        COUNT(*) FILTER (WHERE ae.event_type = 'approval_change') AS approval_event_count,
                        COUNT(*) FILTER (WHERE ae.event_type = 'address_interaction') AS interaction_event_count,
                        COUNT(*) FILTER (WHERE ae.notification_status = 'failed') AS failed_notification_count
                    FROM alert_events ae
                    JOIN active_rules ar ON ar.id = ae.watch_rule_id
                    WHERE ae.created_at >= %s
                      AND ae.created_at < %s
                )
                SELECT *
                FROM rule_stats
                CROSS JOIN event_stats
                """,
                (debox_user_id, period_start, period_end),
            )
            row = cur.fetchone() or {}
            return {key: int(value or 0) for key, value in row.items()}


def list_summary_recent_events(
    debox_user_id: str,
    period_start: datetime,
    period_end: datetime,
    limit: int = 5,
) -> list[dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT ae.*, wr.chain_key, wr.wallet_address, wr.token_address,
                       wr.rule_type, wr.target_address
                FROM alert_events ae
                JOIN watch_rules wr ON wr.id = ae.watch_rule_id
                WHERE wr.debox_user_id = %s
                  AND wr.enabled = 1
                  AND wr.run_status = 'active'
                  AND ae.created_at >= %s
                  AND ae.created_at < %s
                ORDER BY ae.created_at DESC, ae.id DESC
                LIMIT %s
                """,
                (
                    debox_user_id,
                    period_start,
                    period_end,
                    max(1, min(int(limit), 20)),
                ),
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


def delete_notification_group(group_id: int, debox_user_id: str) -> dict:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT *
                FROM notification_groups
                WHERE id = %s AND debox_user_id = %s
                FOR UPDATE
                """,
                (group_id, debox_user_id),
            )
            group = cur.fetchone()
            if group is None:
                raise ValueError("群通知不存在或已经解绑。")

            cur.execute(
                """
                SELECT id
                FROM subscriptions
                WHERE debox_user_id = %s
                  AND status = 'active'
                  AND expires_at > NOW()
                  AND daily_summary_chat_type = 'group'
                  AND daily_summary_chat_id = %s
                ORDER BY id ASC
                """,
                (debox_user_id, group["gid"]),
            )
            subscription_ids = [int(row["id"]) for row in cur.fetchall()]
            for subscription_id in subscription_ids:
                cur.execute(
                    "SELECT pg_advisory_xact_lock(%s, %s)",
                    (SUMMARY_LOCK_NAMESPACE, subscription_id),
                )

            cur.execute(
                """
                UPDATE notification_groups
                SET enabled = 0
                WHERE id = %s AND debox_user_id = %s
                """,
                (group_id, debox_user_id),
            )
            fallbacks = []
            if subscription_ids:
                cur.execute(
                    """
                    UPDATE subscriptions
                    SET daily_summary_chat_type = 'private',
                        daily_summary_chat_id = %s
                    WHERE id = ANY(%s)
                    RETURNING *
                    """,
                    (debox_user_id, subscription_ids),
                )
                fallbacks = cur.fetchall()
            return {
                "group": serialize(group) or {},
                "summary_fallbacks": serialize_many(fallbacks),
            }


def disable_daily_summaries(subscription_ids: list[int], debox_user_id: str) -> int:
    if not subscription_ids:
        return 0
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE subscriptions
                SET daily_summary_enabled = 0
                WHERE debox_user_id = %s AND id = ANY(%s)
                """,
                (debox_user_id, subscription_ids),
            )
            return cur.rowcount


def update_daily_summary_settings(
    *,
    debox_user_id: str,
    enabled: bool,
    push_time: str,
    timezone_name: str,
    chat_type: str,
    chat_id: str,
    label: str,
    language: str,
) -> dict:
    language = normalize_language(language)
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
                    daily_summary_label = %s,
                    daily_summary_language = %s
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
                    language,
                    debox_user_id,
                ),
            )
            row = cur.fetchone()
            if not row:
                raise ValueError("没有可更新的有效订阅。")
            return serialize(row) or {}


def list_due_scheduled_subscriptions(after_id: int = 0, limit: int = 100) -> list[dict]:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT *
                FROM subscriptions
                WHERE status = 'active'
                  AND expires_at > NOW()
                  AND daily_summary_enabled = 1
                  AND id > %s
                ORDER BY id ASC
                LIMIT %s
                """,
                (max(0, int(after_id)), max(1, min(int(limit), 1000))),
            )
            return serialize_many(cur.fetchall())


def get_scheduled_subscription(subscription_id: int) -> dict | None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT *
                FROM subscriptions
                WHERE id = %s
                  AND status = 'active'
                  AND expires_at > NOW()
                  AND daily_summary_enabled = 1
                LIMIT 1
                """,
                (subscription_id,),
            )
            return serialize(cur.fetchone())


@contextmanager
def scheduled_summary_lock(subscription_id: int) -> Iterator[bool]:
    conn = connect()
    acquired = False
    try:
        with conn.cursor() as cur:
            cur.execute(
                "SELECT pg_try_advisory_lock(%s, %s) AS acquired",
                (SUMMARY_LOCK_NAMESPACE, subscription_id),
            )
            acquired = bool(cur.fetchone()["acquired"])
        yield acquired
    finally:
        if acquired:
            try:
                with conn.cursor() as cur:
                    cur.execute(
                        "SELECT pg_advisory_unlock(%s, %s)",
                        (SUMMARY_LOCK_NAMESPACE, subscription_id),
                    )
            except Exception:
                pass
        conn.close()


def mark_scheduled_push_sent(
    subscription_id: int,
    sent_date: str,
    period_end: datetime,
) -> None:
    with connect() as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                UPDATE subscriptions
                SET daily_summary_last_sent_date = %s,
                    scheduled_push_last_sent_at = NOW(),
                    daily_summary_last_period_end_at = %s
                WHERE id = %s
                """,
                (sent_date, period_end, subscription_id),
            )
