#!/usr/bin/env bash
# scripts/check-runtime-grants.sh
# Verify Postgres GRANTs match expected per-service boundaries.
# Intended run targets:
#   - CI: against a fresh staging DB after ops/db-roles.sql applied.
#   - Cutover day T-0 00:42: against live prod DB after ops/db-roles.sql applied.
#
# Exits 0 if all assertions pass. Exits 1 and prints drift on failure.
# Expected wall time: < 2s.

set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set (supply the superuser or owner DSN)}"

# === DB ISOLATION GUARD (2026-04-23) ===
# Refuse to run against the pristine live DB. Enterprise-rebuild schema/grants
# target the -enterprise clone only. Source of truth: feedback_db_isolation.md.
current_db=$(psql "$DATABASE_URL" -tAc "SELECT current_database();" 2>/dev/null | tr -d '[:space:]')
if [[ "$current_db" == "lakehouse2ontology" ]]; then
  echo "REFUSED: connected DB is 'lakehouse2ontology' (pristine live DB)."
  echo "         This script must run against 'lakehouse2ontology-enterprise' clone."
  echo "         Fix: update DATABASE_URL path to /lakehouse2ontology-enterprise."
  exit 2
fi
if [[ -z "$current_db" ]]; then
  echo "REFUSED: could not determine current_database() — aborting."
  exit 2
fi

check() {
  local role="$1"; local tbl="$2"; local priv="$3"; local expect="$4"
  local actual
  # psql `-c` does NOT do `:'var'` substitution (server receives literal colon).
  # Heredoc via stdin DOES process variable substitution client-side before send.
  actual=$(psql "$DATABASE_URL" -v role="$role" -v tbl="$tbl" -v priv="$priv" -tA <<SQL
SELECT has_table_privilege(:'role', :'tbl', :'priv');
SQL
)
  actual="${actual//[[:space:]]/}"
  if [[ "$actual" != "$expect" ]]; then
    echo "DRIFT: $role / $tbl / $priv: expected=$expect got=$actual"
    return 1
  fi
}

fail=0

# P4 enforcement: only agent-server writes thread_state.
check agent_server_user         ont_agent_thread UPDATE t || fail=1
check backend_api_user          ont_agent_thread UPDATE f || fail=1
check recall_server_user        ont_agent_thread UPDATE f || fail=1
check lakehouse_sql_server_user ont_agent_thread UPDATE f || fail=1

# agent-server also owns ont_agent_step writes.
check agent_server_user         ont_agent_step   INSERT t || fail=1
check backend_api_user          ont_agent_step   INSERT f || fail=1

# Vector storage was refactored from a dedicated ont_vector_entry table to
# pgvector COLUMNS on existing tables (ont_property.prop_vector,
# ont_metric.metric_vector, lakehouse_keyword.keyword_vector, etc.), written by
# collector during ontology population; recall-server only READS them. The old
# ont_vector_entry table no longer exists, so its grant assertions are removed.

# backend-api owns ontology CRUD.
check backend_api_user          ont_object_type  UPDATE t || fail=1
check agent_server_user         ont_object_type  UPDATE f || fail=1
check lakehouse_sql_server_user ont_object_type  UPDATE f || fail=1

# lakehouse-sql-server owns staging writes (sample: lakehouse_keyword stays ontology, so RO here).
check lakehouse_sql_server_user lakehouse_keyword UPDATE f || fail=1
check backend_api_user          lakehouse_keyword UPDATE t || fail=1

# === DYNAMIC PER-PROJECT SCHEMA COVERAGE (Finding #4) ===
# The named-table checks above are blind to the runtime proj_<hex> path: collector
# creates per-project schemas, and lakehouse-sql-server reads project data there.
# A throwaway proj_test schema exercises that path: lakehouse_sql_server_user must
# be able to SELECT a granted table inside a proj_ schema, while STILL being unable
# to write ontology tables (reader-of-project-data, not ontology-writer).
#
# NOTE: the spec's ontology-write negative used ont_version, which does not exist in
# this -enterprise clone's schema; ont_object_type is the live ontology table that
# carries the same boundary (already asserted RO for lakehouse_sql_server_user above),
# so the proj_test block reuses it for the cannot-write-ontology assertion.
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q <<'PROJSQL'
DROP SCHEMA IF EXISTS proj_test CASCADE;
CREATE SCHEMA proj_test;
GRANT USAGE ON SCHEMA proj_test TO lakehouse_sql_server_user;
CREATE TABLE proj_test.t1(id int);
GRANT SELECT ON proj_test.t1 TO lakehouse_sql_server_user;
PROJSQL

proj_read=$(psql "$DATABASE_URL" -tA <<'PROJSQL'
SELECT has_table_privilege('lakehouse_sql_server_user', 'proj_test.t1', 'SELECT');
PROJSQL
)
proj_read="${proj_read//[[:space:]]/}"
if [[ "$proj_read" != "t" ]]; then
  echo "DRIFT: lakehouse_sql_server_user / proj_test.t1 / SELECT: expected=t got=$proj_read"
  fail=1
fi

proj_ont_write=$(psql "$DATABASE_URL" -tA <<'PROJSQL'
SELECT has_table_privilege('lakehouse_sql_server_user', 'ont_object_type', 'INSERT');
PROJSQL
)
proj_ont_write="${proj_ont_write//[[:space:]]/}"
if [[ "$proj_ont_write" != "f" ]]; then
  echo "DRIFT: lakehouse_sql_server_user / ont_object_type / INSERT: expected=f got=$proj_ont_write (reader must not write ontology)"
  fail=1
fi

# Always tear down the throwaway schema, even if an assertion above failed.
psql "$DATABASE_URL" -q -c 'DROP SCHEMA IF EXISTS proj_test CASCADE;' >/dev/null

if (( fail )); then
  echo "RUNTIME GRANT CHECK FAILED — see drift above"
  exit 1
fi
echo "OK: runtime GRANTs match expected per-service boundaries"
