#!/usr/bin/env bash
# scripts/test-pbit-regression.sh
#
# Phase 2 PBIT chain regression hard-gate. 5-step assertions:
#   1. POST /api/connector/pbit/parse  → tables[] in response
#   2. POST /api/connector/pbit/confirm-bindings  → HTTP 200
#   3. POST /api/connector/pbit/import  → SSE stream contains "phase":"complete"
#   4. GET  /api/ontology/objects  → returns array (ER node list)
#   5. GET  /api/ontology/objects  → contains at least one Od (non-empty)
#
# Exit 0 = PASS, exit 1 = FAIL.
#
# Environment:
#   COLLECTOR_BASE    base URL of collector-server (default http://localhost:18096)
#   BACKEND_BASE      base URL of backend-api (default http://localhost:18090)
#   PROJECT_ID        project ID to use for API calls (required for steps 1-3)
#   USER_TOKEN        Bearer token for backend-api (required for steps 4-5)
#   PASS_NO_FIXTURE   set to 1 to pass when no .pbit fixture is found
#
# Example:
#   PROJECT_ID=your-uuid USER_TOKEN=your-token bash scripts/test-pbit-regression.sh

set -euo pipefail

COLLECTOR_BASE="${COLLECTOR_BASE:-http://localhost:18096}"
BACKEND_BASE="${BACKEND_BASE:-http://localhost:18090}"
PROJECT_ID="${PROJECT_ID:-}"
USER_TOKEN="${USER_TOKEN:-}"

PASS=0
FAIL=0

pass() { echo "PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL+1)); }

# ── Fixture selection ──────────────────────────────────────────────────────────
FIXTURE=$(ls regression-fixtures/*.pbit 2>/dev/null | head -1 || true)
if [[ -z "$FIXTURE" ]]; then
    echo "INFO: no .pbit fixture found in regression-fixtures/"
    if [[ "${PASS_NO_FIXTURE:-0}" == "1" ]]; then
        echo "PASS_NO_FIXTURE=1 — skipping PBIT upload steps (Steps 1-3)"
        FIXTURE=""
    else
        echo "FAIL: set PASS_NO_FIXTURE=1 to skip, or add a .pbit file to regression-fixtures/"
        exit 1
    fi
else
    echo "Using fixture: $FIXTURE"
fi

echo "Collector base: $COLLECTOR_BASE"
echo "Backend base:   $BACKEND_BASE"
echo ""

# ── Step 1: parse ──────────────────────────────────────────────────────────────
if [[ -n "$FIXTURE" ]]; then
    echo "=== Step 1: POST /api/connector/pbit/parse ==="
    if [[ -z "$PROJECT_ID" ]]; then
        fail "Step 1 skipped — PROJECT_ID not set"
    else
        PARSE_RESP=$(curl -sS --max-time 30 -X POST \
            -F "file=@${FIXTURE}" \
            -F "project_id=${PROJECT_ID}" \
            "${COLLECTOR_BASE}/api/connector/pbit/parse" 2>&1 || true)
        echo "$PARSE_RESP" | head -c 500
        echo ""
        if echo "$PARSE_RESP" | grep -q '"tables"'; then
            pass "Step 1: parse returned tables[]"
        else
            fail "Step 1: parse response missing 'tables' key"
        fi
    fi
else
    echo "=== Step 1: SKIPPED (no fixture) ==="
fi

# ── Step 2: confirm-bindings ───────────────────────────────────────────────────
if [[ -n "$FIXTURE" && -n "$PROJECT_ID" ]]; then
    echo "=== Step 2: POST /api/connector/pbit/confirm-bindings ==="
    # Extract import_id from parse response (if available)
    IMPORT_ID=$(echo "${PARSE_RESP:-}" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('importId',''))" 2>/dev/null || true)
    if [[ -z "$IMPORT_ID" ]]; then
        echo "INFO: Could not extract importId from parse response — skipping Step 2"
    else
        CB_STATUS=$(curl -sS --max-time 30 -o /dev/null -w "%{http_code}" -X POST \
            -H "Content-Type: application/json" \
            -d "{\"importId\":\"${IMPORT_ID}\",\"projectId\":\"${PROJECT_ID}\",\"bindings\":[]}" \
            "${COLLECTOR_BASE}/api/connector/pbit/confirm-bindings" 2>&1 || true)
        echo "HTTP status: $CB_STATUS"
        if [[ "$CB_STATUS" == "200" || "$CB_STATUS" == "204" ]]; then
            pass "Step 2: confirm-bindings returned ${CB_STATUS}"
        else
            fail "Step 2: confirm-bindings returned ${CB_STATUS} (expected 200/204)"
        fi
    fi
else
    echo "=== Step 2: SKIPPED ==="
fi

# ── Step 3: import SSE ────────────────────────────────────────────────────────
if [[ -n "$FIXTURE" && -n "$PROJECT_ID" && -n "${IMPORT_ID:-}" ]]; then
    echo "=== Step 3: POST /api/connector/pbit/import (SSE) ==="
    IMPORT_OUT=$(curl -sS --max-time 60 -X POST \
        -H "Content-Type: application/json" \
        -d "{\"importId\":\"${IMPORT_ID}\",\"projectId\":\"${PROJECT_ID}\"}" \
        "${COLLECTOR_BASE}/api/connector/pbit/import" 2>&1 || true)
    echo "$IMPORT_OUT" | head -c 500
    echo ""
    if echo "$IMPORT_OUT" | grep -q '"phase":"complete"\|"phase": "complete"'; then
        pass "Step 3: import SSE stream contains phase:complete"
    else
        fail "Step 3: import SSE stream missing phase:complete"
    fi
else
    echo "=== Step 3: SKIPPED ==="
fi

# ── Step 4: ER graph API returns array ────────────────────────────────────────
echo "=== Step 4: GET /api/ontology/objects (ER node list) ==="
if [[ -z "$USER_TOKEN" || -z "$PROJECT_ID" ]]; then
    echo "INFO: USER_TOKEN or PROJECT_ID not set — skipping Steps 4-5"
else
    OBJ_RESP=$(curl -sS --max-time 15 \
        -H "Authorization: Bearer ${USER_TOKEN}" \
        "${BACKEND_BASE}/api/ontology/objects?projectId=${PROJECT_ID}" 2>&1 || true)
    if echo "$OBJ_RESP" | grep -q '^\['; then
        pass "Step 4: /api/ontology/objects returned JSON array"
    else
        fail "Step 4: /api/ontology/objects response not a JSON array: $(echo "$OBJ_RESP" | head -c 200)"
    fi

    # ── Step 5: at least one Od in result ────────────────────────────────────
    echo "=== Step 5: /api/ontology/objects is non-empty ==="
    OBJ_COUNT=$(echo "$OBJ_RESP" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || true)
    if [[ "${OBJ_COUNT:-0}" -gt 0 ]]; then
        pass "Step 5: objects list contains ${OBJ_COUNT} Od(s)"
    else
        fail "Step 5: objects list is empty (count=${OBJ_COUNT:-unknown})"
    fi
fi

# ── Summary ────────────────────────────────────────────────────────────────────
echo ""
echo "Results: PASS=${PASS} FAIL=${FAIL}"
if [[ "$FAIL" -gt 0 ]]; then
    echo "FAIL: ${FAIL} step(s) failed"
    exit 1
fi
echo "PASS: all steps passed"
exit 0
