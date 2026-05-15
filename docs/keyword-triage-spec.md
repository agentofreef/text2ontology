# Keyword Triage & Metric Intent Linkage — Design Spec

Status: agreed 2026-04-17. Implementation in progress.

## 1. Problem

Lakehouse agent recall is keyword-driven (`services/recall-server/recall/recall_lakehouse.go`).
A token coming out of `simple_split` can land in one of three places:

1. `lakehouse_keyword` with `property_id` → property/value recall path
2. `lakehouse_keyword` with `metric_intent_id` → canonical smartquery template
3. Nothing at all (MISS) — orphan token, agent has no hook

There are three operational gaps:

- **No Od alias path.** `lakehouse_keyword` requires either `property_id` or `metric_intent_id`; there is no clean way to say "this token refers to the whole Od (e.g. 'early order' = Order)."
- **No triage surface.** `ont_agent_annotation.tokens` records what the agent saw per question, but nothing aggregates the orphans, nothing distinguishes "never indexed" from "indexed but not bound to anything."
- **Binding is SQL-only.** Adding a metric intent still requires `UPDATE lakehouse_keyword SET metric_intent_id=...` by hand. `/dax/ontology/lakehouse-metric-intents` shows intents but is blind to which keywords trigger them.

## 2. Core insight — one keyword, multiple hats

A single token can legitimately carry more than one binding. `early order` is the canonical example:

| Hat | Why |
|---|---|
| Value alias | `Order.Order_Type = 'Early Order'` is a real data value |
| Od alias | Business speak uses "early order" to mean the whole Order table |
| Intent trigger | Bound to `Order.Quantity` metric intent so the canonical pivoted query fires |

Therefore: **bindings are tags, not a single dropdown.** Storage = one `lakehouse_keyword` row per binding; the same keyword string appears N times with different anchor columns set. This matches the current runtime, which already runs the property and intent recall paths concurrently.

## 3. Four binding targets + one ignore bucket

| Target | Storage shape | Recall path |
|---|---|---|
| **Od alias** | `object_id=X, property_id=NULL, metric_intent_id=NULL` | `token` → Od block directly |
| **Property (column) alias** | `property_id=X, is_column_name=true` | `token` → property, used as groupBy/select |
| **Value alias** | `property_id=X, is_column_name=false` | `token` → property, used as filter value |
| **Metric intent trigger** | `metric_intent_id=Y` | `token` → canonical smartquery template |
| **Ignore (stopword)** | `is_stopword=true, all anchor cols NULL` | Skipped by every tier |

Multi-select is the norm. Clicking "save" on the triage panel diffs the checkbox set against the current rows and issues INSERT / DELETE accordingly.

## 4. Triage queue — three badge states

A token on the left list can be in one of:

| Badge | Condition |
|---|---|
| 🟠 Orphan | Token appears in `ont_agent_annotation.tokens` but no `lakehouse_keyword` row matches (case-insensitive) |
| 🟡 Floating | `lakehouse_keyword` row exists but all four anchor columns (`object_id`, `property_id`, `metric_intent_id`, `is_stopword`) are null/false |
| 🔵 Partial | ≥1 binding exists, but the token still has MISS hits in some annotations (e.g. "early order" bound to Intent but not to Order Od yet) |

All three are included in the queue by default; a sidebar filter flips between them. Rationale: real operator flow is "see orphan → bind Intent → come back later to add Od alias," so the same token needs to resurface.

## 5. Layout — 3 columns

Mirrors `/dax/ontology/lakehouse-agent/dataset-testing/detail` (`VersionSidebar | RunCaseList | CaseDetailPanel`).

```
┌─────────────┬──────────────────────┬────────────────────────┐
│ L sidebar    │ M token queue        │ R assignment panel     │
│ 240px        │ flex                 │ flex                   │
│              │                      │                        │
│ ▸ All (N)    │ token · cnt · badge  │ Top-10 questions       │
│ ▸ 🟠 Orphan  │ ...                  │ with per-token binding │
│ ▸ 🟡 Float.  │ ...                  │ chips for context      │
│ ▸ 🔵 Partial │                      │                        │
│ ▸ Ignored    │                      │ ── Binding targets ── │
│              │                      │ ☐ Od alias            │
│ search ▢     │                      │ ☐ Column alias         │
│ version ▾    │                      │ ☐ Value alias          │
│              │                      │ ☐ Intent trigger       │
│              │                      │ ☐ Ignore               │
│              │                      │ [Save & Next]          │
└─────────────┴──────────────────────┴────────────────────────┘
```

Visual tokens (matching the rest of the site):

- `font-mono text-[10px]` badges
- `bg-canvas-alt` section dividers
- Left-border accent on selected row
- EXACT=emerald / FUZZY=amber / VEC=blue / MISS=ghost for binding chips

## 6. Right panel — context is king

For the selected token, render Top-10 questions that contain it. Each question row shows **every token in that question** with its current binding(s) as chips. This gives the reviewer the context needed to decide which hats the selected token should wear.

Example render for token "early order":

```
"X11 的 early order 按周分布"
  X11         → [Product.Product_Name · value]
  early order → MISS ⚠ (this token — deciding now)
  按周         → [time dim]
  分布         → [Intent · Order.Quantity.Distribution]
```

Reviewer can scan three such questions and infer: this token = Order Od alias AND Order_Type value alias AND already bound to Order.Quantity intent.

## 7. Metric-intents page — two sibling changes

`/dax/ontology/lakehouse-metric-intents` gets:

