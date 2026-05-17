#!/usr/bin/env bash
# scripts/e2e-smoke.sh — End-to-end smoke test of the import → build → ask flow.
#
# Simulates a human operator driving the stack purely through public HTTP APIs:
#   1. mint an auth token  (HMAC, no password needed — reads AUTH_TOKEN_SECRET)
#   2. create a fresh project
#   3. upload a SQLite file        POST /api/connector/sqlite/sources
#   4. sync all tables to staging  POST /api/connector/sqlite/sources/{id}/sync   (SSE)
#   5. confirm the wizard          POST /api/connector/wizard/{id}/confirm
#   6. verify rows landed in proj_<hex>.<table>  (direct psql)
#   7. ask the Lakehouse Agent a question  POST /api/ontology/lakehouse-agent-stream (SSE)
#
# Talks DIRECTLY to each service port (bypasses nginx). All ports require the
# same Bearer token (authmw is mounted on every service).
#
# Usage:   scripts/e2e-smoke.sh [path/to/database.sqlite]
# Default db: extracted from the running collector-server's existing upload.
#
# Exit code: 0 = full flow OK, 1 = a step failed.

set -uo pipefail
cd "$(dirname "$0")/.."

# ── Config ───────────────────────────────────────────────────────────────────
API="${API:-http://127.0.0.1:18090}"        # backend-api
COLLECTOR="${COLLECTOR:-http://127.0.0.1:18096}"
AGENT="${AGENT:-http://127.0.0.1:18092}"
ADMIN_ID="a0000000-0000-0000-0000-000000000001"
ENV_FILE=".env.shared"
SYNC_TIMEOUT="${SYNC_TIMEOUT:-1800}"        # 30 min — large DBs copy row-by-row
ASK_TIMEOUT="${ASK_TIMEOUT:-300}"
QUESTION="${QUESTION:-Which 3 employees handled the most orders? Please list their full names and order counts, in English.}"
EXPECT_IN_ANSWER="${EXPECT_IN_ANSWER:-Peacock}"

PSQL=(docker compose --env-file "$ENV_FILE" exec -T postgres
      psql -U lakehouse2ontology-enterprise -d lakehouse2ontology-enterprise -tA)

