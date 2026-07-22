from __future__ import annotations

from decimal import Decimal
from functools import lru_cache
import re
from typing import Any

import requests

from app.config import settings


ADDRESS_RE = re.compile(r"^0x[a-fA-F0-9]{40}$")
TX_HASH_RE = re.compile(r"^0x[a-fA-F0-9]{64}$")
TRANSFER_SELECTOR = "a9059cbb"


SUPPORTED_CHAINS = {
    "bsc": {
        "chain": "bnb",
        "network": "mainnet",
        "chain_id": 56,
        "chain_id_hex": "0x38",
        "name": "BNB Chain",
        "native_symbol": "BNB",
    },
    "ethereum": {
        "chain": "ethereum",
        "network": "mainnet",
        "chain_id": 1,
        "chain_id_hex": "0x1",
        "name": "Ethereum",
        "native_symbol": "ETH",
    },
    "base": {
        "chain": "base",
        "network": "mainnet",
        "chain_id": 8453,
        "chain_id_hex": "0x2105",
        "name": "Base",
        "native_symbol": "ETH",
    },
    "polygon": {
        "chain": "polygon",
        "network": "mainnet",
        "chain_id": 137,
        "chain_id_hex": "0x89",
        "name": "Polygon",
        "native_symbol": "POL",
    },
    "arbitrum": {
        "chain": "arbitrum",
        "network": "mainnet",
        "chain_id": 42161,
        "chain_id_hex": "0xa4b1",
        "name": "Arbitrum",
        "native_symbol": "ETH",
    },
    "optimism": {
        "chain": "optimism",
        "network": "mainnet",
        "chain_id": 10,
        "chain_id_hex": "0xa",
        "name": "Optimism",
        "native_symbol": "ETH",
    },
}


def normalize_chain_key(chain_key: str | None = None) -> str:
    key = (chain_key or settings.chain_key or "bsc").strip().lower()
    aliases = {"bnb": "bsc", "bnbchain": "bsc", "bnb_chain": "bsc"}
    return aliases.get(key, key)


def chain_profile(chain_key: str | None = None) -> dict:
    key = normalize_chain_key(chain_key)
    if key not in SUPPORTED_CHAINS:
        allowed = ", ".join(sorted(SUPPORTED_CHAINS))
        raise ValueError(f"Unsupported chain: {key}. Supported chains: {allowed}")
    return {"key": key, **SUPPORTED_CHAINS[key]}


def supported_chains() -> list[dict]:
    return [
        {
            "key": key,
            "chain_id": value["chain_id"],
            "chain_id_hex": value["chain_id_hex"],
            "name": value["name"],
            "native_symbol": value["native_symbol"],
        }
        for key, value in SUPPORTED_CHAINS.items()
    ]


def validate_address(address: str) -> str:
    value = (address or "").strip()
    if not ADDRESS_RE.fullmatch(value):
        raise ValueError("Invalid EVM address")
    return "0x" + value[2:].lower()


def validate_transaction_hash(tx_hash: str) -> str:
    value = (tx_hash or "").strip()
    if not TX_HASH_RE.fullmatch(value):
        raise ValueError("Invalid transaction hash")
    return "0x" + value[2:].lower()


def format_units(raw: str | int, decimals: int) -> str:
    value = Decimal(str(raw or "0")) / (Decimal(10) ** decimals)
    text = format(value, "f")
    if "." in text:
        text = text.rstrip("0").rstrip(".")
    return text or "0"


def amount_to_units(value: Decimal, decimals: int) -> int:
    units = value * (Decimal(10) ** decimals)
    if units != units.to_integral_value():
        raise ValueError("Payment amount has too many decimal places")
    return int(units)


def encode_erc20_transfer(recipient_address: str, amount_units: int) -> str:
    recipient = validate_address(recipient_address)[2:].rjust(64, "0")
    amount = hex(amount_units)[2:].rjust(64, "0")
    return f"0x{TRANSFER_SELECTOR}{recipient}{amount}"


