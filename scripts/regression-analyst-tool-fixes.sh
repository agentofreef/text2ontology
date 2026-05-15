#!/usr/bin/env bash
# scripts/regression-analyst-tool-fixes.sh — regression for two dogfood-discovered
# analyst-tool bug fixes.
#
# Fix 1 — draft_od_create idempotent on (workspaceId, name):
#   Re-creating a draft OD with the same name must return the existing row with
#   `existed: true` instead of erroring. Pre-fix it surfaced an "already exists"
#   error and the LLM retried in a loop.
#
# Fix 2 — draft_property_add_or_update infers dataType:
#   When `dataType` is omitted on the insert path, the server tries
#   information_schema via the parent draft OD's semanticSql, defaults to "text"
#   on miss, and decorates the response with `dataType_defaulted: true` + a
#   `note` describing what happened.
#
# Both tool results stream back as part of the `function_call` SSE event payload
# (`{name, arguments, result: {...}}`), so we can grep the raw SSE bytes for the
# success markers.
#
# This probe steers the LLM with explicit Chinese prompts that name the tool,
# the args, and the duplicate scenario. It is not bullet-proof against an
# exceptionally evasive model — failures should always be triaged by reading
# the captured SSE file before claiming a regression.
#
# Required:
#   - agent-server :18092 healthy
#   - .env.shared (DATABASE_URL, INTERNAL_TOKEN)
#   - jq, psql or docker-running text2ontology-enterprise-postgres-1
#
# Output:
#   regression-fixtures/analyst-tool-fixes-<ts>/
#     turn-1.sse … turn-4.sse
#     summary.json

set -uo pipefail
cd "$(dirname "$0")/.."

HOST_AGENT="${HOST_AGENT:-http://127.0.0.1:18092}"
TOKEN="${TOKEN:-bearer-a0000000-0000-0000-0000-000000000001}"
TURN_TIMEOUT="${TURN_TIMEOUT:-90}"

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

PID=$(psql_exec -tAc "SELECT id FROM project LIMIT 1" | tr -d '[:space:]')
if [ -z "$PID" ]; then
  echo "ERROR: could not resolve projectId from DATABASE_URL"; exit 2
fi

TS=$(date -u +%Y%m%dT%H%M%SZ)
OUTDIR="regression-fixtures/analyst-tool-fixes-${TS}"
mkdir -p "$OUTDIR"

# Use a unique OD name per run so reruns don't collide on workspace state
# from prior probes (a fresh thread is created each run, so collision is
# theoretical, but the timestamp suffix makes intent obvious in the DB too).
OD_NAME="RegOd_${TS}"

echo "▼// REGRESSION — analyst tool bug fixes"
echo "   agent-server: $HOST_AGENT"
echo "   projectId:    $PID"
echo "   draft od:     $OD_NAME"
echo "   output:       $OUTDIR"
echo ""

if ! curl -sf --max-time 5 "$HOST_AGENT/healthz" >/dev/null 2>&1; then
  echo "FATAL: agent-server unreachable at $HOST_AGENT/healthz"
  exit 1
fi
echo "   [infra] agent-server OK"
echo ""

THREAD_ID=""
PASS=0
FAIL=0

# ── Turn helper ──────────────────────────────────────────────────────────────
# send_turn <label> <question> — streams SSE to $OUTDIR/turn-<label>.sse,
# captures threadId from the first thread event into $THREAD_ID.
send_turn() {
  local label="$1"
  local question="$2"
  local sse_file="$OUTDIR/turn-${label}.sse"

  local body
  if [ -z "$THREAD_ID" ]; then
    body=$(jq -n --arg p "$PID" --arg q "$question" \
      '{projectId:$p, mode:"analyst", messages:[{role:"user",content:$q}]}')
  else
    body=$(jq -n --arg p "$PID" --arg q "$question" --arg t "$THREAD_ID" \
      '{projectId:$p, mode:"analyst", threadId:$t, messages:[{role:"user",content:$q}]}')
  fi

  curl -sS --max-time "$TURN_TIMEOUT" -N \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "$body" \
    "$HOST_AGENT/api/ontology/lakehouse-agent-stream" \
    > "$sse_file" 2>&1 || true

  if [ -z "$THREAD_ID" ]; then
    THREAD_ID=$(grep -oE '"threadId":"[^"]*"' "$sse_file" 2>/dev/null \
                | head -1 | cut -d'"' -f4 || echo "")
  fi
}

