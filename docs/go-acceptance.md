# Go isolated acceptance

Step 15 validates the Go migration against a real local PostgreSQL database while all
external systems remain fake.

## Safety boundary

- `RUN_ACCEPTANCE=1` is required.
- `TEST_DATABASE_URL` is required and must resolve to `localhost` or a loopback IP.
- The helper never reads `DATABASE_URL`.
- Every test creates and drops its own PostgreSQL schema.
- DeBox profiles, Bot messages, Nodit reads, and BNB Chain payment reads are test doubles.
- The full application is not started, so no Bot event consumer or monitor runner is active.

## Run

```powershell
$env:RUN_ACCEPTANCE = "1"
$env:RUN_POSTGRES_INTEGRATION = "1"
$env:TEST_DATABASE_URL = "postgresql://postgres@127.0.0.1:55432/debox_acceptance?sslmode=disable"
go test -count=1 ./...
```

The acceptance suite covers:

- wallet challenge, signature verification, session persistence, and logout;
- rule creation, persisted listing, and free-plan quota rejection;
- professional group binding and summary fallback to private chat after unbinding;
- daily summary event aggregation, fake delivery, and persisted delivery state;
- live-mode USDT order preparation, mocked chain confirmation, and subscription activation.
