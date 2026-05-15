#!/usr/bin/env bash
# scripts/dogfood-analyst-northwind.sh — Deep analyst-mode dogfood probe.
#
# Validates AGENT BEHAVIOR (right tools, right order, workspace hygiene),
# not just SSE plumbing. Hardcoded to the Northwind 测试项目.
#
# Prerequisites:
#   - agent-server (:18092), backend-api (:18090), lakehouse-sql-server (:18094) up
#   - .env.shared present (DATABASE_URL, INTERNAL_TOKEN)
#   - jq installed
#
# Output:
#   regression-fixtures/dogfood-northwind-<ts>/turn-<n>.sse
#   regression-fixtures/dogfood-northwind-<ts>/turn-<n>-workspace.json
#   regression-fixtures/dogfood-northwind-<ts>/summary.json
#
# Exit code: 0 if probe completed (even with partial failures)
#            1 if infrastructure down (agent-server unreachable)

set -uo pipefail
cd "$(dirname "$0")/.."

HOST_AGENT="${HOST_AGENT:-http://127.0.0.1:18092}"
HOST_API="${HOST_API:-http://127.0.0.1:18090}"
TOKEN="bearer-a0000000-0000-0000-0000-000000000001"
PROJECT_ID="16d0a9a7-cfd4-437b-8bd9-e8738fbaa315"
TURN_TIMEOUT=90

TS=$(date -u +%Y%m%dT%H%M%SZ)
OUTDIR="regression-fixtures/dogfood-northwind-${TS}"
mkdir -p "$OUTDIR"

echo "▼// DOGFOOD ANALYST — Northwind 深度探针"
echo "   agent-server: $HOST_AGENT"
echo "   projectId:    $PROJECT_ID"
echo "   output:       $OUTDIR"
echo ""

# ── Infrastructure check ─────────────────────────────────────────────────────
if ! curl -sf --max-time 5 "$HOST_AGENT/healthz" >/dev/null 2>&1; then
  echo "FATAL: agent-server unreachable at $HOST_AGENT/healthz"
  exit 1
fi
echo "   [infra] agent-server OK"

if ! curl -sf --max-time 5 "$HOST_API/healthz" >/dev/null 2>&1; then
  echo "WARN: backend-api unreachable — workspace GET will likely fail"
fi

# ── State tracking ───────────────────────────────────────────────────────────
THREAD_ID=""
TURNS_EXECUTED=0
declare -a TOOL_CALLS_PER_TURN=()
declare -a ISSUES=()

add_issue() {
  ISSUES+=("$1")
  echo "   [ISSUE] $1"
}

# ── SSE turn helper ───────────────────────────────────────────────────────────
# send_turn <turn_num> <question>
# Uses THREAD_ID (global). Updates THREAD_ID on first turn.
# Writes SSE to $OUTDIR/turn-<n>.sse
send_turn() {
  local n="$1"
  local question="$2"
  local sse_file="$OUTDIR/turn-${n}.sse"
  local ws_file="$OUTDIR/turn-${n}-workspace.json"

  local body
  if [ -z "$THREAD_ID" ]; then
    body=$(jq -n \
      --arg p "$PROJECT_ID" \
      --arg q "$question" \
      '{projectId:$p, mode:"analyst", messages:[{role:"user",content:$q}]}')
  else
    body=$(jq -n \
      --arg p "$PROJECT_ID" \
      --arg q "$question" \
      --arg t "$THREAD_ID" \
      '{projectId:$p, mode:"analyst", threadId:$t, messages:[{role:"user",content:$q}]}')
  fi

  echo -n "   [turn ${n}] sending... "

  # Stream SSE; tolerate curl errors (don't let them kill the script)
  curl -sS --max-time "$TURN_TIMEOUT" -N \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "$body" \
    "$HOST_AGENT/api/ontology/lakehouse-agent-stream" \
    > "$sse_file" 2>&1 || true

  local event_count
  event_count=$(grep -c '^data: ' "$sse_file" 2>/dev/null || echo 0)

  # Extract threadId from first thread event
  if [ -z "$THREAD_ID" ]; then
    THREAD_ID=$(grep -oE '"threadId":"[^"]*"' "$sse_file" 2>/dev/null | head -1 | cut -d'"' -f4 || echo "")
  fi

  # Parse tool calls from function_call SSE events
  local tools_this_turn
  tools_this_turn=$(grep '"function_call"' "$sse_file" 2>/dev/null \
    | grep -oE '"name":"[^"]*"' \
    | cut -d'"' -f4 \
    | sort -u \
    | tr '\n' ',' \
    | sed 's/,$//' || echo "")

  TOOL_CALLS_PER_TURN+=("$tools_this_turn")

  # Check for SSE errors
  local has_error=false
  if grep -q '"error"' "$sse_file" 2>/dev/null; then
    has_error=true
  fi

  echo "events=$event_count tools=[${tools_this_turn}]$([ "$has_error" = "true" ] && echo " ERROR-IN-SSE" || echo "")"

  if [ "$event_count" -eq 0 ]; then
    add_issue "Turn ${n}: zero SSE events — agent may have crashed or timed out"
  fi

  TURNS_EXECUTED=$((TURNS_EXECUTED + 1))

  # GET workspace state after each turn
  if [ -n "$THREAD_ID" ]; then
    curl -sS --max-time 15 \
      -H "Authorization: Bearer $TOKEN" \
      "$HOST_AGENT/api/ontology/analyst/workspace?threadId=${THREAD_ID}" \
      > "$ws_file" 2>/dev/null || echo '{"error":"workspace GET failed"}' > "$ws_file"
  else
    echo '{"error":"no threadId yet"}' > "$ws_file"
  fi
}

