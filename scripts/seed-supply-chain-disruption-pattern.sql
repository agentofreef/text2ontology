-- ============================================================================
-- Seed: supply_chain_disruption analysis-pattern OK card (F07)
--
-- Target project : fdw-verify-045039
-- Spec           : .omc/specs/plan-from-ontology-knowledge.md §3.1 + §6
--
-- Inserts one ont_knowledge row with
--   entry_type  = 'analysis'
--   anchor_type = 'analysis_pattern'
--   skill_config = {trigger, features[], synthesis}
-- plus the trigger keywords into ont_knowledge_keyword.
--
-- Idempotent: re-running will not duplicate the card or its keywords. The
-- project_id is resolved by project name; the lead OD anchor (INGREDIENT) is
-- resolved by OD name in this project — left NULL if that OD does not exist.
-- ============================================================================

BEGIN;

DO $seed$
DECLARE
    v_project_id   UUID;
    v_anchor_id    UUID;
    v_knowledge_id UUID;
    v_inserted     BOOLEAN := FALSE;
BEGIN
    -- ── resolve project ────────────────────────────────────────────────
    SELECT id INTO v_project_id
    FROM project
    WHERE name = 'fdw-verify-045039';
    IF v_project_id IS NULL THEN
        RAISE EXCEPTION
          'seed-supply-chain-disruption-pattern: project "fdw-verify-045039" not found';
    END IF;

    -- ── resolve lead OD (INGREDIENT preferred; falls back to NULL) ────
    -- The anchor is for traceability — recall does not gate on it for
    -- analysis_pattern cards (trigger.keywords drives recall via
    -- ont_knowledge_keyword).
    SELECT id INTO v_anchor_id
    FROM ont_object_type
    WHERE project_id = v_project_id
      AND name = 'INGREDIENT'
    LIMIT 1;

    -- ── idempotency: skip the insert if the card already exists ────────
    SELECT id INTO v_knowledge_id
    FROM ont_knowledge
    WHERE project_id = v_project_id
      AND entry_type  = 'analysis'
      AND anchor_type = 'analysis_pattern'
      AND title       = '供应中断影响测算';

    IF v_knowledge_id IS NULL THEN
        INSERT INTO ont_knowledge (
            project_id, title, summary, content,
            entry_type, anchor_type, anchor_id, skill_config
        ) VALUES (
            v_project_id,
            '供应中断影响测算',
            '某原料 / SKU 断供 → 沿配方链路 → 营收 + 替代品 + 时间口径，综合估算净损失',
            E'**适用问题**\n- "燕麦奶断供，影响多少营收？"\n- "受 X 停产冲击的门店"\n- "如果 Y 缺货会怎样"\n\n**输出形态**\n- 受冲击毛营收（按城市分布）\n- 可替代 SKU 候选列表\n- 受影响订单时间窗\n- 净损失估算 + 注意事项',
            'analysis',
            'analysis_pattern',
            v_anchor_id,
            $skill$
            {
              "trigger": {
                "keywords": ["断供", "停供", "缺货", "下架", "停产", "供应中断", "断货", "受冲击"],
                "structural_hints": [
                  "如果 X 断供 / 停供 / 下架，影响多少",
                  "X 缺货会怎样",
                  "受 X 供应中断影响的"
                ]
              },
              "features": [
                {
                  "id": "gross_revenue_impact",
                  "behavior": "受冲击毛营收（按城市分布）— 沿配方链路找到所有用到该原料的 SKU、聚合订单营收",
                  "verification": "result.rows > 0 且总额数值合理（不是 0 也不是天文数字）",
                  "tool_hints": [
                    {"tool": "smartquery", "intent": "supply_chain_impact"},
                    {"tool": "compose_query"}
                  ]
                },
                {
                  "id": "substitution_candidates",
                  "behavior": "可替代 SKU 候选列表（同类产品 / 同 ingredient_class）",
                  "verification": "result 是一个 SKU 列表，rows >= 0（允许没有候选 → 替代率 0 → 净损失 ≈ 毛营收）",
                  "tool_hints": [
                    {"tool": "compose_query"}
                  ]
                },
                {
                  "id": "time_horizon",
                  "behavior": "受影响订单的时间窗（min/max placed_at）",
                  "verification": "result 含 min/max 日期字段",
                  "tool_hints": [
                    {"tool": "compose_query"}
                  ]
                }
              ],
              "synthesis": {
                "template": "受冲击毛营收 {{ .features.gross_revenue_impact.value }} 元（{{ .features.gross_revenue_impact.summary }}）。{{ if .features.time_horizon.summary }}\n时间口径：{{ .features.time_horizon.summary }}{{ end }}\n可替代 SKU 候选 {{ .features.substitution_candidates.rows }} 个 → 净损失估算 = 毛营收 × (1 − 替代率)。",
                "caveats": [
                  "毛营收不等于净损失：需扣除可替代部分",
                  "时间口径是全量历史，不直接换算成年度",
                  "替代率为定性估计，未做品类间需求弹性建模"
                ]
              }
            }
            $skill$::jsonb
        )
        RETURNING id INTO v_knowledge_id;
        v_inserted := TRUE;
    END IF;

    -- ── trigger keywords (idempotent insert) ───────────────────────────
    -- Inserts into ont_knowledge_keyword, which fallbackOkEntries() in
    -- recall-server already joins for OK recall. No new keyword binding
    -- table is needed (spec §9.1 revised decision).
    INSERT INTO ont_knowledge_keyword (knowledge_id, project_id, keyword)
    SELECT v_knowledge_id, v_project_id, kw
    FROM (VALUES
        ('断供'), ('停供'), ('缺货'), ('下架'),
        ('停产'), ('供应中断'), ('断货'), ('受冲击')
    ) AS k(kw)
    WHERE NOT EXISTS (
        SELECT 1 FROM ont_knowledge_keyword existing
        WHERE existing.knowledge_id = v_knowledge_id
          AND LOWER(existing.keyword) = LOWER(k.kw)
    );

    RAISE NOTICE
      'seed-supply-chain-disruption-pattern: project=%, knowledge_id=%, inserted=%, anchor_od=%',
      v_project_id, v_knowledge_id, v_inserted,
      COALESCE(v_anchor_id::text, '(NULL — INGREDIENT OD not found)');
END
$seed$;

COMMIT;

-- ── post-seed sanity check (read-only) ──────────────────────────────────
-- Run these to verify the seed landed:
--
--   SELECT id, title, entry_type, anchor_type,
--          jsonb_array_length(skill_config->'features') AS feature_count
--   FROM ont_knowledge
--   WHERE project_id = (SELECT id FROM project WHERE name = 'fdw-verify-045039')
--     AND anchor_type = 'analysis_pattern';
--
--   SELECT keyword
--   FROM ont_knowledge_keyword
--   WHERE project_id = (SELECT id FROM project WHERE name = 'fdw-verify-045039')
--     AND knowledge_id IN (
--       SELECT id FROM ont_knowledge
--       WHERE project_id = (SELECT id FROM project WHERE name = 'fdw-verify-045039')
--         AND anchor_type = 'analysis_pattern')
--   ORDER BY keyword;
