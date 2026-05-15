'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback, useMemo } from 'react'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { SQLEditor } from '@/components/ui/SQLEditor'
import {
  Play, Save, AlertCircle, Database, History, Star,
  ChevronDown, ChevronRight, Trash2, Download,
  X, FileCode, Table2,
  ChevronLeft, ChevronsLeft, ChevronsRight,
  Lock, Timer, ShieldCheck,
} from 'lucide-react'

// ─── Types ──────────────────────────────────────────────────────

interface LakehouseColumn {
  name: string
  type: string
}
interface LakehouseTable {
  name: string
  columns: LakehouseColumn[]
  rowCount: number
}

interface HistoryEntry {
  id: string
  sql: string
  rowCount: number
  durationMs: number
  error: string
  createdAt: string
}

interface Snippet {
  id: string
  name: string
  sql: string
  description: string
  createdAt: string
}

// Server-driven pagination: a single fetch returns ONE page of rows + the
// total row count, so we can render prev/next/jump without buffering the full
// result set. (Differs from sql-passthrough, which fetches up to N rows in one
// shot and paginates client-side.)
interface QueryResult {
  columns?: { name: string; type: string }[]
  rows?: unknown[][]
  rowCount: number
  totalCount: number
  page: number
  pageSize: number
  durationMs: number
  error?: string
  blocked?: boolean
}

const PAGE_SIZES = [25, 50, 100, 200, 500] as const
type PageSize = (typeof PAGE_SIZES)[number]
const DEFAULT_PAGE_SIZE: PageSize = 50

// ─── Helpers ────────────────────────────────────────────────────

function isNumericType(t: string): boolean {
  const u = (t || '').toLowerCase()
  return /^(int|integer|bigint|smallint|numeric|decimal|real|double|float|serial|number)/.test(u)
}

