'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
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
  X, FileCode, Box, Link2, Eye, EyeOff,
  Lock, Timer, ShieldCheck, Terminal,
} from 'lucide-react'

// ─── Types ──────────────────────────────────────────────────────

interface OntologyProperty {
  name: string
  dataType: string
  description: string
  isMachineCode: boolean
}
interface OntologyLink {
  targetOd: string
  fromProp: string
  toProp: string
  cardinality: string
}
interface OntologyObject {
  name: string
  kind: string
  description: string
  hasCanonical: boolean
  properties: OntologyProperty[]
  links: OntologyLink[]
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

interface QueryResult {
  columns?: { name: string; type: string }[]
  rows?: unknown[][]
  rowCount: number
  durationMs: number
  error?: string
  blocked?: boolean
  rewrittenSql?: string
  usedObjects?: string[]
}

// ─── Constants ──────────────────────────────────────────────────

const ROW_LIMIT_OPTIONS = [100, 1000, 10000, 50000] as const
const DEFAULT_ROW_LIMIT: (typeof ROW_LIMIT_OPTIONS)[number] = 1000
const RESULT_PAGE_SIZE = 500 // client-side pagination chunk

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

export default function SQLPassthroughPage() {
  const t = useTranslations('sql')
  const industrial = useStyleMode().mode === 'industrial'
  const msg = useMessage()
  const { currentProject } = useProject()
  const reduce = useReducedMotion()

  // Editor state
  const [sql, setSql] = useState<string>('')
  const [result, setResult] = useState<QueryResult | null>(null)
  const [running, setRunning] = useState(false)
  const [showPhysical, setShowPhysical] = useState(false)
  const [rowLimit, setRowLimit] = useState<(typeof ROW_LIMIT_OPTIONS)[number]>(DEFAULT_ROW_LIMIT)
  const abortRef = useRef<AbortController | null>(null)

  // Sidebar state
  const [tab, setTab] = useState<'schema' | 'snippets' | 'history'>('schema')
  const [schema, setSchema] = useState<OntologyObject[]>([])
  const [snippets, setSnippets] = useState<Snippet[]>([])
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [schemaSearch, setSchemaSearch] = useState('')
  const [schemaOnlyQueryable, setSchemaOnlyQueryable] = useState(true)
  const [expandedOds, setExpandedOds] = useState<Set<string>>(new Set())

  // Save dialog
  const [saveOpen, setSaveOpen] = useState(false)
  const [saveName, setSaveName] = useState('')
  const [saveDesc, setSaveDesc] = useState('')

  // Result pagination
  const [resultPage, setResultPage] = useState(1)

  // Fetchers
  const fetchSchema = useCallback(async () => {
    if (!currentProject) return
    try {
      const res = await api<{ data: OntologyObject[] }>(`/ontology/sql-passthrough/schema?projectId=${currentProject.id}`)
      setSchema(res.data || [])
    } catch { setSchema([]) }
  }, [currentProject])

  const fetchSnippets = useCallback(async () => {
    if (!currentProject) return
    try {
      const res = await api<{ data: Snippet[] }>(`/ontology/sql-passthrough/snippets?projectId=${currentProject.id}`)
      setSnippets(res.data || [])
    } catch { setSnippets([]) }
  }, [currentProject])

  const fetchHistory = useCallback(async () => {
    if (!currentProject) return
    try {
      const res = await api<{ data: HistoryEntry[] }>(`/ontology/sql-passthrough/history?projectId=${currentProject.id}`)
      setHistory(res.data || [])
    } catch { setHistory([]) }
  }, [currentProject])

  useEffect(() => { fetchSchema() }, [fetchSchema])
  useEffect(() => { fetchSnippets(); fetchHistory() }, [fetchSnippets, fetchHistory])

  // Seed query once
  const didInitSql = useMemo(() => ({ v: false }), [])
  useEffect(() => {
    if (didInitSql.v || schema.length === 0) return
    didInitSql.v = true
    if (sql) return
    const first = schema.find(o => o.hasCanonical)
    if (!first) return
    const propList = first.properties.slice(0, 5).map(p => `  "${first.name}"."${p.name}"`).join(',\n')
    setSql(`${t('passthrough.seed_comment')}\n\nSELECT\n${propList}\nFROM "${first.name}"\nLIMIT 50;`)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [schema])

  // Editor autocomplete source
  const editorSchema = useMemo(() => {
    const out: Record<string, string[]> = {}
    for (const o of schema) {
      out[o.name] = o.properties.map(p => p.name)
    }
    return out
  }, [schema])

  const filteredSchema = useMemo(() => {
    let list = schema
    if (schemaOnlyQueryable) list = list.filter(o => o.hasCanonical)
    if (!schemaSearch) return list
    const q = schemaSearch.toLowerCase()
    return list.filter(o =>
      o.name.toLowerCase().includes(q) ||
      o.properties.some(p => p.name.toLowerCase().includes(q))
    )
  }, [schema, schemaSearch, schemaOnlyQueryable])

  // Execute query (with AbortController)
  const runQuery = useCallback(async () => {
    if (!currentProject || !sql.trim() || running) return
    // Prefer selection; else full text
    let execSQL = sql
    const selection = typeof window !== 'undefined' ? window.getSelection()?.toString().trim() : ''
    if (selection) execSQL = selection

    const ctrl = new AbortController()
    abortRef.current = ctrl
    setRunning(true)
    setResult(null)
    setResultPage(1)
    try {
      const res = await api<QueryResult>('/ontology/sql-passthrough', {
        method: 'POST',
        body: { sql: execSQL, projectId: currentProject.id, rowLimit },
        signal: ctrl.signal,
      })
      setResult(res)
      if (!res.error) fetchHistory()
    } catch (e) {
      if ((e as { name?: string })?.name === 'AbortError') {
        setResult({ rowCount: 0, durationMs: 0, error: t('passthrough.query_cancelled'), blocked: false })
      } else {
        setResult({ rowCount: 0, durationMs: 0, error: e instanceof Error ? e.message : 'Request failed' })
      }
    } finally {
      setRunning(false)
      abortRef.current = null
    }
  }, [sql, currentProject, running, fetchHistory, rowLimit])

  const cancelQuery = useCallback(() => {
    if (abortRef.current) abortRef.current.abort()
  }, [])

  // ⌘+Enter / Ctrl+Enter
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

  // Save snippet
  const saveSnippet = async () => {
    if (!currentProject || !saveName.trim() || !sql.trim()) return
    try {
      await api('/ontology/sql-passthrough/snippets', {
        method: 'POST',
        body: { projectId: currentProject.id, name: saveName.trim(), sql, description: saveDesc.trim() },
      })
      setSaveOpen(false); setSaveName(''); setSaveDesc('')
      msg.success(t('passthrough.saved')); fetchSnippets()
    } catch (e) { msg.error(e instanceof Error ? e.message : t('passthrough.save_failed')) }
  }

  const deleteSnippet = async (id: string) => {
    if (!confirm(t('passthrough.confirm_delete_snippet'))) return
    try { await api(`/ontology/sql-passthrough/snippets/${id}`, { method: 'DELETE' }); fetchSnippets() }
    catch { msg.error(t('passthrough.delete_failed')) }
  }
  const deleteHistoryEntry = async (id: string) => {
    try { await api(`/ontology/sql-passthrough/history?id=${id}`, { method: 'DELETE' }); fetchHistory() }
    catch { msg.error(t('passthrough.delete_failed')) }
  }

  // Insert helpers
  const insertAtEditor = (text: string) => {
    setSql(prev => prev + (prev && !prev.endsWith(' ') && !prev.endsWith('\n') ? ' ' : '') + text)
  }
  const insertJoin = (sourceOd: string, link: OntologyLink) => {
    const joinText = `\nJOIN "${link.targetOd}" ON "${sourceOd}"."${link.fromProp}" = "${link.targetOd}"."${link.toProp}"`
    setSql(prev => prev + joinText)
  }

  // Export
  const exportCSV = () => {
    if (!result?.columns || !result.rows) return
    const header = result.columns.map(c => csvEscape(c.name)).join(',')
    const body = result.rows.map(row => row.map(csvEscape).join(',')).join('\n')
    const blob = new Blob([header + '\n' + body], { type: 'text/csv;charset=utf-8;' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a'); a.href = url; a.download = `ontology_sql_${Date.now()}.csv`; a.click()
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
    const a = document.createElement('a'); a.href = url; a.download = `ontology_sql_${Date.now()}.json`; a.click()
    URL.revokeObjectURL(url)
  }

  const totalProps = schema.reduce((a, o) => a + o.properties.length, 0)
  const totalLinks = schema.reduce((a, o) => a + o.links.length, 0) / 2 // bidirectional

  // Parse unknown-Od error to surface clickable suggestions
  const unknownMatches = useMemo<string[]>(() => {
    if (!result?.error || !result.blocked) return []
    const m = result.error.match(/未知的 Ontology 对象[:：]\s*([^。]+)/)
    if (!m) return []
    return m[1].split(',').map(s => s.trim()).filter(Boolean)
  }, [result])

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
                // ONTOLOGY SQL
              </span>
              <span className="font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-muted">
                {schema.length} OD · {totalProps} PROPS · {Math.floor(totalLinks)} LINKS
              </span>
            </>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <Terminal size={14} className="text-ink" aria-hidden="true" />
              </div>
              <div className="min-w-0">
                <h1 className="text-base font-semibold tracking-tight text-ink">{t('passthrough.title')}</h1>
                <p className="truncate text-xs text-ink-muted">
                  {t('passthrough.subtitle')}
                </p>
              </div>
            </>
          )}
        </div>
        <div className="flex flex-shrink-0 items-center gap-4">
          {!industrial && (
            <div className="hidden items-baseline gap-x-3 text-xs text-ink-muted lg:flex">
              <span><span className="font-semibold tabular-nums text-ink">{schema.length}</span> Od</span>
              <span aria-hidden="true" className="text-ink-ghost">·</span>
              <span><span className="font-semibold tabular-nums text-ink">{totalProps}</span> {t('passthrough.stat_props')}</span>
              <span aria-hidden="true" className="text-ink-ghost">·</span>
              <span><span className="font-semibold tabular-nums text-ink">{Math.floor(totalLinks)}</span> {t('passthrough.stat_links')}</span>
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
              {t('passthrough.save')}
            </AnimatedButton>
            {running ? (
              <AnimatedButton
                variant="danger"
                size="sm"
                onClick={cancelQuery}
              >
                <X size={12} aria-hidden="true" />
                {t('passthrough.cancel')}
              </AnimatedButton>
            ) : (
              <AnimatedButton
                variant="primary"
                size="sm"
                disabled={!sql.trim()}
                onClick={runQuery}
              >
                <Play size={12} aria-hidden="true" />
                {t('passthrough.run')}
                <span className="ml-1 text-[11px] opacity-70">⌘↵</span>
              </AnimatedButton>
            )}
          </div>
        </div>
      </motion.header>

      {/* ── Toolbar: security pills + row-limit selector ───────────── */}
      <div className="flex flex-shrink-0 flex-wrap items-center justify-between gap-3 border-b border-border bg-white px-6 py-2.5">
        <div className="flex flex-wrap items-center gap-1.5">
          <SecurityPill
            icon={Lock}
            label={t('passthrough.pill_readonly')}
            tooltip={t('passthrough.pill_readonly_tip')}
          />
          <SecurityPill
            icon={Timer}
            label={t('passthrough.pill_timeout')}
            tooltip={t('passthrough.pill_timeout_tip')}
          />
          <SecurityPill
            icon={ShieldCheck}
            label={t('passthrough.pill_whitelist')}
            tooltip={t('passthrough.pill_whitelist_tip')}
          />
        </div>
        <div className="flex items-center gap-2">
          <span className="text-[11px] text-ink-ghost">{t('passthrough.row_limit')}</span>
          <div role="radiogroup" aria-label={t('passthrough.row_limit')} className="flex overflow-hidden rounded-md border border-border">
            {ROW_LIMIT_OPTIONS.map(n => {
              const active = rowLimit === n
              const label = n >= 1000 ? `${n / 1000}k` : String(n)
              return (
                <motion.button
                  key={n}
                  role="radio"
                  aria-checked={active}
                  onClick={() => setRowLimit(n)}
                  whileTap={reduce ? undefined : { scale: 0.97 }}
                  transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                  className={`border-r border-border px-2.5 py-1 text-[11px] tabular-nums outline-none last:border-r-0 cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                    active ? 'bg-ink text-white font-medium' : 'text-ink-muted'
                  }`}
                >
                  {label}
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
          <div role="tablist" aria-label={t('passthrough.sidebar_aria')} className="flex flex-shrink-0 border-b border-border">
            {([
              ['schema', t('passthrough.tab_schema'), Box],
              ['snippets', t('passthrough.tab_snippets'), Star],
              ['history', t('passthrough.tab_history'), History],
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
                    placeholder={t('passthrough.schema_filter_placeholder')}
                    aria-label={t('passthrough.schema_filter_aria')}
                    className="w-full rounded-md border border-border bg-white px-2 py-1 text-[11px] text-ink outline-none placeholder:text-ink-ghost focus:border-ink"
                  />
                  <div className="flex items-center justify-between text-[11px] text-ink-ghost">
                    <label className="inline-flex cursor-pointer items-center gap-1">
                      <input
                        type="checkbox"
                        checked={schemaOnlyQueryable}
                        onChange={e => setSchemaOnlyQueryable(e.target.checked)}
                        className="accent-ink"
                      />
                      {t('passthrough.schema_only_queryable')}
                    </label>
                    <span><span className="tabular-nums">{filteredSchema.length}</span> / <span className="tabular-nums">{schema.length}</span></span>
                  </div>
                </div>
                {schema.length === 0 && (
                  <div className="flex flex-col items-center justify-center gap-1 p-4 text-center">
                    <span className="text-xs text-ink-muted">{t('passthrough.schema_empty')}</span>
                    <span className="text-[11px] text-ink-ghost">{t('passthrough.schema_empty_hint')}</span>
                  </div>
                )}
                {filteredSchema.map(o => {
                  const expanded = expandedOds.has(o.name)
                  const queryable = o.hasCanonical
                  return (
                    <div key={o.name} className="border-b border-border-light">
                      <div className="flex items-center">
                        <button
                          onClick={() => {
                            const next = new Set(expandedOds)
                            if (expanded) next.delete(o.name); else next.add(o.name)
                            setExpandedOds(next)
                          }}
                          className="flex h-7 w-6 flex-shrink-0 items-center justify-center text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                          aria-label={expanded ? t('passthrough.collapse') : t('passthrough.expand')}
                          aria-expanded={expanded}
                        >
                          {expanded ? <ChevronDown className="h-3 w-3" aria-hidden="true" /> : <ChevronRight className="h-3 w-3" aria-hidden="true" />}
                        </button>
                        <button
                          onClick={() => queryable && insertAtEditor(`"${o.name}"`)}
                          disabled={!queryable}
                          title={queryable ? t('passthrough.insert_od_title', { name: o.name }) : t('passthrough.od_not_queryable')}
                          className="group flex flex-1 items-center gap-2 px-1 py-1.5 text-left outline-none disabled:cursor-not-allowed disabled:opacity-50 focus-visible:ring-1 focus-visible:ring-ink"
                        >
                          <Box className={`h-3 w-3 flex-shrink-0 ${queryable ? 'text-ink' : 'text-ink-ghost'}`} aria-hidden="true" />
                          <span className="truncate font-mono text-xs font-semibold text-ink">{o.name}</span>
                          {!queryable && (
                            <span className="rounded border border-dashed border-border px-1 text-[11px] text-ink-ghost">{t('passthrough.not_canonical')}</span>
                          )}
                          <span className="ml-auto text-[11px] tabular-nums text-ink-ghost">
                            {o.properties.length}p{o.links.length > 0 ? ` · ${o.links.length / 2}l` : ''}
                          </span>
                        </button>
                      </div>
                      {expanded && (
                        <div className="bg-canvas-alt/60 pb-1">
                          {o.description && (
                            <div className="px-8 py-1 text-[11px] italic text-ink-ghost">{o.description}</div>
                          )}
                          {o.properties.map(p => (
                            <button
                              key={p.name}
                              onClick={() => insertAtEditor(`"${p.name}"`)}
                              className="group flex w-full items-center justify-between px-8 py-0.5 text-left text-[11px] outline-none focus-visible:ring-1 focus-visible:ring-ink"
                              title={t('passthrough.insert_prop_title')}
                            >
                              <span className="flex items-center gap-1.5">
                                <span className="font-mono text-ink group-hover:text-ink">{p.name}</span>
                                {p.isMachineCode && (
                                  <span
                                    title={t('passthrough.mc_title')}
                                    className="rounded border border-border px-1 text-[11px] font-mono text-ink-ghost"
                                  >
                                    MC
                                  </span>
                                )}
                              </span>
                              <span className="font-mono text-[11px] text-ink-ghost">{p.dataType}</span>
                            </button>
                          ))}
                          {o.links.length > 0 && (
                            <div className="mt-1 border-t border-border-light pt-1">
                              <div className="px-8 pb-1 text-[11px] font-medium text-ink-ghost">{t('passthrough.join_label')}</div>
                              {o.links.map((l, i) => (
                                <button
                                  key={i}
                                  onClick={() => insertJoin(o.name, l)}
                                  className="flex w-full items-start gap-1 px-8 py-0.5 text-left text-[11px] outline-none focus-visible:ring-1 focus-visible:ring-ink"
                                  title={t('passthrough.insert_join_title')}
                                >
                                  <Link2 className="mt-0.5 h-2.5 w-2.5 flex-shrink-0 text-ink" aria-hidden="true" />
                                  <div className="flex-1">
                                    <div className="text-ink">
                                      <span aria-hidden="true">⇄ </span>
                                      <span className="font-mono font-semibold">{l.targetOd}</span>
                                      <span className="ml-1 text-[11px] text-ink-ghost">({l.cardinality || '—'})</span>
                                    </div>
                                    <div className="font-mono text-[11px] text-ink-ghost">
                                      {l.fromProp} = {l.toProp}
                                    </div>
                                  </div>
                                </button>
                              ))}
                            </div>
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
                    <span className="text-xs text-ink-muted">{t('passthrough.snippets_empty')}</span>
                    <span className="text-[11px] text-ink-ghost">{t('passthrough.snippets_empty_hint')}</span>
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
                        aria-label={t('passthrough.delete_snippet_aria')}
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
                    <span className="text-xs text-ink-muted">{t('passthrough.history_empty')}</span>
                    <span className="text-[11px] text-ink-ghost">{t('passthrough.history_empty_hint')}</span>
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
                            {h.error ? t('passthrough.history_error') : <><span className="tabular-nums">{h.rowCount}</span> {t('passthrough.history_rows')}</>}
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
                        aria-label={t('passthrough.delete_history_aria')}
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
                <span className="tabular-nums">{sql.length}</span> {t('passthrough.editor_hint')}
              </div>
            </div>
            <div className="flex-1 min-h-0 overflow-auto">
              <SQLEditor value={sql} onChange={setSql} schema={editorSchema} height="100%" />
            </div>
          </div>

          {/* Results frame */}
          <div className="flex flex-1 min-h-0 flex-col overflow-hidden bg-white">
            <div className="flex flex-shrink-0 flex-wrap items-center justify-between gap-2 border-b border-border-light bg-canvas-alt px-3 py-1.5">
              <div className="flex items-center gap-3 text-xs font-medium text-ink">
                <div className="flex items-center gap-1.5">
                  <Database className="h-3 w-3 text-ink-muted" aria-hidden="true" />
                  {t('passthrough.results')}
                </div>
                {result?.usedObjects && result.usedObjects.length > 0 && (
                  <div className="flex flex-wrap items-center gap-1 text-[11px] text-ink-muted">
                    <span className="text-ink-ghost">{t('passthrough.used_objects')}</span>
                    {result.usedObjects.map(o => (
                      <span key={o} className="rounded border border-border bg-white px-1 py-0.5 font-mono text-[11px] text-ink">
                        {o}
                      </span>
                    ))}
                  </div>
                )}
              </div>
              <div className="flex items-center gap-2 text-[11px]">
                {result && !result.error && (
                  <>
                    <span className="text-ink-muted">
                      <span className="font-semibold tabular-nums text-ink">{result.rowCount}</span> {t('passthrough.rows')}
                    </span>
                    <span aria-hidden="true" className="text-ink-ghost">·</span>
                    <span className="text-ink-muted">
                      <span className="tabular-nums text-ink">{result.durationMs}</span>ms
                    </span>
                    {result.rewrittenSql && (
                      <motion.button
                        onClick={() => setShowPhysical(v => !v)}
                        whileTap={reduce ? undefined : { scale: 0.97 }}
                        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                        className="inline-flex items-center gap-1 rounded border border-border bg-white px-1.5 py-0.5 text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
                      >
                        {showPhysical ? <EyeOff className="h-2.5 w-2.5" aria-hidden="true" /> : <Eye className="h-2.5 w-2.5" aria-hidden="true" />}
                        {showPhysical ? t('passthrough.hide_physical') : t('passthrough.show_physical')}
                      </motion.button>
                    )}
                    <motion.button
                      onClick={exportCSV}
                      whileTap={reduce ? undefined : { scale: 0.97 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      className="inline-flex items-center gap-1 rounded border border-border bg-white px-1.5 py-0.5 text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      <Download className="h-2.5 w-2.5" aria-hidden="true" /> CSV
                    </motion.button>
                    <motion.button
                      onClick={exportJSON}
                      whileTap={reduce ? undefined : { scale: 0.97 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      className="inline-flex items-center gap-1 rounded border border-border bg-white px-1.5 py-0.5 text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      <Download className="h-2.5 w-2.5" aria-hidden="true" /> JSON
                    </motion.button>
                  </>
                )}
                {result?.error && (
                  <span className="inline-flex items-center gap-1 font-medium text-danger">
                    <AlertCircle size={11} aria-hidden="true" />
                    {result.blocked ? t('passthrough.blocked') : t('passthrough.exec_error')}
                  </span>
                )}
              </div>
            </div>

            <div className="flex-1 min-h-0 overflow-auto">
              {/* Idle */}
              {!result && !running && (
                <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
                  <div className="text-sm text-ink-muted">
                    {t('passthrough.idle_hint')}
                  </div>
                  <div className="text-xs text-ink-ghost">{t('passthrough.idle_sub', { rowLimit: rowLimit.toLocaleString() })}</div>
                </div>
              )}

              {/* Running */}
              {running && (
                <div className="flex h-full items-center justify-center">
                  <InlineLoader text={t('passthrough.running_long')} />
                </div>
              )}

              {/* Error */}
              {result?.error && (
                <div className="p-4">
                  <div className="overflow-hidden rounded-md border border-danger/40 bg-danger/5">
                    <div className="flex items-center gap-2 border-b border-danger/30 bg-danger/10 px-3 py-2">
                      <AlertCircle size={14} className="flex-shrink-0 text-danger" aria-hidden="true" />
                      <span className="text-sm font-semibold text-danger">
                        {result.blocked ? t('passthrough.blocked') : t('passthrough.exec_error')}
                      </span>
                    </div>
                    <pre className="whitespace-pre-wrap break-words px-3 py-2 font-mono text-xs leading-relaxed text-ink">
                      {result.error}
                    </pre>
                    {unknownMatches.length > 0 && schema.length > 0 && (
                      <div className="border-t border-danger/20 px-3 py-2">
                        <div className="mb-1.5 text-[11px] text-ink-muted">{t('passthrough.avail_od_hint')}</div>
                        <div className="flex flex-wrap gap-1">
                          {schema.filter(o => o.hasCanonical).map(o => (
                            <motion.button
                              key={o.name}
                              onClick={() => insertAtEditor(`"${o.name}"`)}
                              whileHover={reduce ? undefined : { scale: 1.03 }}
                              whileTap={reduce ? undefined : { scale: 0.97 }}
                              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                              className="rounded-md border border-border bg-white px-1.5 py-0.5 font-mono text-[11px] text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
                            >
                              {o.name}
                            </motion.button>
                          ))}
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              )}

              {/* Physical SQL preview */}
              {result && !result.error && showPhysical && result.rewrittenSql && (
                <div className="border-b border-border bg-canvas-alt px-3 py-2">
                  <div className="mb-1 text-[11px] font-medium text-ink-muted">{t('passthrough.physical_sql_label')}</div>
                  <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed text-ink">
                    {result.rewrittenSql}
                  </pre>
                </div>
              )}

              {/* Table */}
              {result && !result.error && result.columns && (
                <ResultsTable
                  columns={result.columns}
                  rows={result.rows || []}
                  page={resultPage}
                  onPageChange={setResultPage}
                />
              )}
            </div>
          </div>
        </div>
      </div>

      {/* ── Footer (compact status) ────────────────────────────────── */}
      <div className="flex flex-shrink-0 items-center justify-between border-t border-border bg-canvas-alt px-6 py-1.5 text-[11px] text-ink-muted">
        <div className="flex items-center gap-3">
          <span>{t('passthrough.footer_project')}<span className="font-medium text-ink">{currentProject?.name || '—'}</span></span>
          <span aria-hidden="true" className="text-ink-ghost">·</span>
          <span>{t('passthrough.footer_readonly')}</span>
        </div>
        <div className="tabular-nums">
          {running ? t('passthrough.status_running') : result
            ? (result.error ? t('passthrough.status_fail') : t('passthrough.status_ok', { rowCount: result.rowCount, durationMs: result.durationMs }))
            : t('passthrough.status_idle')}
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
            aria-label={t('passthrough.save_dialog_aria')}
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
                <span className="text-sm font-semibold tracking-tight text-ink">{t('passthrough.save_dialog_title')}</span>
                <motion.button
                  onClick={() => setSaveOpen(false)}
                  whileHover={reduce ? undefined : { scale: 1.15 }}
                  whileTap={reduce ? undefined : { scale: 0.9 }}
                  transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                  aria-label={t('passthrough.save_dialog_close')}
                  title={t('passthrough.save_dialog_close')}
                  className="rounded p-1 text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                >
                  <X className="h-4 w-4" aria-hidden="true" />
                </motion.button>
              </div>
              <div className="space-y-3 p-4">
                <div>
                  <label className="mb-1 block text-xs font-medium text-ink-muted">{t('passthrough.save_name_label')}</label>
                  <input
                    value={saveName}
                    onChange={e => setSaveName(e.target.value)}
                    placeholder={t('passthrough.save_name_placeholder')}
                    className="h-8 w-full rounded-md border border-border bg-white px-2.5 text-sm text-ink outline-none focus:border-ink"
                    autoFocus
                    aria-label={t('passthrough.save_name_aria')}
                  />
                </div>
                <div>
                  <label className="mb-1 block text-xs font-medium text-ink-muted">{t('passthrough.save_desc_label')}</label>
                  <input
                    value={saveDesc}
                    onChange={e => setSaveDesc(e.target.value)}
                    className="h-8 w-full rounded-md border border-border bg-white px-2.5 text-sm text-ink outline-none focus:border-ink"
                    aria-label={t('passthrough.save_desc_aria')}
                  />
                </div>
                <div className="flex justify-end gap-2 pt-1">
                  <AnimatedButton variant="secondary" size="sm" onClick={() => setSaveOpen(false)}>
                    {t('passthrough.save_cancel')}
                  </AnimatedButton>
                  <AnimatedButton
                    variant="primary"
                    size="sm"
                    onClick={saveSnippet}
                    disabled={!saveName.trim()}
                  >
                    <Save size={12} aria-hidden="true" />
                    {t('passthrough.save_confirm')}
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

// ─── Results Table with client-side pagination ─────────────────

function ResultsTable({ columns, rows, page, onPageChange }: {
  columns: { name: string; type: string }[]
  rows: unknown[][]
  page: number
  onPageChange: (page: number) => void
}) {
  const reduce = useReducedMotion()
  const t = useTranslations('sql')
  const totalRows = rows.length

  if (totalRows === 0) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1 text-center">
        <span className="text-sm text-ink-muted">{t('passthrough.empty_result')}</span>
        <span className="text-xs text-ink-ghost">{t('passthrough.empty_result_hint')}</span>
      </div>
    )
  }

  const totalPages = Math.max(1, Math.ceil(totalRows / RESULT_PAGE_SIZE))
  const clampedPage = Math.min(Math.max(1, page), totalPages)
  const start = (clampedPage - 1) * RESULT_PAGE_SIZE
  const end = Math.min(start + RESULT_PAGE_SIZE, totalRows)
  const pageRows = rows.slice(start, end)

  return (
    <div className="flex h-full flex-col">
      {totalPages > 1 && (
        <div className="flex flex-shrink-0 items-center justify-between border-b border-border-light bg-white px-3 py-1.5 text-[11px]">
          <div className="text-ink-muted">
            {t('passthrough.page_showing_prefix')} <span className="font-semibold tabular-nums text-ink">{start + 1}</span>
            {' '}– <span className="font-semibold tabular-nums text-ink">{end}</span>
            {' '}{t('passthrough.page_showing_suffix', { total: totalRows })}
          </div>
          <div className="flex items-center gap-1.5">
            <motion.button
              onClick={() => onPageChange(Math.max(1, clampedPage - 1))}
              disabled={clampedPage === 1}
              whileTap={reduce ? undefined : { scale: 0.95 }}
              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
              className="inline-flex h-6 items-center rounded border border-border bg-white px-2 text-ink outline-none hover:border-ink disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink"
            >
              {t('passthrough.prev_page')}
            </motion.button>
            <span className="tabular-nums text-ink-muted">{clampedPage} / {totalPages}</span>
            <motion.button
              onClick={() => onPageChange(Math.min(totalPages, clampedPage + 1))}
              disabled={clampedPage >= totalPages}
              whileTap={reduce ? undefined : { scale: 0.95 }}
              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
              className="inline-flex h-6 items-center rounded border border-border bg-white px-2 text-ink outline-none hover:border-ink disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink"
            >
              {t('passthrough.next_page')}
            </motion.button>
          </div>
        </div>
      )}
      <div className="flex-1 min-h-0 overflow-auto">
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
            {pageRows.map((row, ri) => {
              const absRowIdx = start + ri
              return (
                <tr key={absRowIdx}>
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
      </div>
    </div>
  )
}