class NoditClient:
    def __init__(self) -> None:
        if not settings.nodit_api_key:
            raise RuntimeError("NODIT_API_KEY is required")
        self.base_url = settings.nodit_base_url.rstrip("/")
        self.session = requests.Session()
        self.session.headers.update(
            {
                "X-API-KEY": settings.nodit_api_key,
                "Content-Type": "application/json",
            }
        )

    def post(self, profile: dict, path: str, payload: dict) -> dict:
        url = (
            f"{self.base_url}/{profile['chain']}/{profile['network']}/"
            f"{path.lstrip('/')}"
        )
        response = self.session.post(url, json=payload, timeout=20)
        if response.status_code >= 400:
            raise RuntimeError(
                f"Nodit API error {response.status_code}: {response.text[:300]}"
            )
        data = response.json()
        if not isinstance(data, dict):
            raise RuntimeError("Unexpected Nodit API response")
        return data

    def rpc(self, profile: dict, method: str, params: list) -> Any:
        url = f"https://{profile['chain']}-{profile['network']}.nodit.io"
        response = self.session.post(
            url,
            json={"jsonrpc": "2.0", "id": 1, "method": method, "params": params},
            timeout=20,
        )
        if response.status_code >= 400:
            raise RuntimeError(
                f"Nodit Node API error {response.status_code}: {response.text[:300]}"
            )
        data = response.json()
        if not isinstance(data, dict):
            raise RuntimeError("Unexpected Nodit Node API response")
        if data.get("error"):
            error = data["error"]
            raise RuntimeError(str(error.get("message") if isinstance(error, dict) else error))
        return data.get("result")


@lru_cache
def nodit_client() -> NoditClient:
    return NoditClient()


def native_balance(address: str, chain_key: str | None = None) -> dict:
    profile = chain_profile(chain_key)
    wallet = validate_address(address)
    data = nodit_client().post(
        profile,
        "native/getNativeBalanceByAccount",
        {"accountAddress": wallet},
    )
    raw = data.get("balance", "0")
    return {
        "value": format_units(raw, 18),
        "symbol": profile["native_symbol"],
        "decimals": 18,
    }


def _first_token_item(data: dict, token_address: str) -> dict | None:
    expected = validate_address(token_address)
    for item in data.get("items") or []:
        contract = item.get("contract") or {}
        address = (
            contract.get("address")
            or contract.get("contractAddress")
            or item.get("contractAddress")
        )
        if address and validate_address(address) == expected:
            return item
    return None


def token_balance(address: str, token_address: str, chain_key: str | None = None) -> dict:
    profile = chain_profile(chain_key)
    wallet = validate_address(address)
    token = validate_address(token_address)
    data = nodit_client().post(
        profile,
        "token/getTokensOwnedByAccount",
        {
            "accountAddress": wallet,
            "contractAddresses": [token],
            "rpp": 1,
        },
    )
    item = _first_token_item(data, token)
    contract = (item or {}).get("contract") or {}
    decimals = int(
        contract.get("decimals")
        or contract.get("decimal")
        or contract.get("tokenDecimal")
        or 18
    )
    symbol = (
        contract.get("symbol")
        or contract.get("tokenSymbol")
        or contract.get("name")
        or "TOKEN"
    )
    raw = (item or {}).get("balance", "0")
    return {"value": format_units(raw, decimals), "symbol": symbol, "decimals": decimals}


def balance(
    address: str,
    token_address: str | None = None,
    chain_key: str | None = None,
) -> dict:
    profile = chain_profile(chain_key)
    result = (
        token_balance(address, token_address, profile["key"])
        if token_address
        else native_balance(address, profile["key"])
    )
    return {
        **result,
        "chain_key": profile["key"],
        "chain_id": profile["chain_id"],
        "chain_name": profile["name"],
        "wallet_address": validate_address(address),
        "token_address": validate_address(token_address) if token_address else None,
    }


def transaction_by_hash(tx_hash: str, chain_key: str | None = None) -> dict:
    profile = chain_profile(chain_key)
    return nodit_client().post(
        profile,
        "blockchain/getTransactionByHash",
        {"transactionHash": validate_transaction_hash(tx_hash), "withBalanceChanges": True},
    )


def rpc_transaction_by_hash(tx_hash: str, chain_key: str | None = None) -> dict | None:
    profile = chain_profile(chain_key)
    result = nodit_client().rpc(
        profile,
        "eth_getTransactionByHash",
        [validate_transaction_hash(tx_hash)],
    )
    if result is None:
        return None
    if not isinstance(result, dict):
        raise RuntimeError("Unexpected transaction response")
    return result


