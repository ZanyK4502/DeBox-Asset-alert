from __future__ import annotations

import requests

from app.config import settings


class DeBoxOpenAPI:
    def __init__(self) -> None:
        if not settings.debox_bot_api_key:
            raise RuntimeError("DEBOX_BOT_API_KEY is required")
        self.base_url = settings.debox_openapi_base.rstrip("/")
        self.session = requests.Session()
        self.session.headers.update({"X-API-KEY": settings.debox_bot_api_key})

    def get(self, path: str, params: dict) -> dict:
        response = self.session.get(
            f"{self.base_url}{path}",
            params={key: value for key, value in params.items() if value not in {None, ""}},
            timeout=15,
        )
        if response.status_code >= 400:
            raise RuntimeError(f"DeBox OpenAPI error {response.status_code}: {response.text[:300]}")
        payload = response.json()
        if not isinstance(payload, dict):
            raise RuntimeError("Unexpected DeBox OpenAPI response")
        if payload.get("success") is False:
            raise RuntimeError(str(payload.get("message") or payload.get("msg") or payload))
        return payload.get("data") if "data" in payload else payload


def client() -> DeBoxOpenAPI:
    return DeBoxOpenAPI()


def user_info(user_id: str = "", wallet_address: str = "") -> dict:
    params = {}
    if user_id:
        params["user_id"] = user_id
    if wallet_address:
        params["address"] = wallet_address
    if not params:
        raise ValueError("user_id or wallet_address is required")
    return client().get("/openapi/user/info", params)


def token_info(contract_address: str, chain_id: int) -> dict:
    return client().get(
        "/openapi/token/info",
        {"contract_address": contract_address, "chain_id": chain_id},
    )


def group_info(gid: str) -> dict:
    if not gid:
        raise ValueError("gid is required")
    return client().get("/openapi/group/info", {"gid": gid})


def is_group_joined(gid: str, wallet_address: str) -> dict:
    if not gid or not wallet_address:
        raise ValueError("gid and wallet_address are required")
    return client().get(
        "/openapi/group/is_join",
        {"gid": gid, "walletAddress": wallet_address},
    )
