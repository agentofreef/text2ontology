'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback } from 'react'
import { useRouter } from '@/i18n/navigation'
import { useSearchParams } from 'next/navigation'
import { useProject } from '@/lib/project'
import { useMessage } from '@/lib/message'
import { Button } from '@/components/ui/Button'
import { ChevronLeft, Database, FileSpreadsheet, BarChart3, AlertCircle, Loader2, CheckCircle2 } from 'lucide-react'
import { getApiBase } from '@/lib/api'

// ─── Types ────────────────────────────────────────────────────────────────────

interface CatalogTable {
  name: string
  columns: { name: string; data_type?: string }[]
}

interface DetectedLink {
  from_table: string
  from_column: string
  to_table: string
  to_column: string
}

interface DataSourceMeta {
  id: string
  type: string
  label: string
  status: string
  config_json?: string
  created_at?: string
}

const TYPE_META_ICONS: Record<string, { icon: typeof Database }> = {
  postgres: { icon: Database        },
  pbi:      { icon: BarChart3       },
  file:     { icon: FileSpreadsheet },
}

// Format bytes to a short human-readable string. Used for file-type config display.
function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '-'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

// parseConfig turns the raw config_json blob into a clean [label, value] list
// for display. Per source type it picks the most useful fields and skips noise
// (and never shows password / api keys, which the API masks anyway).
// Labels are i18n keys resolved by the caller via t().
function parseConfig(
  src: { type: string; config_json?: string; created_at?: string },
  tFn: (key: string) => string,
): [string, string][] {
  const out: [string, string][] = []
  let cfg: Record<string, unknown> = {}
  try {
    cfg = JSON.parse(src.config_json || '{}') as Record<string, unknown>
  } catch { /* fall through with empty cfg */ }

  const srcType = String(src.type || '').toLowerCase()
  const get = (k: string) => (cfg[k] == null ? '' : String(cfg[k]))

  if (srcType === 'file') {
    if (get('filename')) out.push([tFn('cfg_filename'), get('filename')])
    if (get('ext'))      out.push([tFn('cfg_filetype'), get('ext')])
    if (cfg.size != null && Number(cfg.size) > 0) out.push([tFn('cfg_filesize'), formatBytes(Number(cfg.size))])
    if (get('source_url')) out.push([tFn('cfg_source_url'), get('source_url')])
  } else if (srcType === 'postgres') {
    if (get('host'))     out.push([tFn('cfg_host'), get('host')])
    if (cfg.port)        out.push([tFn('cfg_port'), String(cfg.port)])
    if (get('database')) out.push([tFn('cfg_database'), get('database')])
    if (get('user'))     out.push([tFn('cfg_user'), get('user')])
    if (get('schema'))   out.push(['schema', get('schema')])
  } else if (srcType === 'pbi') {
    if (get('filename'))    out.push([tFn('cfg_filename'), get('filename')])
    if (cfg.table_count)    out.push([tFn('cfg_table_count'), String(cfg.table_count)])
    if (cfg.measure_count)  out.push([tFn('cfg_measure_count'), String(cfg.measure_count)])
  }

  if (src.created_at) {
    const d = new Date(src.created_at)
    if (!Number.isNaN(d.getTime())) {
      out.push([tFn('cfg_created_at'), d.toLocaleString('zh-CN', { hour12: false })])
    }
  }

  return out.length > 0 ? out : [[tFn('cfg_raw'), src.config_json || tFn('cfg_empty')]]
}

// ─── Component ────────────────────────────────────────────────────────────────

