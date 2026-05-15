# ops/sli-slo.md
# SLI/SLO Definitions — lakehouse2ontology service split
#
# Source: plan §OQ-8 (Observability stack) + §4.2 (Expanded Test Plan)
# Status: BASELINE PENDING — thresholds marked TBD until 7-14 day OTel
#         baseline collection completes on the monolith (Phase 0, Week 1-2).
#
# Threshold derivation methodology:
#   p99 + 3σ from 7-14 day baseline on the current monolith.
#   Collected via OTel SDK wired into monolith in Phase 0 Week 1.
#   Derived values replace every "TBD after 7-14d baseline" cell below
#   at Phase 0 exit (T-14d milestone).
#
# Alert rule: fire P1 if metric exceeds threshold for > 5 consecutive minutes
# (except error_rate metrics: fire immediately on threshold breach in any 1min window).

## Metric definitions

| Metric | Purpose | Alert Threshold | Source / Span |
|--------|---------|----------------|---------------|
| `ledger_save_conflict_rate` | Rate of optimistic-concurrency conflicts on `ont_agent_thread.thread_state` ledger saves. Elevated rate signals hot thread contention or save retry storm. | TBD after 7-14d baseline (plan estimate: < 0.1% baseline → alert at > 0.5%) | agent-server / `ledger.SaveWithRetry` span |
| `ledger_save_duration_seconds` (p99) | Latency of a single ledger save (including retries). Baseline establishes expected cost of JSONB merge + optimistic CAS. | TBD after 7-14d baseline | agent-server / `ledger.Save` span |
| `recall_build_context_duration_seconds` (p99) | End-to-end latency of `recall.BuildLakehouseContextCached` per agent turn. Covers DB tier + ledger cache hit logic. Degraded mode (EXACT-only) must still meet this SLO. | TBD after 7-14d baseline | recall-server / `recall.BuildContext` span |
| `recall_embed_duration_seconds` (p99) | Latency of embedding model inference per request (interactive lane). Covers bge-large-zh model call. The two-lane priority router (OQ-4) keeps interactive p99 below this threshold even during concurrent batch embedding. | TBD after 7-14d baseline (plan estimate: ~80ms baseline → alert at > 200ms) | recall-server / `embed.Interactive` span |
| `smartquery_execute_duration_seconds` (p99) | Latency of Postgres SQL generation + execution for a single QuerySpec. Covers NormalizeQuerySpec + GenerateSQL + ExecuteSQL round-trip. | TBD after 7-14d baseline | lakehouse-sql-server / `smartquery.Execute` span |
| `mcp_tool_call_duration_seconds` (p99) | Latency of a single MCP tool call from external client to response. Covers mcp-tools-server → recall-server or lakehouse-sql-server fan-out. | TBD after 7-14d baseline | mcp-tools-server / `mcp.ToolCall` span |
| `mcp_tool_call_error_rate` | Rate of MCP tool calls returning error (5xx or tool-level error). Covers both HTTP transport errors and tool execution failures. | TBD after 7-14d baseline (plan estimate: ~0 baseline → alert at > 0.5%) | mcp-tools-server / `mcp.ToolCall` span |
| `cross_service_http_duration_seconds` (p99) | Latency of internal HTTP calls between services (agent-server → recall-server, agent-server → lakehouse-sql-server, backend-api → agent-server proxy). Isolates network + serialization overhead from business logic. | TBD after 7-14d baseline | all services / `http.client` spans on internal routes |
| `sse_stream_duration_seconds` (p99) | Time-to-first-chunk for SSE streams delivered to the frontend. Covers agent turn initiation → first SSE event written. MTU fragmentation (Scenario D) shows up here. | TBD after 7-14d baseline | agent-server / `sse.FirstChunk` span |
| `sse_stream_errors` | Count of SSE streams terminated with an error event before normal completion. Covers both infrastructure errors and agent-level panics. | TBD after 7-14d baseline (plan estimate: ~0 baseline → alert at > 0.5% of streams) | agent-server / `sse.Stream` span |
| `postgres_pool_in_use` | Current number of Postgres connections in use across all services. Sum across all 5 DB-connected services must stay < `max_connections - 20`. Baseline ceiling: 120 connections total. | > 120 (hard ceiling regardless of baseline; alert before hitting max_connections) | all services / `db.pool` gauge |

---

## Threshold derivation methodology

1. **Instrument**: Wire OTel SDK into the current monolith binary in Phase 0 Week 1. Export traces + metrics to Jaeger + Prometheus in the compose observability stack.
2. **Collect**: Run 7-14 days of production workload through the instrumented monolith. Capture all metrics listed above (where applicable on a monolith — cross-service spans collapse to in-process spans; that is acceptable as a lower bound).
3. **Derive**: For each metric, compute p99 over the collection window. Compute σ (standard deviation) over hourly p99 samples. Alert threshold = p99 + 3σ, rounded up to the nearest clean value.
4. **Pin**: At Phase 0 exit (T-14d), replace all "TBD after 7-14d baseline" cells with the derived values. Commit the update to this file. The updated thresholds become the gates for Phase 1 integration tests (`ops/sli-slo.md` alert thresholds ≤ 2x monolith baseline from ITER-4).
5. **Recompute**: After 30 days post-cutover, run the same derivation against the new 6-service stack's baseline. If traffic patterns diverged significantly (new MCP load, seasonality), update thresholds. Documented as ADR Follow-up item.

---

## Phase 0 exit gate (T-14d)

Before proceeding to Phase 1 (service extraction):
- [ ] All 11 metrics above have derived thresholds (no more TBD cells)
- [ ] Prometheus alert rules committed to `ops/alerts/` with the derived thresholds
- [ ] Grafana dashboard committed showing all 11 metrics with threshold lines
- [ ] `ops/sli-slo.md` updated with actual values and re-committed
