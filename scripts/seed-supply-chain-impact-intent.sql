-- Authorization data for the spec's "hero question" composite Intent.
-- Spec: .omc/specs/plan-mode-composite-intent.md §3.6.
--
-- Inserts one lakehouse_metric_intent row for project fdw-verify-045039
-- whose `plan` JSONB encodes the 4-step ingredient → SKU → RecipeLine →
-- OrderLine fan-out. Idempotent on (project_id, name).
--
-- The Intent is anchored on ORDERLINE because that's the OD of the
-- terminal `impact` step (where the aggregation lives). canonical_metric
-- is a placeholder — composite Intents ignore canonical_metric /
-- auto_group_by per spec §3.1; the plan column is the source of truth.

\set ON_ERROR_STOP on

INSERT INTO lakehouse_metric_intent
    (project_id, object_id, name, display_name,
     canonical_metric, description, priority, mark, plan)
SELECT
    '57832811-fed2-482b-be41-9bf27e49ccf6'::uuid,
    o.id,
    'supply_chain_impact',
    '供应链断供影响',
    '(plan)',  -- placeholder; plan-mode ignores canonical_metric
    '原料断供影响测算：给定一种原料，沿 Ingredient → SKU → RecipeLine → OrderLine 链路展开，按城市汇总受冲击营收与门店数。',
    100,
    true,
    $plan${
      "params": [
        {"name": "ingredient", "type": "string", "description": "原料名，如 燕麦奶"}
      ],
      "steps": [
        {
          "id": "ingredient",
          "od": "INGREDIENT",
          "select": ["id"],
          "filters": [{"prop": "name", "op": "like", "value": "%$param.ingredient%"}]
        },
        {
          "id": "skus",
          "od": "SKU",
          "select": ["id"],
          "filters": [{"prop": "ingredient_id", "op": "in", "value": "$ingredient.id"}]
        },
        {
          "id": "recipes",
          "od": "RECIPELINE",
          "select": ["spec_id"],
          "filters": [{"prop": "sku_code", "op": "in", "value": "$skus.id"}]
        },
        {
          "id": "impact",
          "od": "ORDERLINE",
          "metric": "SUM(ORDERLINE.line_total)",
          "groupBy": ["STORE.city"],
          "filters": [{"prop": "spec_id", "op": "in", "value": "$recipes.spec_id"}]
        }
      ],
      "output": "impact"
    }$plan$::jsonb
FROM ont_object_type o
WHERE o.project_id = '57832811-fed2-482b-be41-9bf27e49ccf6'
  AND o.name = 'ORDERLINE'
ON CONFLICT (project_id, name) DO UPDATE
    SET plan         = EXCLUDED.plan,
        display_name = EXCLUDED.display_name,
        description  = EXCLUDED.description,
        priority     = EXCLUDED.priority,
        updated_at   = now();

-- Mirror plan.params into the `parameters` column. The agent surfaces a
-- Metric Intent's user-level parameter schema (the "🎯 查询意图" context
-- section + the per-Intent smartquery tool def) from `parameters`, NOT from
-- the plan JSON. If `parameters` is empty the LLM has no schema and guesses
-- the param name (observed: it sent `ingredient_name` instead of `ingredient`,
-- which the plan executor rejects as "unbound param"). For a composite Intent
-- the user-facing schema IS plan.params, so the two must stay in sync.
UPDATE lakehouse_metric_intent
SET parameters = plan->'params'
WHERE project_id = '57832811-fed2-482b-be41-9bf27e49ccf6'
  AND name = 'supply_chain_impact'
  AND plan ? 'params';

-- Recall keywords so the Builder/Query agent surfaces this composite Intent
-- in its "🎯 查询意图" context section. Per spec §2 a composite Intent is
-- keyword-recalled exactly like an ordinary Intent — without these rows the
-- LLM never sees the Intent name and can't dispatch to it.
INSERT INTO lakehouse_keyword (project_id, object_type_id, metric_intent_id, keyword)
SELECT mi.project_id, mi.object_id, mi.id, kw
FROM lakehouse_metric_intent mi
CROSS JOIN unnest(ARRAY['断供','停供','缺货','供应中断','受冲击','受影响','原料断供','断供影响']) AS kw
WHERE mi.project_id = '57832811-fed2-482b-be41-9bf27e49ccf6'
  AND mi.name = 'supply_chain_impact'
ON CONFLICT DO NOTHING;

SELECT mi.id, mi.name, jsonb_array_length(mi.plan->'steps') AS steps,
       count(k.id) AS keywords
FROM lakehouse_metric_intent mi
LEFT JOIN lakehouse_keyword k ON k.metric_intent_id = mi.id
WHERE mi.project_id = '57832811-fed2-482b-be41-9bf27e49ccf6'
  AND mi.name = 'supply_chain_impact'
GROUP BY mi.id, mi.name, mi.plan;
