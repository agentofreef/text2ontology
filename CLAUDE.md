# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is (and the one invariant that explains everything)

text2ontology turns natural language into data analysis **without letting the LLM write executable
queries**. The LLM only picks `(Object Definition, Intent, Keyword)` from finite, organization-curated
sets and fills in parameters; a deterministic compiler (the SmartQuery engine) stitches the JOINs from
pre-defined `ont_link` relationships and emits Postgres SQL. **The LLM never sees a table or a JOIN.**
Every wrong answer therefore has an *address* — which Intent, which alias, which causality edge — so it
can be fixed once in the ontology rather than re-prompted. Keep this in mind for any agent/query change:
do not introduce a path where the model emits free-form SQL/DSL.

The ontology is curated by humans, not auto-learned. Read `docs/manifesto/manifesto.en.md` and
`docs/spec/design-philosophy.en.md` for the full thesis.

## Naming (three coexisting names — do not "fix" them)

- **Product / repo / Docker images / compose**: `text2ontology` (`ghcr.io/agentofreef/text2ontology-*`).
- **Go module import prefix + internal architecture docs**: `github.com/lakehouse2ontology/...`. The
  project was renamed; import paths and `docs/architecture-lakehouse2ontology.md` still say
  `lakehouse2ontology`. This is expected, not a bug.
- **Frontend base path**: `/lakehouse` (legacy URL tree). All routes live under it.

DuckDB, DAX execution, the Power BI live connector, and the old `daxengine/`/`/dax/*` tree have been
**fully removed**. Postgres is the only OLAP backend. Do not reference the removed pieces.

## Build / run / test

ONE compose file — `docker-compose.yml` — the hardened single-ingress stack. It pulls
prebuilt images; `docker compose` auto-reads `.env` for overrides, and every secret has a
safe dev default, so it runs with zero config.

```bash
docker compose up -d  # pull prebuilt images + start the whole stack (zero config)
make up / make down   # same, via the Makefile
make build            # MAINTAINERS: build all 8 images locally (docker build); CI publishes :latest on push to main
make health           # probe gateway :28080 + container status
make logs SVC=agent-server
make test             # go build + go test ./... across the services
make db-psql          # psql shell inside the postgres container
make help             # full target list
```

Local quick start: `docker compose up -d`, then open http://localhost:28080 (admin / admin).
For production: `cp .env.example .env`, set strong secrets, `REQUIRE_STRONG_SECRETS=true`.

Single Go service / single test (run inside the service dir — the `go.work` workspace ties modules together):

```bash
cd services/agent-server && go build ./... && go test ./...
cd services/agent-server && go test ./handler/ -run TestBuilder -timeout 30s   # one test
```

Frontend (Next.js 16 static export; output goes to `frontend/out/`, served by the frontend nginx behind the gateway):

```bash
cd frontend && npm run build            # static export to out/ — the ONLY correct build
cd frontend && npm run dev              # dev server on :3000
cd frontend && npx tsc --noEmit         # typecheck (CI gate)
```

When running `npm run dev` against local services, set
`NEXT_PUBLIC_BACKEND_API_URL=http://127.0.0.1:18090`,
`NEXT_PUBLIC_AGENT_SERVER_URL=http://127.0.0.1:18092`,
`NEXT_PUBLIC_RECALL_SERVER_URL=http://127.0.0.1:18093`.

### CI gates (these must pass — run them before claiming done)

- `go vet ./...` and `go test ./...` for all 6 services (tests skip cleanly with empty `DATABASE_URL`).
- `bash scripts/check-service-deps.sh` — no service imports another service's packages.
- `bash scripts/check-layer-deps.sh` — 4-layer import direction (see below).
- `govulncheck ./...` across all 9 `pkg/*` modules + 6 services.
- Frontend: `npm install` → `npx tsc --noEmit` → `npm run build` (Node 22, Go 1.25).
- `staticcheck.conf` exists for `staticcheck ./...` (not in CI but configured).

## Architecture

### Topology (one isolated `docker compose` stack)

| Container | Port (host) | Role |
|---|---|---|
| gateway (nginx) | 28080 | **Single ingress** — the ONLY published host port. Reverse-proxies to frontend + services by path. |
| frontend | internal | Next.js static export served by internal nginx; reached only via gateway. |
| backend-api | 18090 | CRUD for `ont_*` / `lakehouse_*`, auth, projects, config, export/import. |
| agent-server | 18092 | Lakehouse Agent SSE, Thread Memory Ledger, annotations, dataset testing. |
| recall-server | 18093 | 3-tier recall (EXACT / FUZZY / VEC) over `ont_*`. |
| lakehouse-sql-server | 18094 | SmartQuery engine: `QuerySpec` → Postgres SQL gen + execution. |
| mcp-tools-server | 18095 | MCP tool gateway (`lookup_od`, `execute_smartquery`, …) — thin proxy, no direct DB. |
| collector-server | 18096 | **Sole** data-ingest entry: PBI / Postgres / File connectors + wizard. |
| postgres (pgvector/pgvector:pg16) | 5438 | Single source of truth + vector similarity. Bundled. |
| otel-collector / jaeger / prometheus / grafana | 16686 / 9090 / 3000 (loopback) | Observability. |

