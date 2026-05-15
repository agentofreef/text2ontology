# text2ontology — Design Philosophy

> Three-layer ontology + two-level query + one-pass recall

---

text2ontology is a system that lets enterprises **ask data questions in natural language and get consistent answers**.

The fundamental difference from Text2SQL: **the LLM does not generate SQL — the LLM only fills parameters**. SQL is generated deterministically from an ontology that the enterprise maintains internally.

This essay walks the whole design through **one concrete Q&A**. By the end you should be able to answer three things:
- What happens when a user asks one sentence
- What each of the 7 core concepts (OD, OK, OL, Link, Causality, Intent, Keyword) is responsible for
- Why the system is structured this way and not another way

---

## 1. Start with one Q&A: "How's the early-order rate?"

This is the spine of the whole essay. See the full flow first, then unpack each step.

```
User asks: "How's the early-order rate?"
   ↓
[1] Forced tokenization
    Every user question is tokenized — no exceptions.
    Result: ["early-order rate", "how's"]
   ↓
[2] Recall (deterministic backend code, no LLM)
    Each token runs through three cascading tiers:
    EXACT match → FUZZY match → VECTOR (semantic) match

    "early-order rate" hits:
      - Keyword "early-order rate" in the keyword table (EXACT)
      - Points to Intent: Order.EarlyOrderRate
      - That Intent is anchored on OD: Order
      - Order's attached OKs: "business definition of early order"
      - Order's Links: Order → Customer

    "how's" is a non-content token; ignored.
   ↓
[3] Context assembly
    All recalled structured info is assembled into an LLM-readable shape.
    Context tells the LLM explicitly:
      "You were given these ODs, these Intents, these OKs."
      "Your only tools are Lookup and Query."
   ↓
[4] LLM tool call
    Looking at the context, the LLM decides:
      - Call Query directly (just fill the params) — most cases
      - Call Lookup first (need more detail) — when info is insufficient
      - Or answer directly (already enough)

    The LLM cannot freely assemble SQL. It can only fill:
      {intent_id: "Order.EarlyOrderRate", params: {period: "Q1"}}
   ↓
[5] Query tool executes
    Intent carries the full query shape: metric, filters, groupBy, pivot config
    System translates Intent + params into SQL, runs against Postgres, returns rows
   ↓
[6] LLM summarizes
    LLM reads the result, generates natural-language answer
    (with a preset response_template, or freely)
```

**The core insight:**

> **"Tokenize + recall" is deterministic — backend SQL code, no LLM.**
> **The LLM is a constrained executor, not a source of truth — it can only pick from recalled context, fill parameters, and call tools.**

This is the essential difference between text2ontology and the "free-form AI agent" school.

---

## 2. Overall architecture

Abstract the runtime above into structure, and you get text2ontology's two core frameworks:

### Three-layer ontology lifecycle

```
┌────────────────────────────────────────────────┐
│  Existence Layer                                │
│    OD (Object)         OK (Knowledge)           │
│    Answers: what exists in the business         │
├────────────────────────────────────────────────┤
│  Connection Layer                               │
│    Link (OD↔OD)        Causality (OK↔OK)       │
│    Answers: how these things connect            │
├────────────────────────────────────────────────┤
│  Learning Layer                                 │
│    OL (operational facts, can sediment into OK) │
│    Answers: what was learned in operation       │
├────────────────────────────────────────────────┤
│  Entry Layer                                    │
│    Intent + Keyword                             │
│    Answers: how natural language enters         │
└────────────────────────────────────────────────┘
```

Free composition within a layer; **dependencies flow one way across layers** — Entry references Existence, Connection sits on top of Existence, Learning is a byproduct.

### Two-level query architecture

```
Level 1: Ontology Level
  Abstract function call: {intent_id, params}
  Operates on: OD / Intent / Property
  Property: deterministic, decoupled from physical tables
                  ↓
        (translation via OD.semantic_sql)
                  ↓
Level 2: Physical SQL
  Actual SELECT ... FROM ... statement
  Operates on: Postgres staging schema
  Property: EXPLAIN-able, auditable
```

**What the two-level architecture really does:** distribute the cost of change across layers. Schema changes only touch an OD's `semantic_sql`; Intent changes only touch an Intent row; NL changes only add a Keyword alias. **Each layer absorbs changes below it and exposes a stable interface above.**

---

## 3. What each of the 7 concepts handles

| Concept | Physical table | The tension it resolves |
|---|---|---|
| **OD** (Ontology Data) | `ont_object_type` + `ont_property` | Business entity vs. physical table decoupling |
| **OK** (Ontology Knowledge) | `ont_knowledge` | Business structure vs. business knowledge separation |
| **OL** (Ontology Learned-fact) | `ont_learned_fact` | Static knowledge vs. dynamic learning coexistence |
| **Link** | `ont_link_type` | Making physical JOIN paths explicit |
| **Causality** | `ont_causality` | Business causation vs. physical relation distinction |
| **Intent** | `lakehouse_metric_intent` | Bridge between NL vagueness and SQL determinism |
| **Keyword** | `lakehouse_keyword` | Dual-channel entry: literal match + semantic match |

### Three key relationships

**OD and OK**: an OK only exists attached to an OD (can attach to the OD itself, to a Property, or form an OK tree). OK is OD's semantic patch.

**OL and OK**: OL is a fact the AI proposes during a conversation (`confidence=pending`). After BOE review it becomes `confirmed`. **Multiple similar OLs, after repeated confirmations, can sediment into a single OK** (experience abstracted into knowledge).

