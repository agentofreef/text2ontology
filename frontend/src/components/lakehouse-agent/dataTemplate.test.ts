// Tests for the data-template resolver. No bundled test runner in this repo —
// run with Node's built-in test runner + type stripping:
//
//   node --experimental-strip-types --test \
//     src/components/lakehouse-agent/dataTemplate.test.ts
//
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  collectStepResults,
  resolveReference,
  renderDataTemplates,
  rowsToMarkdownTable,
  formatNumber,
} from './dataTemplate.ts'

const fcs = [
  {
    result: {
      step_id: 't1',
      execution_result: JSON.stringify([
        { city: '上海', amount: 2735766 },
        { city: '北京', amount: 1973348 },
        { city: '成都', amount: 872018 },
      ]),
    },
  },
  { result: { content: 'no table here' } }, // untagged tool — ignored
]

test('collectStepResults indexes tagged results only', () => {
  const m = collectStepResults(fcs)
  assert.equal(m.size, 1)
  assert.equal(m.get('t1')!.rows.length, 3)
})

test('resolveReference: sum', () => {
  const m = collectStepResults(fcs)
  assert.equal(resolveReference('sum(t1.amount)', m), '5,581,132')
})

test('resolveReference: avg / min / max / count', () => {
  const m = collectStepResults(fcs)
  assert.equal(resolveReference('min(t1.amount)', m), '872,018')
  assert.equal(resolveReference('max(t1.amount)', m), '2,735,766')
  assert.equal(resolveReference('count(t1.amount)', m), '3')
  assert.equal(resolveReference('avg(t1.amount)', m), '1,860,377.33')
})

test('resolveReference: WHERE equality filter', () => {
  const m = collectStepResults(fcs)
  assert.equal(resolveReference("sum(t1.amount WHERE city='上海')", m), '2,735,766')
  assert.equal(resolveReference('sum(t1.amount WHERE city="北京")', m), '1,973,348')
  assert.equal(resolveReference("count(t1.amount WHERE city='成都')", m), '1')
  // == also accepted
  assert.equal(resolveReference("max(t1.amount WHERE city=='上海')", m), '2,735,766')
})

test('resolveReference: WHERE on unknown filter column → null', () => {
  const m = collectStepResults(fcs)
  assert.equal(resolveReference("sum(t1.amount WHERE region='x')", m), null)
})

// ── multi-equality WHERE (AND) ───────────────────────────────────────────
// Mirrors the real e2e bug: agent emits
//   「sum(t1.qty WHERE GEO=t1.GEO[i] AND MONTH=t1.MONTH[j])」
// for a row-locator over two grouping dimensions. The single-equality form
// can't address one cell of a (GEO × MONTH) cross — only the AND-conjoined
// form can.
const andFcs = [
  {
    result: {
      step_id: 't1',
      execution_result: JSON.stringify([
        { GEO: 'PRC',  MONTH: '2026-01', qty: 100 },
        { GEO: 'PRC',  MONTH: '2026-02', qty: 50 },
        { GEO: 'EMEA', MONTH: '2026-01', qty: 80 },
        { GEO: 'EMEA', MONTH: '2026-02', qty: 30 },
      ]),
    },
  },
]

test('resolveReference: WHERE A AND B (two cell refs)', () => {
  const m = collectStepResults(andFcs)
  // PRC × 2026-02 → 50
  assert.equal(
    resolveReference('sum(t1.qty WHERE GEO=t1.GEO[1] AND MONTH=t1.MONTH[1])', m),
    '50',
  )
})

test('resolveReference: WHERE A AND B (literals)', () => {
  const m = collectStepResults(andFcs)
  assert.equal(
    resolveReference("sum(t1.qty WHERE GEO='EMEA' AND MONTH='2026-01')", m),
    '80',
  )
})

test('resolveReference: WHERE A AND B AND C — ≥3 equalities still work', () => {
  const triFcs = [{ result: { step_id: 't1', execution_result: JSON.stringify([
    { a: '1', b: '1', c: '1', v: 10 },
    { a: '1', b: '1', c: '2', v: 99 },
    { a: '1', b: '2', c: '1', v: 7 },
  ]) } }]
  const m = collectStepResults(triFcs)
  assert.equal(resolveReference("sum(t1.v WHERE a='1' AND b='1' AND c='1')", m), '10')
})

test('resolveReference: case-insensitive AND splitter', () => {
  const m = collectStepResults(andFcs)
  assert.equal(
    resolveReference("sum(t1.qty WHERE GEO='PRC' and MONTH='2026-01')", m),
    '100',
  )
})

test('resolveReference: AND with one unknown filter col → null', () => {
  const m = collectStepResults(andFcs)
  assert.equal(
    resolveReference("sum(t1.qty WHERE GEO='PRC' AND ZONE='APAC')", m),
    null,
  )
})

test('resolveReference: AND with broken cell ref aborts whole expr', () => {
  const m = collectStepResults(andFcs)
  // out-of-range index on the second clause's cell ref → null, not silent skip
  assert.equal(
    resolveReference('sum(t1.qty WHERE GEO=t1.GEO[0] AND MONTH=t1.MONTH[99])', m),
    null,
  )
})

