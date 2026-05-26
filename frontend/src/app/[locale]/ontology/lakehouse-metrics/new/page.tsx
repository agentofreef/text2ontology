'use client'

// 新建口径（Metric, table lakehouse_metric）— two-pane editor. Mirrors the
// object-detail page chrome: a top bar (back + title + save) over a full-width
// two-pane MetricEditor. A metric is a parameterized SQL = a virtual table,
// authored in 结构化 (structured) or SQL mode (the toggle lives in the editor's
// right pane and drives form.level).

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { ChevronLeft, BarChart3, Save } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { useFetch } from '@/lib/hooks'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { api } from '@/lib/api'
import type { OntObjectType } from '@/types/api'
import {
  MetricEditor, blankMetricEditorForm, validateMetricEditorForm, buildMetricEditorPayload,
  type MetricEditorForm,
} from '../_components/MetricEditor'
import { extractApiError } from '../_components/errors'

export default function NewMetricPage() {
  const t = useTranslations('metric.new')
  const te = useTranslations('metric.editor')
  const router = useRouter()
  const msg = useMessage()
  const { currentProject } = useProject()

  const { data: objects } = useFetch<OntObjectType>('/ontology/objects')

  const [form, setForm] = useState<MetricEditorForm>(blankMetricEditorForm)
  const [sampleValues, setSampleValues] = useState<Record<string, string>>({})
  const [saving, setSaving] = useState(false)
  const [apiError, setApiError] = useState<string | null>(null)

  const validation = validateMetricEditorForm(form, te)
  const goBack = () => router.push('/ontology/lakehouse-metrics')

  const handleSubmit = async () => {
    if (validation) { msg.error(validation); return }
    setApiError(null)
    setSaving(true)
    try {
      await api(`/ontology/lakehouse-metrics?projectId=${currentProject?.id}`, {
        method: 'POST',
        body: { ...buildMetricEditorPayload(form), projectId: currentProject?.id },
      })
      msg.success(t('create_success'))
      goBack()
    } catch (e) {
      const detail = extractApiError(e, t)
      setApiError(detail)
      msg.error(detail)
    } finally {
      setSaving(false)
    }
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
          <h1 className="font-display text-base font-bold tracking-tight text-ink">{t('page_title')}</h1>
        </div>
        <Button variant="primary" size="sm" onClick={handleSubmit} disabled={!!validation || saving} title={validation || ''}>
          <Save size={14} /> {saving ? t('creating') : t('submit')}
        </Button>
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
