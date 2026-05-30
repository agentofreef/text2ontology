# The Compiler for Data Analysis

> Code gets written correctly because tests give you an oracle — an automatic check on whether the answer is right. Data analysis has no oracle, so it has been stuck in demos for three years. But "no oracle" is only half true.

---

You ask an AI for a business number. It crisply returns "12.3%." Stop for a second — **how do you know 12.3% is right?**

You can't check it. Whether "early order" means `status='CONFIRMED'` or `status IN ('CONFIRMED','SHIPPED')` lives in someone's head — not in the code, not in the data. Writing code is different: the AI writes it, you run the tests, pass or fail, done. **The test suite is the oracle — it tells you, automatically, whether you're right.** Data analysis has no such oracle.

That is why three years of "chat with your database" demos have flooded the internet while almost none reached enterprise production: with no oracle, the model cannot converge, and it stalls at "looks impressive." Not because the model is weak — because the task is **structurally** missing something. (I make the full "no oracle → no convergence" case in [the manifesto](../manifesto/manifesto.en.md) — but this essay does not need you to have read it.)

And "data analysis has no oracle" is only half true. **The part you actually cannot verify was never the whole question — it is one cut**: how the number is defined. That cut has a precise name — the **metric**, its *caliber*: a fixed bore, an authorized definition of what a number means.

The metric is the compiler for data analysis. This essay is about why that sentence is true — and why it means you can finally tell, the moment an answer is wrong, that it is wrong.

## Collapse the under-determination onto the one thing that's under-determined

Take that one cut apart. In "early-order rate", which part is genuinely under-determined?

- "by region" / "Q4 only" / "exclude test data" — these are **dimensions and filters**. Stated at question time, fully determined.
- "do we join the product table" — a **physical path**, frozen in the ontology's relationship graph. Determined.
- **"early order = `status='CONFIRMED'` or `IN ('CONFIRMED','SHIPPED')`"** — *this* is the one genuinely ambiguous cut. It's a **measurement caliber**, and the answer isn't in the data; it's an organizational consensus.

Every "AI talks to your database" failure of the last three years bottoms out at that last cut: **the LLM is forced to guess a definition that exists only in someone's head, then reports the guess as fact.**

A metric moves that cut from "guessed at runtime by the LLM" to "written down once, by a human, stored, and auditable." Concretely, a metric is the most minimal possible measure on one object:

```sql
select "ORDER_TYPE", sum("ORDER_QUANTITY") as "TOTAL"
from "EARLY_ORDER"
group by "ORDER_TYPE"
```

On save it's decomposed into three things: the **primary object** (`EARLY_ORDER`), the **measure** (`sum(ORDER_QUANTITY)`), and the **base dimension** (`ORDER_TYPE`). It answers "what to compute" — never "how to filter or slice", which stays at runtime. It's the legally-binding definition of that number inside your org.

## Why this is a compiler, not a semantic layer

dbt, Cube, and LookML have done "curate a set of metric definitions" for years. If a metric were just another semantic layer, it wouldn't be worth an essay.

The difference is one gate:

> **Any aggregated measure must be endorsed by an authorized metric. Otherwise the system refuses to run — it returns `NO_AUTHORIZED_METRIC`, instead of fabricating an unbacked number to cover the gap.**

That gate is the line between a semantic layer and a compiler.

A compiler's essence isn't "it generates code." It's that **well-formed programs compile, and ill-formed programs are rejected with an error — not silently mis-compiled.** The metric system has exactly that shape:

- The question hits an authorized metric → the engine deterministically assembles Postgres SQL along the ontology's relationships, runs it, returns rows. **The LLM never touches SQL and never sees a JOIN.**
- The question hits no metric → it says "that metric doesn't exist" and declines (a capability gap).

So the failure mode that has haunted the whole field — **the confidently-wrong number** — is structurally gone. Only two outcomes remain:

> **Either the value its metric defines, or a refusal that the metric doesn't exist.**

"Confidently report a number nobody authorized" is not a reachable state in a compiler. That's why I'll say it plainly: a metric is not a better semantic layer. **A metric is the first time data analysis has had a compiler.**

## The North Star is auditability, not accuracy

This is the most important paragraph in the essay, and the easiest to overstate, so let me nail it down — because it inverts the whole field's default assumption.

Every "AI talks to your data" product optimizes the same number: **accuracy** — 90%, 95%, a bigger model, a better prompt. But in a domain with no oracle, **accuracy isn't a thing you can optimize directly** — you can't even decide whether *this* answer is right, so what gradient are you descending? It's the flip side of the "no oracle, no convergence" point from the top.

So this system's North Star was never accuracy. It's **auditability** — a plainer and more uncomfortable question:

> **How do I know this answer is wrong?**

Here's the counterintuitive part that matters most:

**An "accurate" answer you can't check is more dangerous than a possibly-wrong answer you can point at and refute.**

An LLM hands you "12.3%". Even if it's right 95% of the time, you cannot tell *in the moment* whether this is the wrong 5% — and the CFO who cuts 20% of the budget on it is betting on luck. A metric-backed number is different: it **points at a definition you can open, read, and argue with.** You see "early order = `status='CONFIRMED'`", you say "no, we count SHIPPED too", you change that one row — and every future query is corrected.

Borrow Popper: **an unfalsifiable claim is worthless for a decision.** The LLM's "12.3%" is unfalsifiable — you have nothing to test it against. A metric-backed number is falsifiable — it's anchored to an explicit definition you can challenge line by line. **The compiler's gift to data analysis was never correctness. It's falsifiability.**

