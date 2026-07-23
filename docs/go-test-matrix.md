# Go Migration Test Matrix

Steps 14 through 16 froze the Python production behavior and verified the Go
replacement before the runtime switch. Step 17 removed the Python runtime and
its tests. The Go suite is now the executable source of truth; the table below
is retained as the historical behavior mapping used during migration.

## Archived Python Baseline

The 54 Python behavior tests are grouped as follows:

| Area | Python tests | Go coverage |
| --- | ---: | --- |
| Authentication and H5 identity | 7 | `internal/auth/*_test.go`, `internal/httpapi/routes_test.go` |
| Monitoring execution | 6 | `internal/monitor/*_test.go` |
| Payment verification | 10 | `internal/payment/*_test.go`, `internal/httpapi/payment_routes_test.go`, `internal/store/store_test.go` |
| Bot product copy | 4 | `internal/bot/content_test.go` |
| Subscription and expiry | 12 | `internal/subscription/*_test.go`, `internal/store/entitlements_test.go` |
| Daily summaries | 15 | `internal/summary/*_test.go`, `internal/httpapi/management_routes_test.go` |
| **Total** | **54** | Go unit, contract, mock, and integration tests |

Table-driven Go tests may cover multiple Python test cases in one Go test
function. The behavior mapping, rather than the function count, is the
compatibility requirement.

## Contract And Mock Coverage

- `internal/httpapi/compatibility_test.go` freezes all 30 Python route
  method/path pairs and the public plan, rule type, and chain payloads.
- `tests/h5_contract_test.mjs` checks H5 DOM references, required API calls,
  Chinese and English translation keys, and script load order.
- `internal/chain/client_test.go` simulates Nodit balance, allowance,
  transaction, RPC, and error responses.
- `internal/debox/openapi_test.go` and `internal/debox/messenger_test.go`
  simulate DeBox API responses and message delivery.
- `internal/bot/service_test.go` and `internal/bot/runner_test.go` cover private
  and group Bot flows, callbacks, webhook payloads, polling offsets, and the
  singleton listener lock.

## PostgreSQL Integration

PostgreSQL integration tests never read `DATABASE_URL`. They require an
explicit disposable database and create a unique temporary schema for every
test:

```powershell
$env:RUN_POSTGRES_INTEGRATION = "1"
$env:TEST_DATABASE_URL = "postgresql://user:password@host:5432/test_database"
& "C:\Program Files\Go\bin\go.exe" test ./internal/store -run Postgres -v
```

The integration suite verifies the migration schema, concurrent free-plan
quota enforcement, and global payment transaction hash uniqueness. Temporary
schemas are dropped after each test.

## Full Verification

```powershell
& "C:\Program Files\Go\bin\go.exe" test ./...
& "C:\Program Files\Go\bin\go.exe" vet ./...
& "C:\Program Files\Go\bin\go.exe" build ./cmd/...
node --check static/app.js
node --check static/i18n.js
node tests/h5_contract_test.mjs
```

Run the race detector in an environment with CGO and a C compiler:

```powershell
$env:CGO_ENABLED = "1"
& "C:\Program Files\Go\bin\go.exe" test -race ./...
```
