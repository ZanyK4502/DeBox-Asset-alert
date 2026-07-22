from __future__ import annotations

from datetime import timedelta
import hashlib
import secrets

from eth_account import Account
from eth_account.messages import encode_defunct

from app.chain_service import validate_address
from app.db import (
    cleanup_auth_records,
    consume_auth_challenge,
    create_auth_challenge,
    create_auth_session,
    get_active_auth_challenge,
    get_active_auth_session,
    now_utc,
    revoke_auth_session,
)
from app.openapi_service import user_info


AUTH_COOKIE_NAME = "debox_asset_alert_session"
CHALLENGE_TTL = timedelta(minutes=5)
SESSION_TTL = timedelta(days=7)


class AuthenticationError(ValueError):
    pass


class DeBoxIdentityError(AuthenticationError):
    pass


def hash_secret(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def debox_user_id_from_profile(profile: object) -> str:
    if not isinstance(profile, dict):
        return ""
    for key in ("user_id", "userId", "uid", "id"):
        value = profile.get(key)
        if value is not None and str(value).strip():
            return str(value).strip()
    nested = profile.get("data")
    return debox_user_id_from_profile(nested) if isinstance(nested, dict) else ""


def create_wallet_challenge(wallet_address: str, domain: str) -> dict:
    wallet = validate_address(wallet_address)
    challenge_id = secrets.token_urlsafe(24)
    nonce = secrets.token_urlsafe(32)
    issued_at = now_utc()
    expires_at = issued_at + CHALLENGE_TTL
    safe_domain = (domain or "DeBox Asset Alert").strip()[:255]
    message = (
        "DeBox Asset Alert login / 登录\n\n"
        "Sign this message to verify wallet ownership. No transaction or gas fee will occur.\n"
        "签名仅用于确认钱包归属，不会发起交易或产生 Gas 费。\n\n"
        f"Domain: {safe_domain}\n"
        f"Wallet: {wallet}\n"
        f"Nonce: {nonce}\n"
        f"Issued At: {issued_at.isoformat()}\n"
        f"Expiration Time: {expires_at.isoformat()}"
    )
    cleanup_auth_records()
    create_auth_challenge(
        challenge_id=challenge_id,
        wallet_address=wallet,
        nonce_hash=hash_secret(nonce),
        message=message,
        expires_at=expires_at,
    )
    return {
        "challenge_id": challenge_id,
        "wallet_address": wallet,
        "message": message,
        "expires_at": expires_at.isoformat(),
    }


def verify_wallet_challenge(challenge_id: str, wallet_address: str, signature: str) -> dict:
    wallet = validate_address(wallet_address)
    challenge = get_active_auth_challenge((challenge_id or "").strip(), wallet)
    if challenge is None:
        raise AuthenticationError("签名请求已失效，请重新连接钱包。")
    normalized_signature = (signature or "").strip()
    if not normalized_signature.startswith("0x"):
        if len(normalized_signature) == 130 and all(char in "0123456789abcdefABCDEF" for char in normalized_signature):
            normalized_signature = f"0x{normalized_signature}"
        else:
            raise AuthenticationError("钱包签名无效，请重新连接钱包。")
    if len(normalized_signature) != 132:
        raise AuthenticationError("钱包签名无效，请重新连接钱包。")

    try:
        recovered = Account.recover_message(
            encode_defunct(text=str(challenge["message"])),
            signature=normalized_signature,
        )
    except Exception as exc:
        raise AuthenticationError("钱包签名无效，请重新连接钱包。") from exc
    if validate_address(recovered) != wallet:
        raise AuthenticationError("签名钱包与连接钱包不一致。")

    try:
        profile = user_info(wallet_address=wallet)
    except Exception as exc:
        raise AuthenticationError("暂时无法验证 DeBox 账号，请稍后重试。") from exc
    debox_user_id = debox_user_id_from_profile(profile)
    if not debox_user_id:
        raise DeBoxIdentityError("未识别到 DeBox 账号。")
    if not consume_auth_challenge(str(challenge["challenge_id"]), wallet):
        raise AuthenticationError("签名请求已使用，请重新连接钱包。")

    session_token = secrets.token_urlsafe(48)
    expires_at = now_utc() + SESSION_TTL
    create_auth_session(
        token_hash=hash_secret(session_token),
        debox_user_id=debox_user_id,
        wallet_address=wallet,
        expires_at=expires_at,
    )
    return {
        "session_token": session_token,
        "expires_at": expires_at.isoformat(),
        "debox_user_id": debox_user_id,
        "wallet_address": wallet,
        "profile": profile,
    }


def authenticated_session(session_token: str) -> dict | None:
    token = (session_token or "").strip()
    return get_active_auth_session(hash_secret(token)) if token else None


def revoke_session(session_token: str) -> bool:
    token = (session_token or "").strip()
    return revoke_auth_session(hash_secret(token)) if token else False
