# Architecture: lakehouse2ontology (Postgres-only, 4-layer hexagonal)

> Branch: `lakehouse2ontology` (forked from `lakehouse-only` @ `ba1a365`)
> Last updated: 2026-04-19

## Why this document

After removing DuckDB and the Power BI live connector, the lakehouse subsystem
was refactored into 4 explicit layers with single-direction imports. This doc
summarises the architecture, the interfaces that define each layer boundary, and
the 3 ADRs that shaped the design.

For day-to-day guidance see `CLAUDE.md`. For the full design rationale and
risk register see `.omc/plans/lakehouse2ontology-consensus-plan-v2.md`.

---

## The 4 layers

```
┌─ OntologyAgent (ontology/handler_agent_lakehouse.go) ─────┐
│   HTTP handler; SSE streaming; depends on Engine       │
└──────────────────┬────────────────────────────────────────┘
                   │ uses smartquery.Engine
┌──────────────────▼────────────────────────────────────────┐
│ SmartqueryEngine (ontology/smartquery/)                    │
│   QuerySpec types + Postgres SQL generation + execution    │
└──────────────────┬────────────────────────────────────────┘
                   │ reads via CatalogReader
┌──────────────────▼────────────────────────────────────────┐
│ LakehouseStore (ontology/lakehouse/)                       │
│   Staging schema management + catalog metadata             │
└──────────────────┬────────────────────────────────────────┘
                   │ written by StagingWriter
┌──────────────────▼────────────────────────────────────────┐
│ IngestionPort (services/collector-server/ingest/)          │
│   PBIT (schema) + Excel/CSV (data) header-binding         │
│   NOTE: collector-server (:18096) is the sole data entry  │
│   point. Three source types: pbi / postgres / file        │
└────────────────────────────────────────────────────────────┘
```

Each layer except OntologyAgent has a `ports.go` file declaring its public
interface. Dependency direction is enforced by `scripts/check-layer-deps.sh`
(CI gate added in Stage 3B).

---

## Layer detail

### IngestionPort (`services/collector-server/ingest/`)

Accepts a typed `Source` (kind: pbit / excel / csv) and routes it through the
appropriate parser → header-binder → LakehouseStore's `StagingWriter`.

**Public interface:**

```go
type IngestionPort interface {
    Ingest(ctx context.Context, src Source, target StagingTarget) (IngestResult, error)
}

type StagingTarget interface {
    OpenStaging(ctx context.Context, schemaName string) (StagingWriter, error)
}

type StagingWriter interface {
    WriteRows(table string, columns []string, rows [][]any) error
    CommitSwap(ctx context.Context) error
    Rollback(ctx context.Context) error
    Close() error
}
```

Key types:
- `Source` — carries Kind, Filename, Reader (io.ReadSeeker), ProjectID, VersionID, BindingHint
- `IngestResult` — returns StagingSchema, TablesWritten, RowCounts, Warnings

**Only concrete implementation:** `ingest/pbitlakehouse/` — PBIT JSON schema
parsing (`model.bim`) + Excel/CSV data binding. PBIT provides the table and
column definitions; Excel/CSV provides the row data matched via header binding.

`StagingTarget` is declared in the `ingest` package (not `lakehouse`) so that
ingest does not import lakehouse — import is satisfied the other way around at
wire-up time in `main.go`.

---

### LakehouseStore (`services/lakehouse-sql-server/lakehouse/ports.go`)

Postgres staging schema management and read-only catalog access.

**Public interfaces:**

```go
type CatalogReader interface {
    ListTables(ctx context.Context, projectID, versionID string) ([]TableMeta, error)
    GetColumns(ctx context.Context, projectID, versionID, table string) ([]ColumnMeta, error)
    GetRelationships(ctx context.Context, projectID, versionID string) ([]RelationshipMeta, error)
}

type StagingWriter interface {
    CreateStaging(ctx context.Context, schema string, ddl []TableDDL) error
    WriteRows(table string, columns []string, rows [][]any) error
    CommitSwap(ctx context.Context) error  // atomic rename staging_* → live
    Rollback(ctx context.Context) error
    Close() error
}
```

