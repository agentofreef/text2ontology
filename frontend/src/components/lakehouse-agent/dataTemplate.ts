// Data-template resolution (数据模板).
//
// The LLM never writes literal numbers into its answer — it writes references
// like 「sum(t1.amount)」 or 「t1」. Those references survive into the DB / thread
// verbatim; the concrete numbers exist ONLY here, recomputed at render time
// from the tool-result tables the page already holds.
//
// Reference grammar:
//   「tN」                                 → the whole table tN
//   「tN.col[i]」                           → a single cell: column col, row i
//                                            (0-based) of table tN
//   「<expr>」                              → an arithmetic expression, evaluated
//                                            to a scalar
//
// An <expr> is built from:
//   - aggregate atoms:  agg(tN.column)  or  agg(tN.column WHERE fcol=<val>)
//                       agg ∈ sum|avg|count|min|max
//                       <val> is either a literal ('上海') or a cell ref
//                       tN.col[i] — a cell ref pulls the filter value out of
//                       real data, so it is never a literal the LLM typed
//                       (and therefore never mistyped / hallucinated)
//   - numeric literals
//   - operators + - * /  and parentheses
//
// A bare 「sum(t1.amount)」 is just a one-atom expression. Derived numbers —
// ratios, percentages, differences — are expressions too, e.g.
//   「sum(t1.amt WHERE city='上海') / sum(t1.amt) * 100」
// so the LLM never has to compute (and never fabricates) a derived number.
//
// The WHERE form is row addressing; only a single equality filter is supported.
//
// Resolution is graceful: an unresolvable reference (unknown step, missing
// column, partial text mid-stream, malformed expression, divide-by-zero) is
// left as the raw 「…」 token so the reader sees something is off rather than
// getting a wrong or blank value.
//
// Resolution is graceful: an unresolvable reference (unknown step, missing
// column, partial text mid-stream) is left as the raw 「…」 token so the reader
// can see something is off rather than getting a wrong or blank value.

export interface StepResult {
  /** Stable step id assigned by agent-server, e.g. "t1". */
  stepId: string
  /** Parsed rows of the tool result table. */
  rows: Array<Record<string, unknown>>
}

type Agg = 'sum' | 'avg' | 'count' | 'min' | 'max'

const AGGS: Record<string, Agg> = {
  sum: 'sum', avg: 'avg', count: 'count', min: 'min', max: 'max',
}

// 「 … 」 — full-width brackets.
const REF_RE = /「([^」]+)」/g
// agg( … ) — the inner part is parsed separately by INNER_RE.
const SCALAR_RE = /^([a-zA-Z_]+)\(\s*(.+?)\s*\)$/
// tN.column  with an optional  WHERE fcol = 'value'  clause (= or ==).
const INNER_RE = /^(t\d+)\.(\S+?)(?:\s+WHERE\s+(\S+?)\s*==?\s*(.+?))?$/i
// tN
const TABLE_RE = /^(t\d+)$/
// tN.col[i] — one cell: column `col`, row i (0-based) of step tN.
const CELL_RE = /^(t\d+)\.([^[\]]+)\[(\d+)\]$/
// tN.col — a column reference WITHOUT a row index. Only resolvable when the
// table has exactly one row (a scalar result), in which case it means row 0.
// LLMs naturally write this for single-value tables (e.g. a total).
const BARE_CELL_RE = /^(t\d+)\.([^[\]]+)$/

// stripQuotes removes a single pair of surrounding ' or " quotes.
function stripQuotes(s: string): string {
  const t = s.trim()
  if (t.length >= 2 &&
    ((t[0] === "'" && t[t.length - 1] === "'") ||
      (t[0] === '"' && t[t.length - 1] === '"'))) {
    return t.slice(1, -1)
  }
  return t
}

/**
 * collectStepResults pulls every tagged tool result out of a turn's function
 * calls. `functionCalls` is the loosely-typed array the page holds; each entry
 * may carry result.step_id + result.execution_result (a JSON string of rows).
 */
export function collectStepResults(
  functionCalls: Array<{ result?: { step_id?: string; execution_result?: string } } | null | undefined> | undefined,
): Map<string, StepResult> {
  const out = new Map<string, StepResult>()
  if (!functionCalls) return out
  for (const fc of functionCalls) {
    if (!fc) continue
    const sid = fc.result?.step_id
    const raw = fc.result?.execution_result
    if (!sid || !raw) continue
    let rows: Array<Record<string, unknown>>
    try {
      const parsed = JSON.parse(raw)
      if (!Array.isArray(parsed)) continue
      rows = parsed
    } catch {
      continue
    }
    out.set(sid, { stepId: sid, rows })
  }
  return out
}

