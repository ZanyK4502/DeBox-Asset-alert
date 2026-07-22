import unittest
from decimal import Decimal
from unittest.mock import patch

from app import monitor_service
from app import main


def threshold_rule(last_value: str | None) -> dict:
    return {
        "id": 7,
        "debox_user_id": "user-1",
        "wallet_address": "0x1111111111111111111111111111111111111111",
        "token_address": None,
        "chain_key": "bsc",
        "rule_type": "balance_threshold",
        "threshold": "10",
        "last_value": last_value,
        "notification_chat_id": "user-1",
        "notification_chat_type": "private",
        "notification_language": "zh",
        "effective_plan_code": "standard",
    }


class MonitorThresholdTests(unittest.TestCase):
    @patch("app.monitor_service._record_and_send_rule_alert")
    @patch("app.monitor_service.update_watch_rule_value")
    @patch("app.monitor_service.balance")
    def test_initial_balance_below_threshold_alerts_once(self, mock_balance, _mock_update, mock_alert) -> None:
        mock_balance.return_value = {"value": "5", "symbol": "BNB"}
        mock_alert.return_value = {"id": 1, "notification_status": "sent"}

        result = monitor_service.check_asset_rule(threshold_rule(None))

        self.assertEqual(result["status"], "alerted")
        mock_alert.assert_called_once()
        self.assertIsNone(mock_alert.call_args.args[1])

    @patch("app.monitor_service._record_and_send_rule_alert")
    @patch("app.monitor_service.update_watch_rule_value")
    @patch("app.monitor_service.balance")
    def test_continuing_below_threshold_does_not_repeat(self, mock_balance, _mock_update, mock_alert) -> None:
        mock_balance.return_value = {"value": "4", "symbol": "BNB"}

        result = monitor_service.check_asset_rule(threshold_rule("5"))

        self.assertEqual(result["status"], "no_change")
        mock_alert.assert_not_called()

    def test_recovery_allows_a_later_drop_to_alert(self) -> None:
        threshold = Decimal("10")
        self.assertFalse(monitor_service._should_alert_asset("balance_threshold", Decimal("5"), Decimal("11"), threshold))
        self.assertTrue(monitor_service._should_alert_asset("balance_threshold", Decimal("11"), Decimal("10"), threshold))

    @patch("app.main.check_rule")
    @patch("app.main.entitlement", return_value={})
    @patch("app.main.create_watch_rule")
    @patch("app.main.balance")
    @patch("app.main.notification_target", return_value=("user-1", "private"))
    @patch("app.main.require_rule_creation")
    @patch("app.main.chain_profile", return_value={"key": "bsc", "chain_id": 56})
    def test_new_threshold_rule_runs_an_immediate_check(
        self,
        _mock_profile,
        mock_require_rule,
        _mock_target,
        mock_balance,
        mock_create,
        _mock_entitlement,
        mock_check,
    ) -> None:
        mock_require_rule.return_value = {"code": "standard"}
        mock_balance.return_value = {
            "value": "5",
            "symbol": "BNB",
            "wallet_address": "0x1111111111111111111111111111111111111111",
            "token_address": None,
        }
        mock_create.return_value = {
            **threshold_rule(None),
            "id": 8,
        }
        mock_check.return_value = {"rule_id": 8, "status": "alerted"}
        payload = main.WatchRuleInput(
            wallet_address="0x1111111111111111111111111111111111111111",
            rule_type="balance_threshold",
            threshold="10",
        )

        result = main.post_watch_rule(payload, {"debox_user_id": "user-1"})

        self.assertIsNone(mock_create.call_args.kwargs["last_value"])
        checked_rule = mock_check.call_args.args[0]
        self.assertEqual(checked_rule["effective_plan_code"], "standard")
        self.assertEqual(result["initial_check"]["status"], "alerted")


class MonitorEventDeliveryTests(unittest.TestCase):
    @patch("app.monitor_service.update_alert_event_notification")
    @patch("app.monitor_service._send_rule_alert")
    @patch("app.monitor_service.create_alert_event")
    def test_event_is_created_before_notification_is_sent(self, mock_create, mock_send, mock_update) -> None:
        calls = []
        mock_create.side_effect = lambda **_kwargs: calls.append("create") or {"id": 31}
        mock_send.side_effect = lambda *_args: calls.append("send") or "message-9"
        mock_update.side_effect = lambda *_args, **kwargs: calls.append("update") or {
            "id": 31,
            "notification_status": kwargs["status"],
        }

        event = monitor_service._record_and_send_rule_alert(threshold_rule("11"), "11", "9", "note")

        self.assertEqual(calls, ["create", "send", "update"])
        self.assertEqual(event["notification_status"], "sent")
        mock_update.assert_called_once_with(31, status="sent", message_id="message-9")

    @patch("app.monitor_service.update_alert_event_notification")
    @patch("app.monitor_service._send_rule_alert", side_effect=RuntimeError("DeBox unavailable"))
    @patch("app.monitor_service.create_alert_event", return_value={"id": 32})
    def test_notification_failure_is_recorded(self, _mock_create, _mock_send, mock_update) -> None:
        with self.assertRaisesRegex(RuntimeError, "DeBox unavailable"):
            monitor_service._record_and_send_rule_alert(threshold_rule("11"), "11", "9", "note")

        mock_update.assert_called_once_with(
            32,
            status="failed",
            error="DeBox unavailable",
        )


if __name__ == "__main__":
    unittest.main()
