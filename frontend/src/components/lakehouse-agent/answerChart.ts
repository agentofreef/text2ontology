// Chart-schema parsing for AI answers (图表表达式).
//
// Same invariant as dataTemplate.ts: the LLM never writes data values. For a
// large tool result it instead writes a CHART SCHEMA at the end of its answer —
// a pointer that names a chart type + which columns map to which axes, e.g.
//
//   「chart type=bar from=t1 x=月份 y=销量,成本 series=地区」
//
// The schema references columns of a tool-result table (from=tN); the actual
// numbers come from that table at render time (collectStepResults), never from
// the LLM. The backend injects the instruction to emit this ONLY when a tool
// returns >20 rows (handler/lakehouse_tools.go) — it is not in the system
// prompt and the tool call itself never renders anything; it only fetches data.

export type ChartType = 'bar' | 'line' | 'pie' | 'area'

export interface ChartSpec {
  type: ChartType
  /** Step id of the source table, e.g. "t1" — resolved via collectStepResults. */
  from: string
  /** Category / X-axis column name (as named in the tool result). */
  x: string
  /** One or more measure columns. */
  y: string[]
  /** Optional grouping column → one series per distinct value (single-y only). */
  series?: string
}

export type AnswerSegment =
  | { kind: 'text'; text: string }
  | { kind: 'chart'; spec: ChartSpec; raw: string }

// 「chart … 」 — a complete token only (must be closed). A half-streamed
// 「chart … without the closing 」 is left in the text segment verbatim and
// becomes a chart once the 」 arrives.
const CHART_TOKEN_RE = /「\s*chart\b[^」]*」/g

const KNOWN_KEYS = ['type', 'from', 'x', 'y', 'series'] as const

/**
 * parseChartToken parses the inside of a 「chart …」 token into a ChartSpec, or
 * null if required fields (from / x / y) are missing. Values may contain spaces
 * or CJK: each value runs up to the next ` <knownKey>=` or end-of-string, so
 * column names with spaces survive.
 */
export function parseChartToken(token: string): ChartSpec | null {
  const inner = token.replace(/^「\s*/, '').replace(/\s*」$/, '').trim()
  const body = inner.replace(/^chart\s*/i, '')

  const get = (k: string): string | null => {
    const re = new RegExp(
      `\\b${k}\\s*=\\s*(.+?)(?=\\s+(?:${KNOWN_KEYS.join('|')})\\s*=|$)`,
      'i',
    )
    const m = body.match(re)
    return m ? m[1].trim() : null
  }

  const from = get('from')
  const x = get('x')
  const yRaw = get('y')
  if (!from || !x || !yRaw) return null

  const y = yRaw.split(',').map(s => s.trim()).filter(Boolean)
  if (y.length === 0) return null

  const typeRaw = (get('type') || 'bar').toLowerCase()
  const type: ChartType =
    typeRaw === 'line' || typeRaw === 'pie' || typeRaw === 'area' ? typeRaw : 'bar'

  const series = get('series') || undefined
  return { type, from, x, y, series }
}

/**
 * splitAnswerSegments splits answer text into alternating text and chart
 * segments. Text segments are rendered as markdown (after renderDataTemplates);
 * chart segments are rendered as a visualization. A token that fails to parse
 * is kept as a text segment (graceful — the reader sees the raw token rather
 * than a blank or wrong chart).
 */
export function splitAnswerSegments(content: string): AnswerSegment[] {
  if (!content || content.indexOf('「') < 0) {
    return [{ kind: 'text', text: content }]
  }
  const out: AnswerSegment[] = []
  let last = 0
  for (const m of content.matchAll(CHART_TOKEN_RE)) {
    const start = m.index ?? 0
    const raw = m[0]
    const spec = parseChartToken(raw)
    if (!spec) continue // leave unparseable token inside its surrounding text
    if (start > last) out.push({ kind: 'text', text: content.slice(last, start) })
    out.push({ kind: 'chart', spec, raw })
    last = start + raw.length
  }
  if (last < content.length) out.push({ kind: 'text', text: content.slice(last) })
  if (out.length === 0) out.push({ kind: 'text', text: content })
  return out
}
