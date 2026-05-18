'use client'

// PlanGraph renders the step DAG of a composite-Intent execution.
// Data source: LakehouseResult.planTrace, surfaced by agent-server as
// fc.result.plan_trace on a smartquery tool card.
//
// Spec: .omc/specs/plan-mode-composite-intent.md §3.5 + plan-analysis-dimensions.md.
// The point of this view is to make the "system's reasoning" visible — a plan
// is a bounded, deterministic, addressable thing, and the user should see the
// step chain, not just the final number.

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { Check, X, CircleSlash, Circle, ChevronDown, Star } from 'lucide-react'

export interface StepTrace {
  id: string
  layer: number
  od: string
  status: 'success' | 'empty' | 'empty_upstream' | 'failed' | 'pending'
  rowCount: number
  durationMs: number
  error?: string
  sql?: string
  ontologySQL?: string
  dependsOn?: string[]
  isOutput?: boolean
}

export interface PlanTrace {
  steps: StepTrace[]
  output: string
}

const STATUS_STYLE: Record<
  StepTrace['status'],
  { ring: string; bg: string; text: string; icon: typeof Check }
> = {
  success:        { ring: 'border-emerald-300', bg: 'bg-emerald-50',  text: 'text-emerald-700', icon: Check },
  empty:          { ring: 'border-amber-300',   bg: 'bg-amber-50',    text: 'text-amber-700',   icon: CircleSlash },
  empty_upstream: { ring: 'border-gray-200',    bg: 'bg-gray-50',     text: 'text-gray-400',    icon: CircleSlash },
  failed:         { ring: 'border-red-300',     bg: 'bg-red-50',      text: 'text-red-700',     icon: X },
  pending:        { ring: 'border-gray-200 border-dashed', bg: 'bg-white', text: 'text-gray-400', icon: Circle },
}

function StepCard({ step, t }: { step: StepTrace; t: ReturnType<typeof useTranslations> }) {
  const [open, setOpen] = useState(false)
  const s = STATUS_STYLE[step.status] ?? STATUS_STYLE.pending
  const Icon = s.icon
  const hasSQL = !!(step.sql || step.ontologySQL)

  return (
    <div className={`rounded-md border ${s.ring} ${s.bg} px-2.5 py-1.5 min-w-[150px]`}>
      <div className="flex items-center gap-1.5">
        <Icon className={`w-3.5 h-3.5 shrink-0 ${s.text}`} />
        <span className="font-semibold text-[11px] text-gray-800">{step.od}</span>
        {step.isOutput && (
          <span className="inline-flex items-center gap-0.5 text-[9px] font-semibold text-indigo-600">
            <Star className="w-2.5 h-2.5 fill-indigo-500 text-indigo-500" />
            {t('plan_graph.output')}
          </span>
        )}
      </div>
      <div className="mt-0.5 text-[10px] text-gray-500 font-mono">{step.id}</div>
      <div className="mt-1 flex items-center gap-2 text-[10px]">
        <span className={s.text}>
          {step.status === 'success'
            ? t('plan_graph.rows', { n: step.rowCount })
            : t(`plan_graph.status_${step.status}`)}
        </span>
        <span className="text-gray-400">{step.durationMs}ms</span>
      </div>
      {step.error && (
        <div className="mt-1 text-[10px] text-red-600 break-words">{step.error}</div>
      )}
      {hasSQL && (
        <button
          onClick={() => setOpen(o => !o)}
          className="mt-1 flex items-center gap-0.5 text-[9px] text-gray-400 hover:text-gray-600 uppercase tracking-wider"
        >
          <ChevronDown className={`w-2.5 h-2.5 transition-transform ${open ? 'rotate-180' : ''}`} />
          SQL
        </button>
      )}
      {open && hasSQL && (
        <pre className="mt-1 bg-gray-900 rounded px-2 py-1 font-mono text-[10px] text-blue-300 whitespace-pre-wrap max-h-40 overflow-y-auto">
          {step.sql || step.ontologySQL}
        </pre>
      )}
    </div>
  )
}

export function PlanGraph({ trace }: { trace: PlanTrace }) {
  const t = useTranslations('agent.main')
  if (!trace?.steps?.length) return null

  // Group steps by layer; layers run top→bottom, steps within a layer ran
  // in parallel so they sit side by side.
  const maxLayer = trace.steps.reduce((m, s) => Math.max(m, s.layer), 0)
  const layers: StepTrace[][] = Array.from({ length: maxLayer + 1 }, () => [])
  for (const s of trace.steps) layers[s.layer]?.push(s)

  const failed = trace.steps.some(s => s.status === 'failed')
  const shorted = trace.steps.some(s => s.status === 'empty' || s.status === 'empty_upstream')

  return (
    <div className="rounded-md border border-indigo-100 bg-indigo-50/40 px-2.5 py-2">
      <div className="flex items-center gap-1.5 mb-2">
        <span className="text-[10px] font-semibold uppercase tracking-wider text-indigo-600">
          {t('plan_graph.title')}
        </span>
        <span className="text-[10px] text-gray-400">
          {t('plan_graph.summary', { steps: trace.steps.length, layers: maxLayer + 1 })}
        </span>
        {failed && (
          <span className="text-[9px] px-1 py-0.5 rounded bg-red-100 text-red-600 font-semibold">
            {t('plan_graph.badge_failed')}
          </span>
        )}
        {!failed && shorted && (
          <span className="text-[9px] px-1 py-0.5 rounded bg-amber-100 text-amber-700 font-semibold">
            {t('plan_graph.badge_empty')}
          </span>
        )}
      </div>
      <div className="space-y-1">
        {layers.map((layerSteps, li) => (
          <div key={li}>
            <div className="flex flex-wrap items-stretch gap-2">
              {layerSteps.map(step => (
                <StepCard key={step.id} step={step} t={t} />
              ))}
            </div>
            {li < layers.length - 1 && (
              <div className="flex justify-center py-0.5">
                <ChevronDown className="w-3.5 h-3.5 text-indigo-300" />
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
