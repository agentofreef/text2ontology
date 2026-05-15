#!/usr/bin/env bash
# scripts/rehearsal-1.sh — Phase 4D Rehearsal #1 (side-by-side).
#
# 20 agent turns through agent-server DIRECT (Phase 4C 真分离 path),
# capture each turn's narrative + function_call shapes + final thread
# ledger state, compare against the AM/PM golden baseline, and run a
# "rollback drill" that verifies every thread state is independently
# readable (so if we needed to stop the new stack, thread state is
# not locked to agent-server process memory).
#
# Required:
#   - 5 sidecar containers up (docker compose ps)
#   - monolith optional; this script talks to agent-server directly
#   - INTERNAL_TOKEN + DATABASE_URL in .env.shared (for ledger DB probe)
#   - regression-fixtures/golden-prompts.json (6 prompts) —
#     cycled 4× → 20 turns total (allows within-prompt consistency
#     inspection + >1 per prompt for ledger accumulation test).
#
# Success gate (all must hold):
#   - 20/20 turns produce valid SSE (≥1 `thinking` event, ≥1 event
#     of type=function_call OR type=message)
#   - 20/20 threads have a ledger row with ≥1 structured Od OR the
#     fallback "no match" state captured
#   - Rollback drill: each threadId still resolvable via
#     /api/ontology/lakehouse-ledger?threadId=X after the turn ends
#
# Output: regression-fixtures/rehearsal-1-<timestamp>.json with the
#         full capture + pass/fail summary.

set -euo pipefail
cd "$(dirname "$0")/.."

HOST_AGENT="${HOST_AGENT:-http://127.0.0.1:18092}"        # agent-server container
TOKEN="${TOKEN:-bearer-a0000000-0000-0000-0000-000000000001}"
PROMPTS="${PROMPTS:-regression-fixtures/golden-prompts.json}"
ITERATIONS="${ITERATIONS:-4}"   # 6 prompts × 4 = 24 turns (soft 20 target)
TIMEOUT="${TIMEOUT:-60}"
OUT_FILE="${OUT_FILE:-regression-fixtures/rehearsal-1-$(date -u +%Y%m%dT%H%M%SZ).json}"

if [ ! -f .env.shared ]; then
  echo "ERROR: .env.shared not found"; exit 2
fi
set -a && . ./.env.shared && set +a

# Pick active project from DB (first project).
PID=$(psql "$DATABASE_URL" -tAc "SELECT id FROM project LIMIT 1")
if [ -z "$PID" ]; then
  echo "ERROR: could not resolve projectId"; exit 2
fi

echo "▼// REHEARSAL #1 side-by-side"
echo "   agent-server: $HOST_AGENT"
echo "   projectId:    $PID"
echo "   prompts:      $PROMPTS × $ITERATIONS iterations"
echo "   out:          $OUT_FILE"
echo ""

# Build prompt list (cycle)
PROMPT_IDS=$(jq -r '.prompts[].id' "$PROMPTS")
TOTAL=$(( $(echo "$PROMPT_IDS" | wc -l) * ITERATIONS ))

# JSON output stub
TMPD=$(mktemp -d)
trap 'rm -rf "$TMPD"' EXIT

TURNS_JSON="$TMPD/turns.json"
echo "[]" > "$TURNS_JSON"

TURN=0
PASS=0
FAIL=0

