'use client'

import { useState, useRef, useEffect, useCallback, useMemo, Suspense } from 'react'
import { useTranslations } from 'next-intl'
import { useStyleMode } from '@/lib/style-mode'
import { useSearchParams } from 'next/navigation'
import { Button } from '@/components/ui/Button'
import { Badge } from '@/components/ui/Badge'
import { getApiBase, getApiBaseFor, api } from '@/lib/api'
import { useFetch } from '@/lib/hooks'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { llmDisplay } from '@/lib/llmDisplay'
import { CyberLoader } from '@/components/ui/CyberLoader'
import {
  Send, Bot, User, ChevronDown, ChevronRight, BookOpen, FileText, Database, Play,
  Maximize2, Minimize2, Link2, GitBranch, Check, X, Square, Brain, History, ExternalLink
} from 'lucide-react'
import { ResultViewer } from '@/components/ui/ResultViewer'
import { BuilderProposeOdCard } from '@/components/lakehouse-agent/BuilderProposeOdCard'
import { BuilderProposeIntentCard } from '@/components/lakehouse-agent/BuilderProposeIntentCard'
import { BuilderProposeLinkCard } from '@/components/lakehouse-agent/BuilderProposeLinkCard'
import { PlanGraph, type PlanTrace } from '@/components/lakehouse-agent/PlanGraph'
import { AnalysisPlan, type AnalysisPlanResult } from '@/components/lakehouse-agent/AnalysisPlan'
import { MissionLedger, type Mission } from '@/components/lakehouse-agent/MissionLedger'
import { renderDataTemplates, collectStepResults } from '@/components/lakehouse-agent/dataTemplate'
import { splitAnswerSegments } from '@/components/lakehouse-agent/answerChart'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Link } from '@/i18n/navigation'
import type { OntKnowledge, OntCausality, OntObjectType, OntLinkType, OntLearnedFact, OntFactLink, LLMConfig, LLMRoleBinding } from '@/types/api'
import { OntologyGraph, type GraphHighlight, type GraphLayoutMode } from '@/components/ui/OntologyGraph'
import { DiagnosePanel } from '@/components/lakehouse-agent/DiagnosePanel'
import { HomeDashboard } from '@/components/lakehouse-agent/HomeDashboard'
import type { RecallResult } from '@/components/lakehouse-agent/RecallDiagnostics'
import {
  MotionGroup,
  MotionGroupItem,
  MotionFade,
  MotionScale,
  motion,
  AnimatePresence,
} from '@/lib/motion'

// ─── Types ──────────────────────────────────────────────────────

interface FunctionCall {
  name: string
  arguments: Record<string, unknown>
  result?: {
    content?: string
    ontology_sql?: string
    generated_sql?: string
    execution_status?: string
    execution_result?: string
    execution_error?: string
    step_id?: string
    display_mode?: string
    objects?: string[]
    metrics?: string[]
    topicId?: string
    involved?: GraphHighlight
    pending_confirmation?: boolean
    factId?: string
    factType?: string
    title?: string
    keywords?: string[]
    tags?: string[]
    involvedOds?: string[]
    links?: Array<{ targetType: string; targetName: string; targetId?: string; role: string }>
    unresolvedLinks?: string[]
    summary_text?: string
    matched_intent?: string
    plan_mode?: boolean
    plan_trace?: PlanTrace
    pivoted?: {
      pivotOn?: string
      percentAxis?: string
      percentScope?: string
      totalLabel?: string
      withPercent?: boolean
    }
    error?: string
    switchTo?: string
    childThreadId?: string
    parentThreadId?: string
    ambiguousKeyword?: string
    chosenOd?: string
    clarificationSummary?: string
  }
}

interface ChatMessage {
  role: 'user' | 'assistant'
  content: string
  functionCalls: FunctionCall[]
  thinking?: string
  promptTokens?: number
  completionTokens?: number
  totalTokens?: number
  modelName?: string
}

// ─── Tool Meta (built inside component via hook; this placeholder type helps TS) ─

type ToolMetaMap = Record<string, { icon: typeof BookOpen; label: string; color: string }>

// ─── Annotation Parsing ─────────────────────────────────────────

const ANN_SEP = '\n\n---\n\n'

function parseUserMessage(content: string) {
  const idx = content.indexOf(ANN_SEP)
  if (idx > 0) {
    return { display: content.slice(idx + ANN_SEP.length), annotation: content.slice(0, idx), hasAnnotation: true }
  }
  return { display: content, annotation: '', hasAnnotation: false }
}

function stripFunctionCallBlocks(text: unknown): string {
  // Defensive: tool results may carry non-string content fields (arrays of
  // strings, objects, undefined). The TS prop is typed string but the SSE
  // stream is dynamic — coerce here so a malformed payload doesn't blow
  // up the whole chat render.
  let safe: string
  if (typeof text === 'string') {
    safe = text
  } else if (text == null) {
    safe = ''
  } else if (Array.isArray(text)) {
    safe = text.map(x => (typeof x === 'string' ? x : JSON.stringify(x))).join('\n')
  } else {
    safe = JSON.stringify(text)
  }
  return safe.replace(/<function_call>[\s\S]*?<\/function_call>/g, '').replace(/<function_call>[\s\S]*/g, '').trim()
}

// JsonView renders a tool call's arguments/result as a structured, indented
// tree (keys bold, values colored by type, nested objects/arrays indented)
// instead of a bare JSON.stringify dump — keeps non-special tool cards readable.
function JsonView({ data, depth = 0 }: { data: unknown; depth?: number }) {
  if (data === null || data === undefined) return <span className="text-gray-400">null</span>
  if (typeof data === 'string') return <span className="text-amber-700 break-all whitespace-pre-wrap">{data}</span>
  if (typeof data === 'number') return <span className="text-blue-600">{String(data)}</span>
  if (typeof data === 'boolean') return <span className="text-purple-600">{String(data)}</span>
  if (Array.isArray(data)) {
    if (data.length === 0) return <span className="text-gray-400">[]</span>
    return (
      <div className="border-l border-gray-200 pl-2">
        {data.map((v, i) => (
          <div key={i} className="flex gap-1.5 items-start">
            <span className="text-gray-400 select-none shrink-0">{i}</span>
            <div className="min-w-0 flex-1"><JsonView data={v} depth={depth + 1} /></div>
          </div>
        ))}
      </div>
    )
  }
  if (typeof data === 'object') {
    const entries = Object.entries(data as Record<string, unknown>)
    if (entries.length === 0) return <span className="text-gray-400">{'{}'}</span>
    return (
      <div className={depth > 0 ? 'border-l border-gray-200 pl-2' : ''}>
        {entries.map(([k, v]) => (
          <div key={k} className="flex gap-1.5 items-start">
            <span className="text-gray-600 font-semibold shrink-0">{k}</span>
            <div className="min-w-0 flex-1"><JsonView data={v} depth={depth + 1} /></div>
          </div>
        ))}
      </div>
    )
  }
  return <span className="text-gray-500">{String(data)}</span>
}

// AnswerBody renders an assistant answer, splitting out any 「chart …」 schema
// the AI emits for a large result. Text runs through renderDataTemplates +
// markdown as before; each chart schema is drawn as a visualization from its
// source table's rows (collectStepResults). The tool only fetched the data —
// the chart is rendered HERE, inside the answer, from the AI's schema; concrete
// numbers never come from the LLM. If the source rows aren't present yet (mid
// stream) or can't be found, the raw token is shown as text (graceful).
function AnswerBody({ content, functionCalls }: {
  content: string
  functionCalls: Parameters<typeof renderDataTemplates>[1]
}) {
  const segments = useMemo(() => splitAnswerSegments(content || ''), [content])
  const steps = useMemo(() => collectStepResults(functionCalls), [functionCalls])
  return (
    <>
      {segments.map((seg, i) => {
        if (seg.kind === 'text') {
          if (!seg.text.trim()) return null
          return <StreamMarkdown key={i} content={renderDataTemplates(seg.text, functionCalls)} />
        }
        const step = steps.get(seg.spec.from)
        if (!step || step.rows.length === 0) {
          return <StreamMarkdown key={i} content={seg.raw} />
        }
        return (
          <div key={i} className="my-2">
            <ResultViewer data={JSON.stringify(step.rows)} chartSpec={seg.spec} />
          </div>
        )
      })}
    </>
  )
}

function StreamMarkdown({ content, className = '' }: { content: unknown; className?: string }) {
  const clean = stripFunctionCallBlocks(content)
  return (
    <div className={className}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          p: ({ children }) => <p className="mb-2 last:mb-0 text-sm leading-relaxed">{children}</p>,
          strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
          em: ({ children }) => <em className="text-gray-500">{children}</em>,
          ul: ({ children }) => <ul className="mb-2 ml-4 list-disc last:mb-0 text-sm">{children}</ul>,
          ol: ({ children }) => <ol className="mb-2 ml-4 list-decimal last:mb-0 text-sm">{children}</ol>,
          li: ({ children }) => <li className="mb-0.5">{children}</li>,
          h1: ({ children }) => <h1 className="mb-2 text-base font-bold border-b border-gray-200 pb-1">{children}</h1>,
          h2: ({ children }) => <h2 className="mb-1.5 text-sm font-bold">{children}</h2>,
          h3: ({ children }) => <h3 className="mb-1 text-sm font-semibold text-gray-500">{children}</h3>,
          code: ({ children, className: cn }) => {
            const isBlock = cn?.includes('language-')
            if (isBlock) {
              const lang = cn?.replace('language-', '') || ''
              return (
                <pre className="my-2 overflow-auto bg-gray-900 px-3 py-2 font-mono text-xs text-green-400 whitespace-pre-wrap rounded">
                  {lang && <span className="block text-[10px] text-gray-500 mb-1 uppercase">{lang}</span>}
                  <code>{children}</code>
                </pre>
              )
            }
            return <code className="bg-gray-100 border border-gray-200 px-1 py-0.5 font-mono text-xs text-gray-700 rounded">{children}</code>
          },
          pre: ({ children }) => <>{children}</>,
          table: ({ children }) => (
            <div className="my-2 overflow-x-auto">
              <table className="w-full border-collapse text-xs">{children}</table>
            </div>
          ),
          thead: ({ children }) => <thead className="bg-gray-50">{children}</thead>,
          th: ({ children }) => <th className="border border-gray-200 px-2 py-1.5 text-left font-semibold text-xs">{children}</th>,
          td: ({ children }) => {
            const text = String(children ?? '')
            const isNum = /^[\d,]+\.?\d*$/.test(text.replace(/,/g, ''))
            return <td className={`border border-gray-200 px-2 py-1 ${isNum ? 'text-right font-mono font-semibold' : ''}`}>{children}</td>
          },
          blockquote: ({ children }) => <blockquote className="my-2 border-l-2 border-gray-300 pl-3 text-gray-500 text-sm">{children}</blockquote>,
          hr: () => <hr className="my-3 border-gray-200" />,
          a: ({ href, children }) => <a href={href} className="text-blue-600 underline" target="_blank" rel="noopener noreferrer">{children}</a>,
        }}
      >
        {clean}
      </ReactMarkdown>
    </div>
  )
}

