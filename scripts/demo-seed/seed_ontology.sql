-- demo-seed ontology · project + 6 OD + Properties + Links + Intent + Keywords
-- Mounted at /docker-entrypoint-initdb.d/05-demo-ontology.sql in demo profile.
-- Idempotent: ON CONFLICT DO NOTHING throughout.
-- Depends on: 01-schema.sql, 02-demo-ddl.sql, 03-demo-static.sql, 04-demo-generated.sql

-- ============================================================================
-- 1. Demo project + admin membership
-- ============================================================================

-- Fixed UUIDs for stable cross-restart references.
-- Admin user UUID = a0000000-0000-0000-0000-000000000001 (defined in 01-schema.sql).
-- Demo project UUID = d0000000-0000-0000-0000-000000000001.

INSERT INTO project (id, name, description, owner_id, source_type, status)
VALUES (
    'd0000000-0000-0000-0000-000000000001',
    'Demo · 跨部门新品上市',
    '虚构多品类消费电子公司 · 25 个 Launch / 12 个团队 / ~30 类共享资源 · text2ontology 公开 demo 场景',
    'a0000000-0000-0000-0000-000000000001',
    'demo-seed',
    'active'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO project_member (project_id, user_id, role)
VALUES ('d0000000-0000-0000-0000-000000000001', 'a0000000-0000-0000-0000-000000000001', 'owner')
ON CONFLICT (project_id, user_id) DO NOTHING;


-- ============================================================================
-- 2. Object Types (6 ODs) — each anchored on its staging table via semantic_sql
-- ============================================================================
-- Convention: id_property is named "id"; downstream Links reference by id field
-- regardless of underlying source column.

INSERT INTO ont_object_type (id, project_id, name, display_name, kind, description, source_table, semantic_sql, mark)
VALUES
-- ── Launch ─────────────────────────────────────────────────────────
('d1000000-0000-0000-0000-000000000001',
 'd0000000-0000-0000-0000-000000000001',
 'Launch', '上市项目', 'entity',
 '一个产品的"上市项目"——从立项到上市的整个生命周期。每个 Launch 关联多个 Workstream。',
 'demo_launch',
 'SELECT launch_id AS id, name, product_line, internal_codename, target_market, target_launch_date, status, project_lead, description FROM demo_launch',
 TRUE),

-- ── Workstream ─────────────────────────────────────────────────────
('d1000000-0000-0000-0000-000000000002',
 'd0000000-0000-0000-0000-000000000001',
 'Workstream', '工作流', 'entity',
 '一个 Launch 下由某个团队负责的一条工作流（如硬件设计、CCC 认证、营销发布）。',
 'demo_workstream',
 'SELECT workstream_id AS id, launch_id, name, workstream_type, owner_team_id, start_date, end_date, status, weekly_hours_required FROM demo_workstream',
 TRUE),

-- ── Milestone ──────────────────────────────────────────────────────
('d1000000-0000-0000-0000-000000000003',
 'd0000000-0000-0000-0000-000000000001',
 'Milestone', '里程碑', 'event',
 '一个 Workstream 下的可观察事件——EVT / DVT / PVT / 量产 / 认证完成 / 首发上架等。',
 'demo_milestone',
 'SELECT milestone_id AS id, workstream_id, name, milestone_type, planned_date, actual_date, status, is_critical, required_resource_id, required_capacity FROM demo_milestone',
 TRUE),

-- ── Team ───────────────────────────────────────────────────────────
('d1000000-0000-0000-0000-000000000004',
 'd0000000-0000-0000-0000-000000000001',
 'Team', '团队', 'entity',
 '12 个跨职能团队——手机 HW/SW、PC HW/SW、IoT、外设、供应链、SDM、营销、法务、渠道、QA。',
 'demo_team',
 'SELECT team_id AS id, name, english_name, function_category, weekly_capacity_hours, headcount FROM demo_team',
 TRUE),

-- ── Dependency ─────────────────────────────────────────────────────
('d1000000-0000-0000-0000-000000000005',
 'd0000000-0000-0000-0000-000000000001',
 'Dependency', '依赖', 'entity',
 'Milestone 之间的有向依赖关系——blocks / requires / shares_resource / informs。跨 Launch 的 shares_resource 是"谁动谁就阻塞谁"的核心数据。',
 'demo_dependency',
 'SELECT dependency_id AS id, from_milestone_id, to_milestone_id, dependency_type, lead_time_days, cross_launch, notes FROM demo_dependency',
 TRUE),

-- ── Resource ───────────────────────────────────────────────────────
('d1000000-0000-0000-0000-000000000006',
 'd0000000-0000-0000-0000-000000000001',
 'Resource', '共享资源', 'entity',
 'PCB 主板厂 / 组装线 / 模具厂 / 认证实验室 / 测试治具池等——多个 Workstream 共用，是跨 Launch 阻塞的物理根源。',
 'demo_resource',
 'SELECT resource_id AS id, name, category, total_weekly_capacity, capacity_unit, location FROM demo_resource',
 TRUE)
ON CONFLICT (project_id, name) DO NOTHING;


-- ============================================================================
-- 3. Properties (one row per logical column on each OD)
-- ============================================================================
-- short_description is human-readable (also drives the explanation-layer vector).
-- is_filterable / is_groupable defaults to TRUE; override per column as needed.

-- ── Launch properties ─────────────────────────────────────────────
INSERT INTO ont_property (project_id, object_type_id, name, display_name, data_type, source_column, short_description) VALUES
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000001', 'name',               '名称',     'string', 'name',               '产品的对外正式名称，如 "S25 Pro"。Launch 的主键名。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000001', 'product_line',       '产品线',   'string', 'product_line',       '产品所属品类——Phone / Laptop / Tablet / Wearable / Audio / Display / Other。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000001', 'internal_codename',  '内部代号', 'string', 'internal_codename',  '研发期间的内部代号——如 "Slim25" 是 S25 Pro 的内部叫法。供别名召回使用。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000001', 'target_market',      '目标市场', 'string', 'target_market',      '面向哪些市场首发——Global / CN-only / Asia-Pacific。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000001', 'target_launch_date', '计划上市日','date',  'target_launch_date', '当前计划的首发上市日期。问"能否提前"时，参考点就是这一列。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000001', 'status',             '状态',     'string', 'status',             'Launch 当前状态——planned / on_track / at_risk / delayed / launched。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000001', 'project_lead',       '项目负责人','string','project_lead',       '该 Launch 的负责人姓名。')
ON CONFLICT (object_type_id, name) DO NOTHING;

-- ── Workstream properties ─────────────────────────────────────────
INSERT INTO ont_property (project_id, object_type_id, name, display_name, data_type, source_column, short_description) VALUES
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000002', 'name',                  '名称',     'string', 'name',                  '工作流的全名——"S25 Pro · 硬件设计"格式。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000002', 'workstream_type',       '类型',     'string', 'workstream_type',       '工作流类型——HW Design / SW Dev / Supply Chain / Certification / Marketing / Channel / QA / Legal。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000002', 'start_date',            '开始日期', 'date',   'start_date',            '工作流的实际起始日。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000002', 'end_date',              '结束日期', 'date',   'end_date',              '工作流的计划终止日。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000002', 'status',                '状态',     'string', 'status',                '当前状态——planned / in_progress / at_risk / done / blocked。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000002', 'weekly_hours_required', '每周工时', 'int',    'weekly_hours_required', '工作流占用归属团队的每周人时数。')
ON CONFLICT (object_type_id, name) DO NOTHING;

-- ── Milestone properties ──────────────────────────────────────────
INSERT INTO ont_property (project_id, object_type_id, name, display_name, data_type, source_column, short_description) VALUES
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000003', 'name',               '名称',       'string', 'name',               '里程碑全名，含 Launch 前缀。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000003', 'milestone_type',     '类型',       'string', 'milestone_type',     '里程碑类型——EVT / DVT / PVT / pp_build / mass_production / certification / first_release / kickoff / design_review / gtm_ready。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000003', 'planned_date',       '计划日期',   'date',   'planned_date',       '当前计划达成日。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000003', 'actual_date',        '实际日期',   'date',   'actual_date',        '实际达成日。NULL 表示尚未完成。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000003', 'status',             '状态',       'string', 'status',             'planned / in_progress / done / at_risk / blocked。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000003', 'is_critical',        '关键路径',   'bool',   'is_critical',        'TRUE 表示该里程碑在关键路径上，移动它会影响 Launch 整体日期。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000003', 'required_capacity',  '占用容量',   'int',    'required_capacity',  '该里程碑占用对应资源的容量单位数。')
ON CONFLICT (object_type_id, name) DO NOTHING;

-- ── Team properties ───────────────────────────────────────────────
INSERT INTO ont_property (project_id, object_type_id, name, display_name, data_type, source_column, short_description) VALUES
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000004', 'name',                   '团队名',    'string', 'name',                   '团队的中文名，如"手机 HW"。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000004', 'english_name',           '英文名',    'string', 'english_name',           '团队英文名。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000004', 'function_category',      '职能类别',  'string', 'function_category',      'Engineering / Supply / Marketing / Legal / Channel / QA。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000004', 'weekly_capacity_hours',  '每周产能(h)','int',   'weekly_capacity_hours',  '团队每周总人时数（headcount × 40h）。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000004', 'headcount',              '人数',      'int',    'headcount',              '团队的当前人数。')
ON CONFLICT (object_type_id, name) DO NOTHING;

-- ── Dependency properties ─────────────────────────────────────────
INSERT INTO ont_property (project_id, object_type_id, name, display_name, data_type, source_column, short_description) VALUES
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000005', 'dependency_type', '依赖类型',     'string', 'dependency_type', 'blocks / requires / shares_resource / informs。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000005', 'lead_time_days',  '提前期(天)',    'int',    'lead_time_days',  '从源里程碑完成到目标里程碑可开始之间的天数。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000005', 'cross_launch',    '跨 Launch',    'bool',   'cross_launch',    'TRUE 表示该依赖横跨两个不同 Launch——"谁动谁就阻塞谁"的核心数据。')
ON CONFLICT (object_type_id, name) DO NOTHING;

-- ── Resource properties ───────────────────────────────────────────
INSERT INTO ont_property (project_id, object_type_id, name, display_name, data_type, source_column, short_description) VALUES
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000006', 'name',                  '资源名',     'string', 'name',                  '资源的可读名，如"主板厂 A"或"CCC 认证实验室"。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000006', 'category',              '类别',       'string', 'category',              'Factory / Assembly / Mold / Certification / Test / Packaging / Logistics。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000006', 'total_weekly_capacity', '周总容量',   'int',    'total_weekly_capacity', '该资源每周可提供的容量单位数。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000006', 'capacity_unit',         '容量单位',   'string', 'capacity_unit',         'units / hours / sessions。'),
('d0000000-0000-0000-0000-000000000001', 'd1000000-0000-0000-0000-000000000006', 'location',              '所在地',     'string', 'location',              '资源所在地（用于物流就近匹配）。')
ON CONFLICT (object_type_id, name) DO NOTHING;


-- ============================================================================
-- 4. Links — the 5+ edges the SmartQuery engine walks during the 5-join expand
-- ============================================================================

INSERT INTO ont_link_type (project_id, from_object_id, to_object_id, link_name, fk_column, cardinality, description) VALUES
-- Launch 1—N Workstream
('d0000000-0000-0000-0000-000000000001',
 'd1000000-0000-0000-0000-000000000001',
 'd1000000-0000-0000-0000-000000000002',
 'launch_has_workstreams', 'launch_id', '1:N',
 '一个 Launch 拥有多个 Workstream（硬件 / 软件 / 供应链 / 认证 / 营销 / 渠道 / QA）。'),

-- Workstream 1—N Milestone
('d0000000-0000-0000-0000-000000000001',
 'd1000000-0000-0000-0000-000000000002',
 'd1000000-0000-0000-0000-000000000003',
 'workstream_has_milestones', 'workstream_id', '1:N',
 '一个 Workstream 沿时间线分解为多个 Milestone（EVT / DVT / PVT / 量产 / 认证完成等）。'),

-- Workstream N—1 Team
('d0000000-0000-0000-0000-000000000001',
 'd1000000-0000-0000-0000-000000000002',
 'd1000000-0000-0000-0000-000000000004',
 'workstream_owner_team', 'owner_team_id', 'N:1',
 '每个 Workstream 归属一个团队负责。多个 Workstream 可能由同一团队负责——共享团队产能正是冲突的根源之一。'),

-- Milestone N—1 Resource
('d0000000-0000-0000-0000-000000000001',
 'd1000000-0000-0000-0000-000000000003',
 'd1000000-0000-0000-0000-000000000006',
 'milestone_uses_resource', 'required_resource_id', 'N:1',
 '关键里程碑需要占用某个共享资源（PCB 厂 / 组装线 / 认证实验室）。跨 Launch 同周抢占同一资源 = 跨-Launch 冲突。'),

-- Dependency N—1 Milestone (from)
('d0000000-0000-0000-0000-000000000001',
 'd1000000-0000-0000-0000-000000000005',
 'd1000000-0000-0000-0000-000000000003',
 'dependency_from_milestone', 'from_milestone_id', 'N:1',
 '依赖关系的起点（"该里程碑完成之后才能开始下游"）。'),

-- Dependency N—1 Milestone (to)
('d0000000-0000-0000-0000-000000000001',
 'd1000000-0000-0000-0000-000000000005',
 'd1000000-0000-0000-0000-000000000003',
 'dependency_to_milestone', 'to_milestone_id', 'N:1',
 '依赖关系的终点（"必须等上游完成才能开始的里程碑"）。')

ON CONFLICT DO NOTHING;


-- ============================================================================
-- 5. Metric Intent — the canonical "advance_launch_feasibility" query template
-- ============================================================================
-- This is the heart of the demo. Hero question "S25 Pro 能提前 2 周上市吗?"
-- recall-server maps the question to this Intent; agent-server's LLM only
-- fills params {launch, advance_weeks}; SmartQuery engine generates the
-- 5-JOIN deterministic SQL by walking the Link graph above.

INSERT INTO lakehouse_metric_intent (
    id, project_id, object_id, name, display_name,
    canonical_metric, canonical_filters, auto_group_by,
    response_template, description, priority, mark,
    default_order_by_label, default_order_by_dir, default_limit, parameters
) VALUES (
    'd2000000-0000-0000-0000-000000000001',
    'd0000000-0000-0000-0000-000000000001',
    'd1000000-0000-0000-0000-000000000001',  -- anchored on Launch
    'advance_launch_feasibility',
    '可否前移上市',
    'COUNT(DISTINCT Milestone.id)',
    '[]'::jsonb,
    ARRAY['Workstream.name', 'Team.name', 'Resource.name'],
    '{{Launch.name}} 当前计划 {{Launch.target_launch_date}} 上市。若前移 {{advance_weeks}} 周，将涉及 {{count}} 个里程碑、{{team_count}} 个团队、{{resource_count}} 类共享资源。其中跨-Launch 共享资源冲突 {{cross_launch_conflicts}} 处。',
    '回答"X Launch 能否前移 Y 周上市"——返回该 Launch 涉及的所有 Workstream / Milestone / Team / Resource / 跨-Launch 依赖。LLM 据此判断可行性与阻塞点。',
    10,
    TRUE,
    'Milestone.planned_date',
    'ASC',
    500,
    '[
        {"label": "launch", "kind": "string", "required": true, "description": "目标 Launch 名（如 S25 Pro）"},
        {"label": "advance_weeks", "kind": "int", "required": false, "default": 0, "description": "想要前移的周数；0 表示仅查现状"}
    ]'::jsonb
)
ON CONFLICT (project_id, name) DO NOTHING;