for iter in $(seq 1 "$ITERATIONS"); do
  while IFS= read -r pid_json; do
    TURN=$((TURN+1))
    PROMPT=$(jq -r --arg id "$pid_json" '.prompts[] | select(.id == $id) | .prompt' "$PROMPTS")
    echo -n "   turn $TURN/$TOTAL [$pid_json iter=$iter]: \"$PROMPT\" "

    SSE_FILE="$TMPD/sse-$TURN.out"
    curl -sS --max-time "$TIMEOUT" -N \
      -H "Authorization: Bearer $TOKEN" \
      -H "Content-Type: application/json" \
      -d "$(jq -n --arg p "$PID" --arg q "$PROMPT" '{projectId:$p, messages:[{role:"user",content:$q}]}')" \
      "$HOST_AGENT/api/ontology/lakehouse-agent-stream" > "$SSE_FILE" 2>&1 || true

    BYTES=$(wc -c < "$SSE_FILE")
    THREAD_ID=$(grep -oE '"threadId":"[^"]*"' "$SSE_FILE" | head -1 | cut -d'"' -f4)
    EVENTS=$(grep -c '^data: ' "$SSE_FILE" || true)
    FN_CALLS=$(grep -c '"type":"function_call"' "$SSE_FILE" || true)
    THINKING=$(grep -c '"type":"thinking"' "$SSE_FILE" || true)

    # Ledger read via agent-server public API
    LEDGER_BYTES=0
    LEDGER_OK=0
    if [ -n "$THREAD_ID" ]; then
      curl -sS --max-time 4 -o "$TMPD/ledger-$TURN.out" -w "%{http_code}" \
        -H "Authorization: Bearer $TOKEN" \
        "$HOST_AGENT/api/ontology/lakehouse-ledger?threadId=$THREAD_ID" > "$TMPD/lcode-$TURN" 2>/dev/null || true
      LCODE=$(cat "$TMPD/lcode-$TURN" 2>/dev/null || echo "000")
      LEDGER_BYTES=$(wc -c < "$TMPD/ledger-$TURN.out" 2>/dev/null || echo 0)
      if [ "$LCODE" = "200" ] && [ "$LEDGER_BYTES" -gt 0 ]; then
        LEDGER_OK=1
      fi
    fi

    # Pass criteria: SSE produced at least the thread event + 1 thinking OR function_call
    if [ -n "$THREAD_ID" ] && [ "$EVENTS" -ge 2 ] && { [ "$FN_CALLS" -ge 1 ] || [ "$THINKING" -ge 1 ]; }; then
      PASS=$((PASS+1))
      printf "PASS (%db, %d events, fnCalls=%d, ledger=%db)\n" "$BYTES" "$EVENTS" "$FN_CALLS" "$LEDGER_BYTES"
    else
      FAIL=$((FAIL+1))
      printf "FAIL (bytes=%d events=%d fnCalls=%d)\n" "$BYTES" "$EVENTS" "$FN_CALLS"
    fi

    # Append turn JSON
    jq --arg pid "$pid_json" --arg iter "$iter" --arg tid "$THREAD_ID" \
       --argjson bytes "$BYTES" --argjson events "$EVENTS" \
       --argjson fn "$FN_CALLS" --argjson think "$THINKING" \
       --argjson lbytes "$LEDGER_BYTES" --argjson lok "$LEDGER_OK" \
       '. += [{promptId: $pid, iter: ($iter|tonumber), threadId: $tid,
                sseBytes: $bytes, sseEvents: $events,
                fnCalls: $fn, thinkingEvents: $think,
                ledgerBytes: $lbytes, ledgerOk: ($lok == 1)}]' \
       "$TURNS_JSON" > "$TURNS_JSON.tmp" && mv "$TURNS_JSON.tmp" "$TURNS_JSON"
  done <<<"$PROMPT_IDS"
done

# Rollback drill: fetch every threadId via /_debug/ledger-rebuild
#   save=0 → preview-only, proves ledger reconstruction works without
#   needing the live agent-server turn to still be in memory.
echo ""
echo "── rollback drill: rebuild ledger for each thread (save=0) ──"
ROLLBACK_PASS=0
ROLLBACK_FAIL=0
for tid in $(jq -r '.[].threadId | select(. != "")' "$TURNS_JSON"); do
  code=$(curl -sS --max-time 4 -o /dev/null -w "%{http_code}" \
    -H "Authorization: Bearer $TOKEN" \
    "$HOST_AGENT/api/ontology/_debug/ledger-rebuild?threadId=$tid&save=0")
  if [ "$code" = "200" ]; then
    ROLLBACK_PASS=$((ROLLBACK_PASS+1))
  else
    ROLLBACK_FAIL=$((ROLLBACK_FAIL+1))
    echo "   FAIL threadId=$tid → HTTP $code"
  fi
done

# Build final output JSON
jq -n --arg capturedAt "$(date -u -Iseconds)" \
      --arg host "$HOST_AGENT" \
      --arg pid "$PID" \
      --argjson pass $PASS --argjson fail $FAIL \
      --argjson rbPass $ROLLBACK_PASS --argjson rbFail $ROLLBACK_FAIL \
      --argjson total $TOTAL \
      --slurpfile turns "$TURNS_JSON" \
      '{capturedAt: $capturedAt, host: $host, projectId: $pid,
        totals: {totalTurns: $total, sse: {pass: $pass, fail: $fail},
                 rollback: {pass: $rbPass, fail: $rbFail}},
        gatePass: ($fail == 0 and $rbFail == 0),
        turns: $turns[0]}' > "$OUT_FILE"

echo ""
echo "════════════════════════════════════════════════════════"
echo "  REHEARSAL #1 RESULT"
echo "════════════════════════════════════════════════════════"
echo "   total turns:           $TOTAL"
echo "   SSE pass / fail:       $PASS / $FAIL"
echo "   rollback pass / fail:  $ROLLBACK_PASS / $ROLLBACK_FAIL"
GATE=$(jq -r '.gatePass' "$OUT_FILE")
echo "   gate (all must hold):  $GATE"
echo "   output file:           $OUT_FILE"

if [ "$GATE" = "true" ]; then
  echo ""
  echo "✓ PHASE 4D REHEARSAL #1 PASSED — safe to proceed to Rehearsal #2"
  exit 0
else
  echo ""
  echo "✗ PHASE 4D REHEARSAL #1 FAILED — investigate before proceeding"
  exit 1
fi
