'use client'

// AnalysisPlan renders the WIP=1 feature ledger driven by the three plan-mode
// tools (start_analysis_plan / verify_feature / complete_analysis).
//
// Spec: .omc/specs/plan-from-ontology-knowledge.md §3.6 + §9.7.
//
// Data source: each plan-mode tool's `result` field carries the *current full
// feature list* (so a single tool card has enough information to render the
// whole ledger — we don't need to stitch the sequence). The complete_analysis
// result additionally carries `finalAnswer` (machine-stitched synthesis with
// caveats already appended, §9.5).

import { useTranslations } from 'next-intl'
import { Check, X, CircleSlash, Loader2, Sparkles } from 'lucide-react'

export type FeatureState = 'not_started' | 'active' | 'passing' | 'blocked'

export interface AnalysisFeatureDescriptor {
  id: string
  behavior: string
  verification: string
  state: FeatureState
  toolHints?: Array<{ tool: string; intent?: string; ref?: string }>
}

export interface AnalysisPlanResult {
  // start_analysis_plan
  started?: boolean
  patternName?: string
  patternId?: string
  features?: AnalysisFeatureDescriptor[]
  activeFeature?: AnalysisFeatureDescriptor
  allSettled?: boolean
  instruction?: string
  // verify_feature
  featureId?: string
  outcome?: 'passing' | 'retry' | 'blocked'
  progress?: { passing: number; blocked: number; notStarted: number; active: number }
  // complete_analysis
  completed?: boolean
  finalAnswer?: string
  passingCount?: number
  blockedCount?: number
  // error path
  error?: string
}

const STATE_STYLE: Record<FeatureState, { ring: string; bg: string; text: string; Icon: typeof Check }> = {
  passing:     { ring: 'border-emerald-300', bg: 'bg-emerald-50', text: 'text-emerald-700', Icon: Check },
  active:      { ring: 'border-sky-400',     bg: 'bg-sky-50',     text: 'text-sky-700',     Icon: Loader2 },
  blocked:     { ring: 'border-amber-300',   bg: 'bg-amber-50',   text: 'text-amber-700',   Icon: CircleSlash },
  not_started: { ring: 'border-gray-200',    bg: 'bg-gray-50',    text: 'text-gray-500',    Icon: X },
}

interface AnalysisPlanProps {
  toolName: string                   // start_analysis_plan | verify_feature | complete_analysis
  result: AnalysisPlanResult
}

export function AnalysisPlan({ toolName, result }: AnalysisPlanProps) {
  const t = useTranslations('analysis_plan')

  if (result?.error) {
    return (
      <div className="border border-rose-300 bg-rose-50 text-rose-700 text-xs px-2 py-1.5 rounded">
        <span className="font-semibold">{t('error_prefix')}</span> {result.error}
      </div>
    )
  }

  const features = result.features || []
  const counts =
    result.progress ||
    (result.completed
      ? { passing: result.passingCount || 0, blocked: result.blockedCount || 0, notStarted: 0, active: 0 }
      : countByState(features))

  return (
    <div className="border border-indigo-200 bg-indigo-50/60 rounded p-2.5 space-y-2">
      {/* header: pattern name + state pills */}
      <div className="flex items-center gap-2 flex-wrap">
        <Sparkles size={14} className="text-indigo-600" />
        <span className="text-[11px] text-indigo-700 font-semibold">{t('header')}</span>
        {result.patternName && (
          <span className="text-[11px] text-gray-900 font-semibold">{result.patternName}</span>
        )}
        {result.completed && (
          <span className="ml-auto text-[10px] bg-emerald-600 text-white px-1.5 py-0.5 rounded">
            ✓ {t('completed')}
          </span>
        )}
        {!result.completed && (
          <span className="ml-auto flex items-center gap-1">
            <Pill label="passing" n={counts.passing} cls="bg-emerald-100 text-emerald-700" />
            <Pill label="active" n={counts.active} cls="bg-sky-100 text-sky-700" />
            <Pill label="blocked" n={counts.blocked} cls="bg-amber-100 text-amber-700" />
            <Pill label="pending" n={counts.notStarted} cls="bg-gray-100 text-gray-600" />
          </span>
        )}
      </div>

      {/* feature rows */}
      {features.length > 0 && (
        <div className="space-y-1.5">
          {features.map((f) => (
            <FeatureRow key={f.id} f={f} />
          ))}
        </div>
      )}

      {/* instruction shown during analysis */}
      {!result.completed && result.instruction && (
        <div className="text-[11px] text-gray-600 italic border-t border-indigo-100 pt-1.5">
          → {result.instruction}
        </div>
      )}

      {/* final answer block — only on complete_analysis */}
      {result.completed && result.finalAnswer && (
        <div className="border border-emerald-200 bg-white rounded p-2 mt-1">
          <div className="text-[10px] text-emerald-700 font-semibold mb-1">{t('final_answer')}</div>
          <pre className="text-[12px] text-gray-900 whitespace-pre-wrap font-sans leading-relaxed">{result.finalAnswer}</pre>
        </div>
      )}

      <div className="text-[10px] text-gray-400 pt-0.5">
        {t('tool_label')}: <code className="bg-white px-1 rounded">{toolName}</code>
        {result.patternId && <> · <code className="bg-white px-1 rounded">patternId={result.patternId}</code></>}
      </div>
    </div>
  )
}

function Pill({ label, n, cls }: { label: string; n: number; cls: string }) {
  return (
    <span className={`text-[10px] px-1.5 py-0.5 rounded font-mono ${cls}`}>
      {label} {n}
    </span>
  )
}

function FeatureRow({ f }: { f: AnalysisFeatureDescriptor }) {
  const s = STATE_STYLE[f.state] || STATE_STYLE.not_started
  const Icon = s.Icon
  const animate = f.state === 'active' ? 'animate-spin' : ''
  return (
    <div className={`flex items-start gap-2 px-2 py-1.5 border ${s.ring} ${s.bg} rounded`}>
      <Icon size={13} className={`${s.text} ${animate} mt-0.5 shrink-0`} />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-1.5 flex-wrap">
          <code className="text-[10px] text-gray-500 bg-white/70 px-1 rounded">{f.id}</code>
          <span className={`text-[11px] font-semibold ${s.text}`}>{f.behavior}</span>
        </div>
        <div className="text-[10px] text-gray-500 mt-0.5">
          <span className="text-gray-400">verify:</span> {f.verification}
        </div>
      </div>
    </div>
  )
}

function countByState(features: AnalysisFeatureDescriptor[]) {
  let passing = 0, blocked = 0, notStarted = 0, active = 0
  for (const f of features) {
    if (f.state === 'passing') passing++
    else if (f.state === 'blocked') blocked++
    else if (f.state === 'active') active++
    else notStarted++
  }
  return { passing, blocked, notStarted, active }
}
