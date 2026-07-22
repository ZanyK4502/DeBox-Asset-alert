from __future__ import annotations

import unittest
from unittest.mock import patch

from eth_account import Account
from eth_account.messages import encode_defunct

from app.auth_service import (
    AuthenticationError,
    debox_user_id_from_profile,
    verify_wallet_challenge,
)
from app import main
from app.main import app, require_authenticated_session
from fastapi.routing import APIRoute


class AuthServiceTests(unittest.TestCase):
    def test_extracts_nested_debox_user_id(self) -> None:
        self.assertEqual(debox_user_id_from_profile({"data": {"userId": "user-123"}}), "user-123")
        self.assertEqual(debox_user_id_from_profile({}), "")

    def test_verifies_signature_and_creates_session(self) -> None:
        account = Account.create()
        message = "DeBox Asset Alert test login"
        signature = Account.sign_message(encode_defunct(text=message), account.key).signature.hex()
        challenge = {
            "challenge_id": "challenge-1",
            "wallet_address": account.address.lower(),
            "message": message,
        }

        with (
            patch("app.auth_service.get_active_auth_challenge", return_value=challenge),
            patch("app.auth_service.user_info", return_value={"user_id": "debox-user"}),
            patch("app.auth_service.consume_auth_challenge", return_value=True),
            patch("app.auth_service.create_auth_session") as create_session,
        ):
            result = verify_wallet_challenge("challenge-1", account.address, signature)

        self.assertEqual(result["debox_user_id"], "debox-user")
        self.assertEqual(result["wallet_address"], account.address.lower())
        self.assertTrue(result["session_token"])
        create_session.assert_called_once()

    def test_rejects_signature_from_another_wallet(self) -> None:
        requested_account = Account.create()
        signing_account = Account.create()
        message = "DeBox Asset Alert test login"
        signature = Account.sign_message(encode_defunct(text=message), signing_account.key).signature.hex()
        challenge = {
            "challenge_id": "challenge-2",
            "wallet_address": requested_account.address.lower(),
            "message": message,
        }

        with patch("app.auth_service.get_active_auth_challenge", return_value=challenge):
            with self.assertRaisesRegex(AuthenticationError, "不一致"):
                verify_wallet_challenge("challenge-2", requested_account.address, signature)

    def test_rejects_already_consumed_challenge(self) -> None:
        account = Account.create()
        message = "DeBox Asset Alert test login"
        signature = Account.sign_message(encode_defunct(text=message), account.key).signature.hex()
        challenge = {
            "challenge_id": "challenge-3",
            "wallet_address": account.address.lower(),
            "message": message,
        }

        with (
            patch("app.auth_service.get_active_auth_challenge", return_value=challenge),
            patch("app.auth_service.user_info", return_value={"user_id": "debox-user"}),
            patch("app.auth_service.consume_auth_challenge", return_value=False),
        ):
            with self.assertRaisesRegex(AuthenticationError, "已使用"):
                verify_wallet_challenge("challenge-3", account.address, signature)

    def test_private_h5_routes_require_server_session(self) -> None:
        protected_paths = {
            "/api/subscription/current",
            "/api/subscription/free-trial",
            "/api/subscription/complimentary",
            "/api/subscription/summary-settings",
            "/api/watch-rules",
            "/api/watch-rules/paused",
            "/api/watch-rules/{rule_id}",
            "/api/watch-rules/{rule_id}/free-monitor",
            "/api/watch-rules/{rule_id}/restore",
            "/api/watch-rules/{rule_id}/notification-language",
            "/api/debox/user",
            "/api/notification-groups",
            "/api/notification-groups/{group_id}",
            "/api/payment/prepare",
            "/api/payment/verify",
        }
        routes = {
            route.path: route
            for route in app.routes
            if isinstance(route, APIRoute) and route.path in protected_paths
        }
        self.assertEqual(set(routes), protected_paths)
        for route in routes.values():
            dependency_calls = {dependency.call for dependency in route.dependant.dependencies}
            self.assertIn(require_authenticated_session, dependency_calls, route.path)


    def test_private_request_models_do_not_accept_a_debox_identity_field(self) -> None:
        models = (
            main.WatchRuleInput,
            main.GroupInput,
            main.PreparePaymentInput,
            main.VerifyPaymentInput,
            main.RuleLanguageInput,
            main.SummarySettingsInput,
        )
        for model in models:
            fields = model.model_fields
            self.assertNotIn("debox_user_id", fields, model.__name__)

    @patch("app.main.entitlement", return_value={})
    @patch("app.main.delete_watch_rule", return_value=True)
    def test_rule_deletion_uses_the_authenticated_user(
        self,
        mock_delete_watch_rule,
        _mock_entitlement,
    ) -> None:
        result = main.remove_watch_rule(42, {"debox_user_id": "session-user"})

        self.assertTrue(result["ok"])
        mock_delete_watch_rule.assert_called_once_with(42, "session-user")


if __name__ == "__main__":
    unittest.main()
