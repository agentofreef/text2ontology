-- demo-seed DDL · staging tables for the text2ontology demo
-- Mounted at /docker-entrypoint-initdb.d/02-demo-ddl.sql in demo profile.
-- Idempotent: all CREATE statements use IF NOT EXISTS.
-- Tables are namespaced with `demo_` prefix so they don't collide with real ingestion.

-- ==================== demo_team (12 rows) ====================
CREATE TABLE IF NOT EXISTS demo_team (
    team_id              VARCHAR(20)  PRIMARY KEY,
    name                 VARCHAR(100) NOT NULL,
    english_name         VARCHAR(100) NOT NULL,
    function_category    VARCHAR(50)  NOT NULL,  -- Engineering | Supply | Marketing | Legal | Channel | QA
    weekly_capacity_hours INTEGER     NOT NULL,
    headcount            INTEGER      NOT NULL,
    created_at           TIMESTAMPTZ  DEFAULT now()
);

-- ==================== demo_resource (~30 rows) ====================
CREATE TABLE IF NOT EXISTS demo_resource (
    resource_id          VARCHAR(20)  PRIMARY KEY,
    name                 VARCHAR(100) NOT NULL,
    category             VARCHAR(50)  NOT NULL,  -- Factory | Assembly | Mold | Certification | Test | Packaging | Logistics
    total_weekly_capacity INTEGER     NOT NULL,
    capacity_unit        VARCHAR(20)  NOT NULL,  -- units | hours | sessions
    location             VARCHAR(50),
    created_at           TIMESTAMPTZ  DEFAULT now()
);

