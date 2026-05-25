// Tests for answerChart's segmentation + ```chart fence normalisation.
// Run with:
//   node --experimental-strip-types --test \
//     src/components/lakehouse-agent/answerChart.test.ts

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { splitAnswerSegments } from './answerChart.ts'

test('splitAnswerSegments: full-width 「chart …」 token still recognised', () => {
  const text = '前文……「chart type=bar from=t1 x=city y=amount」后文。'
  const out = splitAnswerSegments(text)
  assert.equal(out.length, 3)
  assert.equal(out[0].kind, 'text')
  assert.equal(out[1].kind, 'chart')
  if (out[1].kind === 'chart') {
    assert.equal(out[1].spec.type, 'bar')
    assert.equal(out[1].spec.from, 't1')
    assert.deepEqual(out[1].spec.y, ['amount'])
  }
})

test('splitAnswerSegments: ```chart fence is normalised to a chart segment', () => {
  const text = `前文。

\`\`\`chart
chart type=line from=t1 x=MONTH y=Total_ORDER_QUANTITY series=GEO
\`\`\`

后文。`
  const out = splitAnswerSegments(text)
  const chart = out.find(s => s.kind === 'chart')
  assert.ok(chart, 'fenced ```chart``` must be parsed as a chart segment')
  if (chart && chart.kind === 'chart') {
    assert.equal(chart.spec.type, 'line')
    assert.equal(chart.spec.from, 't1')
    assert.equal(chart.spec.x, 'MONTH')
    assert.deepEqual(chart.spec.y, ['Total_ORDER_QUANTITY'])
    assert.equal(chart.spec.series, 'GEO')
  }
})

test('splitAnswerSegments: ``` fence WITHOUT chart language tag still works', () => {
  // Some LLMs omit the language tag entirely; as long as the body's first
  // word is `chart` the rewrite must still fire.
  const text = '看图：\n```\nchart type=pie from=t2 x=label y=value\n```\n完。'
  const out = splitAnswerSegments(text)
  const chart = out.find(s => s.kind === 'chart')
  assert.ok(chart, 'plain ``` fence with chart body must be parsed')
})

test('splitAnswerSegments: ``` fence whose body is NOT chart is left alone', () => {
  // Guard: a code sample inside a fence must not be hijacked into a chart.
  const text = '示例代码：\n```\nconst x = 1\n```\n完。'
  const out = splitAnswerSegments(text)
  // No chart segment should exist — the fence stays inside the text segment.
  assert.equal(out.filter(s => s.kind === 'chart').length, 0)
  assert.ok(out[0].kind === 'text' && out[0].text.includes('const x = 1'))
})

test('splitAnswerSegments: multi-line fenced chart with split key=value pairs', () => {
  // LLMs sometimes break each key=value onto its own line. Whitespace
  // collapse in the rewrite must fold these back into one line for the
  // tokeniser.
  const text = `\`\`\`chart
chart type=bar
from=t1
x=geo
y=qty
\`\`\``
  const out = splitAnswerSegments(text)
  const chart = out.find(s => s.kind === 'chart')
  assert.ok(chart, 'multi-line key=value chart body must still parse')
})
