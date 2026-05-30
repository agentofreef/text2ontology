// Pure logic for explore-mode CommitCard handling (chat-first redesign,
// Step 6a). No React import; no DOM dependency; safe to
// import from CommitCard.tsx or to unit-test standalone.
//
// Frontend TS mirror of pkg/contracts/commit_card.go (CommitCardPayload). The
// agent-server emits this via SSE `data: {"type":"commit_card", ...}` events
// when the explore-mode LLM converges on a candidate metric.

export interface CommitCardParameter {
  name: string
  type: string
  optional?: boolean
  description?: string
}

export interface CommitCardPayload {
  id: string
  name: string
  displayName: string
  // "aggregate" (default) — a measure; canonicalMetric is the aggregate expr.
  // "enumerate" — a distinct-value list; canonicalMetric is empty and querySql
  // is a SELECT DISTINCT. Optional for backward-compat with pre-intent cards.
  intent?: 'aggregate' | 'enumerate'
  // Structured spec the LLM emitted (engine compiles SQL). Display-only on the
  // frontend; canonicalMetric/querySql below are the derived/compiled form.
  measure?: { agg: string; column?: string }
  dimensions?: string[]
  filters?: Array<{ prop: string; op: string; value: string }>
  canonicalMetric: string
  querySql: string
  autoGroupBy: string[]
  parameters: CommitCardParameter[]
  triggerKeywords: string[]
  responseTemplate: string
  description: string
  primaryOd: string
  draftId: string
}

// ── synthesizeBareSql (PROMOTED to exported per plan A-2) ────────────────
//
// Build a BARE simple SQL from primaryOd + canonicalMetric (+ optional
// autoGroupBy). Frontend-only synthesis used by CommitCard's read-only
// preview; the backend pkg/sqlrewrite synthesis is unaffected.
export function synthesizeBareSql(input: {
  canonicalMetric: string
  primaryOd: string
  autoGroupBy?: string[]
}): string {
  const od = input.primaryOd
  const agg = stripOdPrefix(stripAlias(input.canonicalMetric), od)
  const groups = input.autoGroupBy ?? []
  if (groups.length === 0) return `SELECT ${agg} AS value\nFROM "${od}"`
  const groupCols = groups.map((g) => stripOdPrefix(g, od)).join(', ')
  return `SELECT ${groupCols}, ${agg} AS value\nFROM "${od}"\nGROUP BY ${groupCols}`
}

function stripOdPrefix(expr: string, od: string): string {
  return expr.replace(new RegExp(`\\b${od}\\.`, 'g'), '')
}

// Strip a trailing `AS <ident>` (or `AS "ident"`) from an expression.
// Anchored to end so `AS` inside a CASE expression survives.
export function stripAlias(expr: string): string {
  return expr.replace(/\s+AS\s+("?\w+"?)\s*$/i, '').trim()
}

// ── Save payload builder ─────────────────────────────────────────────────
//
// Shape required by PUT /api/ontology/lakehouse-metrics/<id>. Matches the
// backend handler_lakehouse_metric.go expected body. The 采纳 flow calls
// this with `promote: true` so `mark=true` flips the row.

export interface MetricSavePayload {
  name: string
  displayName: string
  description: string
  level: 'simple'
  canonicalMetric: string
  autoGroupBy: string[]
  parameters: Array<{ name: string; type: string; optional: boolean; description?: string }>
  responseTemplate: string
  querySql: string
  mark: boolean
  triggerKeywords: string[]
}

export function buildSavePayload(
  m: CommitCardPayload,
  opts: { promote?: boolean } = {},
): MetricSavePayload {
  const canonical = stripAlias(m.canonicalMetric || '')
  return {
    name: m.name,
    displayName: m.displayName,
    description: m.description || '',
    level: 'simple',
    canonicalMetric: canonical,
    autoGroupBy: m.autoGroupBy ?? [],
    parameters: (m.parameters ?? []).map((p) => ({
      name: p.name,
      type: p.type,
      optional: p.optional !== false ? !!p.optional : false,
      description: p.description,
    })),
    responseTemplate: m.responseTemplate || '',
    querySql: m.querySql || '',
    mark: opts.promote ? true : false,
    triggerKeywords: m.triggerKeywords ?? [],
  }
}

// ── Promote blocker ──────────────────────────────────────────────────────
//
// Returns null when the CommitCard is ready for 采纳; otherwise returns a
// human-readable reason. Drives the 采纳 button's enabled/disabled state.
export function promoteBlocker(m: CommitCardPayload): string | null {
  if (!m.primaryOd) return '主 OD 未填'
  if (!m.canonicalMetric) return 'canonical_metric 未填'
  if (!m.querySql) return 'querySql 未填'
  if (!m.triggerKeywords || m.triggerKeywords.length === 0) {
    return '召回 KW 为空(LLM 找不到这条口径)'
  }
  if (!m.name || !/^[a-z][a-z0-9_]*$/.test(m.name)) {
    return `name "${m.name}" 不符合 snake_case 规范`
  }
  return null
}