function csvEscape(v: unknown): string {
  if (v === null || v === undefined) return ''
  const s = typeof v === 'string' ? v : JSON.stringify(v)
  if (s.includes(',') || s.includes('"') || s.includes('\n')) {
    return '"' + s.replace(/"/g, '""') + '"'
  }
  return s
}

function formatCell(v: unknown): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

function formatRowCount(n: number): string {
  if (n <= 0) return '—'
  if (n < 1000) return `${n}`
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`
  return `${(n / 1_000_000).toFixed(1)}M`
}

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

function SecurityPill({ icon: Icon, label, tooltip }: {
  icon: React.ComponentType<{ size?: number; className?: string; 'aria-hidden'?: boolean }>
  label: string
  tooltip: string
}) {
  const industrial = useStyleMode().mode === 'industrial'
  return (
    <span
      title={tooltip}
      className={`inline-flex items-center gap-1 bg-white px-2 py-0.5 text-ink-muted ${
        industrial
          ? 'border border-ink font-mono text-[10px] uppercase tracking-[0.12em]'
          : 'rounded-md border border-border text-[11px]'
      }`}
    >
      <Icon size={11} className="text-ink-ghost" aria-hidden={true} />
      {label}
    </span>
  )
}

// ─── Page ───────────────────────────────────────────────────────

export default function LakehouseSQLPage() {
  const t = useTranslations('sql')
  const industrial = useStyleMode().mode === 'industrial'
  const msg = useMessage()
  const { currentProject } = useProject()
  const reduce = useReducedMotion()

  // Editor / execution state
  const [sql, setSql] = useState<string>('')
  const [result, setResult] = useState<QueryResult | null>(null)
  const [running, setRunning] = useState(false)

  // Server-side pagination is keyed off the SQL that produced the current result
  // so changing pages re-runs the SAME query (not the editor's current text,
  // which the user may have edited in the meantime).
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState<PageSize>(DEFAULT_PAGE_SIZE)
  const [lastExecutedSQL, setLastExecutedSQL] = useState<string>('')

  // Sidebar
  const [tab, setTab] = useState<'schema' | 'snippets' | 'history'>('schema')
  const [schema, setSchema] = useState<LakehouseTable[]>([])
  const [snippets, setSnippets] = useState<Snippet[]>([])
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [schemaSearch, setSchemaSearch] = useState('')
  const [expandedTables, setExpandedTables] = useState<Set<string>>(new Set())

  // Save dialog
  const [saveOpen, setSaveOpen] = useState(false)
  const [saveName, setSaveName] = useState('')
  const [saveDesc, setSaveDesc] = useState('')

  // Fetchers
  const fetchSchema = useCallback(async () => {
    if (!currentProject) return
    try {
      const res = await api<{ data: LakehouseTable[] }>(`/lakehouse-sql/schema?projectId=${currentProject.id}`)
      setSchema(res.data || [])
    } catch { setSchema([]) }
  }, [currentProject])

  const fetchSnippets = useCallback(async () => {
    if (!currentProject) return
    try {
      const res = await api<{ data: Snippet[] }>(`/lakehouse-sql/snippets?projectId=${currentProject.id}`)
      setSnippets(res.data || [])
    } catch { setSnippets([]) }
  }, [currentProject])

  const fetchHistory = useCallback(async () => {
    if (!currentProject) return
    try {
      const res = await api<{ data: HistoryEntry[] }>(`/lakehouse-sql/history?projectId=${currentProject.id}`)
      setHistory(res.data || [])
    } catch { setHistory([]) }
  }, [currentProject])

  useEffect(() => { fetchSchema() }, [fetchSchema])
  useEffect(() => { fetchSnippets(); fetchHistory() }, [fetchSnippets, fetchHistory])

  // Seed query exactly once (after first schema load) so the editor isn't blank
  // for a fresh user. didInit is held in a ref-shaped useMemo so React's strict
  // mode double-effect doesn't seed twice.
  const didInitSql = useMemo(() => ({ v: false }), [])
  useEffect(() => {
    if (didInitSql.v || schema.length === 0) return
    didInitSql.v = true
    if (sql) return
    const first = schema[0]
    setSql(`${t('lakehouse.seed_comment')}\n\nSELECT * FROM "${first.name}";`)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [schema])

  // Editor autocomplete source
  const editorSchema = useMemo(() => {
    const out: Record<string, string[]> = {}
    for (const t of schema) out[t.name] = t.columns.map(c => c.name)
    return out
  }, [schema])

  const filteredSchema = useMemo(() => {
    if (!schemaSearch) return schema
    const q = schemaSearch.toLowerCase()
    return schema.filter(t =>
      t.name.toLowerCase().includes(q) ||
      t.columns.some(c => c.name.toLowerCase().includes(q))
    )
  }, [schema, schemaSearch])

  // Core: run a SQL string against the lakehouse with given page/pageSize.
  // Used by both runQuery (new run, page 1) and goToPage (paginate prior run).
  const executeSQL = useCallback(async (execSQL: string, p: number, ps: number) => {
    if (!currentProject || !execSQL.trim()) return
    setRunning(true)
    try {
      const res = await api<QueryResult>('/lakehouse-sql/execute', {
        method: 'POST',
        body: { sql: execSQL, projectId: currentProject.id, page: p, pageSize: ps },
      })
      setResult(res)
      if (!res.error) {
        setLastExecutedSQL(execSQL)
        fetchHistory()
      }
    } catch (e) {
      setResult({
        rowCount: 0, totalCount: 0, page: p, pageSize: ps, durationMs: 0,
        error: e instanceof Error ? e.message : 'Request failed',
      })
    } finally { setRunning(false) }
  }, [currentProject, fetchHistory])

  // New run: reset to page 1, prefer a selected fragment over the full editor
  // text (matches DataGrip / DBeaver convention — selection wins).
  const runQuery = useCallback(() => {
    if (running) return
    let execSQL = sql
    const sel = typeof window !== 'undefined' ? window.getSelection()?.toString().trim() : ''
    if (sel) execSQL = sel
    setPage(1)
    executeSQL(execSQL, 1, pageSize)
  }, [sql, running, pageSize, executeSQL])

  // Page navigation re-runs lastExecutedSQL — NOT the editor text — so a user
  // who has edited the SQL after running can still paginate the prior result.
  const goToPage = useCallback((newPage: number, newSize?: PageSize) => {
    if (!lastExecutedSQL || running) return
    const ps: PageSize = newSize ?? pageSize
    setPage(newPage)
    if (newSize) setPageSize(newSize)
    executeSQL(lastExecutedSQL, newPage, ps)
  }, [lastExecutedSQL, running, pageSize, executeSQL])

  // ⌘+Enter / Ctrl+Enter shortcut
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault()
        runQuery()
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [runQuery])

  // Snippet ops
  const saveSnippet = async () => {
    if (!currentProject || !saveName.trim() || !sql.trim()) return
    try {
      await api('/lakehouse-sql/snippets', {
        method: 'POST',
        body: { projectId: currentProject.id, name: saveName.trim(), sql, description: saveDesc.trim() },
      })
      setSaveOpen(false); setSaveName(''); setSaveDesc('')
      msg.success(t('lakehouse.saved')); fetchSnippets()
    } catch (e) { msg.error(e instanceof Error ? e.message : t('lakehouse.save_failed')) }
  }
  const deleteSnippet = async (id: string) => {
    if (!confirm(t('lakehouse.confirm_delete_snippet'))) return
    try { await api(`/lakehouse-sql/snippets/${id}`, { method: 'DELETE' }); fetchSnippets() }
    catch { msg.error(t('lakehouse.delete_failed')) }
  }
  const deleteHistoryEntry = async (id: string) => {
    try { await api(`/lakehouse-sql/history?id=${id}`, { method: 'DELETE' }); fetchHistory() }
    catch { msg.error(t('lakehouse.delete_failed')) }
  }

  // Insert helper — appends with a sensible separator to whatever's in the editor
  const insertAtEditor = (text: string) => {
    setSql(prev => prev + (prev && !prev.endsWith(' ') && !prev.endsWith('\n') ? ' ' : '') + text)
  }

  // Export current page only (server-paginated; full export would need a separate API)
  const exportCSV = () => {
    if (!result?.columns || !result.rows) return
    const header = result.columns.map(c => csvEscape(c.name)).join(',')
    const body = result.rows.map(row => row.map(csvEscape).join(',')).join('\n')
    const blob = new Blob([header + '\n' + body], { type: 'text/csv;charset=utf-8;' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a'); a.href = url
    a.download = `lakehouse_page${result.page}_${Date.now()}.csv`; a.click()
    URL.revokeObjectURL(url)
  }
  const exportJSON = () => {
    if (!result?.columns || !result.rows) return
    const data = result.rows.map(row => {
      const obj: Record<string, unknown> = {}
      result.columns!.forEach((c, i) => { obj[c.name] = row[i] })
      return obj
    })
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a'); a.href = url
    a.download = `lakehouse_page${result.page}_${Date.now()}.json`; a.click()
    URL.revokeObjectURL(url)
  }

  const totalPages = result ? Math.max(1, Math.ceil(result.totalCount / result.pageSize)) : 1
  const totalCols = useMemo(() => schema.reduce((a, t) => a + t.columns.length, 0), [schema])
  const totalRows = useMemo(() => schema.reduce((a, t) => a + (t.rowCount > 0 ? t.rowCount : 0), 0), [schema])

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* ── Header ─────────────────────────────────────────────────── */}
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
                // LAKEHOUSE SQL
              </span>
              <span className="font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-muted">
                {schema.length} TABLES · {totalCols} COLS · {formatRowCount(totalRows)} ROWS
              </span>
            </>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <Database size={14} className="text-ink" aria-hidden="true" />
              </div>
              <div className="min-w-0">
                <h1 className="text-base font-semibold tracking-tight text-ink">{t('lakehouse.title')}</h1>
                <p className="truncate text-xs text-ink-muted">
                  {t('lakehouse.subtitle')}
                </p>
              </div>
            </>
          )}
        </div>
        <div className="flex flex-shrink-0 items-center gap-4">
          {!industrial && (
            <div className="hidden items-baseline gap-x-3 text-xs text-ink-muted lg:flex">
              <span><span className="font-semibold tabular-nums text-ink">{schema.length}</span> {t('lakehouse.stat_tables')}</span>
              <span aria-hidden="true" className="text-ink-ghost">·</span>
              <span><span className="font-semibold tabular-nums text-ink">{totalCols}</span> {t('lakehouse.stat_cols')}</span>
              <span aria-hidden="true" className="text-ink-ghost">·</span>
              <span><span className="font-semibold tabular-nums text-ink">{formatRowCount(totalRows)}</span> {t('lakehouse.stat_rows')}</span>
            </div>
          )}
          <div className="flex items-center gap-2">
            <AnimatedButton
              variant="secondary"
              size="sm"
              disabled={!sql.trim()}
              onClick={() => setSaveOpen(true)}
            >
              <Save size={12} aria-hidden="true" />
              {t('lakehouse.save')}
            </AnimatedButton>
            <AnimatedButton
              variant="primary"
              size="sm"
              disabled={running || !sql.trim()}
              onClick={runQuery}
            >
              <Play size={12} aria-hidden="true" />
              {running ? t('lakehouse.running') : t('lakehouse.run')}
              <span className="ml-1 text-[11px] opacity-70">⌘↵</span>
            </AnimatedButton>
          </div>
        </div>
      </motion.header>

      {/* ── Toolbar: security pills + page-size selector ───────────── */}
      <div className="flex flex-shrink-0 flex-wrap items-center justify-between gap-3 border-b border-border bg-white px-6 py-2.5">
        <div className="flex flex-wrap items-center gap-1.5">
          <SecurityPill
            icon={Lock}
            label={t('lakehouse.pill_readonly')}
            tooltip={t('lakehouse.pill_readonly_tip')}
          />
          <SecurityPill
            icon={Timer}
            label={t('lakehouse.pill_timeout')}
            tooltip={t('lakehouse.pill_timeout_tip')}
          />
          <SecurityPill
            icon={ShieldCheck}
            label={t('lakehouse.pill_scope')}
            tooltip={t('lakehouse.pill_scope_tip')}
          />
        </div>
        <div className="flex items-center gap-2">
          <span className="text-[11px] text-ink-ghost">{t('lakehouse.per_page')}</span>
          <div role="radiogroup" aria-label={t('lakehouse.per_page_aria')} className="flex overflow-hidden rounded-md border border-border">
            {PAGE_SIZES.map(n => {
              const active = pageSize === n
              return (
                <motion.button
                  key={n}
                  role="radio"
                  aria-checked={active}
                  // Page size change DURING a result set: re-fetch page 1 with new size
                  onClick={() => {
                    if (lastExecutedSQL) goToPage(1, n)
                    else setPageSize(n)
                  }}
                  disabled={running}
                  whileTap={reduce ? undefined : { scale: 0.97 }}
                  transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                  className={`border-r border-border px-2.5 py-1 text-[11px] tabular-nums outline-none last:border-r-0 cursor-pointer focus-visible:ring-1 focus-visible:ring-ink disabled:opacity-50 disabled:cursor-not-allowed ${
                    active ? 'bg-ink text-white font-medium' : 'text-ink-muted'
                  }`}
                >
                  {n}
                </motion.button>
              )
            })}
          </div>
        </div>
      </div>

      {/* ── Main area ──────────────────────────────────────────────── */}
      <div className="flex flex-1 min-h-0 overflow-hidden">
        {/* ── Left sidebar (320px) ─────────────────────────────── */}
        <aside className="flex w-80 flex-shrink-0 flex-col min-h-0 border-r border-border bg-white">
          <div role="tablist" aria-label={t('lakehouse.sidebar_aria')} className="flex flex-shrink-0 border-b border-border">
            {([
              ['schema', t('lakehouse.tab_schema'), Table2],
              ['snippets', t('lakehouse.tab_snippets'), Star],
              ['history', t('lakehouse.tab_history'), History],
            ] as const).map(([key, label, Icon]) => {
              const active = tab === key
              return (
                <motion.button
                  key={key}
                  role="tab"
                  aria-selected={active}
                  onClick={() => setTab(key)}
                  whileTap={reduce ? undefined : { scale: 0.97 }}
                  transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                  className={`flex flex-1 items-center justify-center gap-1.5 border-b-2 py-2.5 text-xs outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                    active
                      ? 'border-ink bg-canvas-alt font-semibold text-ink'
                      : 'border-transparent text-ink-muted hover:text-ink'
                  }`}
                >
                  <Icon className="h-3 w-3" aria-hidden="true" />
                  {label}
                </motion.button>
              )
            })}
          </div>

          <div className="flex-1 min-h-0 overflow-auto">
            {/* ── Schema tab ───────────────────────────────── */}
            {tab === 'schema' && (
              <div>
                <div className="sticky top-0 z-10 space-y-2 border-b border-border bg-white p-2">
                  <input
                    value={schemaSearch}
                    onChange={e => setSchemaSearch(e.target.value)}
                    placeholder={t('lakehouse.schema_filter_placeholder')}
                    aria-label={t('lakehouse.schema_filter_aria')}
                    className="w-full rounded-md border border-border bg-white px-2 py-1 text-[11px] text-ink outline-none placeholder:text-ink-ghost focus:border-ink"
                  />
                  <div className="flex items-center justify-between text-[11px] text-ink-ghost">
                    <span>
                      <span className="tabular-nums">{filteredSchema.length}</span> / <span className="tabular-nums">{schema.length}</span> {t('lakehouse.stat_tables')}
                    </span>
                    <span>{t('lakehouse.click_insert')}</span>
                  </div>
                </div>
                {schema.length === 0 && (
                  <div className="flex flex-col items-center justify-center gap-1 p-4 text-center">
                    <span className="text-xs text-ink-muted">{t('lakehouse.schema_empty')}</span>
                    <span className="text-[11px] text-ink-ghost">{t('lakehouse.schema_empty_hint')}</span>
                  </div>
                )}
                {filteredSchema.map(tbl => {
                  const expanded = expandedTables.has(tbl.name)
                  return (
                    <div key={tbl.name} className="border-b border-border-light">
                      <div className="flex items-center">
                        <button
                          onClick={() => {
                            const next = new Set(expandedTables)
                            if (expanded) next.delete(tbl.name); else next.add(tbl.name)
                            setExpandedTables(next)
                          }}
                          className="flex h-7 w-6 flex-shrink-0 items-center justify-center text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                          aria-label={expanded ? t('lakehouse.collapse') : t('lakehouse.expand')}
                          aria-expanded={expanded}
                        >
                          {expanded
                            ? <ChevronDown className="h-3 w-3" aria-hidden="true" />
                            : <ChevronRight className="h-3 w-3" aria-hidden="true" />}
                        </button>
                        <button
                          onClick={() => insertAtEditor(`"${tbl.name}"`)}
                          title={`${t('lakehouse.insert_table')} ${tbl.name}`}
                          className="group flex flex-1 items-center gap-2 px-1 py-1.5 text-left outline-none focus-visible:ring-1 focus-visible:ring-ink"
                        >
                          <Table2 className="h-3 w-3 flex-shrink-0 text-ink" aria-hidden="true" />
                          <span className="truncate font-mono text-xs font-semibold text-ink">{tbl.name}</span>
                          <span className="ml-auto text-[11px] tabular-nums text-ink-ghost">
                            {tbl.columns.length}c · {formatRowCount(tbl.rowCount)}r
                          </span>
                        </button>
                      </div>
                      {expanded && (
                        <div className="bg-canvas-alt/60 pb-1">
                          {tbl.columns.map(c => (
                            <button
                              key={c.name}
                              onClick={() => insertAtEditor(`"${c.name}"`)}
                              className="group flex w-full items-center justify-between px-8 py-0.5 text-left text-[11px] outline-none focus-visible:ring-1 focus-visible:ring-ink"
                              title={t('lakehouse.insert_col')}
                            >
                              <span className="font-mono text-ink">{c.name}</span>
                              <span className="font-mono text-[11px] text-ink-ghost">{c.type}</span>
                            </button>
                          ))}
                          {tbl.columns.length === 0 && (
                            <div className="px-8 py-1 text-[11px] italic text-ink-ghost">{t('lakehouse.no_cols_meta')}</div>
                          )}
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>
            )}

            {/* ── Snippets tab ─────────────────────────────── */}
            {tab === 'snippets' && (
              <div>
                {snippets.length === 0 && (
                  <div className="flex flex-col items-center justify-center gap-1 p-4 text-center">
                    <span className="text-xs text-ink-muted">{t('lakehouse.no_snippets')}</span>
                    <span className="text-[11px] text-ink-ghost">{t('lakehouse.no_snippets_hint')}</span>
                  </div>
                )}
                {snippets.map(s => (
                  <div key={s.id} className="group border-b border-border-light p-2">
                    <div className="flex items-start justify-between gap-2">
                      <button
                        onClick={() => setSql(s.sql)}
                        className="flex-1 text-left outline-none focus-visible:ring-1 focus-visible:ring-ink"
                      >
                        <div className="text-xs font-semibold text-ink">{s.name}</div>
                        {s.description && <div className="mt-0.5 text-[11px] text-ink-muted">{s.description}</div>}
                        <div className="mt-1 line-clamp-2 font-mono text-[11px] text-ink-ghost">{s.sql}</div>
                      </button>
                      <motion.button
                        onClick={() => deleteSnippet(s.id)}
                        whileHover={reduce ? undefined : { scale: 1.15 }}
                        whileTap={reduce ? undefined : { scale: 0.9 }}
                        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                        aria-label={t('lakehouse.delete_snippet')}
                        className="text-ink-ghost opacity-0 transition-opacity outline-none group-hover:opacity-100 hover:text-danger focus-visible:opacity-100 focus-visible:ring-1 focus-visible:ring-ink"
                      >
                        <Trash2 className="h-3 w-3" aria-hidden="true" />
                      </motion.button>
                    </div>
                  </div>
                ))}
              </div>
            )}

            {/* ── History tab ──────────────────────────────── */}
            {tab === 'history' && (
              <div>
                {history.length === 0 && (
                  <div className="flex flex-col items-center justify-center gap-1 p-4 text-center">
                    <span className="text-xs text-ink-muted">{t('lakehouse.no_history')}</span>
                    <span className="text-[11px] text-ink-ghost">{t('lakehouse.no_history_hint')}</span>
                  </div>
                )}
                {history.map(h => (
                  <div key={h.id} className="group border-b border-border-light p-2">
                    <div className="flex items-start justify-between gap-2">
                      <button
                        onClick={() => setSql(h.sql)}
                        className="flex-1 text-left outline-none focus-visible:ring-1 focus-visible:ring-ink"
                      >
                        <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-ink-ghost">
                          <span className="tabular-nums">{new Date(h.createdAt).toLocaleString('zh-CN')}</span>
                          <span aria-hidden="true">·</span>
                          <span className={h.error ? 'text-danger' : 'text-ink'}>
                            {h.error ? t('lakehouse.hist_error') : <><span className="tabular-nums">{h.rowCount}</span> {t('lakehouse.hist_rows')}</>}
                          </span>
                          <span aria-hidden="true">·</span>
                          <span className="tabular-nums">{h.durationMs}ms</span>
                        </div>
                        <div className="mt-1 line-clamp-2 font-mono text-[11px] text-ink">{h.sql}</div>
                      </button>
                      <motion.button
                        onClick={() => deleteHistoryEntry(h.id)}
                        whileHover={reduce ? undefined : { scale: 1.15 }}
                        whileTap={reduce ? undefined : { scale: 0.9 }}
                        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                        aria-label={t('lakehouse.delete_history')}
                        className="text-ink-ghost opacity-0 transition-opacity outline-none group-hover:opacity-100 hover:text-danger focus-visible:opacity-100 focus-visible:ring-1 focus-visible:ring-ink"
                      >
                        <Trash2 className="h-3 w-3" aria-hidden="true" />
                      </motion.button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </aside>

        {/* ── Center: editor + results (vertical split) ────────── */}
        <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
          {/* Editor frame */}
          <div className="flex flex-col min-h-0 border-b border-border bg-white" style={{ flexBasis: '40%' }}>
            <div className="flex flex-shrink-0 items-center justify-between border-b border-border-light bg-canvas-alt px-3 py-1.5">
              <div className="flex items-center gap-1.5 text-xs font-medium text-ink">
                <FileCode className="h-3 w-3 text-ink-muted" aria-hidden="true" />
                query.sql
              </div>
              <div className="text-[11px] text-ink-ghost">
                <span className="tabular-nums">{sql.length}</span> {t('lakehouse.editor_hint')}
              </div>
            </div>
            <div className="flex-1 min-h-0 overflow-auto">
              <SQLEditor value={sql} onChange={setSql} schema={editorSchema} height="100%" />
            </div>
          </div>

          {/* Results frame */}
          <div className="flex flex-1 min-h-0 flex-col overflow-hidden bg-white">
            <div className="flex flex-shrink-0 flex-wrap items-center justify-between gap-2 border-b border-border-light bg-canvas-alt px-3 py-1.5">
              <div className="flex items-center gap-1.5 text-xs font-medium text-ink">
                <Database className="h-3 w-3 text-ink-muted" aria-hidden="true" />
                {t('lakehouse.results')}
              </div>
              <div className="flex items-center gap-2 text-[11px]">
                {result && !result.error && (
                  <>
                    <span className="text-ink-muted">
                      {result.totalCount > 0 ? (
                        <>
                          <span className="font-semibold tabular-nums text-ink">
                            {((result.page - 1) * result.pageSize + 1).toLocaleString()}
                          </span>
                          –
                          <span className="font-semibold tabular-nums text-ink">
                            {Math.min(result.page * result.pageSize, result.totalCount).toLocaleString()}
                          </span>
                          {' / '}
                          <span className="font-semibold tabular-nums text-ink">
                            {result.totalCount.toLocaleString()}
                          </span>
                        </>
                      ) : t('lakehouse.zero_rows')}
                    </span>
                    <span aria-hidden="true" className="text-ink-ghost">·</span>
                    <span className="text-ink-muted">
                      <span className="tabular-nums text-ink">{result.durationMs}</span>ms
                    </span>
                    <motion.button
                      onClick={exportCSV}
                      whileTap={reduce ? undefined : { scale: 0.97 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      title={t('lakehouse.export_csv')}
                      className="inline-flex items-center gap-1 rounded border border-border bg-white px-1.5 py-0.5 text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      <Download className="h-2.5 w-2.5" aria-hidden="true" /> CSV
                    </motion.button>
                    <motion.button
                      onClick={exportJSON}
                      whileTap={reduce ? undefined : { scale: 0.97 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      title={t('lakehouse.export_json')}
                      className="inline-flex items-center gap-1 rounded border border-border bg-white px-1.5 py-0.5 text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      <Download className="h-2.5 w-2.5" aria-hidden="true" /> JSON
                    </motion.button>
                  </>
                )}
                {result?.error && (
                  <span className="inline-flex items-center gap-1 font-medium text-danger">
                    <AlertCircle size={11} aria-hidden="true" />
                    {result.blocked ? t('lakehouse.blocked') : t('lakehouse.exec_error')}
                  </span>
                )}
              </div>
            </div>

            <div className="flex-1 min-h-0 overflow-auto">
              {/* Idle */}
              {!result && !running && (
                <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
                  <div className="text-sm text-ink-muted">
                    {t('lakehouse.idle_hint')}
                  </div>
                  <div className="text-xs text-ink-ghost">
                    {t('lakehouse.idle_sub', { pageSize })}
                  </div>
                </div>
              )}

              {/* Running */}
              {running && (
                <div className="flex h-full items-center justify-center">
                  <InlineLoader text={t('lakehouse.running_long')} />
                </div>
              )}

              {/* Error */}
              {result?.error && (
                <div className="p-4">
                  <div className="overflow-hidden rounded-md border border-danger/40 bg-danger/5">
                    <div className="flex items-center gap-2 border-b border-danger/30 bg-danger/10 px-3 py-2">
                      <AlertCircle size={14} className="flex-shrink-0 text-danger" aria-hidden="true" />
                      <span className="text-sm font-semibold text-danger">
                        {result.blocked ? t('lakehouse.blocked') : t('lakehouse.exec_error')}
                      </span>
                    </div>
                    <pre className="whitespace-pre-wrap break-words px-3 py-2 font-mono text-xs leading-relaxed text-ink">
                      {result.error}
                    </pre>
                    {schema.length > 0 && (
                      <div className="border-t border-danger/20 px-3 py-2">
                        <div className="mb-1.5 text-[11px] text-ink-muted">{t('lakehouse.avail_tables')}</div>
                        <div className="flex flex-wrap gap-1">
                          {schema.slice(0, 24).map(tbl => (
                            <motion.button
                              key={tbl.name}
                              onClick={() => insertAtEditor(`"${tbl.name}"`)}
                              whileHover={reduce ? undefined : { scale: 1.03 }}
                              whileTap={reduce ? undefined : { scale: 0.97 }}
                              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                              className="rounded-md border border-border bg-white px-1.5 py-0.5 font-mono text-[11px] text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
                            >
                              {tbl.name}
                            </motion.button>
                          ))}
                          {schema.length > 24 && (
                            <span className="px-1.5 py-0.5 text-[11px] text-ink-ghost">
                              +{schema.length - 24}
                            </span>
                          )}
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              )}

              {/* Table */}
              {result && !result.error && result.columns && (
                <ResultsTable
                  columns={result.columns}
                  rows={result.rows || []}
                  startIdx={(result.page - 1) * result.pageSize}
                />
              )}
            </div>

            {/* Pagination bar (server-side; only present when result has rows) */}
            {result && !result.error && result.totalCount > 0 && (
              <div className="flex flex-shrink-0 items-center justify-between border-t border-border-light bg-white px-3 py-1.5 text-[11px]">
                <div className="text-ink-muted">
                  {t('lakehouse.page_label', { page: result.page, total: totalPages, count: result.totalCount.toLocaleString() })}
                </div>
                <div className="flex items-center gap-1.5">
                  <PageButton
                    onClick={() => goToPage(1)}
                    disabled={running || result.page === 1}
                    title={t('lakehouse.page_first')}
                    reduce={!!reduce}
                  >
                    <ChevronsLeft className="h-3 w-3" aria-hidden="true" />
                  </PageButton>
                  <PageButton
                    onClick={() => goToPage(result.page - 1)}
                    disabled={running || result.page === 1}
                    title={t('lakehouse.page_prev')}
                    reduce={!!reduce}
                  >
                    <ChevronLeft className="h-3 w-3" aria-hidden="true" />
                  </PageButton>
                  <PageButton
                    onClick={() => goToPage(result.page + 1)}
                    disabled={running || result.page >= totalPages}
                    title={t('lakehouse.page_next')}
                    reduce={!!reduce}
                  >
                    <ChevronRight className="h-3 w-3" aria-hidden="true" />
                  </PageButton>
                  <PageButton
                    onClick={() => goToPage(totalPages)}
                    disabled={running || result.page >= totalPages}
                    title={t('lakehouse.page_last')}
                    reduce={!!reduce}
                  >
                    <ChevronsRight className="h-3 w-3" aria-hidden="true" />
                  </PageButton>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* ── Footer (compact status) ────────────────────────────────── */}
      <div className="flex flex-shrink-0 items-center justify-between border-t border-border bg-canvas-alt px-6 py-1.5 text-[11px] text-ink-muted">
        <div className="flex items-center gap-3">
          <span>{t('lakehouse.footer_project')}<span className="font-medium text-ink">{currentProject?.name || '—'}</span></span>
          <span aria-hidden="true" className="text-ink-ghost">·</span>
          <span>{t('lakehouse.footer_readonly')}</span>
        </div>
        <div className="tabular-nums">
          {running ? t('lakehouse.running') : result
            ? (result.error
              ? t('lakehouse.status_fail')
              : t('lakehouse.status_ok', { page: result.page, totalPages, rowCount: result.rowCount, totalCount: result.totalCount, durationMs: result.durationMs }))
            : t('lakehouse.status_idle')}
        </div>
      </div>

      {/* ── Save dialog ────────────────────────────────────────────── */}
      <AnimatePresence>
        {saveOpen && (
          <motion.div
            initial={reduce ? undefined : { opacity: 0 }}
            animate={reduce ? undefined : { opacity: 1 }}
            exit={reduce ? undefined : { opacity: 0 }}
            transition={{ duration: 0.15 }}
            className="fixed inset-0 z-50 flex items-center justify-center bg-ink/20"
            role="dialog"
            aria-modal="true"
            aria-label={t('lakehouse.save_dialog_aria')}
            onClick={() => setSaveOpen(false)}
          >
            <motion.div
              initial={reduce ? undefined : { scale: 0.98, opacity: 0 }}
              animate={reduce ? undefined : { scale: 1, opacity: 1 }}
              exit={reduce ? undefined : { scale: 0.98, opacity: 0 }}
              transition={{ duration: 0.15, ease: 'easeOut' }}
              className="relative w-[440px] rounded-md border border-border bg-white shadow-lg"
              onClick={e => e.stopPropagation()}
            >
              <div className="flex items-center justify-between border-b border-border-light bg-canvas-alt px-4 py-2">
                <span className="text-sm font-semibold tracking-tight text-ink">{t('lakehouse.save_dialog_title')}</span>
                <motion.button
                  onClick={() => setSaveOpen(false)}
                  whileHover={reduce ? undefined : { scale: 1.15 }}
                  whileTap={reduce ? undefined : { scale: 0.9 }}
                  transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                  aria-label={t('lakehouse.close')}
                  title={t('lakehouse.close')}
                  className="rounded p-1 text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                >
                  <X className="h-4 w-4" aria-hidden="true" />
                </motion.button>
              </div>
              <div className="space-y-3 p-4">
                <div>
                  <label className="mb-1 block text-xs font-medium text-ink-muted">{t('lakehouse.snippet_name')}</label>
                  <input
                    value={saveName}
                    onChange={e => setSaveName(e.target.value)}
                    placeholder={t('lakehouse.snippet_name_placeholder')}
                    className="h-8 w-full rounded-md border border-border bg-white px-2.5 text-sm text-ink outline-none focus:border-ink"
                    autoFocus
                    aria-label={t('lakehouse.snippet_name_aria')}
                  />
                </div>
                <div>
                  <label className="mb-1 block text-xs font-medium text-ink-muted">{t('lakehouse.snippet_desc')}</label>
                  <input
                    value={saveDesc}
                    onChange={e => setSaveDesc(e.target.value)}
                    className="h-8 w-full rounded-md border border-border bg-white px-2.5 text-sm text-ink outline-none focus:border-ink"
                    aria-label={t('lakehouse.snippet_desc_aria')}
                  />
                </div>
                <div className="flex justify-end gap-2 pt-1">
                  <AnimatedButton variant="secondary" size="sm" onClick={() => setSaveOpen(false)}>
                    {t('lakehouse.cancel')}
                  </AnimatedButton>
                  <AnimatedButton
                    variant="primary"
                    size="sm"
                    onClick={saveSnippet}
                    disabled={!saveName.trim()}
                  >
                    <Save size={12} aria-hidden="true" />
                    {t('lakehouse.save')}
                  </AnimatedButton>
                </div>
              </div>
            </motion.div>
          </motion.div>
        )}
      </AnimatePresence>

    </div>
  )
}

// ─── Page button (small icon-only nav button) ─────────────────────

function PageButton({ onClick, disabled, title, reduce, children }: {
  onClick: () => void
  disabled: boolean
  title: string
  reduce: boolean
  children: React.ReactNode
}) {
  return (
    <motion.button
      onClick={onClick}
      disabled={disabled}
      title={title}
      aria-label={title}
      whileTap={reduce || disabled ? undefined : { scale: 0.92 }}
      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
      className="inline-flex h-6 w-6 items-center justify-center rounded border border-border bg-white text-ink outline-none hover:border-ink disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink"
    >
      {children}
    </motion.button>
  )
}

// ─── Results Table ──────────────────────────────────────────────

function ResultsTable({ columns, rows, startIdx }: {
  columns: { name: string; type: string }[]
  rows: unknown[][]
  startIdx: number
}) {
  const t = useTranslations('sql')
  if (rows.length === 0) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1 text-center">
        <span className="text-sm text-ink-muted">{t('lakehouse.empty_result')}</span>
        <span className="text-xs text-ink-ghost">{t('lakehouse.empty_result_hint')}</span>
      </div>
    )
  }

  return (
    <table className="min-w-full border-collapse">
      <thead className="sticky top-0 z-10 bg-canvas-alt">
        <tr>
          <th className="sticky left-0 z-20 w-12 border-b border-border bg-canvas-alt px-2 py-1 text-right text-[11px] font-medium text-ink-ghost">
            #
          </th>
          {columns.map((c, i) => (
            <th
              key={i}
              className={`border-b border-border bg-canvas-alt px-2 py-1 text-[11px] font-medium ${
                isNumericType(c.type) ? 'text-right' : 'text-left'
              }`}
            >
              <div className="font-mono text-ink">{c.name}</div>
              <div className="font-mono text-[11px] font-normal text-ink-ghost">{c.type}</div>
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((row, ri) => {
          const absRowIdx = startIdx + ri
          return (
            <tr key={absRowIdx} className="hover:bg-canvas-alt/40">
              <td className="sticky left-0 border-b border-border-light bg-white px-2 py-1 text-right text-[11px] tabular-nums text-ink-ghost">
                {absRowIdx + 1}
              </td>
              {row.map((v, ci) => {
                const col = columns[ci]
                const numeric = col && isNumericType(col.type)
                return (
                  <td
                    key={ci}
                    className={`max-w-[400px] truncate border-b border-border-light px-2 py-1 text-[12px] ${
                      numeric ? 'text-right font-mono tabular-nums text-ink' : 'text-ink'
                    }`}
                    title={formatCell(v)}
                  >
                    {v === null
                      ? <span className="italic text-ink-ghost">NULL</span>
                      : formatCell(v)}
                  </td>
                )
              })}
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}