**Concrete root:** `*Store` wraps `*sql.DB`, returned by `NewStore(db *sql.DB)`.
- `store.Reader()` returns `CatalogReader` (the store itself)
- `store.OpenStaging(ctx, schemaName)` returns `StagingWriter`

Stub method bodies exist after Stage 3a; Stage 3B wires them to the query
logic currently inlined in `handler_object.go` and `pbitlakehouse/`.

**AggCustomSQL note:** The constant `AggCustomSQL lakehouse.AggregateKind = 10`
lives in `lakehouse/types.go` as a downstream extension to the `AggregateKind`
enum whose canonical home is `smartquery`. This is the type-ownership inversion
described in ADR-003: `lakehouse` imports `smartquery` for the enum base type,
then extends it from outside. Any change to `smartquery.AggregateKind`'s
underlying type must coordinate with `lakehouse`.

---

### SmartqueryEngine (`services/lakehouse-sql-server/smartquery/ports.go`)

Translates the LLM-emitted `QuerySpec` into Postgres SQL and executes it.

**Public interface:**

```go
type Engine interface {
    Resolve(ctx context.Context, spec QuerySpec) (ResolvedQuery, error)
    GenerateSQL(rq ResolvedQuery) (string, error)
    ExecuteSQL(ctx context.Context, sql string) (Rows, error)
}

type Rows struct {
    Columns []string
    Data    [][]any
}
```

The interface is named `Engine` (not `Engine`) to avoid collision with the
existing concrete `*Engine` struct in `engine.go` that Stage 3B adapts to
satisfy it. `Engine` is the only seam mocked in agent unit tests.

**Canonical type home:** `smartquery` is the canonical declaration site for all
12 QuerySpec-family types:

```
QuerySpec, FilterItem, OrderByItem, PropertyInfo, KeywordCorrection,
ResolvedGroupBy, ResolvedFilter, ResolvedAggregate, ResolvedOrderBy,
MetricFilter, DerivedMetricDef, ResolveError
```

Plus `AggregateKind` (the enum). Previously these lived in `daxengine/` and
were re-exported via type aliases in `lakehouse/types.go`; after Stage 3a they
are promoted to canonical declarations here, and `lakehouse/types.go` imports
them directly.

**Metric Intent interaction:** Intent detection happens upstream in `recall/`
during the recall step. SmartqueryEngine just consumes the already-resolved
`QuerySpec` — it has no knowledge of Intents directly.

---

### OntologyAgent (`services/agent-server/handler/handler_agent_lakehouse.go`)

The terminal HTTP layer. Accepts a user question, invokes recall, injects
Metric Intent constraints, constructs a `QuerySpec`, passes it to
`SmartqueryEngine`, and streams results as SSE.

No public interface — handlers are functions, not a port. The constructor
pattern uses `AgentDeps`:

```go
type AgentDeps struct {
    DB     *sql.DB
    Engine smartquery.Engine
    LLM    llmclient.Client
    Recall *recall.Recaller
}

func RegisterAgentRoutes(mux *http.ServeMux, deps AgentDeps)
```

`AgentDeps` replaces the previous positional-argument forest. `Engine` is the
only field that may be mocked in unit tests.

---

## Dependency direction

```
ingest → (none of the upper layers)
lakehouse → smartquery (for QuerySpec family types)
smartquery → lakehouse (for CatalogReader in Resolve)
agent → smartquery + lakehouse + recall
```

The `ingest` → `lakehouse` direction is inverted via `StagingTarget`: ingest
declares the interface it needs; lakehouse's `*Store` satisfies it. This keeps
ingest's import set minimal and avoids a cycle.

Enforced by `scripts/check-layer-deps.sh`:

```bash
# ingest must NOT import smartquery or handler packages
# smartquery must NOT import ingest or handler packages
# lakehouse must NOT import smartquery, ingest, or handler packages
```

The script exits non-zero if any forbidden import edge is found; it runs in CI
on every PR.

---

## ADR summaries

### ADR-001: Postgres-only OLAP

- **Decision**: No embedded columnar engine (DuckDB / chDB / DataFusion /
  Polars / Trino). All OLAP execution goes through PostgreSQL.
- **Rationale**: Spec mandate (Postgres-only); operational simplicity — one DB
  to back up, one connection pool, one query language; data already lives in
  Postgres so no export/load cycle on each refresh.
