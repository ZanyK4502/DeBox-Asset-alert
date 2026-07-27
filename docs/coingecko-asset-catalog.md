# CoinGecko multi-chain asset catalog

This document records the Step 4 backend contract. It does not enable a
production deployment.

## Identity boundary

CoinGecko is the authoritative source for:

- canonical coin ID, name, symbol, and logo;
- supported-chain contract mappings;
- reverse lookup from a supported chain and contract to one canonical coin.

Only these CoinGecko platform IDs are accepted:

| Product chain | EIP-155 ID | CoinGecko platform |
| --- | ---: | --- |
| BNB Chain | 56 | `binance-smart-chain` |
| Ethereum | 1 | `ethereum` |
| Base | 8453 | `base` |
| Polygon | 137 | `polygon-pos` |
| Arbitrum | 42161 | `arbitrum-one` |
| Optimism | 10 | `optimistic-ethereum` |

DexScreener is a long-tail search fallback only. Every fallback result is
`single_chain`; matching names or symbols from different chains are never
merged into one verified asset.

## Backend endpoints

All three endpoints require an authenticated DeBox session:

- `GET /api/market/assets/search?q=<name-or-symbol>&limit=<1..25>`
- `GET /api/market/assets/resolve?chain=<chain-key>&contract=<address>`
- `GET /api/market/assets/logo?source=<encoded-approved-CDN-URL>`

Search returns a canonical asset ID, local logo-proxy URL, identity source and
status, and all contracts CoinGecko maps to the six supported chains.

The logo endpoint accepts HTTPS URLs only from the explicit CoinGecko and
DexScreener image CDN allowlist. It rejects redirects outside that allowlist,
non-image responses, SVG, and bodies larger than 2 MiB. Successful images are
cached for 24 hours within a 32 MiB process-wide cap and served with an ETag
and `nosniff`.

## Stability controls

- CoinGecko search cache: 10 minutes.
- CoinGecko full identity index cache: 6 hours, stale fallback for 7 days.
- Duplicate requests are coalesced.
- Demo client pacing: approximately 100 requests/minute.
- Pro client pacing: approximately 300 requests/minute.
- HTTP 429 and temporary 5xx responses use bounded retries and `Retry-After`.
- Three consecutive temporary failures open a 30-second circuit.
- CoinGecko failure or an empty CoinGecko result falls back to DexScreener.
- Provider response bodies and API keys are never returned to the H5.

## Railway variables for the production configuration step

Do not place the key in source control or chat.

For a free Demo key:

```text
COINGECKO_API_TIER=demo
COINGECKO_API_KEY=<Railway secret>
```

For any paid CoinGecko API plan:

```text
COINGECKO_API_TIER=pro
COINGECKO_API_KEY=<Railway secret>
```

Leave `COINGECKO_BASE_URL` unset in production. The application selects the
official Demo or Pro API root from the tier. The variable exists only as a
controlled test/emergency override and still rejects non-HTTPS remote URLs.

The application can start without a Demo key and will use CoinGecko's keyless
public API, but that mode is not the production recommendation.

## Minimum plan decision

The Demo allowance is sufficient for development and a small private
acceptance test because the full contract index is shared and cached. For this
commercial, paid-subscription product, the minimum production choice is
CoinGecko Basic: it supplies a paid Pro API key, commercial use terms, 100,000
monthly credits, and a 300 request/minute limit.

The H5 creation wizard must include the required “Data provided by CoinGecko”
attribution and link when Step 10 is implemented.