// ─── Learn Confirmation Card ─────────────────────────────────────

function LearnConfirmCard({ fc }: { fc: FunctionCall }) {
  const t = useTranslations('agent.main')
  const [status, setStatus] = useState<'pending' | 'confirmed' | 'rejected' | 'loading'>('pending')
  const factId = fc.result?.factId

  const confirm = async (action: 'confirmed' | 'rejected') => {
    if (!factId) return
    setStatus('loading')
    try {
      await api(`/ontology/learned-facts/${factId}`, {
        method: 'PUT',
        body: JSON.stringify({ confidence: action }),
      })
      setStatus(action)
    } catch {
      setStatus('pending')
    }
  }

  const title = String(fc.arguments.title || fc.result?.title || '')
  const summary = String(fc.arguments.summary || fc.result?.summary_text?.split('\n')[0] || '')
  const content = String(fc.arguments.content || '')
  const rawTags = (fc.result?.tags ?? fc.arguments.tags ?? fc.result?.keywords ?? fc.arguments.keywords ?? []) as unknown[]
  const tags: string[] = Array.isArray(rawTags) ? rawTags.map(t => typeof t === 'string' ? t : JSON.stringify(t)).filter(Boolean) : []
  type LinkRow = { targetType: string; targetName: string; role: string }
  const rawLinks = (fc.result?.links || fc.arguments.links) as unknown
  let links: LinkRow[] = []
  if (Array.isArray(rawLinks)) {
    links = rawLinks
      .filter((l): l is Record<string, unknown> => typeof l === 'object' && l !== null)
      .map(l => ({
        targetType: String(l.targetType || 'object'),
        targetName: String(l.targetName || ''),
        role: String(l.role || 'about'),
      }))
      .filter(l => l.targetName)
  } else {
    const involvedOds = (fc.result?.involvedOds as string[]) || (fc.arguments.involvedOds as string[]) || []
    links = involvedOds.map(o => ({ targetType: 'object', targetName: o, role: 'about' }))
  }

  return (
    <MotionScale>
      <div className="border border-green-200 bg-green-50 rounded-lg p-3 space-y-2">
        <div className="flex items-center gap-2">
          <FileText className="h-4 w-4 text-green-600" />
          <span className="text-xs font-semibold text-green-700">{t('learn_confirm.enable_q')}</span>
          {fc.result?.factType && (
            <span className="text-[9px] font-semibold px-1.5 py-0.5 rounded border border-green-300 text-green-700">
              {({business_rule: t('learn_confirm.fact_types.business_rule'), calibration: t('learn_confirm.fact_types.calibration'), misconception: t('learn_confirm.fact_types.misconception'), filter_hint: t('learn_confirm.fact_types.filter_hint'), calculation_note: t('learn_confirm.fact_types.calculation_note')} as Record<string,string>)[fc.result.factType as string] || fc.result.factType}
            </span>
          )}
        </div>

        <div className="space-y-1.5 bg-white border border-green-100 rounded px-3 py-2">
          {title && <div className="text-xs font-bold text-green-800">{title}</div>}
          <div className="text-sm font-semibold text-gray-800">{summary}</div>
          {content && content !== summary && (
            <div className="text-xs text-gray-500 whitespace-pre-wrap">{content}</div>
          )}
          {tags.length > 0 && (
            <div className="flex items-center gap-1.5 flex-wrap">
              <span className="text-[10px] text-gray-400">{t('learn_confirm.label_tag')}</span>
              {tags.map((tk, i) => (
                <span key={i} className="border border-green-200 text-green-700 px-1.5 py-0.5 rounded text-[10px] font-medium">{tk}</span>
              ))}
            </div>
          )}
          {links.length > 0 && (
            <div className="flex items-center gap-1.5 flex-wrap">
              <span className="text-[10px] text-gray-400">{t('learn_confirm.label_rel')}</span>
              {links.map((l, i) => (
                <span key={i} className="text-[10px] bg-gray-100 border border-gray-200 rounded px-1.5 py-0.5 font-medium">
                  {l.targetType === 'object' ? 'Od' : l.targetType === 'knowledge' ? 'Ok' : 'Ol'}:{l.targetName}
                  <span className="ml-1 text-gray-400">{l.role}</span>
                </span>
              ))}
            </div>
          )}
        </div>

        {Array.isArray(fc.result?.unresolvedLinks) && (fc.result.unresolvedLinks as string[]).length > 0 && (
          <div className="text-[10px] text-amber-600 bg-amber-50 border border-amber-200 rounded px-2 py-1">
            {t('learn_confirm.unresolved_links')} {(fc.result.unresolvedLinks as string[]).join('、')}
          </div>
        )}

        {status === 'pending' && (
          <div className="flex gap-2">
            <button
              onClick={() => confirm('confirmed')}
              className="flex items-center gap-1 bg-green-600 text-white text-xs px-3 py-1.5 rounded hover:bg-green-700 transition-colors"
            >
              <Check className="h-3.5 w-3.5" /> {t('learn_confirm.btn_enable')}
            </button>
            <button
              onClick={() => confirm('rejected')}
              className="flex items-center gap-1 border border-gray-200 text-gray-500 text-xs px-3 py-1.5 rounded hover:border-gray-400 transition-colors"
            >
              <X className="h-3.5 w-3.5" /> {t('learn_confirm.btn_disable')}
            </button>
          </div>
        )}
        {status === 'loading' && <span className="text-xs text-gray-400 animate-pulse">{t('learn_confirm.processing')}</span>}
        {status === 'confirmed' && (
          <div className="flex items-center gap-1.5 text-xs text-green-600 font-semibold">
            <Check className="h-4 w-4" /> {t('learn_confirm.enabled')}
          </div>
        )}
        {status === 'rejected' && (
          <div className="flex items-center gap-1.5 text-xs text-gray-400">
            <X className="h-3.5 w-3.5" /> {t('learn_confirm.disabled')}
          </div>
        )}
      </div>
    </MotionScale>
  )
}

// ─── Clarify & Branch Card ──────────────────────────────────────

function ClarifyBranchCard({ fc, onGoto }: { fc: FunctionCall; onGoto: (childId: string) => void }) {
  const t = useTranslations('agent.main')
  const ambiguousKeyword = String(fc.result?.ambiguousKeyword || fc.arguments.ambiguousKeyword || '')
  const chosenOd = String(fc.result?.chosenOd || fc.arguments.chosenOd || '')
  const clarificationSummary = String(fc.result?.clarificationSummary || fc.arguments.clarificationSummary || '')
  const childId = String(fc.result?.childThreadId || fc.result?.switchTo || '')
  const err = fc.result?.error ? String(fc.result.error) : ''

  if (err) {
    return (
      <div className="border border-red-200 bg-red-50 rounded-lg p-3 space-y-1">
        <div className="flex items-center gap-2">
          <GitBranch className="h-4 w-4 text-red-500" />
          <span className="text-xs font-semibold text-red-600">{t('branch.failed')}</span>
        </div>
        <div className="text-xs text-red-700">{err}</div>
      </div>
    )
  }

  return (
    <MotionScale>
      <div className="border border-blue-200 bg-blue-50 rounded-lg p-3 space-y-2">
        <div className="flex items-center gap-2">
          <GitBranch className="h-4 w-4 text-blue-600" />
          <span className="text-xs font-semibold text-blue-700">{t('branch.title')}</span>
        </div>
        <div className="bg-white border border-blue-100 rounded px-3 py-2 space-y-1.5">
          <div className="flex items-center gap-1.5">
            <span className="text-[10px] text-gray-400">{t('branch.ambiguous_kw')}</span>
            <span className="border border-red-200 text-red-600 px-1.5 py-0.5 rounded text-[10px] font-semibold">{ambiguousKeyword}</span>
          </div>
          <div className="flex items-center gap-1.5">
            <span className="text-[10px] text-gray-400">{t('branch.user_chosen')}</span>
            <span className="bg-gray-800 text-white px-1.5 py-0.5 rounded text-[10px] font-semibold">{chosenOd}</span>
          </div>
          {clarificationSummary && (
            <div className="text-xs text-gray-500 pt-1 border-t border-gray-100">{clarificationSummary}</div>
          )}
        </div>
        {childId && (
          <button
            onClick={() => onGoto(childId)}
            className="flex items-center gap-1.5 bg-blue-600 text-white text-xs px-3 py-1.5 rounded hover:bg-blue-700 transition-colors w-full justify-center"
          >
            <GitBranch className="h-3.5 w-3.5" /> {t('branch.goto_btn')}
          </button>
        )}
      </div>
    </MotionScale>
  )
}

// ─── Single Tool Card ───────────────────────────────────────────

