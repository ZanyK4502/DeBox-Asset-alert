# Long-tail search and manual contract preflight

This document records the Step 5 backend contract. It does not deploy or
change production data.

## Long-tail name search

`GET /api/market/assets/search?q=<name-or-symbol>&limit=<1..25>` first searches
CoinGecko. When CoinGecko is unavailable or has no matching supported-chain
asset, the same query searches DexScreener.

DexScreener candidates are grouped by chain and contract address, ranked by
exact name, exact symbol, and liquidity, and marked `single_chain`. Equal
names or symbols on different chains are never merged.

## Manual contract preflight

Authenticated clients can submit:

```http
POST /api/market/assets/manual-resolve
Content-Type: application/json
```

```json
{
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

Rules:

- one to six items;
- only BNB Chain, Ethereum, Base, Polygon, Arbitrum, and Optimism;
- one contract per chain in one preflight;
- every value must be a valid EVM address;
- every contract must answer standard ERC-20 `name`, `symbol`, `decimals`, and
  `totalSupply` calls through Nodit;
- pool discovery runs independently for every chain and is non-blocking;
- nothing is written to PostgreSQL.

Each result reports normalized chain and contract data, on-chain token
metadata, canonical asset ID, identity source/status, market lookup status,
and discovered-pool count.

## Merge decision

The preflight response has one of these statuses:

- `single_chain`: one valid contract; it can proceed as a single-chain asset.
- `verified`: two or more contracts all resolve to the same CoinGecko coin ID.
- `requires_separate_projects`: multiple contracts do not share authoritative
  CoinGecko identity evidence.

Names, symbols, decimals, supply, logos, and DexScreener pools are supporting
display data only. They never prove cross-chain identity. Step 6 performs the
final strict identity validation before project creation through
`POST /api/market/assets/verify-cross-chain`.

## Failure behavior

- malformed batch or duplicate chain: HTTP 400;
- unreadable/non-ERC-20 contract: HTTP 422;
- catalog/provider unavailable: HTTP 503;
- a DexScreener outage does not invalidate an on-chain contract; the item
  returns `market_lookup_status=unavailable` and zero pools;
- provider diagnostics and API secrets are not returned to the client.
