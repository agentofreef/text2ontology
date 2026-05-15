# ops/cutover-parity-checklist.md
# Cutover Parity Checklist — Scenario D (Staging-Prod Environmental Drift)
#
# Source: plan §4.1 Scenario D (ITER-5)
# Committed: T-7d before cutover. Executed T-7d through T-3d against prod
# DB with read-only creds. If ANY check fails → abort cutover, reschedule.

## Execution window

- **Start**: T-7d (7 days before cutover night)
- **Deadline**: T-3d (must be fully green by T-3d)
- **Executor**: on-call operator with read-only prod DB creds + network access to future compose hosts

---

## Check 0: `lakehouse2ontology-enterprise` clone exists and is current

**Risk**: running ops/db-roles.sql, rollback-schema-unsplit.sql, or any service writes against the pristine live `lakehouse2ontology` DB corrupts the Big Bang rollback fallback. Per user directive 2026-04-23 (`feedback_db_isolation.md`), all enterprise-rebuild work targets `lakehouse2ontology-enterprise` clone exclusively.

```bash
# Verify the clone exists.
psql $PROD_SUPERUSER_URL -tAc \
  "SELECT 1 FROM pg_database WHERE datname='lakehouse2ontology-enterprise';"
# Expected output: 1

# Verify recency: clone must be ≤ 7 days old AND ingest row counts match source
# within 0.5% (acceptable drift window during concurrent prod writes).
psql $PROD_SUPERUSER_URL -tAc \
  "SELECT (now() - datctime)::interval FROM pg_database WHERE datname='lakehouse2ontology-enterprise';"
# Expected: < 7 days. If stale → re-clone before proceeding:
#   DROP DATABASE "lakehouse2ontology-enterprise";
#   CREATE DATABASE "lakehouse2ontology-enterprise" TEMPLATE lakehouse2ontology;
# (requires no active connections to source DB during clone).

# Verify DATABASE_URL targets the clone, NOT live.
echo "$DATABASE_URL" | grep -q '/lakehouse2ontology-enterprise?\|/lakehouse2ontology-enterprise$' \
  || { echo "ABORT: DATABASE_URL does not target -enterprise clone"; exit 2; }
```

**Pass**: clone present, age < 7d, all service DSNs target `-enterprise`.
**Fail**: any check fails → abort cutover. Fix: re-clone and re-point DSNs before rescheduling.

**This is a hard precondition for ALL subsequent checks.** Do not proceed to Check 1 if Check 0 fails.

---

## Check 1: Postgres major version parity

**Risk**: staging 15.x vs prod 14.x — SQL behavior differences, extension API changes.

```bash
# Run on BOTH staging DB and prod DB; major version must match.
psql $STAGING_DATABASE_URL -tAc "SELECT version();"
psql $PROD_DATABASE_URL    -tAc "SELECT version();"
```

**Pass**: major version (first number) is identical. Minor version mismatch is acceptable.
**Fail**: major version differs → abort; align prod to match staging (or vice versa) before rescheduling.

---

## Check 2: max_connections ceiling

**Risk**: sum of per-service pool ceilings (120) + reserved (20) = 140 required; prod may cap at 100.

```bash
psql $PROD_DATABASE_URL -tAc "SHOW max_connections;"
```

**Pass**: value >= 140.
**Fail**: value < 140 → abort; increase `max_connections` in prod Postgres config + reload before rescheduling.

---

## Check 3: pgvector extension version parity

**Risk**: staging installed pgvector 0.6.x, prod has 0.5.x — index type or operator differences.

```bash
psql $STAGING_DATABASE_URL -tAc "SELECT extname, extversion FROM pg_extension WHERE extname='vector';"
psql $PROD_DATABASE_URL    -tAc "SELECT extname, extversion FROM pg_extension WHERE extname='vector';"
```

**Pass**: `extversion` matches between staging and prod.
**Fail**: version mismatch → align prod extension version before rescheduling.

---

## Check 4: TLS certificate chain (prod DB TLS)

**Risk**: staging uses self-signed cert; prod uses Let's Encrypt with stricter chain validation — Go `lib/pq` may reject.

```bash
# Replace <prod-db-host> with the actual prod DB hostname.
openssl s_client -connect <prod-db-host>:5432 -starttls postgres 2>&1 | grep -E "Verify|depth|error"
```

**Pass**: output shows `Verify return code: 0 (ok)` — no cert errors.
**Fail**: any verification error → fix cert chain or configure `sslmode=verify-ca` with correct root CA before rescheduling.

---

## Check 5: Network MTU (future compose host → prod DB)

