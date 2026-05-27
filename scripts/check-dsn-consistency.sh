#!/usr/bin/env bash
# scripts/check-dsn-consistency.sh
# Verify all 6 services point at the same Postgres database by comparing
# the db_target_hash returned by each service's /healthz?check=db endpoint.
#
# === DB ISOLATION GUARD (2026-04-23) ===
# All service DSNs must point at text2ontology_community (clone), NOT
# lakehouse2ontology (pristine live DB). This script ensures split-brain
# consistency; operator pre-check must also verify the -enterprise DB exists
# (see cutover-parity-checklist.md Check 0).
# Source of truth: feedback_db_isolation.md.
#
# Intended run targets:
#   - CI: after bringing up the 6-service compose stack against a test DB.
#   - Cutover day T-0 00:40: against the live prod compose stack.
#
# Exits 0 if all services share an identical db_target_hash.
# Exits 1 (with DSN DRIFT message) if any service reports a different hash.
# Expected wall time: < 5s.
#
# DERIVED FROM PLAN §3.8 #7 DESCRIPTION + inline bash block (§3.8 #7c)

set -euo pipefail

expected=""
fail=0

for svc in backend-api agent-server recall-server lakehouse-sql-server mcp-tools-server; do
  port=$(case "$svc" in
    backend-api) echo 8090;;
    agent-server) echo 8092;;
    recall-server) echo 8093;;
    lakehouse-sql-server) echo 8094;;
    mcp-tools-server) echo 8095;;
  esac)
  hash=$(curl -sf "http://localhost:${port}/healthz?check=db" | jq -r '.db_target_hash')
  if [[ -z "$hash" || "$hash" == "null" ]]; then
    echo "DSN DRIFT: $svc (port $port) returned empty or null db_target_hash"
    fail=1
    continue
  fi
  if [[ -z "$expected" ]]; then
    expected="$hash"
    echo "ANCHOR: $svc (port $port) db_target_hash=$hash"
  elif [[ "$hash" != "$expected" ]]; then
    echo "DSN DRIFT: $svc (port $port) reports $hash vs expected $expected"
    fail=1
  else
    echo "OK:     $svc (port $port) db_target_hash=$hash"
  fi
done

if (( fail )); then
  echo "DSN CONSISTENCY CHECK FAILED — split-brain DSN detected; abort cutover"
  exit 1
fi
echo "OK: all services share db_target_hash=$expected"
