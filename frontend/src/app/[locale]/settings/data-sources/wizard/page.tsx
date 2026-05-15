'use client'

import { useTranslations } from 'next-intl'
import { Suspense } from 'react'
import { WizardClient } from '../components/wizard/WizardClient'

function WizardInner() {
  return <WizardClient />
}

export default function WizardPage() {
  const t = useTranslations('settings.ds.wizard')
  return (
    <Suspense fallback={<div className="flex h-full items-center justify-center"><span className="text-sm text-ink-ghost">{t('loading')}</span></div>}>
      <WizardInner />
    </Suspense>
  )
}
