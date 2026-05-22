#!/bin/sh
# scripts/run-migrations.sh
#
# Idempotent database migration runner. Brings ANY database — fresh OR existing —
# up to the current schema + per-service roles, then applies versioned
# incremental migrations exactly once each. This closes the gap left by the old
# initdb-only bootstrap (which ran ONLY on an empty volume, so existing DBs never
# received schema changes or the per-service least-privilege roles).
#
# Run order:
#   1. ensure schema_migrations tracking table
#   2. baseline (0001):
#        - fresh DB (no core tables)  -> apply schema.sql, record 0001
#        - existing DB (core present) -> ADOPT baseline (record 0001, do NOT
#          re-run schema.sql — it has bare ADD CONSTRAINT / seed INSERTs that are
#          not safe to re-run)
#   3. db-roles.sql        — idempotent infra, applied EVERY run (so role/grant
#                            changes propagate to existing DBs)
#   4. role passwords      — idempotent, set from POSTGRES_PASSWORD every run
#   5. versioned migrations — docs/migrations/*.sql, applied once each in lexical
#                            order, each in its own transaction, tracked
#
# Connects as the superuser DSN (DATABASE_URL) — it CREATEs roles and applies
# DDL. Inside the db-migrate container the files live under /migrations.
# Override paths with SCHEMA_FILE / ROLES_FILE / MIGRATIONS_DIR for local use.
set -eu

DSN="${DATABASE_URL:?DATABASE_URL (superuser) is required}"
SCHEMA_FILE="${SCHEMA_FILE:-/migrations/schema.sql}"
ROLES_FILE="${ROLES_FILE:-/migrations/db-roles.sql}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/migrations/versions}"

psql_do() { psql -v ON_ERROR_STOP=1 "$DSN" "$@"; }
psql_q()  { psql -tA -v ON_ERROR_STOP=1 "$DSN" -c "$1"; }

echo "[migrate] ensuring schema_migrations table"
psql_do -c "CREATE TABLE IF NOT EXISTS schema_migrations (
  version    TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);"

# ── 1+2: baseline ─────────────────────────────────────────────────────────────
if [ "$(psql_q "SELECT 1 FROM schema_migrations WHERE version='0001_baseline'")" = "1" ]; then
  echo "[migrate] baseline 0001 already applied"
else
  # app_setting is a stable core table seeded by schema.sql; use it (not the
  # reserved word "user") as the "is this DB already initialized" sentinel.
  if [ "$(psql_q "SELECT to_regclass('public.app_setting') IS NOT NULL")" = "t" ]; then
    echo "[migrate] existing DB detected — ADOPTING baseline (not re-running schema.sql)"
  else
    echo "[migrate] fresh DB — applying baseline schema: $SCHEMA_FILE"
    psql_do -f "$SCHEMA_FILE"
  fi
  psql_do -c "INSERT INTO schema_migrations(version) VALUES ('0001_baseline') ON CONFLICT DO NOTHING;"
fi

# ── 3: per-service roles + grants (idempotent; applied every run) ─────────────
echo "[migrate] applying roles + grants: $ROLES_FILE"
psql_do -f "$ROLES_FILE"

# ── 4: role passwords from POSTGRES_PASSWORD (idempotent; every run) ──────────
if [ -n "${POSTGRES_PASSWORD:-}" ]; then
  echo "[migrate] setting per-service role passwords from POSTGRES_PASSWORD"
  esc=$(printf '%s' "$POSTGRES_PASSWORD" | sed "s/'/''/g")
  for role in backend_api_user agent_server_user recall_server_user \
              lakehouse_sql_server_user mcp_tools_server_user collector_server_user; do
    psql_do -c "ALTER ROLE ${role} PASSWORD '${esc}';"
  done
else
  echo "[migrate] POSTGRES_PASSWORD unset — skipping role-password step"
fi

# ── 5: versioned incremental migrations (apply once each, in order) ──────────
if [ -d "$MIGRATIONS_DIR" ]; then
  for f in $(ls "$MIGRATIONS_DIR"/*.sql 2>/dev/null | sort); do
    v=$(basename "$f" .sql)
    if [ "$(psql_q "SELECT 1 FROM schema_migrations WHERE version='$v'")" = "1" ]; then
      continue
    fi
    echo "[migrate] applying migration $v"
    # -1 wraps the file in a single transaction: a failed migration rolls back
    # fully and is NOT recorded, so it retries cleanly on the next run.
    psql_do -1 -f "$f"
    psql_do -c "INSERT INTO schema_migrations(version) VALUES ('$v');"
  done
fi

echo "[migrate] complete"