say()  { printf '\n\033[1m▼// %s\033[0m\n' "$*"; }
ok()   { printf '   \033[32m✓\033[0m %s\n' "$*"; }
die()  { printf '   \033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

# ── Step 0: prerequisites ────────────────────────────────────────────────────
say "STEP 0 — prerequisites"
for tool in curl jq openssl docker; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool not installed"
done
[[ -f "$ENV_FILE" ]] || die "$ENV_FILE not found (run from repo root)"
SECRET=$(grep -E '^AUTH_TOKEN_SECRET=' "$ENV_FILE" | cut -d= -f2-)
[[ -n "$SECRET" ]] || die "AUTH_TOKEN_SECRET missing from $ENV_FILE"
for name in backend-api:"$API" collector-server:"$COLLECTOR" agent-server:"$AGENT"; do
  svc=${name%%:*}; url=${name#*:}
  curl -sf --max-time 5 "$url/healthz" >/dev/null 2>&1 \
    && ok "$svc reachable" || die "$svc unreachable at $url/healthz"
done

# Resolve the SQLite file to upload.
DB_FILE="${1:-}"
if [[ -z "$DB_FILE" ]]; then
  DB_FILE="/tmp/e2e-smoke-source.db"
  DSPATH=$("${PSQL[@]}" -c \
    "SELECT config_json->>'disk_path' FROM data_source WHERE type='sqlite' ORDER BY created_at LIMIT 1;" \
    2>/dev/null | tr -d '[:space:]')
  [[ -n "$DSPATH" ]] || die "no SQLite file given and none found in data_source — pass a path as \$1"
  CID=$(docker compose --env-file "$ENV_FILE" ps -q collector-server)
  docker cp "$CID:$DSPATH" "$DB_FILE" >/dev/null 2>&1 \
    || die "could not extract $DSPATH from collector-server"
  ok "using SQLite from running collector: $DSPATH"
fi
[[ -f "$DB_FILE" ]] || die "SQLite file not found: $DB_FILE"
ok "source file: $DB_FILE ($(du -h "$DB_FILE" | cut -f1))"

# ── Step 1: mint token ───────────────────────────────────────────────────────
say "STEP 1 — mint auth token"
EXPIRY=$(( $(date +%s) + 3600 ))
PAYLOAD="${ADMIN_ID}.${EXPIRY}"
SIG=$(printf '%s' "$PAYLOAD" \
  | openssl dgst -sha256 -hmac "$SECRET" -binary \
  | openssl base64 -A | tr '+/' '-_' | tr -d '=')
TOKEN="${PAYLOAD}.${SIG}"
AUTH=(-H "Authorization: Bearer $TOKEN")
ME=$(curl -s "${AUTH[@]}" "$API/api/auth/me")
echo "$ME" | jq -e '.username' >/dev/null 2>&1 \
  && ok "token accepted — logged in as $(echo "$ME" | jq -r '.username')" \
  || die "token rejected by /api/auth/me: $ME"

# ── Step 2: create project ───────────────────────────────────────────────────
say "STEP 2 — create project"
PNAME="e2e-smoke-$(date +%Y%m%d-%H%M%S)"
PROJ=$(curl -s "${AUTH[@]}" -H 'Content-Type: application/json' \
  -X POST "$API/api/projects" \
  -d "{\"name\":\"$PNAME\",\"description\":\"e2e smoke test\",\"sourceType\":\"sqlite\"}")
PROJECT_ID=$(echo "$PROJ" | jq -r '.data.id // empty')
[[ -n "$PROJECT_ID" ]] || die "project create failed: $PROJ"
ok "project created: $PNAME  ($PROJECT_ID)"

# ── Step 3: upload SQLite ────────────────────────────────────────────────────
say "STEP 3 — upload SQLite file"
UP=$(curl -s "${AUTH[@]}" \
  -F "project_id=$PROJECT_ID" \
  -F "label=$PNAME" \
  -F "file=@${DB_FILE};type=application/octet-stream" \
  -X POST "$COLLECTOR/api/connector/sqlite/sources")
DS_ID=$(echo "$UP" | jq -r '.id // empty')
[[ -n "$DS_ID" ]] || die "upload failed: $UP"
TABLES=()
while IFS= read -r _t; do
  [[ -n "$_t" ]] && TABLES+=("$_t")
done < <(echo "$UP" | jq -r '.catalog.tables[].name')
[[ ${#TABLES[@]} -gt 0 ]] || die "upload returned empty catalog: $UP"
ok "uploaded — data_source $DS_ID, ${#TABLES[@]} tables discovered:"
printf '       %s\n' "${TABLES[@]}"

# ── Step 4: sync all tables to staging (SSE) ─────────────────────────────────
say "STEP 4 — sync tables to Postgres staging"
TABLES_JSON=$(printf '%s\n' "${TABLES[@]}" | jq -R . | jq -cs '{tables: .}')
SYNC_LOG=$(mktemp)
curl -sN --max-time "$SYNC_TIMEOUT" "${AUTH[@]}" -H 'Content-Type: application/json' \
  -X POST "$COLLECTOR/api/connector/sqlite/sources/$DS_ID/sync" \
  -d "$TABLES_JSON" > "$SYNC_LOG"
LAST_TABLE=""
while IFS= read -r line; do
  [[ "$line" == data:* ]] || continue
  ev=${line#data: }
  phase=$(echo "$ev" | jq -r '.phase // empty')
  case "$phase" in
    sync_progress)
      t=$(echo "$ev" | jq -r '.table_name'); rows=$(echo "$ev" | jq -r '.rows_synced')
      [[ "$t" != "$LAST_TABLE" ]] && { [[ -n "$LAST_TABLE" ]] && echo; LAST_TABLE="$t"; }
      printf '\r       %-28s %s rows' "$t" "$rows" ;;
    sync_complete) echo; ok "sync complete" ;;
    sync_failed)   echo; die "sync failed: $(echo "$ev" | jq -r '.error')" ;;
  esac
done < "$SYNC_LOG"
grep -q '"phase":"sync_complete"' "$SYNC_LOG" || die "sync did not complete — see $SYNC_LOG"
rm -f "$SYNC_LOG"

# ── Step 5: confirm wizard (staging → proj_<hex>) ────────────────────────────
say "STEP 5 — confirm wizard (build lakehouse)"
CONF=$(curl -s "${AUTH[@]}" -H 'Content-Type: application/json' \
  -X POST "$COLLECTOR/api/connector/wizard/$DS_ID/confirm" -d '{}')
echo "$CONF" | jq -e '.ok == true' >/dev/null 2>&1 \
  && ok "wizard confirmed — status: $(echo "$CONF" | jq -r '.status')" \
  || die "confirm failed: $CONF"

# ── Step 6: verify data landed ───────────────────────────────────────────────
say "STEP 6 — verify lakehouse data"
SCHEMA=$("${PSQL[@]}" -c "SELECT lakehouse_schema FROM project WHERE id='$PROJECT_ID';" | tr -d '[:space:]')
[[ -n "$SCHEMA" ]] || die "project.lakehouse_schema not set after confirm"
ok "lakehouse schema: $SCHEMA"
echo "       table                          rows   numeric-cols"
TOTAL=0
for t in "${TABLES[@]}"; do
  rc=$("${PSQL[@]}" -c "SELECT count(*) FROM \"$SCHEMA\".\"$t\";" 2>/dev/null | tr -d '[:space:]')
  nc=$("${PSQL[@]}" -c "SELECT count(*) FROM information_schema.columns WHERE table_schema='$SCHEMA' AND table_name='$t' AND data_type IN ('integer','numeric','double precision','bigint');" 2>/dev/null | tr -d '[:space:]')
  printf '       %-30s %6s   %s\n' "$t" "${rc:-ERR}" "${nc:-ERR}"
  [[ "$rc" =~ ^[0-9]+$ ]] && TOTAL=$((TOTAL + rc))
done
ok "verified — $TOTAL total rows across ${#TABLES[@]} tables"

# ── Agent helpers ────────────────────────────────────────────────────────────
# sse_array <file> — collect every SSE `data:` line into a JSON array,
# skipping any non-JSON line so one bad frame can't break parsing.
sse_array() { grep '^data: ' "$1" | sed 's/^data: //' | jq -cRn '[inputs|fromjson?]'; }

# agent_post <mode> <messages-json> <out-file> — one streamed turn of the
# unified agent endpoint. Sends threadId only when $THREAD_ID is set.
THREAD_ID=""
agent_post() {
  local mode="$1" msgs="$2" out="$3" body
  body=$(jq -cn --argjson m "$msgs" --arg p "$PROJECT_ID" --arg md "$mode" --arg t "$THREAD_ID" \
    '{messages:$m, projectId:$p, mode:$md} + (if $t=="" then {} else {threadId:$t} end)')
  curl -sN --max-time "$ASK_TIMEOUT" "${AUTH[@]}" -H 'Content-Type: application/json' \
    -X POST "$AGENT/api/ontology/lakehouse-agent-stream" -d "$body" > "$out"
}

# transient_err <file> — echo the SSE error iff it is a retryable network /
# LLM-reachability failure (flaky DNS, dial timeout, upstream EOF). A genuine
# logic error returns empty so the caller fails loudly instead of looping.
transient_err() {
  local e
  e=$(sse_array "$1" | jq -r 'map(select(.type=="error").content)[0] // empty')
  case "$e" in
    *"no such host"*|*"dial tcp"*|*"i/o timeout"*|*"connection refused"*|\
    *"context deadline exceeded"*|*"EOF"*|*"connection reset"*|\
    *"timeout awaiting response headers"*|*"LLM request failed"*) echo "$e" ;;
    *) echo "" ;;
  esac
}

# agent_post_retry — agent_post that retries the SAME turn when the agent
# reports a transient LLM/DNS failure (the LLM provider is reached over the
# public internet; intermittent DNS is an environment fact, not a test bug).
agent_post_retry() {
  local mode="$1" msgs="$2" out="$3" attempt terr
  for attempt in 1 2 3 4 5; do
    agent_post "$mode" "$msgs" "$out"
    terr=$(transient_err "$out")
    [[ -z "$terr" ]] && return 0
    printf '   \033[33m⟳ transient LLM/network error (attempt %s) — retrying in 6s\033[0m\n' "$attempt"
    sleep 6
  done
  return 0   # exhausted retries — caller will surface the persisting error
}

# build_and_activate_od <od-name> <chinese-build-prompt>
# Runs a fresh-thread Builder Agent conversation (the interview enforces
# ≥3 rounds before propose_od) and activates the resulting OD. The function
# uses a `local THREAD_ID` so bash dynamic scoping makes agent_post see this
# OD's own thread, leaving the outer THREAD_ID untouched. Sets the global
# BUILT_OD_ID on success.
BUILT_OD_ID=""
build_and_activate_od() {
  local od_name="$1" first_prompt="$2"
  local THREAD_ID=""             # local scope — visible to agent_post via dynamic scoping
  local msgs='[]' object_id="" tools assist err next_msg turn_sse arr
  next_msg="$first_prompt"
  for turn in 1 2 3 4 5 6; do
    msgs=$(jq -cn --argjson m "$msgs" --arg c "$next_msg" '$m + [{role:"user",content:$c}]')
    turn_sse=$(mktemp)
    agent_post_retry builder "$msgs" "$turn_sse"
    arr=$(sse_array "$turn_sse")
    [[ -z "$THREAD_ID" ]] && THREAD_ID=$(echo "$arr" | jq -r 'map(select(.type=="thread"))[0].threadId // empty')
    err=$(echo "$arr" | jq -r 'map(select(.type=="error").content)[0] // empty')
    [[ -n "$err" ]] && { rm -f "$turn_sse"; die "Builder Agent error on $od_name (turn $turn): $err"; }
    tools=$(echo "$arr" | jq -r 'map(select(.type=="function_call").name)|join(", ")')
    object_id=$(echo "$arr" | jq -r 'map(select(.type=="function_call" and .name=="propose_od"))[-1].result.objectId // empty')
    assist=$(echo "$arr" | jq -r 'map(select(.type=="token").content)|join("")')
    msgs=$(jq -cn --argjson m "$msgs" --arg c "$assist" '$m + [{role:"assistant",content:$c}]')
    rm -f "$turn_sse"
    if [[ -n "$object_id" ]]; then
      ok "$od_name turn $turn — tools[$tools] → propose_od OK, objectId=$object_id"
      break
    fi
    ok "$od_name turn $turn — tools[${tools:-none}], no propose_od yet — nudging"
    next_msg="信息已足够，不要再追问。请立即调用 propose_od 工具创建 ${od_name} 这个 OD。"
  done
  [[ -n "$object_id" ]] || die "Builder Agent did not propose $od_name after 6 turns"

  local act
  act=$(curl -s "${AUTH[@]}" -H 'Content-Type: application/json' \
    -X POST "$API/api/ontology/builder/activate-od" \
    -d "$(jq -cn --arg o "$object_id" --arg p "$PROJECT_ID" '{objectId:$o,projectId:$p,edits:{}}')")
  echo "$act" | jq -e '.success == true' >/dev/null 2>&1 \
    || die "activate-od failed for $od_name: $act"
  ok "$od_name activated — canonical_query trial-run rowCount=$(echo "$act" | jq -r '.rowCount // "?"')"
  BUILT_OD_ID="$object_id"
}

# build_and_activate_link <link-name> <chinese-build-prompt>
# Drives the Builder Agent to call propose_link in a fresh thread, then
# activates via /api/ontology/builder/activate-link. Only counts propose_link
# function_calls that actually returned a linkId (no error result), so a
# validation failure followed by a retry on the next turn is handled cleanly.
BUILT_LINK_ID=""
build_and_activate_link() {
  local link_name="$1" first_prompt="$2"
  local THREAD_ID=""
  local msgs='[]' link_id="" from_prop_id="" to_prop_id="" tools assist err next_msg turn_sse arr
  next_msg="$first_prompt"
  for turn in 1 2 3 4 5 6 7; do
    msgs=$(jq -cn --argjson m "$msgs" --arg c "$next_msg" '$m + [{role:"user",content:$c}]')
    turn_sse=$(mktemp)
    agent_post_retry builder "$msgs" "$turn_sse"
    arr=$(sse_array "$turn_sse")
    [[ -z "$THREAD_ID" ]] && THREAD_ID=$(echo "$arr" | jq -r 'map(select(.type=="thread"))[0].threadId // empty')
    err=$(echo "$arr" | jq -r 'map(select(.type=="error").content)[0] // empty')
    [[ -n "$err" ]] && { rm -f "$turn_sse"; die "Builder Agent error on link $link_name (turn $turn): $err"; }
    tools=$(echo "$arr" | jq -r 'map(select(.type=="function_call").name)|join(", ")')
    link_id=$(echo "$arr" | jq -r 'map(select(.type=="function_call" and .name=="propose_link" and (.result.linkId // null) != null))[-1].result.linkId // empty')
    from_prop_id=$(echo "$arr" | jq -r 'map(select(.type=="function_call" and .name=="propose_link" and (.result.linkId // null) != null))[-1].result.fromPropertyId // empty')
    to_prop_id=$(echo "$arr" | jq -r 'map(select(.type=="function_call" and .name=="propose_link" and (.result.linkId // null) != null))[-1].result.toPropertyId // empty')
    assist=$(echo "$arr" | jq -r 'map(select(.type=="token").content)|join("")')
    msgs=$(jq -cn --argjson m "$msgs" --arg c "$assist" '$m + [{role:"assistant",content:$c}]')
    rm -f "$turn_sse"
    if [[ -n "$link_id" ]]; then
      ok "$link_name turn $turn — tools[$tools] → propose_link OK, linkId=$link_id"
      break
    fi
    ok "$link_name turn $turn — tools[${tools:-none}], no propose_link yet — nudging"
    next_msg="信息已足够，不要再追问。请立即调用 propose_link 工具创建 ${link_name} 这个 Link。如果还没拿到 property.id，请先 list type=ods 取出来。"
  done
  [[ -n "$link_id" && -n "$from_prop_id" && -n "$to_prop_id" ]] \
    || die "Builder Agent did not propose link $link_name after 7 turns"

  local act
  act=$(curl -s "${AUTH[@]}" -H 'Content-Type: application/json' \
    -X POST "$API/api/ontology/builder/activate-link" \
    -d "$(jq -cn --arg l "$link_id" --arg p "$PROJECT_ID" --arg fp "$from_prop_id" --arg tp "$to_prop_id" \
          '{linkId:$l, projectId:$p, fromPropertyId:$fp, toPropertyId:$tp, edits:{}}')")
  echo "$act" | jq -e '.success == true' >/dev/null 2>&1 \
    || die "activate-link failed for $link_name: $act"
  ok "$link_name activated"
  BUILT_LINK_ID="$link_id"
}

# ── Step 7: build ontology via the Builder Agent (multiple ODs, in Chinese) ──
# Drive the Builder Agent with Chinese prompts to create several ODs from the
# lakehouse tables, one OD per fresh-thread conversation. Each activation
# trial-runs the canonical_query so any modelling error surfaces here.
say "STEP 7 — build ontology via Builder Agent (Chinese prompts)"
OD_NAMES=()
OD_IDS=()

# Each entry: <OD_NAME>|<chinese build prompt>. Primary keys + FK columns must
# be exposed as properties so the Builder Agent can later anchor Links to them.
build_and_activate_od "PRODUCT" \
  "请基于湖仓里的 Products 表，创建一个名为 PRODUCT 的本体对象(OD)。属性至少包含 ProductID(产品ID, 主键)、ProductName(产品名)、UnitPrice(单价)、UnitsInStock(库存)。请尽快直接调用 propose_od 完成建模，不要反复追问。"
OD_NAMES+=("PRODUCT"); OD_IDS+=("$BUILT_OD_ID")

build_and_activate_od "CUSTOMER" \
  "请基于湖仓里的 Customers 表，创建一个名为 CUSTOMER 的本体对象(OD)。属性至少包含 CustomerID(客户ID, 主键)、CompanyName(公司名)、ContactName(联系人)、Country(国家)。请尽快直接调用 propose_od 完成建模，不要反复追问。"
OD_NAMES+=("CUSTOMER"); OD_IDS+=("$BUILT_OD_ID")

build_and_activate_od "EMPLOYEE" \
  "请基于湖仓里的 Employees 表，创建一个名为 EMPLOYEE 的本体对象(OD)。属性至少包含 EmployeeID(员工ID, 主键)、FirstName(名)、LastName(姓)、Country(国家)、Title(职位)。请尽快直接调用 propose_od 完成建模，不要反复追问。"
OD_NAMES+=("EMPLOYEE"); OD_IDS+=("$BUILT_OD_ID")

build_and_activate_od "ORDER_ENT" \
  "请基于湖仓里的 Orders 表，创建一个名为 ORDER_ENT 的本体对象(OD)（用 ORDER_ENT 避免 SQL 关键字冲突）。属性至少包含 OrderID(订单ID, 主键)、CustomerID(客户ID, 外键)、EmployeeID(员工ID, 外键)、OrderDate(下单日期)、Freight(运费)。请尽快直接调用 propose_od 完成建模，不要反复追问。"
OD_NAMES+=("ORDER_ENT"); OD_IDS+=("$BUILT_OD_ID")

build_and_activate_od "ORDER_DETAIL" \
  "请基于湖仓里的 \"Order Details\" 表（注意表名有空格，需要带双引号），创建一个名为 ORDER_DETAIL 的本体对象(OD)。属性至少包含 OrderID(订单ID, 外键)、ProductID(产品ID, 外键)、UnitPrice(单价)、Quantity(数量)、Discount(折扣)。请尽快直接调用 propose_od 完成建模，不要反复追问。"
OD_NAMES+=("ORDER_DETAIL"); OD_IDS+=("$BUILT_OD_ID")

ok "built & activated ${#OD_IDS[@]} ODs: ${OD_NAMES[*]}"

# ── Step 7c: build Links between ODs (FK relationships) ──────────────────────
# 4 Links anchor the join graph that makes cross-OD questions answerable:
#
#   ORDER_DETAIL --OrderID--→ ORDER_ENT --CustomerID--→ CUSTOMER
#                  --ProductID--→ PRODUCT     --EmployeeID--→ EMPLOYEE
#
# Each Link is its own Builder-Agent conversation: the agent must call
# list type=ods to discover property IDs, then propose_link with the right
# UUIDs. We then activate via /api/ontology/builder/activate-link.
say "STEP 7c — build Links between ODs (Chinese prompts)"
LINK_NAMES=()
LINK_IDS=()

build_and_activate_link "ORDER_CUSTOMER" \
  "在已激活的 ORDER_ENT 和 CUSTOMER 这两个 OD 之间建立一条 many_to_one Link，名为 ORDER_CUSTOMER，外键列是 ORDER_ENT.CustomerID 指向 CUSTOMER.CustomerID。请：1) 先调用 list type=ods 拿到 ORDER_ENT 和 CUSTOMER 这两个 OD 各自的 property.id (UUID)；2) 立即调用 propose_link，参数 fromObjectId=ORDER_ENT 的 id、toObjectId=CUSTOMER 的 id、fromPropertyId=ORDER_ENT.CustomerID 的 property.id、toPropertyId=CUSTOMER.CustomerID 的 property.id、fkColumn=\"CustomerID\"、linkName=\"ORDER_CUSTOMER\"、cardinality=\"many_to_one\"。不要反复追问。"
LINK_NAMES+=("ORDER_CUSTOMER"); LINK_IDS+=("$BUILT_LINK_ID")

build_and_activate_link "ORDER_EMPLOYEE" \
  "在已激活的 ORDER_ENT 和 EMPLOYEE 这两个 OD 之间建立一条 many_to_one Link，名为 ORDER_EMPLOYEE，外键列是 ORDER_ENT.EmployeeID 指向 EMPLOYEE.EmployeeID。请：1) 先调用 list type=ods 拿到两个 OD 的 property.id；2) 立即调用 propose_link，fromObjectId=ORDER_ENT 的 id、toObjectId=EMPLOYEE 的 id、fromPropertyId=ORDER_ENT.EmployeeID 的 property.id、toPropertyId=EMPLOYEE.EmployeeID 的 property.id、fkColumn=\"EmployeeID\"、linkName=\"ORDER_EMPLOYEE\"、cardinality=\"many_to_one\"。不要反复追问。"
LINK_NAMES+=("ORDER_EMPLOYEE"); LINK_IDS+=("$BUILT_LINK_ID")

build_and_activate_link "OD_ORDER" \
  "在已激活的 ORDER_DETAIL 和 ORDER_ENT 这两个 OD 之间建立一条 many_to_one Link，名为 OD_ORDER，外键列是 ORDER_DETAIL.OrderID 指向 ORDER_ENT.OrderID。请：1) 先调用 list type=ods 拿到两个 OD 的 property.id；2) 立即调用 propose_link，fromObjectId=ORDER_DETAIL 的 id、toObjectId=ORDER_ENT 的 id、fromPropertyId=ORDER_DETAIL.OrderID 的 property.id、toPropertyId=ORDER_ENT.OrderID 的 property.id、fkColumn=\"OrderID\"、linkName=\"OD_ORDER\"、cardinality=\"many_to_one\"。不要反复追问。"
LINK_NAMES+=("OD_ORDER"); LINK_IDS+=("$BUILT_LINK_ID")

build_and_activate_link "OD_PRODUCT" \
  "在已激活的 ORDER_DETAIL 和 PRODUCT 这两个 OD 之间建立一条 many_to_one Link，名为 OD_PRODUCT，外键列是 ORDER_DETAIL.ProductID 指向 PRODUCT.ProductID。请：1) 先调用 list type=ods 拿到两个 OD 的 property.id；2) 立即调用 propose_link，fromObjectId=ORDER_DETAIL 的 id、toObjectId=PRODUCT 的 id、fromPropertyId=ORDER_DETAIL.ProductID 的 property.id、toPropertyId=PRODUCT.ProductID 的 property.id、fkColumn=\"ProductID\"、linkName=\"OD_PRODUCT\"、cardinality=\"many_to_one\"。不要反复追问。"
LINK_NAMES+=("OD_PRODUCT"); LINK_IDS+=("$BUILT_LINK_ID")

ok "built & activated ${#LINK_IDS[@]} Links: ${LINK_NAMES[*]}"

# ── Step 8: ask the Lakehouse Agent ──────────────────────────────────────────
say "STEP 8 — ask the Lakehouse Agent"
echo "       Q: $QUESTION"
echo "       ----------------------------------------------------------------"
THREAD_ID=""
ASK_LOG=$(mktemp)
agent_post_retry lakehouse "$(jq -cn --arg q "$QUESTION" '[{role:"user",content:$q}]')" "$ASK_LOG"
printf '       '
ANSWER="" ; SAW_DONE=0 ; SAW_ERR=""
while IFS= read -r line; do
  [[ "$line" == data:* ]] || continue
  ev=${line#data: }
  typ=$(echo "$ev" | jq -r '.type // empty' 2>/dev/null)
  case "$typ" in
    token)    tok=$(echo "$ev" | jq -r '.content // empty'); ANSWER+="$tok"; printf '%s' "$tok" ;;
    thinking) printf '\033[2m·\033[0m' ;;
    function_call)
      fn=$(echo "$ev" | jq -r '.name // "tool"')
      printf '\n       \033[36m[tool: %s]\033[0m ' "$fn" ;;
    error)    SAW_ERR=$(echo "$ev" | jq -r '.content // "unknown"') ;;
    done)
      SAW_DONE=1
      PT=$(echo "$ev" | jq -r '.promptTokens // .content.promptTokens // 0')
      CT=$(echo "$ev" | jq -r '.completionTokens // .content.completionTokens // 0')
      MN=$(echo "$ev" | jq -r '.modelName // .content.modelName // "?"') ;;
  esac