# Returns 0 if any function_call event in $1 invoked tool $2.
sse_has_tool() {
  local sse_file="$1" tool="$2"
  grep -F '"function_call"' "$sse_file" 2>/dev/null \
    | grep -q "\"name\":\"${tool}\""
}

# Returns 0 if the SSE bytes contain the literal marker $2.
sse_has_marker() {
  local sse_file="$1" marker="$2"
  grep -qF "$marker" "$sse_file" 2>/dev/null
}

assert() {
  local label="$1" cond="$2" detail="$3"
  if [ "$cond" = "true" ]; then
    echo "   ✓ $label"
    PASS=$((PASS + 1))
  else
    echo "   ✗ $label  ($detail)"
    FAIL=$((FAIL + 1))
  fi
}

# ── Turn 1: bootstrap workspace + first draft_od_create ──────────────────────
echo "── [1/4] Bootstrap thread + first draft_od_create($OD_NAME)"
send_turn 1 "请直接调用 analyst 工具 draft_od_create，参数 name=\"${OD_NAME}\", kind=\"entity\"。不要先访谈、不要先 inspect、不要写其它工具。只调用一次 draft_od_create 然后回复 ok。"

if [ -z "$THREAD_ID" ]; then
  echo "   FATAL: turn 1 produced no threadId — agent unreachable or SSE error"
  cat "$OUTDIR/turn-1.sse" | head -20
  exit 1
fi
echo "   threadId=$THREAD_ID"

T1_HAS_CREATE=false
sse_has_tool "$OUTDIR/turn-1.sse" "draft_od_create" && T1_HAS_CREATE=true
assert "turn 1 invoked draft_od_create" "$T1_HAS_CREATE" \
       "agent ignored the explicit instruction"

# Turn 1 should NOT show existed=true (this is the first creation).
T1_NOT_EXISTED=true
if sse_has_marker "$OUTDIR/turn-1.sse" '"existed":true'; then
  T1_NOT_EXISTED=false
fi
assert "turn 1 result existed=false (fresh create)" "$T1_NOT_EXISTED" \
       "first call should be a fresh insert, not an existed=true reply"

# ── Turn 2: duplicate draft_od_create — must succeed with existed=true ───────
echo "── [2/4] Duplicate draft_od_create — fix 1 trigger"
send_turn 2 "再次调用 draft_od_create 工具，参数完全相同：name=\"${OD_NAME}\", kind=\"entity\"。我故意要测试同名重复创建是否会报错。请直接调用，不要先 read workspace。"

T2_HAS_CREATE=false
sse_has_tool "$OUTDIR/turn-2.sse" "draft_od_create" && T2_HAS_CREATE=true
assert "turn 2 invoked draft_od_create" "$T2_HAS_CREATE" \
       "agent skipped the second call; cannot exercise idempotency"

# Fix 1 success marker.
T2_HAS_EXISTED=false
sse_has_marker "$OUTDIR/turn-2.sse" '"existed":true' && T2_HAS_EXISTED=true
assert "fix 1: duplicate returned existed=true (idempotent)" "$T2_HAS_EXISTED" \
       "duplicate draft_od_create did not return existed=true"

# Fix 1 anti-marker — old behaviour erred with "already exists".
T2_NO_OLD_ERROR=true
if sse_has_marker "$OUTDIR/turn-2.sse" 'already exists in this workspace' \
   || sse_has_marker "$OUTDIR/turn-2.sse" '"error":"AppendDraftOD failed:'; then
  T2_NO_OLD_ERROR=false
fi
assert "fix 1: no pre-fix \"already exists\" error" "$T2_NO_OLD_ERROR" \
       "old error string still surfaces in SSE — binary may not be rebuilt"

