'use client'

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { motion, useReducedMotion } from 'motion/react'
import { useStyleMode } from '@/lib/style-mode'
import { LakehouseTablesView } from './components/LakehouseTablesView'
import { DataArchitectureView } from './components/DataArchitectureView'
import { SourceErView } from './components/SourceErView'
import type { SourceGroupData } from './components/SourceNode'

type ViewMode = 'tables' | 'architecture'

// The lakehouse (data layer) page. Default view is the table list (the original
// page); a toggle reveals the data-source architecture view (sources → ER
// drill-down), relocated here from lakehouse-objects. The toggle is rendered
// INSIDE each view's own header (via headerSlot) so there is a single header
// bar at the standard height, not a stacked second bar.
export default function LakehousePage() {
  const t = useTranslations('lakehouse')
  const industrial = useStyleMode().mode === 'industrial'
  const reduce = useReducedMotion()

  const [view, setView] = useState<ViewMode>('tables')
  const [selectedSource, setSelectedSource] = useState<SourceGroupData | null>(null)

  const toggle = (
    <ViewToggleControl
      value={view}
      onChange={(v) => { setView(v); setSelectedSource(null) }}
      industrial={industrial}
      reduce={reduce}
      t={t}
    />
  )

  if (view === 'tables') {
    return <LakehouseTablesView headerSlot={toggle} />
  }

  // architecture: overview → drill into one source's ER diagram. The drill-down
  // (SourceErView) has its own breadcrumb header with a back button, so it does
  // not carry the toggle.
  if (selectedSource) {
    return (
      <SourceErView
        group={{ label: selectedSource.label, memberIds: selectedSource.memberIds }}
        onBack={() => setSelectedSource(null)}
      />
    )
  }
  return <DataArchitectureView headerSlot={toggle} onOpenSource={(g) => setSelectedSource(g)} />
}

// ViewToggleControl is the segmented control (表 / 数据架构) embedded in a view's
// header — no bar/title of its own.
function ViewToggleControl({
  value, onChange, industrial, reduce, t,
}: {
  value: ViewMode
  onChange: (v: ViewMode) => void
  industrial: boolean
  reduce: boolean | null
  t: ReturnType<typeof useTranslations>
}) {
  const options: [ViewMode, string][] = [
    ['tables', t('view_tables')],
    ['architecture', t('view_architecture')],
  ]
  return (
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
                layoutId="lakehouse-page-view-toggle"
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
  )
}
