-- demo-seed static · 25 Launch + 12 Team + 30 Resource
-- Mounted at /docker-entrypoint-initdb.d/03-demo-static.sql in demo profile.
-- Idempotent: ON CONFLICT DO NOTHING throughout.
-- Names match the deep-interview spec verbatim (S25 / S25 Pro / 手机HW / 主板厂 A / ...)

-- ==================== Teams (12) ====================
INSERT INTO demo_team (team_id, name, english_name, function_category, weekly_capacity_hours, headcount) VALUES
('T-PHONE-HW', '手机 HW',          'Phone HW',     'Engineering', 200, 5),
('T-PHONE-SW', '手机 SW',          'Phone SW',     'Engineering', 240, 6),
('T-PC-HW',    'PC HW',           'PC HW',        'Engineering', 200, 5),
('T-PC-SW',    'PC SW',           'PC SW',        'Engineering', 200, 5),
('T-IOT',      'IoT (表/耳机)',    'IoT',          'Engineering', 160, 4),
('T-PERI',     '外设 (键鼠/显示器)', 'Peripherals',  'Engineering', 120, 3),
('T-SUPPLY',   '供应链',           'Supply Chain', 'Supply',      240, 6),
('T-SDM',      'SDM',             'SDM',          'Supply',      160, 4),
('T-MKT',      '营销',             'Marketing',    'Marketing',   240, 6),
('T-LEGAL',    '法务',             'Legal',        'Legal',       120, 3),
('T-CHANNEL',  '渠道',             'Channel',      'Channel',     200, 5),
('T-QA',       'QA',              'QA',           'QA',          240, 6)
ON CONFLICT (team_id) DO NOTHING;

-- ==================== Resources (30) ====================
INSERT INTO demo_resource (resource_id, name, category, total_weekly_capacity, capacity_unit, location) VALUES
-- PCB factories (3) — shared bottleneck between phones and laptops
('R-PCB-A',     '主板厂 A',     'Factory',       1500, 'units',    '苏州'),
('R-PCB-B',     '主板厂 B',     'Factory',       1200, 'units',    '深圳'),
('R-PCB-C',     '主板厂 C',     'Factory',        900, 'units',    '合肥'),
-- Assembly lines (5)
('R-ASSY-1',    '组装线 1',     'Assembly',      2000, 'units',    '合肥'),
('R-ASSY-2',    '组装线 2',     'Assembly',      2000, 'units',    '合肥'),
('R-ASSY-3',    '组装线 3',     'Assembly',      1800, 'units',    '武汉'),
('R-ASSY-4',    '组装线 4',     'Assembly',      1500, 'units',    '深圳'),
('R-ASSY-5',    '组装线 5',     'Assembly',      1500, 'units',    '深圳'),
-- Mold shops (2)
('R-MOLD-X',    '模具厂 X',     'Mold',           80, 'units',    '东莞'),
('R-MOLD-Y',    '模具厂 Y',     'Mold',           60, 'units',    '苏州'),
-- Certification labs (4)
('R-CERT-FCC',  'FCC 认证实验室', 'Certification',  10, 'sessions', '深圳'),
('R-CERT-CE',   'CE 认证实验室',  'Certification',  10, 'sessions', '深圳'),
('R-CERT-CCC',  'CCC 认证实验室', 'Certification',   8, 'sessions', '北京'),
('R-CERT-3C',   '3C 认证实验室',  'Certification',   8, 'sessions', '北京'),
-- Test fixture pools (3)
('R-TEST-RF',   'RF 测试治具池',  'Test',           20, 'sessions', '深圳'),
('R-TEST-AUDIO','音频测试治具池', 'Test',           15, 'sessions', '苏州'),
('R-TEST-DROP', '跌落测试治具池', 'Test',           12, 'sessions', '苏州'),
-- Packaging lines (3)
('R-PKG-N',     '包装产线 北',    'Packaging',    1200, 'units',    '北京'),
('R-PKG-S',     '包装产线 南',    'Packaging',    1200, 'units',    '深圳'),
('R-PKG-E',     '包装产线 东',    'Packaging',    1000, 'units',    '上海'),
-- Logistics (2)
('R-LOG-N',     '物流仓 N',      'Logistics',    3000, 'units',    '北京'),
('R-LOG-S',     '物流仓 S',      'Logistics',    3500, 'units',    '深圳'),
-- Sample labs (2)
('R-SAMPLE-A',  '样机厂 A',      'Factory',        50, 'units',    '深圳'),
('R-SAMPLE-B',  '样机厂 B',      'Factory',        40, 'units',    '苏州'),
-- Color tuning labs (2)
('R-COLOR-A',   '色彩调校 A',    'Test',           20, 'sessions', '苏州'),
('R-COLOR-B',   '色彩调校 B',    'Test',           15, 'sessions', '上海'),
-- Reliability lab (1)
('R-RELY',      '可靠性实验室',   'Test',           10, 'sessions', '苏州'),
-- Mfg engineering process pools (3)
('R-MFG-PHONE', '手机制程',       'Assembly',       30, 'sessions', '合肥'),
('R-MFG-PC',    'PC 制程',       'Assembly',       25, 'sessions', '合肥'),
('R-MFG-IOT',   'IoT 制程',      'Assembly',       20, 'sessions', '武汉')
ON CONFLICT (resource_id) DO NOTHING;

