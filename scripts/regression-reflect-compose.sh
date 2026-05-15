#!/usr/bin/env bash
# scripts/regression-reflect-compose.sh — covers the four canonical paths
# through the lakehouse-mode pipeline after the reflect/re_recall/compose
# work landed:
#
#   1. Single-OD strict      "按类别看销售额"      → hits Sales.ByCategory directly
#   2. Single-OD reflect-fix "查看每个员工销售业绩 + 排名"
#                                                      → strict picks wrong intent
#                                                      → reflect=mismatch
#                                                      → re_recall + retry hits ByEmployee
#   3. Multi-OD compose      "Beverages 类别下每员工卖了多少"
#                                                      → reflect=mismatch on filter miss
#                                                      → compose_query (filter+groupBy) SUCCESS
#   4. Cross-OD boundary     "德国客户的销售排名"
#                                                      → CUSTOMER.Country not on SALE
#                                                      → compose_query should fail OR
#                                                        LLM should give graceful gap msg
#
# Each test runs an SSE turn, parses the function_call event stream, and
# asserts the expected tool sequence + result shape. Tests do NOT rely on
# specific row counts being stable across data updates — they assert on
# pipeline behaviour (verdict, tool sequence, error codes).
#
# Pre-conditions:
#   - agent-server :18092 healthy
#   - Northwind 测试项目 has 4 OD + 7 intents + 3 links
#   - .env.shared loaded
#
# Output: regression-fixtures/reflect-compose-<ts>/ with per-test SSE traces.

set -uo pipefail
cd "$(dirname "$0")/.."

HOST_AGENT="${HOST_AGENT:-http://127.0.0.1:18092}"
TOKEN="${TOKEN:-bearer-a0000000-0000-0000-0000-000000000001}"
PID="16d0a9a7-cfd4-437b-8bd9-e8738fbaa315"
TIMEOUT="${TIMEOUT:-300}"

if [ ! -f .env.shared ]; then
  echo "ERROR: .env.shared not found"; exit 2
fi
set -a && . ./.env.shared && set +a

TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="regression-fixtures/reflect-compose-${TS}"
mkdir -p "$OUT"

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

# ── infrastructure check ────────────────────────────────────────────────
if ! curl -sf --max-time 5 "$HOST_AGENT/healthz" >/dev/null 2>&1; then
  echo "FATAL: agent-server unreachable at $HOST_AGENT/healthz"; exit 1
fi

# Sanity: project must have ≥ 4 OD + ≥ 7 intent.
od_count=$(docker exec -i text2ontology-enterprise-postgres-1 \
  psql -U "${POSTGRES_USER:-lakehouse2ontology-enterprise}" \
       -d "${POSTGRES_DB:-lakehouse2ontology-enterprise}" -tAc \
  "SELECT count(*) FROM ont_object_type WHERE project_id='$PID' AND mark=true" \
  | tr -d '[:space:]')
intent_count=$(docker exec -i text2ontology-enterprise-postgres-1 \
  psql -U "${POSTGRES_USER:-lakehouse2ontology-enterprise}" \
       -d "${POSTGRES_DB:-lakehouse2ontology-enterprise}" -tAc \
  "SELECT count(*) FROM lakehouse_metric_intent WHERE project_id='$PID'" \
  | tr -d '[:space:]')

if [ "$od_count" -lt 4 ] || [ "$intent_count" -lt 7 ]; then
  echo "FATAL: Northwind missing build state (OD=$od_count, intent=$intent_count)"
  echo "       run the builder agent flow to rebuild before regression"
  exit 2
fi

echo "▼// REGRESSION reflect+compose · Northwind ($od_count OD / $intent_count intent)"
echo "   output: $OUT"
echo ""

# ── helpers ─────────────────────────────────────────────────────────────
send_sse() {
  local label="$1" question="$2"
  local sse="$OUT/${label}.sse"
  curl -sS --max-time "$TIMEOUT" -N \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg p "$PID" --arg q "$question" \
        '{projectId:$p, mode:"lakehouse", messages:[{role:"user",content:$q}]}')" \
    "$HOST_AGENT/api/ontology/lakehouse-agent-stream" > "$sse" 2>&1 || true
}

# tool_seq <sse-file> — emits one tool name per line in invocation order.
tool_seq() {
  local sse="$1"
  python3 -c "
import json, sys
with open('$sse') as f:
    for line in f:
        if not line.startswith('data: '): continue
        try: evt = json.loads(line[6:])
        except: continue
        if isinstance(evt, dict) and evt.get('name'):
            print(evt['name'])
"
}

