#!/usr/bin/env bash
# scripts/check-layer-deps.sh
# ADR-003 4-layer hexagonal import-direction gate.
#
# Source of truth: docs/architecture-lakehouse2ontology.md
#   "## The 4 layers" + "## Dependency direction" + "### ADR-003".
#
# The four layers (bottom -> top) and their physical package locations in the
# post-split monorepo:
#
#   IngestionPort   services/*/ingest/      (collector-server)
#   LakehouseStore  services/*/lakehouse/   (lakehouse-sql-server, agent-server)
#   SmartqueryEngine services/*/smartquery/ (lakehouse-sql-server, agent-server)
#   OntologyAgent   services/*/handler/     (agent-server, backend-api)  [top]
#
# Allowed dependency edges (from the doc's "Dependency direction" section):
#
#   ingest     -> (none of the upper layers)
#   lakehouse  -> smartquery        (QuerySpec family enum base types — ADR-003)
#   smartquery -> lakehouse         (CatalogReader in Resolve)
#   agent      -> smartquery + lakehouse + recall
#
# The lakehouse <-> smartquery pair is the one intentional mutual edge; the
# cycle is broken at runtime via the StagingTarget / CatalogReader interface
# inversion, not by an import restriction. It is therefore NOT a violation.
#
# Forbidden import edges enforced here (a non-zero exit on any match):
#
#   ingest     must NOT import smartquery, lakehouse, or handler
#   smartquery must NOT import ingest or handler
#   lakehouse  must NOT import ingest or handler
#
# (handler == the OntologyAgent terminal layer; nothing below it may depend
# upward on it.)
#
# Run from repo root: bash scripts/check-layer-deps.sh

set -euo pipefail
cd "$(dirname "$0")"/..
REPO_ROOT="$(pwd)"

FAIL=0

# layer_exists <module-dir> <package-glob>
# True (0) if the module contains at least one matching layer package on disk.
# Uses the filesystem rather than `go list` so a package that fails to compile
# (e.g. because it carries a forbidden import) is still recognised as present —
# otherwise a cycle-inducing violation would make the layer "vanish" and slip
# past the gate.
layer_exists() {
  local mod_dir="$1" pkg_glob="$2"
  # pkg_glob is "./<layer>/..."; map it to the on-disk directory.
  local sub="${pkg_glob#./}"; sub="${sub%/...}"
  [ -d "$mod_dir/$sub" ]
}

# check_forbidden <layer-name> <module-dir> <package-glob> <forbidden-regex>
# Fails the gate if any dependency of the layer matches the forbidden regex.
#
# Critically: a forbidden upward import frequently induces an import cycle
# (handler -> smartquery -> handler), which makes `go list -deps` exit non-zero
# and print nothing on stdout. We therefore inspect the exit status: if the
# layer package exists on disk but `go list -deps` fails, that is itself a
# FAIL (do not swallow the error and report "clean").
check_forbidden() {
  local layer="$1" mod_dir="$2" pkg_glob="$3" forbidden="$4"
  # Skip silently if this module has no such layer package.
  layer_exists "$mod_dir" "$pkg_glob" || return 0

  local deps rc
  # `set -e` would abort the script on a failed command substitution, so run
  # the (potentially failing) go list with errexit temporarily disabled and
  # capture its exit status explicitly.
  set +e
  deps=$( cd "$mod_dir" && go list -deps "$pkg_glob" 2>/dev/null )
  rc=$?
  set -e
  if [ $rc -ne 0 ]; then
    # The package exists but won't resolve — almost always a forbidden
    # upward import creating a cycle. Surface the real go-list diagnostic.
    echo "FAIL: ${layer} (${mod_dir#"$REPO_ROOT"/}) does not resolve — likely a forbidden import / cycle:"
    ( cd "$mod_dir" && go list -deps "$pkg_glob" 2>&1 | sed 's/^/  /' )
    FAIL=1
    return 0
  fi

  local bad
  bad=$(printf '%s\n' "$deps" | sort -u | grep -E "$forbidden" || true)
  if [ -n "$bad" ]; then
    echo "FAIL: ${layer} (${mod_dir#"$REPO_ROOT"/}) imports forbidden upper-layer package(s):"
    echo "$bad" | sed 's/^/  /'
    FAIL=1
  fi
}

# Forbidden-target regexes. Matched against full import paths emitted by
# `go list -deps`, e.g. github.com/lakehouse2ontology/services/<svc>/<layer>.
RE_SMARTQUERY='^github\.com/lakehouse2ontology/services/[^/]+/smartquery(/|$)'
RE_LAKEHOUSE='^github\.com/lakehouse2ontology/services/[^/]+/lakehouse(/|$)'
RE_INGEST='^github\.com/lakehouse2ontology/services/[^/]+/ingest(/|$)'
RE_HANDLER='^github\.com/lakehouse2ontology/services/[^/]+/handler(/|$)'

# Modules that may contain layer packages.
LAYER_MODULES="services/collector-server services/lakehouse-sql-server services/agent-server"

for mod in $LAYER_MODULES; do
  mod_dir="$REPO_ROOT/$mod"
  [ -d "$mod_dir" ] || continue

  # ingest must NOT import smartquery, lakehouse, or handler.
  check_forbidden "ingest" "$mod_dir" "./ingest/..." \
    "${RE_SMARTQUERY}|${RE_LAKEHOUSE}|${RE_HANDLER}"

  # smartquery must NOT import ingest or handler. (lakehouse is allowed.)
  check_forbidden "smartquery" "$mod_dir" "./smartquery/..." \
    "${RE_INGEST}|${RE_HANDLER}"

  # lakehouse must NOT import ingest or handler. (smartquery is allowed.)
  check_forbidden "lakehouse" "$mod_dir" "./lakehouse/..." \
    "${RE_INGEST}|${RE_HANDLER}"
done

if [ $FAIL -eq 0 ]; then
  echo "OK: ADR-003 layer import directions clean"
fi
exit $FAIL
