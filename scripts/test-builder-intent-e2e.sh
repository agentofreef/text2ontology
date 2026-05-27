#!/usr/bin/env bash
# scripts/test-builder-intent-e2e.sh — drives the builder agent through one
# full propose_intent → activate-intent cycle to prove the structural fix:
# every code path that creates an active metric intent now produces trigger
# keyword bindings atomically, so the orphan bug we burned a day on cannot
# recur via this flow.
#
# Pre-conditions:
#   - Northwind 测试项目 has the 4 ODs + 3 links (rebuilt via SQL); 0 intents.
#   - agent-server, backend-api, lakehouse-sql-server up and rebuilt with the
#     new helper / orphan-guard.

set -uo pipefail
cd "$(dirname "$0")/.."

HOST_AGENT="${HOST_AGENT:-http://127.0.0.1:18092}"
HOST_API="${HOST_API:-http://127.0.0.1:18090}"
TOKEN="${TOKEN:-bearer-a0000000-0000-0000-0000-000000000001}"
PID="16d0a9a7-cfd4-437b-8bd9-e8738fbaa315"
SALE_OD="160e3ddf-f6c9-458c-a147-95eabfc629cf"
TIMEOUT=120

set -a && . ./.env.shared && set +a

psql_exec() {
  if command -v psql >/dev/null 2>&1; then psql "$DATABASE_URL" "$@"
  else docker exec -i text2ontology-enterprise-postgres-1 \
        psql -U "${POSTGRES_USER:-text2ontology_community}" \
             -d "${POSTGRES_DB:-text2ontology_community}" "$@"; fi
}

TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT=/tmp/builder-intent-e2e-$TS
mkdir -p "$OUT"

PASS=0; FAIL=0
assert() {
  local label="$1" cond="$2" detail="${3:-}"
  if [ "$cond" = "true" ]; then echo "   ✓ $label"; PASS=$((PASS+1))
  else echo "   ✗ $label  ($detail)"; FAIL=$((FAIL+1)); fi
}

echo "▼// E2E builder-intent — verify orphan bug is structurally fixed"
echo "   project:   Northwind ($PID)"
echo "   anchor OD: SALE ($SALE_OD)"
echo "   output:    $OUT"
echo ""