-- ==================== Launches (25) ====================
-- Time anchor: today = 2027-03-15. All targets spread across 2027 Q2-Q4.
-- S25 Pro at 2027-09-15 (Q3 mid) — the hero question targets this row.

INSERT INTO demo_launch (launch_id, name, product_line, internal_codename, target_market, target_launch_date, status, project_lead, description) VALUES

-- ── Phones (6) ─────────────────────────────────────────────────
('L-S25',         'S25',           'Phone',    'Orion',      'Global',         '2027-09-01', 'on_track', 'Wang Lei',      'S 系列标准款，主销机型，量产准备中'),
('L-S25-PRO',     'S25 Pro',       'Phone',    'Slim25',     'Global',         '2027-09-15', 'on_track', 'Chen Xiaomi',   '旗舰手机，业务焦点；与 X1 Carbon Slim 共享主板厂 A'),
('L-S25-SLIM',    'S25 Slim',      'Phone',    'ThinFold',   'Global',         '2027-10-05', 'planned',  'Liu Jin',       '超薄机身工程探索版'),
('L-S25-LITE',    'S25 Lite',      'Phone',    'Comet',      'Asia-Pacific',   '2027-10-20', 'planned',  'Zhang Wei',     'S 系列下沉款，新兴市场为主'),
('L-A25',         'A25',           'Phone',    'Atlas',      'CN-only',        '2027-11-10', 'planned',  'Sun Ming',      'A 系列中端，国内首发'),
('L-A25-PRO',     'A25 Pro',       'Phone',    'Atlas-X',    'CN-only',        '2027-11-25', 'planned',  'Sun Ming',      'A 系列中端 Pro'),

-- ── Laptops (5) ────────────────────────────────────────────────
('L-T-PRO-14',    'T-Pro 14',      'Laptop',   'Triton14',   'Global',         '2027-05-15', 'on_track', 'Brian Zhao',    'ThinkPad-style 商务旗舰'),
('L-X1-CARBON-S', 'X1 Carbon Slim','Laptop',   'CarbonAir',  'Global',         '2027-09-22', 'at_risk',  'Helen Liu',     'X1 Carbon 超薄版；与 S25 Pro 共享主板厂 A 关键周'),
('L-Y-GAMING-16', 'Y Gaming 16',   'Laptop',   'Yokai16',    'Global',         '2027-08-10', 'on_track', 'Marcus Yang',   'Y 系列游戏旗舰'),
('L-Z-LITE-13',   'Z Lite 13',     'Laptop',   'ZephyrLite', 'Asia-Pacific',   '2027-11-05', 'planned',  'Helen Liu',     'Z 系列轻薄入门款'),
('L-X1-FOLD',     'X1 Fold',       'Laptop',   'OrigamiX',   'Global',         '2027-12-10', 'planned',  'Brian Zhao',    'X1 折叠屏笔记本，新品类探索'),