test('resolveReference: AND inside arithmetic expression', () => {
  const m = collectStepResults(andFcs)
  // (PRC,Jan)=100 + (EMEA,Jan)=80 = 180
  assert.equal(
    resolveReference(
      "sum(t1.qty WHERE GEO='PRC' AND MONTH='2026-01') + sum(t1.qty WHERE GEO='EMEA' AND MONTH='2026-01')",
      m,
    ),
    '180',
  )
})

test('renderDataTemplates: WHERE refs substitute per row', () => {
  const text = '上海「sum(t1.amount WHERE city=\'上海\')」、北京「sum(t1.amount WHERE city=\'北京\')」'
  const out = renderDataTemplates(text, fcs)
  assert.ok(out.includes('上海2,735,766'))
  assert.ok(out.includes('北京1,973,348'))
})

test('resolveReference: arithmetic expression — percentage', () => {
  const m = collectStepResults(fcs)
  // 上海 2735766 / total 5581132 * 100 = 49.02...
  const out = resolveReference("sum(t1.amount WHERE city='上海') / sum(t1.amount) * 100", m)
  assert.equal(out, '49.02')
})

test('resolveReference: arithmetic — literals, parens, precedence', () => {
  const m = collectStepResults(fcs)
  assert.equal(resolveReference('sum(t1.amount) + 1000', m), '5,582,132')
  assert.equal(resolveReference('max(t1.amount) - min(t1.amount)', m), '1,863,748')
  assert.equal(resolveReference('(sum(t1.amount) - min(t1.amount)) / 2', m), '2,354,557')
})

test('resolveReference: arithmetic — unresolvable atom fails whole expr', () => {
  const m = collectStepResults(fcs)
  assert.equal(resolveReference('sum(t9.amount) / sum(t1.amount)', m), null)
})

test('resolveReference: arithmetic — divide by zero → null', () => {
  const zero = [{ result: { step_id: 't1', execution_result: '[{"x":0},{"x":0}]' } }]
  const m = collectStepResults(zero)
  assert.equal(resolveReference('sum(t1.x) / sum(t1.x)', m), null) // 0/0 = NaN
})

test('resolveReference: malformed expression → null', () => {
  const m = collectStepResults(fcs)
  assert.equal(resolveReference('sum(t1.amount) +', m), null)
  assert.equal(resolveReference('sum(t1.amount) sum(t1.amount)', m), null)
})

test('resolveReference: fuzzy column match tolerates LLM name slips', () => {
  // real e2e case: column is Total_amount, LLM wrote .amount
  const fz = [{ result: { step_id: 't3', execution_result: '[{"Total_amount":"27218258.85","city":"上海"}]' } }]
  const m = collectStepResults(fz)
  assert.equal(resolveReference('sum(t3.amount)', m), '27,218,258.85')          // substring
  assert.equal(resolveReference('sum(t3.AMOUNT)', m), '27,218,258.85')          // case
  assert.equal(resolveReference("sum(t3.amount WHERE CITY='上海')", m), '27,218,258.85') // filter col case
})

test('resolveReference: ambiguous fuzzy match → null (never guesses)', () => {
  // two columns both contain "amount" — must not pick one
  const amb = [{ result: { step_id: 't1', execution_result: '[{"net_amount":1,"gross_amount":2}]' } }]
  const m = collectStepResults(amb)
  assert.equal(resolveReference('sum(t1.amount)', m), null)
})

test('resolveReference: whole table → markdown', () => {
  const m = collectStepResults(fcs)
  const out = resolveReference('t1', m)!
  assert.ok(out.includes('| city | amount |'))
  assert.ok(out.includes('上海'))
  assert.ok(out.includes('2,735,766'))
})

test('resolveReference: unresolvable → null', () => {
  const m = collectStepResults(fcs)
  assert.equal(resolveReference('sum(t9.amount)', m), null) // unknown step
  assert.equal(resolveReference('sum(t1.ghost)', m), null)  // unknown column
  assert.equal(resolveReference('garbage', m), null)        // not a reference
})

test('renderDataTemplates: scalar + table substitution', () => {
  const text = '总营收「sum(t1.amount)」元。分布：「t1」'
  const out = renderDataTemplates(text, fcs)
  assert.ok(out.includes('总营收5,581,132元'))
  assert.ok(out.includes('| city | amount |'))
  assert.ok(!out.includes('「sum(t1.amount)」'))
})

test('renderDataTemplates: unresolvable refs left verbatim', () => {
  const text = '坏引用「sum(t9.x)」保持原样'
  assert.equal(renderDataTemplates(text, fcs), text)
})

test('renderDataTemplates: no refs → unchanged', () => {
  assert.equal(renderDataTemplates('普通文本无引用', fcs), '普通文本无引用')
})