# ── Behavior analysis helpers ─────────────────────────────────────────────────

# Check if a tool appears in turn N's SSE
turn_has_tool() {
  local n="$1" tool="$2"
  grep '"function_call"' "$OUTDIR/turn-${n}.sse" 2>/dev/null | grep -q "\"name\":\"${tool}\"" || return 1
}

# Check if a banned tool appears
turn_has_banned_tool() {
  local n="$1"
  local banned_tools=("propose_od" "propose_link" "propose_intent" "clarify_and_branch")
  for bt in "${banned_tools[@]}"; do
    if grep '"function_call"' "$OUTDIR/turn-${n}.sse" 2>/dev/null | grep -q "\"name\":\"${bt}\""; then
      add_issue "Turn ${n}: BANNED tool '${bt}' was called — Phase 2D removed it"
    fi
  done
}

# ── 10-turn probe sequence ────────────────────────────────────────────────────

echo ""
echo "── Phase 1: Business understanding + exploration ──"

send_turn 1 "我想给 Northwind 数据集里的订单流程建一个 OD。从订单头开始 —— 用户、订单时间、金额是核心。"

if [ -z "$THREAD_ID" ]; then
  add_issue "Turn 1: no threadId returned — cannot continue"
  echo ""
  echo "FATAL: no threadId from turn 1; aborting remaining turns"
  # Write minimal summary and exit 1
  jq -n \
    --arg ts "$TS" \
    --arg dir "$OUTDIR" \
    --argjson turns "$TURNS_EXECUTED" \
    '{"timestamp":$ts,"outdir":$dir,"turns_executed":$turns,"fatal":"no threadId from turn 1"}' \
    > "$OUTDIR/summary.json"
  exit 1
fi
echo "   [infra] threadId=$THREAD_ID"

# Behavior check: turn 1 should call decision_log(business_context_digest)
if ! turn_has_tool 1 "decision_log"; then
  add_issue "Turn 1: agent did NOT call decision_log to capture business_context_digest — prompt instruction violated"
fi
turn_has_banned_tool 1

send_turn 2 "我看了一下，主表应该是 Orders，过滤未取消的（status != 'Cancelled'）。粒度=一行一订单。请你查 schema 然后建议哪些列做 properties。"

# Behavior check: turn 2 should call inspect(mode=schema) or list(type=tables)
if ! turn_has_tool 2 "inspect" && ! turn_has_tool 2 "list"; then
  add_issue "Turn 2: agent did NOT call inspect or list before suggesting properties — explored blindly"
fi
turn_has_banned_tool 2

send_turn 3 "请用 inspect 看一下 Orders 表的 schema，特别是 ShipperID 和 EmployeeID 这种 FK 列的值分布。"

# Behavior check: turn 3 should call inspect
if ! turn_has_tool 3 "inspect"; then
  add_issue "Turn 3: agent did NOT call inspect when explicitly asked to inspect Orders schema"
fi
# Check if findings were written
if ! turn_has_tool 3 "finding_create"; then
  add_issue "Turn 3: agent did NOT call finding_create after inspect — data discovered but not persisted to workspace"
fi
turn_has_banned_tool 3

echo ""
echo "── Phase 2: Draft OD + properties ──"

send_turn 4 "好的，请 propose 一个 draft_od 叫 NorthwindOrder。properties 至少包含 OrderID, CustomerID, EmployeeID, OrderDate, ShippedDate, ShipCountry。"

