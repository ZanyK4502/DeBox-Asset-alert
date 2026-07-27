# Strict cross-chain asset identity verification

This document records the Step 6 backend contract. It does not deploy or
change production data.

## Endpoint

Authenticated clients submit the CoinGecko asset selected by the user and two
to six chain contracts:

```http
POST /api/market/assets/verify-cross-chain
Content-Type: application/json
```

```json
{
  "canonical_asset_id": "project-token",
  "contracts": [
    {
      "chain_key": "bsc",
      "contract_address": "0x..."
    },
    {
      "chain_key": "base",
      "contract_address": "0x..."
    }
  ]
}
```

This is a strict server-side verification, not a confirmation of client-side
preflight data.

## Required proof

Every selected contract must pass all checks:

1. The chain is one of the six supported EVM chains.
2. There is only one selected contract on each chain.
3. The address answers the standard ERC-20 metadata calls through Nodit.
4. CoinGecko resolves the exact chain and contract to the requested coin ID.
5. The returned CoinGecko candidate contains the exact selected deployment.
6. Every selected deployment resolves to that same coin ID.
7. On-chain name or symbol is compatible with the authoritative candidate.

The verifier is fail-closed. A missing mapping, conflicting mapping, malformed
contract, or unavailable authority never becomes a successful cross-chain
identity.

Ordinary catalog search may use the documented stale fallback to keep browsing
available during an outage. Final identity verification uses a separate
authoritative index: a successful CoinGecko snapshot can be reused for six
hours, but the seven-day stale search fallback is never accepted for project
creation.

## Data that never proves identity

The verifier deliberately ignores the following as authority:

- DexScreener search results or pools;
- matching token name or symbol by itself;
- logo, website, social links, liquidity, or trading volume;
- a user confirmation or client-provided `verified` flag.

These values remain useful for display and diagnostics only.

## Evidence output

A successful response contains:

- the canonical CoinGecko asset ID;
- normalized metadata for every selected contract;
- one `canonical_asset_id` evidence record per deployment;
- source `coingecko`, verdict `supports`, and confidence `1.0000`;
- one UTC observation time shared by the verification result and evidence.

The evidence shape maps directly to
`market_asset_identity_evidence` when the later multi-chain project creation
transaction persists the verified asset. The project creation domain guard
requires this evidence for every selected deployment; changing status fields
alone cannot bypass it.

## Public failures

- invalid asset ID, chain, address, item count, or duplicate chain: HTTP 400;
- valid ERC-20 contract without authoritative cross-chain mapping: HTTP 422;
- different CoinGecko IDs or conflicting contract metadata: HTTP 409;
- CoinGecko/catalog unavailable: HTTP 503;
- unreadable or non-ERC-20 contract: HTTP 422.

Provider diagnostics and secrets are never returned to the client. There is no
force-merge mode.