-- ==================== demo_launch (25 rows) ====================
CREATE TABLE IF NOT EXISTS demo_launch (
    launch_id            VARCHAR(20)  PRIMARY KEY,
    name                 VARCHAR(100) NOT NULL,
    product_line         VARCHAR(20)  NOT NULL,  -- Phone | Laptop | Tablet | Wearable | Audio | Display | Other
    internal_codename    VARCHAR(50),            -- e.g. "Slim25" alias source
    target_market        VARCHAR(50)  NOT NULL,  -- Global | CN-only | Asia-Pacific
    target_launch_date   DATE         NOT NULL,  -- 2027 Q2-Q4
    status               VARCHAR(20)  NOT NULL,  -- planned | on_track | at_risk | delayed | launched
    project_lead         VARCHAR(50)  NOT NULL,
    description          TEXT,
    created_at           TIMESTAMPTZ  DEFAULT now(),
    updated_at           TIMESTAMPTZ  DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_demo_launch_product_line ON demo_launch(product_line);
CREATE INDEX IF NOT EXISTS idx_demo_launch_target_date ON demo_launch(target_launch_date);
CREATE INDEX IF NOT EXISTS idx_demo_launch_status ON demo_launch(status);

-- ==================== demo_workstream (~140 rows, generated) ====================
CREATE TABLE IF NOT EXISTS demo_workstream (
    workstream_id        VARCHAR(30)  PRIMARY KEY,
    launch_id            VARCHAR(20)  NOT NULL REFERENCES demo_launch(launch_id) ON DELETE CASCADE,
    name                 VARCHAR(150) NOT NULL,
    workstream_type      VARCHAR(50)  NOT NULL,  -- HW Design | SW Dev | Supply Chain | Certification | Marketing | Channel | QA | Legal
    owner_team_id        VARCHAR(20)  NOT NULL REFERENCES demo_team(team_id),
    start_date           DATE         NOT NULL,
    end_date             DATE         NOT NULL,
    status               VARCHAR(20)  NOT NULL,  -- planned | in_progress | at_risk | done | blocked
    weekly_hours_required INTEGER     NOT NULL,
    created_at           TIMESTAMPTZ  DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_demo_workstream_launch ON demo_workstream(launch_id);
CREATE INDEX IF NOT EXISTS idx_demo_workstream_team ON demo_workstream(owner_team_id);
CREATE INDEX IF NOT EXISTS idx_demo_workstream_type ON demo_workstream(workstream_type);

-- ==================== demo_milestone (~400 rows, generated) ====================
CREATE TABLE IF NOT EXISTS demo_milestone (
    milestone_id         VARCHAR(30)  PRIMARY KEY,
    workstream_id        VARCHAR(30)  NOT NULL REFERENCES demo_workstream(workstream_id) ON DELETE CASCADE,
    name                 VARCHAR(150) NOT NULL,
    milestone_type       VARCHAR(50)  NOT NULL,  -- kickoff | design_review | EVT | DVT | PVT | pp_build | mass_production | certification | first_release | gtm_ready
    planned_date         DATE         NOT NULL,
    actual_date          DATE,                   -- nullable; only set when actually completed
    status               VARCHAR(20)  NOT NULL,  -- planned | in_progress | done | at_risk | blocked
    is_critical          BOOLEAN      DEFAULT FALSE,
    required_resource_id VARCHAR(20)  REFERENCES demo_resource(resource_id),
    required_capacity    INTEGER      DEFAULT 0, -- units of resource needed during this milestone
    notes                TEXT,
    created_at           TIMESTAMPTZ  DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_demo_milestone_workstream ON demo_milestone(workstream_id);
CREATE INDEX IF NOT EXISTS idx_demo_milestone_type ON demo_milestone(milestone_type);
CREATE INDEX IF NOT EXISTS idx_demo_milestone_planned ON demo_milestone(planned_date);
CREATE INDEX IF NOT EXISTS idx_demo_milestone_resource ON demo_milestone(required_resource_id) WHERE required_resource_id IS NOT NULL;

-- ==================== demo_dependency (~280 rows, generated) ====================
CREATE TABLE IF NOT EXISTS demo_dependency (
    dependency_id        VARCHAR(40)  PRIMARY KEY,
    from_milestone_id    VARCHAR(30)  NOT NULL REFERENCES demo_milestone(milestone_id) ON DELETE CASCADE,
    to_milestone_id      VARCHAR(30)  NOT NULL REFERENCES demo_milestone(milestone_id) ON DELETE CASCADE,
    dependency_type      VARCHAR(20)  NOT NULL,  -- blocks | requires | shares_resource | informs
    lead_time_days       INTEGER      DEFAULT 0,
    cross_launch         BOOLEAN      DEFAULT FALSE,  -- true if from and to belong to different Launches
    notes                TEXT,
    created_at           TIMESTAMPTZ  DEFAULT now(),
    CHECK (from_milestone_id <> to_milestone_id)
);
CREATE INDEX IF NOT EXISTS idx_demo_dependency_from ON demo_dependency(from_milestone_id);
CREATE INDEX IF NOT EXISTS idx_demo_dependency_to   ON demo_dependency(to_milestone_id);
CREATE INDEX IF NOT EXISTS idx_demo_dependency_type ON demo_dependency(dependency_type);
CREATE INDEX IF NOT EXISTS idx_demo_dependency_cross ON demo_dependency(cross_launch) WHERE cross_launch = TRUE;

-- ==================== Comment block ====================
COMMENT ON TABLE demo_launch IS
  'Demo: 25 product launches across phones/laptops/tablets/wearables/audio/displays. Time anchor: 2027 Q2-Q4.';
COMMENT ON TABLE demo_team IS
  'Demo: 12 cross-functional teams (HW/SW/IoT/peripherals/supply/SDM/marketing/legal/channel/QA).';
COMMENT ON TABLE demo_resource IS
  'Demo: ~30 shared resources (PCB factories, assembly lines, mold shops, cert labs, packaging lines).';
COMMENT ON TABLE demo_workstream IS
  'Demo: ~140 workstreams. Each Launch breaks into ~5-7 workstreams owned by different teams.';
COMMENT ON TABLE demo_milestone IS
  'Demo: ~400 milestones. Sequenced within each workstream; some are critical-path nodes.';
COMMENT ON TABLE demo_dependency IS
  'Demo: ~280 directed dependencies. cross_launch=true rows are the inter-Launch blocking edges that make "可否前移" non-trivial.';