`docker-compose.yml` is the SINGLE, hardened, production-grade stack (compose project
`text2ontology`): it publishes **only** the gateway (host `28080`); Postgres, the 6 Go services,
and the observability stack are internal (obs UIs bind to 127.0.0.1). It pulls prebuilt `:latest`
images (no build sections — maintainers build with `make build`); runs zero-config with safe dev
defaults; set strong secrets + `REQUIRE_STRONG_SECRETS=true` in `.env` for production. The per-table
"Port (host)" values below are the services' INTERNAL ports — only the gateway is published. (The
old per-service-port dev compose and the demo compose were removed — there is one compose file now.)

### 4-layer hexagonal (enforced on every PR by `scripts/check-layer-deps.sh`)

```
OntologyAgent     services/*/handler/      [top]
SmartqueryEngine  services/*/smartquery/   <-+ intentional mutual edge, broken at runtime
LakehouseStore    services/*/lakehouse/    --+ via CatalogReader/StagingTarget interface inversion
IngestionPort     services/*/ingest/       [bottom]
```

Forbidden edges: `ingest` must not import `smartquery`/`lakehouse`/`handler`; `smartquery`/`lakehouse`
must not import `ingest`/`handler`. Port interfaces live in `ports.go` within each layer package.
Services talk to each other over **HTTP only** — never by importing a sibling's Go packages. Shared
types come from `pkg/contracts/` (frozen, additive-only during the cutover window).

### Shared libraries (`pkg/`, 9 modules in `go.work`)

`authmw` (bearer + internal-token middleware), `contracts` (frozen DTOs), `dsnguard` (DSN validation at
startup), `httputil` (CORS, SSE helpers, `/healthz?check=db`), `llmclient` (role-based model config,
embedding, streaming, thinking-model detection), `observability` (OTel + Prometheus), plus `mission`,
`ontology`, `srvkit`. `pkg/*` must never import from `services/`.

## Cross-cutting conventions you must respect

- **`project_id` scoping**: every core `ont_*` / `lakehouse_*` table has a `project_id`; every handler
  must scope queries to the caller's project. Access is gated by `project_member`.
- **Internal service-to-service calls** ride `/internal/*` paths with `X-Internal-Token` +
  `X-On-Behalf-Of` headers, enforced by `pkg/authmw`. Public routes use HMAC-signed bearer tokens.
- **SSE streaming** (agent responses, ingest progress) requires `http.Flusher`. `pkg/authmw`'s
  `statusRecorder` forwards `Flush()` so SSE survives the auth middleware — don't break that.
- **Two agent modes**, distinguished by `agent_type` on the thread (immutable once set):
  `lakehouse` (NL → SmartQuery → answer) and `builder` (interview-driven OD/Intent/Link creation with a
  `mark=false → human activate` lifecycle; ≥3-turn interview gate before proposing).
- **pgvector**: vector columns are `vector(1024)` with bge-large-zh embeddings.
- **SQL identifier safety**: dynamic identifiers go through `pq.QuoteIdentifier` (prior SQL-injection
  fixes). Don't string-concatenate table/column names into queries.

## Database

- Schema: `docs/schema/schema.sql` — **update it after any table change.** Table families are `ont_*`
  (ontology core, agent threads, audit) and `lakehouse_*` (keywords, metric intents, staging status).
- Migrations: `docs/migrations/` applied by `scripts/run-migrations.sh`. Run all:
  `make migrate-up`. Apply one file: `make migrate FILE=docs/migrations/<name>.sql`.
- DSN comes from `DATABASE_URL` in `.env.shared`; `pkg/dsnguard` validates it at startup.
- **Least-privilege roles** (optional, recommended for prod): `ops/db-roles.sql` defines a scoped
  Postgres role per service; verify grants with `scripts/check-runtime-grants.sh` (refuses to run
  against a live/pristine DB). `scripts/check-dsn-consistency.sh` confirms all services share one DB via
  the `db_target_hash` from `/healthz?check=db`.

## Frontend specifics

- Next.js 16 App Router, **static export only** (`output: 'export'`), base path `/lakehouse`. No SSR, no
  API routes — all data is client-side via `src/lib/api.ts` (`api<T>()` for JSON, `apiStream()` for SSE).
- Routes are under `src/app/[locale]/...` (i18n locale segment). Import alias `@/*` → `./src/*`.
- `useProject()` (project scoping — read `currentProject.id` on every data page) and `useAuth()` hooks.
- Industrial design system: no shadows, no rounded corners, no spring animations; colors via Tailwind v4
  theme tokens (`bg-ink`, `text-accent`, `bg-canvas-alt`, …). See `docs/design/`.
- No frontend test suite — verify by running `npm run dev` and exercising pages, or `npm run build`.

## Where to read more

`AGENTS.md` (this file's companion, per-directory `AGENTS.md` tree), `docs/architecture-lakehouse2ontology.md`
(4-layer + ADRs), `docs/spec/design-philosophy.en.md` (tool surfaces per agent mode),
`ops/cutover-parity-checklist.md` (before any ops/DB cutover work).
