import unittest

from app import bot_service


class BotProductCopyTests(unittest.TestCase):
    def test_secure_sign_in_copy_is_available_in_both_languages(self) -> None:
        chinese = bot_service.menu_text("zh")
        english = bot_service.menu_text("en")

        self.assertIn("不会发起交易或消耗 Gas", chinese)
        self.assertIn("sends no transaction and uses no gas", english)

    def test_monitoring_copy_explains_threshold_and_summary_behavior(self) -> None:
        chinese = bot_service.features_text("zh")
        english = bot_service.features_text("en")

        self.assertIn("创建规则时余额已达到或低于阈值会立即提醒一次", chinese)
        self.assertIn("首次统计此前 24 小时", chinese)
        self.assertIn("通知失败次数", chinese)
        self.assertIn("私聊确认失败", chinese)
        self.assertIn("already at or below the threshold when created", english)
        self.assertIn("previous 24 hours", english)
        self.assertIn("notification failures", english)
        self.assertIn("private confirmation fails", english)

    def test_plan_copy_explains_payment_and_switching_rules(self) -> None:
        chinese = bot_service.plans_text("zh")
        english = bot_service.plans_text("en")

        self.assertIn("套餐到期后才能选择其他套餐", chinese)
        self.assertIn("3 个区块确认", chinese)
        self.assertIn("不支持退款", chinese)
        self.assertIn("choose another plan after it expires", english)
        self.assertIn("3 block confirmations", english)
        self.assertIn("non-refundable", english)


if __name__ == "__main__":
    unittest.main()
