#!/usr/bin/env bash
# scripts/bench/analyst-validators/run-bench.sh
#
# Runs each of the 5 Phase 3 validators against pre-seeded draft entities
# (created by setup.sql + a manual workspace bootstrap) and reports
# observed wall-clock latency.
#
# This is INFORMATIONAL — there is no automated p95 enforcement in MVP.
# Per the consensus plan, the target is p95 < 3000 ms on this fixture.
# v1.1 lifts this into a CI gate.
#
# Required env:
#   DATABASE_URL          — Postgres DSN (sets DB env)
#   LAKEHOUSE_SQL_URL     — defaults to http://127.0.0.1:18094
#   INTERNAL_TOKEN        — internal auth token (matches lakehouse-sql-server)
#   BENCH_DRAFT_OD_ID     — UUID of seeded draft_od (run setup.sql + manual seed)
#   BENCH_DRAFT_PROP_ID   — UUID of seeded draft_property (machine-code)
#   BENCH_DRAFT_LINK_ID   — UUID of seeded draft_link
#   BENCH_DRAFT_INTENT_ID — UUID of seeded draft_intent

set -euo pipefail

LAKEHOUSE_SQL_URL="${LAKEHOUSE_SQL_URL:-http://127.0.0.1:18094}"

if [[ -z "${INTERNAL_TOKEN:-}" ]]; then
  echo "ERROR: INTERNAL_TOKEN is required" >&2
  exit 2
fi

missing=()
for v in BENCH_DRAFT_OD_ID BENCH_DRAFT_PROP_ID BENCH_DRAFT_LINK_ID BENCH_DRAFT_INTENT_ID; do
  [[ -z "${!v:-}" ]] && missing+=("$v")
done
if [[ ${#missing[@]} -gt 0 ]]; then
  echo "ERROR: missing required env: ${missing[*]}" >&2
  echo "Hint: run setup.sql, then create one analyst_workspace + draft_* fixture and export the IDs." >&2
  exit 2
fi

call() {
  local route="$1" idField="$2" idValue="$3"
  local body
  body=$(printf '{"%s":"%s"}' "$idField" "$idValue")
  local start_ms end_ms duration_ms
  start_ms=$(date +%s%3N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1000))')
  local resp
  resp=$(curl -sS -m 30 \
      -H "Content-Type: application/json" \
      -H "X-Internal-Token: ${INTERNAL_TOKEN}" \
      -H "X-On-Behalf-Of: bench" \
      -d "$body" \
      "${LAKEHOUSE_SQL_URL}/internal/validate/${route}" || true)
  end_ms=$(date +%s%3N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1000))')
  duration_ms=$((end_ms - start_ms))
  local status
  status=$(echo "$resp" | grep -oE '"passed"[[:space:]]*:[[:space:]]*(true|false)' | head -1)
  if [[ "$status" == *"true"* ]]; then
    status_label="PASS"
  elif [[ "$status" == *"false"* ]]; then
    status_label="FAIL"
  else
    status_label="ERR"
  fi
  printf "[%-32s] %5d ms  %s\n" "$route" "$duration_ms" "$status_label"
}

echo "── analyst-validator bench (informational) ──"
echo "  LAKEHOUSE_SQL_URL = $LAKEHOUSE_SQL_URL"
echo

call semantic-sql           draftODID       "$BENCH_DRAFT_OD_ID"
call grain                  draftODID       "$BENCH_DRAFT_OD_ID"
call referential-integrity  draftLinkID     "$BENCH_DRAFT_LINK_ID"
call machine-code           draftPropertyID "$BENCH_DRAFT_PROP_ID"
call intent-runs            draftIntentID   "$BENCH_DRAFT_INTENT_ID"

echo
echo "Reminder: target p95 < 3000 ms each. v1.1 turns this into a CI gate."
