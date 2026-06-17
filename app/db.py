import sqlite3
from pathlib import Path

from app.config import settings


SCHEMA = """
CREATE TABLE IF NOT EXISTS watch_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    debox_user_id TEXT NOT NULL,
    chain_id INTEGER NOT NULL,
    wallet_address TEXT NOT NULL,
    token_address TEXT,
    rule_type TEXT NOT NULL,
    threshold TEXT NOT NULL,
    notification_chat_id TEXT NOT NULL,
    notification_chat_type TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    last_value TEXT,
    last_checked_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    debox_user_id TEXT NOT NULL,
    plan_code TEXT NOT NULL,
    status TEXT NOT NULL,
    starts_at TEXT,
    expires_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    debox_user_id TEXT NOT NULL,
    payer_address TEXT,
    chain_id INTEGER NOT NULL,
    token_address TEXT,
    recipient_address TEXT,
    payment_contract_address TEXT,
    total_amount TEXT NOT NULL,
    plan_code TEXT NOT NULL DEFAULT 'standard',
    tx_hash TEXT,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS alert_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    watch_rule_id INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    previous_value TEXT,
    current_value TEXT,
    notification_message_id TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (watch_rule_id) REFERENCES watch_rules(id)
);
"""


def connect() -> sqlite3.Connection:
    database_path: Path = settings.database_path
    database_path.parent.mkdir(parents=True, exist_ok=True)
    connection = sqlite3.connect(database_path)
    connection.row_factory = sqlite3.Row
    return connection


def initialize_database() -> None:
    with connect() as connection:
        connection.executescript(SCHEMA)
        columns = {
            row["name"]
            for row in connection.execute("PRAGMA table_info(watch_rules)").fetchall()
        }
        if "last_value" not in columns:
            connection.execute("ALTER TABLE watch_rules ADD COLUMN last_value TEXT")
        if "last_checked_at" not in columns:
            connection.execute("ALTER TABLE watch_rules ADD COLUMN last_checked_at TEXT")
        order_columns = {
            row["name"]
            for row in connection.execute("PRAGMA table_info(orders)").fetchall()
        }
        for name in ("payer_address", "recipient_address", "payment_contract_address"):
            if name not in order_columns:
                connection.execute(f"ALTER TABLE orders ADD COLUMN {name} TEXT")
        if "plan_code" not in order_columns:
            connection.execute(
                "ALTER TABLE orders ADD COLUMN plan_code TEXT NOT NULL DEFAULT 'standard'"
            )


def expire_pending_orders(minutes: int = 30, expire_all: bool = False) -> int:
    with connect() as connection:
        if expire_all:
            cursor = connection.execute(
                "UPDATE orders SET status = 'expired' WHERE status = 'pending'"
            )
        else:
            cursor = connection.execute(
                """
                UPDATE orders
                SET status = 'expired'
                WHERE status = 'pending'
                  AND created_at <= datetime(CURRENT_TIMESTAMP, ?)
                """,
                (f"-{minutes} minutes",),
            )
    return cursor.rowcount