# Sanity: confirm clean slate (0 intents, 0 keywords).
COUNT_BEFORE=$(psql_exec -tAc "
  SELECT count(*) FROM lakehouse_metric_intent WHERE project_id='$PID'
" | tr -d '[:space:]')
[ "$COUNT_BEFORE" = "0" ] || { echo "FATAL: project not empty (intents=$COUNT_BEFORE)"; exit 1; }
echo "   [pre-check] 0 intents, 0 keywords ✓"
echo ""

THREAD_ID=""
# send_turn writes SSE to $OUT/turn-$1.sse and updates global THREAD_ID
# (do not call inside $() — would run in a subshell and lose the variable).
send_turn() {
  local n="$1" q="$2"
  local sse="$OUT/turn-$n.sse"
  local body
  if [ -z "$THREAD_ID" ]; then
    body=$(jq -n --arg p "$PID" --arg q "$q" \
      '{projectId:$p, mode:"builder", messages:[{role:"user",content:$q}]}')
  else
    body=$(jq -n --arg p "$PID" --arg q "$q" --arg t "$THREAD_ID" \
      '{projectId:$p, mode:"builder", threadId:$t, messages:[{role:"user",content:$q}]}')
  fi
  curl -sS --max-time $TIMEOUT -N \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "$body" "$HOST_AGENT/api/ontology/lakehouse-agent-stream" > "$sse" 2>&1 || true
  if [ -z "$THREAD_ID" ]; then
    THREAD_ID=$(grep -oE '"threadId":"[^"]*"' "$sse" | head -1 | cut -d'"' -f4)
  fi
  EVENTS=$(grep -c '^data: ' "$sse" 2>/dev/null || echo 0)
}

# ── 3 turns of business context (3-turn gate is for propose_od; intents may
# be allowed earlier, but a few turns prime the LLM with real context).
echo "── Phase 1: business context"
send_turn 1 "我想给 Northwind 销售业务建查询意图。我们的事实表是 SALE，已经存在并激活，OD ID 是 ${SALE_OD}。它有 NetAmount, CategoryName, ShipCountry, OrderDate, CustomerID, EmployeeID, ProductName 等 properties。"
echo "   turn 1: events=$EVENTS, threadId=$THREAD_ID"
[ -n "$THREAD_ID" ] || { echo "FATAL: no threadId from turn 1"; exit 1; }

send_turn 2 "业务诉求：销售经理需要按多个维度切片分析销售总额，最关键是\"按类别看销售额\"——把每个 Category 的 sum(NetAmount) 列出来。"
echo "   turn 2: events=$EVENTS"

send_turn 3 "请你直接调用 propose_intent 工具创建这个 Intent，参数：objectId=\"$SALE_OD\", name=\"Sales.ByCategory\", canonicalMetric=\"sum(NetAmount)\", autoGroupBy=[\"CategoryName\"], triggerKeywords=[\"销售额\",\"按类别\",\"类别\",\"各类别\",\"按品类\",\"category\",\"sales by category\"]。一次性给完，不要先问澄清。"
echo "   turn 3: events=$EVENTS"

# ── Inspect SSE for propose_intent result ─────────────────────────────────
echo ""
echo "── Phase 2: capture propose_intent payload"

PROPOSE_RESULT=$(grep -F '"name":"propose_intent"' "$OUT/turn-3.sse" | head -1)
if [ -z "$PROPOSE_RESULT" ]; then
  # Sometimes the propose lands in the next round; check turns 1-3 broadly.
  PROPOSE_RESULT=$(grep -F '"name":"propose_intent"' "$OUT"/turn-*.sse | head -1)
fi
T1_OK="false"
[ -n "$PROPOSE_RESULT" ] && T1_OK="true"
assert "agent invoked propose_intent" "$T1_OK" "no function_call event"

INTENT_ID=$(echo "$PROPOSE_RESULT" | grep -oE '"intentId":"[a-f0-9-]{36}"' | head -1 | cut -d'"' -f4)
T2_OK="false"
[ -n "$INTENT_ID" ] && T2_OK="true"
assert "propose_intent returned intentId" "$T2_OK" "intentId missing from result"

# Verify the row exists AND is mark=false (pending).
if [ -n "$INTENT_ID" ]; then
  PEND=$(psql_exec -tAc "
    SELECT mark FROM lakehouse_metric_intent WHERE id='$INTENT_ID'
  " | tr -d '[:space:]')
  T3_OK="false"
  [ "$PEND" = "f" ] && T3_OK="true"
  assert "DB row inserted with mark=false (pending)" "$T3_OK" "mark=$PEND"

  PEND_KW=$(psql_exec -tAc "
    SELECT count(*) FROM lakehouse_keyword WHERE metric_intent_id='$INTENT_ID'
  " | tr -d '[:space:]')
  T4_OK="false"
  [ "$PEND_KW" = "0" ] && T4_OK="true"
  assert "Pending intent has 0 keywords yet (deferred to activate)" "$T4_OK" "kw=$PEND_KW"
fi

# ── Phase 3: activate-intent — the structural fix kicks in ─────────────────
echo ""
echo "── Phase 3: activate-intent enforces atomicity"

# 3a — try activating WITHOUT triggerKeywords; expect 400.
echo "   [3a] activate without triggers → expect 400"
RC_NO=$(curl -sS -o /tmp/act-no.json -w "%{http_code}" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -X POST -d "{\"intentId\":\"$INTENT_ID\",\"projectId\":\"$PID\"}" \
  "$HOST_API/api/ontology/builder/activate-intent")
T5_OK="false"
[ "$RC_NO" = "400" ] && T5_OK="true"
assert "activate-intent without triggers → HTTP 400" "$T5_OK" \
       "got HTTP $RC_NO body=$(head -c 200 /tmp/act-no.json)"

# Confirm intent still pending (not flipped to mark=true on the bad request).
STILL_PENDING=$(psql_exec -tAc "
  SELECT mark FROM lakehouse_metric_intent WHERE id='$INTENT_ID'
" | tr -d '[:space:]')
T5B_OK="false"
[ "$STILL_PENDING" = "f" ] && T5B_OK="true"
assert "intent stayed pending after rejected activate" "$T5B_OK" "mark=$STILL_PENDING"

# 3b — activate WITH triggers; expect 200 + atomic keyword writes.
echo "   [3b] activate with triggers → expect 200 + 7 keywords"
ACT_BODY=$(cat <<EOF
{
  "intentId":"$INTENT_ID",
  "projectId":"$PID",
  "triggerKeywords":["销售额","按类别","类别","各类别","按品类","category","sales by category"]
}
EOF
)
RC_OK=$(curl -sS -o /tmp/act-ok.json -w "%{http_code}" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -X POST -d "$ACT_BODY" "$HOST_API/api/ontology/builder/activate-intent")
T6_OK="false"
[ "$RC_OK" = "200" ] && T6_OK="true"
assert "activate-intent with 7 triggers → HTTP 200" "$T6_OK" \
       "got HTTP $RC_OK body=$(head -c 200 /tmp/act-ok.json)"

ACTIVE_KW=$(psql_exec -tAc "
  SELECT count(*) FROM lakehouse_keyword WHERE metric_intent_id='$INTENT_ID'
" | tr -d '[:space:]')
T7_OK="false"
[ "$ACTIVE_KW" = "7" ] && T7_OK="true"
assert "lakehouse_keyword has 7 trigger rows for activated intent" "$T7_OK" "kw=$ACTIVE_KW"

ACTIVE_MARK=$(psql_exec -tAc "
  SELECT mark FROM lakehouse_metric_intent WHERE id='$INTENT_ID'
" | tr -d '[:space:]')
T8_OK="false"
[ "$ACTIVE_MARK" = "t" ] && T8_OK="true"
assert "intent flipped to mark=true (active)" "$T8_OK" "mark=$ACTIVE_MARK"

# ── Phase 4: zero orphans invariant ────────────────────────────────────────
echo ""
echo "── Phase 4: zero ACTIVE orphans invariant"
# Pending intents (mark=false) are by design without keywords — they get
# triggers atomically at activate-intent. Only ACTIVE orphans (mark=true,
# triggers=0) represent the bug we eliminated.
ORPHANS=$(psql_exec -tAc "
  SELECT count(*) FROM lakehouse_metric_intent mi
  WHERE mi.project_id='$PID' AND mi.mark = true
    AND NOT EXISTS (SELECT 1 FROM lakehouse_keyword lk WHERE lk.metric_intent_id = mi.id)
" | tr -d '[:space:]')
T9_OK="false"
[ "$ORPHANS" = "0" ] && T9_OK="true"
assert "zero ACTIVE orphan intents (mark=true with no triggers)" "$T9_OK" "orphans=$ORPHANS"

# ── Phase 5: end-to-end recall via lakehouse mode ─────────────────────────
echo ""
echo "── Phase 5: lakehouse mode recall hits the new intent"
LH_BODY=$(jq -n --arg p "$PID" \
  '{projectId:$p, mode:"lakehouse", messages:[{role:"user",content:"按类别看销售额"}]}')
curl -sS --max-time $TIMEOUT -N \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$LH_BODY" "$HOST_AGENT/api/ontology/lakehouse-agent-stream" \
  > "$OUT/lh-query.sse" 2>&1 || true

T10_OK="false"
grep -q '"matched_intent":"Sales.ByCategory"' "$OUT/lh-query.sse" 2>/dev/null && T10_OK="true"
assert "lakehouse query matched Sales.ByCategory intent" "$T10_OK" \
       "matched_intent absent or different"

T11_OK="false"
grep -q '"execution_status":"success"' "$OUT/lh-query.sse" 2>/dev/null && T11_OK="true"
assert "smartquery executed against staging successfully" "$T11_OK" "no success status"

ROW_COUNT=$(grep -oE '"total_rows":[0-9]+' "$OUT/lh-query.sse" | head -1 | grep -oE '[0-9]+')
T12_OK="false"
[ -n "$ROW_COUNT" ] && [ "$ROW_COUNT" -ge 1 ] && T12_OK="true"
assert "smartquery returned ≥1 row (got $ROW_COUNT)" "$T12_OK" "rows=$ROW_COUNT"

# ── Result ─────────────────────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════════════════════"
echo "  E2E BUILDER-INTENT — RESULT"
echo "════════════════════════════════════════════════════════"
echo "   pass / fail / total: $PASS / $FAIL / $((PASS + FAIL))"
echo "   intentId:            ${INTENT_ID:-<missing>}"
echo "   threadId:            ${THREAD_ID:-<missing>}"
echo "   sse traces:          $OUT/"
if [ "$FAIL" -eq 0 ]; then
  echo ""
  echo "✓ STRUCTURAL FIX VERIFIED via builder agent + activate flow"
  exit 0
else
  echo ""
  echo "✗ FAIL — see SSE files in $OUT for diagnostics"
  exit 1
fi