/** toNum coerces a cell value to a finite number, or null if not numeric. */
function toNum(v: unknown): number | null {
  if (typeof v === 'number') return Number.isFinite(v) ? v : null
  if (typeof v === 'string') {
    const cleaned = v.replace(/,/g, '').trim()
    if (cleaned === '') return null
    const n = Number(cleaned)
    return Number.isFinite(n) ? n : null
  }
  return null
}

/** formatNumber renders a computed scalar with thousands separators. */
export function formatNumber(n: number): string {
  if (!Number.isFinite(n)) return String(n)
  if (Number.isInteger(n)) return n.toLocaleString('en-US')
  return n.toLocaleString('en-US', { maximumFractionDigits: 2 })
}

/**
 * resolveColumn maps a requested column name to an actual column, tolerating
 * the column-name slips LLMs make (case, or "amount" vs "Total_amount"):
 *   1. exact match
 *   2. case-insensitive exact match (if unambiguous)
 *   3. unique case-insensitive substring match in either direction
 * Returns null when there is no match or the match is ambiguous — never guesses
 * between two candidates.
 */
function resolveColumn(cols: string[], requested: string): string | null {
  if (cols.includes(requested)) return requested
  const lower = requested.toLowerCase()
  const ci = cols.filter(c => c.toLowerCase() === lower)
  if (ci.length === 1) return ci[0]
  if (ci.length > 1) return null
  const sub = cols.filter(c => {
    const cl = c.toLowerCase()
    return cl.includes(lower) || lower.includes(cl)
  })
  return sub.length === 1 ? sub[0] : null
}

/** aggregate applies agg over the named column of rows. */
function aggregate(rows: Array<Record<string, unknown>>, agg: Agg, requestedColumn: string):
  { ok: true; value: number } | { ok: false; error: string } {
  if (rows.length === 0) return { ok: false, error: '结果为空' }
  const column = resolveColumn(Object.keys(rows[0]), requestedColumn)
  if (!column) {
    return { ok: false, error: `列 "${requestedColumn}" 不存在` }
  }
  if (agg === 'count') {
    let c = 0
    for (const r of rows) {
      const v = r[column]
      if (v !== null && v !== undefined && v !== '') c++
    }
    return { ok: true, value: c }
  }
  const nums: number[] = []
  for (const r of rows) {
    const n = toNum(r[column])
    if (n !== null) nums.push(n)
  }
  if (nums.length === 0) return { ok: false, error: `列 "${column}" 无数值` }
  switch (agg) {
    case 'sum': return { ok: true, value: nums.reduce((a, b) => a + b, 0) }
    case 'avg': return { ok: true, value: nums.reduce((a, b) => a + b, 0) / nums.length }
    case 'min': return { ok: true, value: Math.min(...nums) }
    case 'max': return { ok: true, value: Math.max(...nums) }
  }
}

/** rowsToMarkdownTable renders rows as a GitHub-flavoured markdown table. */
export function rowsToMarkdownTable(rows: Array<Record<string, unknown>>): string {
  if (rows.length === 0) return '_（空结果）_'
  const cols = Object.keys(rows[0])
  // 1×1 table → just the cell (a one-number "table" should read as a number).
  if (rows.length === 1 && cols.length === 1) {
    const v = rows[0][cols[0]]
    const n = toNum(v)
    return n !== null ? formatNumber(n) : String(v ?? '')
  }
  const esc = (v: unknown) => String(v ?? '').replace(/\|/g, '\\|').replace(/\n/g, ' ')
  const head = `| ${cols.map(esc).join(' | ')} |`
  const sep = `| ${cols.map(() => '---').join(' | ')} |`
  const body = rows.map(r => `| ${cols.map(c => {
    const n = toNum(r[c])
    return esc(n !== null ? formatNumber(n) : r[c])
  }).join(' | ')} |`)
  return [head, sep, ...body].join('\n')
}

/**
 * resolveCellRef resolves a single-cell reference `tN.col[i]` to its raw cell
 * value as a string, or null if it cannot be resolved. The value is returned
 * verbatim (no number formatting) so it is usable as a filter value — the
 * point of a cell ref is that the value comes from real data, never typed.
 */
function resolveCellRef(text: string, steps: Map<string, StepResult>): string | null {
  const m = text.trim().match(CELL_RE)
  if (!m) return null
  const step = steps.get(m[1])
  if (!step) return null
  const idx = Number(m[3])
  if (!Number.isInteger(idx) || idx < 0 || idx >= step.rows.length) return null
  const row = step.rows[idx]
  const column = resolveColumn(Object.keys(row), m[2].trim())
  if (!column) return null
  const v = row[column]
  return v === null || v === undefined ? null : String(v)
}