- **Alternatives rejected**: DuckDB (status quo — removed by spec), chDB
  (CGo dep + separate dialect), DataFusion (Arrow marshalling overhead),
  Polars Go bindings (immature), Trino (JVM cluster — overkill).
- **Consequences**: Wide-pivot queries may be 2-5x slower than DuckDB on
  very large datasets. Acceptable per spec Non-Goals (performance optimisation
  is a separate follow-up item). Materialised views and covering indexes remain
  available options if pivot perf becomes a bottleneck.

### ADR-002: Heavy rename (DB name + Git dir + binary + module + base path)

- **Decision**: Rename in 8 physical locations: Git repository directory,
  Postgres DB name, Postgres role, Go module path (`lakehouse2ontology`),
  binary name (`lakehouse2ontology-server`), frontend `package.json` name,
  frontend basePath (`/lakehouse`), package path `daxengine/` →
  `smartquery/`.
- **Rationale**: Spec Round 3 selection ("重档"); half-renames produce
  permanent cognitive load ("why is the DB still called text2dax when the
  binary is lakehouse2ontology?"); identifier coherence across the stack.
- **Alternatives rejected**: Light rename (code only — spec rejected), medium
  rename (code + binary, keep DB + Git dir — spec rejected).
- **Consequences**: One-shot data migration required (Stage 4 `migrate.sh`).
  Local clones must `mv` their directory — cannot self-rename (documented in
  PR description). Old `/dax/*` URLs return 404; this is intentional — a
  clean break surfaces stale bookmarks.

### ADR-003: 4-layer hexagonal (vs flat handlers)

- **Decision**: Define explicit Go interfaces at each layer boundary
  (`ports.go` per package); enforce single-direction imports via CI script.
- **Rationale**: Spec Round 4 mandate ("真架构重构"); current code has
  implicit coupling (`lakehouse/types.go` aliases 12 types from `daxengine`,
  `duckdb` imports `handler` creating a cycle, recall → lakehouse → daxengine
  chain); stable interfaces allow swapping one layer (e.g. a different ingest
  source) without touching others.
- **Alternatives rejected**: Flat handler structure + dead-code removal (spec
  explicitly rejected this); 3 layers merging LakehouseStore into
  SmartqueryEngine (couples SQL execution to schema management); 5 layers
  splitting OntologyAgent (overkill for one terminal layer).
- **Consequences**:
  - Type ownership inverts: 12 `QuerySpec`-family types move from
    `daxengine` (now `smartquery`) to being canonical there, eliminating the
    alias indirection layer.
  - `AggCustomSQL` constant stays in `lakehouse` as a downstream extension
    to `smartquery.AggregateKind` — future changes to that enum must
    coordinate with `lakehouse`.
  - New `ports.go` files add ~150 LOC across 3 packages.
  - `Engine` is the single seam mocked in agent unit tests.
  - Dependency-direction lint is now a CI gate.

---

## Commits that shaped this architecture

| Commit | Stage | Description |
|--------|-------|-------------|
| `ba1a365` | baseline | Last commit on `lakehouse-only` before refactor |
| Stage 1 commit | 1/4 | Delete DuckDB + PowerBI live + dead `csv_datasource` |
| Stage 2 commit | 2/4 | Rename `text2dax` → `lakehouse2ontology`, `daxengine` → `smartquery`, `/dax` → `/lakehouse` |
| `9cfcf25` | 3a/4 | Introduce ports + promote QuerySpec aliases to canonical declarations |
| Stage 3B commit | 3b/4 | Wire handlers to Engine; add layer-dep CI script |
| Stage 3C commit | 3c/4 | CLAUDE.md rewrite + this doc |
| Stage 4 commit | 4/4 | DB migration script + historical question replay test |

---

## See also

- [CLAUDE.md](../CLAUDE.md) — day-to-day build, run, and code guidance
- [Spec](./../.omc/specs/deep-interview-lakehouse2ontology.md) — 5-round deep
  interview transcript + acceptance criteria
- [Plan](./../.omc/plans/lakehouse2ontology-consensus-plan-v2.md) — full
  4-stage consensus plan with risk register and pre-mortem