function ToolCard({ fc, expanded, onToggle, onGotoBranch, toolMeta }: { fc: FunctionCall; expanded: boolean; onToggle: () => void; onGotoBranch?: (childId: string) => void; toolMeta: ToolMetaMap }) {
  const t = useTranslations('agent.main')
  const meta = toolMeta[fc.name] || { icon: Database, label: fc.name, color: 'border-gray-200 bg-gray-50' }
  const Icon = meta.icon

  return (
    // w-full + min-w-0 + max-w-full = card fills the bubble width, never grows
    // beyond it. Combined with overflow-hidden, wide table contents must scroll
    // horizontally inside the card (see the table wrapper for overflow-auto).
    <div className={`w-full min-w-0 max-w-full border rounded-lg overflow-hidden ${meta.color}`}>
      <button onClick={onToggle} className="flex w-full items-center gap-2 px-3 py-2 hover:bg-black/[0.03] transition-colors">
        {expanded ? <ChevronDown className="h-3 w-3 text-gray-400" /> : <ChevronRight className="h-3 w-3 text-gray-400" />}
        <Icon className="h-3.5 w-3.5 text-gray-500" />
        <span className="text-xs font-semibold text-gray-700">{meta.label}</span>
        {fc.result?.execution_status === 'success' && <Badge variant="success" className="text-[9px] px-1 py-0">OK</Badge>}
        {fc.result?.error && <Badge variant="accent" className="text-[9px] px-1 py-0">ERR</Badge>}
        <span className="ml-auto text-[10px] text-gray-400 font-mono">{fc.name}</span>
      </button>
      <AnimatePresence>
        {expanded && (
          <MotionScale className="border-t border-gray-100">
            <div className="px-3 py-2 space-y-1.5">
              {fc.name === 'lookup' && (
                <div className="space-y-1">
                  {(Array.isArray(fc.arguments.ontology_name) ? fc.arguments.ontology_name as string[] : []).length > 0 && (
                    <div className="flex items-center gap-1.5 flex-wrap">
                      <span className="text-[10px] text-gray-400">{t('tool_card.label_body')}</span>
                      {(Array.isArray(fc.arguments.ontology_name) ? fc.arguments.ontology_name as string[] : []).map((n, i) => (
                        <span key={i} className="bg-gray-800 text-white px-1.5 py-0.5 rounded text-[10px] font-semibold">{n}</span>
                      ))}
                    </div>
                  )}
                  {(Array.isArray(fc.arguments.keyword) ? fc.arguments.keyword as string[] : []).length > 0 && (
                    <div className="flex items-center gap-1.5 flex-wrap">
                      <span className="text-[10px] text-gray-400">{t('tool_card.label_keyword')}</span>
                      {(Array.isArray(fc.arguments.keyword) ? fc.arguments.keyword as string[] : []).map((k, i) => (
                        <span key={i} className="border border-blue-200 text-blue-600 px-1.5 py-0.5 rounded text-[10px]">{k}</span>
                      ))}
                    </div>
                  )}
                  {fc.result?.involved && fc.result.involved.odNames?.length > 0 && (
                    <div className="flex items-center gap-1.5 flex-wrap">
                      <span className="text-[10px] text-gray-400">{t('tool_card.label_hit_od')}</span>
                      {fc.result.involved.odNames.map((n: string, i: number) => (
                        <span key={i} className="bg-blue-50 text-blue-700 border border-blue-200 px-1.5 py-0.5 rounded text-[10px] font-semibold">{n}</span>
                      ))}
                      {(fc.result.involved.okTitles || []).map((t: string, i: number) => (
                        <span key={`ok-${i}`} className="bg-purple-50 text-purple-600 border border-purple-200 px-1.5 py-0.5 rounded text-[10px] font-semibold">{t}</span>
                      ))}
                    </div>
                  )}
                </div>
              )}

              {fc.name === 'smartquery' && (
                <div className="space-y-1.5">
                  {(fc.result?.matched_intent || fc.result?.pivoted) && (
                    <div className="flex items-center gap-1.5 flex-wrap">
                      {fc.result?.matched_intent && (
                        <>
                          <span className="text-[10px] text-gray-400">{t('tool_card.label_intent')}</span>
                          <span className="border border-gray-300 bg-gray-100 px-1.5 py-0.5 rounded text-[10px] text-gray-700 font-semibold">{String(fc.result.matched_intent)}</span>
                        </>
                      )}
                      {fc.result?.pivoted?.pivotOn && (
                        <span className="border border-gray-200 px-1.5 py-0.5 rounded text-[10px] text-gray-500">
                          pivot: {fc.result.pivoted.pivotOn}
                        </span>
                      )}
                    </div>
                  )}
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="text-[10px] text-gray-400">{t('tool_card.label_object')}</span>
                    {(Array.isArray(fc.arguments.objects) ? fc.arguments.objects : []).map((o: string, oi: number) => (
                      <span key={oi} className="bg-gray-800 text-white px-1.5 py-0.5 rounded text-[10px] font-semibold">{o}</span>
                    ))}
                    {fc.arguments.metric ? <><span className="text-[10px] text-gray-400 ml-1">{t('tool_card.label_metric')}</span><Badge variant="accent">{String(fc.arguments.metric)}</Badge></> : null}
                  </div>
                  {(Array.isArray(fc.arguments.groupBy) ? fc.arguments.groupBy as string[] : []).length > 0 && (
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="text-[10px] text-gray-400">{t('tool_card.label_group')}</span>
                      {(Array.isArray(fc.arguments.groupBy) ? fc.arguments.groupBy as string[] : []).map((g, gi) => (
                        <span key={gi} className="border border-gray-200 px-1.5 py-0.5 rounded text-[10px] text-gray-500">{g}</span>
                      ))}
                    </div>
                  )}
                  {(() => {
                    const filters = Array.isArray(fc.arguments.filters) ? fc.arguments.filters as Array<Record<string, string>> : []
                    if (filters.length === 0) return null
                    return (
                      <div className="flex items-center gap-1.5 flex-wrap">
                        <span className="text-[10px] text-gray-400">{t('tool_card.label_filter')}</span>
                        {filters.map((f, fi) => (
                          <span key={fi} className="border border-amber-200 bg-amber-50 px-1.5 py-0.5 rounded text-[10px] text-amber-700">
                            {f.prop || f.property || f.field || f.column || ''} {f.op || f.operator || '='} {f.value || ''}
                          </span>
                        ))}
                      </div>
                    )
                  })()}
                </div>
              )}

              {fc.name === 'smartquery' && fc.result?.plan_trace && (
                <PlanGraph trace={fc.result.plan_trace} />
              )}

              {(fc.name === 'start_analysis_plan' || fc.name === 'verify_feature' || fc.name === 'complete_analysis') && fc.result && (
                <AnalysisPlan toolName={fc.name} result={fc.result as AnalysisPlanResult} />
              )}

              {fc.name === 'propose_learned_fact' && <LearnConfirmCard fc={fc} />}

              {fc.name === 'clarify_and_branch' && (
                <ClarifyBranchCard fc={fc} onGoto={(childId) => onGotoBranch?.(childId)} />
              )}

              {!['lookup', 'smartquery', 'read', 'request_query', 'propose_learned_fact', 'clarify_and_branch', 'return_to_parent'].includes(fc.name) && Object.keys(fc.arguments).length > 0 && (
                <div className="bg-gray-50 border border-gray-200 rounded px-2 py-1.5 font-mono text-[11px] text-gray-600 leading-relaxed max-h-48 overflow-auto">
                  <JsonView data={fc.arguments} />
                </div>
              )}

              {fc.result?.content && fc.name !== 'propose_learned_fact' && (
                <div className="border border-gray-100 bg-white rounded px-2 py-1.5 max-h-40 overflow-y-auto">
                  <StreamMarkdown content={fc.result.content} className="text-xs text-gray-600" />
                </div>
              )}

              {fc.result?.ontology_sql && (
                <details>
                  <summary className="text-[10px] text-gray-400 cursor-pointer hover:text-gray-600 mt-1 font-mono uppercase tracking-wider">
                    Ontology SQL ({String(fc.result.ontology_sql).split('\n').length} lines)
                  </summary>
                  <pre className="mt-1 bg-gray-900 rounded px-2 py-1.5 font-mono text-xs text-green-400 whitespace-pre-wrap max-h-32 overflow-y-auto">{fc.result.ontology_sql}</pre>
                </details>
              )}

              {fc.result?.generated_sql && (
                <details>
                  <summary className="text-[10px] text-gray-400 cursor-pointer hover:text-gray-600 mt-1">
                    Physical SQL ({fc.result.generated_sql.split('\n').length} lines)
                  </summary>
                  <pre className="mt-1 bg-gray-900 rounded px-2 py-1.5 font-mono text-xs text-blue-400 whitespace-pre-wrap max-h-32 overflow-y-auto">{fc.result.generated_sql}</pre>
                </details>
              )}

              {fc.result?.execution_result && (
                // Fixed-height + bounded-width scrollable viewport. w-full
                // min-w-0 max-w-full forces the inner ResultViewer to inherit
                // the card width (instead of growing to its natural table
                // width); overflow-auto then activates real horizontal +
                // vertical scrolling for wide multi-column results.
                <div className="w-full min-w-0 max-w-full max-h-[420px] overflow-auto rounded border border-gray-100 bg-white">
                  <ResultViewer data={fc.result.execution_result} initialMode={fc.result.display_mode as 'table' | 'bar' | 'pie' | 'line' | undefined} />
                </div>
              )}

              {fc.result?.execution_error && (
                <div className="border border-red-200 bg-red-50 rounded px-2 py-1 text-xs text-red-600 max-h-16 overflow-y-auto">{fc.result.execution_error}</div>
              )}

              {fc.result?.error && (
                <div className="text-xs text-red-500">{fc.result.error}</div>
              )}
            </div>
          </MotionScale>
        )}
      </AnimatePresence>
    </div>
  )
}

// ─── Streaming dot indicator ─────────────────────────────────────

function StreamingDot() {
  return (
    <span className="inline-flex gap-0.5 items-center ml-1">
      {[0, 1, 2].map(i => (
        <motion.span
          key={i}
          className="inline-block w-1 h-1 rounded-full bg-gray-400"
          animate={{ opacity: [0.3, 1, 0.3] }}
          transition={{ duration: 1.2, repeat: Infinity, delay: i * 0.2 }}
        />
      ))}
    </span>
  )
}

// ─── Main Component ─────────────────────────────────────────────

