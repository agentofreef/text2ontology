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
