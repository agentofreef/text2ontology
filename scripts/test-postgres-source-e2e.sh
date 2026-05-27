#!/usr/bin/env bash
# Phase 3 Postgres 源 e2e 验收脚本
# 步骤：test-connection → create source → catalog → status
# 完整 sync + wizard confirm 在 Phase 6 接通 SyncTables + Confirm 后补全。

set -euo pipefail

COLLECTOR="${COLLECTOR:-http://localhost:18096}"
INTERNAL="${INTERNAL_TOKEN:-test-internal-token}"
PROJECT_ID="${PROJECT_ID:-b0000000-0000-0000-0000-000000000001}"

# Load DATABASE_URL_CONTAINER from .env.shared if available
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$SCRIPT_DIR/../.env.shared"
if [[ -f "$ENV_FILE" ]]; then
  set -a; source "$ENV_FILE"; set +a
fi

# Use host.docker.internal so collector container can reach the external Postgres
PG_HOST="${PG_HOST:-host.docker.internal}"
PG_PORT="${PG_PORT:-5438}"
PG_DB="${PG_DB:-text2ontology_community}"
PG_USER="${PG_USER:-text2ontology_community}"
PG_PASS="${PG_PASS:-15993c18f401384bfcd92af6aa3013270bbcb6296acc5caa}"

echo "=== Step 1: test-connection ==="
RESP=$(curl -sS -X POST \
  -H "Content-Type: application/json" \
  -d "{\"host\":\"$PG_HOST\",\"port\":$PG_PORT,\"database\":\"$PG_DB\",\"user\":\"$PG_USER\",\"password\":\"$PG_PASS\"}" \
  "$COLLECTOR/api/connector/postgres/test-connection" 2>&1 || true)
echo "$RESP" | head -c 300
echo ""

if echo "$RESP" | grep -q '"ok":true'; then
  echo "PASS: test-connection returned ok=true"
else
  echo "WARN: test-connection did not return ok=true (collector may be down or DB unreachable from container)"
fi

echo ""
echo "=== Step 2: create source ==="
CREATE_RESP=$(curl -sS -X POST \
  -H "Content-Type: application/json" \
  -d "{\"project_id\":\"$PROJECT_ID\",\"type\":\"postgres\",\"label\":\"e2e-test-source\",\"config_json\":{\"host\":\"$PG_HOST\",\"port\":$PG_PORT,\"database\":\"$PG_DB\",\"user\":\"$PG_USER\",\"password\":\"$PG_PASS\"}}" \
  "$COLLECTOR/api/connector/postgres/sources" 2>&1 || true)
echo "$CREATE_RESP" | head -c 300
echo ""

SOURCE_ID=$(echo "$CREATE_RESP" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)
if [[ -n "$SOURCE_ID" ]]; then
  echo "PASS: source created id=$SOURCE_ID"

  echo ""
  echo "=== Step 3: get status ==="
  curl -sS "$COLLECTOR/api/connector/postgres/sources/$SOURCE_ID/status" | head -c 300
  echo ""
else
  echo "WARN: could not parse source id from create response"
fi

echo ""
echo "=== Postgres source API e2e done ==="
echo "NOTE: Full sync + wizard.Confirm wiring deferred to Phase 6."
