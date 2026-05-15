// Merge _split_en/*.json overlays onto zh.json (so untranslated namespaces fall back to zh)
// and write final messages/en.json.
import fs from 'node:fs'
import path from 'node:path'

const dir = path.dirname(new URL(import.meta.url).pathname)
const splitEn = path.join(dir, '_split_en')

// Start from zh.json (fallback for untranslated)
const out = JSON.parse(fs.readFileSync(path.join(dir, 'zh.json'), 'utf8'))

// Overlay each _split_en/<ns>.json
for (const f of fs.readdirSync(splitEn).sort()) {
  if (!f.endsWith('.json')) continue
  const ns = f.replace(/\.json$/, '')
  const enSubtree = JSON.parse(fs.readFileSync(path.join(splitEn, f), 'utf8'))
  out[ns] = enSubtree
  console.log(`overlaid: ${ns}`)
}

fs.writeFileSync(path.join(dir, 'en.json'), JSON.stringify(out, null, 2) + '\n', 'utf8')
console.log(`\nWrote en.json (${Object.keys(out).length} top-level namespaces)`)
