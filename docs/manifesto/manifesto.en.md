# Ontology Before Query

> How to rebuild determinism in a domain where LLMs cannot judge correctness

---

AI is already writing code. Cursor, Copilot, and Devin let one engineer do the work of ten. But in data analysis — **a domain ten times larger than coding** — the same LLM technology has failed to break into enterprise mainstream over the past three years.

Not because the models aren't strong enough. Not because the prompts haven't been tuned. **The paradigm is wrong.**

This manifesto argues: **letting LLMs write SQL directly from natural language is structurally infeasible at enterprise scale**. The way out is to invert the order — first use AI to crystallize "what is this business actually analyzing" into an **ontology**, then let queries grow from the ontology.

---

## I. The Oracle Problem

Software engineering has a classical concept called the **test oracle** — a black box that tells you what the correct answer is. AI coding can converge precisely because **the test suite is the oracle**: model produces code, runs tests, passes or fails, iterates.

**In data analysis, there is no such oracle.**

You ask: "What's our early order rate?" The LLM answers: "12.3%." You have **no automated mechanism** to verify that 12.3% is correct. Whether "early order" means `status='CONFIRMED'` or `status IN ('CONFIRMED', 'SHIPPED')` depends on consensus living in human heads — it does not exist in any code or data.

No oracle = no iterative convergence = forever a demo, never a product.

This is not a model capability problem. It is a paradigm problem.

---

## II. Three Failures That Compound

Mainstream Text2SQL / "AI chat with your data" fails in enterprise contexts in three distinct ways. **They share one root cause.**

### Failure 1 (theoretical): No oracle, no LLM convergence

As above. This is a **mathematical** limit, not an engineering problem. No bigger model rescues it.

### Failure 2 (practical): Semantic metadata is a dealbreaker for mid-market

Snowflake Cortex, Databricks Genie, Cube, dbt + LookML — all require enterprises to **pre-build a semantic layer**: business glossaries, schema descriptions, metric definitions, entity relationships.

Building this metadata requires a dedicated data engineering team for months. An acceptable cost for the Fortune 500, **a dealbreaker for 80% of mid-market companies** — they have neither the team nor the budget, and no comparable case study to justify the investment.

### Failure 3 (practical): 90% accuracy is not enterprise-grade accuracy

Every Text2SQL company's marketing claims "90%+ accuracy."

**90% means 1 in 10 answers is wrong.**

When a CFO says "we should cut Q4 marketing budget by 20% based on the AI's answer" and that answer is wrong, **who bears the consequence?**

This is the real reason "AI chat with your data" demos have flooded social media for three years while enterprise production deployments remain scarce. **90% is breathtaking in entertainment. Unusable in decision-making.**

---

## III. The Proposal: The Ontology IS the Oracle

**In a domain without a test oracle, we build a semantic oracle. That oracle is the ontology.**

What an ontology is:
- **Human-crafted**: built by ontology curators in collaboration with AI, not pre-existing
- **Versioned**: lives in Postgres tables with full CRUD, mark states, audit logs
- **Deterministic**: given the same parameters, Metric Intent produces the same SQL every time
- **Auditable**: any decision-maker can open `lakehouse_metric_intent` and read, in plain sight, what "early order" means in your company

It is not schema — **schema is physical fact; ontology is business fact**.
It is not a traditional semantic layer — those assume you build first; **ontology is constructed incrementally with AI**.
It is not a prompt description — prompts cannot be versioned, audited, or CRUD'd.

**Trust transfers here**: users are no longer asked to trust the AI. Users are asked to trust the **ontology**. When the ontology is wrong, fix one row in the DB — every future query is corrected. When the AI is wrong, you do not even know where to look.

**This is why enterprises will pay for the latter — trust has a home.**

---

## IV. Nine Principles

> This manifesto does not ask you to agree with every principle. If you accept even one and practice it in your project, you are already on our side.

### 1. No oracle, no convergence

Without a truth judge, do not expect LLMs to converge. In data analysis, the oracle must be human-made — this is an engineering problem, not a model problem.

### 2. Business meaning is an asset, not a prompt

The definition of "early order" is a corporate asset, not a paragraph in a system prompt. **Assets persist in databases; prompts decay.**

### 3. Queries grow from ontology, not from schema

Schema tells you which columns exist; ontology tells you what those columns mean. Anchoring queries to ontology rather than schema is **the paradigm watershed**.

### 4. Build the ontology WITH AI, not BEFORE AI

Traditional semantic layers demand months of upfront investment. We reject that premise — use AI for **incremental ontology construction**, so mid-market enterprises can participate.

### 5. Accumulation compounds: every question is teaching, not consumption

Do not expect the first query to return a correct answer. The intelligence of an ontology system is **accumulated, not granted** by the model.

Every time a user asks, the ontology curator should review the LLM's tokenization output — supplement missing keywords, correct wrong aliases, adjust Intent priorities. Every correction makes the system more accurate next time.