def transaction_receipt(tx_hash: str, chain_key: str | None = None) -> dict | None:
    profile = chain_profile(chain_key)
    result = nodit_client().rpc(
        profile,
        "eth_getTransactionReceipt",
        [validate_transaction_hash(tx_hash)],
    )
    if result is None:
        return None
    if not isinstance(result, dict):
        raise RuntimeError("Unexpected transaction receipt response")
    return result


def latest_block_number(chain_key: str | None = None) -> int:
    profile = chain_profile(chain_key)
    result = nodit_client().rpc(profile, "eth_blockNumber", [])
    if not isinstance(result, str) or not result.startswith("0x"):
        raise RuntimeError("Unexpected latest block response")
    return int(result, 16)


def _items(payload: Any) -> list:
    if isinstance(payload, list):
        return payload
    if not isinstance(payload, dict):
        return []
    for key in ("items", "transactions", "data", "result"):
        value = payload.get(key)
        if isinstance(value, list):
            return value
        if isinstance(value, dict):
            nested = _items(value)
            if nested:
                return nested
    return []


def _first_value(payload: Any, keys: tuple[str, ...]) -> str | None:
    if isinstance(payload, dict):
        for key in keys:
            value = payload.get(key)
            if value is not None:
                return str(value)
        for value in payload.values():
            found = _first_value(value, keys)
            if found is not None:
                return found
    if isinstance(payload, list):
        for value in payload:
            found = _first_value(value, keys)
            if found is not None:
                return found
    return None


def _contains_address(payload: Any, expected: str) -> bool:
    if isinstance(payload, dict):
        for value in payload.values():
            if _contains_address(value, expected):
                return True
    elif isinstance(payload, list):
        for value in payload:
            if _contains_address(value, expected):
                return True
    elif isinstance(payload, str):
        candidate = payload.strip()
        if ADDRESS_RE.fullmatch(candidate) and validate_address(candidate) == expected:
            return True
        return expected[2:] in candidate.lower()
    return False


def token_allowance(
    owner_address: str,
    token_address: str,
    spender_address: str,
    chain_key: str | None = None,
) -> dict:
    profile = chain_profile(chain_key)
    owner = validate_address(owner_address)
    token = validate_address(token_address)
    spender = validate_address(spender_address)
    data = nodit_client().post(
        profile,
        "token/getTokenAllowance",
        {
            "ownerAddress": owner,
            "contractAddress": token,
            "spenderAddress": spender,
        },
    )
    raw = _first_value(data, ("allowance", "value", "amount", "balance")) or "0"
    decimals = int(_first_value(data, ("decimals", "decimal", "tokenDecimal")) or 18)
    symbol = _first_value(data, ("symbol", "tokenSymbol", "name")) or "TOKEN"
    return {
        "value": format_units(raw, decimals),
        "raw": raw,
        "symbol": symbol,
        "decimals": decimals,
        "chain_key": profile["key"],
        "chain_id": profile["chain_id"],
        "chain_name": profile["name"],
        "wallet_address": owner,
        "token_address": token,
        "spender_address": spender,
    }


def transactions_by_account(address: str, chain_key: str | None = None, rpp: int = 20) -> list[dict]:
    profile = chain_profile(chain_key)
    wallet = validate_address(address)
    data = nodit_client().post(
        profile,
        "blockchain/getTransactionsByAccount",
        {
            "accountAddress": wallet,
            "rpp": max(1, min(int(rpp), 100)),
            "withLogs": True,
            "withDecode": True,
        },
    )
    return [item for item in _items(data) if isinstance(item, dict)]


def latest_interaction(
    address: str,
    target_address: str,
    chain_key: str | None = None,
) -> dict:
    profile = chain_profile(chain_key)
    target = validate_address(target_address)
    wallet = validate_address(address)
    for item in transactions_by_account(wallet, profile["key"], rpp=30):
        if _contains_address(item, target):
            tx_hash = (
                item.get("transactionHash")
                or item.get("hash")
                or item.get("txHash")
                or item.get("id")
                or "matched"
            )
            return {
                "cursor": str(tx_hash),
                "matched": True,
                "transaction": item,
                "chain_key": profile["key"],
                "chain_id": profile["chain_id"],
                "wallet_address": wallet,
                "target_address": target,
            }
    return {
        "cursor": "none",
        "matched": False,
        "transaction": None,
        "chain_key": profile["key"],
        "chain_id": profile["chain_id"],
        "wallet_address": wallet,
        "target_address": target,
    }