-- ── Tablets (3) ────────────────────────────────────────────────
('L-M14',         'M14',           'Tablet',   'Manta14',    'Global',         '2027-06-20', 'on_track', 'Daisy Wu',      'M 系列标准平板'),
('L-M14-PRO',     'M14 Pro',       'Tablet',   'Manta14X',   'Global',         '2027-10-15', 'planned',  'Daisy Wu',      'M14 Pro 旗舰平板'),
('L-M14-MINI',    'M14 Mini',      'Tablet',   'MantaMini',  'Asia-Pacific',   '2027-07-25', 'on_track', 'Eric Tan',      'M14 紧凑版'),

-- ── Wearables (3) ──────────────────────────────────────────────
('L-WATCH-S',     'Watch S',       'Wearable', 'Saturn',     'Global',         '2027-04-15', 'launched', 'Iris Ma',       '腕表标准款，已上市，作为基线数据'),
('L-WATCH-S-PRO', 'Watch S Pro',   'Wearable', 'SaturnPro',  'Global',         '2027-09-20', 'on_track', 'Iris Ma',       '腕表 Pro，与 S25 Pro 跨品类联动'),
('L-WATCH-ACT',   'Watch S Active','Wearable', 'SaturnAct',  'Global',         '2027-11-30', 'planned',  'Iris Ma',       '运动版腕表'),

-- ── Audio (4) ──────────────────────────────────────────────────
('L-BUDS-AIR',    'Buds Air',      'Audio',    'Zephyr',     'Global',         '2027-05-05', 'launched', 'Ken Pan',       '入门 TWS，已上市'),
('L-BUDS-PRO',    'Buds Pro',      'Audio',    'ZephyrPro',  'Global',         '2027-09-18', 'on_track', 'Ken Pan',       'Buds Pro，与 S25 Pro 同档发布'),
('L-BUDS-SPORT',  'Buds Sport',    'Audio',    'ZephyrFit',  'Global',         '2027-08-25', 'on_track', 'Ken Pan',       'Buds 运动版'),
('L-OVEREAR-PRO', 'Over-Ear Pro',  'Audio',    'AuralPro',   'Global',         '2027-12-05', 'planned',  'Ken Pan',       '头戴旗舰耳机'),

-- ── Displays (2) ───────────────────────────────────────────────
('L-P27-4K',      'P27 4K',        'Display',  'Pixel27',    'Global',         '2027-07-10', 'on_track', 'Owen Shi',      '27" 4K 专业显示器'),
('L-G27-165',     'G27 165Hz',     'Display',  'Glance27',   'Global',         '2027-10-30', 'planned',  'Owen Shi',      '27" 165Hz 游戏显示器'),

-- ── Other (2) ──────────────────────────────────────────────────
('L-BAND',        '智能手环',       'Other',    'Pulse',      'CN-only',        '2027-06-15', 'on_track', 'Iris Ma',       '基础腕带，对标小米手环'),
('L-FOLD-KB',     '折叠键盘',       'Other',    'OrigamiKey', 'Global',         '2027-08-20', 'planned',  'Owen Shi',      '配件类折叠键盘')

ON CONFLICT (launch_id) DO NOTHING;

-- ==================== Sanity checks ====================
DO $$
DECLARE
    cnt INTEGER;
BEGIN
    SELECT COUNT(*) INTO cnt FROM demo_team;
    ASSERT cnt = 12, format('demo_team expected 12 rows, got %s', cnt);

    SELECT COUNT(*) INTO cnt FROM demo_resource;
    ASSERT cnt = 30, format('demo_resource expected 30 rows, got %s', cnt);

    SELECT COUNT(*) INTO cnt FROM demo_launch;
    ASSERT cnt = 25, format('demo_launch expected 25 rows, got %s', cnt);

    SELECT COUNT(*) INTO cnt FROM demo_launch WHERE launch_id = 'L-S25-PRO';
    ASSERT cnt = 1, 'S25 Pro hero row missing';

    RAISE NOTICE 'demo-seed static: 12 teams, 30 resources, 25 launches loaded OK';
END $$;
