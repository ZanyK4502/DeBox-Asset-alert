# Go production cutover

This runbook switches Railway from the Python compatibility bridge to the Go
runtime without deleting Python or changing the database.

## Release boundary

- The bridge commit keeps `web: python run_all.py`.
- The cutover commit uses the Railpack Go provider and starts `./out`.
- `/api/ready` returns `200` only after the application can reach PostgreSQL.
- Python and Go use the same PostgreSQL advisory lock keys for Bot polling,
  monitoring, payment reconciliation, and per-subscription summaries.
- Database migrations are backward compatible, so the bridge deployment is the
  immediate rollback target.

## Before the maintenance window

1. Confirm Railway `web` and `Postgres` are online.
2. Confirm the production service still points to the expected repository and
   `production` environment.
3. Do not change secrets, payment addresses, Bot credentials, or `DATABASE_URL`.
4. Record the active deployment ID and bridge/cutover commit IDs.
5. Keep the Python source and `requirements.txt`; they are removed only in step 17.

## Release A: compatibility bridge

The currently deployed Python version predates the shared worker locks. The first
bridge release therefore requires a short maintenance stop.

1. Stop the active Railway `web` deployment and confirm the public URL is offline.
2. Push only the bridge commit to the production branch.
3. Wait for Railway to build and for `GET /api/ready` to return `200`.
4. Confirm the deployment is active and the logs show:
   - the Python web process started;
   - the Bot listener acquired its singleton lock;
   - the monitor process acquired the monitor and payment locks.
5. Test `/start`, open the H5, and load the current subscription and rule list.

Do not proceed if the bridge deployment is unhealthy or any production data is
missing.

## Release B: Go runtime

1. Push the cutover commit to the same production branch.
2. Confirm the build uses the Go provider and produces the `out` binary.
3. Wait for Railway to receive `200` from `/api/ready`.
4. Confirm the new deployment becomes active and the bridge deployment stops.
5. Check logs for:
   - `HTTP server starting`;
   - `bot listener started`;
   - successful monitor, payment, and summary cycles;
   - no repeated migration, authentication, or notification errors.
6. Test:
   - `GET /api/health` and `GET /api/ready`;
   - private `/start` and one Bot button;
   - H5 wallet login and current subscription;
   - existing rule list and one harmless balance query;
   - a test notification when an appropriate test rule is available.

## Immediate rollback

1. Use Railway Instant Rollback to reactivate the successful bridge deployment.
2. Wait for `/api/ready` to return `200`.
3. Confirm Python reacquires the shared worker locks.
4. Retest `/start`, H5 login, and existing rules.
5. Revert the cutover configuration commit before the next source deployment.

Do not roll back database migrations. The bridge and Go runtimes use the same
backward-compatible schema.

## Observation

Railway healthchecks protect deployment startup but are not continuous runtime
monitoring. Keep the deployment logs open during the switch and observe the Go
deployment through at least one complete summary cycle before step 17.
