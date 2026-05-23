<p align="center">
  <img src="./docs/cover.svg" alt="text2ontology — Ontology before query" width="100%" />
</p>

# TEXT2ONTOLOGY

> **Ontology before query.** Build the meaning before you analyze.

[中文 README](./README.zh.md) · [Manifesto](./docs/manifesto/manifesto.en.md) · [Design Philosophy](./docs/spec/design-philosophy.en.md)

---

## What this is

text2ontology is my answer to what **natural-language → data analysis** should actually look like in practice. The design starts from a concrete engineer's question: **when the AI gets an answer wrong, where do I go to fix it?** Not *whose fault* — that's blame-shifting. *Where do I open the file, fix one thing, and stop seeing the same shape of error next week.* If the answer is "the LLM is stochastic, just retry," then this isn't a system I can ship — I can't be accountable for its error rate. This is the position I was forced into after watching the Text2SQL / Agentic-Analyst space miss the same problem repeatedly.

> **LLM-driven analysis should not rely on the LLM freely generating executable queries (SQL / DAX / Pandas / any DSL).** The LLM should fill *parameters* into an organization-maintained *intent template*; a deterministic compiler turns those into executable queries.

A structured map of *what your data means* — the ontology — is maintained separately from the data itself. The LLM picks an intent and supplies parameters; the engine compiles those to SQL (or whatever the data layer takes). Every error has an **address** — which Intent, which alias, which causality edge. Fix it once. Don't see the same shape next week.

If you've ever tried to put Text2SQL into production and watched it crumble on column-name aliasing, KPI ambiguity, or the question "is this answer correct?", this repo is what I think the real shape of the solution is.

Three core beliefs that drive the design:

1. **AI coding works because tests are the oracle. Data analysis has no oracle.** So we don't ask the AI to find the right answer — we let the organization specify one.
2. **What gets sold is consistency, not correctness.** "Correct" assumes a unique answer; business questions are under-determined. We deliver *the same answer to the same question, every time*.
3. **Bounded error is a different species from unbounded hallucination.** Every error in this system has an address (which Intent, which alias, which causality edge). Fix once, propagate forever.

Read the full thesis: [**Ontology Before Query** (manifesto)](./docs/manifesto/manifesto.en.md).

---

## Architecture

Six Go services plus a Next.js frontend, all behind an nginx gateway. **Only the gateway publishes a host port (`28080`)** — the frontend, the six services, Postgres, and the observability stack are reachable **only on the internal Docker network**. Eight built images (gateway + frontend + 6 Go services), plus Postgres and the observability stack. Ports in the diagram are each service's **internal** port (not mapped to the host):

```
                    ┌─────────────────────────────────┐
       browser ──▶  │     gateway   :28080            │   nginx — the only public ingress
                    │     (reverse-proxy, by path)    │   (everything below is internal-only)
                    └──────────────┬──────────────────┘
                                   │
       ┌──────────────┬────────────┼────────────┬──────────────┐
       ▼              ▼            ▼            ▼              ▼
  frontend       backend-api   agent-server  recall-server  collector-server
  :8080          :8090         :8092         :8093          :8096
  Next.js        Ontology CRUD AI Agent      3-tier recall  Data connectors:
                 Auth+projects (query/build) (EXACT/FUZZY   PBIT/Excel/CSV
                                              /VEC)          /SQLite/Postgres
                      │            │            │
                      ▼            ▼            ▼
              lakehouse-sql-server :8094     mcp-tools-server :8095
              SmartQuery engine              MCP gateway
              (ontology → SQL compiler)      (lookup_od / execute_smartquery)
                      │
                      ▼
                    ┌─────────────────────────────────┐
                    │     Postgres + pgvector         │   bundled in compose
                    │     (single source of truth)    │
                    └─────────────────────────────────┘
```

Per-service responsibility (internal ports; only the gateway is published):
- **gateway** `:28080` (public) — nginx; the only external ingress, reverse-proxies to frontend + services by path
- **frontend** `:8080` — Next.js static export served by nginx
- **backend-api** `:8090` — CRUD for `ont_*` / `lakehouse_*` tables, auth, projects, export/import
- **agent-server** `:8092` — Lakehouse Agent SSE (lakehouse / builder modes)
- **recall-server** `:8093` — Exact + vector + intent recall over `ont_*`
- **lakehouse-sql-server** `:8094` — SmartQuery engine (deterministic ontology → SQL compiler). The LLM picks `(OD, Intent, Keyword)` from finite sets; this engine stitches the JOINs deterministically via pre-defined `ont_link` relationships — **the LLM never sees a table or a JOIN**.
- **mcp-tools-server** `:8095` — MCP tool gateway for external clients (Claude Code, etc.)
- **collector-server** `:8096` — Sole data-ingest entrypoint (PBI/Postgres/File + wizard state machine)

