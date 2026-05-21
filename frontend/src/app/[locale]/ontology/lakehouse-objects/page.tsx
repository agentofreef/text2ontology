'use client'

import { useTranslations } from 'next-intl'
import { useProject } from '@/lib/project'
import { ObjectListView } from './components/ObjectListView'

// lakehouse-objects is the ontology configuration page (OD list + editing). The
// data-source architecture view now lives on the /ontology/lakehouse page; this
// page is purely the original object/ontology config surface.
export default function LakehouseObjectsPage() {
  const t = useTranslations('objects')
  const { currentProject } = useProject()

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
        <div className="text-sm text-ink-muted">{t('no_project_message')}</div>
        <div className="text-xs text-ink-ghost">{t('no_project_hint')}</div>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      <ObjectListView />
    </div>
  )
}
