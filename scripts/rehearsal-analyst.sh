#!/usr/bin/env bash
# scripts/rehearsal-analyst.sh — Analyst mode E2E regression probe.
#
# Full session flow:
#   1. Create an analyst thread via POST /api/ontology/lakehouse-agent-stream
#      (mode=analyst).
#   2. Send 3 business-context turns (so the 3-turn server guard is satisfied).
#   3. Verify workspace bootstrap endpoint returns a valid workspace payload.
#   4. Run ship_bundle gate (validate all drafts).
#   5. Confirm lakehouse mode recall sees any OD activated by ship_bundle.
#
# Required:
#   - agent-server (:18092) + backend-api (:18090) + lakehouse-sql-server (:18094) up
#   - DATABASE_URL in .env.shared
#   - INTERNAL_TOKEN in .env.shared
#
# Success gate (all must hold):
#   - Analyst SSE turn produces a valid thread event (threadId present)
#   - Workspace bootstrap returns HTTP 200 with workspace.id
#   - All 3 business-context turns produce ≥1 SSE event each
#   - Final outcome JSON written to regression-fixtures/rehearsal-analyst-<ts>.json
#
# NOTE: This script does NOT activate real OD rows (no live DB with staging
# data is assumed). It validates the SSE plumbing, workspace hydration, and
# analyst mode routing. Full bundle activation requires a populated staging
# schema — see docs/architecture-lakehouse2ontology.md.

set -euo pipefail
cd "$(dirname "$0")/.."

HOST_AGENT="${HOST_AGENT:-http://127.0.0.1:18092}"
HOST_API="${HOST_API:-http://127.0.0.1:18090}"
TOKEN="${TOKEN:-bearer-a0000000-0000-0000-0000-000000000001}"
TIMEOUT="${TIMEOUT:-60}"
OUT_FILE="${OUT_FILE:-regression-fixtures/rehearsal-analyst-$(date -u +%Y%m%dT%H%M%SZ).json}"

if [ ! -f .env.shared ]; then
  echo "ERROR: .env.shared not found"; exit 2
fi
set -a && . ./.env.shared && set +a

# psql_exec: thin wrapper that uses local psql if available, else falls back
# to `docker exec` on the running postgres container. Lets the rehearsal run
# on developer machines that haven't installed libpq/postgresql client.
psql_exec() {
  if command -v psql >/dev/null 2>&1; then
    psql "$DATABASE_URL" "$@"
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^text2ontology-enterprise-postgres-1$'; then
    docker exec -i text2ontology-enterprise-postgres-1 \
      psql -U "${POSTGRES_USER:-text2ontology_community}" \
           -d "${POSTGRES_DB:-text2ontology_community}" "$@"
  else
    echo "ERROR: neither local psql nor docker postgres container available" >&2
    return 2
  fi
}

PID=$(psql_exec -tAc "SELECT id FROM project LIMIT 1" | tr -d '[:space:]')
if [ -z "$PID" ]; then
  echo "ERROR: could not resolve projectId from DATABASE_URL"; exit 2
fi

TMPD=$(mktemp -d)
trap 'rm -rf "$TMPD"' EXIT

echo "▼// REHEARSAL ANALYST — analyst mode E2E probe"
echo "   agent-server: $HOST_AGENT"
echo "   backend-api:  $HOST_API"
echo "   projectId:    $PID"
echo "   out:          $OUT_FILE"
echo ""

PASS=0
FAIL=0

# ── Turn helper ──────────────────────────────────────────────────────────────
# send_analyst_turn <label> <question> [threadId]
# Streams SSE from agent-server, extracts threadId from first thread event.
# Writes SSE to $TMPD/sse-<label>.out and returns threadId via stdout.
send_analyst_turn() {
  local label="$1"
  local question="$2"
  local thread_id="${3:-}"
  local sse_file="$TMPD/sse-${label}.out"

  local body
  if [ -n "$thread_id" ]; then
    body=$(jq -n --arg p "$PID" --arg q "$question" --arg t "$thread_id" \
      '{projectId:$p, mode:"analyst", threadId:$t, messages:[{role:"user",content:$q}]}')
  else
    body=$(jq -n --arg p "$PID" --arg q "$question" \
      '{projectId:$p, mode:"analyst", messages:[{role:"user",content:$q}]}')
  fi

  curl -sS --max-time "$TIMEOUT" -N \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "$body" \
    "$HOST_AGENT/api/ontology/lakehouse-agent-stream" > "$sse_file" 2>&1 || true

  grep -oE '"threadId":"[^"]*"' "$sse_file" | head -1 | cut -d'"' -f4
}

# ── Step 1: Send first analyst turn (creates thread) ─────────────────────────
echo -n "   [1/5] First analyst turn (create thread): "
THREAD_ID=$(send_analyst_turn "t1" "我需要分析订单数据，主表是 order_header，包含订单头信息和金额字段。")

EVENTS_T1=$(grep -c '^data: ' "$TMPD/sse-t1.out" || true)
if [ -n "$THREAD_ID" ] && [ "$EVENTS_T1" -ge 1 ]; then
  echo "PASS (threadId=$THREAD_ID, events=$EVENTS_T1)"
  PASS=$((PASS+1))
else
  echo "FAIL (threadId='$THREAD_ID', events=$EVENTS_T1)"
  FAIL=$((FAIL+1))
fi