export function WizardClient() {
  const t = useTranslations('wizard')
  const router = useRouter()
  const searchParams = useSearchParams()
  const dsId = searchParams.get('id') ?? ''
  const { currentProject } = useProject()
  const msg = useMessage()

  const [source, setSource] = useState<DataSourceMeta | null>(null)
  const [catalog, setCatalog] = useState<CatalogTable[]>([])
  const [detectedLinks, setDetectedLinks] = useState<DetectedLink[]>([])
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  // suppress unused-warning while keeping useProject side effects
  void currentProject

  const authHeaders = useCallback((): Record<string, string> => {
    const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
    return {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    }
  }, [])

  const load = useCallback(async () => {
    if (!dsId) {
      setLoading(false)
      setError(t('missing_id'))
      return
    }
    setLoading(true)
    setError('')
    try {
      // 1. data_source 元数据 → 决定走哪个 catalog endpoint
      const srcRes = await fetch(`${getApiBase()}/connector/sources/${dsId}`, { headers: authHeaders() })
      if (!srcRes.ok) {
        throw new Error(t('load_source_failed', { status: srcRes.status }))
      }
      const src = (await srcRes.json()) as DataSourceMeta
      setSource(src)

      // 2. catalog (按 type 分发)
      const srcType = String(src.type || '').toLowerCase()
      const catalogPath =
        srcType === 'file'     ? `/connector/file/sources/${dsId}/catalog` :
        srcType === 'pbi'      ? `/connector/pbit/sources/${dsId}/catalog` :
        srcType === 'postgres' ? `/connector/postgres/sources/${dsId}/catalog` :
        srcType === 'sqlite'   ? `/connector/sqlite/sources/${dsId}/catalog` :
        `/connector/postgres/sources/${dsId}/catalog`

      const cRes = await fetch(`${getApiBase()}${catalogPath}`, { headers: authHeaders() })
      if (cRes.ok) {
        const data = await cRes.json()
        const tables: CatalogTable[] = data.tables ?? (Array.isArray(data) ? data : [])
        setCatalog(tables)
        if (Array.isArray(data.detected_links)) setDetectedLinks(data.detected_links)
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : t('load_failed'))
    } finally {
      setLoading(false)
    }
  }, [dsId, authHeaders])

  useEffect(() => { load() }, [load])

  // While the source is in 'syncing' (collector-server is COPY-ing rows in
  // background after upload), poll its status every 3s. As soon as the job
  // finishes (status flips to 'ready'), the confirm button enables itself.
  useEffect(() => {
    if (source?.status !== 'syncing' || !dsId) return
    const pollTimer = setInterval(async () => {
      try {
        const res = await fetch(`${getApiBase()}/connector/sources/${dsId}`, { headers: authHeaders() })
        if (!res.ok) return
        const src = (await res.json()) as DataSourceMeta
        setSource(src)
      } catch {
        /* swallow — drawer is the user-visible progress channel */
      }
    }, 3000)
    return () => clearInterval(pollTimer)
  }, [source?.status, dsId, authHeaders])

  // The wizard has no role-picker UI — every detected table imports as a
  // dim with all columns kept as attributes. The backend /confirm endpoint
  // still requires `table_roles` + `column_roles`, so we synthesise sensible
  // defaults at submit time. Users who need fine-grained control can edit
  // the resulting Ods on the Object Types page after import.
  const handleConfirm = async () => {
    if (!dsId || catalog.length === 0) return
    setSubmitting(true)
    setError('')
    try {
      const tableRoles: Record<string, string> = {}
      const columnRoles: Record<string, Record<string, string>> = {}
      for (const tbl of catalog) {
        tableRoles[tbl.name] = 'dim'
        columnRoles[tbl.name] = {}
        for (const c of tbl.columns ?? []) {
          columnRoles[tbl.name][c.name] = 'attribute'
        }
      }
      // Backend expects []{from_table, from_column, to_table, to_column, create}
      // (see pkg/contracts/connector.go LinkDecision). A map was silently failing
      // JSON decode → wizard_state stayed NULL → ontology was never populated.
      const linkDecisions = detectedLinks.map((l) => ({
        from_table:  l.from_table,
        from_column: l.from_column,
        to_table:    l.to_table,
        to_column:   l.to_column,
        create:      true,
      }))

      const res = await fetch(`${getApiBase()}/connector/wizard/${dsId}/confirm`, {
        method: 'POST',
        headers: authHeaders(),
        body: JSON.stringify({ table_roles: tableRoles, column_roles: columnRoles, link_decisions: linkDecisions }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(
          (data as { error?: string; message?: string }).error ||
            (data as { message?: string }).message ||
            `HTTP ${res.status}`,
        )
      }
      msg.success(t('import_success', { count: catalog.length }))
      router.push('/settings/data-sources')
    } catch (e) {
      const errMsg = e instanceof Error ? e.message : t('import_failed')
      setError(errMsg)
      msg.error(errMsg)
    } finally {
      setSubmitting(false)
    }
  }

  // ─── Render ─────────────────────────────────────────────────────────────────

  if (!dsId) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 text-sm text-ink-muted">
        <AlertCircle size={20} className="text-ink-ghost" aria-hidden="true" />
        {t('missing_id')}
      </div>
    )
  }

  if (loading) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 text-sm text-ink-muted">
        <Loader2 size={20} className="animate-spin text-ink-ghost" aria-hidden="true" />
        {t('loading')}
      </div>
    )
  }

  const typeKey = String(source?.type || '').toLowerCase()
  const metaIcon = TYPE_META_ICONS[typeKey] ?? { icon: Database }
  const Icon = metaIcon.icon
  const typeLabel = typeKey === 'postgres' ? t('type_postgres') : typeKey === 'pbi' ? t('type_pbi') : typeKey === 'file' ? t('type_file') : source?.type || t('type_unknown')
  const totalColumns = catalog.reduce((n, tbl) => n + (tbl.columns?.length || 0), 0)

  return (
    <div className="mx-auto flex h-full w-full max-w-3xl flex-col gap-6 px-6 py-8">
      {/* Header */}
      <div>
        <button
          type="button"
          onClick={() => router.push('/settings/data-sources')}
          className="mb-3 inline-flex items-center gap-1 text-xs text-ink-ghost outline-none hover:text-ink"
        >
          <ChevronLeft size={12} aria-hidden="true" />
          {t('back_to_list')}
        </button>
        <h1 className="text-lg font-semibold text-ink">{t('confirm_import_title')}</h1>
        <p className="mt-1 text-sm text-ink-muted">
          {t('confirm_import_hint')}
        </p>
      </div>

      {/* Source meta card */}
      {source && (
        <div className="rounded-lg border border-border bg-white">
          <div className="flex items-start gap-3 border-b border-border-light px-4 py-3">
            <div className="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
              <Icon className="h-4 w-4 text-ink-muted" aria-hidden="true" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="truncate text-sm font-medium text-ink">{source.label}</span>
                <span className="rounded border border-border bg-canvas-alt px-1.5 py-0.5 font-mono text-[10px] text-ink-muted">
                  {typeLabel}
                </span>
                <span className="rounded border border-border-light bg-canvas-alt px-1.5 py-0.5 font-mono text-[10px] text-ink-ghost">
                  {source.status}
                </span>
              </div>
            </div>
          </div>
          <dl className="grid grid-cols-2 gap-x-4 gap-y-2 px-4 py-3 text-xs">
            {parseConfig(source, t).map(([label, value]) => (
              <div key={label} className="flex items-center justify-between gap-2 border-b border-border-light/60 pb-1.5 last:border-0 last:pb-0">
                <dt className="text-ink-muted">{label}</dt>
                <dd className="truncate font-mono text-ink" title={value}>{value}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}

      {/* Tables list */}
      <div className="flex min-h-0 flex-1 flex-col rounded-lg border border-border bg-white">
        <div className="flex flex-shrink-0 items-center justify-between border-b border-border-light px-4 py-2.5">
          <span className="text-sm font-medium text-ink">{t('detected_tables')}</span>
          <span className="font-mono text-xs tabular-nums text-ink-ghost">
            {t('table_col_count', { tables: catalog.length, columns: totalColumns })}
          </span>
        </div>
        {catalog.length === 0 ? (
          <div className="flex flex-1 items-center justify-center px-6 py-12 text-sm text-ink-ghost">
            {t('no_tables_detected')}
          </div>
        ) : (
          <ul className="flex-1 divide-y divide-border-light overflow-y-auto">
            {catalog.map((tbl) => (
              <li key={tbl.name} className="flex items-center justify-between px-4 py-2.5 hover:bg-canvas-alt">
                <span className="truncate font-mono text-sm text-ink">{tbl.name}</span>
                <span className="font-mono text-[11px] tabular-nums text-ink-ghost">
                  {t('col_count', { count: tbl.columns?.length || 0 })}
                </span>
              </li>
            ))}
          </ul>
        )}
        {detectedLinks.length > 0 && (
          <div className="flex flex-shrink-0 items-center justify-between border-t border-border-light bg-canvas-alt px-4 py-2 text-xs text-ink-ghost">
            <span>{t('detected_links', { count: detectedLinks.length })}</span>
          </div>
        )}
      </div>

      {/* Error / footer */}
      {error && (
        <div className="rounded-md border border-danger/30 bg-danger/5 p-3 text-sm text-danger">{error}</div>
      )}

      {source?.status === 'completed' ? (
        <div className="flex flex-shrink-0 items-center justify-between gap-3 rounded-md border border-success/30 bg-success/5 px-3 py-3">
          <div className="flex items-center gap-2 text-sm text-success">
            <CheckCircle2 size={16} aria-hidden="true" />
            {t('already_imported')}
          </div>
          <Button variant="primary" onClick={() => router.push('/settings/data-sources')}>
            {t('back_to_list')}
          </Button>
        </div>
      ) : (
        <div className="flex flex-shrink-0 items-center justify-end gap-2 border-t border-border-light pt-4">
          <Button
            variant="ghost"
            onClick={() => router.push('/settings/data-sources')}
            disabled={submitting}
          >
            {t('cancel')}
          </Button>
          <Button
            variant="primary"
            onClick={handleConfirm}
            disabled={
              submitting || catalog.length === 0 || source?.status === 'syncing'
            }
            title={
              source?.status === 'syncing'
                ? t('syncing_tooltip')
                : undefined
            }
          >
            {submitting
              ? t('importing')
              : source?.status === 'syncing'
                ? t('syncing_btn')
                : t('import_tables', { count: catalog.length })}
          </Button>
        </div>
      )}
    </div>
  )
}
