# demo-seed

Pre-baked business scenario for the text2ontology public demo. Loads a virtual consumer-electronics company (25 product launches, 12 cross-functional teams, ~30 shared resources, ~544 milestones, ~608 dependencies) into a clean Postgres on first start, so visitors can walk through the hero question **"S25 Pro 能提前 2 周上市吗?"** without any setup.

See full spec: [`.omc/specs/deep-interview-demo-launch-scenario.md`](../../.omc/specs/deep-interview-demo-launch-scenario.md).

## Files

| File | Purpose | Mounted as |
|---|---|---|
| `ddl.sql` | 6 staging tables (`demo_launch` / `demo_workstream` / ...) | `z-01-demo-ddl.sql` |
| `seed_static.sql` | 25 Launches + 12 Teams + 30 Resources (hand-curated, names verbatim from spec) | `z-02-demo-static.sql` |
| `generate.py` | Generator script for Workstream/Milestone/Dependency (deterministic) | — |
| `seed_generated.sql` | Output of `generate.py` (~163 WS / ~544 MS / ~608 Dep, including 111 cross-launch resource conflicts) | `z-03-demo-generated.sql` |
| `seed_ontology.sql` | Demo project + 6 ODs + 6 Links + 1 Intent + ~50 Keywords | `z-04-demo-ontology.sql` |

The `z-` prefix on the mount names ensures these run **after** the base `schema.sql` (which creates `ont_*` / `lakehouse_*` tables) inside Postgres's `/docker-entrypoint-initdb.d/`.

## How to bring it up

```bash
# 1) (optional) regenerate the workstream/milestone/dependency SQL
python3 scripts/demo-seed/generate.py

# 2) start the full stack with the demo overlay
docker compose -f docker-compose.yml -f docker-compose.demo.yml up -d

# 3) verify (one-shot sanity check)
docker compose -f docker-compose.yml -f docker-compose.demo.yml --profile demo-verify run --rm demo-init-check

# 4) open
open http://localhost:18080
```

Login as `admin` / your `.env.shared` `ADMIN_PASSWORD`.
The demo project is named **"Demo · 跨部门新品上市"** (uuid `d0000000-0000-0000-0000-000000000001`).

## Reset

```bash
docker compose -f docker-compose.yml -f docker-compose.demo.yml down -v
```

The `-v` wipes the `postgres-data-demo` volume, so the next `up` re-runs all seeds fresh.

The demo volume is **separate** from the real `postgres-data` volume — your real ingested projects are untouched by switching profiles.

## Hero question

```
S25 Pro 能提前 2 周上市吗?
```

Recall will resolve `S25 Pro` → `Launch.name`, hit the `advance_launch_feasibility` Intent (anchored on Launch), and the engine walks the 5+ Link edges
(`Launch → Workstream → Milestone → Resource`, `Workstream → Team`,
`Dependency → Milestone × 2`) to assemble a deterministic JOIN.

The pre-loaded data is shaped so:
- `S25 Pro` (Q3 2027, target `2027-09-15`) shares Main-PCB-A with `X1 Carbon Slim` (Q3 2027 `2027-09-22`) — the cross-launch resource conflict.
- Multiple wearables/audio launches (`Watch S Pro`, `Buds Pro`) target the same week — additional cross-launch CCC certification load.
- The reply text from the canned LLM summary (or any real LLM with the rendered context) can honestly say "可前移 ~12 天 ≠ 14 天, 卡点是 PC HW + CCC 认证".

## Determinism

`generate.py` uses `random.seed(42)`. Re-running produces byte-identical `seed_generated.sql`. This makes the file safe to commit and `git diff`-meaningful.

## What's intentionally NOT here

- No `prop_vector` embeddings on ontology rows — VEC tier won't work, but the demo's hero question is designed to hit EXACT/FUZZY recall. Embeddings can be added later by running the recall-server's index-rebuild routine against the demo project.
- No failure→fix flow demo data — this seed pack supports the 1-Act, 2-3-minute single-hero-question demo. A separate seed pack will be built for the failure→fix demo.
- No `ont_knowledge` / `ont_causality` / `ont_learned_fact` rows — those layers are intentionally omitted from this demo (paradigm focus is 5-join expansion, not business-knowledge maintenance).

## Maintainer notes

If you add a new `Launch` row to `seed_static.sql`, also update the `LAUNCHES` list in `generate.py` and re-run. The script will refuse to run if `len(LAUNCHES) != 25` — easy guardrail to catch drift.
