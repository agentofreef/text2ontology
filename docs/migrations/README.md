# Database migrations

Versioned, ordered, exactly-once schema changes — applied automatically on every
deploy by the `db-migrate` one-shot service (`scripts/run-migrations.sh`).

## Why this exists

Historically the schema was applied **only via Postgres `initdb`**, which runs
*once on an empty data volume*. An existing/upgraded database therefore never
received new schema changes or the per-service least-privilege roles. This
directory + the migration runner close that gap.

## How it works

On every `up`, `scripts/run-migrations.sh` (run by the `db-migrate` service
before any app service starts):

1. Ensures a `schema_migrations(version, applied_at)` tracking table.
2. **Baseline (`0001_baseline`)**:
   - Fresh DB → applies `docs/schema/schema.sql`, records `0001_baseline`.
   - Existing DB → *adopts* the baseline (records it **without** re-running
     `schema.sql`, which contains bare `ADD CONSTRAINT` / seed `INSERT`s that are
     not safe to re-run).
3. Re-applies `ops/db-roles.sql` (idempotent) + sets per-service role passwords
   from `POSTGRES_PASSWORD` — **every run**, so role/grant changes reach existing
   DBs too.
4. Applies every `docs/migrations/NNNN_*.sql` not yet in `schema_migrations`, in
   lexical order, each in its own transaction, recording the version on success.

## Writing a migration

For **any schema change to an existing database**, add a numbered file here —
do not rely on editing `schema.sql` alone (that only affects fresh installs).

```
docs/migrations/0002_add_widget_table.sql
docs/migrations/0003_backfill_widget_owner.sql
```

Rules:
- **Filename**: zero-padded sequence + short description, e.g. `0002_add_x.sql`.
  The numeric prefix defines apply order; never renumber a released file.
- **Idempotency is not required** (each file runs exactly once, tracked) but is
  encouraged for safety. The whole file runs in **one transaction** — a failure
  rolls back and the version is not recorded, so it retries next run.
- **Keep `schema.sql` in sync** for fresh installs: a new fresh DB applies
  `schema.sql` (which should already reflect the change), while existing DBs get
  the same change via the numbered migration. (Both paths converge.)
- **No down-migrations**: roll forward with a new numbered file.

## Running manually

```bash
# against any database (e.g. local/dev), from repo root:
make migrate-up                       # runs scripts/run-migrations.sh vs $DATABASE_URL
# single ad-hoc file (legacy):
make migrate FILE=docs/migrations/0002_add_widget_table.sql
```
