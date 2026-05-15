'use client'

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { api } from '@/lib/api'
import { Play, RotateCcw, Cpu, Keyboard, Telescope, AlertTriangle, CheckCircle2 } from 'lucide-react'

// ─── Types ───────────────────────────────────────────────────

interface KeywordHit {
  keywordId: string
  keyword: string
  mappedTable: string
  mappedField: string
  keywordExplain: string
  tier: 'EXACT' | 'FUZZY' | 'VEC'
  score: number
  matchedToken: string
}

interface PropertyMatch {
  propertyId: string
  name: string
  displayName: string
  sourceColumn: string
  dataType: string
  description: string
  objectTypeId: string
  okId?: string
  okTitle?: string
  okSummary?: string
  okDefs?: string[]
  keywords: KeywordHit[]
}

interface OdBlock {
  odId: string
  name: string
  kind: string
  description: string
  matchedProps: PropertyMatch[]
  allPropNames: string[]
  links?: { targetOdName: string; cardinality: string }[]
  matchedVia?: string[]
}

interface OkEntry {
  id: string
  title: string
  summary: string
  tokens: string[]
}

interface AmbiguityCandidate {
  odId: string
  odName: string
  odDescription: string
  propertyName: string
  propertyDesc: string
}

interface Ambiguity {
  keyword: string
  candidates: AmbiguityCandidate[]
}

interface RecallResult {
  odBlocks: OdBlock[]
  okEntries: OkEntry[]
  directOds: OdBlock[]
  hasMatches: boolean
  tokenDetails: Record<string, KeywordHit[]>
  contextMarkdown: string
  ambiguities?: Ambiguity[]
}

interface VectorCandidate {
  keywordId: string
  keyword: string
  matched: string
  source: 'keyword' | 'alias'
  sim: number
  mappedTable: string
  mappedField: string
}

interface TokenizeDebug {
  path: string
  reason: string
}

// ─── Helpers ─────────────────────────────────────────────────

function tierStyle(tier: string) {
  if (tier === 'EXACT') return 'border-success/50 text-success bg-success/5'
  if (tier === 'FUZZY') return 'border-border text-ink bg-white'
  if (tier === 'VEC') return 'border-border-light text-ink-muted bg-canvas-alt'
  return 'border-dashed border-border text-ink-ghost bg-white'
}

const MATCHED_VIA_STYLE: Record<string, { labelKey: string; className: string }> = {
  property:           { labelKey: 'matched_via_property',           className: 'border-ink bg-ink text-white' },
  'od-alias-keyword': { labelKey: 'matched_via_od_alias_keyword',   className: 'border-ink text-ink bg-white' },
  name:               { labelKey: 'matched_via_name',               className: 'border-ink-muted text-ink-muted bg-white' },
  display_name:       { labelKey: 'matched_via_display_name',       className: 'border-ink-muted text-ink-muted bg-canvas-alt' },
  alias:              { labelKey: '',                                className: 'border-dashed border-border text-ink-ghost bg-white' },
}

function MatchedViaBadges({ vias }: { vias?: string[] }) {
  const t = useTranslations('agent.token_recall')
  if (!vias || vias.length === 0) return null
  return (
    <span className="inline-flex flex-wrap gap-1">
      {vias.map(v => {
        const s = MATCHED_VIA_STYLE[v]
        const label = s ? (s.labelKey ? t(s.labelKey as Parameters<typeof t>[0]) : v) : v
        const className = s ? s.className : 'border-border text-ink-ghost bg-canvas-alt'
        return (
          <span key={v} className={`rounded-md border px-1.5 py-0.5 text-[11px] ${className}`}>
            {label}
          </span>
        )
      })}
    </span>
  )
}

function SectionCard({ title, subtitle, children }: {
  title: string
  subtitle?: string
  children: React.ReactNode
}) {
  return (
    <div className="overflow-hidden rounded-md border border-border bg-white">
      <div className="flex items-baseline gap-2 border-b border-border-light bg-canvas-alt px-4 py-2">
        <span className="text-xs font-medium text-ink">{title}</span>
        {subtitle && <span className="text-[11px] text-ink-ghost">{subtitle}</span>}
      </div>
      {children}
    </div>
  )
}

// ─── Result Display ──────────────────────────────────────────