-- ============================================================================
-- 6. Keywords — what the user might type → Launch/Property/Intent
-- ============================================================================
-- 别名 / 业务知识词 / 触发词。recall-server 用这张表做 EXACT/FUZZY/VEC 三级召回。

-- Launch 名 + 别名 (指向 Launch.name property)
INSERT INTO lakehouse_keyword (project_id, object_type_id, property_id, keyword)
SELECT 'd0000000-0000-0000-0000-000000000001',
       'd1000000-0000-0000-0000-000000000001',
       (SELECT id FROM ont_property WHERE object_type_id = 'd1000000-0000-0000-0000-000000000001' AND name = 'name'),
       k
FROM (VALUES
    ('S25'), ('S25 Pro'), ('S25Pro'), ('S25 Slim'), ('S25 Lite'),
    ('A25'), ('A25 Pro'),
    ('Slim25'),         -- ← spec 要求的别名，指向 S25 Pro 的同一 property
    ('旗舰手机'),       -- ← 业务知识词；指向 Launch.name property，配合 ranking
    ('T-Pro 14'), ('X1 Carbon Slim'), ('Y Gaming 16'), ('Z Lite 13'), ('X1 Fold'),
    ('M14'), ('M14 Pro'), ('M14 Mini'),
    ('Watch S'), ('Watch S Pro'), ('Watch S Active'),
    ('Buds Air'), ('Buds Pro'), ('Buds Sport'), ('Over-Ear Pro'),
    ('P27 4K'), ('G27 165Hz'),
    ('智能手环'), ('折叠键盘')
) AS t(k)
ON CONFLICT (property_id, keyword) DO NOTHING;