function LakehouseAgentChat() {
  const t = useTranslations('agent.main')
  const tw = useTranslations('workbench')
  const industrial = useStyleMode().mode === 'industrial'
  const searchParams = useSearchParams()
  const { currentProject } = useProject()
  const msg = useMessage()

  const toolMeta: ToolMetaMap = {
    lookup:               { icon: BookOpen,  label: t('tools.lookup.label'),               color: 'border-gray-200 bg-gray-50' },
    smartquery:           { icon: Play,      label: t('tools.smartquery.label'),            color: 'border-gray-300 bg-white' },
    link_to_od:           { icon: Link2,     label: t('tools.link_to_od.label'),            color: 'border-gray-200 bg-gray-50' },
    create_causality:     { icon: GitBranch, label: t('tools.create_causality.label'),      color: 'border-gray-200 bg-gray-50' },
    propose_learned_fact: { icon: FileText,  label: t('tools.propose_learned_fact.label'), color: 'border-green-200 bg-green-50' },
}

  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [input, setInput] = useState('')
  const [loading, setLoading] = useState(false)
  const [loadingThread, setLoadingThread] = useState(false)
  const [threadId, setThreadId] = useState(searchParams.get('threadId') || '')
  // MissionAct M4 — the mission ledger for the current thread.
  const [missions, setMissions] = useState<Mission[]>([])
  const [expandedTools, setExpandedTools] = useState<Set<string>>(new Set())
  const [expandedThinking, setExpandedThinking] = useState<Set<number>>(new Set())
  const [expandedAnnotations, setExpandedAnnotations] = useState<Set<number>>(new Set())
  const [graphHighlight, setGraphHighlight] = useState<GraphHighlight | null>(null)
  const [todoItems, setTodoItems] = useState<Array<{ id: string; text: string; status: string }>>([])
  interface BranchEntry { threadId: string; title: string }
  const [branchStack, setBranchStack] = useState<BranchEntry[]>([])
  const [threadStatus, setThreadStatus] = useState<'active' | 'suspended' | 'completed'>('active')
  const [mode, setMode] = useState<'lakehouse' | 'builder'>(() => {
    const m = searchParams.get('mode')
    if (m === 'builder') return 'builder'
    return 'lakehouse'
  })
  const [ambiguousKeyword, setAmbiguousKeyword] = useState<string>('')
  // Thread Memory Ledger panel
  interface LedgerSummary {
    version: number; turnCount: number; odCount: number; intentCount: number;
    tokenCount: number; strongTokenCount: number; okCount: number; olCount: number;
    rebuiltFromStep: number; projectVersionId?: string; versionDrift?: boolean
  }
  interface LedgerOd {
    odId: string; name: string; kind: string; description: string;
    loadedInTurn: number; loadMethod: string;
    matchedPropsCount: number; allPropNamesCount: number; linkCount: number;
    matchedPropNames: string[]; versionStale: boolean
  }
  interface LedgerIntent {
    intentId: string; name: string; canonicalMetric: string;
    autoGroupBy: string[]; matchedTokens: string[]; firstSeenInTurn: number;
    versionStale: boolean
  }
  interface LedgerToken {
    token: string; firstSeen: number; lastSeen: number; strongHit: boolean;
    matchedOdIds: string[]; matchedIntentIds: string[]; matchedPropCount: number
  }
  interface LedgerView {
    summary: LedgerSummary
    ods: LedgerOd[]
    intents: LedgerIntent[]
    tokens: LedgerToken[]
    okEntries: Array<{ id: string; title: string; summary: string; firstSeenInTurn: number }>
    olEntries: Array<{ id: string; title: string; summary: string; firstSeenInTurn: number }>
  }
  const [showMemory, setShowMemory] = useState(false)
  const [ledger, setLedger] = useState<LedgerView | null>(null)
  const [ledgerLoading, setLedgerLoading] = useState(false)
  const bottomRef = useRef<HTMLDivElement>(null)
  const chatInputRef = useRef<HTMLInputElement>(null)
  const abortCtrlRef = useRef<AbortController | null>(null)
  type PendingSwitch =
    | { kind: 'push'; targetId: string; parentId: string; parentTitle: string }
    | { kind: 'pop'; targetId: string }
  const pendingBranchSwitch = useRef<PendingSwitch | null>(null)

  const [graphFullscreen, setGraphFullscreen] = useState(false)
  const [graphLayoutMode, setGraphLayoutMode] = useState<GraphLayoutMode>('circular-od')
  // Right pane tabs. 任务 (mission/reachability + the question's live 分词) is the
  // default and leads, since the agent forcibly tokenizes + judges reachability
  // for every question as it answers; 诊断 / 图谱 follow.
  const [panelTab, setPanelTab] = useState<'mission' | 'diagnose' | 'graph'>('mission')
  // 分词 + 召回 for the current turn, delivered by the agent's 'recall' SSE event
  // (not a client-side HTTP call). Feeds both the 任务 and 诊断 panels.
  const [streamRecall, setStreamRecall] = useState<{ tokens: string[]; recall: RecallResult } | null>(null)
  // History preview drawer — pick a past thread without leaving the workbench.
  type HistoryThread = { id: string; title: string; agentType?: 'lakehouse' | 'builder'; updatedAt: string }
  const [historyOpen, setHistoryOpen] = useState(false)
  const [historyThreads, setHistoryThreads] = useState<HistoryThread[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const openHistory = async () => {
    setHistoryOpen(true)
    setHistoryLoading(true)
    try {
      const res = await api<{ data: HistoryThread[] }>(`/ontology/lakehouse-agent-threads?projectId=${currentProject?.id}`)
      setHistoryThreads(res.data || [])
    } catch { /* ignore — drawer shows empty state */ }
    finally { setHistoryLoading(false) }
  }

  // Agent LLM binding — 让用户在顶部直接切换 Agent 用的模型，
  // 写入 /llm-role-binding（roleName=agent）。下一轮发送即生效。
  const { data: llmConfigs } = useFetch<LLMConfig>('/llm-config')
  const { data: roleBindings, refetch: refetchBindings } = useFetch<LLMRoleBinding>('/llm-role-binding')
  const chatConfigs = useMemo(() => llmConfigs.filter(c => c.configType === 'chat'), [llmConfigs])
  const activeChatId = useMemo(() => chatConfigs.find(c => c.isActive)?.id || '', [chatConfigs])
  const agentBindingId = useMemo(
    () => roleBindings.find(b => b.roleName === 'agent')?.configId || '',
    [roleBindings],
  )
  // 下拉显示值：有 binding 则显示 binding；没有则显示当前 active 模型 id（fallback 路径）
  const agentSelectedId = agentBindingId || activeChatId

  const onAgentLLMChange = async (newId: string) => {
    try {
      if (!newId) {
        await api(`/llm-role-binding/agent`, { method: 'DELETE' })
        msg.success(t('llm.unbound_success'))
      } else {
        await api('/llm-role-binding', { method: 'PUT', body: { roleName: 'agent', configId: newId } })
        const c = chatConfigs.find(x => x.id === newId)
        msg.success(t('llm.switch_success', { model: llmDisplay(c) || c?.modelName || '' }))
      }
      refetchBindings()
    } catch {
      msg.error(t('llm.switch_fail'))
    }
  }

  const { data: knowledge } = useFetch<OntKnowledge>('/ontology/knowledge')
  const { data: causalities } = useFetch<OntCausality>('/ontology/causality')
  const { data: objects } = useFetch<OntObjectType>('/ontology/objects')
  const { data: odLinks } = useFetch<OntLinkType>('/ontology/links')
  const { data: learnedFacts } = useFetch<OntLearnedFact>('/ontology/learned-facts')
  const { data: factLinks } = useFetch<OntFactLink>('/ontology/fact-links')

  const markedObjects = useMemo(() => objects.filter(o => o.mark), [objects])
  const joinKeyLinks = useMemo(() => causalities.filter(c => c.relationType === 'join_key'), [causalities])

  useEffect(() => { bottomRef.current?.scrollIntoView({ behavior: 'smooth' }) }, [messages])

  // Sync mode + threadId to URL
  useEffect(() => {
    const url = `/lakehouse/ontology/lakehouse-agent?mode=${mode}${threadId ? `&threadId=${threadId}` : ''}`
    window.history.replaceState(null, '', url)
  }, [mode, threadId])

  // Thread Memory Ledger — fetch on toggle / after each SSE 'done'.
  const fetchLedger = useCallback(async (id: string) => {
    if (!id) { setLedger(null); return }
    setLedgerLoading(true)
    try {
      const res = await api<LedgerView>(`/ontology/lakehouse-ledger?threadId=${encodeURIComponent(id)}`)
      setLedger(res)
    } catch {
      setLedger(null)
    } finally {
      setLedgerLoading(false)
    }
  }, [])
  useEffect(() => {
    if (showMemory && threadId) {
      fetchLedger(threadId)
    } else if (!threadId) {
      setLedger(null)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [showMemory, threadId])

  // MissionAct M4 — fetch the mission ledger whenever the thread changes.
  // Empty list is the legitimate response (USE_MISSION_ACT off, or no
  // missions yet); the component renders nothing in that case.
  const refetchMissions = useCallback(async (tid?: string | null) => {
    const id = tid ?? threadId
    if (!id) { setMissions([]); return }
    try {
      const res = await api<{ missions?: Mission[] }>(`/ontology/lakehouse-missions?thread_id=${encodeURIComponent(id)}`)
      setMissions(res?.missions ?? [])
    } catch {
      setMissions([])
    }
  }, [threadId])
  useEffect(() => {
    void refetchMissions(threadId)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [threadId, messages.length])

  const loadThread = useCallback(async (id: string) => {
    if (!id) return
    setLoadingThread(true)
    setStreamRecall(null)
    try {
      const res = await api<{
        id: string
        title?: string
        status?: 'active' | 'suspended' | 'completed'
        parentThreadId?: string
        ambiguousKeyword?: string
        steps: Array<{
          role: string; content: string; thinking?: string; functionCall?: unknown
        }>
      }>(`/ontology/lakehouse-agent-threads/${id}`)
      // One agent turn spans MULTIPLE ont_agent_step rows (a tool-call round,
      // a reflect round, …, the final-answer round). Merge each turn's
      // assistant rows into one message — matching the live-streaming path —
      // so the final answer keeps that turn's functionCalls in scope. This is
      // required for data-template references (「sum(t1.col)」 etc.) to resolve
      // on page reload: renderDataTemplates resolves m.content against
      // m.functionCalls, and step ids are per-turn.
      const restored: ChatMessage[] = []
      let pendingAsst: ChatMessage | null = null
      const flushAsst = () => {
        if (pendingAsst) { restored.push(pendingAsst); pendingAsst = null }
      }
      for (const step of res.steps || []) {
        if (step.role === 'user') {
          flushAsst()
          restored.push({ role: 'user', content: step.content || '', functionCalls: [] })
        } else if (step.role === 'assistant') {
          if (!pendingAsst) pendingAsst = { role: 'assistant', content: '', functionCalls: [] }
          if (step.functionCall) {
            const arr = Array.isArray(step.functionCall) ? step.functionCall : [step.functionCall]
            for (const fc of arr) {
              const f = fc as Record<string, unknown>
              if (f.name) pendingAsst.functionCalls.push({ name: String(f.name), arguments: (f.arguments as Record<string, unknown>) || {}, result: f.result as FunctionCall['result'] })
            }
          }
          if (step.content) {
            pendingAsst.content = pendingAsst.content ? pendingAsst.content + '\n' + step.content : step.content
          }
          if (step.thinking) {
            pendingAsst.thinking = (pendingAsst.thinking ? pendingAsst.thinking + '\n' : '') + step.thinking
          }
        }
      }
      flushAsst()
      setMessages(restored)
      setThreadId(id)
      setThreadStatus(res.status || 'active')
      setAmbiguousKeyword(res.ambiguousKeyword || '')
      const loadedAgentType = (res as Record<string, unknown>).agentType as string | undefined
      if (loadedAgentType === 'builder' || loadedAgentType === 'lakehouse') {
        if (loadedAgentType !== mode) setMode(loadedAgentType)
      }
      return res
    } catch {
      msg.error(t('thread.load_fail'))
      return null
    } finally {
      setLoadingThread(false)
    }
  }, [msg, t])

  useEffect(() => {
    const tid = searchParams.get('threadId')
    if (tid && messages.length === 0) loadThread(tid)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams])

  const startNewThread = () => {
    setThreadId('')
    setMessages([])
    setStreamRecall(null)
    setBranchStack([])
    setThreadStatus('active')
    setAmbiguousKeyword('')
    window.history.replaceState(null, '', '/lakehouse/ontology/lakehouse-agent')
  }

  const handleSend = async (messageText?: string) => {
    const text = messageText || input.trim()
    if (!text || loading) return
    const userMsg: ChatMessage = { role: 'user', content: text, functionCalls: [] }
    const newMessages = [...messages, userMsg]
    setMessages(newMessages)
    if (!messageText) setInput('')
    setLoading(true)
    setGraphHighlight(null)
    setStreamRecall(null)

    const assistantIdx = newMessages.length
    setMessages(prev => [...prev, { role: 'assistant', content: '', functionCalls: [] }])

    const abortCtrl = new AbortController()
    abortCtrlRef.current = abortCtrl

    try {
      const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
      const res = await fetch(`${getApiBaseFor('/ontology/lakehouse-agent-stream')}/ontology/lakehouse-agent-stream`, {
        method: 'POST',
        signal: abortCtrl.signal,
        headers: {
          'Content-Type': 'application/json',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify({
          messages: newMessages.map(m => ({ role: m.role, content: m.content })),
          projectId: currentProject?.id,
          threadId: threadId || undefined,
          mode: mode,
        }),
      })

      if (!res.ok || !res.body) {
        const data = await res.json().catch(() => ({}))
        setMessages(prev => {
          const n = [...prev]
          n[assistantIdx] = { role: 'assistant', content: (data as Record<string, string>).content || (data as Record<string, string>).error || t('chat.load_fail'), functionCalls: [] }
          return n
        })
        return
      }

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      let current: ChatMessage = { role: 'assistant', content: '', functionCalls: [] }
      // Effective thread id for this stream — the closure's threadId is stale
      // for a brand-new thread (empty at send time); the 'thread' event below
      // fills it in. Used to live-refetch the mission ledger as the turn runs.
      let streamThreadId = threadId

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })

        const lines = buffer.split('\n')
        buffer = lines.pop() || ''
        for (const line of lines) {
          if (!line.startsWith('data: ')) continue
          const data = line.slice(6)
          if (data === '[DONE]') continue
          try {
            const evt = JSON.parse(data) as Record<string, unknown>
            if (evt.type === 'thread' && evt.threadId) {
              streamThreadId = evt.threadId as string
              setThreadId(evt.threadId as string)
              // Reconcile mode if backend reported a different agentType
              const incomingAgentType = evt.agentType as string | undefined
              if (incomingAgentType === 'builder' || incomingAgentType === 'lakehouse') {
                if (incomingAgentType !== mode) {
                  setMode(incomingAgentType)
                }
              }
              // URL update is handled by the mode/threadId useEffect
            } else if (evt.type === 'todo_update') {
              const items = evt.items as Array<{ id: string; text: string; status: string }>
              if (Array.isArray(items)) setTodoItems(items)
            } else if (evt.type === 'token') {
              current = { ...current, content: current.content + ((evt.content as string) || '') }
            } else if (evt.type === 'thinking') {
              current = { ...current, thinking: (current.thinking || '') + ((evt.content as string) || '') }
            } else if (evt.type === 'error') {
              current = { ...current, content: current.content || ((evt.content as string) || t('chat.load_fail')) }
            } else if (evt.type === 'function_call') {
              const fc = {
                name: (evt.name as string) || '',
                arguments: (evt.arguments as Record<string, unknown>) || {},
                result: evt.result as FunctionCall['result'],
              }
              current = { ...current, functionCalls: [...current.functionCalls, fc] }
              const toolKey = `${assistantIdx}-${current.functionCalls.length - 1}`
              setExpandedTools(prev => new Set(prev).add(toolKey))
              if (fc.result?.involved && (fc.name === 'lookup' || fc.name === 'smartquery')) {
                setGraphHighlight(fc.result.involved as GraphHighlight)
              }
              const resObj = fc.result as { switchTo?: string } | null
              if (resObj?.switchTo) {
                if (fc.name === 'clarify_and_branch') {
                  pendingBranchSwitch.current = { kind: 'push', targetId: resObj.switchTo, parentId: threadId, parentTitle: t('thread.main_title') }
                } else if (fc.name === 'return_to_parent') {
                  pendingBranchSwitch.current = { kind: 'pop', targetId: resObj.switchTo }
                }
              }
            } else if (evt.type === 'done') {
              if (evt.promptTokens) current = { ...current, promptTokens: evt.promptTokens as number, completionTokens: evt.completionTokens as number, totalTokens: evt.totalTokens as number }
              if (evt.modelName) current = { ...current, modelName: evt.modelName as string }
            } else if (evt.type === 'recall') {
              // 分词 + 召回 straight from the agent's pipeline — populates the
              // 任务/诊断 panels without any extra client HTTP call.
              setStreamRecall({ tokens: (evt.tokens as string[]) || [], recall: evt.recall as RecallResult })
            }
            setMessages(prev => {
              const n = [...prev]
              n[assistantIdx] = { ...current }
              return n
            })
            // Live-refresh the 任务可达器 panel on every structural SSE event
            // (a new turn step, tool call, todo update, done) — but NOT on the
            // streaming text events (token / thinking), which fire per-character
            // and would hammer the endpoint. The reachability verdict lands
            // early in the turn, so this surfaces it without waiting for 'done'.
            if (evt.type !== 'token' && evt.type !== 'thinking') {
              void refetchMissions(streamThreadId)
            }
          } catch { /* skip */ }
        }
      }
    } catch (e) {
      if (e instanceof Error && e.name !== 'AbortError') {
        msg.error(e.message || t('chat.load_fail'))
      }
    } finally {
      setLoading(false)
      const pending = pendingBranchSwitch.current
      pendingBranchSwitch.current = null
      if (pending) {
        if (pending.kind === 'push') {
          setBranchStack(prev => [...prev, { threadId: pending.parentId, title: pending.parentTitle || t('thread.main_title') }])
          await loadThread(pending.targetId)
        } else {
          setBranchStack(prev => prev.slice(0, -1))
          await loadThread(pending.targetId)
        }
      }
      if (showMemory && threadId) {
        void fetchLedger(threadId)
      }
      // MissionAct — turn just ended; refetch the mission so the panel
      // picks up the latest reachability / synthesis / status. Without
      // this, the panel shows the early snapshot taken at threadId-mount
      // time, before reachability was persisted.
      if (threadId) {
        void refetchMissions(threadId)
      }
    }
  }

  const toggleTool = (key: string) => setExpandedTools(prev => { const n = new Set(prev); n.has(key) ? n.delete(key) : n.add(key); return n })
  const toggleThinking = (idx: number) => setExpandedThinking(prev => { const n = new Set(prev); n.has(idx) ? n.delete(idx) : n.add(idx); return n })

  // Current question's 分词 — taken straight from the agent's 'recall' SSE event
  // (no extra HTTP). strongHit = the token matched at least one keyword.
  // MUST stay ABOVE the early return below: putting a hook after the
  // `if (loadingThread)` return changes the hook count between renders and
  // crashes with React #300 when a thread loads.
  const currentTurnTokens = useMemo(() => {
    if (!streamRecall) return []
    const td = streamRecall.recall?.tokenDetails || {}
    return streamRecall.tokens.map(t => ({ token: t, strongHit: (td[t]?.length ?? 0) > 0 }))
  }, [streamRecall])

  if (loadingThread) return <div className="flex h-64 items-center justify-center"><CyberLoader /></div>

  // Last streaming message (for fade-in during active SSE)
  const lastMessage = loading && messages.length > 0 ? messages[messages.length - 1] : null

  return (
    <div className="flex flex-col overflow-hidden h-full">
      {/* Header */}
      <div className={`flex h-14 items-center justify-between px-4 flex-shrink-0 bg-white ${industrial ? 'border-b-2 border-ink' : 'border-b border-gray-200'}`}>
        <div className="flex items-center gap-3">
          {industrial ? (
            // min-width pins the title cell so the [QUERY|BUILDER] toggle to its
            // right doesn't shift horizontally when the label changes length
            // (e.g. `// LAKEHOUSE AGENT` 18ch vs `// 本体构造助手` ~12ch).
            <span className="inline-block min-w-[15rem] font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // {(mode === 'builder' ? t('header.title_builder') : t('header.title_lakehouse')).toString().toUpperCase()}
            </span>
          ) : (
            <>
              <BookOpen className="h-5 w-5 text-gray-600" />
              <h1 className="inline-block min-w-[10rem] text-base font-semibold text-gray-900">
                {mode === 'builder' ? t('header.title_builder') : t('header.title_lakehouse')}
              </h1>
            </>
          )}
          <div className={`flex overflow-hidden ${industrial ? 'border border-ink' : 'border border-gray-200'}`}>
            <button
              onClick={() => { if (mode !== 'lakehouse') { setMode('lakehouse'); setThreadId(''); setMessages([]) } }}
              className={
                industrial
                  ? `font-mono text-[10px] tracking-[0.18em] px-3 py-1 transition-colors ${mode === 'lakehouse' ? 'bg-ink text-white' : 'text-ink-muted hover:text-ink'}`
                  : `text-xs px-3 py-1 transition-colors ${mode === 'lakehouse' ? 'bg-gray-900 text-white font-semibold' : 'text-gray-500 hover:text-gray-800 hover:bg-gray-50'}`
              }
            >
              {industrial ? t('header.mode_query').toString().toUpperCase() : t('header.mode_query')}
            </button>
            <button
              onClick={() => { if (mode !== 'builder') { setMode('builder'); setThreadId(''); setMessages([]) } }}
              className={
                industrial
                  ? `font-mono text-[10px] tracking-[0.18em] px-3 py-1 transition-colors border-l border-ink ${mode === 'builder' ? 'bg-ink text-white' : 'text-ink-muted hover:text-ink'}`
                  : `text-xs px-3 py-1 transition-colors border-l border-gray-200 ${mode === 'builder' ? 'bg-gray-900 text-white font-semibold' : 'text-gray-500 hover:text-gray-800 hover:bg-gray-50'}`
              }
            >
              {industrial ? t('header.mode_builder').toString().toUpperCase() : t('header.mode_builder')}
            </button>
          </div>
          {branchStack.length > 0 && (
            <div className="flex items-center gap-1.5 text-xs border-l border-gray-200 pl-3 ml-1">
              {branchStack.map((b, i) => (
                <span key={b.threadId} className="flex items-center gap-1.5">
                  <button
                    className="text-gray-400 hover:text-blue-600 underline decoration-dotted"
                    onClick={async () => {
                      setBranchStack(prev => prev.slice(0, i))
                      await loadThread(b.threadId)
                    }}
                  >
                    {b.title}
                  </button>
                  <span className="text-gray-300">›</span>
                </span>
              ))}
              <span className="text-blue-600 font-medium">{t('header.branch_label')}{ambiguousKeyword ? `（${ambiguousKeyword}）` : ''}</span>
            </div>
          )}
          {threadId && <span className="text-[10px] text-gray-400 font-mono">#{threadId.slice(0, 8)}</span>}
          {mode === 'lakehouse' && threadId && (
            <button
              onClick={() => setShowMemory(v => !v)}
              className={`flex items-center gap-1 px-2 py-0.5 text-[10px] rounded-full border transition-colors ${showMemory ? 'border-blue-300 text-blue-600 bg-blue-50' : 'border-gray-200 text-gray-500 hover:text-blue-600 hover:border-blue-300'}`}
              title={t('header.memory_title')}
            >
              <Brain className="h-3 w-3" />
              <span>{t('header.memory_btn')}</span>
              {ledger?.summary && (
                <span className="text-gray-400">
                  {ledger.summary.odCount}Od·{ledger.summary.intentCount}It·{ledger.summary.strongTokenCount}/{ledger.summary.tokenCount}tk
                </span>
              )}
            </button>
          )}
          {threadStatus === 'suspended' && (
            <span className="text-xs px-2 py-0.5 rounded bg-amber-50 border border-amber-200 text-amber-600">{t('header.suspended_badge')}</span>
          )}
          {(() => {
            // Show the model that produced the last assistant turn. If a config
            // exists with this modelName and has an alias, prefer the alias —
            // keeps the header consistent with the Agent LLM selector above.
            const last = [...messages].reverse().find(m => m.modelName)
            if (!last?.modelName) return null
            const cfg = chatConfigs.find(c => c.modelName === last.modelName)
            const text = (cfg?.alias?.trim()) || last.modelName
            return <span className="text-[10px] text-ink-ghost" title={cfg ? `${cfg.vendor} / ${cfg.modelName}` : last.modelName}>{text}</span>
          })()}
        </div>
        <div className="flex items-center gap-2">
          {/* Agent LLM 选择器 — 写入 role binding，下一轮发送即生效.
              Display rule: alias if user set one, else vendor/modelName.
              Hover-tooltip surfaces the technical identity when alias hides it. */}
          {chatConfigs.length > 0 && (() => {
            const selectedConfig = chatConfigs.find(c => c.id === agentSelectedId)
            const tooltip = agentBindingId
              ? t('header.agent_llm_bound_tooltip', { vendor: selectedConfig?.vendor ?? '', modelName: selectedConfig?.modelName ?? '' })
              : t('header.agent_llm_unbound_tooltip')
            return (
              <label className="flex items-center gap-1.5">
                <span className="text-[10px] font-semibold uppercase tracking-wider text-ink-ghost">Agent LLM</span>
                <select
                  value={agentSelectedId}
                  onChange={(e) => onAgentLLMChange(e.target.value)}
                  title={tooltip}
                  className="max-w-[220px] rounded border border-border bg-white px-2 py-1 text-xs text-ink transition-colors hover:border-ink-strong focus:border-ink focus:outline-none"
                >
                  <option value="">{t('header.agent_llm_unbound')}</option>
                  {chatConfigs.map(c => (
                    <option key={c.id} value={c.id}>
                      {llmDisplay(c)}{c.isActive ? ' ★' : ''}
                    </option>
                  ))}
                </select>
              </label>
            )
          })()}
          <Button variant="ghost" size="sm" onClick={() => setGraphFullscreen(!graphFullscreen)}>
            {graphFullscreen ? <Minimize2 className="h-3.5 w-3.5 mr-1" /> : <Maximize2 className="h-3.5 w-3.5 mr-1" />}
            {graphFullscreen ? t('header.graph_exit_fullscreen') : t('header.graph_fullscreen')}
          </Button>
          <Button variant="ghost" size="sm" onClick={startNewThread}>{t('header.new_thread')}</Button>
          <button onClick={openHistory} className="border border-gray-200 rounded px-3 py-1.5 text-sm text-gray-500 hover:text-gray-800 hover:border-gray-400 transition-colors">{t('header.history')}</button>
        </div>
      </div>

      {/* Body: Chat (primary, left) + diagnostics panel (right). flex-row-reverse
          keeps the chat — where you act — on the left while the panel's DOM stays
          after it; the panel opens on the 诊断 tab (graph / mission one click away). */}
      <div className="flex flex-row-reverse flex-1 min-h-0">
        {/* History panel — PUSHES the workbench left (not an overlay): in
            flex-row-reverse, being the first DOM child renders it on the far
            right, and the flex-1 chat shrinks to make room. Pick a past thread
            in place; the panel stays open so you can browse. */}
        {historyOpen && (
          <div className="flex w-[320px] flex-shrink-0 flex-col overflow-hidden border-l border-gray-200 bg-white">
            <div className="flex h-14 flex-shrink-0 items-center justify-between border-b border-gray-200 bg-gray-50 px-3">
              <div className="flex items-center gap-2">
                <History className="h-4 w-4 text-gray-600" />
                <span className="text-sm font-semibold text-gray-800">{t('header.history')}</span>
              </div>
              <div className="flex items-center gap-1">
                <Link
                  href={`/ontology/lakehouse-agent/history?mode=${mode}`}
                  onClick={() => setHistoryOpen(false)}
                  className="inline-flex items-center gap-1 rounded border border-transparent px-2 py-1 text-[10px] text-gray-500 hover:border-gray-200 hover:bg-white hover:text-blue-600"
                  title={tw('history_open_full')}
                >
                  <ExternalLink className="h-3 w-3" />{tw('history_open_full')}
                </Link>
                <button onClick={() => setHistoryOpen(false)} className="p-1 text-gray-400 hover:text-gray-700" title={t('ledger.close')}>
                  <X className="h-4 w-4" />
                </button>
              </div>
            </div>
            <div className="flex-1 overflow-y-auto">
              {historyLoading && <div className="p-4 text-sm text-gray-500">…</div>}
              {!historyLoading && historyThreads.filter(th => (th.agentType || 'lakehouse') === mode).length === 0 && (
                <div className="p-4 text-sm text-gray-500">{tw('history_empty')}</div>
              )}
              {!historyLoading && (
                <div className="divide-y divide-gray-100">
                  {historyThreads
                    .filter(th => (th.agentType || 'lakehouse') === mode)
                    .map(th => (
                      <button
                        key={th.id}
                        onClick={() => { void loadThread(th.id) }}
                        className={`flex w-full flex-col items-start gap-0.5 px-4 py-2.5 text-left transition-colors hover:bg-gray-50 ${th.id === threadId ? 'bg-gray-50' : ''}`}
                      >
                        <span className="w-full truncate text-sm text-gray-800">{th.title || tw('history_no_title')}</span>
                        <span className="text-[11px] tabular-nums text-gray-400">{new Date(th.updatedAt).toLocaleString('zh-CN')}</span>
                      </button>
                    ))}
                </div>
              )}
            </div>
          </div>
        )}
        {/* Right pane — tabbed: 诊断 (default) / 图谱 / 任务. */}
        {(
        <div className={`${graphFullscreen ? 'w-full' : 'w-[46%]'} border-l border-gray-200 flex flex-col overflow-hidden`}>
          {/* Panel tab bar */}
          <div className={`flex items-center gap-0 px-2 flex-shrink-0 bg-white ${industrial ? 'border-b border-ink' : 'border-b border-gray-200'}`}>
            {([
              ['mission', tw('tab_mission')],
              ['diagnose', tw('tab_diagnose')],
              ['graph', tw('tab_graph')],
            ] as const).map(([key, label]) => {
              const active = panelTab === key
              return (
                <button
                  key={key}
                  onClick={() => setPanelTab(key)}
                  className={`-mb-px h-9 px-3 border-b-2 transition-colors ${
                    industrial ? 'font-mono text-[11px] uppercase tracking-[0.12em]' : 'text-xs'
                  } ${active ? 'border-ink font-semibold text-ink' : 'border-transparent text-gray-500 hover:text-ink'}`}
                >
                  {label}
                </button>
              )
            })}
          </div>

          {/* 诊断 tab — diagnose-first default. */}
          {panelTab === 'diagnose' && (
            <DiagnosePanel
              streamRecall={streamRecall}
              graphHighlight={graphHighlight}
            />
          )}

          {/* 图谱 tab */}
          {panelTab === 'graph' && (
          <>
          {/* Graph toolbar */}
          <div className="flex items-center gap-3 px-3 py-1.5 border-b border-gray-200 bg-gray-50 flex-shrink-0">
            <div className="flex gap-3">
              <span className="text-[10px] text-gray-400">Objects: {markedObjects.length}</span>
              <span className="text-[10px] text-gray-400">Links: {joinKeyLinks.length}</span>
            </div>
            {graphHighlight && (
              <div className="flex items-center gap-1.5">
                <span className="text-[10px] font-semibold text-blue-600">
                  {graphHighlight.kind === 'lookup' ? '⊙ LOOKUP' : '▶ QUERY'}: {graphHighlight.odNames.join(', ')}
                </span>
                <button onClick={() => setGraphHighlight(null)} className="text-[9px] text-gray-400 hover:text-gray-600 border border-gray-200 rounded px-1 py-0.5">{t('graph_toolbar.show_all')}</button>
              </div>
            )}
            <div className="ml-auto flex border border-gray-200 rounded overflow-hidden">
              {([
                ['force-all',      t('graph_toolbar.layout_current'),  t('graph_toolbar.layout_force_title')],
                ['circular-all',   t('graph_toolbar.layout_a_all'),    t('graph_toolbar.layout_a_all_title')],
                ['circular-od',    t('graph_toolbar.layout_b_od'),     t('graph_toolbar.layout_b_od_title')],
                ['circular-od-ol', t('graph_toolbar.layout_c_od_ol'),  t('graph_toolbar.layout_c_od_ol_title')],
                ['force-webkit',   t('graph_toolbar.layout_d_webkit'), t('graph_toolbar.layout_d_webkit_title')],
              ] as Array<[GraphLayoutMode, string, string]>).map(([mode, label, title]) => (
                <button
                  key={mode}
                  onClick={() => setGraphLayoutMode(mode)}
                  title={title}
                  className={`text-[10px] px-2 py-0.5 border-r border-gray-200 last:border-r-0 transition-colors ${
                    graphLayoutMode === mode
                      ? 'bg-gray-800 text-white font-semibold'
                      : 'text-gray-400 hover:text-gray-700 hover:bg-gray-100'
                  }`}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>

          {/* ECharts Graph — top half of the left pane (50/50 with the
              reachability panel below; full height when graph is fullscreen). */}
          <OntologyGraph
            className="flex-1 min-h-0 w-full"
            markedObjects={markedObjects}
            objects={objects}
            odLinks={odLinks}
            knowledge={knowledge}
            causalities={causalities}
            learnedFacts={learnedFacts}
            factLinks={factLinks}
            highlight={graphHighlight}
            layoutMode={graphLayoutMode}
          />
          </>
          )}

          {/* 任务 tab — the mission ledger (任务可达器). The current question's 分词
              is rendered inside the mission card, directly under the question
              (passed as `tokens`), surfaced live from the ledger as the agent
              runs — no manual trigger. */}
          {panelTab === 'mission' && (
            <div className="flex-1 flex flex-col min-h-0 overflow-hidden">
              <MissionLedger missions={missions} tokens={currentTurnTokens} onRefresh={() => refetchMissions(threadId)} />
            </div>
          )}
        </div>
        )}

        {/* Chat — the primary column (left). Hidden only when the panel is
            fullscreened. */}
        {!graphFullscreen && (
          <div className="flex-1 min-w-0 flex flex-col overflow-hidden bg-white">
            {/* Plan strip */}
            {todoItems.length > 0 && (
              <div className="border-b border-gray-200 px-3 py-2 flex-shrink-0 bg-gray-50">
                <div className="text-[10px] text-gray-400 mb-1 font-semibold uppercase tracking-wider">Plan</div>
                <div className="space-y-0.5">
                  {todoItems.map((item) => (
                    <div key={item.id} className={`flex items-center gap-1.5 text-xs ${item.status === 'completed' ? 'text-gray-400 line-through' : item.status === 'in_progress' ? 'text-blue-600 font-semibold' : 'text-gray-500'}`}>
                      <span className={`inline-block w-3 h-3 rounded border text-center text-[8px] leading-3 flex-shrink-0 ${item.status === 'completed' ? 'bg-gray-400 text-white border-gray-400' : item.status === 'in_progress' ? 'border-blue-400 bg-blue-50' : 'border-gray-300'}`}>
                        {item.status === 'completed' ? '✓' : item.status === 'in_progress' ? '▶' : ''}
                      </span>
                      <span>{item.text}</span>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Chat messages — STREAMING GATE: MotionGroup disabled when loading */}
            <div className="flex-1 overflow-y-auto overflow-x-hidden px-4 py-4">
              {messages.length === 0 && (
                <MotionFade className="flex h-full flex-col items-center justify-center gap-3 text-gray-400">
                  {mode === 'lakehouse' ? (
                    // Lakehouse home: brand logo + ontology-health dashboard
                    // (objects / keywords / example questions / described-prop
                    // ratio). Replaces the old starter-question chips. Lives in
                    // the messages.length===0 gate, so it disappears the instant
                    // a conversation starts.
                    <HomeDashboard />
                  ) : (
                    <>
                      <BookOpen className="h-10 w-10 text-gray-300" />
                      <span className="whitespace-nowrap text-sm font-medium text-gray-500">
                        {t('chat.empty_title_builder')}
                      </span>
                      <span className="block w-72 min-h-[3rem] text-xs text-gray-400 text-center">
                        {t('chat.empty_hint_builder')}
                      </span>
                    </>
                  )}
                </MotionFade>
              )}

              {/* Message list: stagger on idle, no animation during streaming */}
              <MotionGroup disabled={loading} staggerMs={60} className="space-y-5">
                {messages.map((m, i) => {
                  const isStreamingLast = loading && i === messages.length - 1

                  const messageContent = (
                    <div className={`flex gap-3 ${m.role === 'user' ? 'justify-end' : 'justify-start'}`}>
                      {m.role === 'assistant' && (
                        <div className="flex-shrink-0 mt-0.5">
                          <div className="w-7 h-7 rounded-full bg-gray-100 flex items-center justify-center">
                            <Bot className="h-4 w-4 text-gray-500" />
                          </div>
                        </div>
                      )}

                      <div className={`max-w-[88%] min-w-0 space-y-2 ${m.role === 'user' ? 'items-end' : 'items-start'} flex flex-col`}>
                        {/* User message bubble */}
                        {m.role === 'user' && (() => {
                          const { display, annotation, hasAnnotation } = parseUserMessage(m.content)
                          return (
                            <div className="space-y-1">
                              <div className="flex items-end justify-end gap-2">
                                {hasAnnotation && (
                                  <button
                                    onClick={() => setExpandedAnnotations(prev => { const n = new Set(prev); n.has(i) ? n.delete(i) : n.add(i); return n })}
                                    className={`text-[9px] px-1.5 py-0.5 rounded border transition-colors ${expandedAnnotations.has(i) ? 'border-blue-300 text-blue-600 bg-blue-50' : 'border-gray-200 text-gray-400 hover:text-gray-600'}`}
                                  >
                                    {t('chat.annotation_label')}
                                  </button>
                                )}
                                <div className="inline-block bg-gray-900 text-white rounded-2xl rounded-br-sm px-4 py-2.5 text-sm">{display}</div>
                              </div>
                              {hasAnnotation && expandedAnnotations.has(i) && (
                                <MotionFade className="border border-gray-200 bg-gray-50 rounded px-2 py-1.5 max-h-36 overflow-y-auto">
                                  <StreamMarkdown content={annotation} className="text-[10px] text-gray-500" />
                                </MotionFade>
                              )}
                            </div>
                          )
                        })()}

                        {/* Thinking */}
                        {m.role === 'assistant' && m.thinking && (
                          <button onClick={() => toggleThinking(i)} className="flex items-center gap-1 text-[10px] text-gray-400 hover:text-gray-600">
                            {expandedThinking.has(i) ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}{t('chat.thinking_label')}
                          </button>
                        )}
                        {m.role === 'assistant' && m.thinking && expandedThinking.has(i) && (
                          <MotionFade>
                            <pre className="bg-gray-50 border border-gray-200 rounded px-2 py-1.5 text-[10px] text-gray-500 whitespace-pre-wrap max-h-20 overflow-y-auto">{m.thinking}</pre>
                          </MotionFade>
                        )}

                        {/* Tool calls — spring-expand with MotionScale */}
                        {m.role === 'assistant' && m.functionCalls.map((fc, fi) => {
                          if (fc.name === 'propose_od' && fc.result?.pending_confirmation) {
                            return (
                              <BuilderProposeOdCard
                                key={fi}
                                fc={fc}
                                projectId={currentProject?.id}
                              />
                            )
                          }
                          if (fc.name === 'propose_intent' && fc.result?.pending_confirmation) {
                            return (
                              <BuilderProposeIntentCard
                                key={fi}
                                fc={fc}
                                projectId={currentProject?.id}
                              />
                            )
                          }
                          if (fc.name === 'propose_link' && fc.result?.pending_confirmation) {
                            return (
                              <BuilderProposeLinkCard
                                key={fi}
                                fc={fc}
                                projectId={currentProject?.id}
                              />
                            )
                          }
                          return (
                            <ToolCard
                              key={fi}
                              fc={fc}
                              expanded={expandedTools.has(`${i}-${fi}`)}
                              onToggle={() => toggleTool(`${i}-${fi}`)}
                              onGotoBranch={async (childId) => {
                                if (!childId || childId === threadId) return
                                setBranchStack(prev => [...prev, { threadId, title: t('thread.main_title') }])
                                await loadThread(childId)
                              }}
                              toolMeta={toolMeta}
                            />
                          )
                        })}

                        {/* Assistant text — fade-in only for the streaming last message */}
                        {m.role === 'assistant' && m.content && (
                          isStreamingLast ? (
                            <MotionFade key={`stream-${i}`} className="bg-white rounded-2xl rounded-tl-sm px-4 py-3 text-gray-800 text-sm leading-relaxed shadow-sm border border-gray-100">
                              <AnswerBody content={m.content} functionCalls={m.functionCalls} />
                              {loading && <StreamingDot />}
                            </MotionFade>
                          ) : (
                            <div className="bg-white rounded-2xl rounded-tl-sm px-4 py-3 text-gray-800 text-sm leading-relaxed shadow-sm border border-gray-100">
                              <AnswerBody content={m.content} functionCalls={m.functionCalls} />
                            </div>
                          )
                        )}

                        {/* Empty streaming assistant placeholder */}
                        {m.role === 'assistant' && !m.content && m.functionCalls.length === 0 && loading && i === messages.length - 1 && (
                          <div className="flex items-center gap-2 text-sm text-gray-400 px-1">
                            {t('chat.streaming')}<StreamingDot />
                          </div>
                        )}

                        {/* Tokens — per-message LLM usage. Backend emits
                            promptTokens/completionTokens/totalTokens on the
                            `done` SSE event; surfaced here so each answer
                            carries its own cost trace. */}
                        {m.role === 'assistant' && m.totalTokens != null && m.totalTokens > 0 && (
                          <div className={`mt-1 flex items-center gap-1.5 ${
                            industrial
                              ? 'font-mono text-[10px] tracking-[0.08em] text-ink-muted'
                              : 'text-[10px] text-gray-400'
                          }`}>
                            <span className={industrial ? 'text-ink-ghost' : 'text-gray-400'}>
                              {industrial ? '// TOKENS' : 'tokens'}
                            </span>
                            <span className="tabular-nums">
                              {m.promptTokens}<span className="text-ink-ghost"> in</span>
                              {' · '}{m.completionTokens}<span className="text-ink-ghost"> out</span>
                              {' · '}<span className="font-semibold">{m.totalTokens}</span><span className="text-ink-ghost"> total</span>
                            </span>
                            {m.modelName && (
                              <span className={`ml-1 ${industrial ? 'text-ink-ghost uppercase tracking-[0.1em]' : 'text-gray-400'}`}>
                                {m.modelName}
                              </span>
                            )}
                          </div>
                        )}
                      </div>

                      {m.role === 'user' && (
                        <div className="flex-shrink-0 mt-0.5">
                          <div className="w-7 h-7 rounded-full bg-gray-200 flex items-center justify-center">
                            <User className="h-4 w-4 text-gray-500" />
                          </div>
                        </div>
                      )}
                    </div>
                  )

                  return (
                    <MotionGroupItem key={i}>
                      {/* Thread switch: shared layout layoutId per message */}
                      <motion.div layoutId={`msg-${threadId}-${i}`}>
                        {messageContent}
                      </motion.div>
                    </MotionGroupItem>
                  )
                })}
              </MotionGroup>

              {/* Streaming indicator when no messages yet in this stream turn */}
              {loading && messages.length > 0 && messages[messages.length - 1].role === 'user' && (
                <MotionFade className="flex gap-3 justify-start mt-5">
                  <div className="w-7 h-7 rounded-full bg-gray-100 flex items-center justify-center flex-shrink-0">
                    <Bot className="h-4 w-4 text-gray-400" />
                  </div>
                  <div className="flex items-center gap-2 text-sm text-gray-400 px-1">
                    {t('chat.thinking_label')}<StreamingDot />
                  </div>
                </MotionFade>
              )}

              <div ref={bottomRef} />
            </div>

            {/* Input — bar height matches the sidebar footer (user/logout) row
                so the bottom gridline aligns left-to-right. */}
            <div className="flex h-14 items-center gap-2 px-4 border-t border-gray-200 flex-shrink-0 bg-white">
              <input
                ref={chatInputRef}
                className="h-9 flex-1 border border-gray-200 rounded-xl px-4 text-sm focus:border-gray-400 focus:outline-none disabled:bg-gray-50 disabled:cursor-not-allowed transition-colors"
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); handleSend() } }}
                placeholder={mode === 'builder' ? t('chat.placeholder_builder') : (threadStatus === 'suspended' ? t('chat.placeholder_suspended') : t('chat.placeholder_default'))}
                disabled={loading || threadStatus === 'suspended'}
              />
              {loading ? (
                <button
                  onClick={() => { abortCtrlRef.current?.abort(); setLoading(false) }}
                  className="bg-red-500 text-white px-3 py-2 rounded-xl hover:bg-red-600 transition-colors"
                  title={t('chat.stop_title')}
                >
                  <Square className="h-4 w-4 fill-white" />
                </button>
              ) : (
                <button
                  onClick={() => handleSend()}
                  disabled={!input.trim() || threadStatus === 'suspended'}
                  className="bg-gray-900 text-white px-3 py-2 rounded-xl hover:bg-gray-700 disabled:opacity-30 transition-colors"
                >
                  <Send className="h-4 w-4" />
                </button>
              )}
            </div>
          </div>
        )}
      </div>

      {/* Thread Memory Ledger drawer (minimal theme) */}
      {showMemory && (
        <div className="fixed top-0 right-0 h-full w-[420px] bg-white border-l border-gray-200 z-40 shadow-xl flex flex-col">
          <div className="flex items-center justify-between px-3 py-2.5 border-b border-gray-200 bg-gray-50">
            <div className="flex items-center gap-2">
              <Brain className="h-4 w-4 text-blue-600" />
              <span className="text-sm font-semibold text-gray-800">{t('ledger.title')}</span>
            </div>
            <div className="flex items-center gap-1">
              <button
                onClick={() => threadId && fetchLedger(threadId)}
                disabled={ledgerLoading}
                className="text-[10px] text-gray-500 hover:text-blue-600 px-2 py-1 rounded hover:bg-white border border-transparent hover:border-gray-200 disabled:opacity-50"
              >
                {ledgerLoading ? '…' : t('ledger.refresh')}
              </button>
              <button onClick={() => setShowMemory(false)} className="text-gray-400 hover:text-gray-700 p-1" title={t('ledger.close')}>
                <X className="h-4 w-4" />
              </button>
            </div>
          </div>

          <div className="flex-1 overflow-y-auto">
            {!threadId && (
              <div className="p-4 text-sm text-gray-500">{t('ledger.no_thread')}</div>
            )}
            {threadId && !ledger && !ledgerLoading && (
              <div className="p-4 text-sm text-gray-500">{t('ledger.no_data')}</div>
            )}
            {threadId && ledger && (
              <div className="divide-y divide-gray-100">
                {/* Summary */}
                <div className="px-4 py-3 bg-gray-50">
                  <div className="text-[10px] uppercase tracking-wider text-gray-400 mb-2">Summary</div>
                  <div className="grid grid-cols-3 gap-1.5">
                    <div className="border border-gray-200 rounded px-2 py-1 bg-white">
                      <div className="text-[9px] text-gray-400">{t('ledger.turns')}</div><div className="text-sm font-semibold text-gray-800">{ledger.summary.turnCount}</div>
                    </div>
                    <div className="border border-gray-200 rounded px-2 py-1 bg-white">
                      <div className="text-[9px] text-gray-400">{t('ledger.version')}</div><div className="text-sm font-semibold text-gray-800">v{ledger.summary.version}</div>
                    </div>
                    <div className="border border-gray-200 rounded px-2 py-1 bg-white">
                      <div className="text-[9px] text-gray-400">Od</div><div className="text-sm font-semibold text-blue-600">{ledger.summary.odCount}</div>
                    </div>
                    <div className="border border-gray-200 rounded px-2 py-1 bg-white">
                      <div className="text-[9px] text-gray-400">Metric</div><div className="text-sm font-semibold text-blue-600">{ledger.summary.intentCount}</div>
                    </div>
                    <div className="border border-gray-200 rounded px-2 py-1 bg-white">
                      <div className="text-[9px] text-gray-400">Token</div><div className="text-sm font-semibold text-gray-800">{ledger.summary.strongTokenCount}/{ledger.summary.tokenCount}</div>
                    </div>
                    <div className="border border-gray-200 rounded px-2 py-1 bg-white">
                      <div className="text-[9px] text-gray-400">Ok/Ol</div><div className="text-sm font-semibold text-gray-800">{ledger.summary.okCount}/{ledger.summary.olCount}</div>
                    </div>
                  </div>
                  {ledger.summary.rebuiltFromStep > 0 && (
                    <div className="mt-2 text-[10px] text-gray-400">{t('ledger.rebuilt_from', { step: ledger.summary.rebuiltFromStep })}</div>
                  )}
                  {ledger.summary.versionDrift && (
                    <div className="mt-2 text-[10px] text-amber-600">{t('ledger.version_drift')}</div>
                  )}
                </div>

                {/* Ods */}
                {ledger.ods.length > 0 && (
                  <div className="px-4 py-3">
                    <div className="text-[10px] uppercase tracking-wider text-gray-400 mb-2">Ods · {ledger.ods.length}</div>
                    <div className="space-y-1.5">
                      {ledger.ods.map(od => (
                        <div key={od.odId} className={`rounded border px-2.5 py-1.5 ${od.versionStale ? 'border-amber-200 bg-amber-50/40' : 'border-gray-200 bg-white'}`}>
                          <div className="flex items-center gap-2">
                            <span className="inline-block w-1.5 h-1.5 rounded-full bg-gray-400 flex-shrink-0" />
                            <span className="text-sm font-medium text-gray-800 truncate">{od.name}</span>
                            <span className="text-[10px] text-gray-400 px-1 rounded bg-gray-100">{od.kind}</span>
                            <span className="text-[10px] text-gray-400 ml-auto">T{od.loadedInTurn} · {od.loadMethod}</span>
                          </div>
                          {od.description && (
                            <div className="text-xs text-gray-500 mt-0.5 truncate" title={od.description}>{od.description}</div>
                          )}
                          <div className="text-[11px] text-gray-500 mt-0.5">
                            props: <span className="text-gray-800">{od.matchedPropsCount}/{od.allPropNamesCount}</span>
                            {od.linkCount > 0 && <span> · links: <span className="text-gray-800">{od.linkCount}</span></span>}
                            {od.versionStale && <span className="text-amber-600"> · {t('ledger.od_version_stale')}</span>}
                          </div>
                          {od.matchedPropNames.length > 0 && (
                            <div className="mt-1.5 flex flex-wrap gap-1">
                              {od.matchedPropNames.slice(0, 8).map((n, i) => (
                                <span key={i} className="rounded bg-gray-100 text-gray-600 px-1.5 py-0.5 text-[10px]">{n}</span>
                              ))}
                              {od.matchedPropNames.length > 8 && (
                                <span className="text-[10px] text-gray-400 py-0.5">+{od.matchedPropNames.length - 8}</span>
                              )}
                            </div>
                          )}
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {/* Intents */}
                {ledger.intents.length > 0 && (
                  <div className="px-4 py-3">
                    <div className="text-[10px] uppercase tracking-wider text-gray-400 mb-2">Metrics · {ledger.intents.length}</div>
                    <div className="space-y-1.5">
                      {ledger.intents.map(mi => (
                        <div key={mi.intentId} className={`rounded border px-2.5 py-1.5 ${mi.versionStale ? 'border-amber-200 bg-amber-50/40' : 'border-gray-200 bg-white'}`}>
                          <div className="flex items-center gap-2">
                            <span className="inline-block w-1.5 h-1.5 rounded-full bg-blue-500 flex-shrink-0" />
                            <span className="text-sm font-medium text-gray-800 truncate">{mi.name}</span>
                            <span className="text-[10px] text-gray-400 ml-auto">T{mi.firstSeenInTurn}</span>
                          </div>
                          <div className="text-[11px] text-gray-500 mt-0.5 truncate" title={mi.canonicalMetric}>
                            metric: <span className="text-gray-800">{mi.canonicalMetric}</span>
                          </div>
                          {mi.matchedTokens.length > 0 && (
                            <div className="mt-1.5 flex flex-wrap gap-1">
                              {mi.matchedTokens.map((t, i) => (
                                <span key={i} className="rounded bg-blue-50 text-blue-600 px-1.5 py-0.5 text-[10px] border border-blue-100">{t}</span>
                              ))}
                            </div>
                          )}
                          {mi.versionStale && (
                            <div className="mt-1 text-[10px] text-amber-600">{t('ledger.intent_version_stale')}</div>
                          )}
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {/* Tokens */}
                {ledger.tokens.length > 0 && (
                  <div className="px-4 py-3">
                    <div className="text-[10px] uppercase tracking-wider text-gray-400 mb-2">Tokens · {ledger.tokens.length}</div>
                    <div className="flex flex-wrap gap-1">
                      {ledger.tokens.map(t => (
                        <span
                          key={t.token}
                          className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] border ${t.strongHit ? 'border-blue-200 bg-blue-50 text-blue-700' : 'border-gray-200 bg-gray-50 text-gray-500'}`}
                          title={`${t.strongHit ? 'STRONG' : 'WEAK'} · T${t.firstSeen}-T${t.lastSeen} · ods=${t.matchedOdIds.length} intents=${t.matchedIntentIds.length} props=${t.matchedPropCount}`}
                        >
                          <span className={`inline-block w-1 h-1 rounded-full ${t.strongHit ? 'bg-blue-500' : 'bg-gray-300'}`} />
                          {t.token}
                        </span>
                      ))}
                    </div>
                  </div>
                )}

                {(ledger.okEntries.length > 0 || ledger.olEntries.length > 0) && (
                  <div className="px-4 py-3">
                    <div className="text-[10px] uppercase tracking-wider text-gray-400 mb-2">Knowledge</div>
                    <div className="space-y-1">
                      {ledger.okEntries.map(e => (
                        <div key={`ok-${e.id}`} className="rounded border border-gray-200 bg-white px-2.5 py-1 text-[11px]">
                          <span className="text-gray-400 mr-1">Ok</span>
                          <span className="text-gray-800">{e.title}</span>
                          <span className="text-gray-400 ml-2">T{e.firstSeenInTurn}</span>
                        </div>
                      ))}
                      {ledger.olEntries.map(e => (
                        <div key={`ol-${e.id}`} className="rounded border border-gray-200 bg-white px-2.5 py-1 text-[11px]">
                          <span className="text-blue-500 mr-1">Ol</span>
                          <span className="text-gray-800">{e.title}</span>
                          <span className="text-gray-400 ml-2">T{e.firstSeenInTurn}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {ledger.ods.length === 0 && ledger.intents.length === 0 && ledger.tokens.length === 0 && (
                  <div className="px-4 py-8 text-sm text-gray-400 text-center">
                    {t('ledger.empty')}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

export default function LakehouseAgentPage() {
  return (
    <Suspense fallback={<div className="flex h-64 items-center justify-center"><CyberLoader /></div>}>
      <LakehouseAgentChat />
    </Suspense>
  )
}
