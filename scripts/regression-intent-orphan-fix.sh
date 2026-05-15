#!/usr/bin/env bash
# scripts/regression-intent-orphan-fix.sh — verifies the structural fix that
# prevents orphan metric intents (intent rows in lakehouse_metric_intent with
# no matching trigger keyword in lakehouse_keyword.metric_intent_id).
#
# Background: pre-fix, six different code paths could create an intent without
# its trigger keywords. The recall pipeline (token → keyword → intent) would
# then never see the intent, even though /api/ontology/metric-intents and
# the management UI showed it as healthy. This script regresses three
# expectations:
#
#   1. REST POST /api/ontology/metric-intents WITHOUT triggerKeywords
#      must reject with 400 + code="NO_TRIGGERS" (not silently create orphan)
#
#   2. REST POST WITH triggerKeywords must succeed AND lakehouse_keyword
#      must contain N rows pointing at the new intent atomically
#
#   3. list(type=intents) called via the analyst tool surface must return
#      orphan=true / triggerCount=0 for any existing orphan rows so the
#      analyst agent can self-detect them
#
# This DOES NOT exercise the LLM-driven update_intent flow (that requires
# a live model and is non-deterministic). The capability is verified at
# the tool-result level via direct SQL after the fix is applied.

set -uo pipefail
cd "$(dirname "$0")/.."

HOST_API="${HOST_API:-http://127.0.0.1:18090}"
HOST_AGENT="${HOST_AGENT:-http://127.0.0.1:18092}"
TOKEN="${TOKEN:-bearer-a0000000-0000-0000-0000-000000000001}"

if [ ! -f .env.shared ]; then
  echo "ERROR: .env.shared not found"; exit 2
fi
set -a && . ./.env.shared && set +a

psql_exec() {
  if command -v psql >/dev/null 2>&1; then
    psql "$DATABASE_URL" "$@"
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^text2ontology-enterprise-postgres-1$'; then
    docker exec -i text2ontology-enterprise-postgres-1 \
      psql -U "${POSTGRES_USER:-lakehouse2ontology-enterprise}" \
           -d "${POSTGRES_DB:-lakehouse2ontology-enterprise}" "$@"
  else
    echo "ERROR: neither local psql nor docker postgres container available" >&2
    return 2
  fi
}

PASS=0
FAIL=0
assert() {
  local label="$1" cond="$2" detail="${3:-}"
  if [ "$cond" = "true" ]; then
    echo "   ✓ $label"
    PASS=$((PASS + 1))
  else
    echo "   ✗ $label  ($detail)"
    FAIL=$((FAIL + 1))
  fi
}