// ── cell-ref atoms in expressions + bare (index-less) column refs ──────────
// Mirrors a real e2e failure: a multi-row channel table (t2) plus a
// single-row total (t3), with the LLM writing cell arithmetic and a bare
// scalar ref instead of forcing everything through agg(...).
const cellFcs = [
  {
    result: {
      step_id: 't2',
      execution_result: JSON.stringify([
        { channel: '堂食',   Total_amount: 17139065.69 },
        { channel: '美团',   Total_amount: 5051860.88 },
        { channel: '自营app', Total_amount: 0 },
        { channel: '饿了么', Total_amount: 5027332.28 },
      ]),
    },
  },
  { result: { step_id: 't3', execution_result: JSON.stringify([{ Total_amount: 27218258.85 }]) } },
]

test('resolveReference: standalone bare cell ref on single-row table', () => {
  const m = collectStepResults(cellFcs)
  assert.equal(resolveReference('t3.Total_amount', m), '27,218,258.85')
})

test('resolveReference: bare cell ref on multi-row table → null (ambiguous)', () => {
  const m = collectStepResults(cellFcs)
  assert.equal(resolveReference('t2.Total_amount', m), null)
})

test('resolveReference: cell-ref arithmetic (sum of two rows)', () => {
  const m = collectStepResults(cellFcs)
  // 美团[1] + 饿了么[3] = 5,051,860.88 + 5,027,332.28 = 10,079,193.16
  assert.equal(resolveReference('t2.Total_amount[1] + t2.Total_amount[3]', m), '10,079,193.16')
})

test('resolveReference: cell-ref ratio with bare scalar denominator', () => {
  const m = collectStepResults(cellFcs)
  // (5,051,860.88 + 5,027,332.28) / 27,218,258.85 * 100 = 37.03...
  assert.equal(
    resolveReference('(t2.Total_amount[1] + t2.Total_amount[3]) / t3.Total_amount * 100', m),
    '37.03',
  )
})

test('resolveReference: cell-ref atom out-of-range row → null', () => {
  const m = collectStepResults(cellFcs)
  assert.equal(resolveReference('t2.Total_amount[9] + t2.Total_amount[1]', m), null)
})

test('rowsToMarkdownTable: 1x1 result renders as a bare number', () => {
  assert.equal(rowsToMarkdownTable([{ total: 8380820 }]), '8,380,820')
})

test('formatNumber: thousands + decimals', () => {
  assert.equal(formatNumber(8380820), '8,380,820')
  assert.equal(formatNumber(1234.5), '1,234.5')
})

// ── strip duplicate hand-typed tables ────────────────────────────────────
// LLM is told never to transcribe cells by hand. When it relapses, the user
// sees both the LLM's hand-typed table (numbers possibly hallucinated) AND
// the canonical 「tN」-rendered table. The strip pass deletes the former.

test('renderDataTemplates: strips hand-typed table when a 「tN」 exists', () => {
  const text = `观察到：

| city | amount |
| --- | --- |
| 上海 | 1 |
| 北京 | 2 |
| 成都 | 3 |

完整数据：「t1」`
  const out = renderDataTemplates(text, fcs)
  // Hand-typed values must be gone — those numbers (1/2/3) are not real.
  assert.ok(!out.includes('| 上海 | 1 |'))
  assert.ok(!out.includes('| 北京 | 2 |'))
  // The 「t1」 expansion still produced the real table with real numbers.
  assert.ok(out.includes('上海'))
  assert.ok(out.includes('2,735,766'))
})

test('renderDataTemplates: keeps hand-typed table when no resolvable 「tN」', () => {
  // No 「tN」 reference at all → strip doesn't fire (the LLM may have a
  // legitimate reason to write a literal table).
  const text = `分类表：

| key | val |
| --- | --- |
| a | 1 |

完。`
  const out = renderDataTemplates(text, fcs)
  assert.ok(out.includes('| a | 1 |'))
})

test('renderDataTemplates: strips multiple hand-typed tables', () => {
  const text = `第一份：

| x | y |
| --- | --- |
| 1 | 2 |

第二份：

| u | v |
| --- | --- |
| 7 | 8 |

真实结果：「t1」`
  const out = renderDataTemplates(text, fcs)
  assert.ok(!out.includes('| 1 | 2 |'))
  assert.ok(!out.includes('| 7 | 8 |'))
  // canonical from 「t1」 still present
  assert.ok(out.includes('2,735,766'))
})

test('renderDataTemplates: ignores stray pipe-containing sentences', () => {
  // Not a markdown table (no `---` separator line) — must not be stripped.
  const text = `结果：a | b | c 然后……「t1」`
  const out = renderDataTemplates(text, fcs)
  assert.ok(out.includes('a | b | c'))
})

test('renderDataTemplates: unresolvable 「tN」 disarms the strip', () => {
  // Hand-typed table stays because 「t9」 (unknown) can't render anything.
  const text = `| k | v |
| --- | --- |
| only | table |

「t9」`
  const out = renderDataTemplates(text, fcs)
  assert.ok(out.includes('| only | table |'))
})
