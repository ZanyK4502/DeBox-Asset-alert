# Go Migration Baseline

This document freezes the observable Python application baseline before the Go rewrite.
It contains no credentials and does not change production behavior.

This is an archived migration record. Step 17 removed the Python runtime from
the current source tree after the Go production deployment passed acceptance.
The original baseline remains recoverable from the commit and tag below.

## Source Baseline

- Commit: `102eb504bb2d65ea07d75f88bd4633e826b5a14d`
- Tag: `python-baseline-20260723`
- Migration branch: `go-migration`
- Production branch: `main`
- Production service: Railway `web`
- Production receive mode: DeBox long polling
- Production database: Railway PostgreSQL

## Runtime Topology

The `Procfile` starts `python run_all.py`, which supervises three processes:

1. FastAPI and Uvicorn HTTP service
2. DeBox Bot long-polling listener
3. Monitoring, payment reconciliation, and summary scheduler

The static H5 is served from `static/`. PostgreSQL is the durable source of truth.

The equivalent Go runtime is `cmd/server`. It owns the HTTP server and starts four
context-managed background workers in the same process:

1. DeBox Bot long polling when `DEBOX_BOT_RECEIVE_MODE=polling`
2. Watch-rule monitoring
3. Confirming-payment reconciliation and pending-order expiry
4. Scheduled daily summaries

Each singleton worker uses a PostgreSQL advisory lock, so a second application replica
does not duplicate Bot polling, monitoring, or payment reconciliation. Summary delivery
continues to use its existing per-subscription lock.

The old local `run_tunnel.py` responsibility is available separately as:

```text
go run ./cmd/tunnel
```

It remains a development-only helper, publishes the verified localhost.run address to
`data/public_url.txt`, and is never started by `cmd/server`.

## HTTP Contract

The Python baseline exposes 30 application routes:

```text
GET    /
GET    /api/health
GET    /api/bot/webhook-status
POST   /bot/webhook
GET    /api/plans
GET    /api/chains
POST   /api/auth/challenge
POST   /api/auth/verify
GET    /api/auth/session
POST   /api/auth/logout
GET    /api/subscription/current
POST   /api/subscription/free-trial
POST   /api/subscription/complimentary
POST   /api/subscription/summary-settings
GET    /api/watch-rules
POST   /api/watch-rules
DELETE /api/watch-rules/paused
DELETE /api/watch-rules/{rule_id}
POST   /api/watch-rules/{rule_id}/free-monitor
POST   /api/watch-rules/{rule_id}/restore
PATCH  /api/watch-rules/{rule_id}/notification-language
GET    /api/chain/balance
GET    /api/debox/user
GET    /api/debox/token
GET    /api/notification-groups
POST   /api/notification-groups
DELETE /api/notification-groups/{group_id}
GET    /api/payment/config
POST   /api/payment/prepare
POST   /api/payment/verify
```

The Go implementation must retain route methods, paths, JSON field names, HTTP cookie
behavior, decimal string serialization, and ISO-8601 timestamp serialization unless a
separate contract change is approved.

## Database Contract

The existing PostgreSQL data remains in place. The baseline has nine application tables:

1. `subscriptions`
2. `watch_rules`
3. `orders`
4. `alert_events`
5. `notification_groups`
6. `user_preferences`
7. `auth_challenges`
8. `auth_sessions`
9. `complimentary_grants`

Existing primary keys, foreign keys, partial unique indexes, transaction boundaries, and
PostgreSQL advisory locks are part of the compatibility contract. Migration work must not
drop, rename, truncate, or recreate these tables.

## Product Contract

- Chains: BNB Chain, Ethereum, Base, Polygon, Arbitrum, and Optimism
- Rule types: balance change, incoming, outgoing, balance threshold, approval change,
  and specified-address interaction
- Plans: Free, Standard, and Professional
- Free plan: one wallet, one base rule, five alerts per Asia/Shanghai day
- Payment: BNB Chain USDT, exact ERC-20 transfer validation, three confirmations
- Authentication: EIP-191 wallet signature, DeBox identity lookup, seven-day server session
- Summary: per-user time zone, scheduled cutoff periods, private or eligible group delivery
- Languages: independent H5, Bot menu, rule notification, and summary preferences

## Verification Baseline

The baseline verification commands are:

```text
python -m unittest discover -s tests -v
node --check static/app.js
node --check static/i18n.js
```

Expected result: 54 Python tests pass, both JavaScript syntax checks pass, and `git status`
is clean before migration work begins.

## Cutover Rule

The Go implementation passed contract, database integration, Bot, monitoring,
summary, payment, and H5 acceptance checks before production was switched.
This rule is retained to document the migration boundary.
