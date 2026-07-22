from __future__ import annotations

import unittest
from unittest.mock import patch

from app.subscription_service import entitlement


class SubscriptionExpiryTests(unittest.TestCase):
    @patch("app.subscription_service.count_notification_groups", return_value=0)
    @patch("app.subscription_service.count_user_wallets", return_value=0)
    @patch("app.subscription_service.count_user_watch_rules", return_value=0)
    @patch("app.subscription_service.list_notification_groups", return_value=[])
    @patch("app.subscription_service.pause_user_watch_rules")
    @patch("app.subscription_service.get_user_preferences", return_value={"free_watch_rule_id": None})
    @patch("app.subscription_service.has_paid_subscription_history", return_value=True)
    @patch("app.subscription_service.get_active_subscription", return_value=None)
    @patch(
        "app.subscription_service.list_user_watch_rules",
        return_value=[
            {
                "id": 17,
                "enabled": 1,
                "run_status": "active",
                "rule_type": "balance_change",
                "wallet_address": "0x1111111111111111111111111111111111111111",
                "notification_chat_type": "private",
            }
        ],
    )
    def test_paid_expiry_pauses_but_keeps_the_existing_rule(
        self,
        _mock_rules,
        _mock_subscription,
        _mock_paid_history,
        _mock_preferences,
        mock_pause,
        _mock_groups,
        _mock_rule_count,
        _mock_wallet_count,
        _mock_group_count,
    ) -> None:
        result = entitlement("user-1", create_trial=False)

        self.assertTrue(result["paid_history"])
        self.assertTrue(result["fallback_free"])
        self.assertEqual([rule["id"] for rule in result["rules"]], [17])
        self.assertEqual(result["active_rules"], [])
        self.assertEqual([rule["id"] for rule in result["paused_rules"]], [17])
        mock_pause.assert_called_once_with("user-1", None)


if __name__ == "__main__":
    unittest.main()
