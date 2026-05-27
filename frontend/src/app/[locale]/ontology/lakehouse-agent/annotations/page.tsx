'use client'

import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslations } from 'next-intl'
import { motion, useReducedMotion } from 'motion/react'
import { api, getApiBase } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { Trash2, Search, Check, X, MessageSquare, RefreshCw, Upload, Download, Sparkles } from 'lucide-react'
import { useAutoAnimate } from '@/lib/motion'

interface VecStatus {
  total: number
  withVector: number
  missing: number
  needsCompute: number
  // Project-wide status counts (header badges read these instead of
  // page-local item counts; otherwise the "X 已确认 / Y 待确认" numbers
  // describe only the visible 50 rows and look wildly wrong after a
  // bulk "select all matching" operation).
  confirmed?: number
  pending?: number
}

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
  // ── Bulk selection ────────────────────────────────────────────────
  //   selected: explicit per-row picks (overlaps with current page)
  //   allMatchingSelected: gmail-style "select all N matching" flag —
  //     when true, bulk ops apply to the full filter, NOT the ids list
  //   selection automatically clears whenever the filter/search/page changes
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [allMatchingSelected, setAllMatchingSelected] = useState(false)
  const [bulkRunning, setBulkRunning] = useState(false)
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
  // Any filter/page change invalidates the selection (the underlying row set changes).
  useEffect(() => {
    setSelected(new Set())
    setAllMatchingSelected(false)
  }, [search, statusFilter, page, currentProject?.id])

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

  // ── Vector status + batch compute (mirrors lakehouse-keywords) ──
  const [vecStatus, setVecStatus] = useState<VecStatus | null>(null)
  const [computing, setComputing] = useState(false)
  const [vecProgress, setVecProgress] = useState<{ done: number; total: number } | null>(null)
  const [importing, setImporting] = useState(false)
  const [exporting, setExporting] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const loadVecStatus = useCallback(async () => {
    if (!currentProject?.id) return
    try {
      const res = await api<VecStatus>(
        `/ontology/agent-annotations-vector-status?projectId=${currentProject.id}`,
      )
      setVecStatus(res)
    } catch {
      setVecStatus(null)
    }
  }, [currentProject])
  useEffect(() => { loadVecStatus() }, [loadVecStatus])

  // SSE stream reader — same shape the lakehouse-keywords page uses.
  const startCompute = useCallback(async () => {
    if (!currentProject?.id || computing) return
    setComputing(true)
    setVecProgress({ done: 0, total: 0 })
    try {
      const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
      const res = await fetch(
        `${getApiBase()}/ontology/agent-annotations-recompute?projectId=${currentProject.id}&all=true`,
        { method: 'POST', headers: token ? { Authorization: `Bearer ${token}` } : {} },
      )
      if (!res.ok || !res.body) {
        msg.error(t('compute_failed_status', { status: res.status }))
        return
      }
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buf = ''
      let embedded = 0
      let failed = 0
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        const lines = buf.split('\n')
        buf = lines.pop() || ''
        for (const ln of lines) {
          if (!ln.startsWith('data: ')) continue
          try {
            const evt = JSON.parse(ln.slice(6))
            if (evt.type === 'start') setVecProgress({ done: 0, total: evt.total || 0 })
            else if (evt.type === 'progress') setVecProgress({ done: evt.done || 0, total: evt.total || 0 })
            else if (evt.type === 'error') msg.error(typeof evt.msg === 'string' ? evt.msg : t('compute_error'))
            else if (evt.type === 'done') { embedded = evt.embedded || 0; failed = evt.failed || 0 }
          } catch { /* skip malformed line */ }
        }
      }
      if (failed > 0) msg.error(t('compute_done_partial', { embedded, failed }))
      else if (embedded > 0) msg.success(t('compute_done_ok', { embedded }))
      else msg.success(t('compute_no_op'))
    } catch (e) {
      msg.error(t('compute_failed_err', { message: (e as Error).message }))
    } finally {
      setComputing(false)
      setVecProgress(null)
      loadVecStatus()
    }
  }, [currentProject, computing, loadVecStatus, msg, t])

  const handleImportClick = () => fileInputRef.current?.click()

  const handleImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = '' // allow re-selecting the same file later
    if (!file || !currentProject?.id) return
    setImporting(true)
    try {
      const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
      const form = new FormData()
      form.append('file', file)
      const res = await fetch(
        `${getApiBase()}/ontology/agent-annotations-import?projectId=${currentProject.id}`,
        { method: 'POST', body: form, headers: token ? { Authorization: `Bearer ${token}` } : {} },
      )
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        msg.error(t('import_fail', { message: data?.error || `HTTP ${res.status}` }))
        return
      }
      const inserted = data.inserted ?? 0
      const skipped = data.skipped ?? 0
      const failed = data.failed ?? 0
      if (failed > 0) msg.error(t('import_partial', { inserted, skipped, failed }))
      else msg.success(t('import_success', { inserted, skipped }))
      load()
      loadVecStatus()
    } catch (err) {
      msg.error(t('import_fail', { message: (err as Error).message }))
    } finally {
      setImporting(false)
    }
  }

  const handleExport = async () => {
    if (!currentProject?.id || exporting) return
    setExporting(true)
    try {
      const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
      const res = await fetch(
        `${getApiBase()}/ontology/agent-annotations-export?projectId=${currentProject.id}`,
        { headers: token ? { Authorization: `Bearer ${token}` } : {} },
      )
      if (!res.ok) {
        const errBody = await res.text().catch(() => '')
        msg.error(t('export_fail', { message: errBody || `HTTP ${res.status}` }))
        return
      }
      const blob = await res.blob()
      // Prefer server-provided filename, fall back to a project-stamped default.
      const cd = res.headers.get('Content-Disposition') || ''
      const m = /filename="([^"]+)"/.exec(cd)
      const filename = m?.[1] || `annotations_${currentProject.id}_${new Date().toISOString().slice(0, 10).replace(/-/g, '')}.csv`
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (err) {
      msg.error(t('export_fail', { message: (err as Error).message }))
    } finally {
      setExporting(false)
    }
  }

  // ── Bulk selection helpers ──────────────────────────────────────
  const toggleSelectOne = (id: string) => {
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
    setAllMatchingSelected(false)
  }

  const allOnPageSelected = items.length > 0 && items.every(i => selected.has(i.id))
  const someOnPageSelected = items.some(i => selected.has(i.id))

  const toggleSelectAllOnPage = () => {
    if (allOnPageSelected) {
      // Deselect just the current-page ids (keep any cross-page picks intact).
      setSelected(prev => {
        const next = new Set(prev)
        items.forEach(i => next.delete(i.id))
        return next
      })
      setAllMatchingSelected(false)
    } else {
      setSelected(prev => {
        const next = new Set(prev)
        items.forEach(i => next.add(i.id))
        return next
      })
    }
  }

  const selectAllMatching = () => setAllMatchingSelected(true)

  const clearSelection = () => {
    setSelected(new Set())
    setAllMatchingSelected(false)
  }

  // Effective count the bulk action will hit. When "all matching" is on we
  // trust the server-reported total; otherwise we count explicit ids.
  const effectiveSelectedCount = allMatchingSelected ? total : selected.size

  const applyBulkStatus = async (newStatus: boolean) => {
    if (!currentProject?.id || bulkRunning) return
    if (effectiveSelectedCount === 0) {
      msg.error(t('bulk_no_selection'))
      return
    }
    setBulkRunning(true)
    try {
      const body: Record<string, unknown> = { status: newStatus }
      if (allMatchingSelected) {
        body.selectAll = true
        if (search) body.search = search
        body.statusFilter = statusFilter === 'pending' ? 'false' : statusFilter === 'confirmed' ? 'true' : ''
      } else {
        body.ids = Array.from(selected)
      }
      const res = await api<{ updated: number }>(
        `/ontology/agent-annotations-bulk-status?projectId=${currentProject.id}`,
        { method: 'POST', body },
      )
      msg.success(t('bulk_success', { updated: res.updated ?? 0 }))
      clearSelection()
      load()
      loadVecStatus()
    } catch (e) {
      msg.error(t('bulk_fail', { message: (e as Error).message }))
    } finally {
      setBulkRunning(false)
    }
  }

  const handleRefresh = () => { setRefreshKey(k => k + 1); load(); loadVecStatus() }

  // Header badges show PROJECT-wide counts (from vec-status endpoint) when
  // available — falls back to current-page counts on first render before the
  // status load completes. Otherwise "X 已确认 · Y 待确认" describes only the
  // visible 50 rows, which looked like a bug right after a bulk "select all
  // matching" run that actually touched rows across pages.
  const confirmed = vecStatus?.confirmed ?? items.filter(i => i.status).length
  const pending = vecStatus?.pending ?? items.filter(i => !i.status).length

  // Early-return AFTER all hooks (React Rules of Hooks: every hook must run
  // every render in the same order). Earlier we returned here before declaring
  // vecStatus / startCompute / etc., which made the hook count jump from
  // ~10 to ~20 once a project loaded — triggering React error #310.
  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
        <div className="text-sm text-ink-muted">{t('no_project_title')}</div>
        <div className="text-xs text-ink-ghost">{t('no_project_hint')}</div>
      </div>
    )
  }

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
          {/* Hidden file input — opens native picker when 导入 button clicked. */}
          <input
            ref={fileInputRef}
            type="file"
            accept=".csv,text/csv"
            className="hidden"
            onChange={handleImportFile}
            aria-hidden="true"
          />
          <AnimatedButton
            variant="ghost"
            size="sm"
            onClick={handleImportClick}
            disabled={importing}
            aria-label={t('import_aria')}
            title={t('import_aria')}
          >
            <Upload size={12} aria-hidden="true" />
            <span>{importing ? t('import_running') : t('import_button')}</span>
          </AnimatedButton>
          <AnimatedButton
            variant="ghost"
            size="sm"
            onClick={handleExport}
            disabled={exporting || !vecStatus || vecStatus.total === 0}
            aria-label={t('export_aria')}
            title={t('export_aria')}
          >
            <Download size={12} aria-hidden="true" />
            <span>{exporting ? t('export_running') : t('export_button')}</span>
          </AnimatedButton>
          <AnimatedButton
            variant="primary"
            size="sm"
            onClick={startCompute}
            disabled={computing || !vecStatus || vecStatus.needsCompute === 0}
            aria-label={t('compute_vec_aria')}
            title={vecStatus?.needsCompute === 0 ? t('vec_all_ready') : t('compute_vec_aria')}
          >
            <motion.span
              className="inline-flex"
              animate={computing && !reduce ? { rotate: 360 } : { rotate: 0 }}
              transition={computing ? { repeat: Infinity, duration: 1, ease: 'linear' } : { duration: 0 }}
              aria-hidden="true"
            >
              <Sparkles size={12} />
            </motion.span>
            {computing && vecProgress ? (
              <span>{t('computing', { done: vecProgress.done, total: vecProgress.total })}</span>
            ) : (
              <span>{t('compute_vec')}{vecStatus?.needsCompute ? ` (${vecStatus.needsCompute})` : ''}</span>
            )}
          </AnimatedButton>
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
        {/* Bulk-select-page checkbox.
            indeterminate = some-but-not-all on the current page are checked. */}
        <input
          type="checkbox"
          className="h-3.5 w-3.5 flex-shrink-0 cursor-pointer accent-ink"
          checked={allOnPageSelected}
          ref={el => { if (el) el.indeterminate = !allOnPageSelected && someOnPageSelected }}
          onChange={toggleSelectAllOnPage}
          aria-label={t('bulk_select_page_aria')}
          title={t('bulk_select_page_aria')}
          disabled={items.length === 0}
        />
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

      {/* Bulk-action toolbar — appears whenever selection is non-empty.
          The "select all matching" link upgrades the picked-id set into a
          server-side filter so the bulk endpoint can update across pages. */}
      {(selected.size > 0 || allMatchingSelected) && (
        <div className={`flex flex-shrink-0 flex-wrap items-center gap-3 bg-canvas-alt px-6 py-2 ${
          industrial ? 'border-b border-ink' : 'border-b border-border'
        }`}>
          <span className={`flex-shrink-0 ${industrial ? 'font-mono text-[11px] tracking-[0.14em]' : 'text-xs'} text-ink-muted`}>
            {allMatchingSelected
              ? t('bulk_all_matching_selected', { total })
              : t('bulk_selected_label', { count: selected.size })}
          </span>
          {!allMatchingSelected && allOnPageSelected && total > items.length && (
            <button
              type="button"
              onClick={selectAllMatching}
              className="text-xs text-ink underline-offset-2 hover:underline focus-visible:ring-1 focus-visible:ring-ink"
            >
              {t('bulk_select_all_matching_link', { total })}
            </button>
          )}
          <div className="ml-auto flex flex-shrink-0 items-center gap-2">
            <AnimatedButton
              variant="primary"
              size="sm"
              onClick={() => applyBulkStatus(true)}
              disabled={bulkRunning || effectiveSelectedCount === 0}
              aria-label={t('bulk_confirm')}
              title={t('bulk_confirm')}
            >
              <Check size={12} aria-hidden="true" />
              <span>{bulkRunning ? t('bulk_running') : `${t('bulk_confirm')} (${effectiveSelectedCount})`}</span>
            </AnimatedButton>
            <AnimatedButton
              variant="ghost"
              size="sm"
              onClick={() => applyBulkStatus(false)}
              disabled={bulkRunning || effectiveSelectedCount === 0}
              aria-label={t('bulk_unconfirm')}
              title={t('bulk_unconfirm')}
            >
              <X size={12} aria-hidden="true" />
              <span>{bulkRunning ? t('bulk_running') : `${t('bulk_unconfirm')} (${effectiveSelectedCount})`}</span>
            </AnimatedButton>
            <AnimatedButton
              variant="ghost"
              size="sm"
              onClick={clearSelection}
              disabled={bulkRunning}
              aria-label={t('bulk_clear')}
              title={t('bulk_clear')}
            >
              {t('bulk_clear')}
            </AnimatedButton>
          </div>
        </div>
      )}

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
                } ${
                  (allMatchingSelected || selected.has(item.id)) ? 'bg-canvas-alt/60' : ''
                }`}
              >
                <div className="flex items-start justify-between gap-3">
                  <input
                    type="checkbox"
                    className="mt-1 h-3.5 w-3.5 flex-shrink-0 cursor-pointer accent-ink"
                    checked={allMatchingSelected || selected.has(item.id)}
                    onChange={() => toggleSelectOne(item.id)}
                    disabled={allMatchingSelected}
                    aria-label={t('bulk_select_row_aria')}
                    title={t('bulk_select_row_aria')}
                  />
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
