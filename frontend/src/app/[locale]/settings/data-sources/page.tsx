'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback } from 'react'
import { useRouter } from '@/i18n/navigation'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { Database, Plus, ChevronRight, RefreshCw } from 'lucide-react'
import { StatusBadge, type DataSourceStatus } from './components/StatusBadge'
import { TypeBadge } from './components/TypeBadge'
import { Button } from '@/components/ui/Button'
import { getApiBase } from '@/lib/api'

interface DataSource {
  id: string
  project_id: string
  type: string
  label: string
  status: DataSourceStatus
  config_json: string
  staging_schema: string
  last_sync_at: string | null
  created_at: string
  updated_at: string
}

// formatRelative is used inside SourceCard which has access to t()
// We'll handle the time labels via t() in the SourceCard component instead.
// Keep this function for the numeric computation, returning raw values.
function formatRelativeParts(iso: string | null): { key: string; count?: number } | null {
  if (!iso) return null
  const d = new Date(iso)
  const now = new Date()
  const diff = (now.getTime() - d.getTime()) / 1000
  if (diff < 60) return { key: 'just_now' }
  if (diff < 3600) return { key: 'minutes_ago', count: Math.floor(diff / 60) }
  if (diff < 86400) return { key: 'hours_ago', count: Math.floor(diff / 3600) }
  return { key: 'days_ago', count: Math.floor(diff / 86400) }
}

