'use client'

import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslations } from 'next-intl'
import { motion, useReducedMotion } from 'motion/react'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { Trash2, Search, Check, X, MessageSquare, RefreshCw } from 'lucide-react'
import { useAutoAnimate } from '@/lib/motion'

interface TokenMapping {
  token: string
  result: string
}

interface AnnotationItem {
  id: string
  question: string
  tokens: string
  tokenMappings: TokenMapping[]
  status: boolean
  createdAt: string
  updatedAt: string
  threadId: string
}

type StatusFilter = 'all' | 'pending' | 'confirmed'

// ─── Inline loader (SV minimal spinner) ─────────────────────────

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

// ─── Token Chips (display mode) ────────────────────────────────

function TokenChips({ tokens, mappings, onClick }: {
  tokens: string
  mappings?: TokenMapping[]
  onClick: () => void
}) {
  const t = useTranslations('agent.annotations')
  const industrial = useStyleMode().mode === 'industrial'
  if (!tokens) {
    return (
      <button
        type="button"
        className={`cursor-pointer outline-none hover:text-ink-muted focus-visible:ring-1 focus-visible:ring-ink ${
          industrial
            ? 'font-mono text-[10px] uppercase tracking-[0.14em] text-ink-ghost'
            : 'text-[11px] italic text-ink-ghost'
        }`}
        onClick={onClick}
        aria-label={t('token_add_aria')}
      >
        {industrial ? `// ${t('token_add_hint')}` : t('token_add_hint')}
      </button>
    )
  }

  const parts = tokens.split('|').filter(Boolean)
  const mappingMap = new Map<string, string>()
  if (mappings) {
    for (const m of mappings) {
      mappingMap.set(m.token, m.result)
    }
  }

  return (
    <button
      type="button"
      className="flex w-full cursor-pointer flex-wrap items-center gap-1 text-left outline-none focus-visible:ring-1 focus-visible:ring-ink"
      onClick={onClick}
      title={t('token_edit_title')}
      aria-label={t('token_edit_aria')}
    >
      {parts.map((tok, i) => {
        const result = mappingMap.get(tok)
        const matched = result && result !== '未命中'
        return (
          <span
            key={i}
            className={`inline-block border px-1.5 py-0.5 text-[11px] ${
              industrial ? 'font-mono tracking-[0.04em]' : 'rounded-md'
            } ${
              matched
                ? (industrial ? 'border-success bg-white text-success' : 'border-success/40 bg-success/5 text-success')
                : (industrial ? 'border-ink/40 bg-canvas-alt text-ink-muted' : 'border-border bg-canvas-alt text-ink-muted')
            }`}
            title={result || t('token_no_match_title')}
          >
            {tok}
          </span>
        )
      })}
    </button>
  )
}

// ─── Token Editor (edit mode) ──────────────────────────────────

function TokenEditor({ initial, onSave, onCancel }: {
  initial: string
  onSave: (tokens: string) => void
  onCancel: () => void
}) {
  const t = useTranslations('agent.annotations')
  const [value, setValue] = useState(initial)
  const inputRef = useRef<HTMLInputElement>(null)
  const reduce = useReducedMotion()

  useEffect(() => { inputRef.current?.focus() }, [])

  return (
    <div className="flex items-center gap-1">
      <input
        ref={inputRef}
        className="min-w-0 flex-1 rounded-md border border-ink px-2 py-1 text-xs text-ink outline-none focus:ring-1 focus:ring-ink/20"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={t('token_editor_placeholder')}
        aria-label={t('token_editor_aria')}
        onKeyDown={(e) => {
          if (e.key === 'Enter') onSave(value)
          if (e.key === 'Escape') onCancel()
        }}
      />
      <motion.button
        onClick={() => onSave(value)}
        whileHover={reduce ? undefined : { scale: 1.1 }}
        whileTap={reduce ? undefined : { scale: 0.9 }}
        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
        className="flex-shrink-0 p-1 text-ink-muted outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
        aria-label={t('token_save_aria')}
        title={t('token_save_title')}
      >
        <Check className="h-3.5 w-3.5" aria-hidden="true" />
      </motion.button>
      <motion.button
        onClick={onCancel}
        whileHover={reduce ? undefined : { scale: 1.1 }}
        whileTap={reduce ? undefined : { scale: 0.9 }}
        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
        className="flex-shrink-0 p-1 text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
        aria-label={t('token_cancel_aria')}
        title={t('token_cancel_title')}
      >
        <X className="h-3.5 w-3.5" aria-hidden="true" />
      </motion.button>
    </div>
  )
}