**Skip correction, and the system stays at cold start forever. Practice correction, and error rate compounds downward week by week.**

This is an advantage that **simply does not exist** in the Text2SQL paradigm — there is no layer to correct. Every Text2SQL query is one-shot, leaves no trace. **Every query we serve reshapes the ontology itself.**

**The sales pitch**:
> Buy Text2SQL — you get a snapshot accuracy number.
> Buy Text2Ontology — you get a compounding upward curve.

### 6. Trust the ontology, not the AI

Trust transfer is the root reason enterprises will pay. Let AI execute on a deterministic path; **let humans own the ground-truth ontology layer.**

### 7. Every change to the ontology must be auditable

AI modifications to the ontology must travel through draft → validation → ship. Mandatory audit, mandatory human sign-off. **Without this layer, every "AI changes schema" claim is gambling.**

### 8. Memory is structured, not token soup

Multi-turn agent memory should not be a pile of historical tokens. It should be a structured record of "ontology units confirmed in this thread." **This is the line between production-grade and demo-grade agents.**

### 9. Bounded error is a different species from unbounded hallucination

We do not claim this system never errs. We claim its **error model is a different species from LLM hallucination**:

| Error dimension | Text2SQL (LLM writes SQL directly) | text2ontology (ontology path) |
|---|---|---|
| Error space | Unbounded — any plausible-looking SQL | Bounded — only from `aliases[]` ∪ `intent.priority` ∪ `causality(join_key)` finite set |
| Error localization | Not localizable (LLM is a black box) | Precisely localizable: `agent_step` shows which tier (EXACT/FUZZY/VEC) and which keyword id |
| Error fix | Edit a prompt → impact unknown | Edit one DB row → impact bounded, all future queries fixed |
| Error reproducibility | Not reproducible (LLMs are non-deterministic) | Fully reproducible (fixed ontology snapshot → fixed SQL) |
| Error audit | Cannot prove "why I trusted this" | Can point to an Intent row and say "the CFO signed off on this definition in Q3" |

**Bounded error is acceptable error. Unbounded hallucination is not.**

---

## V. Engineering Evidence

[text2ontology](https://github.com/agentofreef/text2ontology) is the working implementation of this manifesto.

### Architecture

Four layers, each implementing a group of principles:

```
┌────────────────────────────────────────────┐
│  AI Agent (query / build modes)            │   ← Principles 4, 5, 7, 8
├────────────────────────────────────────────┤
│  Translation Engine (ontology → SQL)       │   ← Principles 3, 6
├────────────────────────────────────────────┤
│  Lakehouse Store (Postgres + ontology)     │   ← Principles 2, 3
├────────────────────────────────────────────┤
│  Data Connectors (PBIT / Excel / CSV / PG) │   ← Principle 4
└────────────────────────────────────────────┘
```

Import direction is strictly unidirectional, enforced by CI. Full architecture in [docs/spec/architecture-overview.md](https://github.com/agentofreef/text2ontology/blob/main/docs/spec/architecture-overview.md).

### What's implemented

- **Seven-image deployment** (6 Go services + nginx-served frontend), four-layer hexagonal, CI-enforced unidirectional imports
- **Two agent modes**: query / build, each with its own tool surface and data targets
- **Metric Intent system**: trigger word → complete query template (metric / filters / auto_group_by / pivot), zero-code additions
- **Thread Memory Ledger**: cross-turn structured memory, token waste reduced by an order of magnitude
- **Per-step agent logging** (`agent_step`): every agent decision is replayable

Code is fully open-source under Apache 2.0. Fork, critique, reuse.

### Still in progress (honest status)

**Principle 7 ("auditable") is currently half-implemented.** Agent-side decision logs (`agent_step`) and the builder-mode "propose → human activate" flow are both shipped. But **full version control of the ontology itself — who changed which Intent field at what time, what was the prior value, whether it can be rolled back — is not yet written**. This is the top priority on the v0.2 roadmap.

Other principles are largely in place, but each has edge cases to refine.

**We do not pretend the system is complete. We pretend our beliefs are clear, our direction is locked, and contributors are welcome.**

---

## VI. The Call

If you **agree**:
- ⭐ Star [text2ontology](https://github.com/agentofreef/text2ontology)
- Practice even one principle in your project
- Forward this manifesto to a friend working on AI + data

If you **disagree**:
- Tell me in the comments which principle you reject
- Write a rebuttal essay and send me the link — I will read it
- Open a GitHub Issue with a concrete counter-example

If you are **curious**:
- Read the essays at [text2ontology.com](https://text2ontology.com) — each principle has its own expansion
- Subscribe to the RSS feed for new posts
- @ me on Twitter / LinkedIn / HN

---

> *This manifesto is licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/) — free to share, adapt, and commercialize, with attribution to text2ontology.com.*

AgentOfReef · 2026-05