done < "$ASK_LOG"
echo
echo "       ----------------------------------------------------------------"
[[ -n "$SAW_ERR" ]] && die "agent returned error: $SAW_ERR"
[[ "$SAW_DONE" == 1 ]] || die "agent stream ended without 'done' event — see $ASK_LOG"
[[ -n "$ANSWER" ]] || die "agent produced no answer tokens — see $ASK_LOG"
[[ "$ANSWER" == *"$EXPECT_IN_ANSWER"* ]] \
  || die "answer missing expected token '$EXPECT_IN_ANSWER': $ANSWER"
ok "agent answered (model=${MN:-?}, ${PT:-?} in / ${CT:-?} out tokens) — answer contains '$EXPECT_IN_ANSWER' ✓"
rm -f "$ASK_LOG"

# ── Summary ──────────────────────────────────────────────────────────────────
say "RESULT — full upload → build → ask flow PASSED"
echo "   project:   $PNAME ($PROJECT_ID)"
echo "   schema:    $SCHEMA"
echo "   tables:    ${#TABLES[@]}   rows: $TOTAL"
echo "   ontology:  ${#OD_IDS[@]} ODs + ${#LINK_IDS[@]} Links built via Builder Agent (Chinese) + activated"
for i in "${!OD_NAMES[@]}"; do
  printf '              OD   %-13s %s\n' "${OD_NAMES[$i]}" "${OD_IDS[$i]}"
done
for i in "${!LINK_NAMES[@]}"; do
  printf '              LINK %-13s %s\n' "${LINK_NAMES[$i]}" "${LINK_IDS[$i]}"
done
echo "   ask:       English question answered (contains '$EXPECT_IN_ANSWER')"
