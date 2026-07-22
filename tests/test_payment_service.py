from __future__ import annotations

from decimal import Decimal
import unittest
from unittest.mock import patch

from app.chain_service import amount_to_units, encode_erc20_transfer
from app.payment_service import (
    BSC_USDT_ADDRESS,
    REQUIRED_CONFIRMATIONS,
    _decode_erc20_transfer,
    _verify_claimed_order,
    prepare_payment,
    verify_payment,
)


PAYER = "0x1111111111111111111111111111111111111111"
RECIPIENT = "0x2222222222222222222222222222222222222222"
TX_HASH = "0x" + "ab" * 32
OTHER_ADDRESS = "0x3333333333333333333333333333333333333333"


def order_payload() -> dict:
    return {
        "id": 7,
        "debox_user_id": "debox-user",
        "payer_address": PAYER,
        "plan_code": "standard",
        "chain_key": "bsc",
        "chain_id": 56,
        "token_address": BSC_USDT_ADDRESS,
        "token_symbol": "USDT",
        "token_decimals": 18,
        "total_amount": "10",
        "recipient_address": RECIPIENT,
        "tx_hash": TX_HASH,
        "status": "confirming",
    }


def transaction_payload(amount: Decimal = Decimal("10")) -> dict:
    return {
        "hash": TX_HASH,
        "from": PAYER,
        "to": BSC_USDT_ADDRESS,
        "value": "0x0",
        "blockNumber": "0x64",
        "input": encode_erc20_transfer(RECIPIENT, amount_to_units(amount, 18)),
    }


class PaymentServiceTests(unittest.TestCase):
    def test_decodes_exact_erc20_transfer(self) -> None:
        recipient, amount = _decode_erc20_transfer(
            encode_erc20_transfer(RECIPIENT, amount_to_units(Decimal("10"), 18))
        )
        self.assertEqual(recipient, RECIPIENT)
        self.assertEqual(amount, 10 * 10**18)

    def test_keeps_order_confirming_until_three_blocks(self) -> None:
        order = order_payload()
        with (
            patch("app.payment_service.rpc_transaction_by_hash", return_value=transaction_payload()),
            patch(
                "app.payment_service.transaction_receipt",
                return_value={"transactionHash": TX_HASH, "blockNumber": "0x64", "status": "0x1"},
            ),
            patch("app.payment_service.latest_block_number", return_value=101),
            patch(
                "app.payment_service.update_order_verification",
                return_value={**order, "tx_confirmations": 2},
            ) as update_order,
        ):
            result = _verify_claimed_order(order)

        self.assertEqual(result["payment_status"], "confirming")
        self.assertEqual(result["confirmations"], 2)
        update_order.assert_called_once()

    def test_rejects_wrong_usdt_amount(self) -> None:
        order = order_payload()
        with (
            patch(
                "app.payment_service.rpc_transaction_by_hash",
                return_value=transaction_payload(Decimal("9")),
            ),
            patch(
                "app.payment_service.transaction_receipt",
                return_value={"transactionHash": TX_HASH, "blockNumber": "0x64", "status": "0x1"},
            ),
            patch(
                "app.payment_service.update_order_verification",
                return_value={**order, "status": "failed"},
            ),
        ):
            result = _verify_claimed_order(order)

        self.assertEqual(result["payment_status"], "failed")
        self.assertIn("金额", result["error"])

    def test_rejects_order_for_a_different_token(self) -> None:
        order = {**order_payload(), "token_address": OTHER_ADDRESS}
        with patch(
            "app.payment_service.update_order_verification",
            return_value={**order, "status": "failed"},
        ):
            result = _verify_claimed_order(order)

        self.assertEqual(result["payment_status"], "failed")

    def test_rejects_transaction_sent_to_a_different_token_contract(self) -> None:
        order = order_payload()
        transaction = {**transaction_payload(), "to": OTHER_ADDRESS}
        with (
            patch("app.payment_service.rpc_transaction_by_hash", return_value=transaction),
            patch(
                "app.payment_service.transaction_receipt",
                return_value={"transactionHash": TX_HASH, "blockNumber": "0x64", "status": "0x1"},
            ),
            patch(
                "app.payment_service.update_order_verification",
                return_value={**order, "status": "failed"},
            ),
        ):
            result = _verify_claimed_order(order)

        self.assertEqual(result["payment_status"], "failed")

    def test_rejects_payment_to_the_wrong_recipient(self) -> None:
        order = order_payload()
        transaction = {
            **transaction_payload(),
            "input": encode_erc20_transfer(OTHER_ADDRESS, amount_to_units(Decimal("10"), 18)),
        }
        with (
            patch("app.payment_service.rpc_transaction_by_hash", return_value=transaction),
            patch(
                "app.payment_service.transaction_receipt",
                return_value={"transactionHash": TX_HASH, "blockNumber": "0x64", "status": "0x1"},
            ),
            patch(
                "app.payment_service.update_order_verification",
                return_value={**order, "status": "failed"},
            ),
        ):
            result = _verify_claimed_order(order)

        self.assertEqual(result["payment_status"], "failed")

    def test_rejects_failed_chain_transaction(self) -> None:
        order = order_payload()
        with (
            patch("app.payment_service.rpc_transaction_by_hash", return_value=transaction_payload()),
            patch(
                "app.payment_service.transaction_receipt",
                return_value={"transactionHash": TX_HASH, "blockNumber": "0x64", "status": "0x0"},
            ),
            patch(
                "app.payment_service.update_order_verification",
                return_value={**order, "status": "failed"},
            ),
        ):
            result = _verify_claimed_order(order)

        self.assertEqual(result["payment_status"], "failed")

    def test_rejects_a_transaction_hash_already_claimed_by_another_order(self) -> None:
        with patch(
            "app.payment_service.claim_order_transaction",
            side_effect=ValueError("transaction already used"),
        ):
            with self.assertRaisesRegex(ValueError, "already used"):
                verify_payment(7, TX_HASH, "debox-user", PAYER)

    def test_finalizes_after_three_confirmations(self) -> None:
        order = order_payload()
        paid_order = {**order, "status": "paid", "tx_confirmations": REQUIRED_CONFIRMATIONS}
        subscription = {"plan_code": "standard", "status": "active"}
        with (
            patch("app.payment_service.rpc_transaction_by_hash", return_value=transaction_payload()),
            patch(
                "app.payment_service.transaction_receipt",
                return_value={"transactionHash": TX_HASH, "blockNumber": "0x64", "status": "0x1"},
            ),
            patch("app.payment_service.latest_block_number", return_value=102),
            patch(
                "app.payment_service.finalize_paid_order",
                return_value=(paid_order, subscription),
            ) as finalize,
        ):
            result = _verify_claimed_order(order)

        self.assertEqual(result["payment_status"], "paid")
        self.assertEqual(result["confirmations"], 3)
        self.assertEqual(result["subscription"], subscription)
        finalize.assert_called_once()

    def test_blocks_switching_between_active_paid_plans(self) -> None:
        with (
            patch(
                "app.payment_service._require_live_configuration",
                return_value=(
                    {
                        "key": "bsc",
                        "chain_id": 56,
                        "chain_id_hex": "0x38",
                    },
                    RECIPIENT,
                    BSC_USDT_ADDRESS,
                ),
            ),
            patch(
                "app.payment_service.get_active_subscription",
                return_value={"plan_code": "professional"},
            ),
        ):
            with self.assertRaisesRegex(ValueError, "只能续费同一套餐"):
                prepare_payment("debox-user", PAYER, "standard")


if __name__ == "__main__":
    unittest.main()