-- 关键触发词 → advance_launch_feasibility Intent
INSERT INTO lakehouse_keyword (project_id, object_type_id, metric_intent_id, keyword)
SELECT 'd0000000-0000-0000-0000-000000000001',
       'd1000000-0000-0000-0000-000000000001',
       'd2000000-0000-0000-0000-000000000001',
       k
FROM (VALUES
    ('提前上市'),
    ('提前发布'),
    ('能否提前'),
    ('可否前移'),
    ('上市日期前移'),
    ('排期前移'),
    ('排期能不能调'),
    ('能不能调档期'),
    ('阻塞节点'),
    ('卡产能'),
    ('谁动谁就阻塞谁')
) AS t(k)
ON CONFLICT (property_id, keyword) DO NOTHING;

-- 业务知识词 → 相关 property (供 FUZZY/VEC tier 命中)
INSERT INTO lakehouse_keyword (project_id, object_type_id, property_id, keyword)
SELECT 'd0000000-0000-0000-0000-000000000001',
       'd1000000-0000-0000-0000-000000000003',  -- Milestone
       (SELECT id FROM ont_property WHERE object_type_id = 'd1000000-0000-0000-0000-000000000003' AND name = 'milestone_type'),
       k
FROM (VALUES
    ('量产'), ('MP'), ('mass_production'),
    ('首发'), ('一波'), ('first_release'),
    ('认证完成'), ('certification')
) AS t(k)
ON CONFLICT (property_id, keyword) DO NOTHING;

