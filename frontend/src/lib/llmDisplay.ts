// llmDisplay — single source of truth for "how do we display an LLM config?"
//
// Logic: alias if user set one (the friendly "Label"), else fall back to
// `vendor / modelName` (the technical identity). Used by every role-binding
// UI, every "current active" card, and every dropdown so the rule stays
// consistent across pages.
//
// Edge cases:
//  - alias = "  " (whitespace) → treat as empty, fall back
//  - missing vendor → just show modelName (or vice-versa)
//  - undefined config → empty string (callers can chain with || 'fallback')
import type { LLMConfig } from '@/types/api'

export function llmDisplay(c?: LLMConfig | null): string {
  if (!c) return ''
  const alias = c.alias?.trim()
  if (alias) return alias
  if (c.vendor && c.modelName) return `${c.vendor} / ${c.modelName}`
  return c.modelName || c.vendor || ''
}

// llmSubtitle — the technical identity shown UNDER the alias (so users who
// only know the model name still recognise it). Returns empty when alias is
// not set (avoids duplicating the primary line).
export function llmSubtitle(c?: LLMConfig | null): string {
  if (!c) return ''
  const alias = c.alias?.trim()
  if (!alias) return ''
  return c.vendor && c.modelName ? `${c.vendor} / ${c.modelName}` : c.modelName || ''
}
