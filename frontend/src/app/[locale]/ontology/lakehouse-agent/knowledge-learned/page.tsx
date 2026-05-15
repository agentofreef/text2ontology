'use client'

import { useState, useEffect, useCallback } from 'react'
import { useTranslations } from 'next-intl'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import {
  Trash2, ChevronDown, ChevronRight, ExternalLink, Check, X, Edit3, Star,
  BookOpen, Search, RefreshCw, AlertTriangle,
} from 'lucide-react'
import { Link } from '@/i18n/navigation'
import type { OntLearnedFact } from '@/types/api'
import { useAutoAnimate } from '@/lib/motion'

// ─── Palette mappings (SV minimal: 黑白灰绿红) ────────────────────

const CONF_STYLE: Record<string, string> = {
  confirmed: 'border-success/30 bg-success/10 text-success',
  rejected:  'border-danger/30 bg-danger/10 text-danger',
  pending:   'border-border bg-canvas-alt text-ink-muted',
}
const CONF_LABEL: Record<string, string> = {
  confirmed: 'confirmed',
  rejected:  'rejected',
  pending:   'pending',
}

const FACT_TYPE_STYLE: Record<string, string> = {
  business_rule:    'border-ink text-ink bg-white',
  calibration:      'border-ink-muted text-ink-muted bg-white',
  misconception:    'border-danger/40 text-danger bg-danger/5',
  filter_hint:      'border-success/40 text-success bg-success/5',
  calculation_note: 'border-dashed border-border text-ink-muted bg-white',
}
const FACT_TYPE_LABEL: Record<string, string> = {
  business_rule:    'business_rule',
  calibration:      'calibration',
  misconception:    'misconception',
  filter_hint:      'filter_hint',
  calculation_note: 'calculation_note',
}

const ROLE_STYLE: Record<string, string> = {
  about:     'bg-ink text-white',
  corrects:  'bg-danger text-white',
  extends:   'bg-ink-muted text-white',
  conflicts: 'bg-danger text-white',
}
function roleStyle(role: string) {
  return ROLE_STYLE[role] || 'bg-success text-white'
}

function parseKeywords(kw: string): string[] {
  return kw ? kw.split('|').map(k => k.trim()).filter(Boolean) : []
}

// ─── Inline loader ────────────────────────────────────────────────

function InlineLoader({ text }: { text: string }) {
  const reduce = useReducedMotion()
  return (
    <div className="flex items-center gap-2 text-sm text-ink-muted">
      <motion.span
        animate={reduce ? undefined : { rotate: 360 }}
        transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
        className="inline-block h-3 w-3 rounded-full border-2 border-ink/20 border-t-ink"
        aria-hidden="true"
      />
      <span>{text}</span>
    </div>
  )
}

// ─── Fact Row ────────────────────────────────────────────────────

