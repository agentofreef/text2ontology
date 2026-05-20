-- widen-period-label-shape.sql
--
-- The 6 boba-demo Intents (store_profit, store_revenue, store_cost_breakdown,
-- channel_revenue_share, member_order_share, store_top_revenue) all expose a
-- parameter `period_label` whose op="starts with" + type="string" already
-- naturally supports both 「YYYY」 and 「YYYY-MM」 prefixes — the smartquery
-- executor compiles either form into `placed_at LIKE '<prefix>%'`.
--
-- However the original description only mentioned YYYY-MM, so the
-- MissionAct 任务可达器 (reachability judge) — which reads parameter
-- description text to decide shape coverage — would mark whole-year
-- questions like "2025 年总营收" as infeasible.
--
-- This script widens that description so the judge sees both shapes.
-- Code paths and SQL templates are unchanged — this is description-only.
--
-- Idempotent: re-running just overwrites with the same text.
UPDATE lakehouse_metric_intent
SET parameters = (
  SELECT jsonb_agg(
    CASE WHEN p->>'name' = 'period_label'
         THEN jsonb_set(p, '{description}',
              to_jsonb('期间标签 — 支持 ISO 短格式: 「YYYY」(按年, 例 "2025", 前缀匹配该年所有月份) 或 「YYYY-MM」(按单月, 例 "2025-12")。op=starts with, 因此 YYYY 形态可直接覆盖年范围；跨年范围(如 2024-2025)需要分多次调用。'::text))
         ELSE p
    END
  )
  FROM jsonb_array_elements(parameters) p
)
WHERE project_id = '57832811-fed2-482b-be41-9bf27e49ccf6'
  AND parameters::text LIKE '%period_label%'
RETURNING name;
