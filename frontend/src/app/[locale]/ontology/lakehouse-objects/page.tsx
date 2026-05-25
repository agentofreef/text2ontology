'use client'

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { useProject } from '@/lib/project'
import { Box, ChevronDown, ChevronUp } from 'lucide-react'
import { ObjectListView } from './components/ObjectListView'
import { OntologyGraph } from './components/OntologyGraph'

// Graph-primary ontology page. The ECharts graph fills the whole surface; a
// floating, collapsible object panel overlays the top-left (above the graph
// layer). Selecting an object in the panel focuses the graph's camera on its
// node; the panel's edit action navigates straight to the /detail editor.
// (Replaces the earlier split-view + bottom inspector drawer.)
export default function LakehouseObjectsPage() {
  const t = useTranslations('objects')
  const { currentProject } = useProject()

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [graphRefreshKey, setGraphRefreshKey] = useState(0)
  const [panelOpen, setPanelOpen] = useState(true)

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
        <div className="text-sm text-ink-muted">{t('no_project_message')}</div>
        <div className="text-xs text-ink-ghost">{t('no_project_hint')}</div>
      </div>
    )
  }

  return (
    <div className="relative h-full w-full overflow-hidden bg-canvas">
      {/* Primary layer: the graph fills the page. */}
      <div className="absolute inset-0">
        <OntologyGraph
          selectedId={selectedId}
          onSelectNode={(n) => setSelectedId(n.id)}
          onSelectEdge={() => { /* edges don't drive the panel in graph-primary mode */ }}
          refreshKey={graphRefreshKey}
        />
      </div>

      {/* Floating, collapsible object panel — sits on a layer above the graph
          (top-left; the graph's own toolbar is right-aligned so they don't
          collide). Collapses to just its header bar ("收上去"). */}
      <div className="absolute left-3 top-3 z-20 flex max-h-[62vh] w-[600px] flex-col border border-border bg-white">
        <button
          type="button"
          onClick={() => setPanelOpen((o) => !o)}
          aria-expanded={panelOpen}
          className="flex h-9 flex-shrink-0 items-center gap-2 border-b border-border-light px-3 text-left outline-none hover:bg-canvas-alt focus-visible:ring-1 focus-visible:ring-ink"
        >
          <Box size={13} className="flex-shrink-0 text-ink-muted" aria-hidden="true" />
          <span className="flex-1 truncate font-mono text-[10px] uppercase tracking-[0.14em] text-ink-ghost">
            {t('page_title')}
          </span>
          {panelOpen
            ? <ChevronUp size={14} className="flex-shrink-0 text-ink-ghost" aria-hidden="true" />
            : <ChevronDown size={14} className="flex-shrink-0 text-ink-ghost" aria-hidden="true" />}
        </button>
        {panelOpen && (
          <div className="flex min-h-0 flex-1 flex-col">
            <ObjectListView
              compact
              selectedId={selectedId ?? undefined}
              onSelect={(id) => setSelectedId(id)}
              onMutated={() => setGraphRefreshKey((k) => k + 1)}
            />
          </div>
        )}
      </div>
    </div>
  )
}
