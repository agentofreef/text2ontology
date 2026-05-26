'use client'

// 编辑指标（Metric, table lakehouse_metric）— two-pane editor, mirrors /new.
// GET /ontology/lakehouse-metrics/{id} returns the full record incl querySql +
// level, so we seed the form directly. Route uses ?id=xxx (not a dynamic [id]
// segment) because the project ships as a static export (output:'export');
// dynamic segments would need generateStaticParams() for arbitrary uuids.
// useSearchParams must live inside a <Suspense> boundary for the static export.

import { useState, useEffect, Suspense } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { useSearchParams } from 'next/navigation'
import { ChevronLeft, BarChart3, Loader2, Trash2, Save, FlaskConical } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { useFetch, useFetchSingle } from '@/lib/hooks'
import { useMessage } from '@/lib/message'
import { api } from '@/lib/api'
import type { OntMetric, OntObjectType } from '@/types/api'
import {
  MetricEditor, blankMetricEditorForm, validateMetricEditorForm, buildMetricEditorPayload,
  type MetricEditorForm,
} from '../_components/MetricEditor'
import { extractApiError } from '../_components/errors'

function EditMetricInner() {
  const t = useTranslations('metric.edit')
  const te = useTranslations('metric.editor')
  const router = useRouter()
  const searchParams = useSearchParams()
  const id = searchParams.get('id') || ''
  const msg = useMessage()

  const { data: target, loading: targetLoading } = useFetchSingle<OntMetric>(
    id ? `/ontology/lakehouse-metrics/${id}` : '/ontology/lakehouse-metrics/__none__',
  )
  const { data: objects } = useFetch<OntObjectType>('/ontology/objects')

  const [form, setForm] = useState<MetricEditorForm>(blankMetricEditorForm)
  const [sampleValues, setSampleValues] = useState<Record<string, string>>({})
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [hydrated, setHydrated] = useState(false)
  const [apiError, setApiError] = useState<string | null>(null)

  // Seed form from the loaded metric ONCE. Legacy level='plan' rows fall back
  // to the structured editor (we only author 'simple' | 'sql' here).
  useEffect(() => {
    if (!target || hydrated) return
    setForm({
      name: target.name,
      displayName: target.displayName || '',
      objectId: target.objectId,
      // Hydrate the multi-OD set from the GET response; fall back to [objectId]
      // for legacy rows that predate odIds (drop empties).
      odIds: (target.odIds && target.odIds.length > 0)
        ? target.odIds
        : (target.objectId ? [target.objectId] : []),
      description: target.description || '',
      level: target.level === 'sql' ? 'sql' : 'simple',
      canonicalMetric: target.canonicalMetric,
      querySql: target.querySql || '',
      canonicalFilters: target.canonicalFilters || [],
      autoGroupBy: target.autoGroupBy || [],
      replaceGroupBy: target.replaceGroupBy ?? false,
      defaultOrderByLabel: target.defaultOrderByLabel || '',
      defaultOrderByDir: target.defaultOrderByDir || '',
      defaultLimit: target.defaultLimit === null || target.defaultLimit === undefined ? '' : String(target.defaultLimit),
      pivotOn: target.pivotOn || '',
      pivotValues: target.pivotValues || [],
      pivotColumnLabels: target.pivotColumnLabels || [],
      pivotTotalLabel: target.pivotTotalLabel || 'Total',
      pivotPercentAxis: target.pivotPercentAxis || 'row',
      pivotPercentScope: target.pivotPercentScope || 'filtered',
      pivotPercentSuffix: target.pivotPercentSuffix || te('pivot_suffix_default'),
      pivotWithPercent: target.pivotWithPercent ?? false,
      pivotAppendGrandTotal: target.pivotAppendGrandTotal ?? false,
      parameters: target.parameters || [],
      triggerKeywords: target.triggerKeywords || [],
      responseTemplate: target.responseTemplate || '',
      priority: target.priority,
      mark: target.mark,
    })
    setHydrated(true)
  }, [target, hydrated, te])

  const validation = validateMetricEditorForm(form, te)
  const goBack = () => router.push('/ontology/lakehouse-metrics')

  const handleSubmit = async () => {
    if (!target) return
    if (validation) { msg.error(validation); return }
    setApiError(null)
    setSaving(true)
    try {
      await api(`/ontology/lakehouse-metrics/${target.id}`, {
        method: 'PUT',
        body: buildMetricEditorPayload(form),
      })
      msg.success(t('update_success'))
      goBack()
    } catch (e) {
      const detail = extractApiError(e, t)
      setApiError(detail)
      msg.error(detail)
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    if (!target) return
    if (!confirm(t('delete_confirm', { name: target.name }))) return
    setDeleting(true)
    try {
      await api(`/ontology/lakehouse-metrics/${target.id}`, { method: 'DELETE' })
      msg.success(t('delete_success'))
      goBack()
    } catch (e) {
      msg.error(t('delete_failed', { error: (e as Error).message }))
    } finally {
      setDeleting(false)
    }
  }

  if (targetLoading && !target) {
    return (
      <div className="flex h-full items-center justify-center bg-canvas">
        <span className="inline-flex items-center gap-2 text-sm text-ink-muted">
          <Loader2 size={14} className="animate-spin" aria-hidden="true" /> {t('loading')}
        </span>
      </div>
    )
  }

  if (!target) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 bg-canvas">
        <span className="text-sm text-ink-muted">{t('not_found')}</span>
        <button type="button" onClick={goBack}
          className="text-xs text-ink underline outline-none focus-visible:ring-1 focus-visible:ring-ink">
          {t('back_btn')}
        </button>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col bg-canvas">
      {/* Top bar */}
      <div className="flex flex-shrink-0 items-center justify-between border-b border-border bg-white px-6 py-3">
        <div className="flex min-w-0 items-center gap-3">
          <button type="button" onClick={goBack} aria-label={t('back_aria')}
            className="inline-flex items-center gap-1 text-xs text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink">
            <ChevronLeft size={12} aria-hidden="true" /> {t('back_btn')}
          </button>
          <span aria-hidden="true" className="text-ink-ghost">·</span>
          <BarChart3 size={16} className="text-ink flex-shrink-0" aria-hidden="true" />
          <h1 className="truncate font-display text-base font-bold tracking-tight text-ink">
            {t('page_title_prefix')}<span className="font-mono">{target.name}</span>
          </h1>
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
          <button type="button" onClick={() => router.push(`/ontology/lakehouse-metrics/simulate?id=${target.id}`)}
            title={t('open_simulator')}
            className="inline-flex h-8 items-center gap-1 border border-border px-2.5 text-xs text-ink-muted outline-none transition-colors hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink">
            <FlaskConical size={12} aria-hidden="true" /> {t('open_simulator')}
          </button>
          <button type="button" onClick={handleDelete} disabled={deleting || saving}
            aria-label={t('delete_aria')} title={t('delete_title')}
            className="inline-flex h-8 items-center gap-1 border border-border px-2.5 text-xs text-ink-muted outline-none transition-colors hover:border-danger hover:text-danger disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink">
            <Trash2 size={12} aria-hidden="true" /> {t('delete_btn')}
          </button>
          <Button variant="primary" size="sm" onClick={handleSubmit} disabled={!!validation || saving} title={validation || ''}>
            <Save size={14} /> {saving ? t('saving') : t('submit')}
          </Button>
        </div>
      </div>

      {apiError && (
        <div className="flex-shrink-0 border-b border-danger/30 bg-danger/5 px-6 py-2 text-sm text-danger">{apiError}</div>
      )}

      {/* Two-pane editor */}
      <MetricEditor
        form={form}
        setForm={setForm}
        objects={objects}
        sampleValues={sampleValues}
        setSampleValues={setSampleValues}
        t={te}
      />
    </div>
  )
}

export default function EditMetricPage() {
  return (
    <Suspense
      fallback={
        <div className="flex h-full items-center justify-center bg-canvas">
          <span className="inline-flex items-center gap-2 text-sm text-ink-muted">
            <Loader2 size={14} className="animate-spin" aria-hidden="true" /> …
          </span>
        </div>
      }
    >
      <EditMetricInner />
    </Suspense>
  )
}
