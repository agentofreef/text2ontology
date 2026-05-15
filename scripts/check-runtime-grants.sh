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

# recall-server owns vector writes.
check recall_server_user        ont_vector_entry INSERT t || fail=1
check agent_server_user         ont_vector_entry INSERT f || fail=1
check backend_api_user          ont_vector_entry INSERT f || fail=1   # v2b REV-2: catch future grant drift

# backend-api owns ontology CRUD.
check backend_api_user          ont_object_type  UPDATE t || fail=1
check agent_server_user         ont_object_type  UPDATE f || fail=1
check lakehouse_sql_server_user ont_object_type  UPDATE f || fail=1

# lakehouse-sql-server owns staging writes (sample: lakehouse_keyword stays ontology, so RO here).
check lakehouse_sql_server_user lakehouse_keyword UPDATE f || fail=1
check backend_api_user          lakehouse_keyword UPDATE t || fail=1

if (( fail )); then
  echo "RUNTIME GRANT CHECK FAILED — see drift above"
  exit 1
fi
echo "OK: runtime GRANTs match expected per-service boundaries"