# Behavior check: turn 4 should call draft_od_create NOT propose_od
if ! turn_has_tool 4 "draft_od_create"; then
  add_issue "Turn 4: agent did NOT call draft_od_create — may have used wrong tool or skipped draft creation"
fi
turn_has_banned_tool 4
# Check for draft_property_add_or_update calls
if ! turn_has_tool 4 "draft_property_add_or_update"; then
  add_issue "Turn 4: agent did NOT call draft_property_add_or_update — properties may not have been added"
fi

echo ""
echo "── Phase 3: FK inspection + draft link ──"

send_turn 5 "请 inspect fk_candidates 看 Orders 和 Customers 之间的 FK 关系。"

if ! turn_has_tool 5 "inspect"; then
  add_issue "Turn 5: agent did NOT call inspect(mode=fk_candidates) when explicitly asked"
fi
turn_has_banned_tool 5

send_turn 6 "如果有合理的 link 候选，propose 一个 draft_link，把 NorthwindOrder.CustomerID 和已经存在的某个 Customer OD 连起来。"

# Behavior check: should use draft_link_create_or_update NOT propose_link
if ! turn_has_tool 6 "draft_link_create_or_update"; then
  add_issue "Turn 6: agent did NOT call draft_link_create_or_update — may have used propose_link (banned) or skipped"
fi
turn_has_banned_tool 6

echo ""
echo "── Phase 4: Metric Intent ──"

send_turn 7 "然后我想要一个 Metric Intent：按月统计订单数。用 draft_intent_create_or_update。"

if ! turn_has_tool 7 "draft_intent_create_or_update"; then
  add_issue "Turn 7: agent did NOT call draft_intent_create_or_update when explicitly asked"
fi
turn_has_banned_tool 7

echo ""
echo "── Phase 5: Validation ──"

send_turn 8 "好，现在帮我跑一下 5 个 validator。从 validate_semantic_sql 开始。"

# Check validator calls
for v in "validate_semantic_sql" "validate_grain" "validate_referential_integrity" "validate_machine_code" "validate_intent_runs"; do
  if ! turn_has_tool 8 "$v"; then
    add_issue "Turn 8: validator '$v' was NOT called — incomplete validation run"
  fi
done
turn_has_banned_tool 8

# Extract validation outcomes from SSE
VALIDATOR_RESULTS="{}"
if [ -f "$OUTDIR/turn-8.sse" ]; then
  # Look for validation_completed events
  VC_LINES=$(grep 'validation_completed\|"passed"' "$OUTDIR/turn-8.sse" 2>/dev/null | head -20 || echo "")
  if [ -n "$VC_LINES" ]; then
    # Try to extract pass/fail from workspace after turn 8
    VALIDATOR_RESULTS=$(cat "$OUTDIR/turn-8-workspace.json" 2>/dev/null \
      | jq '{findings: [.findings[]? | select(.kind=="validation") | {id:.id, content:.content}]} // {}' \
      2>/dev/null || echo "{}")
  fi
fi

echo ""
echo "── Phase 6: Ship bundle ──"

send_turn 9 "如果 validator 都通过了，请尝试 ship_bundle。如果失败，告诉我哪个 validator 红了。"

SHIP_OUTCOME="not_attempted"
if turn_has_tool 9 "ship_bundle"; then
  # Parse ship outcome from SSE
  if grep -q '"shipped":true' "$OUTDIR/turn-9.sse" 2>/dev/null; then
    SHIP_OUTCOME="passed"
  elif grep -q '"blocked_by_validation"' "$OUTDIR/turn-9.sse" 2>/dev/null || grep -q '"shipped":false' "$OUTDIR/turn-9.sse" 2>/dev/null; then
    SHIP_OUTCOME="blocked_by_validation"
  elif grep -q '"error"' "$OUTDIR/turn-9.sse" 2>/dev/null; then
    SHIP_OUTCOME="errored"
    add_issue "Turn 9: ship_bundle returned an error — check turn-9.sse for details"
  else
    SHIP_OUTCOME="unknown"
  fi
else
  add_issue "Turn 9: agent did NOT call ship_bundle when asked — may have predicted failure and skipped"
  SHIP_OUTCOME="agent_skipped"
fi
turn_has_banned_tool 9

send_turn 10 "如果 ship 失败了 —— 请用 force_ship_bundle 试试，rationale 写 'dogfood test of MVP'。"

