import unittest
from contextlib import nullcontext
from datetime import datetime, timedelta, timezone
from unittest.mock import MagicMock, patch

from app import db
from app import monitor_service
from app import main


UTC = timezone.utc


def subscription(**overrides) -> dict:
    data = {
        "id": 12,
        "debox_user_id": "user-1",
        "daily_summary_language": "zh",
        "daily_summary_timezone": "Asia/Shanghai",
        "daily_summary_time": "20:00",
        "daily_summary_label": "晚间摘要",
        "daily_summary_last_sent_date": "",
        "daily_summary_last_period_end_at": None,
    }
    data.update(overrides)
    return data


def statistics(event_count: int) -> dict:
    return {
        "rule_count": 3,
        "wallet_count": 2,
        "asset_rule_count": 1,
        "approval_rule_count": 1,
        "interaction_rule_count": 1,
        "event_count": event_count,
        "asset_event_count": event_count,
        "approval_event_count": 0,
        "interaction_event_count": 0,
        "failed_notification_count": 2 if event_count else 0,
    }


def recent_events(count: int) -> list[dict]:
    return [
        {
            "id": index,
            "event_type": "balance_change",
            "wallet_address": "0x1111111111111111111111111111111111111111",
            "previous_value": str(index),
            "current_value": str(index + 1),
        }
        for index in range(min(count, 5))
    ]


class SummaryContentTests(unittest.TestCase):
    def test_event_totals_are_not_limited_by_recent_event_list(self) -> None:
        period_end = datetime(2026, 7, 22, 12, tzinfo=UTC)
        period_start = period_end - timedelta(hours=24)

        for count in (0, 5, 81, 120):
            with self.subTest(event_count=count), patch(
                "app.monitor_service.daily_summary_statistics",
                return_value=statistics(count),
            ), patch(
                "app.monitor_service.list_summary_recent_events",
                return_value=recent_events(count),
            ) as mock_recent:
                text = monitor_service._summary_text(subscription(), period_start, period_end)

                self.assertIn(f"本期触发次数：{count}", text)
                self.assertIn("运行规则数：3", text)
                self.assertIn("监控钱包数：2", text)
                self.assertNotIn("今日触发", text)
                mock_recent.assert_called_once_with(
                    "user-1",
                    period_start,
                    period_end,
                    limit=5,
                )

    @patch("app.monitor_service.list_summary_recent_events", return_value=[])
    @patch("app.monitor_service.daily_summary_statistics", return_value=statistics(81))
    def test_label_failures_and_english_period_wording(self, _mock_stats, _mock_recent) -> None:
        period_end = datetime(2026, 7, 22, 12, tzinfo=UTC)
        period_start = period_end - timedelta(hours=24)
        text = monitor_service._summary_text(
            subscription(
                daily_summary_language="en",
                daily_summary_label="Treasury <Main>",
            ),
            period_start,
            period_end,
        )

        self.assertIn("Daily Summary · Treasury &lt;Main&gt;", text)
        self.assertIn("Alerts this period: 81", text)
        self.assertIn("Notification failures: 2", text)
        self.assertNotIn("today", text.lower())


