'use client'

// 新建指标（Metric）— full-page form, replaces the old modal flow.
// Mirrors the SV Minimal layout convention: sticky header bar with back +
// title + actions, scrollable body that owns its viewport (no <main> padding —
// path is registered in app/layout.tsx fullHeightExactPaths).

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { motion, useReducedMotion } from 'motion/react'
import { ChevronLeft, Sparkles } from 'lucide-react'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { useFetch } from '@/lib/hooks'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { api } from '@/lib/api'
import type { OntObjectType } from '@/types/api'
import {
  IntentFormFields, blankIntentForm, validateIntentForm, type IntentForm,
} from '../_components/IntentForm'

export default function NewMetricIntentPage() {
  const t = useTranslations('intent.new')
  const router = useRouter()
  const msg = useMessage()
  const reduce = useReducedMotion()
  const { currentProject } = useProject()

  const { data: objects } = useFetch<OntObjectType>('/ontology/objects')

  const [form, setForm] = useState<IntentForm>(blankIntentForm)
  const [gbInput, setGbInput] = useState('')
  const [saving, setSaving] = useState(false)

  const validation = validateIntentForm(form)

  const goBack = () => router.push('/ontology/lakehouse-metric-intents')

  const handleSubmit = async () => {
    const err = validation
    if (err) { msg.error(err); return }
    setSaving(true)
    try {
      await api(`/ontology/metric-intents?projectId=${currentProject?.id}`, {
        method: 'POST',
        body: { ...form, projectId: currentProject?.id },
      })
      msg.success(t('create_success'))
      goBack()
    } catch (e) {
      msg.error(t('create_failed', { error: (e as Error).message }))
    } finally {
      setSaving(false)
    }
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
            <h1 className="text-base font-semibold tracking-tight text-ink">{t('page_title')}</h1>
            <p className="truncate text-xs text-ink-muted">
              {t('page_subtitle')}
            </p>
          </div>
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
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
            {saving ? t('creating') : t('submit')}
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

      {/* Footer (sticky action bar — repeats top buttons for long forms) */}
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
            {saving ? t('creating') : t('submit')}
          </AnimatedButton>
        </div>
      </div>
    </div>
  )
}
