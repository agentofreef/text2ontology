'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback, useRef } from 'react'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { useProject } from '@/lib/project'
import { api, getApiBase } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useStyleMode } from '@/lib/style-mode'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import type { ImportProgressResponse, PbitTablePreview } from '@/types/api'
import {
  Database, ChevronRight, RefreshCw, Upload, Plus, X,
} from 'lucide-react'

interface TableRow {
  name: string
  sourceType: string
  columnCount: number
  rowCount: number | null
  status: string
  columns: { name: string; dataType: string }[]
}

// Status → neutral/green/red (no amber — v2 palette: 黑白灰绿红 only)
function statusDotColor(status: string): string {
  switch (status) {
    case 'loaded': return 'bg-success'
    case 'error': return 'bg-danger'
    case 'pending': return 'bg-ink-ghost' // 中性灰，非 amber
    case 'skipped': return 'bg-ink-ghost'
    default: return 'bg-border'
  }
}

// Industrial chip variant — hairline border + monochrome (success/danger keep
// hue so failure & ok are still scannable at a glance, but everything else
// stays ink).
function statusChipCls(status: string): string {
  switch (status) {
    case 'loaded':  return 'border-success/60 text-success'
    case 'error':   return 'border-danger/60 text-danger'
    case 'pending': return 'border-ink/30 text-ink-muted'
    case 'skipped': return 'border-ink/20 text-ink-ghost'
    default:        return 'border-ink/20 text-ink-ghost'
  }
}

type TFunc = (key: string, vars?: Record<string, string | number>) => string

function statusLabel(status: string, t: TFunc): string {
  switch (status) {
    case 'loaded': return t('status_loaded')
    case 'error': return t('status_error')
    case 'pending': return t('status_pending')
    case 'skipped': return t('status_skipped')
    case 'unknown': return t('status_unknown')
    default: return status
  }
}

function sourceLabel(st: string, t: TFunc): string {
  switch (st) {
    case 'pbix': return 'PBIX'
    case 'excel': return 'Excel'
    case 'sql': return 'SQL'
    case 'derived': return t('source_derived')
    case 'constant': return t('source_constant')
    case 'calculated': return t('source_calculated')
    case 'unsupported': return 'N/A'
    default: return st
  }
}