function FactRow({
  fact,
  onUpdate,
  onDelete,
}: {
  fact: OntLearnedFact
  onUpdate: (id: string, patch: Partial<OntLearnedFact>) => Promise<void>
  onDelete: (id: string) => void
}) {
  const t = useTranslations('agent.knowledge')
  const industrial = useStyleMode().mode === 'industrial'
  const [expanded, setExpanded] = useState(false)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState(fact.title)
  const [editingSummary, setEditingSummary] = useState(false)
  const [summaryDraft, setSummaryDraft] = useState(fact.summary)
  const [editingContent, setEditingContent] = useState(false)
  const [contentDraft, setContentDraft] = useState(fact.content)
  const [editingKeywords, setEditingKeywords] = useState(false)
  const [keywordsDraft, setKeywordsDraft] = useState(fact.keywords)
  const [saving, setSaving] = useState(false)
  const [factLinks, setFactLinks] = useState<Array<{ id: string; targetType: string; targetId: string; targetName: string; role: string }>>([])
  const [linksLoaded, setLinksLoaded] = useState(false)
  const reduce = useReducedMotion()

  const keywords = fact.tags?.length > 0 ? fact.tags : parseKeywords(fact.keywords)

  useEffect(() => {
    if (expanded && !linksLoaded) {
      api<{ data: Array<{ id: string; targetType: string; targetId: string; targetName: string; role: string }> }>(`/ontology/fact-links?factId=${fact.id}`)
        .then(res => { setFactLinks(res.data || []); setLinksLoaded(true) })
        .catch(() => setLinksLoaded(true))
    }
  }, [expanded, linksLoaded, fact.id])

  const save = async (patch: Partial<OntLearnedFact>) => {
    setSaving(true)
    await onUpdate(fact.id, patch)
    setSaving(false)
  }

  const cycleConfidence = () => {
    const next: Record<string, OntLearnedFact['confidence']> = {
      pending: 'confirmed', confirmed: 'rejected', rejected: 'pending',
    }
    save({ confidence: next[fact.confidence] || 'pending' })
  }

  const toggleMark = () => save({ mark: !fact.mark })

  const rowClass = `bg-white ${fact.confidence === 'rejected' ? 'opacity-60' : ''}`

  return (
    <div className={rowClass}>
      {/* Header */}
      <div className="flex items-start gap-2 px-6 py-3">
        <motion.button
          onClick={() => setExpanded(v => !v)}
          whileHover={reduce ? undefined : { scale: 1.1 }}
          whileTap={reduce ? undefined : { scale: 0.9 }}
          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
          className="mt-1 flex-shrink-0 text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
          aria-label={expanded ? t('collapse_aria') : t('expand_aria')}
          aria-expanded={expanded}
        >
          {expanded ? <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" /> : <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />}
        </motion.button>

        <motion.button
          onClick={toggleMark}
          disabled={saving}
          whileHover={reduce ? undefined : { scale: 1.15 }}
          whileTap={reduce ? undefined : { scale: 0.9 }}
          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
          className={`mt-0.5 flex-shrink-0 outline-none disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink ${
            fact.mark ? 'text-ink' : 'text-ink-ghost hover:text-ink'
          }`}
          title={fact.mark ? t('star_marked_title') : t('star_unmark_title')}
          aria-pressed={fact.mark}
        >
          <Star className={`h-3.5 w-3.5 ${fact.mark ? 'fill-ink' : ''}`} aria-hidden="true" />
        </motion.button>

        <motion.button
          onClick={cycleConfidence}
          disabled={saving}
          whileHover={reduce ? undefined : { scale: 1.03 }}
          whileTap={reduce ? undefined : { scale: 0.97 }}
          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
          className={`min-w-[72px] flex-shrink-0 border px-2 py-0.5 text-center font-medium outline-none cursor-pointer disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink ${
            industrial
              ? 'font-mono text-[10px] uppercase tracking-[0.14em]'
              : 'rounded-full text-[11px]'
          } ${
            CONF_STYLE[fact.confidence] || CONF_STYLE.pending
          }`}
          title={t('status_toggle_title')}
        >
          {t(`conf_${CONF_LABEL[fact.confidence] || 'pending'}`)}
        </motion.button>

        <span className={`flex-shrink-0 border px-1.5 py-0.5 font-medium ${
          industrial
            ? 'font-mono text-[10px] uppercase tracking-[0.1em]'
            : 'rounded-md text-[11px]'
        } ${
          FACT_TYPE_STYLE[fact.factType] || FACT_TYPE_STYLE.business_rule
        }`}>
          {t(`fact_type_${FACT_TYPE_LABEL[fact.factType] || 'business_rule'}`)}
        </span>

        {/* Title + summary */}
        <div className="min-w-0 flex-1">
          <div className="group mb-0.5 flex items-center gap-1">
            {editingTitle ? (
              <div className="flex w-full items-center gap-1">
                <input
                  autoFocus
                  className="flex-1 rounded-md border border-ink px-1.5 py-0.5 text-sm font-semibold text-ink outline-none focus:ring-1 focus:ring-ink/20"
                  value={titleDraft}
                  onChange={e => setTitleDraft(e.target.value)}
                  aria-label={t('edit_title_aria')}
                  onKeyDown={e => {
                    if (e.key === 'Enter') { save({ title: titleDraft }); setEditingTitle(false) }
                    if (e.key === 'Escape') { setTitleDraft(fact.title); setEditingTitle(false) }
                  }}
                />
                <button onClick={() => { save({ title: titleDraft }); setEditingTitle(false) }} aria-label={t('save_aria')} className="text-ink outline-none hover:text-ink-muted focus-visible:ring-1 focus-visible:ring-ink">
                  <Check className="h-3 w-3" aria-hidden="true" />
                </button>
                <button onClick={() => { setTitleDraft(fact.title); setEditingTitle(false) }} aria-label={t('cancel_aria')} className="text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink">
                  <X className="h-3 w-3" aria-hidden="true" />
                </button>
              </div>
            ) : (
              <>
                <span className="text-sm font-semibold text-ink">
                  {fact.title || <span className="italic text-ink-ghost">{t('no_title')}</span>}
                </span>
                <button
                  onClick={() => setEditingTitle(true)}
                  aria-label={t('edit_title_aria')}
                  className="text-ink-ghost opacity-0 outline-none transition-opacity group-hover:opacity-100 hover:text-ink focus-visible:opacity-100 focus-visible:ring-1 focus-visible:ring-ink"
                >
                  <Edit3 className="h-3 w-3" aria-hidden="true" />
                </button>
              </>
            )}
          </div>

          <div className="group flex items-center gap-1">
            {editingSummary ? (
              <div className="flex w-full items-center gap-1">
                <input
                  autoFocus
                  className="flex-1 rounded-md border border-ink px-1.5 py-0.5 text-xs text-ink outline-none focus:ring-1 focus:ring-ink/20"
                  value={summaryDraft}
                  onChange={e => setSummaryDraft(e.target.value)}
                  aria-label={t('edit_summary_aria')}
                  onKeyDown={e => {
                    if (e.key === 'Enter') { save({ summary: summaryDraft }); setEditingSummary(false) }
                    if (e.key === 'Escape') { setSummaryDraft(fact.summary); setEditingSummary(false) }
                  }}
                />
                <button onClick={() => { save({ summary: summaryDraft }); setEditingSummary(false) }} aria-label={t('save_aria')} className="text-ink outline-none hover:text-ink-muted focus-visible:ring-1 focus-visible:ring-ink">
                  <Check className="h-3 w-3" aria-hidden="true" />
                </button>
                <button onClick={() => { setSummaryDraft(fact.summary); setEditingSummary(false) }} aria-label={t('cancel_aria')} className="text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink">
                  <X className="h-3 w-3" aria-hidden="true" />
                </button>
              </div>
            ) : (
              <>
                <span className="text-xs text-ink-muted">
                  {fact.summary || <span className="italic text-ink-ghost">{t('no_summary')}</span>}
                </span>
                <button
                  onClick={() => setEditingSummary(true)}
                  aria-label={t('edit_summary_aria')}
                  className="text-ink-ghost opacity-0 outline-none transition-opacity group-hover:opacity-100 hover:text-ink focus-visible:opacity-100 focus-visible:ring-1 focus-visible:ring-ink"
                >
                  <Edit3 className="h-2.5 w-2.5" aria-hidden="true" />
                </button>
              </>
            )}
          </div>

          {keywords.length > 0 && (
            <div className="mt-1 flex flex-wrap items-center gap-1">
              {keywords.map((k, i) => (
                <span key={i} className={`border px-2 py-0 text-ink-muted ${
                  industrial
                    ? 'font-mono text-[10px] tracking-[0.04em] border-ink/40 bg-canvas-alt'
                    : 'rounded-full text-[11px] border-border bg-canvas-alt'
                }`}>
                  {k}
                </span>
              ))}
            </div>
          )}
        </div>

        {/* Meta */}
        <div className="flex flex-shrink-0 items-center gap-2">
          <span className="rounded border border-border bg-canvas-alt px-1 text-[11px] font-mono text-ink-ghost">
            {fact.sourceType}
          </span>
          {fact.definitionCount != null && fact.definitionCount > 0 && (
            <span className="text-[11px] tabular-nums text-ink-ghost">{fact.definitionCount} def</span>
          )}
          {fact.linkCount != null && fact.linkCount > 0 && (
            <span className="text-[11px] tabular-nums text-ink-ghost">{fact.linkCount} links</span>
          )}
          {fact.linkCount === 0 && (
            <span className="inline-flex items-center gap-0.5 text-[11px] text-danger">
              <AlertTriangle size={10} aria-hidden="true" /> 0 links
            </span>
          )}
          {fact.sourceThreadId && (
            <Link
              href={`/ontology/lakehouse-agent?threadId=${fact.sourceThreadId}`}
              className="text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
              title={t('source_thread_title')}
              aria-label={t('source_thread_aria')}
            >
              <ExternalLink className="h-3 w-3" aria-hidden="true" />
            </Link>
          )}
          <span className="text-[11px] tabular-nums text-ink-ghost">
            {new Date(fact.updatedAt).toLocaleDateString()}
          </span>
          <motion.button
            onClick={() => onDelete(fact.id)}
            whileHover={reduce ? undefined : { scale: 1.15 }}
            whileTap={reduce ? undefined : { scale: 0.9 }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            className="text-ink-ghost outline-none hover:text-danger focus-visible:ring-1 focus-visible:ring-ink"
            aria-label={t('delete_aria')}
            title={t('delete_title')}
          >
            <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
          </motion.button>
        </div>
      </div>

      {/* Expanded detail */}
      <AnimatePresence initial={false}>
        {expanded && (
          <motion.div
            initial={reduce ? undefined : { height: 0, opacity: 0 }}
            animate={reduce ? undefined : { height: 'auto', opacity: 1 }}
            exit={reduce ? undefined : { height: 0, opacity: 0 }}
            transition={{ duration: 0.2, ease: 'easeOut' }}
            style={{ overflow: 'hidden' }}
            className="border-t border-border-light bg-canvas-alt"
          >
            <div className="space-y-3 px-8 py-4">
              <div>
                <span className="text-[11px] font-medium text-ink-ghost">{t('detail_link_label')}</span>
                {factLinks.length > 0 ? (
                  <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                    {factLinks.map(l => {
                      const prefix = l.targetType === 'object' ? 'Od' : l.targetType === 'knowledge' ? 'Ok' : l.targetType === 'fact' ? 'Ol' : l.targetType
                      return (
                        <span key={l.id} className="inline-flex items-center gap-0.5">
                          <span className="rounded border border-border bg-white px-1.5 py-0.5 text-[11px] font-medium text-ink">
                            <span className="font-mono text-ink-muted">{prefix}:</span>
                            <span>{l.targetName || l.targetId.slice(0, 8)}</span>
                          </span>
                          <span className={`rounded px-1 py-0.5 text-[11px] font-medium ${roleStyle(l.role)}`}>
                            {l.role}
                          </span>
                        </span>
                      )
                    })}
                  </div>
                ) : linksLoaded ? (
                  <span className="ml-2 inline-flex items-center gap-0.5 text-xs text-danger">
                    <AlertTriangle size={11} aria-hidden="true" /> {t('detail_link_no_links_warning')}
                  </span>
                ) : (
                  <span className="ml-2 animate-pulse text-xs text-ink-ghost">{t('detail_link_loading')}</span>
                )}
              </div>

              <div>
                <div className="mb-1 flex items-center justify-between">
                  <span className="text-[11px] font-medium text-ink-ghost">{t('detail_kw_label')}</span>
                  {!editingKeywords && (
                    <button
                      onClick={() => setEditingKeywords(true)}
                      className="text-[11px] text-ink-muted outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      {t('detail_kw_edit_btn')}
                    </button>
                  )}
                </div>
                {editingKeywords ? (
                  <div className="flex items-center gap-1">
                    <input
                      autoFocus
                      className="flex-1 rounded-md border border-ink px-2 py-0.5 text-xs text-ink outline-none focus:ring-1 focus:ring-ink/20"
                      value={keywordsDraft}
                      placeholder={t('detail_kw_placeholder')}
                      aria-label={t('detail_kw_edit_aria')}
                      onChange={e => setKeywordsDraft(e.target.value)}
                      onKeyDown={e => {
                        if (e.key === 'Enter') { save({ keywords: keywordsDraft }); setEditingKeywords(false) }
                        if (e.key === 'Escape') { setKeywordsDraft(fact.keywords); setEditingKeywords(false) }
                      }}
                    />
                    <button onClick={() => { save({ keywords: keywordsDraft }); setEditingKeywords(false) }} aria-label={t('detail_kw_save_aria')} className="text-ink outline-none hover:text-ink-muted focus-visible:ring-1 focus-visible:ring-ink">
                      <Check className="h-3.5 w-3.5" aria-hidden="true" />
                    </button>
                    <button onClick={() => { setKeywordsDraft(fact.keywords); setEditingKeywords(false) }} aria-label={t('detail_kw_cancel_aria')} className="text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink">
                      <X className="h-3.5 w-3.5" aria-hidden="true" />
                    </button>
                  </div>
                ) : (
                  <span className="text-xs text-ink-muted">
                    {fact.keywords || <span className="italic text-ink-ghost">{t('detail_kw_no_keywords')}</span>}
                  </span>
                )}
              </div>

              <div>
                <div className="mb-1 flex items-center justify-between">
                  <span className="text-[11px] font-medium text-ink-ghost">{t('detail_content_label')}</span>
                  {!editingContent && (
                    <button
                      onClick={() => setEditingContent(true)}
                      className="text-[11px] text-ink-muted outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      {t('detail_content_edit_btn')}
                    </button>
                  )}
                </div>
                {editingContent ? (
                  <div className="space-y-1">
                    <textarea
                      autoFocus
                      className="min-h-[80px] w-full resize-y rounded-md border border-ink bg-white px-2 py-1.5 text-sm text-ink outline-none focus:ring-1 focus:ring-ink/20"
                      value={contentDraft}
                      onChange={e => setContentDraft(e.target.value)}
                      aria-label={t('detail_content_edit_aria')}
                    />
                    <div className="flex gap-2">
                      <AnimatedButton
                        variant="primary"
                        size="sm"
                        onClick={() => { save({ content: contentDraft }); setEditingContent(false) }}
                      >
                        {t('detail_content_save')}
                      </AnimatedButton>
                      <AnimatedButton
                        variant="secondary"
                        size="sm"
                        onClick={() => { setContentDraft(fact.content); setEditingContent(false) }}
                      >
                        {t('detail_content_cancel')}
                      </AnimatedButton>
                    </div>
                  </div>
                ) : (
                  <pre className="whitespace-pre-wrap text-sm leading-relaxed text-ink">
                    {fact.content || <span className="italic text-ink-ghost">{t('detail_content_no_content')}</span>}
                  </pre>
                )}
              </div>

              <div className="text-[11px] text-ink-ghost">
                {t('detail_id_label')}<span className="font-mono">{fact.id}</span>
                <span className="mx-1">·</span>
                {t('detail_created_label')}<span className="tabular-nums">{new Date(fact.createdAt).toLocaleString()}</span>
              </div>
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

// ─── Page ────────────────────────────────────────────────────────

export default function LakehouseKnowledgeLearnedPageMinimal() {
  const t = useTranslations('agent.knowledge')
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()

  const [facts, setFacts] = useState<OntLearnedFact[]>([])
  const [loading, setLoading] = useState(false)
  const [confidence, setConfidence] = useState<string>('')
  const [search, setSearch] = useState('')
  const [markOnly, setMarkOnly] = useState(false)
  const [factTypeFilter, setFactTypeFilter] = useState<string>('')
  const [refreshKey, setRefreshKey] = useState(0)

  const factListRef = useAutoAnimate<HTMLDivElement>()

  const load = useCallback(async () => {
    if (!currentProject?.id) return
    setLoading(true)
    try {
      let url = `/ontology/learned-facts?projectId=${currentProject.id}`
      if (confidence) url += `&confidence=${confidence}`
      if (search) url += `&search=${encodeURIComponent(search)}`
      const res = await api<{ data: OntLearnedFact[] }>(url)
      const data = res.data || []
      let filtered = data
      if (markOnly) filtered = filtered.filter(f => f.mark)
      if (factTypeFilter) filtered = filtered.filter(f => f.factType === factTypeFilter)
      setFacts(filtered)
    } catch {
      setFacts([])
    } finally {
      setLoading(false)
    }
  }, [currentProject, confidence, search, markOnly, factTypeFilter])

  useEffect(() => { load() }, [load])

  const handleUpdate = useCallback(async (id: string, patch: Partial<OntLearnedFact>) => {
    try {
      await api(`/ontology/learned-facts/${id}`, { method: 'PUT', body: patch })
      setFacts(prev => prev.map(f => f.id === id ? { ...f, ...patch } : f))
    } catch { msg.error(t('update_fail')) }
  }, [msg])

  const handleDelete = useCallback(async (id: string) => {
    try {
      await api(`/ontology/learned-facts/${id}`, { method: 'DELETE' })
      setFacts(prev => prev.filter(f => f.id !== id))
      msg.success(t('delete_success'))
    } catch { msg.error(t('delete_fail')) }
  }, [msg])

  const counts = {
    all: facts.length,
    confirmed: facts.filter(f => f.confidence === 'confirmed').length,
    pending: facts.filter(f => f.confidence === 'pending').length,
    rejected: facts.filter(f => f.confidence === 'rejected').length,
    marked: facts.filter(f => f.mark).length,
  }

  const handleRefresh = () => { setRefreshKey(k => k + 1); load() }

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
            <>
              <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
                // LEARNED KNOWLEDGE
              </span>
              <span className="font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-muted">
                {counts.all} TOTAL · {counts.confirmed} CONFIRMED · {counts.pending} PENDING · {counts.marked} ★
              </span>
            </>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <BookOpen size={14} className="text-ink" aria-hidden="true" />
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
        <div className="flex flex-shrink-0 items-center gap-4">
          {!industrial && (
            <div className="hidden items-baseline gap-x-3 text-xs text-ink-muted lg:flex">
              <span>
                {t('total_label')} <span className="font-semibold tabular-nums text-ink">{counts.all}</span>
              </span>
              <span aria-hidden="true" className="text-ink-ghost">·</span>
              <span>
                <span className="font-semibold tabular-nums text-success">{counts.confirmed}</span> {t('confirmed_label')}
              </span>
              <span aria-hidden="true" className="text-ink-ghost">·</span>
              <span>
                <span className="font-semibold tabular-nums text-ink">{counts.pending}</span> {t('pending_label')}
              </span>
              <span aria-hidden="true" className="text-ink-ghost">·</span>
              <span>
                <Star className="mb-0.5 inline-block h-3 w-3 fill-ink text-ink" aria-hidden="true" />
                <span className="ml-0.5 font-semibold tabular-nums text-ink">{counts.marked}</span>
              </span>
            </div>
          )}
          <motion.button
            onClick={handleRefresh}
            whileHover={reduce ? undefined : { scale: 1.05 }}
            whileTap={reduce ? undefined : { scale: 0.95 }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            aria-label={t('refresh_aria')}
            title={t('refresh_title')}
            className={`inline-flex h-7 w-7 items-center justify-center bg-white text-ink-muted outline-none hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink ${
              industrial ? 'border border-ink' : 'rounded-md border border-border'
            }`}
          >
            <motion.span
              key={refreshKey}
              animate={reduce ? undefined : { rotate: 360 }}
              transition={{ duration: 0.5, ease: 'easeOut' }}
              className="inline-flex"
            >
              <RefreshCw size={12} aria-hidden="true" />
            </motion.span>
          </motion.button>
        </div>
      </motion.header>

      {/* Filter strip */}
      <div className={`flex flex-shrink-0 flex-wrap items-center gap-2 bg-white px-6 py-3 ${industrial ? 'border-b border-ink' : 'border-b border-border'}`}>
        <div role="tablist" className={`flex overflow-hidden ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}>
          {(['', 'confirmed', 'pending', 'rejected'] as const).map(c => {
            const active = confidence === c
            const label = c === '' ? t('filter_all', { count: counts.all }) : `${t(`conf_${c}`)} (${counts[c as keyof typeof counts]})`
            return (
              <motion.button
                key={c}
                role="tab"
                aria-selected={active}
                onClick={() => setConfidence(c)}
                whileTap={reduce ? undefined : { scale: 0.97 }}
                transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                className={`px-2.5 py-1.5 outline-none last:border-r-0 cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                  industrial
                    ? 'border-r border-ink font-mono text-[10px] uppercase tracking-[0.14em]'
                    : 'border-r border-border text-[11px]'
                } ${
                  active ? 'bg-ink text-white font-medium' : 'text-ink-muted'
                }`}
              >
                {label}
              </motion.button>
            )
          })}
        </div>

        <motion.button
          onClick={() => setMarkOnly(v => !v)}
          whileHover={reduce ? undefined : { scale: 1.02 }}
          whileTap={reduce ? undefined : { scale: 0.97 }}
          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
          aria-pressed={markOnly}
          className={`inline-flex items-center gap-1 border px-2.5 py-1.5 outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
            industrial
              ? 'font-mono text-[10px] uppercase tracking-[0.14em]'
              : 'rounded-md text-[11px]'
          } ${
            markOnly
              ? 'border-ink bg-ink text-white font-medium'
              : (industrial ? 'border-ink/40 text-ink-muted hover:border-ink' : 'border-border text-ink-muted')
          }`}
        >
          <Star className={`h-3 w-3 ${markOnly ? 'fill-white' : ''}`} aria-hidden="true" />
          {t('mark_only_btn', { count: counts.marked })}
        </motion.button>

        <div role="tablist" className={`flex overflow-hidden ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}>
          {(['', 'business_rule', 'calibration', 'misconception', 'filter_hint', 'calculation_note'] as const).map(ft => {
            const active = factTypeFilter === ft
            return (
              <motion.button
                key={ft}
                role="tab"
                aria-selected={active}
                onClick={() => setFactTypeFilter(ft)}
                whileTap={reduce ? undefined : { scale: 0.97 }}
                transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                className={`px-2 py-1.5 outline-none last:border-r-0 cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                  industrial
                    ? 'border-r border-ink font-mono text-[10px] uppercase tracking-[0.12em]'
                    : 'border-r border-border text-[11px]'
                } ${
                  active ? 'bg-ink text-white font-medium' : 'text-ink-muted'
                }`}
              >
                {ft === '' ? t('filter_all_types') : t(`fact_type_${FACT_TYPE_LABEL[ft]}`)}
              </motion.button>
            )
          })}
        </div>

        <div className="flex-1" />

        <div className={`flex h-8 items-center gap-1.5 bg-white px-2.5 ${
          industrial ? 'border border-ink' : 'rounded-md border border-border focus-within:border-ink'
        }`}>
          <Search className="h-3.5 w-3.5 flex-shrink-0 text-ink-ghost" aria-hidden="true" />
          <input
            className={`w-48 bg-transparent outline-none placeholder:text-ink-ghost ${
              industrial ? 'font-mono text-[11px] tracking-[0.04em] text-ink' : 'text-xs text-ink'
            }`}
            placeholder={industrial ? 'SEARCH...' : t('search_placeholder')}
            aria-label={t('search_aria')}
            value={search}
            onChange={e => setSearch(e.target.value)}
          />
        </div>
      </div>

      {/* Scrollable list */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        {loading ? (
          <div className="flex h-full items-center justify-center">
            <InlineLoader text={t('loading')} />
          </div>
        ) : facts.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center gap-1 text-center">
            <span className="text-sm text-ink-muted">{t('empty_title')}</span>
            <span className="text-xs text-ink-ghost">
              {search || markOnly || confidence || factTypeFilter
                ? t('empty_hint_filter')
                : t('empty_hint_default')}
            </span>
          </div>
        ) : (
          <div ref={factListRef} className="divide-y divide-border-light">
            {facts.map(f => (
              <FactRow key={f.id} fact={f} onUpdate={handleUpdate} onDelete={handleDelete} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