Detailed architecture and design rationale: [**Design Philosophy**](./docs/spec/design-philosophy.en.md) ([中文](./docs/spec/design-philosophy.zh.md)).

---

## Quick Start

**Prerequisites:** Docker (with `docker compose` v2+).

Two commands. The single `docker-compose.yml` pulls prebuilt multi-arch images from GHCR and starts the whole stack — schema, least-privilege DB roles, and observability are all wired up automatically. Default login is `admin / admin` for local trial — harden before exposing beyond localhost (see Production below).

```bash
# 1. clone
git clone https://github.com/agentofreef/text2ontology
cd text2ontology

# 2. start everything (pulls images + applies schema, ~1-3 min first time)
docker compose up -d

# 3. verify (the gateway is the sole public ingress)
curl -fsS http://localhost:28080/healthz   # -> ok

# 4. open
open http://localhost:28080
```

Open `http://localhost:28080` and sign in as `admin` / `admin`. No `.env`, no flags — every secret has a safe dev default baked into `docker-compose.yml`.

**To use the Agent**: sign in as `admin` with the `ADMIN_PASSWORD` you set in `.env.shared`, then go to `/settings/llm-config` and add at least one chat model (Claude / OpenAI / DeepSeek / Qwen — vendor + base URL + API key + model name) and activate it for the chat role. Credentials live in the database — **no env changes, no container restart needed.**

**Status:** This setup brings the system up cleanly but **the UI is empty until you ingest data**. See `docs/` for ingestion guides (PBIT / Excel / CSV / Postgres mirror).

---

## Production deployment (hardened, single ingress)

There is ONE compose file — `docker-compose.yml` — and it is already the hardened, single-ingress topology. Only the nginx `gateway` publishes a host port (`28080`); Postgres, the six Go services, and the whole observability stack (otel-collector / Jaeger / Prometheus / Grafana / Alertmanager) are reachable **only on the internal Docker network** (the observability UIs bind to `127.0.0.1` for local inspection). Non-root containers, per-service CPU / memory / PID limits, HTTP timeouts + DB connection-pool caps, graceful shutdown, panic-recover middleware, least-privilege DB roles, and a one-shot `db-migrate` runner are all built in.

Quick Start runs this exact stack with safe **dev defaults**. Before exposing it beyond localhost, set strong secrets and turn on fail-closed enforcement:

### 1. Set strong secrets + enable enforcement

```bash
cp .env.example .env
```

`.env` is auto-read by docker compose. Set every secret to a strong value and flip enforcement on:

| Variable | Purpose |
|---|---|
| `POSTGRES_PASSWORD` | Postgres superuser + every scoped role password (use hex) |
| `ADMIN_PASSWORD` | initial `admin` web login |
| `AUTH_TOKEN_SECRET` | HMAC key for user session tokens (≥ 32 chars) |
| `INTERNAL_TOKEN` | service-to-service auth token |
| `GRAFANA_ADMIN_PASSWORD` | Grafana admin login |
| `REQUIRE_STRONG_SECRETS=true` | makes services **FAIL-CLOSED** on any weak/placeholder secret |

Generate strong values, e.g. `openssl rand -hex 32`. With `REQUIRE_STRONG_SECRETS=true`, any leftover `change_me` / `admin` makes the services refuse to start — so a misconfigured deploy fails loudly instead of running insecure.

### 2. Start and verify

```bash
docker compose up -d                                     # pulls prebuilt :latest images
curl -fsS http://localhost:28080/healthz                 # -> ok
curl -sI  http://localhost:28080/ | grep -i location     # -> 302 Location: /lakehouse/  (relative)
```

Open **http://localhost:28080**, sign in as `admin` with your `ADMIN_PASSWORD`, then add a chat model under `/settings/llm-config` (same as the Quick Start note).

### Operating notes

- **TLS** — the gateway serves plain HTTP on `28080`; terminate TLS at a reverse proxy in front (or add a TLS `server {}` to `services/gateway/nginx.conf`). HSTS is emitted but is a no-op over plain HTTP.
- **Restrict the public port** — edit the gateway `ports:` in `docker-compose.yml` (e.g. bind `127.0.0.1:28080:8080` to keep it off the LAN behind your own TLS proxy).
- **Build / publish images** — users pull prebuilt images; maintainers build locally with `make build` (per-service `docker build`). CI publishes multi-arch (amd64 + arm64) `:latest` to `ghcr.io/agentofreef/text2ontology-*` on every push to `main`.
- **Lifecycle**
  ```bash
  docker compose logs -f gateway
  docker compose down            # stop, keep the DB volume
  docker compose down -v         # stop and WIPE the DB volume
  git pull && docker compose pull && docker compose up -d   # update to latest images
  ```

