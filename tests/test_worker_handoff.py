from __future__ import annotations

from unittest import TestCase
from unittest.mock import patch

from fastapi import HTTPException

from app import db
from app import main


class _Cursor:
    def __init__(self, calls: list[tuple[str, tuple[int, ...] | None]]) -> None:
        self.calls = calls

    def __enter__(self) -> "_Cursor":
        return self

    def __exit__(self, *_args: object) -> None:
        return None

    def execute(self, statement: str, params: tuple[int, ...] | None = None) -> None:
        self.calls.append((" ".join(statement.split()), params))


class _Connection:
    def __init__(self) -> None:
        self.calls: list[tuple[str, tuple[int, ...] | None]] = []
        self.closed = False

    def cursor(self) -> _Cursor:
        return _Cursor(self.calls)

    def close(self) -> None:
        self.closed = True


class WorkerHandoffTests(TestCase):
    def test_lock_keys_match_the_go_runtime(self) -> None:
        self.assertEqual(db.MONITOR_EXECUTION_LOCK_KEY, 7_220_026_010)
        self.assertEqual(db.PAYMENT_RECONCILIATION_LOCK_KEY, 7_220_026_011)
        self.assertEqual(db.BOT_POLLING_LOCK_KEY, 7_220_026_012)
        self.assertEqual(db.SUMMARY_LOCK_NAMESPACE, 7_220_026)

    def test_singleton_worker_lock_is_held_for_the_context(self) -> None:
        connection = _Connection()
        with patch("app.db.connect", return_value=connection):
            with db.singleton_worker_lock(db.BOT_POLLING_LOCK_KEY):
                self.assertEqual(
                    connection.calls,
                    [("SELECT pg_advisory_lock(%s)", (db.BOT_POLLING_LOCK_KEY,))],
                )
                self.assertFalse(connection.closed)

        self.assertEqual(
            connection.calls[-1],
            ("SELECT pg_advisory_unlock(%s)", (db.BOT_POLLING_LOCK_KEY,)),
        )
        self.assertTrue(connection.closed)

    @patch("app.main.ping_database")
    def test_ready_checks_database(self, ping_database) -> None:
        self.assertEqual(main.ready(), {"ok": True, "status": "ready"})
        ping_database.assert_called_once_with()

        ping_database.side_effect = RuntimeError("database offline")
        with self.assertRaises(HTTPException) as raised:
            main.ready()
        self.assertEqual(raised.exception.status_code, 503)
