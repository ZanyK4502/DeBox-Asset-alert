from __future__ import annotations

import unittest
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

from app import db, main
from app.subscription_service import (
    activate_complimentary_plan,
    complimentary_access,
    entitlement,
)


ALLOWLIST_WALLET = "0xcba3fce9d49ce5d7870443f324a8dd56a5788bfc"
SECOND_ALLOWLIST_WALLET = "0x6613aba1989f7c5a27858a5050f463405cbba486"
OTHER_WALLET = "0x1111111111111111111111111111111111111111"
ALLOWLIST_SETTINGS = SimpleNamespace(
    complimentary_wallet_addresses=f"{ALLOWLIST_WALLET},{SECOND_ALLOWLIST_WALLET}"
)


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


class ComplimentaryAccessTests(unittest.TestCase):
    @patch("app.subscription_service.settings", ALLOWLIST_SETTINGS)
    @patch("app.subscription_service.get_complimentary_grant", return_value=None)
    def test_allowlisted_wallet_has_one_time_access(self, mock_get_grant) -> None:
        result = complimentary_access(ALLOWLIST_WALLET.upper().replace("0X", "0x"))

        self.assertTrue(result["eligible"])
        self.assertTrue(result["available"])
        self.assertFalse(result["used"])
        mock_get_grant.assert_called_once_with(ALLOWLIST_WALLET)

    @patch("app.subscription_service.settings", ALLOWLIST_SETTINGS)
    @patch(
        "app.subscription_service.get_complimentary_grant",
        return_value={"plan_code": "professional", "expires_at": "2026-08-21T00:00:00+00:00"},
    )
    def test_used_allowlist_access_cannot_be_claimed_again(self, _mock_get_grant) -> None:
        result = complimentary_access(ALLOWLIST_WALLET)

        self.assertTrue(result["eligible"])
        self.assertTrue(result["used"])
        self.assertFalse(result["available"])
        self.assertEqual(result["plan_code"], "professional")

    @patch("app.subscription_service.settings", ALLOWLIST_SETTINGS)
    @patch("app.subscription_service.get_complimentary_grant")
    def test_non_allowlisted_wallet_is_rejected_without_database_lookup(self, mock_get_grant) -> None:
        result = complimentary_access(OTHER_WALLET)

        self.assertFalse(result["eligible"])
        self.assertFalse(result["available"])
        mock_get_grant.assert_not_called()

    @patch("app.subscription_service.settings", ALLOWLIST_SETTINGS)
    @patch("app.subscription_service.activate_complimentary_subscription")
    def test_activation_uses_normalized_wallet_and_thirty_days(self, mock_activate) -> None:
        mock_activate.return_value = {"subscription": {"plan_code": "standard"}}

        result = activate_complimentary_plan("user-1", ALLOWLIST_WALLET.upper().replace("0X", "0x"), "standard")

        self.assertEqual(result["subscription"]["plan_code"], "standard")
        mock_activate.assert_called_once_with("user-1", ALLOWLIST_WALLET, "standard", 30)

    @patch("app.subscription_service.settings", ALLOWLIST_SETTINGS)
    @patch("app.subscription_service.activate_complimentary_subscription")
    def test_activation_rejects_non_allowlisted_wallet(self, mock_activate) -> None:
        with self.assertRaisesRegex(ValueError, "白名单"):
            activate_complimentary_plan("user-1", OTHER_WALLET, "professional")

        mock_activate.assert_not_called()

    @patch("app.subscription_service.settings", ALLOWLIST_SETTINGS)
    def test_activation_rejects_free_plan(self) -> None:
        with self.assertRaisesRegex(ValueError, "标准版或专业版"):
            activate_complimentary_plan("user-1", ALLOWLIST_WALLET, "free")

    @patch("app.main.complimentary_access", return_value={"eligible": True, "used": True, "available": False})
    @patch("app.main.entitlement", return_value={"plan": {"code": "standard"}})
    @patch("app.main.activate_complimentary_plan", return_value={"subscription": {"plan_code": "standard"}})
    def test_endpoint_uses_wallet_from_authenticated_session(
        self,
        mock_activate,
        _mock_entitlement,
        _mock_access,
    ) -> None:
        identity = {"debox_user_id": "session-user", "wallet_address": ALLOWLIST_WALLET}

        result = main.activate_complimentary_plan_endpoint(
            main.PreparePaymentInput(plan_code="standard"),
            identity,
        )

        self.assertEqual(result["activation"]["subscription"]["plan_code"], "standard")
        mock_activate.assert_called_once_with("session-user", ALLOWLIST_WALLET, "standard")


class ComplimentaryGrantDatabaseTests(unittest.TestCase):
    @patch("app.db._activate_subscription")
    @patch("app.db.connect")
    def test_grant_is_recorded_in_the_same_transaction(self, mock_connect, mock_activate) -> None:
        connection = MagicMock()
        cursor = MagicMock()
        mock_connect.return_value.__enter__.return_value = connection
        connection.cursor.return_value.__enter__.return_value = cursor
        cursor.fetchone.side_effect = [None, {"wallet_address": ALLOWLIST_WALLET}]
        mock_activate.return_value = {
            "starts_at": "2026-07-22T00:00:00+00:00",
            "expires_at": "2026-08-21T00:00:00+00:00",
            "plan_code": "standard",
        }

        result = db.activate_complimentary_subscription(
            "user-1",
            ALLOWLIST_WALLET,
            "standard",
            30,
        )

        self.assertEqual(result["grant"]["wallet_address"], ALLOWLIST_WALLET)
        mock_activate.assert_called_once_with(
            cursor,
            "user-1",
            "standard",
            30,
            allow_renewal=False,
        )

    @patch("app.db._activate_subscription")
    @patch("app.db.connect")
    def test_existing_grant_blocks_a_second_activation(self, mock_connect, mock_activate) -> None:
        connection = MagicMock()
        cursor = MagicMock()
        mock_connect.return_value.__enter__.return_value = connection
        connection.cursor.return_value.__enter__.return_value = cursor
        cursor.fetchone.return_value = {"exists": 1}

        with self.assertRaisesRegex(ValueError, "已经领取"):
            db.activate_complimentary_subscription(
                "user-1",
                ALLOWLIST_WALLET,
                "professional",
                30,
            )

        mock_activate.assert_not_called()

    def test_complimentary_activation_does_not_extend_an_active_plan(self) -> None:
        cursor = MagicMock()
        cursor.fetchone.return_value = {
            "id": 9,
            "plan_code": "standard",
            "daily_summary_enabled": 0,
        }

        with self.assertRaisesRegex(ValueError, "暂时不能切换套餐"):
            db._activate_subscription(
                cursor,
                "user-1",
                "standard",
                30,
                allow_renewal=False,
            )


if __name__ == "__main__":
    unittest.main()