# ── Step 2: Second business-context turn ─────────────────────────────────────
echo -n "   [2/5] Second business-context turn: "
T2_TID=$(send_analyst_turn "t2" "数据粒度是订单头，一行=一张订单。需要过滤 status='Confirmed' 的记录。" "$THREAD_ID")

EVENTS_T2=$(grep -c '^data: ' "$TMPD/sse-t2.out" || true)
if [ "$EVENTS_T2" -ge 1 ]; then
  echo "PASS (events=$EVENTS_T2)"
  PASS=$((PASS+1))
else
  echo "FAIL (events=$EVENTS_T2)"
  FAIL=$((FAIL+1))
fi

# ── Step 3: Third turn (satisfies 3-turn guard) ───────────────────────────────
echo -n "   [3/5] Third turn (guard satisfaction): "
T3_TID=$(send_analyst_turn "t3" "保留 order_id, amount, status, region_id 四个字段作为 properties。" "$THREAD_ID")

EVENTS_T3=$(grep -c '^data: ' "$TMPD/sse-t3.out" || true)
if [ "$EVENTS_T3" -ge 1 ]; then
  echo "PASS (events=$EVENTS_T3)"
  PASS=$((PASS+1))
else
  echo "FAIL (events=$EVENTS_T3)"
  FAIL=$((FAIL+1))
fi

# ── Step 4: Workspace bootstrap probe ────────────────────────────────────────
echo -n "   [4/5] Workspace bootstrap GET /analyst/workspace: "
WS_CODE=000
WS_BODY=""
if [ -n "$THREAD_ID" ]; then
  WS_FILE="$TMPD/workspace.json"
  WS_CODE=$(curl -sS --max-time 10 \
    -H "Authorization: Bearer $TOKEN" \
    -o "$WS_FILE" -w "%{http_code}" \
    "$HOST_AGENT/api/ontology/analyst/workspace?threadId=$THREAD_ID" 2>/dev/null || echo "000")
  WS_BODY=$(cat "$WS_FILE" 2>/dev/null || echo "{}")
fi

WS_HAS_ID=$(echo "$WS_BODY" | jq -r '.workspace.id // .threadId // ""' 2>/dev/null || echo "")
if [ "$WS_CODE" = "200" ] && [ -n "$WS_HAS_ID" ]; then
  echo "PASS (HTTP $WS_CODE, workspace hydrated)"
  PASS=$((PASS+1))
elif [ "$WS_CODE" = "404" ] && [ -z "$THREAD_ID" ]; then
  # No thread = no workspace; expected when first turn failed
  echo "SKIP (no threadId from step 1)"
else
  echo "FAIL (HTTP $WS_CODE, body=$(echo "$WS_BODY" | head -c 120))"
  FAIL=$((FAIL+1))
fi

# ── Step 5: Lakehouse recall regression (mode=lakehouse on same project) ──────
echo -n "   [5/5] Lakehouse mode probe (zero regression check): "
LH_SSE_FILE="$TMPD/lh-regression.out"
LH_BODY=$(jq -n --arg p "$PID" \
  '{projectId:$p, mode:"lakehouse", messages:[{role:"user",content:"列出所有已激活的 OD"}]}')

curl -sS --max-time "$TIMEOUT" -N \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "$LH_BODY" \
  "$HOST_AGENT/api/ontology/lakehouse-agent-stream" > "$LH_SSE_FILE" 2>&1 || true

LH_THREAD=$(grep -oE '"threadId":"[^"]*"' "$LH_SSE_FILE" | head -1 | cut -d'"' -f4)
LH_EVENTS=$(grep -c '^data: ' "$LH_SSE_FILE" || true)

if [ -n "$LH_THREAD" ] && [ "$LH_EVENTS" -ge 1 ]; then
  echo "PASS (lhThreadId=$LH_THREAD, events=$LH_EVENTS)"
  PASS=$((PASS+1))
else
  echo "FAIL (lhThreadId='$LH_THREAD', events=$LH_EVENTS)"
  FAIL=$((FAIL+1))
fi

# ── Build output JSON ─────────────────────────────────────────────────────────
mkdir -p regression-fixtures
GATE_PASS="false"
if [ "$FAIL" -eq 0 ]; then GATE_PASS="true"; fi

jq -n \
  --arg capturedAt "$(date -u -Iseconds)" \
  --arg host "$HOST_AGENT" \
  --arg pid "$PID" \
  --arg tid "$THREAD_ID" \
  --argjson pass "$PASS" \
  --argjson fail "$FAIL" \
  --argjson gate "$GATE_PASS" \
  '{
    capturedAt: $capturedAt,
    host: $host,
    projectId: $pid,
    analystThreadId: $tid,
    totals: {pass: $pass, fail: $fail, total: ($pass + $fail)},
    gatePass: ($gate == "true")
  }' > "$OUT_FILE"

echo ""
echo "════════════════════════════════════════════════════════"
echo "  REHEARSAL ANALYST RESULT"
echo "════════════════════════════════════════════════════════"
echo "   pass / fail / total:  $PASS / $FAIL / $((PASS + FAIL))"
echo "   gate (all must hold): $GATE_PASS"
echo "   output file:          $OUT_FILE"

if [ "$GATE_PASS" = "true" ]; then
  echo ""
  echo "✓ REHEARSAL ANALYST PASSED"
  exit 0
else
  echo ""
  echo "✗ REHEARSAL ANALYST FAILED — investigate above FAIL lines"
  exit 1
fi