export default function LakehousePageMinimal() {
  const t = useTranslations('lakehouse')
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()

  const [tables, setTables] = useState<TableRow[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [expandedTable, setExpandedTable] = useState<string | null>(null)
  const [showUpload, setShowUpload] = useState(false)
  const [uploadTableName, setUploadTableName] = useState('')
  const [uploading, setUploading] = useState(false)
  const [refreshKey, setRefreshKey] = useState(0) // bumped on Refresh click → drives 1-shot rotate
  const fileRef = useRef<HTMLInputElement>(null)

  const fetchData = useCallback(async () => {
    if (!currentProject) return
    setLoading(true)
    setError('')
    try {
      // Reads the actual proj_<hex>.<table> rows that landed in the lakehouse
      // (cross-source-type — postgres / pbi / file all merge here). Replaces
      // the legacy /connector/pbit/progress call which only saw PBIT imports.
      const data = await api<{
        schema: string
        tables: { name: string; row_count: number; columns: { name: string; data_type: string }[] }[]
      }>(`/connector/lakehouse/tables?project_id=${currentProject.id}`)

      const rows: TableRow[] = (data.tables || []).map((t) => ({
        name: t.name,
        sourceType: 'lakehouse', // source-type-agnostic — everything in proj_<hex> is "in the lakehouse"
        columnCount: t.columns?.length || 0,
        rowCount: t.row_count,
        status: 'loaded',
        columns: (t.columns || []).map((c) => ({ name: c.name, dataType: c.data_type || 'text' })),
      }))
      setTables(rows)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('load_failed'))
    } finally { setLoading(false) }
  }, [currentProject])

  useEffect(() => { fetchData() }, [fetchData])

  const handleRefresh = () => {
    setRefreshKey(k => k + 1)
    fetchData()
  }

  const handleUpload = async () => {
    const file = fileRef.current?.files?.[0]
    if (!file || !uploadTableName.trim() || !currentProject) return
    setUploading(true)
    try {
      const form = new FormData()
      form.append('projectId', currentProject.id)
      form.append('tableName', uploadTableName.trim())
      form.append('file', file)
      const token = localStorage.getItem('lakehouse2ontology_token') || ''
      const res = await fetch(`${getApiBase()}/connector/pbit/add-table`, {
        method: 'POST',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        body: form,
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.error || t('upload_failed'))
      msg.success(t('upload_success', { name: uploadTableName.trim(), count: data.rowCount }))
      setShowUpload(false)
      setUploadTableName('')
      if (fileRef.current) fileRef.current.value = ''
      fetchData()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('upload_failed'))
    } finally { setUploading(false) }
  }

  const totalRows = tables.reduce((sum, t) => sum + (t.rowCount || 0), 0)
  const totalCols = tables.reduce((sum, t) => sum + t.columnCount, 0)
  const loadedCount = tables.filter((t) => t.status === 'loaded').length

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
        <div className="text-sm text-ink-muted">{t('no_project')}</div>
        <div className="text-xs text-ink-ghost">{t('no_project_hint')}</div>
      </div>
    )
  }

  // ─────────────────────────────────────────────────────────────────
  // Silicon Valley Minimal · list-create-detail archetype
  //   docs/design/design-system.md v2
  //   docs/design/frontend-quality/{01..06}.md
  // ─────────────────────────────────────────────────────────────────
  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header (紧凑单条 + subtle shadow / industrial: 2px ink rule, no shadow) */}
      <motion.header
        initial={reduce ? undefined : { opacity: 0, y: -4 }}
        animate={reduce ? undefined : { opacity: 1, y: 0 }}
        transition={{ duration: 0.2, ease: 'easeOut' }}
        className={`flex h-14 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'}`}
      >
        <div className="flex min-w-0 items-center gap-3">
          {industrial ? (
            <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // {t('title').toString().toUpperCase()}
            </span>
          ) : (
            <>
              <Database size={18} className="text-ink" aria-hidden="true" />
              <h1 className="text-base font-semibold tracking-tight text-ink whitespace-nowrap">
                {t('title')}
              </h1>
            </>
          )}
          {!loading && (
            <span className={industrial ? 'font-mono text-[10px] tracking-[0.14em] text-ink-muted truncate tabular-nums' : 'text-xs text-ink-ghost truncate'}>
              {industrial
                ? `${tables.length} TABLES · ${totalRows.toLocaleString()} ROWS · ${totalCols} COLS`
                : t('stats', { tables: tables.length, rows: totalRows.toLocaleString(), cols: totalCols })}
            </span>
          )}
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
          <AnimatedButton
            variant={showUpload ? 'primary' : 'secondary'}
            size="sm"
            onClick={() => setShowUpload(!showUpload)}
            aria-label={t('upload_table')}
          >
            {showUpload ? <X size={12} aria-hidden="true" /> : <Plus size={12} aria-hidden="true" />}
            {showUpload ? t('close') : t('upload_table')}
          </AnimatedButton>
          <motion.button
            onClick={handleRefresh}
            disabled={loading}
            whileHover={reduce || loading ? undefined : { scale: 1.05 }}
            whileTap={reduce || loading ? undefined : { scale: 0.95 }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            aria-label={t('refresh')}
            className={`inline-flex h-7 w-7 items-center justify-center bg-white text-ink-muted outline-none hover:border-ink hover:text-ink disabled:opacity-40 disabled:cursor-not-allowed focus-visible:ring-1 focus-visible:ring-ink ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}
          >
            <motion.span
              key={refreshKey}
              animate={reduce ? undefined : loading
                ? { rotate: 360 }
                : { rotate: 360 }}
              transition={loading
                ? { repeat: Infinity, duration: 1, ease: 'linear' }
                : { duration: 0.5, ease: 'easeOut' }}
              className="inline-flex"
            >
              <RefreshCw size={12} aria-hidden="true" />
            </motion.span>
          </motion.button>
        </div>
      </motion.header>

      {/* Inline upload panel */}
      <AnimatePresence initial={false}>
        {showUpload && (
          <motion.div
            key="upload"
            initial={reduce ? undefined : { height: 0, opacity: 0 }}
            animate={reduce ? undefined : { height: 'auto', opacity: 1 }}
            exit={reduce ? undefined : { height: 0, opacity: 0 }}
            transition={{ duration: 0.2, ease: 'easeOut' }}
            style={{ overflow: 'hidden' }}
            className="flex-shrink-0 border-b border-border bg-canvas-alt"
          >
            <div className="flex flex-wrap items-end gap-3 px-6 py-3">
              <div className="flex flex-col gap-1">
                <label htmlFor="lh-upload-name" className="text-[11px] font-medium text-ink-muted">
                  {t('table_name')}
                </label>
                <input
                  id="lh-upload-name"
                  type="text"
                  value={uploadTableName}
                  onChange={(e) => setUploadTableName(e.target.value)}
                  placeholder={t('table_name_placeholder')}
                  className="h-8 w-56 rounded-md border border-border bg-white px-2.5 text-sm text-ink outline-none placeholder:text-ink-ghost focus:border-ink focus:ring-1 focus:ring-ink/10"
                />
              </div>
              <div className="flex flex-col gap-1">
                <label htmlFor="lh-upload-file" className="text-[11px] font-medium text-ink-muted">
                  {t('file_label')}
                </label>
                <input
                  id="lh-upload-file"
                  ref={fileRef}
                  type="file"
                  accept=".csv,.xlsx,.xls"
                  aria-label={t('choose_file')}
                  className="h-8 text-xs text-ink-muted file:mr-3 file:h-full file:rounded-md file:border file:border-border file:bg-white file:px-2.5 file:text-xs file:text-ink hover:file:border-ink"
                />
              </div>
              <AnimatedButton
                variant="primary"
                size="md"
                onClick={handleUpload}
                disabled={uploading || !uploadTableName.trim()}
                aria-label={t('upload')}
              >
                <motion.span
                  animate={uploading && !reduce ? { rotate: 360 } : undefined}
                  transition={uploading ? { repeat: Infinity, duration: 1, ease: 'linear' } : { duration: 0 }}
                  className="inline-flex"
                >
                  <Upload size={14} aria-hidden="true" />
                </motion.span>
                {uploading ? t('uploading') : t('upload')}
              </AnimatedButton>
              <AnimatedButton
                variant="ghost"
                size="md"
                onClick={() => setShowUpload(false)}
                aria-label={t('cancel')}
              >
                {t('cancel')}
              </AnimatedButton>
            </div>
          </motion.div>
        )}
      </AnimatePresence>

      {/* Error banner */}
      <AnimatePresence>
        {error && (
          <motion.div
            key="err"
            initial={reduce ? undefined : { opacity: 0, height: 0 }}
            animate={reduce ? undefined : { opacity: 1, height: 'auto' }}
            exit={reduce ? undefined : { opacity: 0, height: 0 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
            style={{ overflow: 'hidden' }}
            role="alert"
            className={`flex-shrink-0 px-6 py-2 ${industrial ? 'border-b-2 border-danger bg-danger/5' : 'border-b border-danger/30 bg-danger/10'}`}
          >
            <div className={`flex items-center gap-2 text-xs text-danger ${industrial ? 'font-mono tracking-[0.06em]' : ''}`}>
              <span
                className={`inline-block h-1.5 w-1.5 bg-danger ${industrial ? '' : 'rounded-full'}`}
                aria-hidden="true"
              />
              <span className="font-medium">
                {industrial ? `// ${t('load_error_prefix').toString().toUpperCase()}` : t('load_error_prefix')}
              </span>
              <span>{error}</span>
            </div>
          </motion.div>
        )}
      </AnimatePresence>

      {/* Stats band (仅在有数据时) */}
      <AnimatePresence initial={false}>
        {!loading && tables.length > 0 && (
          <motion.div
            key="stats"
            initial={reduce ? undefined : { opacity: 0, y: -4 }}
            animate={reduce ? undefined : { opacity: 1, y: 0 }}
            transition={{ duration: 0.2, ease: 'easeOut' }}
            className={`flex flex-shrink-0 ${industrial ? 'border-b-2 border-ink bg-canvas-alt' : 'border-b border-border bg-white'}`}
          >
            {[
              { label: t('stat_tables'), value: tables.length.toString(), tone: 'ink' },
              { label: t('stat_loaded'), value: loadedCount.toString(), tone: loadedCount === tables.length ? 'success' : 'ink' },
              { label: t('stat_total_rows'), value: totalRows.toLocaleString(), tone: 'ink' },
              { label: t('stat_total_cols'), value: totalCols.toString(), tone: 'ink' },
            ].map((s, i, arr) => (
              <div
                key={s.label}
                className={`flex-1 px-6 py-3 text-center ${
                  i < arr.length - 1
                    ? industrial
                      ? 'border-r border-ink/30'
                      : 'border-r border-border-light'
                    : ''
                }`}
              >
                <div
                  className={
                    industrial
                      ? 'font-mono text-[10px] tracking-[0.18em] text-ink-ghost'
                      : 'text-[11px] tracking-wide text-ink-ghost'
                  }
                >
                  {industrial ? `// ${s.label.toString().toUpperCase()}` : s.label}
                </div>
                <div
                  className={`tabular-nums ${
                    industrial
                      ? 'font-mono text-xl font-bold text-ink'
                      : `text-xl font-semibold ${s.tone === 'success' ? 'text-success' : 'text-ink'}`
                  }`}
                >
                  {s.value}
                </div>
              </div>
            ))}
          </motion.div>
        )}
      </AnimatePresence>

      {/* Content area (scrolls inside) */}
      <div className="flex flex-1 min-h-0 flex-col">
        {loading ? (
          <div className="flex h-full items-center justify-center">
            <InlineLoader text={t('loading')} />
          </div>
        ) : tables.length === 0 ? (
          <EmptyState />
        ) : (
          <>
            {/* Sticky column header */}
            <div
              className={`sticky top-0 z-10 flex flex-shrink-0 items-center px-6 py-2 ${
                industrial
                  ? 'border-b-2 border-ink bg-white font-mono text-[10px] uppercase tracking-[0.18em] text-ink-muted'
                  : 'border-b border-border bg-canvas-alt text-[11px] font-medium tracking-wide text-ink-ghost'
              }`}
            >
              <div className="w-6" />
              <div className="mr-3 w-4" />
              <div className="flex-1">{industrial ? `// ${t('col_name').toString().toUpperCase()}` : t('col_name')}</div>
              <div className="w-20 text-right">{industrial ? t('col_type').toString().toUpperCase() : t('col_type')}</div>
              <div className="w-24 text-right">{industrial ? t('col_rows').toString().toUpperCase() : t('col_rows')}</div>
              <div className="w-16 text-right">{industrial ? t('col_cols').toString().toUpperCase() : t('col_cols')}</div>
              <div className="w-20 text-right">{industrial ? t('col_status').toString().toUpperCase() : t('col_status')}</div>
            </div>

            {/* Scroll region */}
            <div className="flex-1 min-h-0 overflow-y-auto">
              <div className={`bg-white ${industrial ? 'divide-y divide-ink/15' : 'divide-y divide-border'}`}>
                <AnimatePresence initial={false}>
                  {tables.map((tbl) => (
                    <TableRowItem
                      key={tbl.name}
                      t={tbl}
                      tl={(k, v) => t(k, v)}
                      expanded={expandedTable === tbl.name}
                      onToggle={() => setExpandedTable(expandedTable === tbl.name ? null : tbl.name)}
                      reduce={!!reduce}
                    />
                  ))}
                </AnimatePresence>
              </div>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

// ─────────────────────────────────────────────────────────────────
// Sub-components
// ─────────────────────────────────────────────────────────────────

function TableRowItem({ t: row, tl, expanded, onToggle, reduce }: {
  t: TableRow
  tl: TFunc
  expanded: boolean
  onToggle: () => void
  reduce: boolean
}) {
  const industrial = useStyleMode().mode === 'industrial'
  return (
    <motion.div layout transition={{ duration: 0.2 }}>
      <motion.button
        onClick={onToggle}
        aria-expanded={expanded}
        aria-label={`${expanded ? tl('collapse') : tl('expand')} ${tl('table_label')} ${row.name}`}
        className={`group flex w-full items-center px-6 py-2.5 text-left outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
          expanded ? 'bg-canvas-alt' : industrial ? 'hover:bg-canvas-alt' : ''
        }`}
      >
        <div className="w-6 flex-shrink-0">
          <motion.span
            animate={{ rotate: expanded ? 90 : 0 }}
            transition={reduce ? { duration: 0 } : { duration: 0.15, ease: 'easeOut' }}
            className="inline-flex text-ink-ghost transition-colors duration-150 group-hover:text-ink"
          >
            <ChevronRight size={14} aria-hidden="true" />
          </motion.span>
        </div>
        <div className="mr-3 w-4 flex-shrink-0">
          <span
            className={`inline-block h-2 w-2 ${industrial ? '' : 'rounded-full'} ${statusDotColor(row.status)}`}
            title={statusLabel(row.status, tl)}
            aria-label={statusLabel(row.status, tl)}
          />
        </div>
        <div className="min-w-0 flex-1">
          <span className={`block truncate text-sm text-ink ${industrial ? 'font-mono tracking-[0.02em]' : 'font-medium'}`}>
            {row.name}
          </span>
        </div>
        <div className="w-20 text-right">
          <span className={industrial ? 'font-mono text-[10px] tracking-[0.14em] text-ink-muted uppercase' : 'text-xs text-ink-ghost'}>
            {sourceLabel(row.sourceType, tl)}
          </span>
        </div>
        <div className="w-24 text-right">
          <span className={`tabular-nums text-ink ${industrial ? 'font-mono text-[13px] font-bold' : 'text-sm font-semibold'}`}>
            {row.rowCount != null ? row.rowCount.toLocaleString() : '—'}
          </span>
        </div>
        <div className="w-16 text-right">
          <span className={`tabular-nums text-ink-muted ${industrial ? 'font-mono text-[13px]' : 'text-sm'}`}>
            {row.columnCount}
          </span>
        </div>
        <div className="w-20 text-right">
          {industrial ? (
            <span
              className={`inline-flex items-center justify-center border px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-[0.14em] ${
                statusChipCls(row.status)
              }`}
            >
              {statusLabel(row.status, tl)}
            </span>
          ) : (
            <span className="text-xs text-ink-muted">{statusLabel(row.status, tl)}</span>
          )}
        </div>
      </motion.button>

      <AnimatePresence initial={false}>
        {expanded && row.columns.length > 0 && (
          <motion.div
            key="body"
            initial={reduce ? undefined : { height: 0, opacity: 0 }}
            animate={reduce ? undefined : { height: 'auto', opacity: 1 }}
            exit={reduce ? undefined : { height: 0, opacity: 0 }}
            transition={{ duration: 0.2, ease: 'easeOut' }}
            style={{ overflow: 'hidden' }}
            className="bg-canvas-alt/60"
          >
            <div className="px-6 pb-3 pt-2">
              <div className="mb-1.5 text-[11px] font-medium text-ink-muted">
                {tl('columns_count', { count: row.columns.length })}
              </div>
              <div className="grid max-h-56 grid-cols-2 gap-x-6 gap-y-1 overflow-y-auto">
                {row.columns.map((col, ci) => (
                  <div key={ci} className="flex items-baseline gap-2 text-xs">
                    <span className="truncate text-ink">{col.name}</span>
                    <span className="font-mono text-[11px] text-ink-ghost">{col.dataType}</span>
                  </div>
                ))}
              </div>
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </motion.div>
  )
}

function InlineLoader({ text }: { text: string }) {
  const reduce = useReducedMotion()
  return (
    <div className="flex items-center gap-2 text-sm text-ink-muted">
      <motion.span
        animate={reduce ? undefined : { rotate: 360 }}
        transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
        className="inline-flex"
      >
        <RefreshCw size={14} aria-hidden="true" />
      </motion.span>
      <span>{text}</span>
    </div>
  )
}

function EmptyState() {
  const t = useTranslations('lakehouse')
  const industrial = useStyleMode().mode === 'industrial'
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
      <div className={industrial ? 'flex h-12 w-12 items-center justify-center border-2 border-ink' : 'flex h-12 w-12 items-center justify-center rounded-lg border border-border bg-canvas-alt'}>
        <Database size={20} className={industrial ? 'text-ink' : 'text-ink-ghost'} aria-hidden="true" />
      </div>
      {industrial && (
        <div className="font-mono text-[10px] tracking-[0.22em] text-ink-ghost">// NO TABLES</div>
      )}
      <div className="space-y-1">
        <div className={industrial ? 'font-mono text-sm font-bold uppercase tracking-[0.06em] text-ink' : 'text-sm font-medium text-ink'}>{t('empty_title')}</div>
        <div className="text-xs text-ink-ghost">
          {t('empty_hint_prefix')} <a href="/lakehouse/settings/data-sources" className="text-ink underline-offset-2 hover:underline">{t('empty_hint_link')}</a> {t('empty_hint_suffix')}
        </div>
      </div>
    </div>
  )
}
