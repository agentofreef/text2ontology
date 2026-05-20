'use client'

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { motion, useReducedMotion } from 'motion/react'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { Database } from 'lucide-react'
import { ObjectListView } from './components/ObjectListView'
import { DataArchitectureView } from './components/DataArchitectureView'
import { SourceErView } from './components/SourceErView'
import type { SourceGroupData } from './components/SourceNode'

type ViewMode = 'architecture' | 'list'

export default function LakehouseObjectsPageMinimal() {
  const t = useTranslations('objects')
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const reduce = useReducedMotion()

  const [view, setView] = useState<ViewMode>('architecture')
  const [selectedSource, setSelectedSource] = useState<SourceGroupData | null>(null)

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
        <div className="text-sm text-ink-muted">{t('no_project_message')}</div>
        <div className="text-xs text-ink-ghost">{t('no_project_hint')}</div>
      </div>
    )
  }

  // 列表 mode preserves the original list view (header, table, modals) verbatim,
  // so it renders standalone with no extra page chrome.
  if (view === 'list') {
    return (
      <div className="flex h-full min-h-0 flex-col bg-canvas">
        <ViewToggle value={view} onChange={(v) => { setView(v); setSelectedSource(null) }} industrial={industrial} reduce={reduce} t={t} />
        <div className="flex flex-1 min-h-0 flex-col">
          <ObjectListView />
        </div>
      </div>
    )
  }

  // 数据架构 mode: overview → drill-down to one source's ER diagram.
  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      <ViewToggle value={view} onChange={(v) => { setView(v); setSelectedSource(null) }} industrial={industrial} reduce={reduce} t={t} />
      <div className="flex flex-1 min-h-0 flex-col">
        {selectedSource ? (
          <SourceErView
            group={{ label: selectedSource.label, memberIds: selectedSource.memberIds }}
            onBack={() => setSelectedSource(null)}
          />
        ) : (
          <DataArchitectureView onOpenSource={(g) => setSelectedSource(g)} />
        )}
      </div>
    </div>
  )
}

// ViewToggle is the top-right segmented control switching between 数据架构 and 列表.
function ViewToggle({
  value, onChange, industrial, reduce, t,
}: {
  value: ViewMode
  onChange: (v: ViewMode) => void
  industrial: boolean
  reduce: boolean | null
  t: ReturnType<typeof useTranslations>
}) {
  const options: [ViewMode, string][] = [
    ['architecture', t('view_architecture')],
    ['list', t('view_list')],
  ]
  return (
    <div className={`flex h-12 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${
      industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'
    }`}>
      <div className="flex min-w-0 items-center gap-3">
        {industrial ? (
          <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
            // {t('page_title').toString().toUpperCase()}
          </span>
        ) : (
          <>
            <Database size={18} className="text-ink" aria-hidden="true" />
            <h1 className="text-base font-semibold tracking-tight text-ink whitespace-nowrap">
              {t('page_title')}
            </h1>
          </>
        )}
      </div>
      <div
        role="radiogroup"
        className={`relative flex h-7 items-center p-0.5 ${
          industrial ? 'border border-ink bg-white' : 'rounded-md border border-border bg-canvas-alt'
        }`}
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
                industrial
                  ? 'font-mono text-[10px] uppercase tracking-[0.14em]'
                  : 'rounded-[5px] text-[11px] font-medium'
              }`}
            >
              {selected && (
                <motion.span
                  layoutId="lakehouse-view-toggle"
                  className={`absolute inset-0 ${industrial ? 'bg-ink' : 'rounded-[5px] bg-white shadow-sm'}`}
                  transition={reduce ? { duration: 0 } : { type: 'spring', stiffness: 500, damping: 35 }}
                />
              )}
              <span
                className={`relative ${
                  selected
                    ? industrial ? 'text-white' : 'text-ink'
                    : 'text-ink-muted hover:text-ink'
                }`}
              >
                {label}
              </span>
            </button>
          )
        })}
      </div>
    </div>
  )
}
