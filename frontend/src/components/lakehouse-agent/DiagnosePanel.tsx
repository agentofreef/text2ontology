'use client'

// Diagnose-first panel — the right column's 诊断 tab.
//
// Pure view: it renders the 分词 + recall that the agent ALREADY streamed via
// the 'recall' SSE event (passed down as `streamRecall`). It makes NO HTTP call
// of its own — the data rides the same stream as the answer. Embodies the thesis
// "every wrong answer has an address": it shows how the question was understood,
// with one-click jumps to the two fix levers.

import { useTranslations } from 'next-intl'
import { useStyleMode } from '@/lib/style-mode'
import { useRouter } from '@/i18n/navigation'
import { Box, Tags, Telescope } from 'lucide-react'
import { RecallResultView, type RecallResult } from '@/components/lakehouse-agent/RecallDiagnostics'
import type { GraphHighlight } from '@/components/ui/OntologyGraph'

export function DiagnosePanel({
  streamRecall,
  graphHighlight,
}: {
  streamRecall: { tokens: string[]; recall: RecallResult } | null
  graphHighlight: GraphHighlight | null
}) {
  const tw = useTranslations('workbench')
  const industrial = useStyleMode().mode === 'industrial'
  const router = useRouter()

  if (!streamRecall) {
    return (
      <div className="flex flex-1 min-h-0 flex-col items-center justify-center gap-2 p-6 text-center">
        <Telescope className="h-9 w-9 text-gray-300" aria-hidden="true" />
        <div className="text-sm font-medium text-gray-500">{tw('diag_empty_title')}</div>
        <div className="max-w-xs text-xs text-gray-400">{tw('diag_empty_hint')}</div>
      </div>
    )
  }

  const { tokens, recall } = streamRecall
  const td = recall?.tokenDetails || {}
  const involved = graphHighlight?.odNames ?? []
  const leverCls = `inline-flex items-center gap-1.5 rounded border px-2.5 py-1.5 text-xs transition-colors ${
    industrial ? 'border-ink text-ink hover:bg-ink hover:text-white' : 'border-border text-ink-muted hover:border-ink hover:text-ink'
  }`

  return (
    <div className="flex-1 min-h-0 space-y-4 overflow-y-auto bg-canvas p-4">
      {/* 分词 — straight from the agent's recall step (SSE), not a separate call. */}
      {tokens.length > 0 && (
        <div className="overflow-hidden rounded-md border border-border bg-white">
          <div className="border-b border-border-light bg-canvas-alt px-3 py-2 text-[11px] font-semibold uppercase tracking-wider text-ink-ghost">
            {tw('diag_tokens_label')}
          </div>
          <div className="flex flex-wrap gap-1.5 px-3 py-2">
            {tokens.map((t, i) => {
              const strong = (td[t]?.length ?? 0) > 0
              return (
                <span
                  key={i}
                  title={strong ? 'STRONG' : 'WEAK'}
                  className={`rounded-md border px-2 py-0.5 font-mono text-xs ${
                    strong ? 'border-ink bg-white text-ink' : 'border-border bg-canvas-alt text-ink-muted'
                  }`}
                >
                  {t}
                </span>
              )
            })}
          </div>
        </div>
      )}

      {/* Matched ontology objects — the "address", from the streamed lookup. */}
      {involved.length > 0 && (
        <div className="overflow-hidden rounded-md border border-border bg-white">
          <div className="border-b border-border-light bg-canvas-alt px-3 py-2 text-[11px] font-semibold uppercase tracking-wider text-ink-ghost">
            {tw('diag_involved_label')}
          </div>
          <div className="flex flex-wrap gap-1.5 px-3 py-2">
            {involved.map((name) => (
              <span key={name} className="rounded-md border border-ink bg-white px-2 py-0.5 font-mono text-xs text-ink">{name}</span>
            ))}
          </div>
        </div>
      )}

      {/* Fix levers — jump straight to where a wrong answer is fixed once. */}
      <div className="flex flex-wrap gap-2">
        <button onClick={() => router.push('/ontology/lakehouse-keyword-triage')} className={leverCls}>
          <Tags className="h-3.5 w-3.5" aria-hidden="true" />
          {tw('diag_fix_tokens')}
        </button>
        <button onClick={() => router.push('/ontology/lakehouse-objects')} className={leverCls}>
          <Box className="h-3.5 w-3.5" aria-hidden="true" />
          {tw('diag_fix_ontology')}
        </button>
      </div>

      {/* Full recall breakdown — token→hits, OD blocks, context — from the stream. */}
      <RecallResultView result={recall} />
    </div>
  )
}