**Intent and Keyword**: Intent is a query template (anchored on one OD). Keyword is a trigger word (points to a property or to an Intent). Together they bridge NL to deterministic queries.

### OL → OK sedimentation (design intent, not yet implemented)

Example: three OLs separately record "April 2024 / April 2025 / April 2026 sales all exceeded 1M."

```
AI periodically clusters similar OLs
    ↓
For tight OL groups, generates a "sedimentation candidate OK"
    e.g. "April is historically a sales-strong month, typically above 1M"
    ↓
BOE reviews → if accepted, written to OK table, with evidence_for_ok pointing back to source OLs
Source OLs are not deleted (audit trail)
```

**Key principle**: AI detects patterns, **humans decide truth**. Induction cannot be left to an algorithm in isolation — it will hallucinate regularities out of noise.

---

## 4. Two core tools: Lookup and Query

The entire tool surface is two tools. Minimalism is the design philosophy.

**Lookup** — query ontology contents; read-only, no side effects
- Input: keyword / OD name / Intent name
- Output: the unit's full definition + explanation text + its associations
- When: LLM needs to look up a more detailed definition, explanation, or relationship

**Query** — execute an Ontology query
- Input: `{intent_id, params}`
- Goes through Level 1 → Level 2 translation, runs in Postgres
- When: LLM is ready to answer the data question

**Key constraint**: Query cannot take SQL. The LLM may only supply `intent` and `params` — it cannot assemble SQL strings freely. This is the engineering payoff of "ontology before query" — **the LLM is forever a slot-filler on a deterministic path**.

---

## 5. Four design principles

The whole design compressed into four lines:

### 1. OD is the root

A project without an active OD cannot answer any business question. OK gives answers their "why"; OL lets the system evolve. **OD is the only necessary condition.**

### 2. Ontology and Schema are two different levels

Schema is physical fact (tables, columns, types). Ontology is business fact (business objects, relations, definitions). One OD corresponds to **exactly one** `semantic_sql`, and that SQL may reference any number of physical tables. Physical tables are implementation detail; ontology is the business encapsulation.

### 3. Every ontology unit has a mandatory dual layer

Every OD, OK, OL, Link, Causality, Intent, Keyword carries both a **structural layer** (for machines) and an **explanation layer** (for AI and humans). The explanation layer is vector-embedded, and recall is dual-channel (literal + semantic).

> **At the natural-language entry, the name doesn't matter — the meaning does.**
> **Column names tell the machine; explanations tell the AI.**

### 4. "Correct" is organizational consensus, not objective truth

"What's the early-order rate?" is not a question with a single correct answer — it is **under-determined**. Early order = confirmed / paid / shipped? Denominator = touched customers / new customers / target customers? Each combination corresponds to a real number; **none of them is "the answer"; each is one solution in a solution set**.

What ontology does is not Discovery (finding truth) — it is **Resolution** (specifying consensus):
- Among the legal solutions, choose one as the organization's standard
- Lock that choice so every future query uses the same one
- When wrong, it's not "objectively wrong" — it's "inconsistent with current consensus" → change the ontology, and all future queries are corrected in lockstep

**This is what the system actually sells**: not "more accurate AI," but **governable organizational consensus**.

---

## 6. Three hard invariants + two future work items

### Three hard invariants (enforced by architecture, cannot be bypassed)

1. **OD necessity** — a project without active OD: Query tool refuses to execute
2. **OD 1:1 semantic_sql** — every active OD has exactly one SQL definition (which may reference multiple physical tables)
3. **No island OD** — when there's more than one active OD, any active OD must be connected to at least one other active OD through at least one active Link

### Two future work items

1. **OL → OK automatic sedimentation**: clustering + AI proposal + BOE review workflow — not yet implemented
2. **Explanation-layer versioning**: every ontology unit's description should leave a history of edits — not yet implemented

### Relationship with the surrounding ecosystem

- vs **OWL / RDF / Semantic Web**: same root idea, different engineering path (SQL tables vs. triples + SPARQL)
- vs **dbt Semantic Layer / Cube**: they solve BI consistency (consumer = dashboard); text2ontology solves AI answer consistency (consumer = LLM agent)
- vs **LangChain / LlamaIndex**: those are LLM toolchains; text2ontology is ontology governance infrastructure *outside* the LLM

Recall mechanics in depth (three-tier cascade + Intent priority + explanation-layer vector recall) are not covered in this essay — see `recall-server/` in the [codebase](https://github.com/agentofreef/text2ontology) for the implementation.

---

## 7. Why 7 concepts, not 3

Each concept corresponds to **a real, unavoidable design tension** (see table in §3).

Having many concepts is not a flaw. **Having few, vague concepts is the flaw** — when a domain's complexity cannot be fused, trying to fuse it merely transfers the complexity to the user.

If you remember one line:

> **OD is the root. Two-level queries are the skeleton. The runtime core is recall, not reasoning.**
> **The AI is not a source of truth; it is a constrained executor.**

Everything else is that line unfolded from different angles.

---

> This essay is part of the [text2ontology](https://github.com/agentofreef/text2ontology) series:
> - Technical thesis: [*Ontology Before Query*](./manifesto.en.md)
> - Commercial thesis: [*Responsibility as Moat*](../essays/responsibility-as-moat.en.md)
> - Counter-narrative: [*"AI Agentic Data Analyst" is the most expensive illusion of 2026*](../essays/ai-agentic-illusion.en.md)
> - Role definition: [*Business Ontology Engineer*](../essays/business-ontology-engineer.en.md)
> - **Design philosophy**: this essay ★
>
> Licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).

AgentOfReef · 2026-05
