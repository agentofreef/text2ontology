# Analyst-validator bench fixture

Phase 3 acceptance gate (per the consensus plan, line 617): each of the 5
validators should run in **p95 < 3000 ms** against a representative bench:

- 1 OD with 20 properties
- 1 source table with 100,000 rows
- 1 link (1 FK)
- 1 intent (canonical metric + auto group-by)

For MVP this bench is **informational only** — there is no automated
regression gate enforcing the p95. The plan defers automated p95
enforcement to v1.1 once we have a stable baseline. This directory
exists so the fixture is checked-in and runnable on demand.

## Files

| File | Purpose |
|------|---------|
| `setup.sql` | Idempotent schema+data builder. Creates a `bench_analyst_*` schema and populates 100k rows. Safe to re-run. |
| `run-bench.sh` | Runs each of the 5 validator endpoints once and reports observed wall-clock latency. |

## Prerequisites

- `DATABASE_URL` set to a Postgres instance with `pgcrypto` + `analyst_*`
  tables already migrated (i.e. one that the agent-server has talked to).
- `LAKEHOUSE_SQL_URL` reachable (defaults to `http://127.0.0.1:18094`).
- `INTERNAL_TOKEN` matching the lakehouse-sql-server's env.

## Running

```bash
cd scripts/bench/analyst-validators
psql "$DATABASE_URL" -f setup.sql
./run-bench.sh
```

The script prints one line per validator:

```
[validate_semantic_sql]            812 ms  PASS
[validate_grain]                   413 ms  PASS
[validate_referential_integrity]  1284 ms  PASS
[validate_machine_code]            697 ms  PASS
[validate_intent_runs]             956 ms  PASS
```

## When to revisit

- If any validator regresses past 3000 ms on this fixture during a code
  change, investigate before merging.
- If the bench data needs to grow past 100k rows (e.g. to stress wider
  staging tables), update `setup.sql` and the README simultaneously.
- v1.1 milestone: lift the p95 latency from informational into a CI gate
  enforced by `make analyst-validator-bench`.
