# Multi-chain market collection

The market collector runs one isolated service for each configured EVM chain:

- BNB Chain (`bsc`, chain ID 56)
- Ethereum (`ethereum`, chain ID 1)
- Base (`base`, chain ID 8453)
- Polygon (`polygon`, chain ID 137)
- Arbitrum (`arbitrum`, chain ID 42161)
- Optimism (`optimism`, chain ID 10)

Each service has its own scanner cursor, confirmation depth, advisory task
locks, webhook inbox claims, health records and reorganization recovery. A
provider failure on one chain does not stop the other chain workers.

## Runtime configuration

`MARKET_CHAIN_KEYS` is a comma-separated allowlist. When omitted, all six
supported chains are enabled once `MARKET_COLLECTOR_ENABLED=true`.

`MARKET_CONFIRMATION_DEPTH` remains the common fallback.
`MARKET_CONFIRMATION_DEPTHS_JSON` can override individual chains:

```text
{"ethereum":20,"polygon":128}
```

New webhook callbacks are chain-scoped:

```text
/api/market/webhook/{chain_key}/{category}
```

The historical `/api/market/webhook/{category}` route remains pinned to BNB
Chain so existing production subscriptions continue to work during migration.
Non-BNB subscriptions must use chain-scoped entries in
`NODIT_WEBHOOK_SIGNING_KEYS_JSON`, for example `base:transfer`. Unscoped entries
remain BNB-only and are never reused across chains.

The application never logs or persists webhook signing keys. Webhook callback
repair updates the remote URL and stores only its hash in PostgreSQL.

## Recovery and deduplication

Webhook inbox rows include `chain_key` and `chain_id`. Claiming stale or pending
messages is filtered by chain, and the worker rejects any message whose chain
does not match its own service. Existing BNB dedupe keys are retained for
rollout compatibility; other chains include the chain key in their dedupe
domain.

Migration `0012_multichain_market_collection.sql` backfills existing inbox rows
from their registered subscription and leaves unbound legacy rows on BNB Chain.
