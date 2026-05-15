#!/usr/bin/env bash
# scripts/check-service-deps.sh
# Phase 1+ dependency gate: monolith stays HTTP-only for extracted services;
# services do not import each other's internals.
#
# Source: plan §3.4 (upgraded from check-layer-deps.sh).
# Activated D4d: lakehouse-sql-server extraction landed, so the former
# Phase 0 "pending" block is now a real enforcement.
#
# Run from repo root: bash scripts/check-service-deps.sh

set -euo pipefail
cd "$(dirname "$0")"/..
REPO_ROOT="$(pwd)"

FAIL=0

# ---------------------------------------------------------------------------
# Section 1+2: monolith intra-binary checks (legacy from Phase 0).
# Skip entirely if the monolith backend/ has been removed (post-split repos).
# ---------------------------------------------------------------------------

if [ -d "$REPO_ROOT/backend" ]; then
  pushd "$REPO_ROOT/backend" >/dev/null

  # ingest must NOT import ontology (anything)
  if go list -deps ./ingest/... 2>/dev/null | grep -E "^lakehouse2ontology/ontology"; then
    echo "FAIL: ingest imports ontology"
    FAIL=1
  fi

  # smartquery must NOT import ingest, must NOT import handler
  if go list -deps ./ontology/smartquery/... 2>/dev/null | grep -E "^lakehouse2ontology/(ingest|handler)"; then
    echo "FAIL: smartquery imports ingest or handler"
    FAIL=1
  fi

  # backend → services/* must be HTTP-only
  if go list -deps ./... 2>/dev/null | grep -E "^github\.com/lakehouse2ontology/services/"; then
    echo "FAIL: backend/ directly imports services/* — use HTTP client instead"
    FAIL=1
  fi

  popd >/dev/null
fi

# ---------------------------------------------------------------------------
# Section 3: services do not import other services.
# ---------------------------------------------------------------------------
# Each extracted service's internals belong only to that service. Cross-
# service calls must ride HTTP + contracts/DTOs (pkg/contracts). We iterate
# so new real services pick up the rule automatically.

for svc in lakehouse-sql-server agent-server recall-server mcp-tools-server backend-api collector-server; do
  svc_dir="$REPO_ROOT/services/$svc"
  [ -d "$svc_dir" ] || continue
  pushd "$svc_dir" >/dev/null
  # The service's own module is allowed; sibling services are not.
  bad=$(go list -deps ./... 2>/dev/null | grep -E "^github\.com/lakehouse2ontology/services/" | grep -v "^github\.com/lakehouse2ontology/services/$svc" || true)
  if [ -n "$bad" ]; then
    echo "FAIL: $svc imports sibling service packages:"
    echo "$bad" | sed 's/^/  /'
    FAIL=1
  fi
  popd >/dev/null
done

# ---------------------------------------------------------------------------
# Result
# ---------------------------------------------------------------------------

if [ $FAIL -eq 0 ]; then
  echo "OK: layer + service dependencies clean"
fi
exit $FAIL