INSERT INTO lakehouse_keyword (project_id, object_type_id, property_id, keyword)
SELECT 'd0000000-0000-0000-0000-000000000001',
       'd1000000-0000-0000-0000-000000000005',  -- Dependency
       (SELECT id FROM ont_property WHERE object_type_id = 'd1000000-0000-0000-0000-000000000005' AND name = 'cross_launch'),
       k
FROM (VALUES
    ('跨产品线'), ('跨 Launch'), ('共享资源冲突'),
    ('谁卡谁'), ('上下游阻塞')
) AS t(k)
ON CONFLICT (property_id, keyword) DO NOTHING;


-- ============================================================================
-- Sanity checks
-- ============================================================================
DO $$
DECLARE cnt INTEGER;
BEGIN
    SELECT COUNT(*) INTO cnt FROM ont_object_type WHERE project_id = 'd0000000-0000-0000-0000-000000000001';
    ASSERT cnt = 6, format('demo ODs expected 6, got %s', cnt);

    SELECT COUNT(*) INTO cnt FROM ont_property WHERE project_id = 'd0000000-0000-0000-0000-000000000001';
    ASSERT cnt >= 30, format('demo properties expected >=30, got %s', cnt);

    SELECT COUNT(*) INTO cnt FROM ont_link_type WHERE project_id = 'd0000000-0000-0000-0000-000000000001';
    ASSERT cnt = 6, format('demo links expected 6, got %s', cnt);

    SELECT COUNT(*) INTO cnt FROM lakehouse_metric_intent WHERE project_id = 'd0000000-0000-0000-0000-000000000001';
    ASSERT cnt >= 1, format('demo intents expected >=1, got %s', cnt);

    SELECT COUNT(*) INTO cnt FROM lakehouse_keyword WHERE project_id = 'd0000000-0000-0000-0000-000000000001';
    ASSERT cnt >= 40, format('demo keywords expected >=40, got %s', cnt);

    RAISE NOTICE 'demo-seed ontology: 6 ODs, % properties, 6 Links, 1 Intent, % Keywords loaded',
                 (SELECT COUNT(*) FROM ont_property WHERE project_id = 'd0000000-0000-0000-0000-000000000001'),
                 (SELECT COUNT(*) FROM lakehouse_keyword WHERE project_id = 'd0000000-0000-0000-0000-000000000001');
END $$;