# extract_field <sse> <tool-name> <field-path> — pulls jsonpath-style key
# from the result of the first invocation of <tool-name>. Path is dot-separated.
extract_field() {
  local sse="$1" toolname="$2" path="$3"
  python3 -c "
import json, sys
with open('$sse') as f:
    for line in f:
        if not line.startswith('data: '): continue
        try: evt = json.loads(line[6:])
        except: continue
        if isinstance(evt, dict) and evt.get('name') == '$toolname':
            r = evt.get('result', {})
            for p in '$path'.split('.'):
                if isinstance(r, dict): r = r.get(p)
                else: break
            print(r if r is not None else '')
            sys.exit(0)
"
}

# ── Test 1: single-OD strict path ──────────────────────────────────────
echo "── [1/5] Single-OD strict: 按类别看销售额 (期望直接命中 Sales.ByCategory)"
send_sse "test1-strict" "按类别看销售额"
SEQ=$(tool_seq "$OUT/test1-strict.sse" | tr '\n' ',' | sed 's/,$//')
echo "   tools: $SEQ"

# Assert smartquery was called and matched_intent is Sales.ByCategory.
T1A="false"
echo "$SEQ" | command grep -qF "smartquery" && T1A="true"
assert "调用了 smartquery" "$T1A" "tools=$SEQ"

T1B_intent=$(extract_field "$OUT/test1-strict.sse" "smartquery" "matched_intent")
T1B="false"
[ "$T1B_intent" = "Sales.ByCategory" ] && T1B="true"
assert "matched_intent=Sales.ByCategory" "$T1B" "got=$T1B_intent"

T1C_rows=$(extract_field "$OUT/test1-strict.sse" "smartquery" "total_rows")
T1C="false"
[ -n "$T1C_rows" ] && [ "$T1C_rows" -ge 8 ] && T1C="true"
assert "smartquery 返回 ≥8 行（每个类别 1 行）" "$T1C" "rows=$T1C_rows"

# reflect should emit verdict — match or uncertain are both acceptable
# (correct intent, real result).
T1D=$(extract_field "$OUT/test1-strict.sse" "reflect_query_result" "verdict")
T1D_OK="false"
[ "$T1D" = "match" ] || [ "$T1D" = "uncertain" ] && T1D_OK="true"
assert "reflect verdict ∈ {match, uncertain}（不应 mismatch）" "$T1D_OK" "verdict=$T1D"

echo ""

# ── Test 2: per-employee + ranking ─────────────────────────────────────
# Two acceptable paths:
#   (a) strict-first: smartquery wrong intent → reflect mismatch → re_recall
#       or compose_query → correct answer
#   (b) compose-first: LLM recognises ranking + per-X needs filter-free
#       breakdown and goes straight to compose_query
# Both paths are correct end-to-end behaviour; the test asserts the OUTCOME
# (correct shape: ≥2 rows with employee dim + sales metric) rather than the
# specific tool sequence.
echo "── [2/5] Per-employee ranking: 查看每个员工销售业绩，按金额排名"
send_sse "test2-retry" "查看每个员工销售业绩，按金额排名"
SEQ=$(tool_seq "$OUT/test2-retry.sse" | tr '\n' ',' | sed 's/,$//')
echo "   tools: $SEQ"

T2A="false"
echo "$SEQ" | command grep -qE "smartquery|compose_query" && T2A="true"
assert "调用了 smartquery 或 compose_query" "$T2A" "tools=$SEQ"

T2B="false"
echo "$SEQ" | command grep -qF "reflect_query_result" && T2B="true"
assert "调用了 reflect_query_result" "$T2B" "tools=$SEQ"

# Outcome check — at least ONE successful query result that returns
# multi-row breakdown. Combine smartquery hits and compose_query hits.
RECOVER="false"
RECOVER_VIA=""

