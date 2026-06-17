from __future__ import annotations

from decimal import Decimal
from functools import lru_cache

from web3 import Web3

from app.config import settings


ERC20_ABI = [
    {
        "constant": True,
        "inputs": [{"name": "account", "type": "address"}],
        "name": "balanceOf",
        "outputs": [{"name": "", "type": "uint256"}],
        "type": "function",
    },
    {
        "constant": True,
        "inputs": [],
        "name": "decimals",
        "outputs": [{"name": "", "type": "uint8"}],
        "type": "function",
    },
    {
        "constant": True,
        "inputs": [],
        "name": "symbol",
        "outputs": [{"name": "", "type": "string"}],
        "type": "function",
    },
]


@lru_cache
def web3() -> Web3:
    if not settings.chain_rpc_url:
        raise RuntimeError("CHAIN_RPC_URL is required")
    return Web3(Web3.HTTPProvider(settings.chain_rpc_url, request_kwargs={"timeout": 15}))


def validate_address(address: str) -> str:
    if not Web3.is_address(address):
        raise ValueError("Invalid EVM address")
    return Web3.to_checksum_address(address)


def native_balance(address: str) -> dict:
    client = web3()
    checksum = validate_address(address)
    value = Decimal(client.from_wei(client.eth.get_balance(checksum), "ether"))
    return {"value": str(value), "symbol": "BNB", "decimals": 18}


def token_balance(address: str, token_address: str) -> dict:
    client = web3()
    checksum = validate_address(address)
    token_checksum = validate_address(token_address)
    contract = client.eth.contract(address=token_checksum, abi=ERC20_ABI)
    decimals = int(contract.functions.decimals().call())
    symbol = str(contract.functions.symbol().call())
    raw = int(contract.functions.balanceOf(checksum).call())
    value = Decimal(raw) / (Decimal(10) ** decimals)
    return {"value": str(value), "symbol": symbol, "decimals": decimals}


def balance(address: str, token_address: str | None = None) -> dict:
    result = token_balance(address, token_address) if token_address else native_balance(address)
    return {
        **result,
        "chain_id": settings.chain_id,
        "chain_name": settings.chain_name,
        "wallet_address": validate_address(address),
        "token_address": validate_address(token_address) if token_address else None,
    }
