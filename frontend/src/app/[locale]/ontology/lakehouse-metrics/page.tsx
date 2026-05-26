'use client'

// 指标 (Metric) list — the NEW unified lakehouse_metric table. Coexists with
// the lakehouse-metric-intents page (lakehouse_metric_intent); this is a fresh
// authoring surface, not a replacement. Mirrors the metric-intents list chrome
// (industrial header, DataTable, SegmentedFilter) but targets the new endpoint
// and drops the CSV / inline-trigger machinery (triggers are edited in-form).

import { useState, useMemo, useCallback } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { motion, useReducedMotion } from 'motion/react'
import { DataTable, Column } from '@/components/ui/DataTable'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { useFetch } from '@/lib/hooks'
import { useMessage } from '@/lib/message'
import { api } from '@/lib/api'
import { useStyleMode } from '@/lib/style-mode'
import type { OntMetric } from '@/types/api'
import {
  Plus, Pencil, Trash2, BarChart3, RefreshCw,
} from 'lucide-react'

type MarkFilter = 'all' | 'marked' | 'unmarked'

export default function LakehouseMetricsPage() {
  const industrial = useStyleMode().mode === 'industrial'
  const t = useTranslations('metric')
  const router = useRouter()
  const msg = useMessage()
  const reduce = useReducedMotion()

  const [markFilter, setMarkFilter] = useState<MarkFilter>('all')

  const { data: metrics, refetch, loading } = useFetch<OntMetric>('/ontology/lakehouse-metrics')

  const filtered = useMemo(() => {
    if (!metrics) return []
    if (markFilter === 'marked') return metrics.filter(m => m.mark)
    if (markFilter === 'unmarked') return metrics.filter(m => !m.mark)
    return metrics
  }, [metrics, markFilter])

  const markedCount = metrics?.filter(m => m.mark).length ?? 0
  const totalCount = metrics?.length ?? 0

  const handleEdit = useCallback((r: OntMetric) => {
    router.push(`/ontology/lakehouse-metrics/edit?id=${encodeURIComponent(r.id)}`)
  }, [router])

  const handleDelete = useCallback(async (r: OntMetric) => {
    if (!confirm(t('delete_confirm', { name: r.name }))) return
    try {
      await api(`/ontology/lakehouse-metrics/${r.id}`, { method: 'DELETE' })
      msg.success(t('deleted'))
      refetch()
    } catch (e) { msg.error(t('delete_failed', { error: (e as Error).message })) }
  }, [msg, refetch, t])

  const columns: Column<OntMetric>[] = useMemo(() => [
    {
      key: 'name', title: t('col_name'), sortable: true,
      render: (_, r) => (
        <div>
          <span className="font-sans text-sm font-semibold text-ink">{r.name}</span>
          {r.displayName && r.displayName !== r.name && (
            <span className="ml-2 text-xs text-ink-ghost">{r.displayName}</span>
          )}
        </div>
      ),
    },
    {
      key: 'objectName', title: t('col_od'), width: '120px',
      render: (_, r) => (
        <span className="inline-flex items-center border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] text-ink-muted font-mono">
          {r.objectName}
        </span>
      ),
    },
    {
      key: 'level', title: t('col_mode'), width: '70px',
      render: (_, r) => (
        <span
          className={`inline-flex items-center border px-1.5 py-0.5 text-[10px] font-mono uppercase tracking-wider ${
            r.level === 'sql' ? 'border-accent text-accent' : 'border-border bg-canvas-alt text-ink-muted'
          }`}
          title={r.level === 'sql' ? t('mode_sql_title') : t('mode_structured_title')}
        >
          {r.level === 'sql' ? t('badge_sql') : t('badge_structured')}
        </span>
      ),
    },
    {
      key: 'canonicalMetric', title: t('col_metric'),
      render: (_, r) => (
        <span className="font-mono text-xs font-semibold text-ink">
          {r.level === 'sql'
            ? <span className="text-ink-muted">{(r.querySql || '').trim().slice(0, 48) || '—'}{(r.querySql || '').trim().length > 48 ? '…' : ''}</span>
            : r.canonicalMetric}
        </span>
      ),
    },
    {
      key: 'parameters', title: t('col_params'),
      render: (_, r) => (
        <div className="flex flex-wrap gap-1">
          {(!r.parameters || r.parameters.length === 0) ? (
            <span className="text-xs text-ink-ghost">—</span>
          ) : (
            r.parameters.map((p, i) => (
              <span
                key={i}
                className={`border px-1.5 py-0.5 text-[11px] font-mono ${
                  p.optional === false ? 'border-ink bg-white text-ink' : 'border-border bg-canvas-alt text-ink-muted'
                }`}
                title={p.description || p.name}
              >
                {p.name}{p.optional === false ? '*' : ''}
              </span>
            ))
          )}
        </div>
      ),
    },
    {
      key: 'triggerKeywords', title: t('col_triggers'),
      render: (_, r) => (
        <div className="flex flex-wrap gap-1">
          {(!r.triggerKeywords || r.triggerKeywords.length === 0) ? (
            <span className="text-xs text-danger">{t('no_triggers')}</span>
          ) : (
            r.triggerKeywords.map((k, i) => (
              <span key={i} className="border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] text-ink-muted">{k}</span>
            ))
          )}
        </div>
      ),
    },
    {
      key: 'priority', title: t('col_priority'), width: '50px',
      render: (_, r) => <span className="text-xs text-ink-ghost tabular-nums">{r.priority}</span>,
    },
    {
      key: 'mark', title: t('col_mark'), width: '60px',
      render: (_, r) => (
        <span className={`inline-block h-2 w-2 rounded-full ${r.mark ? 'bg-success' : 'bg-border'}`} title={r.mark ? t('active') : t('inactive')} />
      ),
    },
    {
      key: 'actions', title: '', width: '80px',
      render: (_, r) => (
        <div className="flex gap-1">
          <motion.button
            onClick={() => handleEdit(r)}
            whileHover={reduce ? {} : { scale: 1.15 }}
            whileTap={reduce ? {} : { scale: 0.9 }}
            transition={{ duration: 0.12 }}
            aria-label={t('edit_aria')}
            className="p-1 text-ink-ghost hover:text-ink cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink"
          >
            <Pencil className="h-3 w-3" />
          </motion.button>
          <motion.button
            onClick={() => handleDelete(r)}
            whileHover={reduce ? {} : { scale: 1.15 }}
            whileTap={reduce ? {} : { scale: 0.9 }}
            transition={{ duration: 0.12 }}
            aria-label={t('delete_aria')}
            className="p-1 text-ink-ghost hover:text-danger cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink"
          >
            <Trash2 className="h-3 w-3" />
          </motion.button>
        </div>
      ),
    },
  ], [handleEdit, handleDelete, reduce, t])

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header — h-14 to align with Sidebar; industrial uses 2px ink rule */}
      <motion.header
        initial={reduce ? undefined : { opacity: 0, y: -4 }}
        animate={reduce ? undefined : { opacity: 1, y: 0 }}
        transition={{ duration: 0.2, ease: 'easeOut' }}
        className={`flex h-14 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'}`}
      >
        <div className="flex min-w-0 items-center gap-3">
          {industrial ? (
            <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // {t('page_title').toString().toUpperCase()}
            </span>
          ) : (
            <>
              <BarChart3 size={18} className="text-ink flex-shrink-0" aria-hidden="true" />
              <h1 className="text-base font-semibold tracking-tight text-ink whitespace-nowrap">
                {t('page_title')}
              </h1>
            </>
          )}
          <span className={industrial ? 'font-mono text-[10px] tracking-[0.14em] text-ink-muted truncate tabular-nums' : 'text-xs text-ink-ghost truncate'}>
            {industrial
              ? `${totalCount} TOTAL · ${markedCount} ACTIVE`
              : t('page_subtitle', { total: totalCount, marked: markedCount })}
          </span>
        </div>

        <div className="flex flex-shrink-0 flex-wrap items-center gap-2">
          <SegmentedFilter
            value={markFilter}
            onChange={setMarkFilter}
            options={[
              ['all', t('filter_all', { count: totalCount })],
              ['marked', t('filter_marked', { count: markedCount })],
              ['unmarked', t('filter_unmarked', { count: totalCount - markedCount })],
            ]}
          />
          <motion.button
            onClick={refetch}
            disabled={loading}
            whileHover={reduce || loading ? undefined : { scale: 1.05 }}
            whileTap={reduce || loading ? undefined : { scale: 0.95 }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            aria-label={t('refresh_aria')}
            title={t('refresh_title')}
            className={`inline-flex h-7 w-7 items-center justify-center bg-white text-ink-muted outline-none hover:border-ink hover:text-ink disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}
          >
            <motion.span
              animate={reduce ? undefined : loading ? { rotate: 360 } : { rotate: 0 }}
              transition={loading ? { repeat: Infinity, duration: 1, ease: 'linear' } : { duration: 0 }}
              className="inline-flex"
            >
              <RefreshCw size={12} aria-hidden="true" />
            </motion.span>
          </motion.button>
          <AnimatedButton
            variant="primary"
            size="sm"
            onClick={() => router.push('/ontology/lakehouse-metrics/new')}
            aria-label={t('new_aria')}
          >
            <Plus size={12} aria-hidden="true" />
            {t('new_btn')}
          </AnimatedButton>
        </div>
      </motion.header>

      {/* Content */}
      <div className="flex flex-1 min-h-0 flex-col">
        {loading && (!metrics || metrics.length === 0) ? (
          <div className="flex h-full items-center justify-center">
            <InlineLoader text={t('loading')} />
          </div>
        ) : (
          <div className="flex-1 min-h-0 overflow-y-auto">
            <DataTable
              columns={columns}
              data={filtered}
              rowKey="id"
              searchable
              searchPlaceholder={t('search_placeholder')}
            />
          </div>
        )}
      </div>
    </div>
  )
}

// ─── Sub-components ──────────────────────────────────────────────────────────

function SegmentedFilter<T extends string>({
  value, onChange, options,
}: {
  value: T
  onChange: (v: T) => void
  options: [T, string][]
}) {
  const reduce = useReducedMotion()
  const industrial = useStyleMode().mode === 'industrial'
  const [layoutId] = useState(() => `seg-${Math.random().toString(36).slice(2, 9)}`)
  return (
    <div
      role="radiogroup"
      className={`relative flex h-7 items-center p-0.5 ${industrial ? 'border border-ink bg-white' : 'rounded-md border border-border bg-canvas-alt'}`}
    >
      {options.map(([v, label]) => {
        const selected = value === v
        return (
          <button
            key={v}
            role="radio"
            aria-checked={selected}
            onClick={() => onChange(v)}
            className={`relative z-10 px-2.5 py-0.5 outline-none focus-visible:ring-1 focus-visible:ring-ink transition-colors ${
              industrial ? 'font-mono text-[10px] uppercase tracking-[0.14em]' : 'rounded-[5px] text-[11px] font-medium'
            }`}
          >
            {selected && (
              <motion.span
                layoutId={layoutId}
                className={`absolute inset-0 ${industrial ? 'bg-ink' : 'rounded-[5px] bg-white shadow-sm'}`}
                transition={reduce ? { duration: 0 } : { type: 'spring', stiffness: 500, damping: 35 }}
              />
            )}
            <span className={`relative ${selected ? (industrial ? 'text-white' : 'text-ink') : 'text-ink-muted hover:text-ink'}`}>
              {label}
            </span>
          </button>
        )
      })}
    </div>
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
