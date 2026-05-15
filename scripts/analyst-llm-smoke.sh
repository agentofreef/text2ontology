#!/usr/bin/env bash
# scripts/analyst-llm-smoke.sh — Phase 2B + 2C + 2D + 3 acceptance gate.
#
# Verifies the 33-tool analyst surface compiles cleanly to vendor-specific JSON
# shapes without truncation or schema-validation errors. Smoke covers:
#   1. claude-sonnet-4-6 (Anthropic format)
#   2. gpt-4-turbo / openai-compatible (OpenAI format)
#   3. deepseek-chat (OpenAI-compatible-format; flagged as the highest-risk
#      vendor for tool-count blowout per plan R8)
#
# Strategy: avoid live API calls (which require DATABASE_URL + role bindings
# + working LLM credentials). Instead, drive the production tool-payload
# builders inside pkg/llmclient — same code path, no network. If a vendor's
# format function rejects the 33-tool surface for any reason (truncation,
# illegal char, missing required field) the smoke catches it pre-network.
#
# Optional live mode: pass --live to run the Go smoke under DATABASE_URL +
# real provider credentials. Skipped by default; CI runs the offline path.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT/services/agent-server"

LIVE=0
for arg in "$@"; do
  case "$arg" in
    --live) LIVE=1 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

echo "── analyst-llm-smoke ──"
echo "  ROOT      = $ROOT"
echo "  LIVE      = $LIVE"
echo "  toolCount = 33 (8 reused builder + 25 analyst — 18 Phase 2B+2C + 7 Phase 3; propose_* removed in 2D)"
echo

# ── Phase 1: offline shape validation ────────────────────────────────────────
# Runs the Go test that resolves analystV1Tools(), serializes each ToolDef
# through every supported vendor format, and asserts shape integrity.
echo "[1/2] offline shape smoke (no network)…"
go test ./handler/ -run "TestAnalystLLMSmoke_OfflineShapes" -timeout 60s -v
echo

# ── Phase 2: live API call (opt-in) ─────────────────────────────────────────
if [[ "$LIVE" -eq 1 ]]; then
  if [[ -z "${DATABASE_URL:-}" ]]; then
    echo "ERROR: --live requires DATABASE_URL" >&2
    exit 3
  fi
  echo "[2/2] live API smoke (one tool-call per provider)…"
  go test ./handler/ -run "TestAnalystLLMSmoke_LiveCall" -timeout 180s -v
else
  echo "[2/2] live API smoke skipped (pass --live to enable)."
fi

echo
echo "✓ analyst-llm-smoke OK"