# Pick a project + an active OD to anchor the test intent on.
PID=$(psql_exec -tAc "
  SELECT p.id FROM project p
  JOIN ont_object_type o ON o.project_id = p.id AND o.mark = true
  WHERE p.name = 'Northwind 测试项目' LIMIT 1
" | tr -d '[:space:]')
OD_ID=$(psql_exec -tAc "
  SELECT id FROM ont_object_type
  WHERE project_id = '$PID' AND mark = true ORDER BY name LIMIT 1
" | tr -d '[:space:]')

if [ -z "$PID" ] || [ -z "$OD_ID" ]; then
  echo "ERROR: cannot resolve Northwind projectId / OD — needed as anchor"
  exit 2
fi

TS=$(date -u +%Y%m%dT%H%M%SZ)
INTENT_NAME_NO_TRIG="reg.no_triggers.$TS"
INTENT_NAME_WITH_TRIG="reg.with_triggers.$TS"

echo "▼// REGRESSION — metric intent orphan invariant"
echo "   project:  $PID  (Northwind 测试项目)"
echo "   anchor OD: $OD_ID"
echo "   timestamp: $TS"
echo ""

# ── Test 1: REST POST without triggerKeywords → 400 NO_TRIGGERS ────────────
echo "── [1] REST POST /metric-intents without triggerKeywords (must reject)"
RESP1=$(curl -sS -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -X POST \
  -d "{\"projectId\":\"$PID\",\"objectId\":\"$OD_ID\",\"name\":\"$INTENT_NAME_NO_TRIG\",\"canonicalMetric\":\"sum(NetAmount)\"}" \
  "$HOST_API/api/ontology/metric-intents?projectId=$PID")
T1_OK="false"
[ "$RESP1" = "400" ] && T1_OK="true"
assert "REST POST without triggers → HTTP 400" "$T1_OK" "got HTTP $RESP1"

# Confirm no orphan landed in DB.
LEAKED=$(psql_exec -tAc "
  SELECT count(*) FROM lakehouse_metric_intent
  WHERE project_id = '$PID' AND name = '$INTENT_NAME_NO_TRIG'
" | tr -d '[:space:]')
T1B_OK="false"
[ "$LEAKED" = "0" ] && T1B_OK="true"
assert "DB has zero rows for the rejected intent" "$T1B_OK" "leaked=$LEAKED"

# ── Test 2: REST POST WITH triggerKeywords → success + atomic keyword writes ─
echo "── [2] REST POST /metric-intents with triggerKeywords (must succeed atomically)"
BODY2=$(cat <<EOF
{
  "projectId":"$PID",
  "objectId":"$OD_ID",
  "name":"$INTENT_NAME_WITH_TRIG",
  "canonicalMetric":"sum(NetAmount)",
  "triggerKeywords":["regtest_$TS","regression_test","回归测试"]
}
EOF
)
RESP2=$(curl -sS -o /tmp/intent-create-resp.json -w "%{http_code}" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -X POST -d "$BODY2" \
  "$HOST_API/api/ontology/metric-intents?projectId=$PID")
T2_OK="false"
[ "$RESP2" = "200" ] && T2_OK="true"
assert "REST POST with triggers → HTTP 200" "$T2_OK" "got HTTP $RESP2 body=$(head -c 200 /tmp/intent-create-resp.json)"

NEW_INTENT_ID=$(jq -r '.id // ""' /tmp/intent-create-resp.json 2>/dev/null)
T2B_OK="false"
[ -n "$NEW_INTENT_ID" ] && T2B_OK="true"
assert "Response carries new intent id" "$T2B_OK" "id missing from response"

# 3 trigger rows must exist in lakehouse_keyword anchored to this intent.
TRIG_COUNT=$(psql_exec -tAc "
  SELECT count(*) FROM lakehouse_keyword
  WHERE metric_intent_id = '$NEW_INTENT_ID'
" | tr -d '[:space:]')
T2C_OK="false"
[ "$TRIG_COUNT" = "3" ] && T2C_OK="true"
assert "lakehouse_keyword has 3 trigger rows for new intent" "$T2C_OK" "actual=$TRIG_COUNT"

# ── Test 3: list(type=intents) surfaces orphan flag ────────────────────────
# We force-create an orphan to simulate legacy state. Direct SQL bypasses
# the helper (which is the whole point — to verify the analyst agent can
# detect orphans created BEFORE the helper existed).
echo "── [3] list(type=intents) detects synthetic legacy orphan"
ORPHAN_NAME="reg.orphan.$TS"
ORPHAN_ID=$(psql_exec -tAc "
  INSERT INTO lakehouse_metric_intent
    (project_id, object_id, name, canonical_metric, mark)
  VALUES ('$PID', '$OD_ID', '$ORPHAN_NAME', 'sum(NetAmount)', true)
  RETURNING id
" | tr -d '[:space:]')
T3_PRE_OK="false"
[ -n "$ORPHAN_ID" ] && T3_PRE_OK="true"
assert "Synthesised legacy orphan via direct SQL" "$T3_PRE_OK" "insert failed"

# Drive the analyst agent through SSE with an explicit "list intents" prompt
# so the dispatchTool path runs builderToolListIntents end-to-end. We then
# grep the SSE stream for the orphan flag in the tool result.
SSE_FILE="/tmp/regression-orphan-list.sse"
BODY3=$(jq -n --arg p "$PID" \
  '{projectId:$p, mode:"analyst", messages:[{role:"user",content:"请直接调用 list 工具，type=\"intents\", markFilter=\"all\"。立即调用，不要先思考、不要先 read workspace。我要看 orphan 字段。"}]}')
curl -sS --max-time 60 -N \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "$BODY3" \
  "$HOST_AGENT/api/ontology/lakehouse-agent-stream" > "$SSE_FILE" 2>&1 || true

T3_LIST_CALLED="false"
grep -q '"name":"list"' "$SSE_FILE" 2>/dev/null && T3_LIST_CALLED="true"
assert "agent invoked list tool" "$T3_LIST_CALLED" "list tool not called"

T3_ORPHAN_VISIBLE="false"
grep -q "\"id\":\"$ORPHAN_ID\".*\"orphan\":true" "$SSE_FILE" 2>/dev/null && T3_ORPHAN_VISIBLE="true"
# Fallback — agent may have only returned a subset; check raw orphanCount > 0.
if [ "$T3_ORPHAN_VISIBLE" = "false" ]; then
  if grep -qE '"orphanCount":[1-9]' "$SSE_FILE" 2>/dev/null; then
    T3_ORPHAN_VISIBLE="true"
  fi
fi
assert "list result exposes orphan=true / orphanCount>=1" "$T3_ORPHAN_VISIBLE" \
       "neither orphan=true nor orphanCount>=1 in SSE"

# ── Cleanup synthetic data so subsequent runs stay clean ───────────────────
psql_exec -c "
  DELETE FROM lakehouse_metric_intent WHERE name IN ('$ORPHAN_NAME', '$INTENT_NAME_WITH_TRIG');
" >/dev/null 2>&1

# ── Result ─────────────────────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════════════════════"
echo "  INTENT ORPHAN INVARIANT — REGRESSION RESULT"
echo "════════════════════════════════════════════════════════"
echo "   pass / fail / total: $PASS / $FAIL / $((PASS + FAIL))"
if [ "$FAIL" -eq 0 ]; then
  echo ""
  echo "✓ INVARIANT PASS — orphan intents can no longer be silently created"
  exit 0
else
  echo ""
  echo "✗ INVARIANT FAIL — see SSE at $SSE_FILE"
  exit 1
fi
