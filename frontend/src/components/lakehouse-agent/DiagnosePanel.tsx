'use client'

// Diagnose-first panel — the right column of the Agent workbench.
//
// Embodies the product thesis: "every wrong answer has an address." For the
// current question it surfaces (1) the matched ontology objects — free, already
// streamed via the SSE `involved` highlight; (2) on demand, the full
// tokenization → recall breakdown by reusing the same endpoint and view as the
// standalone Token-Recall page; (3) one-click jumps to the two fix levers
// (触发词/分词 and 本体). It calls NO new backend endpoint.

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { useMessage } from '@/lib/message'
import { useStyleMode } from '@/lib/style-mode'
import { useRouter } from '@/i18n/navigation'
import { api } from '@/lib/api'
import { RefreshCw, Box, Tags, Telescope } from 'lucide-react'
import {
  RecallResultView,
  type RecallResult,
  type TokenizeDebug,
  type VectorCandidate,
} from '@/components/lakehouse-agent/RecallDiagnostics'
import type { GraphHighlight } from '@/components/ui/OntologyGraph'

export function DiagnosePanel({
  projectId,
  question,
  graphHighlight,
}: {
  projectId?: string
  question: string
  graphHighlight: GraphHighlight | null
}) {
  const tw = useTranslations('workbench')
  const industrial = useStyleMode().mode === 'industrial'
  const msg = useMessage()
  const router = useRouter()
  const [loading, setLoading] = useState(false)
  const [tokens, setTokens] = useState<string[]>([])
  const [result, setResult] = useState<RecallResult | null>(null)
  const [debug, setDebug] = useState<TokenizeDebug | null>(null)
  const [vectorCandidates, setVectorCandidates] = useState<Record<string, VectorCandidate[]>>({})

  const runDiagnose = async () => {
    if (!question.trim()) { msg.error(tw('diag_no_question')); return }
    setLoading(true)
    try {
      const res = await api<{
        question: string
        tokens: string[]
        recall: RecallResult
        tokenizeDebug: TokenizeDebug
        vectorCandidates?: Record<string, VectorCandidate[]>
      }>(`/ontology/lakehouse-token-recall-tokenize?projectId=${projectId}`, {
        method: 'POST',
        body: { question: question.trim() },
      })
      setTokens(res.tokens || [])
      setResult(res.recall)
      setDebug(res.tokenizeDebug || null)
      setVectorCandidates(res.vectorCandidates || {})
    } catch { msg.error(tw('diag_fail')) }
    finally { setLoading(false) }
  }

  if (!question.trim()) {
    return (
      <div className="flex flex-1 min-h-0 flex-col items-center justify-center gap-2 p-6 text-center">
        <Telescope className="h-9 w-9 text-gray-300" aria-hidden="true" />
        <div className="text-sm font-medium text-gray-500">{tw('diag_empty_title')}</div>
        <div className="max-w-xs text-xs text-gray-400">{tw('diag_empty_hint')}</div>
      </div>
    )
  }

  const involved = graphHighlight?.odNames ?? []
  const leverCls = `inline-flex items-center gap-1.5 rounded border px-2.5 py-1.5 text-xs transition-colors ${
    industrial ? 'border-ink text-ink hover:bg-ink hover:text-white' : 'border-border text-ink-muted hover:border-ink hover:text-ink'
  }`

  return (
    <div className="flex-1 min-h-0 space-y-4 overflow-y-auto bg-canvas p-4">
      {/* Current question + re-diagnose */}
      <div className="overflow-hidden rounded-md border border-border bg-white">
        <div className="flex items-center justify-between gap-2 border-b border-border-light bg-canvas-alt px-3 py-2">
          <span className="text-[11px] font-semibold uppercase tracking-wider text-ink-ghost">{tw('diag_question_label')}</span>
          <button
            onClick={runDiagnose}
            disabled={loading}
            className={`inline-flex items-center gap-1 rounded border px-2 py-1 text-[11px] transition-colors disabled:opacity-50 ${
              industrial ? 'border-ink text-ink hover:bg-ink hover:text-white' : 'border-border text-ink-muted hover:border-ink hover:text-ink'
            }`}
          >
            <RefreshCw className={`h-3 w-3 ${loading ? 'animate-spin' : ''}`} aria-hidden="true" />
            {loading ? tw('diag_rerun_loading') : tw('diag_rerun')}
          </button>
        </div>
        <div className="px-3 py-2 text-sm text-ink">{question}</div>
      </div>

      {/* 分词 — how the current question was split into tokens. Shown as soon as
          a diagnose runs, before the deeper recall breakdown. */}
      {tokens.length > 0 && (
        <div className="overflow-hidden rounded-md border border-border bg-white">
          <div className="border-b border-border-light bg-canvas-alt px-3 py-2 text-[11px] font-semibold uppercase tracking-wider text-ink-ghost">
            {tw('diag_tokens_label')}
          </div>
          <div className="flex flex-wrap gap-1.5 px-3 py-2">
            {tokens.map((tk, i) => (
              <span key={i} className="rounded-md border border-ink bg-white px-2 py-0.5 font-mono text-xs text-ink">{tk}</span>
            ))}
          </div>
        </div>
      )}

      {/* Matched ontology objects — the "address", free from the SSE stream. */}
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

      {/* Fix levers — jump straight to where the wrong answer is fixed once. */}
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

      {/* Tokenize path + full recall breakdown (populated by 重新诊断). */}
      {debug && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-canvas-alt px-3 py-2">
          <span className="rounded border border-border bg-white px-1.5 py-0.5 font-mono text-[11px] text-ink">path: {debug.path}</span>
          <span className="text-[11px] text-ink-muted">{debug.reason}</span>
        </div>
      )}
      {result && (
        <RecallResultView result={result} vectorCandidates={vectorCandidates} />
      )}
    </div>
  )
}