class SummaryPeriodTests(unittest.TestCase):
    def test_first_summary_uses_previous_24_hours(self) -> None:
        period_end = datetime(2026, 7, 22, 12, tzinfo=UTC)

        period_start, actual_end = monitor_service._summary_period(subscription(), period_end)

        self.assertEqual(period_start, period_end - timedelta(hours=24))
        self.assertEqual(actual_end, period_end)

    def test_next_summary_starts_at_previous_period_end(self) -> None:
        previous_end = datetime(2026, 7, 21, 12, tzinfo=UTC)
        period_end = datetime(2026, 7, 22, 12, tzinfo=UTC)

        period_start, _ = monitor_service._summary_period(
            subscription(daily_summary_last_period_end_at=previous_end.isoformat()),
            period_end,
        )

        self.assertEqual(period_start, previous_end)

    @patch("app.monitor_service.datetime")
    def test_due_cutoff_is_the_scheduled_local_time(self, mock_datetime) -> None:
        mock_datetime.now.return_value = datetime(2026, 7, 22, 12, 5, tzinfo=UTC)

        due, local_date, period_end = monitor_service._summary_due(subscription())

        self.assertTrue(due)
        self.assertEqual(local_date, "2026-07-22")
        self.assertEqual(period_end, datetime(2026, 7, 22, 12, 0, tzinfo=UTC))


    @patch("app.monitor_service.datetime")
    def test_due_cutoff_supports_tokyo_timezone(self, mock_datetime) -> None:
        mock_datetime.now.return_value = datetime(2026, 7, 22, 12, 5, tzinfo=UTC)

        due, local_date, period_end = monitor_service._summary_due(
            subscription(daily_summary_timezone="Asia/Tokyo", daily_summary_time="21:00")
        )

        self.assertTrue(due)
        self.assertEqual(local_date, "2026-07-22")
        self.assertEqual(period_end, datetime(2026, 7, 22, 12, 0, tzinfo=UTC))

    @patch("app.monitor_service.datetime")
    def test_due_cutoff_supports_new_york_timezone(self, mock_datetime) -> None:
        mock_datetime.now.return_value = datetime(2026, 7, 22, 12, 5, tzinfo=UTC)

        due, local_date, period_end = monitor_service._summary_due(
            subscription(daily_summary_timezone="America/New_York", daily_summary_time="08:00")
        )

        self.assertTrue(due)
        self.assertEqual(local_date, "2026-07-22")
        self.assertEqual(period_end, datetime(2026, 7, 22, 12, 0, tzinfo=UTC))


class SummarySchedulingTests(unittest.TestCase):
    @patch("app.monitor_service.mark_scheduled_push_sent")
    @patch("app.monitor_service.send_notification")
    @patch("app.monitor_service._summary_text", return_value="summary")
    @patch("app.monitor_service._summary_period")
    @patch("app.monitor_service._summary_due")
    @patch("app.monitor_service.get_scheduled_subscription")
    @patch("app.monitor_service.scheduled_summary_lock")
    @patch("app.monitor_service.list_due_scheduled_subscriptions")
    def test_scheduler_pages_through_all_enabled_subscriptions(
        self,
        mock_list,
        mock_lock,
        mock_get,
        mock_due,
        mock_period,
        _mock_text,
        mock_send,
        mock_mark,
    ) -> None:
        rows = [
            subscription(
                id=index,
                debox_user_id=f"user-{index}",
                daily_summary_chat_type="private",
            )
            for index in range(1, 206)
        ]
        mock_list.side_effect = lambda after_id, limit: [
            row for row in rows if row["id"] > after_id
        ][:limit]
        mock_lock.side_effect = lambda _subscription_id: nullcontext(True)
        mock_get.side_effect = lambda subscription_id: rows[subscription_id - 1]
        period_end = datetime(2026, 7, 22, 12, tzinfo=UTC)
        mock_due.return_value = (True, "2026-07-22", period_end)
        mock_period.return_value = (period_end - timedelta(hours=24), period_end)

        result = monitor_service.send_due_scheduled_reports(limit=100)

        self.assertEqual(result["sent"], 205)
        self.assertEqual(result["errors"], [])
        self.assertEqual(mock_send.call_count, 205)
        self.assertEqual(mock_mark.call_count, 205)
        self.assertEqual(
            [call.kwargs["after_id"] for call in mock_list.call_args_list],
            [0, 100, 200, 205],
        )

    @patch("app.monitor_service.scheduled_summary_lock", return_value=nullcontext(False))
    @patch("app.monitor_service.list_due_scheduled_subscriptions")
    def test_scheduler_skips_a_subscription_locked_by_another_worker(self, mock_list, _mock_lock) -> None:
        row = subscription(id=1, daily_summary_chat_type="private")
        mock_list.side_effect = [[row], []]

        result = monitor_service.send_due_scheduled_reports()

        self.assertEqual(result["sent"], 0)
        self.assertEqual(result["locked"], 1)

    @patch("app.monitor_service.get_notification_group", return_value=None)
    def test_group_target_is_checked_immediately_before_sending(self, _mock_group) -> None:
        with self.assertRaisesRegex(ValueError, "已解绑或不可用"):
            monitor_service._summary_target(
                subscription(
                    daily_summary_chat_type="group",
                    daily_summary_chat_id="group-1",
                )
            )


    def test_private_summary_always_targets_the_authenticated_debox_user(self) -> None:
        target = monitor_service._summary_target(
            subscription(
                daily_summary_chat_type="private",
                daily_summary_chat_id="untrusted-client-value",
            )
        )

        self.assertEqual(target, ("user-1", "private"))