export default function DataSourcesPage() {
  const t = useTranslations('settings.ds')
  const industrial = useStyleMode().mode === 'industrial'
  const router = useRouter()
  const { currentProject } = useProject()
  const [sources, setSources] = useState<DataSource[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const loadSources = useCallback(async () => {
    if (!currentProject) return
    setLoading(true)
    setError('')
    try {
      const token = typeof window !== 'undefined'
        ? localStorage.getItem('lakehouse2ontology_token') : null
      const res = await fetch(
        `${getApiBase()}/connector/sources?project_id=${currentProject.id}`,
        { headers: token ? { Authorization: `Bearer ${token}` } : {} }
      )
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data: DataSource[] = await res.json()
      setSources(Array.isArray(data) ? data : [])
    } catch (e) {
      setError(e instanceof Error ? e.message : t('load_failed'))
    } finally {
      setLoading(false)
    }
  }, [currentProject])

  useEffect(() => { loadSources() }, [loadSources])

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header */}
      <div className={`flex h-14 items-center justify-between px-6 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
        <div className="flex items-center gap-3">
          {industrial ? (
            <>
              <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
                // DATA SOURCES
              </span>
              {!loading && (
                <span className="font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-muted">
                  {sources.length > 0 ? `${sources.length} SOURCES` : '// EMPTY'}
                </span>
              )}
            </>
          ) : (
            <>
              <Database className="h-5 w-5 text-ink-ghost" />
              <div>
                <h1 className="text-base font-semibold text-ink">{t('page_title')}</h1>
                {!loading && (
                  <p className="text-xs text-ink-ghost">
                    {sources.length > 0 ? t('source_count', { count: sources.length }) : t('no_sources')}
                  </p>
                )}
              </div>
            </>
          )}
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={loadSources}
            className={`flex items-center justify-center bg-white p-2 text-ink-ghost transition-colors duration-150 hover:border-ink hover:text-ink ${
              industrial ? 'border border-ink' : 'rounded-md border border-border'
            }`}
            title={t('refresh')}
          >
            <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
          </button>
          <Button
            variant="primary"
            size="sm"
            onClick={() => router.push('/settings/data-sources/add')}
          >
            <Plus className="h-3.5 w-3.5" />
            {t('add_source')}
          </Button>
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-6">
        {loading && (
          <div className="flex items-center justify-center py-16">
            <span className="text-sm text-ink-ghost">{t('loading')}</span>
          </div>
        )}

        {!loading && error && (
          <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
            {error}
          </div>
        )}

        {!loading && !error && sources.length === 0 && (
          <div className="flex flex-col items-center justify-center py-20 text-center">
            <Database className="mb-4 h-10 w-10 text-ink-ghost opacity-40" />
            <p className="mb-1 text-sm font-medium text-ink-muted">{t('empty_title')}</p>
            <p className="mb-6 text-xs text-ink-ghost">
              {t('empty_hint')}
            </p>
            <Button
              variant="default"
              size="sm"
              onClick={() => router.push('/settings/data-sources/add')}
            >
              <Plus className="h-3.5 w-3.5" />
              {t('add_first_source')}
            </Button>
          </div>
        )}

        {!loading && !error && sources.length > 0 && (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
            {sources.map((source) => (
              <SourceCard
                key={source.id}
                source={source}
                onClick={() => router.push(`/settings/data-sources/wizard?id=${source.id}`)}
                onRefresh={loadSources}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function SourceCard({
  source,
  onClick,
  onRefresh,
}: {
  source: DataSource
  onClick: () => void
  onRefresh: () => void
}) {
  const t = useTranslations('settings.ds')
  const industrial = useStyleMode().mode === 'industrial'
  const isResumable = source.status === 'failed_resumable'

  return (
    <div
      className={`group relative cursor-pointer bg-white transition-colors duration-150 ${
        industrial ? 'border border-ink hover:bg-canvas-alt' : 'rounded-lg border border-border hover:border-gray-400'
      }`}
      onClick={onClick}
    >
      <div className="p-4">
        {/* Top row: label + chevron */}
        <div className="mb-3 flex items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            {industrial && (
              <div className="mb-1 font-mono text-[9px] uppercase tracking-[0.22em] text-ink-ghost">
                // SOURCE
              </div>
            )}
            <p className={`truncate font-medium text-ink ${
              industrial ? 'font-mono text-[12px] tracking-[0.04em]' : 'text-sm'
            }`}>{source.label}</p>
          </div>
          <ChevronRight className="h-4 w-4 flex-shrink-0 text-ink-ghost transition-colors duration-150 group-hover:text-ink" />
        </div>

        {/* Badges */}
        <div className="mb-4 flex flex-wrap items-center gap-1.5">
          <TypeBadge type={source.type} />
          <StatusBadge status={source.status} />
        </div>

        {/* Footer: last sync */}
        <div className={`flex items-center justify-between text-ink-ghost ${
          industrial ? 'font-mono text-[10px] uppercase tracking-[0.12em]' : 'text-[11px]'
        }`}>
          <span>{t('last_sync')}</span>
          <span className="tabular-nums">{(() => { const p = formatRelativeParts(source.last_sync_at); if (!p) return '—'; return p.count != null ? t(p.key, { count: p.count }) : t(p.key) })()}</span>
        </div>

        {/* Resumable CTA */}
        {isResumable && (
          <div
            className={`mt-3 flex items-center gap-2 pt-3 ${industrial ? 'border-t border-ink' : 'border-t border-border'}`}
            onClick={(e) => e.stopPropagation()}
          >
            <button
              className={`flex-1 bg-white px-2 py-1 font-medium text-ink transition-colors duration-150 hover:border-ink ${
                industrial
                  ? 'border border-ink font-mono text-[10px] uppercase tracking-[0.14em]'
                  : 'rounded border border-border text-[11px]'
              }`}
              onClick={onClick}
            >
              {t('resume')}
            </button>
            <button
              className={`flex-1 px-2 py-1 font-medium transition-colors duration-150 ${
                industrial
                  ? 'border border-danger bg-white text-danger font-mono text-[10px] uppercase tracking-[0.14em] hover:bg-danger/5'
                  : 'rounded border border-red-200 bg-red-50 text-red-700 text-[11px] hover:bg-red-100'
              }`}
              onClick={async () => {
                if (!confirm(t('abandon_confirm'))) return
                try {
                  const token = typeof window !== 'undefined'
                    ? localStorage.getItem('lakehouse2ontology_token') : null
                  await fetch(`${getApiBase()}/connector/sources/${source.id}`, {
                    method: 'DELETE',
                    headers: token ? { Authorization: `Bearer ${token}` } : {},
                  })
                  onRefresh()
                } catch {/* ignore */}
              }}
            >
              {t('abandon')}
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
