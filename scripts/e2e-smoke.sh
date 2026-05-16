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
QUESTION="${QUESTION:-一共有多少个产品？}"

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

# ── Step 7: build ontology via the Builder Agent ─────────────────────────────
# The Builder Agent runs an interview (≥3 rounds) before it will emit a
# propose_od tool call. We drive the conversation: send an explicit modelling
# request, then nudge each turn until a propose_od function_call appears.
say "STEP 7 — build ontology via Builder Agent"
MSGS='[]'
OBJECT_ID=""
NEXT_MSG="请基于湖仓里的 Products 表，创建一个名为 PRODUCT 的本体对象(OD)。属性至少包含 ProductName、UnitPrice、UnitsInStock。请尽快直接调用 propose_od 完成建模。"
for turn in 1 2 3 4 5 6; do
  MSGS=$(jq -cn --argjson m "$MSGS" --arg c "$NEXT_MSG" '$m + [{role:"user",content:$c}]')
  TURN_SSE=$(mktemp)
  agent_post_retry builder "$MSGS" "$TURN_SSE"
  ARR=$(sse_array "$TURN_SSE")
  [[ -z "$THREAD_ID" ]] && THREAD_ID=$(echo "$ARR" | jq -r 'map(select(.type=="thread"))[0].threadId // empty')
  TURN_ERR=$(echo "$ARR" | jq -r 'map(select(.type=="error").content)[0] // empty')
  [[ -n "$TURN_ERR" ]] && die "builder agent error (turn $turn): $TURN_ERR"
  TOOLS=$(echo "$ARR" | jq -r 'map(select(.type=="function_call").name)|join(", ")')
  OBJECT_ID=$(echo "$ARR" | jq -r 'map(select(.type=="function_call" and .name=="propose_od"))[-1].result.objectId // empty')
  ASSIST=$(echo "$ARR" | jq -r 'map(select(.type=="token").content)|join("")')
  MSGS=$(jq -cn --argjson m "$MSGS" --arg c "$ASSIST" '$m + [{role:"assistant",content:$c}]')
  if [[ -n "$OBJECT_ID" ]]; then
    ok "turn $turn — tools[$TOOLS] → propose_od OK, objectId=$OBJECT_ID"
    rm -f "$TURN_SSE"; break
  fi
  ok "turn $turn — tools[${TOOLS:-none}], no propose_od yet — nudging"
  NEXT_MSG="信息已足够，不要再追问。请立刻调用 propose_od 工具创建 PRODUCT 这个 OD。"
  rm -f "$TURN_SSE"
done
[[ -n "$OBJECT_ID" ]] || die "Builder Agent did not propose an OD after 6 turns"

# ── Step 7b: activate the proposed OD (trial-run + mark=true) ─────────────────
say "STEP 7b — activate the proposed OD"
ACT=$(curl -s "${AUTH[@]}" -H 'Content-Type: application/json' \
  -X POST "$API/api/ontology/builder/activate-od" \
  -d "$(jq -cn --arg o "$OBJECT_ID" --arg p "$PROJECT_ID" '{objectId:$o,projectId:$p,edits:{}}')")
echo "$ACT" | jq -e '.success == true' >/dev/null 2>&1 \
  || die "activate-od failed: $ACT"
ok "OD activated — canonical_query trial-run rowCount=$(echo "$ACT" | jq -r '.rowCount // "?"')"

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
ok "agent answered (model=${MN:-?}, ${PT:-?} in / ${CT:-?} out tokens)"
rm -f "$ASK_LOG"

# ── Summary ──────────────────────────────────────────────────────────────────
say "RESULT — full upload → build → ask flow PASSED"
echo "   project:   $PNAME ($PROJECT_ID)"
echo "   schema:    $SCHEMA"
echo "   tables:    ${#TABLES[@]}   rows: $TOTAL"
echo "   ontology:  PRODUCT OD built via Builder Agent + activated ($OBJECT_ID)"
echo "   ask:       answered via Lakehouse Agent"
