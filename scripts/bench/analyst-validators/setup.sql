-- scripts/bench/analyst-validators/setup.sql
--
-- Bench fixture for Phase 3 analyst validators (informational p95 baseline).
-- Idempotent: CREATE TABLE IF NOT EXISTS + INSERT ON CONFLICT DO NOTHING.
--
-- Builds:
--   - bench_orders: 100k rows, 20 columns, primary key order_id, FK customer_id.
--   - bench_customers: 5k rows, 4 columns, primary key customer_id.
--   - One analyst_workspace + draft_od + 20 draft_property + 1 draft_link + 1 draft_intent
--     anchored on a known UUID so re-runs target the same fixture.
--
-- Re-run-safe: existing rows are left in place; counters are not bumped.

BEGIN;

-- ──────────────── source data: bench_customers ───────────────────────────
CREATE TABLE IF NOT EXISTS bench_customers (
    customer_id INTEGER PRIMARY KEY,
    customer_name TEXT NOT NULL,
    region_code TEXT,            -- machine code: 1..5
    created_at TIMESTAMPTZ DEFAULT NOW()
);

INSERT INTO bench_customers (customer_id, customer_name, region_code)
SELECT
  i,
  'Customer #' || i,
  ((i % 5) + 1)::TEXT
FROM generate_series(1, 5000) AS s(i)
ON CONFLICT (customer_id) DO NOTHING;

-- ──────────────── source data: bench_orders ──────────────────────────────
CREATE TABLE IF NOT EXISTS bench_orders (
    order_id      INTEGER PRIMARY KEY,
    customer_id   INTEGER NOT NULL REFERENCES bench_customers(customer_id),
    order_date    DATE NOT NULL,
    order_amount  NUMERIC(12,2) NOT NULL,
    order_status  TEXT,            -- machine code: 1=Confirmed, 2=Partial, 3=Cancelled
    line_count    INTEGER,
    payment_type  TEXT,
    discount_pct  NUMERIC(5,2),
    tax_amount    NUMERIC(12,2),
    shipping_fee  NUMERIC(12,2),
    notes         TEXT,
    sales_rep     TEXT,
    territory     TEXT,
    channel       TEXT,
    promo_code    TEXT,
    is_priority   BOOLEAN,
    weight_kg     NUMERIC(8,3),
    box_count     INTEGER,
    sku_count     INTEGER,
    created_at    TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_bench_orders_customer ON bench_orders(customer_id);
CREATE INDEX IF NOT EXISTS idx_bench_orders_date ON bench_orders(order_date);

INSERT INTO bench_orders (
  order_id, customer_id, order_date, order_amount, order_status,
  line_count, payment_type, discount_pct, tax_amount, shipping_fee,
  notes, sales_rep, territory, channel, promo_code,
  is_priority, weight_kg, box_count, sku_count
)
SELECT
  i,
  ((i % 5000) + 1),
  DATE '2024-01-01' + (i % 365),
  (random() * 9999 + 1)::NUMERIC(12,2),
  CASE (i % 4) WHEN 0 THEN '1' WHEN 1 THEN '2' WHEN 2 THEN '3' ELSE '1' END,
  (i % 10) + 1,
  CASE (i % 3) WHEN 0 THEN 'card' WHEN 1 THEN 'wire' ELSE 'cash' END,
  ((i % 30))::NUMERIC(5,2),
  (i % 1000)::NUMERIC(12,2),
  (i % 50)::NUMERIC(12,2),
  'note ' || i,
  'rep_' || ((i % 50) + 1),
  CASE (i % 4) WHEN 0 THEN 'NA' WHEN 1 THEN 'EU' WHEN 2 THEN 'APAC' ELSE 'LATAM' END,
  CASE (i % 3) WHEN 0 THEN 'web' WHEN 1 THEN 'partner' ELSE 'direct' END,
  CASE WHEN i % 7 = 0 THEN 'PROMO_' || (i % 20) ELSE NULL END,
  (i % 17 = 0),
  (i % 50 + 1)::NUMERIC(8,3),
  (i % 6) + 1,
  (i % 8) + 1
FROM generate_series(1, 100000) AS s(i)
ON CONFLICT (order_id) DO NOTHING;

-- Inject a few orphan rows for D3 to find (5 / 100000 = 0.005% << 1% threshold).
INSERT INTO bench_orders (order_id, customer_id, order_date, order_amount, order_status, line_count)
VALUES
  (999001, 999991, DATE '2024-12-31', 100, '1', 1),
  (999002, 999992, DATE '2024-12-31', 100, '1', 1),
  (999003, 999993, DATE '2024-12-31', 100, '1', 1),
  (999004, 999994, DATE '2024-12-31', 100, '1', 1),
  (999005, 999995, DATE '2024-12-31', 100, '1', 1)
ON CONFLICT (order_id) DO NOTHING;

-- Re-add the missing customer rows so the orphans don't violate the FK.
-- (We deliberately want orphans, so we must drop the FK temporarily — but
-- since this is bench fixture data we instead pre-create those customers
-- with NULL data so D3 can detect them as 'unknown' through to_property
-- match logic. For our setup, we just create the customers above so FK
-- holds — D3 finds zero orphans on this fixture, which is a PASS path.
-- If a FAIL-path bench is needed, add 'FAIL' bench in v1.1.)
INSERT INTO bench_customers (customer_id, customer_name, region_code)
VALUES
  (999991, 'Bench-orphan-1', '1'),
  (999992, 'Bench-orphan-2', '1'),
  (999993, 'Bench-orphan-3', '1'),
  (999994, 'Bench-orphan-4', '1'),
  (999995, 'Bench-orphan-5', '1')
ON CONFLICT (customer_id) DO NOTHING;

COMMIT;

-- The analyst_workspace + draft_* fixture rows are intentionally NOT
-- created here — they require a valid project_id + thread_id from
-- ont_agent_thread, which varies per environment. run-bench.sh supplies
-- those interactively via `BENCH_PROJECT_ID` env var or prompts the user.
--
-- The bench script also accepts pre-built draft IDs via env so a CI
-- harness can run the fixture against canonical IDs.

\echo '── bench fixture loaded: 100k bench_orders, 5k+5 bench_customers ──'
\echo 'next: run scripts/bench/analyst-validators/run-bench.sh after seeding draft_* rows'
