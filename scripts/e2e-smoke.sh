#!/usr/bin/env bash
# scripts/e2e-smoke.sh — End-to-end smoke test of the import → build → ask flow.
#
# Simulates a human operator driving the stack purely through public HTTP APIs:
#   1. mint an auth token  (HMAC, no password needed — reads AUTH_TOKEN_SECRET)
#   2. create a fresh project
#   3. upload a SQLite file        POST /api/connector/sqlite/sources
#   4. sync all tables to staging  POST /api/connector/sqlite/sources/{id}/sync   (SSE)
#   5. confirm the wizard          POST /api/connector/wizard/{id}/confirm
#   6. verify rows + numeric-column promotion  (direct psql)
#   7. build ontology via the Builder Agent — 6 ODs + 5 Links, Chinese prompts,
#      each its own conversation: propose_od/propose_link → activate-od/-link;
#      then create 1 Metric Intent (deterministic multi-hop query template)
#      via the ontology REST API
#   8. ask the Lakehouse Agent 4 English demo questions (warmup → multi-OD
#      centerpiece, routed through the Intent → rephrase twin → deliberate
#      unregistered-term miss); raw SSE saved under regression-fixtures/
#      demo-shots/ for review.
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
FIXTURE_DIR="${FIXTURE_DIR:-regression-fixtures/demo-shots}"  # raw SSE per demo question

PSQL=(docker compose --env-file "$ENV_FILE" exec -T postgres
      psql -U text2ontology_community -d text2ontology_community -tA)

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
  "请基于湖仓里的 Products 表，创建一个名为 PRODUCT 的本体对象(OD)。属性至少包含 ProductID(产品ID, 主键)、ProductName(产品名)、CategoryID(类别ID, 外键)、UnitPrice(单价)、UnitsInStock(库存)。请尽快直接调用 propose_od 完成建模，不要反复追问。"
OD_NAMES+=("PRODUCT"); OD_IDS+=("$BUILT_OD_ID")

build_and_activate_od "CATEGORY" \
  "请基于湖仓里的 Categories 表，创建一个名为 CATEGORY 的本体对象(OD)。属性至少包含 CategoryID(类别ID, 主键)、CategoryName(类别名)、Description(描述)。请尽快直接调用 propose_od 完成建模，不要反复追问。"
OD_NAMES+=("CATEGORY"); OD_IDS+=("$BUILT_OD_ID")

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

build_and_activate_link "PRODUCT_CATEGORY" \
  "在已激活的 PRODUCT 和 CATEGORY 这两个 OD 之间建立一条 many_to_one Link，名为 PRODUCT_CATEGORY，外键列是 PRODUCT.CategoryID 指向 CATEGORY.CategoryID。请：1) 先调用 list type=ods 拿到两个 OD 的 property.id；2) 立即调用 propose_link，fromObjectId=PRODUCT 的 id、toObjectId=CATEGORY 的 id、fromPropertyId=PRODUCT.CategoryID 的 property.id、toPropertyId=CATEGORY.CategoryID 的 property.id、fkColumn=\"CategoryID\"、linkName=\"PRODUCT_CATEGORY\"、cardinality=\"many_to_one\"。不要反复追问。"
LINK_NAMES+=("PRODUCT_CATEGORY"); LINK_IDS+=("$BUILT_LINK_ID")

ok "built & activated ${#LINK_IDS[@]} Links: ${LINK_NAMES[*]}"

# ── Step 7d: build a Metric Intent (deterministic multi-hop query template) ──
# A Metric Intent bakes a multi-OD join + filter set into a named template.
# The Lakehouse Agent then only emits {intent, params} — the canonical SQL
# (4-table join across ORDER_DETAIL→PRODUCT, →ORDER_ENT→CUSTOMER) is fixed,
# so the same question always yields byte-identical SQL. Created via the
# ontology REST API (the same endpoint the modelling UI uses).
say "STEP 7d — build a Metric Intent"
OD_DETAIL_ID=""
for i in "${!OD_NAMES[@]}"; do
  [[ "${OD_NAMES[$i]}" == "ORDER_DETAIL" ]] && OD_DETAIL_ID="${OD_IDS[$i]}"
done
[[ -n "$OD_DETAIL_ID" ]] || die "ORDER_DETAIL OD id not found among built ODs"
INTENT_BODY=$(jq -cn --arg od "$OD_DETAIL_ID" '{
  objectId: $od,
  name: "EU.Beverage.Quantity.2017",
  displayName: "2017 EU Beverage Quantity",
  canonicalMetric: "sum(Quantity)",
  canonicalFilters: [
    {prop:"PRODUCT.CategoryID",  op:"=",       value:"1"},
    {prop:"CUSTOMER.Country",    op:"in",      value:"Germany,France,UK,Italy,Spain"},
    {prop:"ORDER_ENT.OrderDate", op:"between", value:"2017-01-01,2017-12-31"},
    {prop:"Discount",            op:"<=",      value:"0.1"}
  ],
  autoGroupBy: [],
  triggerKeywords: ["beverage sales","drinks sales","EU beverages","European beverage"],
  mark: true
}')
INTENT_RESP=$(curl -s "${AUTH[@]}" -H 'Content-Type: application/json' \
  -X POST "$API/api/ontology/metric-intents?projectId=$PROJECT_ID" -d "$INTENT_BODY")
