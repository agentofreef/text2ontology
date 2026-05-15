'use client'

// 编辑查询意图（Metric Intent）— mirrors the /new page layout. Loads the
// intent by id from the existing /metric-intents collection (no GET-by-id
// endpoint exists, so we filter the list — same pattern as the modal flow).
//
// Route uses ?id=xxx query string (not a dynamic [id] segment) because the
// project ships as a static export (next.config.js output:'export'), where
// dynamic segments would require generateStaticParams() — impossible for
// arbitrary user-created uuids.

import { useState, useEffect, useMemo, Suspense } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { useSearchParams } from 'next/navigation'
import { motion, useReducedMotion } from 'motion/react'
import { ChevronLeft, Sparkles, Loader2, Trash2 } from 'lucide-react'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { useFetch } from '@/lib/hooks'
import { useMessage } from '@/lib/message'
import { api } from '@/lib/api'
import type { OntMetricIntent, OntObjectType } from '@/types/api'
import {
  IntentFormFields, blankIntentForm, validateIntentForm, type IntentForm,
} from '../_components/IntentForm'

function EditMetricIntentInner() {
  const t = useTranslations('intent.edit')
  const router = useRouter()
  const searchParams = useSearchParams()
  const id = searchParams.get('id') || ''
  const msg = useMessage()
  const reduce = useReducedMotion()

  const { data: intents, loading: intentsLoading } = useFetch<OntMetricIntent>('/ontology/metric-intents')
  const { data: objects } = useFetch<OntObjectType>('/ontology/objects')

  const target = useMemo(() => intents.find(i => i.id === id), [intents, id])

  const [form, setForm] = useState<IntentForm>(blankIntentForm)
  const [gbInput, setGbInput] = useState('')
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [hydrated, setHydrated] = useState(false)

  // Seed form from the loaded intent ONCE (additional refetches won't clobber
  // user edits in flight).
  useEffect(() => {
    if (!target || hydrated) return
    setForm({
      name: target.name,
      displayName: target.displayName,
      objectId: target.objectId,
      canonicalMetric: target.canonicalMetric,
      canonicalFilters: target.canonicalFilters || [],
      autoGroupBy: target.autoGroupBy || [],
      pivotOn: target.pivotOn || '',
      pivotValues: target.pivotValues || [],
      pivotColumnLabels: target.pivotColumnLabels || [],
      pivotTotalLabel: target.pivotTotalLabel || 'Total',
      pivotPercentAxis: target.pivotPercentAxis || 'row',
      pivotPercentScope: target.pivotPercentScope || 'filtered',
      pivotPercentSuffix: target.pivotPercentSuffix || t('pivot_percent_suffix_default'),
      pivotWithPercent: target.pivotWithPercent ?? false,
      pivotAppendGrandTotal: target.pivotAppendGrandTotal ?? false,
      responseTemplate: target.responseTemplate,
      description: target.description,
      priority: target.priority,
      mark: target.mark,
    })
    setHydrated(true)
  }, [target, hydrated])

  const validation = validateIntentForm(form)
  const goBack = () => router.push('/ontology/lakehouse-metric-intents')

  const handleSubmit = async () => {
    if (!target) return
    const err = validation
    if (err) { msg.error(err); return }
    setSaving(true)
    try {
      await api(`/ontology/metric-intents/${target.id}`, { method: 'PUT', body: form })
      msg.success(t('update_success'))
      goBack()
    } catch (e) {
      msg.error(t('update_failed', { error: (e as Error).message }))
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    if (!target) return
    if (!confirm(t('delete_confirm', { name: target.name }))) return
    setDeleting(true)
    try {
      await api(`/ontology/metric-intents/${target.id}`, { method: 'DELETE' })
      msg.success(t('delete_success'))
      goBack()
    } catch (e) {
      msg.error(t('delete_failed', { error: (e as Error).message }))
    } finally {
      setDeleting(false)
    }
  }

  // ── Loading state — wait for the list to come back before deciding 404 ─
  if (intentsLoading && !target) {
    return (
      <div className="flex h-full items-center justify-center bg-canvas">
        <span className="inline-flex items-center gap-2 text-sm text-ink-muted">
          <Loader2 size={14} className="animate-spin" aria-hidden="true" />
          {t('loading')}
        </span>
      </div>
    )
  }

  if (!target) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 bg-canvas">
        <span className="text-sm text-ink-muted">{t('not_found')}</span>
        <button
          type="button"
          onClick={goBack}
          className="text-xs text-ink underline outline-none focus-visible:ring-1 focus-visible:ring-ink"
        >
          {t('back_btn')}
        </button>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header */}
      <motion.header
        initial={reduce ? undefined : { opacity: 0, y: -4 }}
        animate={reduce ? undefined : { opacity: 1, y: 0 }}
        transition={{ duration: 0.2, ease: 'easeOut' }}
        className="flex flex-shrink-0 items-center justify-between gap-3 border-b border-border bg-white px-6 py-3 shadow-sm"
      >
        <div className="flex min-w-0 items-center gap-3">
          <button
            type="button"
            onClick={goBack}
            className="inline-flex items-center gap-1 text-xs text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
            aria-label={t('back_aria')}
          >
            <ChevronLeft size={12} aria-hidden="true" />
            {t('back_btn')}
          </button>
          <span aria-hidden="true" className="text-ink-ghost">·</span>
          <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
            <Sparkles size={14} className="text-ink" aria-hidden="true" />
          </div>
          <div className="min-w-0">
            <h1 className="truncate text-base font-semibold tracking-tight text-ink">
              {t('page_title_prefix')}<span className="font-mono">{target.name}</span>
            </h1>
            <p className="truncate text-xs text-ink-muted">
              {target.mark ? t('status_enabled') : t('status_disabled')}
              {target.priority !== 0 && ` · priority=${target.priority}`}
            </p>
          </div>
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={handleDelete}
            disabled={deleting || saving}
            className="inline-flex h-8 items-center gap-1 rounded-md border border-border px-2.5 text-xs text-ink-muted outline-none transition-colors hover:border-danger hover:text-danger disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink"
            aria-label={t('delete_aria')}
            title={t('delete_title')}
          >
            <Trash2 size={12} aria-hidden="true" />
            {t('delete_btn')}
          </button>
          <AnimatedButton variant="ghost" size="sm" onClick={goBack} disabled={saving}>
            {t('cancel')}
          </AnimatedButton>
          <AnimatedButton
            variant="primary"
            size="sm"
            onClick={handleSubmit}
            disabled={!!validation || saving}
            title={validation || ''}
          >
            {saving ? t('saving') : t('submit')}
          </AnimatedButton>
        </div>
      </motion.header>

      {/* Form body */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto max-w-3xl px-6 py-6">
          <IntentFormFields
            form={form}
            setForm={setForm}
            objects={objects}
            gbInput={gbInput}
            setGbInput={setGbInput}
          />
        </div>
      </div>

      {/* Footer */}
      <div className="flex flex-shrink-0 items-center justify-between border-t border-border bg-canvas-alt px-6 py-2.5">
        <span className="text-[11px] text-ink-ghost">
          {validation
            ? <>{t('footer_missing')}<span className="text-ink">{validation}</span></>
            : t('footer_ready')}
        </span>
        <div className="flex items-center gap-2">
          <AnimatedButton variant="ghost" size="sm" onClick={goBack} disabled={saving}>
            {t('cancel')}
          </AnimatedButton>
          <AnimatedButton
            variant="primary"
            size="sm"
            onClick={handleSubmit}
            disabled={!!validation || saving}
          >
            {saving ? t('saving') : t('submit')}
          </AnimatedButton>
        </div>
      </div>
    </div>
  )
}

// Next.js requires useSearchParams to be inside a Suspense boundary so the
// initial static HTML can render while the hook resolves on the client.
export default function EditMetricIntentPage() {
  return (
    <Suspense
      fallback={
        <div className="flex h-full items-center justify-center bg-canvas">
          <span className="inline-flex items-center gap-2 text-sm text-ink-muted">
            <Loader2 size={14} className="animate-spin" aria-hidden="true" />
            …
          </span>
        </div>
      }
    >
      <EditMetricIntentInner />
    </Suspense>
  )
}