def create_watch_rule(
    debox_user_id: str,
    wallet_address: str,
    token_address: str | None,
    rule_type: str,
    threshold: str,
    notification_chat_id: str,
    notification_chat_type: str,
) -> dict:
    with connect() as connection:
        cursor = connection.execute(
            """
            INSERT INTO watch_rules (
                debox_user_id, chain_id, wallet_address, token_address,
                rule_type, threshold, notification_chat_id, notification_chat_type
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                debox_user_id,
                settings.chain_id,
                wallet_address,
                token_address,
                rule_type,
                threshold,
                notification_chat_id,
                notification_chat_type,
            ),
        )
        row = connection.execute(
            "SELECT * FROM watch_rules WHERE id = ?", (cursor.lastrowid,)
        ).fetchone()
    return dict(row)


def count_user_watch_rules(debox_user_id: str) -> int:
    with connect() as connection:
        row = connection.execute(
            "SELECT COUNT(*) AS count FROM watch_rules WHERE debox_user_id = ?",
            (debox_user_id,),
        ).fetchone()
    return int(row["count"])


def list_enabled_watch_rules() -> list[dict]:
    with connect() as connection:
        rows = connection.execute(
            "SELECT * FROM watch_rules WHERE enabled = 1 ORDER BY id"
        ).fetchall()
    return [dict(row) for row in rows]


def update_watch_rule_value(rule_id: int, value: str) -> None:
    with connect() as connection:
        connection.execute(
            """
            UPDATE watch_rules
            SET last_value = ?, last_checked_at = CURRENT_TIMESTAMP
            WHERE id = ?
            """,
            (value, rule_id),
        )


def create_alert_event(
    watch_rule_id: int,
    event_type: str,
    previous_value: str,
    current_value: str,
    notification_message_id: str = "",
) -> None:
    with connect() as connection:
        connection.execute(
            """
            INSERT INTO alert_events (
                watch_rule_id, event_type, previous_value,
                current_value, notification_message_id
            ) VALUES (?, ?, ?, ?, ?)
            """,
            (
                watch_rule_id,
                event_type,
                previous_value,
                current_value,
                notification_message_id,
            ),
        )


def create_order(
    debox_user_id: str,
    payer_address: str,
    token_address: str | None,
    recipient_address: str,
    payment_contract_address: str,
    total_amount: str,
    plan_code: str,
) -> dict:
    with connect() as connection:
        cursor = connection.execute(
            """
            INSERT INTO orders (
                debox_user_id, payer_address, chain_id, token_address,
                recipient_address, payment_contract_address,
                total_amount, plan_code, status
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending')
            """,
            (
                debox_user_id,
                payer_address,
                settings.chain_id,
                token_address,
                recipient_address,
                payment_contract_address,
                total_amount,
                plan_code,
            ),
        )
        row = connection.execute(
            "SELECT * FROM orders WHERE id = ?", (cursor.lastrowid,)
        ).fetchone()
    return dict(row)


def get_order(order_id: int) -> dict | None:
    with connect() as connection:
        row = connection.execute(
            "SELECT * FROM orders WHERE id = ?", (order_id,)
        ).fetchone()
    return dict(row) if row else None


def complete_order(order_id: int, tx_hash: str) -> dict:
    with connect() as connection:
        connection.execute(
            """
            UPDATE orders
            SET tx_hash = ?, status = 'paid'
            WHERE id = ? AND status = 'pending'
            """,
            (tx_hash, order_id),
        )
        row = connection.execute(
            "SELECT * FROM orders WHERE id = ?", (order_id,)
        ).fetchone()
    return dict(row)


def activate_subscription(debox_user_id: str, plan_code: str, days: int) -> dict:
    with connect() as connection:
        connection.execute(
            """
            UPDATE subscriptions
            SET status = 'replaced'
            WHERE debox_user_id = ? AND status = 'active'
            """,
            (debox_user_id,),
        )
        cursor = connection.execute(
            """
            INSERT INTO subscriptions (
                debox_user_id, plan_code, status, starts_at, expires_at
            ) VALUES (
                ?, ?, 'active', CURRENT_TIMESTAMP,
                datetime(CURRENT_TIMESTAMP, ?)
            )
            """,
            (debox_user_id, plan_code, f"+{days} days"),
        )
        row = connection.execute(
            "SELECT * FROM subscriptions WHERE id = ?", (cursor.lastrowid,)
        ).fetchone()
    return dict(row)


def get_active_subscription(debox_user_id: str) -> dict | None:
    with connect() as connection:
        connection.execute(
            """
            UPDATE subscriptions
            SET status = 'expired'
            WHERE debox_user_id = ? AND status = 'active'
              AND expires_at <= CURRENT_TIMESTAMP
            """,
            (debox_user_id,),
        )
        row = connection.execute(
            """
            SELECT * FROM subscriptions
            WHERE debox_user_id = ? AND status = 'active'
              AND expires_at > CURRENT_TIMESTAMP
            ORDER BY id DESC
            LIMIT 1
            """,
            (debox_user_id,),
        ).fetchone()
    return dict(row) if row else None


def has_used_plan(debox_user_id: str, plan_code: str) -> bool:
    with connect() as connection:
        row = connection.execute(
            """
            SELECT 1 FROM subscriptions
            WHERE debox_user_id = ? AND plan_code = ?
            LIMIT 1
            """,
            (debox_user_id, plan_code),
        ).fetchone()
    return row is not None