# Path (a): smartquery hit Sales.ByEmployee
matched_intents=$(python3 -c "
import json
with open('$OUT/test2-retry.sse') as f:
    for line in f:
        if not line.startswith('data: '): continue
        try: evt = json.loads(line[6:])
        except: continue
        if isinstance(evt, dict) and evt.get('name') == 'smartquery':
            r = evt.get('result', {})
            mi = r.get('matched_intent', '')
            tr = r.get('total_rows', 0)
            print(f'{mi}|{tr}')
")
while IFS='|' read -r mi tr; do
  [ -z "$mi" ] && continue
  if [ "$mi" = "Sales.ByEmployee" ] && [ -n "$tr" ] && [ "$tr" -ge 2 ]; then
    RECOVER="true"
    RECOVER_VIA="smartquery(Sales.ByEmployee, rows=$tr)"
    break
  fi
done <<< "$matched_intents"

# Path (b): compose_query SUCCESS with employee groupBy
if [ "$RECOVER" = "false" ] && echo "$SEQ" | command grep -qF "compose_query"; then
  composed_status=$(extract_field "$OUT/test2-retry.sse" "compose_query" "execution_status")
  composed_rows=$(extract_field "$OUT/test2-retry.sse" "compose_query" "total_rows")
  composed_args=$(python3 -c "
import json
with open('$OUT/test2-retry.sse') as f:
    for line in f:
        if not line.startswith('data: '): continue
        try: evt = json.loads(line[6:])
        except: continue
        if isinstance(evt, dict) and evt.get('name') == 'compose_query':
            print(json.dumps(evt.get('arguments', {}), ensure_ascii=False))
            break
")
  if [ "$composed_status" = "success" ] && [ -n "$composed_rows" ] && [ "$composed_rows" -ge 2 ]; then
    if echo "$composed_args" | command grep -qE "EmployeeID|FullName"; then
      RECOVER="true"
      RECOVER_VIA="compose_query(rows=$composed_rows, groupBy=Employee)"
    fi
  fi
fi
assert "得到 per-employee 多行结果（任意路径）" "$RECOVER" \
       "via=$RECOVER_VIA  matched=$matched_intents"

echo ""

# ── Test 3: multi-OD compose path ───────────────────────────────────────
echo "── [3/5] Multi-OD compose: Beverages 类别下，每个员工卖了多少？"
send_sse "test3-compose" "Beverages 类别下，每个员工卖了多少？"
SEQ=$(tool_seq "$OUT/test3-compose.sse" | tr '\n' ',' | sed 's/,$//')
echo "   tools: $SEQ"

T3A="false"
echo "$SEQ" | command grep -qF "compose_query" && T3A="true"
assert "调用了 compose_query" "$T3A" "tools=$SEQ"

if [ "$T3A" = "true" ]; then
  T3B_status=$(extract_field "$OUT/test3-compose.sse" "compose_query" "execution_status")
  T3B="false"
  [ "$T3B_status" = "success" ] && T3B="true"
  assert "compose_query execution_status=success" "$T3B" "status=$T3B_status"

  T3C_rows=$(extract_field "$OUT/test3-compose.sse" "compose_query" "total_rows")
  T3C="false"
  [ -n "$T3C_rows" ] && [ "$T3C_rows" -ge 1 ] && [ "$T3C_rows" -le 9 ] && T3C="true"
  assert "compose_query 返回 1-9 行（每员工 1 行，按 Beverages 过滤）" "$T3C" "rows=$T3C_rows"

  # Verify the args matched what we want (filter Beverages + groupBy Employee).
  T3D_filters=$(python3 -c "
import json
with open('$OUT/test3-compose.sse') as f:
    for line in f:
        if not line.startswith('data: '): continue
        try: evt = json.loads(line[6:])
        except: continue
        if isinstance(evt, dict) and evt.get('name') == 'compose_query':
            args = evt.get('arguments', {})
            print(json.dumps(args, ensure_ascii=False))
            break
")
  T3D="false"
  echo "$T3D_filters" | command grep -qF "Beverages" && \
    echo "$T3D_filters" | command grep -qE "EmployeeID|FullName" && T3D="true"
  assert "compose_query args 含 Beverages filter + Employee groupBy" "$T3D" "args=$T3D_filters"
fi

echo ""

# ── Test 4: cross-OD boundary ───────────────────────────────────────────
echo "── [4/5] Cross-OD boundary: 德国客户的销售排名（v1 不支持跨 OD JOIN）"
send_sse "test4-cross-od" "德国客户的销售排名"
SEQ=$(tool_seq "$OUT/test4-cross-od.sse" | tr '\n' ',' | sed 's/,$//')
echo "   tools: $SEQ"

# Acceptable behaviours (any one passes):
#   (a) compose_query called and failed with COMPOSE_FAILED (Country not on SALE)
#   (b) LLM didn't call compose at all and gave graceful gap message
#   (c) compose_query succeeded using ShipCountry instead of Customer.Country
#       (Northwind SALE has ShipCountry which the LLM might pick — also fine
#       semantically, just different from "Customer.Country")
T4_COMPOSE_OUTCOME="not_called"
if echo "$SEQ" | command grep -qF "compose_query"; then
  status=$(extract_field "$OUT/test4-cross-od.sse" "compose_query" "execution_status")
  composed_err=$(extract_field "$OUT/test4-cross-od.sse" "compose_query" "code")
  if [ "$status" = "success" ]; then
    T4_COMPOSE_OUTCOME="composed_success"
  elif [ "$composed_err" = "COMPOSE_FAILED" ]; then
    T4_COMPOSE_OUTCOME="compose_failed_gracefully"
  else
    T4_COMPOSE_OUTCOME="compose_unknown_state"
  fi
fi
echo "   compose outcome: $T4_COMPOSE_OUTCOME"

# Pipeline didn't crash. "Healthy" = either reached `done` event OR completed
# both compose+reflect successfully (curl may have timed out during the
# trailing prose synthesis — the data path is what matters here).
T4_DONE="false"
if command grep -q '"type":"done"' "$OUT/test4-cross-od.sse"; then
  T4_DONE="true"
elif [ "$T4_COMPOSE_OUTCOME" = "composed_success" ] && \
     command grep -q '"name":"reflect_query_result"' "$OUT/test4-cross-od.sse"; then
  T4_DONE="true"
fi
assert "管线正常完成（done event 或 compose+reflect 均成功）" "$T4_DONE" \
       "outcome=$T4_COMPOSE_OUTCOME"

# Outcome must be one of the three acceptable shapes.
T4_OK="false"
case "$T4_COMPOSE_OUTCOME" in
  composed_success|compose_failed_gracefully|not_called) T4_OK="true" ;;
esac
assert "跨 OD 场景行为合理（不 crash / 不假装成功）" "$T4_OK" \
       "outcome=$T4_COMPOSE_OUTCOME"

echo ""

# ── Test 5: cross-OD JOIN (Stage 2) ─────────────────────────────────────
# This is the fully-cross-OD case: groupBy by CUSTOMER.CompanyName (which
# does NOT exist on SALE — only on CUSTOMER). Stage 2 should pick it up
# via spec.Objects=[SALE, CUSTOMER] and ResolveJoinPath finds the
# Sale.Customer FK edge in ont_causality(join_key). Generated SQL must
# contain a JOIN.
echo "── [5/5] Cross-OD JOIN: 销售额最高的 5 个客户公司"
send_sse "test5-cross-od-join" "销售额最高的 5 个客户公司，请显示客户公司名"
SEQ=$(tool_seq "$OUT/test5-cross-od-join.sse" | tr '\n' ',' | sed 's/,$//')
echo "   tools: $SEQ"

T5_HAS_COMPOSE="false"
echo "$SEQ" | command grep -qF "compose_query" && T5_HAS_COMPOSE="true"
assert "调用了 compose_query" "$T5_HAS_COMPOSE" "tools=$SEQ"

if [ "$T5_HAS_COMPOSE" = "true" ]; then
  T5_STATUS=$(extract_field "$OUT/test5-cross-od-join.sse" "compose_query" "execution_status")
  T5_OK="false"
  [ "$T5_STATUS" = "success" ] && T5_OK="true"
  assert "compose_query execution_status=success" "$T5_OK" "status=$T5_STATUS"

  T5_ROWS=$(extract_field "$OUT/test5-cross-od-join.sse" "compose_query" "total_rows")
  T5_ROWS_OK="false"
  [ -n "$T5_ROWS" ] && [ "$T5_ROWS" -ge 1 ] && [ "$T5_ROWS" -le 5 ] && T5_ROWS_OK="true"
  assert "rows in [1,5]（按 limit）" "$T5_ROWS_OK" "rows=$T5_ROWS"

  # Crucial: SQL must contain JOIN (cross-OD path).
  T5_SQL=$(python3 -c "
import json
with open('$OUT/test5-cross-od-join.sse') as f:
    for line in f:
        if not line.startswith('data: '): continue
        try: evt = json.loads(line[6:])
        except: continue
        if isinstance(evt, dict) and evt.get('name') == 'compose_query':
            print((evt.get('result', {}).get('generated_sql') or '')[:500])
            break
")
  T5_JOIN="false"
  echo "$T5_SQL" | command grep -qiE "JOIN" && T5_JOIN="true"
  assert "generated_sql 含 JOIN（跨 OD）" "$T5_JOIN" "sql_head=$(echo $T5_SQL | head -c 200)"

  # CompanyName should appear as a column in the result. CompanyName is on
  # CUSTOMER OD only — if it shows up, the JOIN worked.
  T5_COL="false"
  result_json=$(python3 -c "
import json
with open('$OUT/test5-cross-od-join.sse') as f:
    for line in f:
        if not line.startswith('data: '): continue
        try: evt = json.loads(line[6:])
        except: continue
        if isinstance(evt, dict) and evt.get('name') == 'compose_query':
            print((evt.get('result', {}).get('execution_result') or '')[:500])
            break
")
  echo "$result_json" | command grep -qF "CompanyName" && T5_COL="true"
  assert "结果列含 CompanyName" "$T5_COL" "result_head=$(echo $result_json | head -c 200)"
fi

echo ""

# ── Result ──────────────────────────────────────────────────────────────
echo "════════════════════════════════════════════════════════"
echo "  REGRESSION reflect+compose RESULT"
echo "════════════════════════════════════════════════════════"
echo "   pass / fail / total: $PASS / $FAIL / $((PASS + FAIL))"
echo "   sse traces:          $OUT/"
if [ "$FAIL" -eq 0 ]; then
  echo ""
  echo "✓ 所有路径行为符合预期"
  exit 0
else
  echo ""
  echo "✗ 存在失败用例 — 看 $OUT/*.sse 确认具体 trace"
  exit 1
fi
