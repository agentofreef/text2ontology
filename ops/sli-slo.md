# ops/sli-slo.md
# SLI/SLO Definitions — lakehouse2ontology service split
#
# Source: plan §OQ-8 (Observability stack) + §4.2 (Expanded Test Plan)
# Status: INITIAL TARGETS SET — thresholds below are seeded with defensible
#         engineering estimates for this dev/reference stack so alert rules
#         and dashboards can ship now. They will be re-pinned from a 7-14 day
#         OTel baseline (p99 + 3σ) once collection completes; the derivation
#         methodology section below remains the source of truth for that pass.
#
# Top-level service SLOs (apply to all 7 services unless overridden per-metric):
#   - Availability: 99.5% monthly (≈3h39m/30d error budget). Measured as
#     (1 - 5xx_rate) over /healthz + business endpoints. "Service down" =
#     no successful scrape / healthz for > 1 min.
#   - HTTP error-rate budget: < 1% 5xx over any rolling 5 min window
#     (interactive request error budget). Page on > 1%.
#   - p95 request latency targets (per service, interactive non-stream paths):
#       backend-api          p95 < 400ms   (CRUD / auth / projects)
#       agent-server         p95 < 800ms   (non-stream control endpoints)
#       recall-server        p95 < 300ms   (context build / vector search)
#       lakehouse-sql-server p95 < 1500ms  (LLM SQL gen + execute)
#       mcp-tools-server     p95 < 600ms   (tool dispatch fan-out)
#       collector-server     p95 < 2000ms  (ingest control plane, excl. job body)
#     (Streaming/SSE + long ingest jobs are governed by the duration metrics
#     in the table below, NOT these interactive p95 targets.)
#
# Alert rule: fire P1 if metric exceeds threshold for > 5 consecutive minutes
# (except error_rate metrics: fire immediately on threshold breach in any 1min window).

## Metric definitions