/**
 * resolveAtom resolves ONE expression atom to a number, or null. An atom is
 * one of three forms (checked in this order):
 *   - agg(tN.column [WHERE fcol=<val>])  — an aggregate
 *   - tN.column[i]                       — a single cell (row i)
 *   - tN.column                          — a column ref with no index, valid
 *                                          only on a single-row (scalar) table
 *
 * The last two let the LLM write natural cell arithmetic like
 *   (t2.amount[1] + t2.amount[3]) / t3.total * 100
 * instead of forcing every term through an agg(...).
 */
function resolveAtom(refText: string, steps: Map<string, StepResult>): number | null {
  const t = refText.trim()

  // Form 2: single cell  tN.col[i]
  if (CELL_RE.test(t)) {
    const cell = resolveCellRef(t, steps)
    return cell === null ? null : toNum(cell)
  }

  // Form 1: aggregate  agg(tN.col [WHERE ...])
  const scalar = t.match(SCALAR_RE)
  if (scalar && AGGS[scalar[1].toLowerCase()]) {
    return resolveAggAtom(scalar, steps)
  }

  // Form 3: bare column ref  tN.col  (single-row table → row 0)
  const bare = t.match(BARE_CELL_RE)
  if (bare) {
    const step = steps.get(bare[1])
    if (!step || step.rows.length !== 1) return null // ambiguous unless scalar
    const col = resolveColumn(Object.keys(step.rows[0]), bare[2].trim())
    if (!col) return null
    return toNum(step.rows[0][col])
  }

  return null
}

/** resolveAggAtom handles the agg(tN.col [WHERE fcol=val]) form. */
function resolveAggAtom(scalar: RegExpMatchArray, steps: Map<string, StepResult>): number | null {
  const agg = AGGS[scalar[1].toLowerCase()]
  if (!agg) return null
  const inner = scalar[2].match(INNER_RE)
  if (!inner) return null
  const stepId = inner[1]
  const column = inner[2].trim()
  const filterCol = inner[3]?.trim()
  // The filter value is either a cell ref (tN.col[i] — resolved from real
  // data) or a quoted literal. A cell ref that fails to resolve aborts the
  // whole reference (null) rather than degrading to a literal lookup.
  let filterVal: string | undefined
  if (inner[4] !== undefined) {
    const rawVal = inner[4].trim()
    if (CELL_RE.test(rawVal)) {
      const cell = resolveCellRef(rawVal, steps)
      if (cell === null) return null
      filterVal = cell
    } else {
      filterVal = stripQuotes(rawVal)
    }
  }

  const step = steps.get(stepId)
  if (!step) return null

  let rows = step.rows
  if (filterCol && filterVal !== undefined) {
    if (rows.length === 0) return null
    const fc = resolveColumn(Object.keys(rows[0]), filterCol)
    if (!fc) return null
    rows = rows.filter(r => String(r[fc] ?? '') === filterVal)
  }
  const res = aggregate(rows, agg, column)
  return res.ok ? res.value : null
}

// ── arithmetic expression evaluator ───────────────────────────────────────
// A safe (no eval) recursive-descent evaluator over: numbers, aggregate-atom
// references, + - * /, parentheses. Derived numbers (ratios/percentages) are
// expressions, so the LLM never computes them itself.

type Token =
  | { t: 'num'; v: number }
  | { t: 'ref'; v: string }
  | { t: 'op'; v: '+' | '-' | '*' | '/' }
  | { t: 'lp' }
  | { t: 'rp' }

/** tokenizeExpr splits an expression string into tokens, or null if malformed. */
function tokenizeExpr(s: string): Token[] | null {
  const toks: Token[] = []
  const n = s.length
  let i = 0
  while (i < n) {
    const c = s[i]
    if (c === ' ' || c === '\t' || c === '\n' || c === '\r' || c === '%') { i++; continue }
    if (c === '+' || c === '-' || c === '*' || c === '/') { toks.push({ t: 'op', v: c }); i++; continue }
    if (c === '(') { toks.push({ t: 'lp' }); i++; continue }
    if (c === ')') { toks.push({ t: 'rp' }); i++; continue }
    if ((c >= '0' && c <= '9') || c === '.') {
      let j = i
      while (j < n && ((s[j] >= '0' && s[j] <= '9') || s[j] === '.' || s[j] === ',')) j++
      const num = Number(s.slice(i, j).replace(/,/g, ''))
      if (!Number.isFinite(num)) return null
      toks.push({ t: 'num', v: num })
      i = j
      continue
    }
    // identifier → an atom in one of three forms:
    //   agg(...)        — word immediately followed by balanced parens
    //   tN.col[i]       — a cell ref
    //   tN.col          — a bare column ref (scalar table)
    if (/[a-zA-Z_]/.test(c)) {
      let j = i
      while (j < n && /[a-zA-Z0-9_]/.test(s[j])) j++ // the leading word (agg name or tN)
      let k = j
      while (k < n && (s[k] === ' ' || s[k] === '\t')) k++
      if (k < n && s[k] === '(') {
        // agg(...) — read to the matching close paren.
        let depth = 0
        let m = k
        for (; m < n; m++) {
          if (s[m] === '(') depth++
          else if (s[m] === ')') { depth--; if (depth === 0) { m++; break } }
        }
        if (depth !== 0) return null
        toks.push({ t: 'ref', v: s.slice(i, m).trim() })
        i = m
        continue
      }
      if (j < n && s[j] === '.') {
        // tN.col  or  tN.col[i] — read the column name up to a delimiter,
        // then an optional [index].
        let m = j + 1
        while (m < n && !/[\s+\-*/()[\]]/.test(s[m])) m++
        if (m < n && s[m] === '[') {
          const close = s.indexOf(']', m)
          if (close < 0) return null
          m = close + 1
        }
        toks.push({ t: 'ref', v: s.slice(i, m).trim() })
        i = m
        continue
      }
      return null // bare identifier — not a valid atom
    }
    return null // unknown character
  }
  return toks
}