INTENT_ID=$(echo "$INTENT_RESP" | jq -r '.id // empty')
[[ -n "$INTENT_ID" ]] || die "metric-intent create failed: $INTENT_RESP"
ok "Metric Intent created — EU.Beverage.Quantity.2017 ($INTENT_ID)"

# ask_demo <tag> <question> <expect|->
# Asks the Lakehouse Agent one English question on a fresh thread, streams the
# answer, saves raw SSE to $FIXTURE_DIR/<tag>.sse (screenshot/regression source),
# and validates that the answer contains <expect> (commas ignored so "88,588"
# matches "88588"). Pass "-" as <expect> for a deliberate-miss question that is
# not asserted on (Q3 — the unregistered-term scenario).
DEMO_PASS=0
DEMO_TOTAL=0
ask_demo() {
  local tag="$1" question="$2" expect="$3"
  local THREAD_ID="" out="$FIXTURE_DIR/${tag}.sse"
  DEMO_TOTAL=$((DEMO_TOTAL + 1))
  echo
  printf '   \033[1m[%s]\033[0m %s\n' "$tag" "$question"
  agent_post_retry lakehouse "$(jq -cn --arg q "$question" '[{role:"user",content:$q}]')" "$out"
  local arr err done_ tools ans mn
  arr=$(sse_array "$out")
  err=$(echo "$arr" | jq -r 'map(select(.type=="error").content)[0] // empty')
  done_=$(echo "$arr" | jq -r 'any(.[]; .type=="done")')
  tools=$(echo "$arr" | jq -r '[.[]|select(.type=="function_call").name]|join(" → ")')
  ans=$(echo "$arr" | jq -r 'map(select(.type=="token").content)|join("")')
  mn=$(echo "$arr" | jq -r 'map(select(.type=="done"))[0].modelName // "?"')
  [[ -n "$err" ]] && die "$tag — agent error: $err"
  [[ "$done_" == "true" ]] || die "$tag — stream ended without 'done' — see $out"
  [[ -n "$ans" ]] || die "$tag — no answer tokens — see $out"
  echo "       tools:  ${tools:-none}"
  echo "       answer: $(echo "$ans" | tr '\n' ' ' | sed 's/  */ /g' | cut -c1-280)"
  if [[ "$expect" == "-" ]]; then
    ok "$tag — answered (model=$mn) [soft: deliberate-miss, not asserted]  → $out"
    DEMO_PASS=$((DEMO_PASS + 1))
  elif [[ "$(echo "$ans" | tr -d ,)" == *"$(echo "$expect" | tr -d ,)"* ]]; then
    ok "$tag — answered (model=$mn), contains '$expect' ✓  → $out"
    DEMO_PASS=$((DEMO_PASS + 1))
  else
    die "$tag — answer missing expected '$expect' — see $out"
  fi
}

# ── Step 8: ask the Lakehouse Agent (demo scenarios, English) ────────────────
# Four questions, escalating: a warmup, the multi-OD centerpiece, its rephrase
# twin (same intent, different words), and a deliberate unregistered-term miss.
say "STEP 8 — ask the Lakehouse Agent (demo questions)"
mkdir -p "$FIXTURE_DIR"

ask_demo "Q1-warmup" \
  "Which 3 employees handled the most orders? Please list their full names and order counts. Respond strictly in English." \
  "Peacock"

ask_demo "Q2-centerpiece" \
  "What was the total quantity of beverages ordered by our key European customers in Germany, France, the UK, Italy and Spain during 2017, excluding heavily-discounted orders with a discount above 10 percent? Respond strictly in English." \
  "88,588"

ask_demo "Q2prime-rephrase" \
  "For the year 2017, how many units of drinks did our major EU clients based in Germany, France, the UK, Italy and Spain purchase, ignoring any order discounted by more than 10 percent? Respond strictly in English." \
  "88,588"

ask_demo "Q3-deliberate-miss" \
  "Which of our VIP customers ordered the most beverages in 2017? Respond strictly in English." \
  "-"

ok "demo questions: $DEMO_PASS/$DEMO_TOTAL ran clean (Q3 is a soft deliberate-miss)"

# ── Summary ──────────────────────────────────────────────────────────────────
say "RESULT — full upload → build → ask flow PASSED"
echo "   project:   $PNAME ($PROJECT_ID)"
echo "   schema:    $SCHEMA"
echo "   tables:    ${#TABLES[@]}   rows: $TOTAL"
echo "   ontology:  ${#OD_IDS[@]} ODs + ${#LINK_IDS[@]} Links (Builder Agent, Chinese) + 1 Metric Intent"
for i in "${!OD_NAMES[@]}"; do
  printf '              OD     %-15s %s\n' "${OD_NAMES[$i]}" "${OD_IDS[$i]}"
done
for i in "${!LINK_NAMES[@]}"; do
  printf '              LINK   %-15s %s\n' "${LINK_NAMES[$i]}" "${LINK_IDS[$i]}"
done
printf '              INTENT %-15s %s\n' "EU.Beverage..." "$INTENT_ID"
echo "   demo:      $DEMO_PASS/$DEMO_TOTAL questions clean — raw SSE in $FIXTURE_DIR/"