function RecallResultView({
  result,
  tokens,
  vectorCandidates,
}: {
  result: RecallResult
  tokens?: string[]
  vectorCandidates?: Record<string, VectorCandidate[]>
}) {
  const t = useTranslations('agent.token_recall')
  const reduce = useReducedMotion()
  const hasAmbiguity = result.ambiguities && result.ambiguities.length > 0

  return (
    <motion.div
      initial={reduce ? undefined : { opacity: 0, y: 4 }}
      animate={reduce ? undefined : { opacity: 1, y: 0 }}
      transition={{ duration: 0.2, ease: 'easeOut' }}
      className="space-y-4"
    >
      {/* Ambiguity banner */}
      {hasAmbiguity && (
        <div className="overflow-hidden rounded-md border border-danger/40 bg-danger/5">
          <div className="flex items-center gap-2 border-b border-danger/30 bg-danger/10 px-4 py-2">
            <AlertTriangle size={14} className="flex-shrink-0 text-danger" aria-hidden="true" />
            <span className="text-sm font-semibold text-danger">{t('result_ambiguity_title')}</span>
          </div>
          <div className="space-y-3 px-4 py-3">
            <p className="text-xs leading-relaxed text-ink-muted">
              {t('result_ambiguity_desc')}
            </p>
            {result.ambiguities!.map((amb, ai) => (
              <div key={ai} className="rounded-md border border-border bg-white px-3 py-2">
                <div className="mb-1.5 text-xs text-ink-muted">
                  {t('result_ambiguity_kw_label')}
                  <span className="ml-1 font-mono font-semibold text-ink">{amb.keyword}</span>
                </div>
                <div className="space-y-1">
                  {amb.candidates.map((c, ci) => (
                    <div key={ci} className="flex items-start gap-2 text-[11px]">
                      <span className="flex-shrink-0 rounded border border-border bg-canvas-alt px-1.5 text-ink-muted tabular-nums">{ci + 1}</span>
                      <div className="min-w-0">
                        <span className="font-mono font-semibold text-ink">{c.odName}</span>
                        {c.propertyName && <span className="ml-1 font-mono text-ink-muted">/ {c.propertyName}</span>}
                        {c.odDescription && (
                          <span className="ml-1 text-ink-ghost">
                            — {c.odDescription.length > 80 ? c.odDescription.slice(0, 80) + '...' : c.odDescription}
                          </span>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* No-ambiguity indicator */}
      {!hasAmbiguity && result.hasMatches && (result.odBlocks || []).length >= 2 && (
        <div className="flex items-center gap-2 rounded-md border border-success/30 bg-success/5 px-4 py-2">
          <CheckCircle2 size={14} className="flex-shrink-0 text-success" aria-hidden="true" />
          <span className="text-sm font-medium text-success">{t('result_no_ambiguity')}</span>
          <span className="text-xs text-ink-muted">
            {t('result_no_ambiguity_desc')}
          </span>
        </div>
      )}

      {/* Tokens row (tokenize mode) */}
      {tokens && tokens.length > 0 && (
        <SectionCard title={t('result_tokens_title')} subtitle={t('result_tokens_subtitle', { count: tokens.length })}>
          <div className="flex flex-wrap gap-1.5 px-4 py-3">
            {tokens.map((t, i) => (
              <span key={i} className="inline-block rounded-md border border-ink bg-white px-2 py-0.5 text-xs text-ink">
                {t}
              </span>
            ))}
          </div>
        </SectionCard>
      )}

      {/* Token → Keyword hits */}
      <SectionCard title={t('result_token_detail_title')} subtitle={t('result_token_detail_subtitle')}>
        <div className="divide-y divide-border-light">
          {Object.entries(result.tokenDetails || {}).map(([token, hits]) => (
            <div key={token} className="px-4 py-2.5">
              <div className="mb-1.5 flex items-baseline gap-2">
                <span className="text-sm font-semibold text-ink">{token}</span>
                <span className="text-[11px] text-ink-ghost">
                  {(hits || []).length > 0 ? t('result_hits', { count: hits.length }) : t('result_miss')}
                </span>
              </div>
              {(hits || []).length > 0 ? (
                <div className="ml-4 space-y-1">
                  {hits.map((h, i) => (
                    <div key={i} className="flex flex-wrap items-center gap-2 text-xs">
                      <span className={`rounded-md border px-1.5 py-0.5 text-[11px] font-semibold ${tierStyle(h.tier)}`}>
                        {h.tier}
                      </span>
                      <span className="font-mono font-semibold text-ink">{h.keyword}</span>
                      <span aria-hidden="true" className="text-ink-ghost">→</span>
                      <span className="font-mono text-ink-muted">
                        {h.mappedTable ? `${h.mappedTable}.${h.mappedField}` : h.mappedField || '—'}
                      </span>
                      {h.tier === 'VEC' && (
                        <span className="text-[11px] tabular-nums text-ink-ghost">{h.score.toFixed(3)}</span>
                      )}
                    </div>
                  ))}
                </div>
              ) : (
                <div className="ml-4 space-y-1.5">
                  <div className="text-xs text-ink-ghost">{t('result_no_match')}</div>
                  {vectorCandidates?.[token] && vectorCandidates[token].length > 0 && (
                    <div className="rounded-md border border-border bg-canvas-alt px-3 py-2">
                      <div className="mb-1 text-[11px] text-ink-muted">
                        <span className="font-medium text-ink">{t('result_vec_top', { count: vectorCandidates[token].length })}</span>
                        <span className="ml-1 text-ink-ghost">{t('result_vec_note')}</span>
                      </div>
                      <div className="space-y-0.5">
                        {vectorCandidates[token].map((c, ci) => {
                          const passed = c.sim >= 0.85
                          return (
                            <div key={ci} className="flex flex-wrap items-center gap-2 text-[11px]">
                              <span className={`rounded border px-1 py-0.5 tabular-nums ${
                                passed ? 'border-success/40 text-success bg-success/5' : 'border-border text-ink-muted bg-white'
                              }`}>
                                {c.sim.toFixed(3)}
                              </span>
                              <span className={`rounded border px-1 py-0.5 ${
                                c.source === 'alias'
                                  ? 'border-ink-muted text-ink bg-white'
                                  : 'border-border text-ink-ghost bg-canvas-alt'
                              }`}>
                                {c.source === 'alias' ? 'ALIAS' : 'KW'}
                              </span>
                              <span className="font-mono font-semibold text-ink">{c.keyword}</span>
                              {c.source === 'alias' && c.matched !== c.keyword && (
                                <span className="text-ink-muted">via「{c.matched}」</span>
                              )}
                              <span aria-hidden="true" className="text-ink-ghost">→</span>
                              <span className="font-mono text-ink-muted">
                                {c.mappedTable ? `${c.mappedTable}.${c.mappedField}` : c.mappedField || '—'}
                              </span>
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  )}
                  {vectorCandidates && !vectorCandidates[token] && (
                    <div className="text-xs italic text-ink-ghost">
                      {t('result_vec_unavailable')}
                    </div>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      </SectionCard>

      {/* Od Blocks */}
      {(result.odBlocks || []).length > 0 && (
        <SectionCard title={t('result_od_title')} subtitle={t('result_od_subtitle')}>
          <div className="divide-y divide-border-light">
            {(result.odBlocks || []).map(od => (
              <OdBlockView key={od.odId} od={od} />
            ))}
          </div>
        </SectionCard>
      )}

      {/* Direct Od Blocks */}
      {(result.directOds || []).length > 0 && (
        <SectionCard title={t('result_direct_od_title')} subtitle={t('result_direct_od_subtitle')}>
          <div className="divide-y divide-border-light">
            {(result.directOds || []).map(od => (
              <div key={od.odId} className="px-4 py-2.5">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-sm font-semibold text-ink">{od.name}</span>
                  <span className="rounded-md border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] font-mono text-ink-muted">{od.kind}</span>
                  <MatchedViaBadges vias={od.matchedVia} />
                </div>
                {od.description && (
                  <div className="mt-0.5 line-clamp-1 text-xs text-ink-muted">{od.description}</div>
                )}
                {od.allPropNames && od.allPropNames.length > 0 && (
                  <div className="mt-1 ml-4 text-[11px] text-ink-ghost">
                    {t('result_direct_od_props')}<span className="font-mono">{od.allPropNames.join(', ')}</span>
                  </div>
                )}
              </div>
            ))}
          </div>
        </SectionCard>
      )}

      {/* Ok Entries */}
      {(result.okEntries || []).length > 0 && (
        <SectionCard title={t('result_ok_title')} subtitle={t('result_ok_subtitle')}>
          <div className="divide-y divide-border-light">
            {(result.okEntries || []).map(ok => (
              <div key={ok.id} className="px-4 py-2.5">
                <span className="text-sm font-semibold text-ink">{ok.title}</span>
                {ok.summary && <span className="ml-2 text-xs text-ink-muted">{ok.summary}</span>}
                <div className="mt-0.5 text-[11px] text-ink-ghost">
                  {t('result_ok_tokens')}<span className="font-mono">{ok.tokens?.join(', ')}</span>
                </div>
              </div>
            ))}
          </div>
        </SectionCard>
      )}

      {/* Context Markdown preview */}
      <SectionCard title={t('result_context_title')} subtitle={t('result_context_subtitle')}>
        <pre className="max-h-[400px] overflow-auto whitespace-pre-wrap bg-canvas-alt p-4 font-mono text-xs leading-relaxed text-ink">
          {result.contextMarkdown || t('result_context_empty')}
        </pre>
      </SectionCard>
    </motion.div>
  )
}

function OdBlockView({ od }: { od: OdBlock }) {
  const t = useTranslations('agent.token_recall')
  const matchedNames = new Set((od.matchedProps || []).map(p => p.displayName || p.name))
  const unmatched = (od.allPropNames || []).filter(n => !matchedNames.has(n))

  return (
    <div className="px-4 py-3">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <span className="font-mono text-sm font-semibold text-ink">{od.name}</span>
        <span className="rounded-md border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] font-mono text-ink-muted">{od.kind}</span>
        <MatchedViaBadges vias={od.matchedVia} />
        {od.description && <span className="line-clamp-1 text-xs text-ink-muted">{od.description}</span>}
      </div>
      {od.matchedProps?.map(p => (
        <div key={p.propertyId} className="mb-2 ml-4 border-l-2 border-ink pl-3">
          <div className="text-sm">
            <span className="font-mono font-semibold text-ink">{p.displayName || p.name}</span>
            {p.dataType && <span className="ml-1 font-mono text-ink-ghost">({p.dataType})</span>}
            {p.description && <span className="ml-2 line-clamp-1 text-xs text-ink-muted">{p.description}</span>}
          </div>
          {p.keywords?.map((kw, ki) => (
            <div key={ki} className="mt-0.5 ml-2 flex flex-wrap items-center gap-1.5 text-[11px] text-ink-muted">
              <span className={`rounded border px-1 py-0.5 font-semibold ${tierStyle(kw.tier)}`}>{kw.tier}</span>
              <span>「{kw.matchedToken}」 → <span className="font-mono text-ink">{kw.keyword}</span></span>
              {kw.mappedTable && (
                <span className="text-ink-ghost">
                  (<span className="font-mono">{kw.mappedTable}.{kw.mappedField}</span>)
                </span>
              )}
            </div>
          ))}
          {p.okDefs && p.okDefs.length > 0 && (
            <div className="mt-1 ml-2 text-[11px] text-success">
              Ok：{p.okDefs.join('；')}
            </div>
          )}
        </div>
      ))}
      {unmatched.length > 0 && (
        <div className="mt-1 ml-4 text-[11px] text-ink-ghost">
          {t('result_od_other_props')}<span className="font-mono">{unmatched.join(', ')}</span>
        </div>
      )}
      {od.links?.map((l, li) => (
        <div key={li} className="mt-1 ml-4 text-[11px] text-ink-muted">
          <span aria-hidden="true">↔ </span>
          <span className="font-mono">{l.targetOdName}</span>
          <span className="ml-1 text-ink-ghost">({l.cardinality})</span>
        </div>
      ))}
    </div>
  )
}

// ─── Page ────────────────────────────────────────────────────

export default function LakehouseTokenRecallPage() {
  const t = useTranslations('agent.token_recall')
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()
  const [tab, setTab] = useState<'tokens' | 'question'>('question')

  const [tokenInput, setTokenInput] = useState('')
  const [tokenLoading, setTokenLoading] = useState(false)
  const [tokenResult, setTokenResult] = useState<RecallResult | null>(null)
  const [tokenVectorCandidates, setTokenVectorCandidates] = useState<Record<string, VectorCandidate[]>>({})

  const [question, setQuestion] = useState('')
  const [questionLoading, setQuestionLoading] = useState(false)
  const [questionTokens, setQuestionTokens] = useState<string[]>([])
  const [questionResult, setQuestionResult] = useState<RecallResult | null>(null)
  const [tokenizeDebug, setTokenizeDebug] = useState<TokenizeDebug | null>(null)
  const [vectorCandidates, setVectorCandidates] = useState<Record<string, VectorCandidate[]>>({})

  const handleTokenRecall = async () => {
    const tokenList = tokenInput.split(/[\n,|;]/).map(t => t.trim()).filter(Boolean)
    if (tokenList.length === 0) { msg.error(t('recall_error_no_token')); return }
    setTokenLoading(true)
    try {
      const res = await api<{
        recall: RecallResult
        vectorCandidates?: Record<string, VectorCandidate[]>
      }>(`/ontology/lakehouse-token-recall-debug?projectId=${currentProject?.id}`, {
        method: 'POST',
        body: { tokens: tokenList },
      })
      setTokenResult(res.recall)
      setTokenVectorCandidates(res.vectorCandidates || {})
      msg.success(t('recall_success', { status: res.recall.hasMatches ? t('recall_hit') : t('recall_miss') }))
    } catch { msg.error(t('recall_fail')) }
    finally { setTokenLoading(false) }
  }

  const handleQuestionRecall = async () => {
    if (!question.trim()) { msg.error(t('recall_error_no_question')); return }
    setQuestionLoading(true)
    try {
      const res = await api<{
        question: string
        tokens: string[]
        recall: RecallResult
        tokenizeDebug: TokenizeDebug
        vectorCandidates?: Record<string, VectorCandidate[]>
      }>(
        `/ontology/lakehouse-token-recall-tokenize?projectId=${currentProject?.id}`,
        { method: 'POST', body: { question: question.trim() } },
      )
      setQuestionTokens(res.tokens || [])
      setQuestionResult(res.recall)
      setTokenizeDebug(res.tokenizeDebug || null)
      setVectorCandidates(res.vectorCandidates || {})
      msg.success(t('tokenize_success', { count: res.tokens?.length || 0, status: res.recall?.hasMatches ? t('recall_hit') : t('recall_miss') }))
    } catch { msg.error(t('question_fail')) }
    finally { setQuestionLoading(false) }
  }

  const handleResetQuestion = () => {
    setQuestion(''); setQuestionResult(null); setQuestionTokens([])
    setTokenizeDebug(null); setVectorCandidates({})
  }
  const handleResetTokens = () => { setTokenInput(''); setTokenResult(null) }

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header strip */}
      <motion.header
        initial={reduce ? undefined : { opacity: 0, y: -4 }}
        animate={reduce ? undefined : { opacity: 1, y: 0 }}
        transition={{ duration: 0.2, ease: 'easeOut' }}
        className={`flex h-14 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${
          industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'
        }`}
      >
        <div className="flex min-w-0 items-center gap-3">
          {industrial ? (
            <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // TOKEN RECALL
            </span>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <Telescope size={14} className="text-ink" aria-hidden="true" />
              </div>
              <div className="min-w-0">
                <h1 className="text-base font-semibold tracking-tight text-ink">{t('page_title')}</h1>
                <p className="truncate text-xs text-ink-muted">
                  {t('page_desc')}
                </p>
              </div>
            </>
          )}
        </div>
      </motion.header>

      {/* Tab strip */}
      <nav role="tablist" aria-label={t('tab_aria')} className={`flex flex-shrink-0 items-center gap-0 bg-white px-6 ${industrial ? 'border-b border-ink' : 'border-b border-border'}`}>
        {([
          ['question', t('tab_question'), Cpu],
          ['tokens', t('tab_tokens'), Keyboard],
        ] as const).map(([key, label, Icon]) => {
          const active = tab === key
          return (
            <motion.button
              key={key}
              role="tab"
              aria-selected={active}
              onClick={() => setTab(key)}
              whileHover={reduce ? undefined : { y: -1 }}
              whileTap={reduce ? undefined : { scale: 0.98 }}
              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
              className={`-mb-px flex h-10 items-center gap-1.5 border-b-2 px-4 outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                industrial ? 'font-mono text-[11px] uppercase tracking-[0.14em]' : 'text-sm'
              } ${
                active
                  ? 'border-ink font-semibold text-ink'
                  : 'border-transparent text-ink-muted hover:text-ink'
              }`}
            >
              <Icon size={14} aria-hidden="true" />
              {label}
            </motion.button>
          )
        })}
      </nav>

      {/* Scrollable content */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <AnimatePresence mode="wait">
          {/* Tab: 分词 + 召回 */}
          {tab === 'question' && (
            <motion.div
              key="question"
              initial={reduce ? undefined : { opacity: 0, y: 4 }}
              animate={reduce ? undefined : { opacity: 1, y: 0 }}
              exit={reduce ? undefined : { opacity: 0 }}
              transition={{ duration: 0.15 }}
              className="space-y-4 p-6"
            >
              <div className="overflow-hidden rounded-md border border-border bg-white">
                <div className="flex items-center justify-between gap-2 border-b border-border-light bg-canvas-alt px-4 py-2">
                  <span className="text-xs font-medium text-ink">{t('question_input_label')}</span>
                  <div className="flex items-center gap-2">
                    <motion.button
                      onClick={handleResetQuestion}
                      whileHover={reduce ? undefined : { scale: 1.05 }}
                      whileTap={reduce ? undefined : { scale: 0.95 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      aria-label={t('question_clear_aria')}
                      className="inline-flex items-center gap-1 rounded-md border border-transparent px-1.5 py-0.5 text-[11px] text-ink-ghost outline-none hover:border-border hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      <RotateCcw size={11} aria-hidden="true" />
                      {t('question_clear_btn')}
                    </motion.button>
                    <AnimatedButton
                      variant="primary"
                      size="sm"
                      onClick={handleQuestionRecall}
                      disabled={questionLoading || !question.trim()}
                    >
                      <Cpu size={12} aria-hidden="true" />
                      {questionLoading ? t('question_submit_loading') : t('question_submit_btn')}
                    </AnimatedButton>
                  </div>
                </div>
                <textarea
                  className="w-full bg-white p-4 font-mono text-sm text-ink outline-none placeholder:text-ink-ghost focus:bg-canvas-alt/50"
                  rows={3}
                  value={question}
                  onChange={e => setQuestion(e.target.value)}
                  placeholder={t('question_placeholder')}
                  spellCheck={false}
                  aria-label={t('question_aria')}
                />
              </div>

              {tokenizeDebug && (
                <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-canvas-alt px-4 py-2">
                  <span className="text-xs font-medium text-ink-muted">{t('tokenize_debug_label')}</span>
                  <span className="rounded border border-border bg-white px-1.5 py-0.5 text-[11px] font-mono text-ink">
                    path: {tokenizeDebug.path}
                  </span>
                  <span className="text-[11px] text-ink-muted">{tokenizeDebug.reason}</span>
                </div>
              )}

              {questionResult && (
                <RecallResultView
                  result={questionResult}
                  tokens={questionTokens}
                  vectorCandidates={vectorCandidates}
                />
              )}
            </motion.div>
          )}

          {/* Tab: Manual Token */}
          {tab === 'tokens' && (
            <motion.div
              key="tokens"
              initial={reduce ? undefined : { opacity: 0, y: 4 }}
              animate={reduce ? undefined : { opacity: 1, y: 0 }}
              exit={reduce ? undefined : { opacity: 0 }}
              transition={{ duration: 0.15 }}
              className="space-y-4 p-6"
            >
              <div className="overflow-hidden rounded-md border border-border bg-white">
                <div className="flex items-center justify-between gap-2 border-b border-border-light bg-canvas-alt px-4 py-2">
                  <span className="text-xs font-medium text-ink">{t('token_input_label')}</span>
                  <span className="text-[11px] text-ink-ghost">{t('token_input_hint')}</span>
                  <div className="ml-auto flex items-center gap-2">
                    <motion.button
                      onClick={handleResetTokens}
                      whileHover={reduce ? undefined : { scale: 1.05 }}
                      whileTap={reduce ? undefined : { scale: 0.95 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      aria-label={t('token_clear_aria')}
                      className="inline-flex items-center gap-1 rounded-md border border-transparent px-1.5 py-0.5 text-[11px] text-ink-ghost outline-none hover:border-border hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      <RotateCcw size={11} aria-hidden="true" />
                      {t('token_clear_btn')}
                    </motion.button>
                    <AnimatedButton
                      variant="primary"
                      size="sm"
                      onClick={handleTokenRecall}
                      disabled={tokenLoading || !tokenInput.trim()}
                    >
                      <Play size={12} aria-hidden="true" />
                      {tokenLoading ? t('token_submit_loading') : t('token_submit_btn')}
                    </AnimatedButton>
                  </div>
                </div>
                <textarea
                  className="w-full bg-white p-4 font-mono text-sm text-ink outline-none placeholder:text-ink-ghost focus:bg-canvas-alt/50"
                  rows={5}
                  value={tokenInput}
                  onChange={e => setTokenInput(e.target.value)}
                  placeholder={t('token_input_placeholder_example')}
                  spellCheck={false}
                  aria-label={t('token_aria')}
                />
              </div>

              {tokenResult && (
                <RecallResultView
                  result={tokenResult}
                  vectorCandidates={tokenVectorCandidates}
                />
              )}
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  )
}
