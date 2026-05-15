import zh from '../../messages/zh.json'
import en from '../../messages/en.json'
import { getCurrentLocale } from './api'

const dicts: Record<string, Record<string, unknown>> = { zh, en }

export function tStatic(key: string, vars?: Record<string, string | number>): string {
  const locale = getCurrentLocale()
  const parts = key.split('.')
  let cur: unknown = dicts[locale]
  for (const p of parts) {
    if (cur && typeof cur === 'object' && p in (cur as object)) {
      cur = (cur as Record<string, unknown>)[p]
    } else {
      return key
    }
  }
  if (typeof cur !== 'string') return key
  if (vars) {
    return cur.replace(/\{(\w+)\}/g, (_, name) => String(vars[name] ?? `{${name}}`))
  }
  return cur
}