/** evalExpr evaluates a token list to a finite number, or null. */
function evalExpr(toks: Token[], steps: Map<string, StepResult>): number | null {
  let pos = 0

  const parseFactor = (): number | null => {
    if (pos >= toks.length) return null
    const tk = toks[pos]
    if (tk.t === 'op' && tk.v === '-') { pos++; const f = parseFactor(); return f === null ? null : -f }
    if (tk.t === 'op' && tk.v === '+') { pos++; return parseFactor() }
    if (tk.t === 'num') { pos++; return tk.v }
    if (tk.t === 'ref') { pos++; return resolveAtom(tk.v, steps) }
    if (tk.t === 'lp') {
      pos++
      const e = parseExpr()
      if (e === null || pos >= toks.length || toks[pos].t !== 'rp') return null
      pos++
      return e
    }
    return null
  }
  const parseTerm = (): number | null => {
    let left = parseFactor()
    if (left === null) return null
    while (pos < toks.length && toks[pos].t === 'op') {
      const op = toks[pos] as { t: 'op'; v: string }
      if (op.v !== '*' && op.v !== '/') break
      pos++
      const right = parseFactor()
      if (right === null) return null
      left = op.v === '*' ? left * right : left / right
    }
    return left
  }
  function parseExpr(): number | null {
    let left = parseTerm()
    if (left === null) return null
    while (pos < toks.length && toks[pos].t === 'op') {
      const op = toks[pos] as { t: 'op'; v: string }
      if (op.v !== '+' && op.v !== '-') break
      pos++
      const right = parseTerm()
      if (right === null) return null
      left = op.v === '+' ? left + right : left - right
    }
    return left
  }

  const result = parseExpr()
  if (result === null || pos !== toks.length) return null
  if (!Number.isFinite(result)) return null // divide-by-zero etc.
  return result
}

/**
 * resolveReference resolves one inner reference string (without the 「」).
 * Returns the rendered replacement string, or null if it cannot be resolved
 * (caller should then keep the raw token).
 */
export function resolveReference(inner: string, steps: Map<string, StepResult>): string | null {
  const trimmed = inner.trim()

  // whole-table form
  const table = trimmed.match(TABLE_RE)
  if (table) {
    const step = steps.get(table[1])
    return step ? rowsToMarkdownTable(step.rows) : null
  }

  // single-cell form  tN.col[i]
  if (CELL_RE.test(trimmed)) {
    const cell = resolveCellRef(trimmed, steps)
    if (cell === null) return null
    const n = toNum(cell)
    return n !== null ? formatNumber(n) : cell
  }

  // arithmetic expression (a single agg(...) atom is a one-term expression)
  const toks = tokenizeExpr(trimmed)
  if (!toks || toks.length === 0) return null
  const val = evalExpr(toks, steps)
  return val === null ? null : formatNumber(val)
}

/**
 * renderDataTemplates rewrites every 「…」 reference in `text` into its resolved
 * value (scalar number or markdown table). Unresolvable references are left
 * verbatim. The output is markdown, ready for the existing markdown renderer.
 */
export function renderDataTemplates(
  text: string,
  functionCalls: Array<{ result?: { step_id?: string; execution_result?: string } } | null | undefined> | undefined,
): string {
  if (!text || text.indexOf('「') < 0) return text
  const steps = collectStepResults(functionCalls)
  if (steps.size === 0) return text
  return text.replace(REF_RE, (raw, inner) => {
    const resolved = resolveReference(inner, steps)
    return resolved ?? raw
  })
}
