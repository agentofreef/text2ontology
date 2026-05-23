'use client'

// Shared recall / tokenization diagnostics view.
//
// Extracted verbatim from the standalone Token-Recall page so that BOTH that
// page and the diagnose-first Agent workbench can render the same "why was this
// understood this way" surface (分词 → 召回 tiers → 本体映射). No logic change:
// the standalone page imports `RecallResultView` + the result types from here.

import { useTranslations } from 'next-intl'
import { motion, useReducedMotion } from 'motion/react'
import { AlertTriangle, CheckCircle2 } from 'lucide-react'

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

export interface RecallResult {
  odBlocks: OdBlock[]
  okEntries: OkEntry[]
  directOds: OdBlock[]
  hasMatches: boolean
  tokenDetails: Record<string, KeywordHit[]>
  contextMarkdown: string
  ambiguities?: Ambiguity[]
}

export interface VectorCandidate {
  keywordId: string
  keyword: string
  matched: string
  source: 'keyword' | 'alias'
  sim: number
  mappedTable: string
  mappedField: string
}

export interface TokenizeDebug {
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

export function RecallResultView({
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