FORCE_SHIP_OUTCOME="not_attempted"
if turn_has_tool 10 "force_ship_bundle"; then
  if grep -q '"shipped":true' "$OUTDIR/turn-10.sse" 2>/dev/null; then
    FORCE_SHIP_OUTCOME="force_shipped"
    SHIP_OUTCOME="force_shipped"
  elif grep -q '"error"' "$OUTDIR/turn-10.sse" 2>/dev/null; then
    FORCE_SHIP_OUTCOME="errored"
    add_issue "Turn 10: force_ship_bundle returned an error"
  else
    FORCE_SHIP_OUTCOME="unknown"
  fi
else
  FORCE_SHIP_OUTCOME="not_called"
fi
turn_has_banned_tool 10

# ── Analyze final workspace state ─────────────────────────────────────────────
echo ""
echo "── Analyzing final workspace state ──"

FINAL_WS="$OUTDIR/turn-10-workspace.json"
if [ ! -f "$FINAL_WS" ] || [ "$(cat "$FINAL_WS")" = '{"error":"no threadId yet"}' ]; then
  FINAL_WS="$OUTDIR/turn-9-workspace.json"
fi

FINDINGS_COUNT=0
DECISIONS_COUNT=0
DRAFT_OD_COUNT=0
DRAFT_PROPERTY_COUNT=0
DRAFT_LINK_COUNT=0
DRAFT_INTENT_COUNT=0
ACTIVATED_ENTITIES="{}"

if [ -f "$FINAL_WS" ]; then
  FINDINGS_COUNT=$(jq '.counts.findings // 0' "$FINAL_WS" 2>/dev/null || echo 0)
  DECISIONS_COUNT=$(jq '.counts.decisions // 0' "$FINAL_WS" 2>/dev/null || echo 0)
  DRAFT_OD_COUNT=$(jq '(.draftOds | length) // 0' "$FINAL_WS" 2>/dev/null || echo 0)
  DRAFT_PROPERTY_COUNT=$(jq '(.draftProperties | length) // 0' "$FINAL_WS" 2>/dev/null || echo 0)
  DRAFT_LINK_COUNT=$(jq '(.draftLinks | length) // 0' "$FINAL_WS" 2>/dev/null || echo 0)
  DRAFT_INTENT_COUNT=$(jq '(.draftIntents | length) // 0' "$FINAL_WS" 2>/dev/null || echo 0)

  # Check decisions have rationale
  EMPTY_RATIONALE=$(jq '[.decisions[]? | select(.rationale == "" or .rationale == null)] | length' "$FINAL_WS" 2>/dev/null || echo 0)
  if [ "$EMPTY_RATIONALE" -gt 0 ]; then
    add_issue "Workspace: $EMPTY_RATIONALE decision(s) have empty rationale — audit trail degraded"
  fi

  # Check if findings count is suspiciously low
  if [ "$FINDINGS_COUNT" -eq 0 ] && [ "$TURNS_EXECUTED" -ge 5 ]; then
    add_issue "Workspace: 0 findings after $TURNS_EXECUTED turns — agent may not be persisting discoveries to workspace"
  fi

  # Check if decisions reflect business context
  if [ "$DECISIONS_COUNT" -eq 0 ] && [ "$TURNS_EXECUTED" -ge 3 ]; then
    add_issue "Workspace: 0 decisions after $TURNS_EXECUTED turns — agent not logging decisions (mandatory per prompt)"
  fi

  # Check if activated entities exist (if ship succeeded)
  if [ "$SHIP_OUTCOME" = "passed" ] || [ "$SHIP_OUTCOME" = "force_shipped" ]; then
    ACTIVATED_ENTITIES=$(jq '.activatedEntities // {}' "$FINAL_WS" 2>/dev/null || echo "{}")
  fi
fi

# Token economy: look for usage events across all SSE files
TOTAL_TOKENS=0
for i in $(seq 1 10); do
  SSE="$OUTDIR/turn-${i}.sse"
  if [ -f "$SSE" ]; then
    T=$(grep -oE '"total_tokens":[0-9]+' "$SSE" 2>/dev/null | grep -oE '[0-9]+' | paste -sd+ | bc 2>/dev/null || echo 0)
    TOTAL_TOKENS=$((TOTAL_TOKENS + T))
  fi
done

if [ "$TOTAL_TOKENS" -gt 50000 ]; then
  add_issue "Token economy: $TOTAL_TOKENS total tokens across 10 turns — may indicate context bloat / no ledger compression"
fi

# Check if expand_workspace_to_multi was called (should be user-confirmed, not proactive)
EXPAND_CALLED=false
for i in $(seq 1 10); do
  if [ -f "$OUTDIR/turn-${i}.sse" ] && grep -q '"expand_workspace_to_multi"' "$OUTDIR/turn-${i}.sse" 2>/dev/null; then
    EXPAND_CALLED=true
    add_issue "Turn ${i}: expand_workspace_to_multi was called — verify user explicitly requested this (should not be proactive)"
    break
  fi