| Metric | Purpose | Alert Threshold | Source / Span |
|--------|---------|----------------|---------------|
| `ledger_save_conflict_rate` | Rate of optimistic-concurrency conflicts on `ont_agent_thread.thread_state` ledger saves. Elevated rate signals hot thread contention or save retry storm. | **> 0.5%** of save attempts over 5 min. *Justification:* clean single-writer baseline is < 0.1%; a sustained 5x rise indicates a retry storm / hot-thread contention worth paging on. | agent-server / `ledger.SaveWithRetry` span |
| `ledger_save_duration_seconds` (p99) | Latency of a single ledger save (including retries). Baseline establishes expected cost of JSONB merge + optimistic CAS. | **p99 > 750ms.** *Justification:* a single JSONB merge + CAS round-trip on a warm pool is tens of ms; 750ms p99 leaves headroom for one retry + GC pause before it signals real DB pressure. | agent-server / `ledger.Save` span |
| `recall_build_context_duration_seconds` (p99) | End-to-end latency of `recall.BuildLakehouseContextCached` per agent turn. Covers DB tier + ledger cache hit logic. Degraded mode (EXACT-only) must still meet this SLO. | **p99 > 2500ms.** *Justification:* cache-hit path is sub-100ms; cold-build crosses the DB tier + ledger logic, so 2.5s p99 catches a degraded/cold-cache regression without flapping on normal cold turns. | recall-server / `recall.BuildContext` span |
| `recall_embed_duration_seconds` (p99) | Latency of embedding model inference per request (interactive lane). Covers bge-large-zh model call. The two-lane priority router (OQ-4) keeps interactive p99 below this threshold even during concurrent batch embedding. | **p99 > 200ms** (interactive lane). *Justification:* bge-large-zh single-text inference baselines ~80ms; > 200ms means the priority router is failing to keep the interactive lane ahead of batch work. | recall-server / `embed.Interactive` span |
| `smartquery_execute_duration_seconds` (p99) | Latency of Postgres SQL generation + execution for a single QuerySpec. Covers NormalizeQuerySpec + GenerateSQL + ExecuteSQL round-trip. | **p99 > 8000ms.** *Justification:* dominated by one LLM SQL-generation call (multi-second) plus Postgres execution; 8s p99 flags an LLM stall or a runaway query while tolerating normal generation latency. | lakehouse-sql-server / `smartquery.Execute` span |
| `mcp_tool_call_duration_seconds` (p99) | Latency of a single MCP tool call from external client to response. Covers mcp-tools-server → recall-server or lakehouse-sql-server fan-out. | **p99 > 3000ms.** *Justification:* a tool call fans out to recall (≤300ms) or smartquery (multi-second); 3s p99 bounds the fan-out + one downstream LLM hop without paging on normal smartquery-backed tools. | mcp-tools-server / `mcp.ToolCall` span |
| `mcp_tool_call_error_rate` | Rate of MCP tool calls returning error (5xx or tool-level error). Covers both HTTP transport errors and tool execution failures. | **> 0.5%** over 5 min. *Justification:* external MCP clients should see near-zero errors; 0.5% sustained indicates a broken tool, expired key, or downstream outage. | mcp-tools-server / `mcp.ToolCall` span |
| `cross_service_http_duration_seconds` (p99) | Latency of internal HTTP calls between services (agent-server → recall-server, agent-server → lakehouse-sql-server, backend-api → agent-server proxy). Isolates network + serialization overhead from business logic. | **p99 > 500ms** (transport + serialization only, business logic excluded). *Justification:* same-host compose-bridge hops are sub-10ms; 500ms p99 isolates network/serialization stalls (e.g. connection-pool starvation) from slow handlers. | all services / `http.client` spans on internal routes |
| `sse_stream_duration_seconds` (p99) | Time-to-first-chunk for SSE streams delivered to the frontend. Covers agent turn initiation → first SSE event written. MTU fragmentation (Scenario D) shows up here. | **p99 time-to-first-chunk > 5000ms.** *Justification:* first token should land within a few seconds of turn start; > 5s TTFC is a perceptible stall (model cold-start, buffering misconfig, or MTU fragmentation per Scenario D). | agent-server / `sse.FirstChunk` span |
| `sse_stream_errors` | Count of SSE streams terminated with an error event before normal completion. Covers both infrastructure errors and agent-level panics. | **> 0.5%** of streams over 5 min. *Justification:* streams should complete cleanly; 0.5% error-termination rate indicates infra instability or agent panics worth paging on. | agent-server / `sse.Stream` span |
| `postgres_pool_in_use` | Current number of Postgres connections in use across all services. Sum across all 7 DB-connected services must stay < `max_connections - 20`. Baseline ceiling: 120 connections total. | **> 120** in-use (hard ceiling). *Justification:* Postgres `max_connections` defaults to ~140 here; alerting at 120 in-use leaves a 20-connection headroom (superuser/maintenance reserve) so we page before new connections start failing. Emitted by the activated `postgres_pool_*` collector (W6-1b). | all services / `db.pool` gauge (`postgres_pool_in_use`) |
| `ingest_job_duration_seconds` (p95) | Wall-clock of a connector ingest job body (PBI/PBIX, Postgres, File). Excludes the synchronous control-plane request (covered by collector-server p95 above). | **p95 > 300s** (5 min). *Justification:* dev-scale PBIX/CSV ingests complete in tens of seconds to a couple minutes; a job exceeding 5 min p95 indicates a stuck connector, oversized upload, or a hung downstream — matches the collector SSE/proxy 300s read timeout. | collector-server / `connector.IngestJob` span |

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
- [x] All metrics above have concrete thresholds (no more TBD cells; initial
      engineering targets set, to be re-pinned from the 7-14d baseline).
- [x] Prometheus alert rules committed to `ops/alerts/` (`lakehouse-alerts.yml`)
      covering error rate, p95/p99 latency, DB pool ceiling, service down, and
      otel-collector down. Loaded via `rule_files:` in `ops/prometheus.yml`.
- [x] Grafana dashboard committed (`ops/grafana/dashboards/lakehouse.json`)
      showing per-service request rate / error rate / latency + the DB pool gauge.
- [ ] Re-pin thresholds from the actual 7-14d baseline (p99 + 3σ) and re-commit
      this file once collection completes.