// ─── Main Page ─────────────────────────────────────────────────

const PAGE_SIZE = 50

export default function LakehouseAnnotationsPageMinimal() {
  const t = useTranslations('agent.annotations')
  const industrial = useStyleMode().mode === 'industrial'
  const [items, setItems] = useState<AnnotationItem[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(false)
  const [search, setSearch] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [page, setPage] = useState(1)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [fadingOut, setFadingOut] = useState<Set<string>>(new Set())
  const [refreshKey, setRefreshKey] = useState(0)
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()

  const listRef = useAutoAnimate<HTMLDivElement>()

  const load = useCallback(async () => {
    if (!currentProject?.id) return
    setLoading(true)
    try {
      let url = `/ontology/agent-annotations?projectId=${currentProject.id}`
      url += `&page=${page}&pageSize=${PAGE_SIZE}`
      if (search) url += `&search=${encodeURIComponent(search)}`
      if (statusFilter === 'pending') url += '&status=false'
      if (statusFilter === 'confirmed') url += '&status=true'
      const res = await api<{ data: AnnotationItem[]; total: number }>(url)
      setItems(res.data || [])
      setTotal(res.total || 0)
    } catch {
      setItems([])
      setTotal(0)
    } finally {
      setLoading(false)
    }
  }, [currentProject, search, statusFilter, page])

  useEffect(() => { load() }, [load])
  useEffect(() => { setPage(1) }, [search, statusFilter])

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  useEffect(() => {
    if (!loading && items.length === 0 && page > 1 && total > 0) {
      setPage(p => Math.min(p - 1, totalPages))
    }
  }, [loading, items.length, page, total, totalPages])

  const toggleStatus = async (item: AnnotationItem) => {
    const newStatus = !item.status
    const willLeaveList = statusFilter !== 'all'

    if (willLeaveList) {
      setFadingOut(prev => new Set(prev).add(item.id))
      setItems(prev => prev.map(i => i.id === item.id ? { ...i, status: newStatus } : i))
      setTimeout(() => {
        setItems(prev => prev.filter(i => i.id !== item.id))
        setTotal(prev => Math.max(0, prev - 1))
        setFadingOut(prev => { const n = new Set(prev); n.delete(item.id); return n })
      }, 350)
    } else {
      setItems(prev => prev.map(i => i.id === item.id ? { ...i, status: newStatus } : i))
    }

    try {
      await api(`/ontology/agent-annotations/${item.id}`, {
        method: 'PUT',
        body: { status: newStatus },
      })
      msg.success(newStatus ? t('op_success_confirmed') : t('op_success_unconfirmed'))
    } catch {
      setFadingOut(prev => { const n = new Set(prev); n.delete(item.id); return n })
      setItems(prev => {
        const exists = prev.find(i => i.id === item.id)
        if (exists) return prev.map(i => i.id === item.id ? { ...i, status: item.status } : i)
        return [...prev, item].sort((a, b) => b.createdAt.localeCompare(a.createdAt))
      })
      if (willLeaveList) setTotal(prev => prev + 1)
      msg.error(t('op_fail'))
    }
  }

  const saveTokens = async (id: string, tokens: string) => {
    const prev = items.find(i => i.id === id)?.tokens
    setItems(items => items.map(i => i.id === id ? { ...i, tokens } : i))
    setEditingId(null)
    try {
      await api(`/ontology/agent-annotations/${id}`, { method: 'PUT', body: { tokens } })
      msg.success(t('save_success'))
    } catch {
      if (prev !== undefined) setItems(items => items.map(i => i.id === id ? { ...i, tokens: prev } : i))
      msg.error(t('save_fail'))
    }
  }

  const deleteItem = async (id: string) => {
    if (!confirm(t('confirm_delete'))) return
    const removed = items.find(i => i.id === id)
    setItems(prev => prev.filter(i => i.id !== id))
    setTotal(prev => Math.max(0, prev - 1))
    try {
      await api(`/ontology/agent-annotations/${id}`, { method: 'DELETE' })
      msg.success(t('delete_success'))
    } catch {
      if (removed) setItems(prev => [...prev, removed].sort((a, b) => b.createdAt.localeCompare(a.createdAt)))
      setTotal(prev => prev + 1)
      msg.error(t('delete_fail'))
    }
  }

  const formatDate = (s: string) => {
    const d = new Date(s)
    return d.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
  }

  const confirmed = items.filter(i => i.status).length
  const pending = items.filter(i => !i.status).length

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
        <div className="text-sm text-ink-muted">{t('no_project_title')}</div>
        <div className="text-xs text-ink-ghost">{t('no_project_hint')}</div>
      </div>
    )
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
                // ANNOTATIONS
              </span>
              <span className="font-mono text-[10px] tracking-[0.14em] text-ink-muted tabular-nums">
                {total} TOTAL · {confirmed} CONFIRMED · {pending} PENDING
              </span>
            </>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <MessageSquare size={14} className="text-ink" aria-hidden="true" />
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
            <div className="hidden items-baseline gap-x-3 text-xs text-ink-muted md:flex">
              <span>
                {t('total_label')} <span className="font-semibold tabular-nums text-ink">{total}</span>
              </span>
              <span aria-hidden="true" className="text-ink-ghost">·</span>
              <span>
                <span className="font-semibold tabular-nums text-ink">{confirmed}</span> {t('confirmed_label')}
              </span>
              <span aria-hidden="true" className="text-ink-ghost">·</span>
              <span>
                <span className="font-semibold tabular-nums text-ink">{pending}</span> {t('pending_label')}
              </span>
            </div>
          )}
          <motion.button
            onClick={handleRefresh}
            whileHover={reduce ? undefined : { scale: 1.05 }}
            whileTap={reduce ? undefined : { scale: 0.95 }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            aria-label={t('refresh_aria')}
            title={t('refresh_aria')}
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
      <div className={`flex flex-shrink-0 flex-wrap items-center gap-3 bg-white px-6 py-3 ${industrial ? 'border-b border-ink' : 'border-b border-border'}`}>
        <div className={`flex h-8 flex-1 min-w-[200px] max-w-md items-center gap-1.5 bg-white px-2.5 ${
          industrial ? 'border border-ink' : 'rounded-md border border-border focus-within:border-ink'
        }`}>
          <Search className="h-3.5 w-3.5 flex-shrink-0 text-ink-ghost" aria-hidden="true" />
          <input
            className={`min-w-0 flex-1 bg-transparent outline-none placeholder:text-ink-ghost ${
              industrial ? 'font-mono text-[12px] tracking-[0.04em] text-ink' : 'text-sm text-ink'
            }`}
            placeholder={industrial ? 'SEARCH...' : t('search_placeholder')}
            aria-label={t('search_aria')}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <div role="tablist" className={`flex flex-shrink-0 overflow-hidden ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}>
          {(['all', 'pending', 'confirmed'] as StatusFilter[]).map((s) => {
            const active = statusFilter === s
            const label = s === 'all' ? t('filter_all') : s === 'pending' ? t('filter_pending') : t('filter_confirmed')
            return (
              <motion.button
                key={s}
                role="tab"
                aria-selected={active}
                onClick={() => setStatusFilter(s)}
                whileTap={reduce ? undefined : { scale: 0.97 }}
                transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                className={`px-3 py-1.5 outline-none last:border-r-0 cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                  industrial
                    ? 'border-r border-ink font-mono text-[10px] uppercase tracking-[0.14em]'
                    : 'border-r border-border text-xs'
                } ${
                  active ? 'bg-ink text-white font-medium' : 'text-ink-muted'
                }`}
              >
                {label}
              </motion.button>
            )
          })}
        </div>
      </div>

      {/* Scrollable list */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        {loading ? (
          <div className="flex h-full items-center justify-center">
            <InlineLoader text={t('loading')} />
          </div>
        ) : items.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
            <div className="text-sm text-ink-muted">{t('empty_title')}</div>
            <div className="text-xs text-ink-ghost">
              {search ? t('empty_hint_search') : t('empty_hint_default')}
            </div>
          </div>
        ) : (
          <div ref={listRef} className="divide-y divide-border-light bg-white">
            {items.map((item) => (
              <div
                key={item.id}
                className={`space-y-2 px-6 py-3.5 transition-all duration-300 ${
                  fadingOut.has(item.id) ? 'max-h-0 overflow-hidden py-0 opacity-0' : 'max-h-[500px] opacity-100'
                }`}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="text-sm leading-relaxed text-ink">{item.question}</div>
                  </div>
                  <div className="flex flex-shrink-0 items-center gap-2">
                    <span className="text-[11px] tabular-nums text-ink-ghost">{formatDate(item.createdAt)}</span>
                    <motion.button
                      onClick={() => toggleStatus(item)}
                      whileHover={reduce ? undefined : { scale: 1.03 }}
                      whileTap={reduce ? undefined : { scale: 0.97 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      aria-pressed={item.status}
                      className={`border px-2 py-0.5 font-medium outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                        industrial
                          ? 'font-mono text-[10px] uppercase tracking-[0.14em]'
                          : 'rounded-full text-[11px]'
                      } ${
                        item.status
                          ? (industrial ? 'border-success bg-success text-white' : 'border-success/30 bg-success/10 text-success')
                          : (industrial ? 'border-ink bg-canvas-alt text-ink' : 'border-border bg-canvas-alt text-ink-muted')
                      }`}
                      title={t('status_toggle_title')}
                    >
                      {item.status ? t('status_confirmed') : t('status_pending')}
                    </motion.button>
                    <motion.button
                      onClick={() => deleteItem(item.id)}
                      whileHover={reduce ? undefined : { scale: 1.15 }}
                      whileTap={reduce ? undefined : { scale: 0.9 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      className="p-0.5 text-ink-ghost outline-none hover:text-danger focus-visible:ring-1 focus-visible:ring-ink"
                      title={t('delete_title')}
                      aria-label={t('delete_aria')}
                    >
                      <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                    </motion.button>
                  </div>
                </div>

                <div>
                  {editingId === item.id ? (
                    <TokenEditor
                      initial={item.tokens}
                      onSave={(tokens) => saveTokens(item.id, tokens)}
                      onCancel={() => setEditingId(null)}
                    />
                  ) : (
                    <TokenChips
                      tokens={item.tokens}
                      mappings={item.tokenMappings}
                      onClick={() => setEditingId(item.id)}
                    />
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Pagination footer */}
      {totalPages > 1 && !loading && items.length > 0 && (
        <div className="flex flex-shrink-0 items-center justify-between gap-3 border-t border-border bg-white px-6 py-2.5">
          <AnimatedButton
            variant="ghost"
            size="sm"
            disabled={page === 1}
            onClick={() => setPage(p => Math.max(1, p - 1))}
          >
            {t('prev_page')}
          </AnimatedButton>
          <span className="text-xs text-ink-muted">
            {t('pagination_page', { page, total: totalPages, count: total })}
          </span>
          <AnimatedButton
            variant="ghost"
            size="sm"
            disabled={page >= totalPages}
            onClick={() => setPage(p => Math.min(totalPages, p + 1))}
          >
            {t('next_page')}
          </AnimatedButton>
        </div>
      )}
    </div>
  )
}