1. **TRIGGERS column** between PIVOT and FILTERS. Renders bound keyword + alias chips (`SELECT ... FROM lakehouse_keyword WHERE metric_intent_id = mi.id`). Click × to remove. Inline `[+ add trigger]` opens a mini triage flow that creates a new `lakehouse_keyword` row scoped to this intent.

2. **Missing pivot fields.** CLAUDE.md documents `pivot_column_labels`, `pivot_with_percent`, `pivot_append_grand_total`; the schema has them (`schema.sql:712-714`) but `handler_intent.go` INSERT/UPDATE does not write them and the form does not display them. Backfill:
   - Toggle: `pivot_with_percent`
   - Toggle: `pivot_append_grand_total`
   - Array editor: `pivot_column_labels` (parallel to `pivot_values`)

## 8. Schema changes

```sql
-- lakehouse_keyword: add Od-alias and stopword support.
ALTER TABLE lakehouse_keyword
  ADD COLUMN IF NOT EXISTS object_id   UUID REFERENCES ont_object_type(id) ON DELETE CASCADE,
  ADD COLUMN IF NOT EXISTS is_stopword BOOLEAN NOT NULL DEFAULT FALSE;

-- Relax legacy CHECK: any one anchor is enough, stopword stands alone.
ALTER TABLE lakehouse_keyword DROP CONSTRAINT IF EXISTS lakehouse_keyword_anchor_chk;
ALTER TABLE lakehouse_keyword
  ADD CONSTRAINT lakehouse_keyword_anchor_chk CHECK (
    is_stopword = TRUE
    OR property_id      IS NOT NULL
    OR object_id        IS NOT NULL
    OR metric_intent_id IS NOT NULL
  );

-- Triage queue scan index.
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_lower
  ON lakehouse_keyword (project_id, LOWER(keyword));
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_object
  ON lakehouse_keyword (object_id) WHERE object_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_stopword
  ON lakehouse_keyword (project_id) WHERE is_stopword = TRUE;
```

Existing `object_type_id NOT NULL` stays: every row still belongs to one Od for list scoping. For pure Od aliases the row's `object_type_id` and new `object_id` will match; kept separate because `object_type_id` was used by legacy recall as a routing hint (removing it is out of scope here).

Note that `lakehouse_metric_intent` already has `pivot_column_labels`, `pivot_with_percent`, `pivot_append_grand_total`, `pivot_percent_axis` — schema is fine, only handler and frontend need updates.

## 9. Recall pipeline changes

`services/recall-server/recall/recall_lakehouse.go`:

- **Stopword skip**: every SQL in `searchLakehouseKeywordFull` + `lookupIntentsForToken` adds `AND COALESCE(lk.is_stopword, false) = false`.
- **Od-alias path**: add a fourth query branch after Tier 3 VEC that matches `lakehouse_keyword.object_id IS NOT NULL` and emits an Od block directly (no property). Existing `fallbackDirectOd` only matches `ont_object_type.name` by exact text — the new branch lets aliases work.
- **Property/Intent paths unchanged**: no SQL shape change for existing callers.

## 10. API surface

All under `/api/ontology/keyword-triage/*` unless noted.

| Method | Path | Role |
|---|---|---|
| `GET` | `/queue?projectId=&versionId=&badge=` | Left sidebar counts + middle queue rows |
| `GET` | `/token?projectId=&versionId=&token=` | Right panel: Top-10 questions with per-token bindings |
| `POST` | `/assign` | Body = `{token, bindings: [...]}`, diffs against existing rows, INSERTs/DELETEs |
| `GET` | `/api/ontology/metric-intents/{id}/triggers` | TRIGGERS chip data for the intents table |
| `DELETE` | `/api/ontology/metric-intents/{id}/triggers/{kwId}` | Remove a single trigger row |

Payload for `/assign`:

```json
{
  "token": "early order",
  "versionId": "<uuid>",
  "projectId": "<uuid>",
  "bindings": [
    { "kind": "od_alias",       "objectId": "<uuid>" },
    { "kind": "value_alias",    "propertyId": "<uuid>", "value": "Early Order" },
    { "kind": "intent_trigger", "intentId": "<uuid>" }
  ],
  "ignore": false
}
```

`kind` ∈ `od_alias` | `column_alias` | `value_alias` | `intent_trigger`. `ignore: true` replaces all rows with a single `is_stopword=true` row.

## 11. Build order

1. **Spec** (this doc)
2. **Schema definition** in `docs/schema/schema.sql` (single source of truth)
3. **Recall**: stopword skip + object_id branch (pure SQL-level changes, no new types)
4. **Metric intent handler**: add missing pivot fields to INSERT/UPDATE/scan/list
5. **Triage API**: queue / token / assign / intent-triggers
6. **Triage page**: new route, reuses `SharedCaseBits` style vocabulary
7. **Metric-intents frontend**: TRIGGERS column + pivot toggles
8. **Verify**: `make build`, schema load on empty DB, smoke endpoints

## 12. Non-goals

- Rewriting `fallbackDirectOd` to kill the hardcoded `ont_object_type.name` match. Leave as-is; Od alias path coexists.
- Merging `object_type_id` and `object_id` into one column. Out of scope; backward compat risk too high.
- Auto-detecting stopwords by frequency. Manual for now; stopword bucket is the only operator action.
- A migration that hoists existing `fallbackDirectOd`-style hardcoded aliases into `lakehouse_keyword.object_id`. Operator can re-enter via triage UI if they want.
