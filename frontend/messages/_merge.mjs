// Merge all _agentN_zh.json fragments + existing zh.json (Phase 1 seed)
// into final frontend/messages/zh.json, then mirror to en.json.
// Phase 4 will re-translate en.json values via AI; for now en mirrors zh (so
// build/runtime works in both locales; en pages just render Chinese until P4).
import fs from 'node:fs'
import path from 'node:path'

const dir = path.dirname(new URL(import.meta.url).pathname)

function deepMerge(target, src) {
  for (const key of Object.keys(src)) {
    const sv = src[key]
    const tv = target[key]
    if (sv && typeof sv === 'object' && !Array.isArray(sv) && tv && typeof tv === 'object' && !Array.isArray(tv)) {
      deepMerge(tv, sv)
    } else {
      target[key] = sv
    }
  }
  return target
}

const fragments = fs
  .readdirSync(dir)
  .filter((f) => /^_agent.*_zh\.json$/.test(f))
  .sort()

console.log('Merging fragments:', fragments)

const existing = JSON.parse(fs.readFileSync(path.join(dir, 'zh.json'), 'utf8'))
const merged = { ...existing }

for (const f of fragments) {
  const frag = JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'))
  deepMerge(merged, frag)
}

fs.writeFileSync(path.join(dir, 'zh.json'), JSON.stringify(merged, null, 2) + '\n', 'utf8')
fs.writeFileSync(path.join(dir, 'en.json'), JSON.stringify(merged, null, 2) + '\n', 'utf8')

// Count keys
function countKeys(obj, prefix = '') {
  const keys = []
  for (const k of Object.keys(obj)) {
    const v = obj[k]
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      keys.push(...countKeys(v, prefix + k + '.'))
    } else {
      keys.push(prefix + k)
    }
  }
  return keys
}

const allKeys = countKeys(merged)
console.log(`Total keys merged into zh.json + en.json (mirror): ${allKeys.length}`)
console.log(`Top-level namespaces: ${Object.keys(merged).sort().join(', ')}`)
