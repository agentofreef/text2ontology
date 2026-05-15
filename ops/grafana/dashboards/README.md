# Grafana Dashboards — Placeholder

Real dashboards will be committed here once Phase 0 baseline metric collection
starts on the monolith. Planned dashboards per plan §OQ-8:

- **Cutover Health** — request rate, error rate, p99 latency per service during
  traffic migration from monolith to split services.
- **Ledger** — thread memory ledger hit/miss rate, ledger size distribution,
  per-turn Od/Intent merge counts.
- **MCP** — MCP tool call volume, latency, error rate per tool type.
- **Cross-service** — end-to-end trace fan-out: agent-server → recall-server →
  lakehouse-sql-server round-trip latency.

## How to add a dashboard

1. Export the dashboard JSON from Grafana UI (Share → Export → Save to file).
2. Drop the `.json` file in this directory.
3. Add a volume mount entry to the `grafana` service in `docker-compose.yml`:
   ```yaml
   volumes:
     - ./ops/grafana/dashboards:/var/lib/grafana/dashboards
   ```
4. Add a dashboard provider in `ops/grafana/provisioning/dashboards.yaml`
   pointing to `/var/lib/grafana/dashboards`.