**Risk**: prod network behind proxy with MTU 1400; staging Docker bridge at 1500. Large Od context blocks fragment → SSE first-chunk latency spikes > 2s.

```bash
# Run from the future compose host (NOT from local dev machine).
# -M do = DF bit set (Don't Fragment). -s 1472 = max standard MTU payload.
ping -M do -s 1472 <prod-db-host>
```

**Pass**: ping succeeds (ICMP reply received, no fragmentation needed error).
**Fail**: "Frag needed" or timeout → reduce compose host MTU or prod network MTU to match; recheck before rescheduling.

---

## Check 6: Env var hash parity (.env.shared)

**Risk**: staging `.env.shared` and prod `.env.shared` diverge in non-obvious ways (trailing newlines, different key ordering, typos) — auth token mismatch, wrong DB, silent misconfiguration.

```bash
sha256sum staging/.env.shared
sha256sum prod/.env.shared
# If hashes differ, enumerate the actual differences:
diff <(sort staging/.env.shared) <(sort prod/.env.shared)
```

**Pass**: differences are ONLY expected (different passwords, different hostnames) — no structural key differences.
**Fail**: unexpected keys missing or extra → fix prod `.env.shared` to match staging structure before rescheduling.

---

## Check 7: Disk space on prod DB server

**Risk**: schema split + pg_dump backup + rollback room exhausts disk mid-cutover.

```bash
# Run on the prod DB server host.
df -h /var/lib/postgresql
# Also check current DB size:
psql $PROD_DATABASE_URL -tAc "SELECT pg_size_pretty(pg_database_size(current_database()));"
```

**Pass**: free space >= 2x current DB size.
**Fail**: insufficient free space → expand disk / purge old backups before rescheduling.

---

## Check 8: NTP time sync (compose host vs prod DB)

**Risk**: clock drift > 1s between compose host and prod DB corrupts ledger timestamps and audit log ordering.

```bash
# On the future compose host:
chronyc tracking | grep "System time"
# Or:
timedatectl show --property=NTPSynchronized,TimeUSec
# Compare with prod DB host time:
psql $PROD_DATABASE_URL -tAc "SELECT now();"
```

**Pass**: drift < 1 second between compose host clock and prod DB `now()`.
**Fail**: drift >= 1s → fix NTP sync on compose host before rescheduling.

---

## Check 9: CDN / proxy SSE passthrough

**Risk**: if a CDN or proxy sits in front of the SSE path, it may buffer SSE chunks — breaking streaming. Per OQ-5, frontend direct-dials agent-server (port 8092), so this should not apply. Verify it doesn't apply.

```bash
# Confirm agent-server SSE endpoint is NOT behind a CDN/proxy in prod.
# Check: does the prod load balancer / CDN config route :8092 directly?
# Manual verification — no automated command. Document the network topology.
curl -I http://<prod-agent-server-host>:8092/api/ontology/agent-lakehouse-stream 2>&1 | head -20
```

**Pass**: no CDN/proxy buffering on the agent-server SSE port; `X-Accel-Buffering: no` passes through if nginx is in the path.
**Fail**: CDN buffering detected → configure CDN bypass for SSE path before rescheduling.

---

## T-minus timing summary

| Milestone | Action |
|-----------|--------|
| T-7d | Commit this checklist; begin executing all 9 checks against prod |
| T-7d to T-3d | All checks must turn green; any red = investigate + fix |
| T-3d | Checklist fully green = gate to proceed with Rehearsal #2 completion |
| T-1d | Re-run checks 1, 2, 6 (fast; catch last-minute changes) |
| T-0 | If any check newly fails at cutover: abort immediately (see below) |

---

## Abort and reschedule containment plan

If any check fails during T-7d to T-3d:
1. **Stop**: do NOT compress the timeline. Do not attempt a "fix it during the cutover window" approach.
2. **Root cause**: document the failing check result in the incident log.
3. **Fix**: resolve the environmental gap (Postgres upgrade, MTU fix, env var correction, etc.) in isolation.
4. **Re-verify**: re-run the specific failing check + its neighbors to confirm no side effects.
5. **Reschedule**: next natural off-peak cutover window (typically +2 weeks per plan Δ-4).

If discovered at T-0 during the cutover window:
1. Rollback immediately per `ops/rollback-schema-unsplit.sql`.
2. Start monolith: `systemctl start lakehouse2ontology-monolith`.
3. Exit maintenance mode.
4. Investigate which parity check was missed; update this checklist.
5. Reschedule with the gap fixed.

**Target rollback wall time**: < 30 minutes from failure detection to monolith serving traffic.
