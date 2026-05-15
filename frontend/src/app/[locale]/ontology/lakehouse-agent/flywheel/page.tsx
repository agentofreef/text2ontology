'use client'

import { useState, useEffect, useCallback } from 'react'
import { useTranslations } from 'next-intl'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { ResultViewer } from '@/components/ui/ResultViewer'
import {
  Trash2, Plus, X, Tag, Search, ChevronDown, ChevronRight,
  BookOpen, Play, User, Activity, Star,
} from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useAutoAnimate } from '@/lib/motion'

interface QueryLog {
  id: string
  userQuestion: string
  tokens: string[] | null
  objects: string
  metric: string
  groupBy: string
  generatedSql: string
  executionStatus: string
  mark: boolean
  isExample: boolean
  source: string
  createdAt: string
}

const SOURCE_LABEL_KEYS: Record<string, string> = {
  chat: 'source_label_chat',
  test: 'source_label_test',
}

interface LogDetail {
  id: string
  userQuestion: string
  generatedSql: string
  executionStatus: string
  executionResult: string
  executionError: string
  source: string
  functionCalls?: Array<{ name: string; arguments: Record<string, unknown>; result: Record<string, unknown> }>
  finalAnswer?: string
}

const TOOL_META_KEYS: Record<string, { labelKey: string }> = {
  lookup:     { labelKey: 'tool_lookup_label' },
  smartquery: { labelKey: 'tool_smartquery_label' },
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

function StatusBadge({ status }: { status: string }) {
  const t = useTranslations('agent.flywheel')
  const industrial = useStyleMode().mode === 'industrial'
  const ok = status === 'success'
  return (
    <span className={`border px-1.5 py-0.5 font-medium ${
      industrial ? 'font-mono text-[10px] uppercase tracking-[0.14em]' : 'rounded text-[11px]'
    } ${
      ok
        ? (industrial ? 'border-success bg-white text-success' : 'border-success/40 bg-success/5 text-success')
        : (industrial ? 'border-danger bg-white text-danger' : 'border-danger/40 bg-danger/5 text-danger')
    }`}>
      {ok ? t('status_success') : status || t('status_fail')}
    </span>
  )
}

function MdContent({ content }: { content: string }) {
  return (
    <div className="text-xs text-ink-muted">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={{
        p: ({ children }) => <p className="mb-1 text-xs leading-relaxed">{children}</p>,
        h1: ({ children }) => <h1 className="mb-1 border-b border-border-light pb-0.5 text-xs font-semibold text-ink">{children}</h1>,
        h2: ({ children }) => <h2 className="mb-0.5 text-xs font-semibold text-ink">{children}</h2>,
        ul: ({ children }) => <ul className="mb-1 ml-3 list-disc text-xs">{children}</ul>,
        li: ({ children }) => <li className="mb-0.5">{children}</li>,
        code: ({ children }) => <code className="rounded bg-canvas-alt px-1 text-[11px] font-mono text-ink">{children}</code>,
        pre: ({ children }) => <pre className="overflow-x-auto rounded bg-canvas-alt px-2 py-1 text-[11px] font-mono text-ink">{children}</pre>,
        strong: ({ children }) => <strong className="font-semibold text-ink">{children}</strong>,
        hr: () => <hr className="my-1 border-border-light" />,
      }}>{content}</ReactMarkdown>
    </div>
  )
}

function FunctionCallRound({ fc, defaultOpen }: {
  fc: { name: string; arguments: Record<string, unknown>; result: Record<string, unknown> }
  defaultOpen: boolean
}) {
  const t = useTranslations('agent.flywheel')
  const [open, setOpen] = useState(defaultOpen)
  const meta = TOOL_META_KEYS[fc.name] || { labelKey: fc.name }
  const result = fc.result || {}
  const reduce = useReducedMotion()

  return (
    <div className="overflow-hidden rounded-md border border-border">
      <motion.button
        onClick={() => setOpen(!open)}
        whileTap={reduce ? undefined : { scale: 0.99 }}
        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
        aria-expanded={open}
        className="flex w-full cursor-pointer items-center gap-2 border-b border-border-light bg-canvas-alt px-3 py-2 text-left outline-none focus-visible:ring-1 focus-visible:ring-ink"
      >
        {open ? <ChevronDown size={12} className="text-ink-ghost" aria-hidden="true" /> : <ChevronRight size={12} className="text-ink-ghost" aria-hidden="true" />}
        {fc.name === 'lookup' && <BookOpen className="h-3 w-3 text-ink-muted" aria-hidden="true" />}
        {fc.name === 'smartquery' && <Play className="h-3 w-3 text-ink-muted" aria-hidden="true" />}
        <span className="text-[11px] font-semibold text-ink">{meta.labelKey in TOOL_META_KEYS ? t(meta.labelKey as Parameters<typeof t>[0]) : meta.labelKey}</span>
        {fc.name === 'smartquery' && Array.isArray(fc.arguments.objects) && (
          <span className="ml-1 text-[11px] font-mono text-ink-ghost">
            [{(fc.arguments.objects as string[]).join(', ')}]
          </span>
        )}
      </motion.button>
      <AnimatePresence initial={false}>
        {open && (
          <motion.div
            initial={reduce ? undefined : { height: 0, opacity: 0 }}
            animate={reduce ? undefined : { height: 'auto', opacity: 1 }}
            exit={reduce ? undefined : { height: 0, opacity: 0 }}
            transition={{ duration: 0.2, ease: 'easeOut' }}
            style={{ overflow: 'hidden' }}
          >
            <div className="space-y-2 px-3 py-2">
              {fc.name === 'lookup' && (
                <div className="space-y-1">
                  {((fc.arguments.ontology_name as string[]) || []).length > 0 && (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-[11px] text-ink-ghost">{t('tool_label_body')}</span>
                      {((fc.arguments.ontology_name as string[]) || []).map((n, i) => (
                        <span key={i} className="rounded-md bg-ink px-1.5 py-0.5 text-[11px] font-mono text-white">{n}</span>
                      ))}
                    </div>
                  )}
                  {((fc.arguments.keyword as string[]) || []).length > 0 && (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-[11px] text-ink-ghost">{t('tool_label_keyword')}</span>
                      {((fc.arguments.keyword as string[]) || []).map((k, i) => (
                        <span key={i} className="rounded border border-success/40 bg-success/5 px-1.5 py-0.5 text-[11px] text-success">{k}</span>
                      ))}
                    </div>
                  )}
                </div>
              )}
              {fc.name === 'smartquery' && (
                <div className="space-y-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="text-[11px] text-ink-ghost">{t('tool_label_object')}</span>
                    {((fc.arguments.objects as string[]) || []).map((o, i) => (
                      <span key={i} className="rounded-md bg-ink px-1.5 py-0.5 text-[11px] font-mono text-white">{o}</span>
                    ))}
                    {!!fc.arguments.metric && (
                      <>
                        <span className="ml-1 text-[11px] text-ink-ghost">{t('tool_label_metric')}</span>
                        <span className="rounded border border-border bg-white px-1.5 py-0.5 text-[11px] font-mono text-ink">{String(fc.arguments.metric)}</span>
                      </>
                    )}
                  </div>
                  {((fc.arguments.groupBy as string[]) || []).length > 0 && (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-[11px] text-ink-ghost">{t('tool_label_group')}</span>
                      {((fc.arguments.groupBy as string[]) || []).map((g, i) => (
                        <span key={i} className="rounded border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] font-mono text-ink-muted">{g}</span>
                      ))}
                    </div>
                  )}
                </div>
              )}
              {!['lookup', 'smartquery'].includes(fc.name) && Object.keys(fc.arguments).length > 0 && (
                <pre className="max-h-24 overflow-y-auto whitespace-pre-wrap rounded border border-border bg-canvas-alt px-2 py-1 text-[11px] font-mono text-ink">
                  {JSON.stringify(fc.arguments, null, 2)}
                </pre>
              )}
              {result.error != null && result.error !== '' && (
                <div className="rounded border border-danger/40 bg-danger/5 px-2 py-1 text-[11px] text-danger">{String(result.error)}</div>
              )}
              {result.content != null && result.content !== '' && (
                <div className="max-h-48 overflow-y-auto rounded border border-border bg-white px-2 py-1.5">
                  <MdContent content={String(result.content)} />
                </div>
              )}
              {result.generated_sql != null && result.generated_sql !== '' && (
                <div>
                  <div className="mb-0.5 text-[11px] text-ink-ghost">SQL</div>
                  <pre className="max-h-32 overflow-y-auto whitespace-pre-wrap rounded bg-canvas-alt px-2 py-1 text-[11px] font-mono leading-relaxed text-ink">
                    {String(result.generated_sql)}
                  </pre>
                </div>
              )}
              {result.execution_status === 'success' && result.execution_result != null && (
                <ResultViewer data={String(result.execution_result)} initialMode={(result.display_mode as 'table' | 'bar' | 'pie' | 'line') || 'table'} />
              )}
              {result.execution_status === 'error' && result.execution_error != null && (
                <div className="rounded border border-danger/40 bg-danger/5 px-2 py-1 text-[11px] text-danger">{String(result.execution_error)}</div>
              )}
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

interface TokenAnnotation {
  id: string
  token: string
  objectName: string
  propertyName: string
  metricName: string
  note: string
  mark: boolean
}

interface OntObject {
  id: string; name: string; kind: string
  properties?: Array<{ name: string; displayName: string; dataType: string }>
  metrics?: Array<{ name: string; aggregation: string; targetProperty: string }>
}

const PAGE_SIZE = 20

export default function LakehouseFlywheelPageMinimal() {
  const t = useTranslations('agent.flywheel')
  const industrial = useStyleMode().mode === 'industrial'
  const [logs, setLogs] = useState<QueryLog[]>([])
  const [editingRow] = useState<string | null>(null)
  const [annotations, setAnnotations] = useState<TokenAnnotation[]>([])
  const [objects, setObjects] = useState<OntObject[]>([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState<'all' | 'marked' | 'unmarked'>('all')
  const [search, setSearch] = useState('')
  const [apiSearch, setApiSearch] = useState('')
  const [tab, setTab] = useState<'logs' | 'annotations'>('logs')
  const [suiteFilter, setSuiteFilter] = useState('')
  const [testSuites, setTestSuites] = useState<Array<{ id: string; name: string }>>([])
  const [page, setPage] = useState(1)
  const [total, setTotal] = useState(0)

  const [selectedLogId, setSelectedLogId] = useState<string | null>(null)
  const [logDetail, setLogDetail] = useState<LogDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)

  const [annotateModal, setAnnotateModal] = useState<{ token: string } | null>(null)
  const [annObj, setAnnObj] = useState('')
  const [annProp, setAnnProp] = useState('')
  const [annMetric, setAnnMetric] = useState('')
  const [annNote, setAnnNote] = useState('')
  const [annMode, setAnnMode] = useState<'property' | 'metric'>('property')
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()

  const logListRef = useAutoAnimate<HTMLDivElement>()
  const annListRef = useAutoAnimate<HTMLDivElement>()

  useEffect(() => {
    const t = setTimeout(() => { setApiSearch(search); setPage(1) }, 400)
    return () => clearTimeout(t)
  }, [search])

  useEffect(() => {
    if (!currentProject?.id) return
    api<{ data: Array<{ id: string; name: string }> }>(`/ontology/lh-test-suites?projectId=${currentProject.id}`)
      .then(res => setTestSuites(res.data || []))
      .catch(() => {})
  }, [currentProject])

  const fetchLogs = useCallback(async () => {
    if (!currentProject?.id) return
    setLoading(true)
    try {
      let logUrl = `/ontology/query-logs?projectId=${currentProject.id}&sourceType=lakehouse`
      logUrl += `&page=${page}&pageSize=${PAGE_SIZE}`
      if (suiteFilter === '__chat__') logUrl += '&source=chat'
      else if (suiteFilter) logUrl += `&suiteId=${suiteFilter}`
      if (filter === 'marked') logUrl += '&mark=true'
      else if (filter === 'unmarked') logUrl += '&mark=false'
      if (apiSearch) logUrl += `&search=${encodeURIComponent(apiSearch)}`
      const logRes = await api<{ data: QueryLog[]; total: number }>(logUrl)
      setLogs(logRes.data || [])
      setTotal(logRes.total || 0)
    } catch { /* */ } finally { setLoading(false) }
  }, [currentProject, suiteFilter, filter, apiSearch, page])

  const fetchMeta = useCallback(async () => {
    if (!currentProject?.id) return
    try {
      const annRes = await api<{ data: TokenAnnotation[] }>(`/ontology/token-annotations?projectId=${currentProject.id}`)
      setAnnotations(annRes.data || [])
      const objRes = await api<{ data: Array<{ id: string; name: string; kind: string; properties?: Array<{ name: string; displayName: string; dataType: string }> }> }>(`/ontology/objects?projectId=${currentProject.id}`)
      const metricRes = await api<{ data: Array<{ name: string; aggregation: string; targetProperty: string; targetObjectId: string }> }>(`/ontology/metrics?projectId=${currentProject.id}`)
      const objs: OntObject[] = (objRes.data || []).map(o => ({
        ...o,
        properties: o.properties || [],
        metrics: (metricRes.data || []).filter(m => m.targetObjectId === o.id).map(m => ({
          name: m.name, aggregation: m.aggregation, targetProperty: m.targetProperty,
        })),
      }))
      setObjects(objs)
    } catch { /* */ }
  }, [currentProject])

  useEffect(() => { fetchLogs() }, [fetchLogs])
  useEffect(() => { fetchMeta() }, [fetchMeta])

  const fetchAnnotations = useCallback(async () => {
    if (!currentProject?.id) return
    const res = await api<{ data: TokenAnnotation[] }>(`/ontology/token-annotations?projectId=${currentProject.id}`)
    setAnnotations(res.data || [])
  }, [currentProject])

  const selectLog = async (id: string) => {
    if (selectedLogId === id) { setSelectedLogId(null); setLogDetail(null); return }
    setSelectedLogId(id)
    setDetailLoading(true)
    try {
      const res = await api<LogDetail>(`/ontology/query-logs/${id}`)
      setLogDetail(res)
    } catch { setLogDetail(null) } finally { setDetailLoading(false) }
  }

  const toggleMark = async (id: string, mark: boolean) => {
    try {
      await api(`/ontology/query-logs/${id}/mark`, { method: 'PUT', body: { mark } })
      setLogs(prev => prev.map(l => l.id === id ? { ...l, mark } : l))
    } catch { msg.error(t('op_fail')) }
  }

  const deleteLog = async (id: string) => {
    try {
      await api(`/ontology/query-logs/${id}`, { method: 'DELETE' })
      setLogs(prev => prev.filter(l => l.id !== id))
      setTotal(prev => prev - 1)
    } catch { msg.error(t('delete_fail')) }
  }

  const addAnnotation = async () => {
    if (!annotateModal || !annObj || !currentProject?.id) return
    const sortedObj = annObj.split(',').filter(Boolean).sort().join(',')
    try {
      await api(`/ontology/token-annotations?projectId=${currentProject.id}`, {
        method: 'POST',
        body: { token: annotateModal.token, objectName: sortedObj, propertyName: annProp, metricName: annMetric, note: annNote },
      })
      msg.success(t('annotation_save_success', { token: annotateModal.token, obj: annObj + (annMetric ? '.' + annMetric : '') }))
      setAnnotateModal(null)
      setAnnObj(''); setAnnMetric(''); setAnnNote('')
      fetchAnnotations()
    } catch { msg.error(t('annotation_save_fail')) }
  }

  const deleteAnnotation = async (id: string) => {
    try {
      await api(`/ontology/token-annotations/${id}`, { method: 'DELETE' })
      setAnnotations(prev => prev.filter(a => a.id !== id))
    } catch { msg.error(t('annotation_delete_fail')) }
  }

  const handleFilterChange = (f: 'all' | 'marked' | 'unmarked') => { setFilter(f); setPage(1) }
  const handleSuiteChange = (s: string) => { setSuiteFilter(s); setPage(1) }

  const totalPages = Math.ceil(total / PAGE_SIZE)
  const searchLower = search.toLowerCase()
  const filteredAnnotations = annotations.filter(a => !search
    || a.token.toLowerCase().includes(searchLower)
    || a.objectName.toLowerCase().includes(searchLower)
  )

  void editingRow

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
                // DATA FLYWHEEL
              </span>
              <span className="font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-muted">
                {total} QUERIES · {annotations.length} ANNOTATIONS
              </span>
            </>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <Activity size={14} className="text-ink" aria-hidden="true" />
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
        {!industrial && (
          <div className="hidden items-baseline gap-x-3 text-xs text-ink-muted md:flex">
            <span>
              {t('query_count', { count: total })}
            </span>
            <span aria-hidden="true" className="text-ink-ghost">·</span>
            <span>
              {t('annotation_count', { count: annotations.length })}
            </span>
          </div>
        )}
      </motion.header>

      {/* Tab strip */}
      <nav role="tablist" aria-label={t('view_switch_aria')} className={`flex flex-shrink-0 items-center gap-0 bg-white px-6 ${industrial ? 'border-b border-ink' : 'border-b border-border'}`}>
        {([
          ['logs', t('tab_logs'), total],
          ['annotations', t('tab_annotations'), annotations.length],
        ] as [string, string, number][]).map(([key, label, n]) => {
          const active = tab === key
          return (
            <motion.button
              key={key}
              role="tab"
              aria-selected={active}
              onClick={() => setTab(key as 'logs' | 'annotations')}
              whileHover={reduce ? undefined : { y: -1 }}
              whileTap={reduce ? undefined : { scale: 0.98 }}
              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
              className={`-mb-px flex h-10 items-center gap-1.5 border-b-2 px-4 outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                industrial ? 'font-mono text-[11px] uppercase tracking-[0.14em]' : 'text-sm'
              } ${
                active ? 'border-ink font-semibold text-ink' : 'border-transparent text-ink-muted hover:text-ink'
              }`}
            >
              {key === 'annotations' && <Tag size={12} aria-hidden="true" />}
              {label}
              <span className={industrial ? 'font-mono text-[10px] tabular-nums tracking-[0.06em] text-ink-ghost' : 'text-xs tabular-nums text-ink-ghost'}>({n})</span>
            </motion.button>
          )
        })}
      </nav>

      {/* Filter/search strip */}
      <div className={`flex flex-shrink-0 flex-wrap items-center gap-2 bg-white px-6 py-3 ${industrial ? 'border-b border-ink' : 'border-b border-border'}`}>
        {tab === 'logs' && (
          <>
            <div role="tablist" className={`flex overflow-hidden ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}>
              {(['all', 'marked', 'unmarked'] as const).map(f => {
                const active = filter === f
                const label = f === 'all' ? t('filter_all') : f === 'marked' ? t('filter_marked') : t('filter_unmarked')
                return (
                  <motion.button
                    key={f}
                    role="tab"
                    aria-selected={active}
                    onClick={() => handleFilterChange(f)}
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
            {testSuites.length > 0 && (
              <select
                value={suiteFilter}
                onChange={e => handleSuiteChange(e.target.value)}
                aria-label={t('source_filter_aria')}
                className={`h-7 bg-white px-2 text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink ${
                  industrial
                    ? 'border border-ink font-mono text-[11px] tracking-[0.04em]'
                    : 'rounded-md border border-border text-[11px]'
                }`}
              >
                <option value="">{t('source_all')}</option>
                <option value="__chat__">{t('source_chat')}</option>
                {testSuites.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}
              </select>
            )}
          </>
        )}
        <div className="flex-1" />
        <div className={`flex h-8 items-center gap-1.5 bg-white px-2.5 ${
          industrial ? 'border border-ink' : 'rounded-md border border-border focus-within:border-ink'
        }`}>
          <Search className="h-3.5 w-3.5 flex-shrink-0 text-ink-ghost" aria-hidden="true" />
          <input
            className={`w-56 bg-transparent outline-none placeholder:text-ink-ghost ${
              industrial ? 'font-mono text-[12px] tracking-[0.04em] text-ink' : 'text-sm text-ink'
            }`}
            placeholder={industrial ? 'SEARCH...' : (tab === 'logs' ? t('search_placeholder_logs') : t('search_placeholder_annotations'))}
            aria-label={t('search_aria')}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      {/* Main area */}
      {tab === 'logs' ? (
        <div className="flex flex-1 min-h-0 overflow-hidden">
          {/* Log list column */}
          <div className={`flex flex-col min-h-0 ${selectedLogId ? 'w-1/2 border-r border-border' : 'w-full'}`}>
            <div className="flex-1 min-h-0 overflow-y-auto" ref={logListRef}>
              {loading && logs.length === 0 ? (
                <div className="flex h-full items-center justify-center">
                  <InlineLoader text={t('loading')} />
                </div>
              ) : logs.length === 0 ? (
                <div className="flex h-full flex-col items-center justify-center gap-1 text-center">
                  <span className="text-sm text-ink-muted">{filter === 'marked' ? t('empty_marked_title') : t('empty_default_title')}</span>
                  <span className="text-xs text-ink-ghost">{filter === 'marked' ? t('empty_marked_hint') : t('empty_default_hint')}</span>
                </div>
              ) : (
                <div className="divide-y divide-border-light bg-white">
                  {logs.map(log => {
                    const isSelected = selectedLogId === log.id
                    return (
                      <div
                        key={log.id}
                        role="button"
                        tabIndex={0}
                        onClick={() => selectLog(log.id)}
                        onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectLog(log.id) } }}
                        className={`group cursor-pointer border-l-2 px-6 py-3 outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                          isSelected ? 'border-l-ink bg-canvas-alt' : 'border-l-transparent'
                        }`}
                      >
                        <div className="flex items-start gap-3">
                          <motion.button
                            onClick={(e) => { e.stopPropagation(); toggleMark(log.id, !log.mark) }}
                            whileHover={reduce ? undefined : { scale: 1.15 }}
                            whileTap={reduce ? undefined : { scale: 0.9 }}
                            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                            aria-pressed={log.mark}
                            aria-label={log.mark ? t('star_in_aria') : t('star_add_aria')}
                            title={log.mark ? t('star_in_title') : t('star_add_title')}
                            className={`mt-0.5 flex-shrink-0 outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                              log.mark ? 'text-ink' : 'text-ink-ghost hover:text-ink'
                            }`}
                          >
                            <Star className={`h-3.5 w-3.5 ${log.mark ? 'fill-ink' : ''}`} aria-hidden="true" />
                          </motion.button>
                          <div className="min-w-0 flex-1">
                            <div className="truncate text-sm text-ink">{log.userQuestion}</div>
                            <div className="mt-1 flex flex-wrap items-center gap-1.5">
                              {log.objects && log.objects.split(',').filter(Boolean).map((o, i) => (
                                <span key={i} className={`bg-ink px-1.5 py-0.5 font-mono text-white ${
                                  industrial ? 'text-[10px] tracking-[0.04em]' : 'rounded-md text-[11px]'
                                }`}>{o}</span>
                              ))}
                              {log.metric && (
                                <span className={`border bg-white px-1.5 py-0.5 font-mono text-ink ${
                                  industrial ? 'border-ink text-[10px] tracking-[0.04em]' : 'rounded border-border text-[11px]'
                                }`}>
                                  {log.metric}
                                </span>
                              )}
                              <StatusBadge status={log.executionStatus} />
                              <span className={industrial ? 'font-mono text-[10px] uppercase tracking-[0.12em] text-ink-ghost' : 'text-[11px] text-ink-ghost'}>
                                {SOURCE_LABEL_KEYS[log.source] ? t(SOURCE_LABEL_KEYS[log.source] as Parameters<typeof t>[0]) : (log.source || t('source_label_chat'))}
                              </span>
                            </div>
                          </div>
                          <div className="flex flex-shrink-0 flex-col items-end gap-1">
                            <span className="text-[11px] tabular-nums text-ink-ghost">
                              {new Date(log.createdAt).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })}
                            </span>
                            <motion.button
                              onClick={(e) => { e.stopPropagation(); deleteLog(log.id) }}
                              whileHover={reduce ? undefined : { scale: 1.15 }}
                              whileTap={reduce ? undefined : { scale: 0.9 }}
                              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                              className="p-0.5 text-ink-ghost outline-none hover:text-danger focus-visible:ring-1 focus-visible:ring-ink"
                              title={t('annotation_delete_title')}
                              aria-label={t('annotation_delete_aria')}
                            >
                              <Trash2 className="h-3 w-3" aria-hidden="true" />
                            </motion.button>
                          </div>
                        </div>
                      </div>
                    )
                  })}
                </div>
              )}
            </div>

            {totalPages > 1 && (
              <div className="flex flex-shrink-0 items-center justify-between gap-3 border-t border-border bg-white px-6 py-2.5">
                <AnimatedButton
                  variant="ghost"
                  size="sm"
                  disabled={page === 1}
                  onClick={() => setPage(p => p - 1)}
                >
                  {t('prev_page')}
                </AnimatedButton>
                <span className="text-xs text-ink-muted">
                  {t('pagination_page', { page, total: totalPages })}
                </span>
                <AnimatedButton
                  variant="ghost"
                  size="sm"
                  disabled={page >= totalPages}
                  onClick={() => setPage(p => p + 1)}
                >
                  {t('next_page')}
                </AnimatedButton>
              </div>
            )}
          </div>

          {/* Detail column */}
          <AnimatePresence>
            {selectedLogId && (
              <motion.div
                key={selectedLogId}
                initial={reduce ? undefined : { opacity: 0, x: 8 }}
                animate={reduce ? undefined : { opacity: 1, x: 0 }}
                exit={reduce ? undefined : { opacity: 0 }}
                transition={{ duration: 0.18, ease: 'easeOut' }}
                className="flex w-1/2 flex-col min-h-0 bg-white"
              >
                <div className="flex flex-shrink-0 items-center justify-between border-b border-border bg-canvas-alt px-4 py-2">
                  <span className="text-xs font-medium text-ink-muted">{t('detail_title')}</span>
                  <motion.button
                    onClick={() => { setSelectedLogId(null); setLogDetail(null) }}
                    whileHover={reduce ? undefined : { scale: 1.15 }}
                    whileTap={reduce ? undefined : { scale: 0.9 }}
                    transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                    aria-label={t('detail_close_aria')}
                    title={t('detail_close_title')}
                    className="rounded p-1 text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                  >
                    <X size={14} aria-hidden="true" />
                  </motion.button>
                </div>
                <div className="flex-1 min-h-0 overflow-y-auto">
                  {detailLoading ? (
                    <div className="flex h-full items-center justify-center">
                      <InlineLoader text={t('loading')} />
                    </div>
                  ) : logDetail ? (
                    <div className="space-y-3 p-4">
                      <div className="rounded-md border border-border bg-canvas-alt px-3 py-2">
                        <div className="mb-1 flex items-center gap-1.5">
                          <User size={12} className="text-ink-muted" aria-hidden="true" />
                          <span className="text-[11px] font-medium text-ink-ghost">{t('detail_question_label')}</span>
                        </div>
                        <div className="text-sm leading-relaxed text-ink">{logDetail.userQuestion}</div>
                      </div>
                      {logDetail.functionCalls && logDetail.functionCalls.length > 0 ? (
                        <div className="space-y-2">
                          <div className="text-[11px] font-medium text-ink-ghost">
                            {t('detail_fn_rounds')} <span className="tabular-nums">({logDetail.functionCalls.length})</span>
                          </div>
                          {logDetail.functionCalls.map((fc, i) => (
                            <FunctionCallRound key={i} fc={fc} defaultOpen={i === logDetail.functionCalls!.length - 1} />
                          ))}
                        </div>
                      ) : logDetail.generatedSql ? (
                        <div className="space-y-2">
                          <div className="text-[11px] font-medium text-ink-ghost">SQL</div>
                          <pre className="max-h-40 overflow-y-auto whitespace-pre-wrap rounded-md bg-canvas-alt px-3 py-2 text-[11px] font-mono leading-relaxed text-ink">
                            {logDetail.generatedSql}
                          </pre>
                          {logDetail.executionStatus === 'success' && logDetail.executionResult && (
                            <ResultViewer data={logDetail.executionResult} initialMode="table" />
                          )}
                          {logDetail.executionError && (
                            <div className="rounded border border-danger/40 bg-danger/5 px-2 py-1 text-[11px] text-danger">
                              {logDetail.executionError}
                            </div>
                          )}
                        </div>
                      ) : null}
                      {logDetail.finalAnswer && (
                        <div className="space-y-1">
                          <div className="text-[11px] font-medium text-ink-ghost">{t('detail_final_answer')}</div>
                          <div className="rounded-md border border-border bg-white px-3 py-2">
                            <MdContent content={logDetail.finalAnswer} />
                          </div>
                        </div>
                      )}
                    </div>
                  ) : (
                    <div className="flex h-full items-center justify-center text-xs text-ink-ghost">{t('detail_load_fail')}</div>
                  )}
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        </div>
      ) : (
        /* Token annotations tab */
        <div className="flex-1 min-h-0 overflow-y-auto">
          <div className="space-y-4 p-6">
            {/* Creator card */}
            <div className="space-y-2.5 rounded-md border border-border bg-white p-4">
              <div className="flex items-center gap-2">
                <input
                  className="h-8 flex-1 rounded-md border border-border px-2.5 text-sm text-ink outline-none focus:border-ink"
                  placeholder={t('annotation_kw_placeholder')}
                  aria-label={t('annotation_kw_aria')}
                  value={annotateModal?.token || ''}
                  onChange={(e) => setAnnotateModal({ token: e.target.value })}
                />
                <input
                  className="h-8 w-32 rounded-md border border-border px-2.5 text-sm text-ink outline-none focus:border-ink"
                  placeholder={t('annotation_note_inline_placeholder')}
                  aria-label={t('annotation_note_inline_aria')}
                  value={annNote}
                  onChange={(e) => setAnnNote(e.target.value)}
                />
                <AnimatedButton
                  variant="primary"
                  size="sm"
                  onClick={addAnnotation}
                  disabled={!annotateModal?.token || !annObj}
                >
                  <Plus className="h-3 w-3" aria-hidden="true" />
                  {t('annotation_save_inline')}
                </AnimatedButton>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <span className="w-12 flex-shrink-0 text-[11px] text-ink-ghost">{t('annotation_obj_label_inline')}</span>
                <div className="flex flex-wrap gap-1">
                  {objects.map(o => {
                    const selected = annObj.split(',').filter(Boolean).includes(o.name)
                    return (
                      <motion.button
                        key={o.id}
                        onClick={() => {
                          const cur = annObj.split(',').filter(Boolean)
                          const next = selected ? cur.filter(x => x !== o.name) : [...cur, o.name]
                          setAnnObj(next.join(','))
                          setAnnMetric('')
                        }}
                        whileTap={reduce ? undefined : { scale: 0.97 }}
                        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                        aria-pressed={selected}
                        className={`rounded-md border px-1.5 py-0.5 text-[11px] font-mono outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                          selected ? 'border-ink bg-ink text-white' : 'border-border text-ink-muted'
                        }`}
                      >
                        {o.name}
                      </motion.button>
                    )
                  })}
                </div>
                {annObj && (
                  <>
                    <span className="ml-2 text-[11px] text-ink-ghost">{t('annotation_metric_label_inline')}</span>
                    <select
                      className="h-7 w-40 rounded-md border border-border bg-white px-2 text-[11px] text-ink outline-none focus:border-ink"
                      value={annMetric}
                      onChange={(e) => setAnnMetric(e.target.value)}
                      aria-label={t('annotation_metric_label_inline')}
                    >
                      <option value="">{t('annotation_metric_unspecified')}</option>
                      {objects.filter(o => annObj.split(',').includes(o.name)).flatMap(o =>
                        (o.metrics || []).map(m => (
                          <option key={`${o.name}.${m.name}`} value={`${o.name}.${m.name}`}>{o.name}.{m.name}</option>
                        ))
                      )}
                    </select>
                  </>
                )}
              </div>
            </div>

            {/* Annotations list */}
            {filteredAnnotations.length === 0 ? (
              <div className="flex h-32 flex-col items-center justify-center gap-1 rounded-md border border-border bg-white text-center">
                <span className="text-sm text-ink-muted">{search ? t('annotation_empty_title_search') : t('annotation_empty_title_default')}</span>
                <span className="text-xs text-ink-ghost">{t('annotation_empty_hint')}</span>
              </div>
            ) : (
              <div ref={annListRef} className="overflow-hidden rounded-md border border-border bg-white">
                <div className="divide-y divide-border-light">
                  {filteredAnnotations.map(ann => (
                    <div key={ann.id} className="flex flex-wrap items-center gap-2 px-4 py-2.5">
                      <span className={`w-36 truncate font-semibold ${
                        industrial ? 'font-mono text-[12px] tracking-[0.04em] text-ink' : 'text-sm text-ink'
                      }`} title={ann.token}>
                        {industrial ? `[${ann.token}]` : `「${ann.token}」`}
                      </span>
                      <span aria-hidden="true" className="text-[11px] text-ink-ghost">→</span>
                      <div className="flex items-center gap-1">
                        {ann.objectName.split(',').filter(Boolean).map((o, i) => (
                          <span key={i} className={`bg-ink px-1.5 py-0.5 font-mono text-white ${
                            industrial ? 'text-[10px] tracking-[0.04em]' : 'rounded-md text-[11px]'
                          }`}>{o}</span>
                        ))}
                      </div>
                      {ann.propertyName && (
                        <>
                          <span aria-hidden="true" className="text-[11px] text-ink-ghost">.</span>
                          <span className={`border bg-canvas-alt px-1.5 py-0.5 font-mono text-ink-muted ${
                            industrial ? 'border-ink/40 text-[10px] tracking-[0.04em]' : 'rounded border-border text-[11px]'
                          }`}>
                            {ann.propertyName}
                          </span>
                        </>
                      )}
                      {ann.metricName && (
                        <>
                          <span aria-hidden="true" className="text-[11px] text-ink-ghost">→</span>
                          <span className={`border border-ink text-ink bg-white px-1.5 py-0.5 font-mono ${
                            industrial ? 'text-[10px] tracking-[0.04em]' : 'rounded text-[11px]'
                          }`}>
                            {ann.metricName}
                          </span>
                        </>
                      )}
                      {ann.note && <span className="text-[11px] text-ink-ghost">— {ann.note}</span>}
                      <span className="flex-1" />
                      <motion.button
                        onClick={() => deleteAnnotation(ann.id)}
                        whileHover={reduce ? undefined : { scale: 1.15 }}
                        whileTap={reduce ? undefined : { scale: 0.9 }}
                        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                        className="text-ink-ghost outline-none hover:text-danger focus-visible:ring-1 focus-visible:ring-ink"
                        aria-label={t('annotation_delete_aria')}
                        title={t('annotation_delete_title')}
                      >
                        <Trash2 className="h-3 w-3" aria-hidden="true" />
                      </motion.button>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>
      )}

      {/* Annotate modal */}
      <AnimatePresence>
        {annotateModal && tab === 'logs' && (
          <motion.div
            initial={reduce ? undefined : { opacity: 0 }}
            animate={reduce ? undefined : { opacity: 1 }}
            exit={reduce ? undefined : { opacity: 0 }}
            transition={{ duration: 0.15 }}
            className="fixed inset-0 z-50 flex items-center justify-center bg-ink/20"
            role="dialog"
            aria-modal="true"
            aria-label={t('annotation_modal_aria')}
          >
            <div className="absolute inset-0" onClick={() => setAnnotateModal(null)} />
            <motion.div
              initial={reduce ? undefined : { scale: 0.98, opacity: 0 }}
              animate={reduce ? undefined : { scale: 1, opacity: 1 }}
              exit={reduce ? undefined : { scale: 0.98, opacity: 0 }}
              transition={{ duration: 0.15, ease: 'easeOut' }}
              className="relative w-[440px] space-y-3 rounded-md border border-border bg-white p-5 shadow-lg"
              onClick={e => e.stopPropagation()}
            >
              <div className="flex items-center justify-between">
                <span className="text-sm font-semibold tracking-tight text-ink">{t('annotation_modal_title')}</span>
                <motion.button
                  onClick={() => setAnnotateModal(null)}
                  whileHover={reduce ? undefined : { scale: 1.15 }}
                  whileTap={reduce ? undefined : { scale: 0.9 }}
                  transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                  className="rounded p-1 text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                  aria-label={t('annotation_modal_close_aria')}
                  title={t('annotation_modal_close_title')}
                >
                  <X className="h-4 w-4" aria-hidden="true" />
                </motion.button>
              </div>
              <div className="text-xs leading-relaxed text-ink-muted">
                {t('annotation_modal_desc', { token: annotateModal.token })}
              </div>
              <div className="space-y-2.5">
                <div>
                  <label className="mb-1 block text-xs font-medium text-ink-muted">{t('annotation_obj_label')}</label>
                  <div className="flex flex-wrap gap-1">
                    {objects.map(o => {
                      const selected = annObj.split(',').filter(Boolean).includes(o.name)
                      return (
                        <motion.button
                          key={o.id}
                          onClick={() => {
                            const cur = annObj.split(',').filter(Boolean)
                            const next = selected ? cur.filter(x => x !== o.name) : [...cur, o.name]
                            setAnnObj(next.join(','))
                            setAnnProp(''); setAnnMetric('')
                          }}
                          whileTap={reduce ? undefined : { scale: 0.97 }}
                          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                          aria-pressed={selected}
                          className={`rounded-md border px-2 py-1 text-[11px] outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                            selected ? 'border-ink bg-ink text-white' : 'border-border text-ink-muted'
                          }`}
                        >
                          <span className="font-mono">{o.name}</span>
                          <span className="ml-1 text-[11px] opacity-60">({o.kind})</span>
                        </motion.button>
                      )
                    })}
                  </div>
                </div>
                {annObj && (
                  <div role="tablist" className="flex overflow-hidden rounded-md border border-border text-[11px]">
                    {(['property', 'metric'] as const).map(m => {
                      const active = annMode === m
                      const label = m === 'property' ? t('annotation_tab_property') : t('annotation_tab_metric')
                      return (
                        <motion.button
                          key={m}
                          role="tab"
                          aria-selected={active}
                          onClick={() => {
                            setAnnMode(m)
                            if (m === 'property') setAnnMetric('')
                            else setAnnProp('')
                          }}
                          whileTap={reduce ? undefined : { scale: 0.97 }}
                          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                          className={`flex-1 border-r border-border px-3 py-1.5 outline-none last:border-r-0 cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                            active ? 'bg-ink text-white font-medium' : 'text-ink-muted'
                          }`}
                        >
                          {label}
                        </motion.button>
                      )
                    })}
                  </div>
                )}
                {annObj && annMode === 'property' && (
                  <div>
                    <label className="mb-1 block text-xs font-medium text-ink-muted">{t('annotation_prop_label')}</label>
                    <select
                      className="h-8 w-full rounded-md border border-border bg-white px-2.5 text-sm text-ink outline-none focus:border-ink"
                      value={annProp}
                      onChange={(e) => setAnnProp(e.target.value)}
                      aria-label={t('annotation_prop_label')}
                    >
                      <option value="">{t('annotation_prop_unspecified')}</option>
                      {objects.filter(o => annObj.split(',').includes(o.name)).flatMap(o =>
                        (o.properties || []).map(p => (
                          <option key={`${o.name}.${p.name}`} value={p.name}>{o.name}.{p.name} [{p.dataType}]</option>
                        ))
                      )}
                    </select>
                  </div>
                )}
                {annObj && annMode === 'metric' && (
                  <div>
                    <label className="mb-1 block text-xs font-medium text-ink-muted">{t('annotation_metric_label')}</label>
                    <select
                      className="h-8 w-full rounded-md border border-border bg-white px-2.5 text-sm text-ink outline-none focus:border-ink"
                      value={annMetric}
                      onChange={(e) => setAnnMetric(e.target.value)}
                      aria-label={t('annotation_metric_label')}
                    >
                      <option value="">{t('annotation_metric_placeholder')}</option>
                      {objects.filter(o => annObj.split(',').includes(o.name)).flatMap(o =>
                        (o.metrics || []).map(m => (
                          <option key={`${o.name}.${m.name}`} value={`${o.name}.${m.name}`}>{o.name}.{m.name}</option>
                        ))
                      )}
                    </select>
                  </div>
                )}
                <div>
                  <label className="mb-1 block text-xs font-medium text-ink-muted">{t('annotation_note_label')}</label>
                  <input
                    className="h-8 w-full rounded-md border border-border px-2.5 text-sm text-ink outline-none focus:border-ink"
                    placeholder={t('annotation_note_placeholder')}
                    value={annNote}
                    onChange={(e) => setAnnNote(e.target.value)}
                    aria-label={t('annotation_note_label')}
                  />
                </div>
              </div>
              <div className="flex justify-end gap-2 pt-1">
                <AnimatedButton variant="secondary" size="sm" onClick={() => setAnnotateModal(null)}>
                  {t('annotation_cancel')}
                </AnimatedButton>
                <AnimatedButton variant="primary" size="sm" onClick={addAnnotation} disabled={!annObj}>
                  {t('annotation_save')}
                </AnimatedButton>
              </div>
            </motion.div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}