And now "consistency over correctness" finally has its causal chain: correctness can't be optimized (no oracle); consistency can — and **consistency is the precondition for auditability.** Only when the same question always yields the same answer can you pin that answer down, trace it to its metric, and refute it. An answer that drifts every time can't be audited at all.

So, precisely:

> **Every number the metric system compiles is correct relative to organizational consensus — not necessarily correct relative to the world.** It does not promise your metric is right. It promises that **when the metric is wrong, you will know, you can locate it, and you fix it once.**

A C compiler never let a programmer escape "my logic might be wrong." It only made "wrong" locatable, fixable, reproducible. A metric does the same for data analysis: it doesn't let you escape "the definition might be wrong" — it makes "wrong" knowable on the spot. *That* is what it sells.

## One metric, many questions

A compiler can't be rigid. The metric's flexibility comes from a runtime three-state on every property:

- **not passed** → the column doesn't appear;
- **passed, value empty** → shown as a dimension (`group by`, no filter);
- **passed, with a value** → filter + show ("filter implies display").

So the one "early-order" metric, unchanged, answers "early orders by type", "early orders by brand" (auto-joining the product object along the ontology's relationships), and "Legion's early orders" — the LLM only **picks the metric and fills parameters**, never writes SQL.

## From compiler to build system: the metric dependency graph

The next step is natural: **let metrics depend on each other.**

Treat each metric as a node. An edge means "metric B needs metric A's output before it can compute" — and **the edge itself carries the parameter binding**: an output column of A feeds a parameter of B.

Once that graph exists, something fundamental flips. Facing a question, **you stop asking "how many intents does this have, how do I split it into sub-questions"** — the most fragile, most guess-driven step in the old pipeline. You only ask: which **target metric** does this hit, and which of its parameters come from the question versus from upstream metrics?

Decomposition stops being an LLM inference and becomes **deterministic resolution of a curated graph** — backward-chaining from the target metric, wiring each unmet parameter to an upstream metric whose output type matches, until every leaf parameter is supplied by the question. This isn't a "task decomposer"; the better name is a **caliber executor** — decomposition is a property of the graph, not a runtime guess. (It's the same shape as a dbt `ref()` DAG, typed function composition, or Datalog backward chaining.)

One discipline keeps this from collapsing back into an open-ended, unbounded planner — the one thing this whole approach exists to avoid:

> **Edges are human-curated and typed. The LLM only picks the target metric and fills leaf parameters. The executor is a deterministic evaluator, not a search-driven planner.**

Hold that line, and "answer correctly or say it doesn't exist" generalizes from a single metric to the whole graph: a question is answerable iff the target metric's dependency closure resolves down to the leaves. If it doesn't, that's a gap — declared honestly.

## So BI was the wrong frame, and the substrate is the metric

Step back. BI threw off a wave of tools (Cognos, Business Objects, early Tableau). In hindsight, the durable value didn't accrue to the **surface BI tools** — it accrued to the **substrate underneath**: the warehouse and transformation layer (Snowflake, Databricks, dbt). The BI tool was the pseudo-proposition; the real driver was "data volume is large enough that an infrastructure layer becomes inevitable."

AI is replaying that act. The **surface** — "AI agentic data analyst", "talk to your database" — is what the foundation-model companies will commoditize. The **substrate** — a curated set of metrics plus a compiler that turns language into SQL against them — is the layer that, once it stands up inside an org, everything else has to route through.

This system isn't another AI BI tool. It's the **substrate for natural-language data analysis**. "AI does the analysis" is the surface; "the org writes the metrics, the AI only compiles against them" is the foundation.

## Where it breaks (it isn't done, and it has edges by design)

Turning the knife on myself:

1. **The compiler checks consistency, not truth.** An authorized-but-wrong metric still compiles, faithfully. The system guarantees "the answer matches the metric and the error has an address" — it cannot guarantee your metric is business-correct. That's always a human's job.
2. **The new failure mode is the false "doesn't exist."** The metric is there, but tokenization/recall missed it, so it wrongly refuses. The error moves from "confidently wrong" to "wrongly declined" — strictly better (honest beats wrong) — but it puts the whole burden on recall quality. **Tokenization is still 60%+ of the outcome.**
3. **Derived metrics and set-valued dependencies are where determinism is thinnest.** Ratios (rate = early / total), and whether to iterate over an upstream metric's output set (map), push the dependency graph from a simple DAG toward a small map/reduce dataflow language. This is the hardest, least-finished part.
4. **At cold start the compiler is empty.** No metrics → everything declines. Its "grammar" is written one metric at a time by a business ontology engineer. **The revolution is in the model; the payoff comes from curation.**

## Closing

The first half is the one we opened with: data analysis has no oracle, so the LLM can't converge.

This essay adds the second half: **the oracle was always there — its name is the metric.** If you're willing to anchor every measure to a human-written, auditable metric, data analysis has its compiler — the metric is its type system, and the metric dependency graph is its build system.

The price is honest: what it compiles is conformance to organizational consensus, not objective truth. But that's exactly the point — in a domain with no oracle, **knowing when you're wrong, where, and how to fix it once is worth far more than being occasionally, uncheckably right.**

It took programmers decades to make the word "compiler" feel inevitable. The compiler for data analysis is only just starting.

The working implementation is open source: [text2ontology](https://github.com/agentofreef/text2ontology).

---

> *Licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/) — share, adapt, and commercialize freely, with attribution to text2ontology.com.*

AgentOfReef · 2026-05
