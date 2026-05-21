-- Seed rows for lakehouse_shape_capability — the data-driven vocabulary
-- consumed by the mission reachability gate
-- (services/agent-server/handler/reachability_judge.go).
--
-- This file is *data*, not code. The Go and TS sources do not name any
-- shape; they only do string-equality on whatever `name` rows live in
-- this table at request time. Add, edit, rename, or delete rows freely
-- to fit your project — the gate adapts automatically on the next turn.
--
-- ── Granularity principle (READ THIS BEFORE ADDING A SHAPE) ─────────
-- 1. Cut shapes by REACHABILITY ("how many tool calls does one element
--    need"), NOT by data type. A single string match (op="=") and a
--    single enum match (enum_ref, op="=") are the SAME reachability
--    shape — both are "one value, one call" — so they share ONE shape
--    name (`single_value`). Do NOT split them into `exact_match` vs
--    `enum_single`: the gate matches the LLM's classification against
--    the parameter's declared shape by string equality, and two
--    synonym shapes make the LLM pick one label while the parameter
--    declares the other → a FEASIBLE question gets falsely refused.
--    (This exact false-positive was observed and is why the value
--    shape is unified here.)
-- 2. A shape is worth adding ONLY when deterministic checking beats the
--    LLM's own judgement. The period case earns its keep: the LLM used
--    to over-claim that a "starts with YYYY-MM" parameter covers a
--    multi-year range. Value equality does not need policing as hard —
--    keep its vocabulary coarse (single_value) to avoid synonym traps.
--
-- Example from this project's `period_label` parameter
-- (op="starts with", supports YYYY or YYYY-MM prefix):
--   - "2025 年营收" / "2025-03 营收"  → single_period_prefix  (one call) → coverable
--   - "2024 到 2025 营收"             → multi_period_range    (N calls)  → NOT coverable
-- The parameter declares shapeCapability="single_period_prefix" and the
-- cross-year question is correctly refused, while the single-year and
-- single-value (single city) questions pass.
--
-- Re-running this file is safe: ON CONFLICT (name) DO NOTHING leaves any
-- operator edits to existing rows intact.

-- ── `satisfies` column (subsumption) ───────────────────────────────
-- A parameter declaring shape X can ALSO serve a requirement the LLM
-- classified as any name in X.satisfies (plus X itself). Direction is
-- strictly broader→narrower. multi_period_range satisfies
-- single_period_prefix because a true start/end range parameter can also
-- answer a single-period question — so if the LLM labels a requirement
-- single_period_prefix and the cited Intent only has a range param, the
-- gate still (correctly) treats it as covered. NEVER list a broader shape
-- under a narrower one (e.g. single_period_prefix must NOT satisfy
-- multi_period_range) — that re-opens the false-acceptance the gate
-- exists to prevent. Empty satisfies = the shape covers only itself.

INSERT INTO lakehouse_shape_capability (name, description, examples, satisfies) VALUES
  (
    'single_value',
    '匹配单个值的等值筛选（op="=" 一次调用即可命中），无论该值是普通字符串还是来自枚举集合。问单个城市/单个渠道/单个状态都属于此形态。若问题要求匹配一组值（多个值的并集），那是另一种形态，不属于 single_value。',
    ARRAY['city = 上海', '渠道 = delivery', '状态 = 已完成'],
    '{}'
  ),
  (
    'single_period_prefix',
    '单个周期前缀 token（YYYY 或 YYYY-MM），一次 starts-with 调用即可命中。覆盖"某一整年"或"某一个月"。注意：这是"一次调用一个周期"的形态，跨多个周期的区间不属于此形态。',
    ARRAY['2025 年的营收', '2025-03 这个月的订单', 'placed_at starts with 2025'],
    '{}'
  ),
  (
    'multi_period_range',
    '跨多个周期的连续区间（例如 2024-2025 跨年，或跨年边界的月区间）。前缀匹配参数一次只能命中一个周期，覆盖这种区间需要分多次调用，按可达性视为不可达——除非存在显式的区间参数（start/end 成对）声明此形态。',
    ARRAY['2024 到 2025 的营收对比', '过去三年的趋势', '2024-11 到 2025-02 的跨年区间'],
    '{single_period_prefix}'
  )
ON CONFLICT (name) DO NOTHING;
