# pkg/contracts/ — Shared DTOs (Phase 0 freeze)

## Purpose

This package contains Go DTO (Data Transfer Object) types that are mirrored
from `backend/ontology/*` for future use across services. It is a **shadow
mirror** created in Phase 0 of the arch-split plan (ADR Accepted 2026-04-23).

The monolith under `backend/` continues to use its own types unchanged.
`pkg/contracts/` exists so that new services extracted in later phases have a
stable, versioned type surface to import without pulling in the full monolith.

## Freeze Clause

**Breaking changes are prohibited during Phase 0–5 (the cutover window).**

Permitted: adding new optional fields (additive changes).
Prohibited: renaming fields, removing fields, changing JSON tags, changing
field types.

The JSON tags in this package must remain byte-identical to the corresponding
types in `backend/ontology/*`. The ledger types especially — their JSON is
written to `ont_agent_thread.thread_state->'ledger'` in Postgres and must
round-trip without any drift.

In Phase 2+, changes follow semver with a CI gate (see §3.5.5 of
`.omc/plans/arch-split-plan-final.md`).

## Files

| File | Contents | Source |
|---|---|---|
| `querySpec.go` | `QuerySpec`, `FilterItem`, `OrderByItem`, `DerivedMetricDef`, `MetricFilter`, `PropertyInfo`, `ResolvedFilter`, `ResolvedGroupBy`, `AggregateKind`, `ResolvedAggregate`, `OrderByKind`, `ResolvedOrderBy`, `KeywordCorrection`, `ResolveError` | `backend/ontology/smartquery/types.go` |
| `rows.go` | `LakehouseResult`, `LakehouseDebugInfo`, `JoinEdge` | `backend/ontology/lakehouse/types.go` |
| `recall.go` | `KeywordHit`, `PropertyMatch`, `OdBlock`, `OdLink`, `OkEntry`, `OlEntry`, `AmbiguityCandidate`, `Ambiguity`, `FilterSpec`, `MetricIntent`, `CachedContext`, `CachedToken`, `CachedPropRef`, `RecallResult` | `backend/ontology/recall/types.go` |
| `ledger.go` | `Ledger`, `LedgerOd`, `LedgerIntent`, `LedgerOk`, `LedgerOl`, `LedgerToken`, `LedgerPropRef`, `LedgerAmbigResolved`, `SchemaVersion` | `backend/ontology/ledger/types.go` |
| `CONTRACTS.md` | This document | N/A |

## Notes on Adaptation

- `LedgerOd`, `LedgerIntent`, `LedgerOk`, `LedgerOl` in `ledger.go` embed the
  corresponding types from `recall.go` (within this package) rather than
  importing `backend/ontology/recall`. This avoids cross-module import cycles
  while preserving the identical JSON serialization shape.
- Methods with business logic (e.g., `New()`, `IsEmpty()`, `EnsureMaps()`,
  `RebuildFromSteps()`) are not mirrored here. They will be re-implemented
  inside the target service package when that service is extracted.
- `IsCold()` on `CachedContext` is included because it is a pure predicate
  with no external dependencies.
- The `Rows` type does not exist as a named type in the backend; query results
  are returned as `ResultJSON string` inside `LakehouseResult`.

## Module

```
module contracts
go 1.25.0
```

Used via Go workspaces (`go.work` at repo root) alongside `./backend`.
