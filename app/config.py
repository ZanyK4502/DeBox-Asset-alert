from dataclasses import dataclass
from decimal import Decimal
import os
from pathlib import Path

from dotenv import load_dotenv


ROOT_DIR = Path(__file__).resolve().parent.parent
load_dotenv(ROOT_DIR / ".env", override=True)


def env(name: str, default: str = "") -> str:
    return os.getenv(name, default).strip()


@dataclass(frozen=True)
class Settings:
    app_name: str = env("APP_NAME", "DeBox Asset Alert")
    app_env: str = env("APP_ENV", "development")
    app_host: str = env("APP_HOST", "0.0.0.0")
    app_port: int = int(env("APP_PORT", env("PORT", "8000")))
    public_app_url: str = env("PUBLIC_APP_URL")
    database_url: str = env("DATABASE_URL")

    debox_bot_api_key: str = env("DEBOX_BOT_API_KEY")
    debox_bot_api_secret: str = env("DEBOX_BOT_API_SECRET")
    debox_bot_receive_mode: str = env("DEBOX_BOT_RECEIVE_MODE", "polling").lower()
    debox_webhook_key: str = env("DEBOX_WEBHOOK_KEY")
    debox_notification_chat_id: str = env("DEBOX_NOTIFICATION_CHAT_ID")
    debox_notification_chat_type: str = env("DEBOX_NOTIFICATION_CHAT_TYPE", "private")

    chain_id: int = int(env("CHAIN_ID", "56"))
    chain_name: str = env("CHAIN_NAME", "BSC")
    chain_rpc_url: str = env("CHAIN_RPC_URL")

    subscription_token_address: str = env("SUBSCRIPTION_TOKEN_ADDRESS")
    subscription_token_symbol: str = env("SUBSCRIPTION_TOKEN_SYMBOL", "USDT")
    subscription_price: Decimal = Decimal(env("SUBSCRIPTION_PRICE", "10"))
    subscription_days: int = int(env("SUBSCRIPTION_DAYS", "30"))
    payment_recipient_address: str = env("PAYMENT_RECIPIENT_ADDRESS")
    payment_mode: str = env("PAYMENT_MODE", "preview").lower()


settings = Settings()