class SummaryGroupFallbackTests(unittest.TestCase):
    def fallback_deletion(self) -> dict:
        return {
            "group": {"id": 9, "gid": "group-1"},
            "summary_fallbacks": [
                subscription(
                    id=22,
                    daily_summary_enabled=1,
                    daily_summary_chat_type="private",
                    daily_summary_chat_id="user-1",
                )
            ],
        }

    @patch("app.main.entitlement", return_value={})
    @patch("app.main.send_notification", return_value="message-1")
    @patch("app.main.delete_notification_group")
    def test_group_unbind_keeps_summary_enabled_after_private_confirmation(
        self,
        mock_delete,
        mock_send,
        _mock_entitlement,
    ) -> None:
        mock_delete.return_value = self.fallback_deletion()

        result = main.delete_group(9, {"debox_user_id": "user-1"})

        self.assertTrue(result["summary_target_changed"])
        self.assertTrue(result["summary_confirmation_sent"])
        self.assertFalse(result["summary_disabled"])
        mock_send.assert_called_once()

    @patch("app.main.entitlement", return_value={})
    @patch("app.main.disable_daily_summaries")
    @patch("app.main.send_notification", side_effect=RuntimeError("private blocked"))
    @patch("app.main.delete_notification_group")
    def test_group_unbind_disables_summary_when_private_confirmation_fails(
        self,
        mock_delete,
        _mock_send,
        mock_disable,
        _mock_entitlement,
    ) -> None:
        mock_delete.return_value = self.fallback_deletion()

        result = main.delete_group(9, {"debox_user_id": "user-1"})

        self.assertTrue(result["summary_disabled"])
        mock_disable.assert_called_once_with([22], "user-1")


class NewSubscriptionSummaryDefaultsTests(unittest.TestCase):
    def test_new_paid_subscription_starts_with_summary_disabled(self) -> None:
        cursor = MagicMock()
        cursor.fetchone.side_effect = [
            None,
            {"id": 31, "plan_code": "standard", "daily_summary_enabled": 0},
        ]

        result = db._activate_subscription(cursor, "user-1", "standard", 30)

        insert_params = cursor.execute.call_args_list[-1].args[1]
        self.assertEqual(insert_params[4], 0)
        self.assertEqual(result["daily_summary_enabled"], 0)

    def test_same_plan_renewal_preserves_existing_summary_setting(self) -> None:
        cursor = MagicMock()
        cursor.fetchone.side_effect = [
            {"id": 31, "plan_code": "standard", "daily_summary_enabled": 1},
            {"id": 31, "plan_code": "standard", "daily_summary_enabled": 1},
        ]

        result = db._activate_subscription(cursor, "user-1", "standard", 30)

        self.assertEqual(result["daily_summary_enabled"], 1)
        self.assertIn("SET expires_at = expires_at", cursor.execute.call_args_list[-1].args[0])


if __name__ == "__main__":
    unittest.main()