# ── Turn 3: capture draft_od_id from DB so turn 4 can address it precisely ───
echo "── [3/4] Look up draftOdId from DB"
DRAFT_OD_ID=$(psql_exec -tAc "
  SELECT od.id
  FROM analyst_draft_od od
  JOIN analyst_workspace ws ON ws.id = od.workspace_id
  WHERE ws.thread_id = '${THREAD_ID}' AND od.name = '${OD_NAME}'
  LIMIT 1
" | tr -d '[:space:]')

if [ -z "$DRAFT_OD_ID" ]; then
  echo "   ✗ could not look up draftOdId for ${OD_NAME} (turn 4 will be skipped)"
  FAIL=$((FAIL + 1))
else
  echo "   draftOdId=$DRAFT_OD_ID"

  # ── Turn 4: draft_property_add_or_update without dataType — fix 2 ──────────
  echo "── [4/4] Property add without dataType — fix 2 trigger"
  PROP_NAME="reg_prop_${TS}"
  send_turn 4 "请调用 draft_property_add_or_update，参数：draftOdId=\"${DRAFT_OD_ID}\", name=\"${PROP_NAME}\", sourceColumn=\"order_id\"。**故意不要传 dataType**——我要测试服务端推断 / 默认 'text' 是否工作。直接调用一次，不要 inspect 也不要先 read。"

  T4_HAS_PROP=false
  sse_has_tool "$OUTDIR/turn-4.sse" "draft_property_add_or_update" && T4_HAS_PROP=true
  assert "turn 4 invoked draft_property_add_or_update" "$T4_HAS_PROP" \
         "agent skipped property creation; cannot exercise dataType inference"

  # Fix 2 success marker.
  T4_HAS_DEFAULTED=false
  sse_has_marker "$OUTDIR/turn-4.sse" '"dataType_defaulted":true' && T4_HAS_DEFAULTED=true
  assert "fix 2: dataType_defaulted=true on missing-dataType insert" "$T4_HAS_DEFAULTED" \
         "tool result missing dataType_defaulted marker"

  # Result row should still have a usable dataType (default 'text' or inferred).
  T4_HAS_DATATYPE=false
  if sse_has_marker "$OUTDIR/turn-4.sse" '"dataType":"text"' \
     || sse_has_marker "$OUTDIR/turn-4.sse" '"dataType":"integer"' \
     || sse_has_marker "$OUTDIR/turn-4.sse" '"dataType":"bigint"' \
     || sse_has_marker "$OUTDIR/turn-4.sse" '"dataType":"varchar"' \
     || sse_has_marker "$OUTDIR/turn-4.sse" '"dataType":"uuid"'; then
    T4_HAS_DATATYPE=true
  fi
  assert "fix 2: response carries a non-empty dataType" "$T4_HAS_DATATYPE" \
         "no dataType field surfaced — server may have rejected the request"

  # Fix 2 anti-marker — old behaviour rejected with "dataType is required".
  T4_NO_OLD_ERROR=true
  if sse_has_marker "$OUTDIR/turn-4.sse" 'dataType is required'; then
    T4_NO_OLD_ERROR=false
  fi
  assert "fix 2: no pre-fix \"dataType is required\" error" "$T4_NO_OLD_ERROR" \
         "old required-dataType error still surfaces — rebuild not deployed?"

  # DB-side cross-check — the property must actually be on disk.
  DB_PROP_DT=$(psql_exec -tAc "
    SELECT data_type FROM analyst_draft_property
    WHERE draft_od_id = '${DRAFT_OD_ID}' AND name = '${PROP_NAME}'
  " | tr -d '[:space:]')
  T4_PERSISTED=false
  if [ -n "$DB_PROP_DT" ]; then T4_PERSISTED=true; fi
  assert "fix 2: property persisted to DB (dataType=${DB_PROP_DT:-<missing>})" \
         "$T4_PERSISTED" "row never landed in analyst_draft_property"
fi

# ── summary.json ─────────────────────────────────────────────────────────────
GATE_PASS="false"
[ "$FAIL" -eq 0 ] && GATE_PASS="true"

jq -n \
  --arg ts "$TS" \
  --arg threadId "${THREAD_ID:-}" \
  --arg odName "$OD_NAME" \
  --arg draftOdId "${DRAFT_OD_ID:-}" \
  --argjson pass "$PASS" \
  --argjson fail "$FAIL" \
  --argjson gate "$GATE_PASS" \
  '{
    timestamp: $ts,
    threadId: $threadId,
    draftOdName: $odName,
    draftOdId: $draftOdId,
    totals: {pass: $pass, fail: $fail, total: ($pass + $fail)},
    gatePass: ($gate == "true")
  }' > "$OUTDIR/summary.json"

echo ""
echo "════════════════════════════════════════════════════════"
echo "  ANALYST TOOL FIXES — REGRESSION RESULT"
echo "════════════════════════════════════════════════════════"
echo "   pass / fail / total: $PASS / $FAIL / $((PASS + FAIL))"
echo "   gate (all must hold): $GATE_PASS"
echo "   sse files:           $OUTDIR/turn-*.sse"
echo "   summary.json:        $OUTDIR/summary.json"

if [ "$GATE_PASS" = "true" ]; then
  echo ""
  echo "✓ REGRESSION PASSED — both bug fixes verified end-to-end"
  exit 0
else
  echo ""
  echo "✗ REGRESSION FAILED — inspect $OUTDIR/turn-*.sse for raw SSE traces"
  exit 1
fi