---

## A note before you start

Most of what people sell as "AI agent + your database" runs on an unspoken assumption: that schema metadata plus an LLM is enough to answer business questions. I spent a long time trying to make that work — in different shapes, on different stacks — and watched it break the same way every time. So a few honest words before you spend your weekend on this.

Schema doesn't carry meaning. `INFORMATION_SCHEMA.COLUMNS` doesn't know that "early order" is `status='CONFIRMED'` in your company and `status IN ('CONFIRMED','SHIPPED')` in someone else's. It doesn't know the Q1 cut-off is the 14th, not the 15th. It doesn't know which customers got misclassified after the 2025 migration. Those things live in people's heads, in audit history, in exception lists nobody wrote down — and an LLM staring at columns alone can't recover them.

So the shape of this project is the inverse of the usual pitch: **the organization slowly accumulates a curated ontology, and the AI just reads it.** Not auto-learning. The closer analogy is onboarding a new analyst — one who, once you've explained something, doesn't forget.

### Questions worth sitting with first

These aren't requirements; the system will start up either way. They're just the places I've watched people (myself included) bump into the same wall when they were unclear up front:

1. What does your business actually do, and what do you want from AI analysis that you can't get today?
2. How clean is the data source? Half-migrated columns, broken FKs, things you've been meaning to fix for two quarters?
3. Are you ok with spending time writing the basics down — what "early order" means, which field defines "core customer", where the Q1 cut-off sits?
4. Has the knowledge in your team's heads — definitions, exception rules, calibration notes — been written somewhere shared?

If any of these feel fuzzy, that's usually the most useful place to start. Not because the system demands it, but because that's where the time goes.

### Once you're in

Clone, `docker compose up`, connect a data source. Open **builder mode** and walk the agent through your business in plain language. The ontology it produces is a *draft* — read it before activating anything. If you can't articulate to a colleague why a particular OD or Intent should exist, that's usually a sign the conversation upstairs isn't done yet.

Then ask a question. The first answer will probably be off. That's normal.

- The **keyword triage page** (`/lakehouse/ontology/lakehouse-keyword-triage`) is where you fix the tokenization — making sure the LLM sees the words the way your team uses them.
- The **metric intents page** (`/lakehouse/ontology/lakehouse-metric-intents`) is where you add a brand-new analytical dimension when none of the existing Intents cover it.

Compared with a traditional BI dashboard, this is heavier. You curate, annotate, activate. It doesn't fall out of the box in fifteen minutes.

What's worth it, in my experience, is that once an answer is fixed it stays fixed. Every error has an address — *which Intent, which alias, which causality edge* — and once you've fixed it there, the same shape of mistake doesn't come back next week. That's the part a traditional BI stack doesn't give you, and it's the part I built this for.

If this sounds like a fit for the shape of work you do, I'd be glad to have you use it. If you're hoping for a black-box magic answer, you might be happier with something else — and that's an honest call, not a put-down.

---

## Documentation

| Topic | English | 中文 |
|---|---|---|
| **Manifesto** — the thesis: why ontology-first | [EN](./docs/manifesto/manifesto.en.md) | [ZH](./docs/manifesto/manifesto.zh.md) |
| **Design Philosophy** — architecture in depth | [EN](./docs/spec/design-philosophy.en.md) | [ZH](./docs/spec/design-philosophy.zh.md) |
| **Responsibility as Moat** — commercial thesis | [EN](./docs/essays/responsibility-as-moat.en.md) | [ZH](./docs/essays/responsibility-as-moat.zh.md) |
| **AI Agentic Illusion** — critique of mainstream Agentic Data Analyst | [EN](./docs/essays/ai-agentic-illusion.en.md) | [ZH](./docs/essays/ai-agentic-illusion.zh.md) |
| **Business Ontology Engineer** — a new role that's emerging | [EN](./docs/essays/business-ontology-engineer.en.md) | [ZH](./docs/essays/business-ontology-engineer.zh.md) |

For internal development guides, see [`docs/`](./docs/).

---

## Two Agent Modes

text2ontology runs two independent agent modes inside a single endpoint, distinguished by `agent_type` on the agent thread (immutable once set):

