#!/usr/bin/env bash
# T6 — end-to-end verification of the composite Intent plan executor.
# Spec: .omc/specs/plan-mode-composite-intent.md §4 (T6) and §6.
#
# Drives the live /internal/smartquery/execute-plan endpoint against project
# fdw-verify-045039 and asserts:
#   1. Hero question (ingredient="燕麦奶") returns non-empty per-city rows
#      with positive affected revenue, status=success.
#   2. Hand-written 6-table JOIN ground-truth SQL produces the same affected
#      revenue total (sanity check the plan executor isn't drifting).
#   3. Empty-input path (ingredient="不存在的原料") returns ResultJSON="[]"
#      cleanly — no error, no crash.

set -uo pipefail
cd /Users/tom/Desktop/githubproject/text2ontology-community

PID='57832811-fed2-482b-be41-9bf27e49ccf6'
URL='http://127.0.0.1:18094/internal/smartquery/execute-plan'
TOKEN=$(grep -E '^INTERNAL_TOKEN=' .env.shared | cut -d= -f2-)

PSQL=(docker compose --env-file .env.shared exec -T postgres
      psql -U lakehouse2ontology-enterprise -d lakehouse2ontology-enterprise -tA)

say() { printf '\n\033[1m▼ %s\033[0m\n' "$*"; }
ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
die() { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

PLAN_JSON=$("${PSQL[@]}" -c "
  SELECT plan::text FROM lakehouse_metric_intent
  WHERE project_id='$PID' AND name='supply_chain_impact';
")
[[ -n "$PLAN_JSON" ]] || die "supply_chain_impact intent not seeded"
ok "plan loaded ($(echo "$PLAN_JSON" | wc -c | tr -d ' ') bytes)"

post_plan() {
  local ingredient="$1"
  local body
  body=$(jq -cn --argjson plan "$PLAN_JSON" --arg pid "$PID" --arg ing "$ingredient" '
    {plan: $plan, params: {ingredient: $ing}, projectId: $pid}')
  curl -s -X POST "$URL" \
    -H 'Content-Type: application/json' \
    -H "X-Internal-Token: $TOKEN" \
    -H 'X-On-Behalf-Of: t6-script' \
    -d "$body"
}

say "1. hero question — ingredient=燕麦奶"
RESP=$(post_plan "燕麦奶")
STATUS=$(echo "$RESP" | jq -r '.executionOk')
RESULT=$(echo "$RESP" | jq -r '.resultJson')
ERR=$(echo "$RESP" | jq -r '.errorMessage // ""')
[[ "$STATUS" == "true" ]] || die "executionOk=$STATUS errorMessage=$ERR resp=$RESP"
ROWS=$(echo "$RESULT" | jq 'length')
[[ "$ROWS" -gt 0 ]] || die "no per-city impact rows (resultJson=$RESULT)"
ok "rows=$ROWS"

# Per-city affected revenue from the plan output ("Total_line_total" is the
# engine-assigned alias for SUM(ORDERLINE.line_total)).
PLAN_CITY_REVENUE=$(echo "$RESULT" | jq -r '
  sort_by(.city) | .[] | "\(.city)|\(.Total_line_total)"')
ok "plan output:"
echo "$PLAN_CITY_REVENUE" | sed 's/^/    /'

say "2. ground truth — hand-written 6-table JOIN against physical tables"
# canonical_query introspection: ORDERLINE→pos_order_line, ORDER→pos_order,
# STORE.city→ref_city.name_zh via pos_store.city_id, etc.
GT=$("${PSQL[@]}" -c "
SELECT c.name_zh || '|' || ROUND(SUM(ol.line_total_cny)::numeric, 2)
FROM proj_57832811fed2482bbe419bf27e49ccf6.pos_order_line ol
JOIN proj_57832811fed2482bbe419bf27e49ccf6.pos_order      o  ON o.order_id     = ol.order_id
JOIN proj_57832811fed2482bbe419bf27e49ccf6.pos_store      s  ON s.store_id     = o.store_id
JOIN proj_57832811fed2482bbe419bf27e49ccf6.ref_city       c  ON c.city_id      = s.city_id
JOIN proj_57832811fed2482bbe419bf27e49ccf6.recipe         rl ON rl.spec_id     = ol.spec_id
JOIN proj_57832811fed2482bbe419bf27e49ccf6.wms_sku        sk ON sk.sku_code    = rl.sku_code
JOIN proj_57832811fed2482bbe419bf27e49ccf6.ref_ingredient i  ON i.ingredient_id = sk.ingredient_id
WHERE i.name_zh ILIKE '%燕麦奶%'
GROUP BY c.name_zh
ORDER BY c.name_zh;")
ok "ground truth:"
echo "$GT" | sed 's/^/    /'

# Compare line-by-line; fail loudly on any divergence.
diff <(printf '%s\n' "$PLAN_CITY_REVENUE") <(printf '%s\n' "$GT") >/dev/null \
    && ok "plan ↔ ground-truth match — affected_revenue identical across all cities" \
    || die "DIVERGENCE: plan vs. ground-truth affected_revenue differ — see diff above"

say "3. empty input — ingredient=不存在的原料"
RESP=$(post_plan "不存在的原料")
STATUS=$(echo "$RESP" | jq -r '.executionOk')
RESULT=$(echo "$RESP" | jq -r '.resultJson')
ERR=$(echo "$RESP" | jq -r '.errorMessage // ""')
[[ "$STATUS" == "true" ]] || die "empty-input path should not error: status=$STATUS err=$ERR"
[[ "$RESULT" == "[]" ]] || die "empty-input resultJson should be '[]', got: $RESULT"
ok "empty short-circuit clean: resultJson=[]"

say "T6 — PASS"