done

# ── Build tool_calls_observed JSON ────────────────────────────────────────────
TOOL_CALLS_JSON="["
for i in "${!TOOL_CALLS_PER_TURN[@]}"; do
  TURN_N=$((i + 1))
  TOOLS="${TOOL_CALLS_PER_TURN[$i]}"
  # Convert comma-separated to JSON array
  TOOLS_ARR=$(echo "$TOOLS" | jq -R 'split(",") | map(select(. != ""))' 2>/dev/null || echo '[]')
  TOOL_CALLS_JSON+=$(jq -n --argjson n "$TURN_N" --argjson t "$TOOLS_ARR" '{turn:$n,tools:$t}')
  if [ "$TURN_N" -lt "${#TOOL_CALLS_PER_TURN[@]}" ]; then
    TOOL_CALLS_JSON+=","
  fi
done
TOOL_CALLS_JSON+="]"

# ── Build issues JSON array ───────────────────────────────────────────────────
ISSUES_JSON="["
for i in "${!ISSUES[@]}"; do
  ISSUES_JSON+=$(echo "${ISSUES[$i]}" | jq -R '.')
  if [ "$i" -lt $((${#ISSUES[@]} - 1)) ]; then
    ISSUES_JSON+=","
  fi
done
ISSUES_JSON+="]"

# ── Write summary.json ────────────────────────────────────────────────────────
jq -n \
  --arg ts "$TS" \
  --arg threadId "$THREAD_ID" \
  --argjson turns "$TURNS_EXECUTED" \
  --argjson toolCalls "$TOOL_CALLS_JSON" \
  --argjson findings "$FINDINGS_COUNT" \
  --argjson decisions "$DECISIONS_COUNT" \
  --argjson draftOds "$DRAFT_OD_COUNT" \
  --argjson draftProps "$DRAFT_PROPERTY_COUNT" \
  --argjson draftLinks "$DRAFT_LINK_COUNT" \
  --argjson draftIntents "$DRAFT_INTENT_COUNT" \
  --arg shipOutcome "$SHIP_OUTCOME" \
  --arg forceShipOutcome "$FORCE_SHIP_OUTCOME" \
  --argjson activatedEntities "$ACTIVATED_ENTITIES" \
  --argjson issues "$ISSUES_JSON" \
  --argjson totalTokens "$TOTAL_TOKENS" \
  --argjson expandCalled "$EXPAND_CALLED" \
  '{
    timestamp: $ts,
    threadId: $threadId,
    turns_executed: $turns,
    tool_calls_observed: $toolCalls,
    findings_count: $findings,
    decisions_count: $decisions,
    draft_od_count: $draftOds,
    draft_property_count: $draftProps,
    draft_link_count: $draftLinks,
    draft_intent_count: $draftIntents,
    validator_results: "see turn-8-workspace.json findings (kind=validation)",
    ship_bundle_outcome: $shipOutcome,
    force_ship_outcome: $forceShipOutcome,
    activated_entities: $activatedEntities,
    token_economy: {total_tokens_across_turns: $totalTokens},
    expand_workspace_to_multi_called: $expandCalled,
    issues_observed: $issues
  }' \
  > "$OUTDIR/summary.json"

# ── Print final report ────────────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════════════════════"
echo "  DOGFOOD ANALYST — NORTHWIND RESULTS"
echo "════════════════════════════════════════════════════════"
echo "   threadId:        $THREAD_ID"
echo "   turns executed:  $TURNS_EXECUTED / 10"
echo "   findings:        $FINDINGS_COUNT"
echo "   decisions:       $DECISIONS_COUNT"
echo "   draft ODs:       $DRAFT_OD_COUNT"
echo "   draft props:     $DRAFT_PROPERTY_COUNT"
echo "   draft links:     $DRAFT_LINK_COUNT"
echo "   draft intents:   $DRAFT_INTENT_COUNT"
echo "   ship outcome:    $SHIP_OUTCOME"
echo "   force ship:      $FORCE_SHIP_OUTCOME"
echo "   total tokens:    $TOTAL_TOKENS"
echo "   issues found:    ${#ISSUES[@]}"
echo ""
if [ "${#ISSUES[@]}" -gt 0 ]; then
  echo "   Issues:"
  for issue in "${ISSUES[@]}"; do
    echo "     - $issue"
  done
  echo ""
fi
echo "   summary.json:    $OUTDIR/summary.json"
echo ""
echo "   Exit 0 (probe complete, regardless of individual turn outcomes)"
exit 0
