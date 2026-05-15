// AI-translate messages/zh.json → messages/en.json via DeepSeek-V4-Flash on SiliconFlow.
// Chunked + retry: large namespaces are broken into ≤30 leaf-key chunks
// (preserving nested structure). Each chunk has 3 retries with exponential backoff.

import fs from 'node:fs'
import path from 'node:path'

const API_KEY = process.env.DEEPSEEK_API_KEY
const API_URL = 'https://api.siliconflow.cn/v1/chat/completions'
const MODEL = 'deepseek-ai/DeepSeek-V4-Flash'
const CHUNK_KEYS = 30
const MAX_RETRIES = 3
const REQUEST_TIMEOUT_MS = 90000

if (!API_KEY) {
  console.error('Set DEEPSEEK_API_KEY env var')
  process.exit(1)
}

const GLOSSARY = [
  'Glossary (keep these terms in English):',
  'Ontology, Smartquery, Intent, OD, Object Type, Property, Link, Recall, Lakehouse,',
  'Knowledge, Learned Fact, Causality, Workspace, Validator, Builder mode, Analyst mode,',
  'Token, Trigger Word, Keyword, Triage, Alias, MCP, Project, Dataset, Suite, Run Case,',
  'Job, Finding, Decision, Open Question, Draft Bundle, Ship, Activate, Propose, Mark.',
  '',
  'Fixed zh→en mappings:',
  '数据湖仓 → Lakehouse | 本体 → Ontology | 对象 → Object Type | 属性 → Property',
  '关系/链接 → Link | 意图 → Intent | 触发词 → Trigger Word | 别名 → Alias',
  '验证 → Validate | 提议 → Propose | 启用 → Activate | 标记 → Mark',
  '召回 → Recall | 知识 → Knowledge | 因果 → Causality | 工作区 → Workspace',
  '校验器 → Validator | 草稿 → Draft | 运行 → Run | 加载中 → Loading',
  '保存 → Save | 取消 → Cancel | 删除 → Delete | 编辑 → Edit | 搜索 → Search',
].join('\n')

const dir = path.dirname(new URL(import.meta.url).pathname)
const zh = JSON.parse(fs.readFileSync(path.join(dir, 'zh.json'), 'utf8'))

// Walk leaves and return flat list of {keyPath, value}
function flatten(obj, prefix = '') {
  const out = []
  for (const k of Object.keys(obj)) {
    const v = obj[k]
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      out.push(...flatten(v, prefix + k + '.'))
    } else {
      out.push({ path: prefix + k, value: v })
    }
  }
  return out
}

// Inverse: build nested object from flat {path, value} list
function unflatten(flat) {
  const root = {}
  for (const { path: p, value } of flat) {
    const parts = p.split('.')
    let cur = root
    for (let i = 0; i < parts.length - 1; i++) {
      if (typeof cur[parts[i]] !== 'object' || cur[parts[i]] === null) cur[parts[i]] = {}
      cur = cur[parts[i]]
    }
    cur[parts[parts.length - 1]] = value
  }
  return root
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms))
}

async function callDeepSeek(messages, attempt = 1) {
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS)
  try {
    const res = await fetch(API_URL, {
      method: 'POST',
      headers: { Authorization: `Bearer ${API_KEY}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        model: MODEL,
        messages,
        temperature: 0,
        max_tokens: 16384,
        response_format: { type: 'json_object' },
      }),
      signal: ctrl.signal,
    })
    if (!res.ok) {
      const text = await res.text()
      throw new Error(`HTTP ${res.status}: ${text.slice(0, 300)}`)
    }
    const data = await res.json()
    const content = data.choices?.[0]?.message?.content
    if (!content) throw new Error(`Empty content: ${JSON.stringify(data).slice(0, 300)}`)
    return content
  } catch (e) {
    if (attempt < MAX_RETRIES) {
      const wait = 1500 * Math.pow(2, attempt - 1)
      console.log(`    retry ${attempt}/${MAX_RETRIES - 1} after ${wait}ms (${e.message.slice(0, 80)})`)
      await sleep(wait)
      return callDeepSeek(messages, attempt + 1)
    }
    throw e
  } finally {
    clearTimeout(timer)
  }
}

async function translateChunk(namespace, chunk) {
  // Build a flat dict { "key.subkey": "中文" }
  const inputDict = {}
  for (const { path: p, value } of chunk) inputDict[p] = value

  const messages = [
    {
      role: 'system',
      content: `You are a professional UI translator for a Chinese-to-English data analytics platform.

CRITICAL OUTPUT RULES (violating any one rejects the response):
1. Output a SINGLE JSON object. Every input key MUST appear in output, with same name.
2. Translate only the values (Chinese → concise English).
3. Preserve ICU placeholders verbatim: {name}, {count}, {error}, plus {var, plural, =0 {...} other {...}} syntax.
4. Preserve \\n newlines, punctuation, and any special markers like ▶ ←.
5. Output JSON ONLY (no markdown fences, no commentary).

${GLOSSARY}`,
    },
    {
      role: 'user',
      content: `Translate these Chinese UI strings to English. Namespace: "${namespace}". Output a JSON object with the same dotted keys.\n\n${JSON.stringify(inputDict, null, 2)}`,
    },
  ]

  const content = await callDeepSeek(messages)
  let parsed
  let body = content.trim()
  if (body.startsWith('```')) body = body.replace(/^```(?:json)?\s*/, '').replace(/```\s*$/, '').trim()
  try {
    parsed = JSON.parse(body)
  } catch (e) {
    throw new Error(`JSON parse: ${e.message}\nHead: ${body.slice(0, 200)}`)
  }

  // Validate every input key is in output
  const inputKeys = Object.keys(inputDict).sort()
  const outputKeys = Object.keys(parsed).sort()
  const missing = inputKeys.filter((k) => !outputKeys.includes(k))
  if (missing.length) {
    throw new Error(`Missing keys (${missing.length}): ${missing.slice(0, 5).join(', ')}`)
  }

  return inputKeys.map((p) => ({ path: p, value: String(parsed[p]) }))
}

async function translateNamespace(namespace, subtree) {
  const flat = flatten(subtree)
  const chunks = []
  for (let i = 0; i < flat.length; i += CHUNK_KEYS) chunks.push(flat.slice(i, i + CHUNK_KEYS))

  const results = []
  for (let i = 0; i < chunks.length; i++) {
    process.stdout.write(`    chunk ${i + 1}/${chunks.length} (${chunks[i].length} keys) ... `)
    const t0 = Date.now()
    try {
      const out = await translateChunk(namespace, chunks[i])
      results.push(...out)
      console.log(`${((Date.now() - t0) / 1000).toFixed(1)}s ✓`)
    } catch (e) {
      console.log(`FAIL: ${e.message.slice(0, 100)}`)
      // Keep zh values for this chunk so build doesn't break
      results.push(...chunks[i])
    }
  }

  return unflatten(results)
}

const en = {}
const namespaces = Object.keys(zh)
console.log(`Translating ${namespaces.length} namespaces (chunked ≤${CHUNK_KEYS} keys, ${MAX_RETRIES} retries):`)

for (const ns of namespaces) {
  const total = flatten(zh[ns]).length
  console.log(`\n  ${ns} (${total} leaf keys)`)
  en[ns] = await translateNamespace(ns, zh[ns])
}

const out = path.join(dir, 'en.json')
fs.writeFileSync(out, JSON.stringify(en, null, 2) + '\n', 'utf8')
console.log(`\nWrote ${out}`)