| Mode | Purpose |
|---|---|
| **lakehouse** (query) | Natural language → SmartQuery → answer |
| **builder** (modeling) | Interview-driven OD / Intent / Link creation, human-activated |

See [`docs/spec/design-philosophy.en.md`](./docs/spec/design-philosophy.en.md) §4 for tool surfaces per mode.

---

## What I take from Palantir's ontology — and what I deliberately don't

I read [*The Palantir Impact*](./the-palantir-impact_en.md) (a CC-BY booklet posted on Hacker News — the [HN discussion is mostly skeptical](https://news.ycombinator.com/from?site=github.com/leading-ai-io)) and came out with two opposite reactions in one sitting. Worth pinning where this repo sits.

### My read of Palantir

The book's framing — "ontology = paradigm shift" — is half marketing. The HN thread pushes back and the strongest critiques are mostly right:

- *"It's just views, materialized views, UDFs, stored procedures in fancy corp speak."* — fair
- The concept of ontology isn't new. Aristotle did it. OWL / RDF did it more rigorously decades ago (transitive properties, decidability proofs, the whole DL family)
- Palantir's real moat is **operational integration** (Forward Deployed Engineers embedded on-site) and **DoD relationships**, not technical novelty
- The product, stripped of mystique, is "accessible UX over a graph store, plus a willingness to take government work no one else will touch"

But four design decisions are still genuinely worth borrowing:

1. **Semantics + Kinetics in one model.** Most data systems stop at "Object / Property / Link" — the noun half. Palantir insisted Action / Function / Dynamic-Security — the verb half — belongs in the same model. That's the right shape.
2. **Branch + Proposal for ontology changes.** Schema changes go through git-PR-style flow: branch, test, review, merge, with an Approvals app for multi-stakeholder sign-off.
3. **Action Log.** Every write becomes a permanent object automatically. Audit lives at the model layer, not the application layer.
4. **AI as a constrained executor.** The LLM can only invoke pre-defined Actions; hallucination is bounded *by design*. That's the same conclusion the [manifesto](./docs/manifesto/manifesto.en.md) reaches from a different lineage.

### Where this repo lands

| Capability | Palantir Foundry | text2ontology |
|---|---|---|
| Object / Property / Link | ✓ | ✓ |
| Knowledge / Causality / Learned-fact | partial (via Functions) | ✓ explicit (`ont_knowledge` / `ont_causality` / `ont_learned_fact`) |
| Metric / Intent templates | via Functions | ✓ `lakehouse_metric_intent` |
| **NL → ontology entry** (3-tier recall + thread Ledger) | inside AIP, opaque | ✓ open: EXACT / FUZZY / VEC + ledger |
| Mandatory explanation layer (vectorized) | implicit | ✓ enforced on every ontology unit |
| **Actions / write transactions (Kinetics)** | ✓ core | ✗ — system is read-only |
| Dynamic security (action-level / property-level RBAC) | ✓ | partial (`project_member` + role) |
| **Branch / Proposal flow for ontology changes** | ✓ Foundry Branching | partial — Builder mode's `mark=false → human activate` is the same shape |
| Action Log (every write becomes an audit object) | ✓ | ✗ |
| Real-time streaming indexing | ✓ Funnel streaming | ✗ — batch only via collector-server |
| AI agent proposing via branch | ✓ AI FDE | ✓ Builder mode does this for OD / Intent / Link |
| Digital twin / write-back to source systems | ✓ | ✗ |

### What I plan to add

1. **Branch / Proposal for ontology changes.** Builder and Analyst modes already carry the "propose → human activate" pattern; extend it so editing a `semantic_sql` or an `Intent` flows through the same review path instead of direct UPDATE.
2. **Action Log on `ont_*` / `lakehouse_*`.** Every INSERT / UPDATE / DELETE writes a row to a permanent audit table. Closes the gap noted in [Design Philosophy §6 "two future work items"](./docs/spec/design-philosophy.en.md).

### What I won't do, and why

1. **Actions / write-back (the Kinetics half).** I'm one person. A system that reorders factory parts and triggers insurance payouts is a categorically different investment — that's defense-contractor / FDE-on-site territory. This repo's thesis is *what NL → data analysis should look like* — read-side. Not "organizational OS."
2. **Real-time streaming indexing.** Batch via `collector-server` is sufficient for the answer-correctness problem. Streaming is an operational concern downstream of the ontology question.
3. **Digital twin.** Same reason — it crosses from "what the data means" into "what the business does."
4. **The Palantir prose voice.** *"Deep-rooted disease of silos"*, *"forged in extreme environments"*, *"eliminated to the absolute limit"* — every time I drift toward that register I rewrite the paragraph. The HN thread's reaction to that book is the reaction I want this README to *not* earn.

---

## Roadmap

Items inside each priority bucket are roughly ordered by what I'd reach for next. Won't-do items are deliberate, not "haven't gotten to" — they cross out of the project's scope.

### Shipped — current state (≈ v0.1)

| Area | What's in |
|---|---|
| Foundation | 6 Go services + Next.js frontend; 4-layer hexagonal architecture; Postgres + pgvector; single hardened docker compose (single ingress); 8 images on GHCR |
| Ontology core | 7 concepts (OD / Property / OK / OL / Link / Causality / Intent / Keyword); three-tier recall (EXACT / FUZZY / VEC); Thread Memory Ledger; SmartQuery engine — deterministic ontology → SQL; per-OD `semantic_sql` over many physical tables |
| Agent | Two modes (lakehouse / builder); ≥3-turn interview gate; `mark=false → human activate` lifecycle |
| Regression testing | Named test suites of agent-facing questions (`ont_test_suite` / `ont_test_case`); background runner executes suites against the live stack; run-over-run diff at `/ontology/lakehouse-agent/dataset-testing` |
| Security | HMAC-signed bearer tokens + iterated SHA-256 password hash; `project_member` access scoping; 7 SQL-injection sites fixed via `pq.QuoteIdentifier`; SQL passthrough cross-schema escape blocked; path-traversal fix on file upload; fail-closed CORS; LLM-key masking in detail GET |
| Docs | README EN / ZH; 5 essays EN / ZH (manifesto, design-philosophy, responsibility-as-moat, ai-agentic-illusion, business-ontology-engineer); Palantir comparison; "Don't expect magic" |

### Next — v0.2 candidates

| Priority | Item | Notes |
|---|---|---|
| 1 | **Ontology audit log** | Every INSERT / UPDATE / DELETE on `ont_*` / `lakehouse_*` writes to a permanent audit table — who, when, what, prior value. Closes the "Principle 7 half-implemented" gap called out in the manifesto |
| 1 | **Branch / Proposal flow for ontology edits** | Editing a `semantic_sql` or `Intent` goes through a branch → review → merge flow instead of direct `UPDATE` — borrowed from Palantir Foundry Branching |
| 2 | **OL → OK auto-sedimentation** | Cluster similar OLs (`confidence='pending'`); AI proposes a candidate OK; BOE reviews → accept writes to OK with evidence pointing back to source OLs. Documented as future work in design philosophy |
| 2 | **Pre-activate validators** | Light optional gate before `mark=true`: semantic_sql executes, grain has no duplicates, FK orphan rate < threshold. Reduced rebirth of the L3 validators that were removed with analyst mode |
| 3 | **Public benchmark suite on top of dataset-testing** | Ship a default suite + scoring rubric so anyone can run their own stack against a baseline and see regressions version-over-version |
| 3 | **English / multi-language token splitter** | Current `simple_split` is Chinese-biased; English needs its own splitter chain |

### Won't do — and why

| Item | Why |
|---|---|
| Streaming ingest | Batch ingest via `collector-server` is sufficient for the answer-correctness problem. Streaming is an operational concern downstream of the ontology question |
| Digital twin / write-back to source systems | Crosses from "what the data means" into "what the business does" — categorically a different system |
| SaaS hosting | Self-hosted by design |
| Mobile / desktop app | Frontend is web-only by design |

---

## Contributing

This repo is where I actually develop — no private mirror, no scheduled sync. Pull requests, issues, and discussions are read as I see them. See [CONTRIBUTING.md](./CONTRIBUTING.md) for the full flow.

For security issues, see [SECURITY.md](./SECURITY.md). Do not file public issues for vulnerabilities.

---

## License

Apache 2.0 — see [LICENSE](./LICENSE).

Documentation (manifesto, design philosophy, essays) is licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).

---

## Contact

- Email: <redeemer@vip.163.com>
- GitHub: [@agentofreef](https://github.com/agentofreef)
- Website: [text2ontology.com](https://text2ontology.com)
- RSS: [text2ontology.com/rss.xml](https://text2ontology.com/rss.xml) ([中文](https://text2ontology.com/zh/rss.xml))
- Comments on essays live on the site under each post (GitHub Discussions via Giscus). For bugs or feature requests, please open a GitHub issue. For security disclosures, see [SECURITY.md](./SECURITY.md).

---

## Credits

Created and maintained by [AgentOfReef](https://github.com/agentofreef). Read more at [text2ontology.com](https://text2ontology.com).
