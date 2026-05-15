'use client'

import { DataTable } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import type { ErNode, ErEdge } from '@/types/api'
import { X } from 'lucide-react'

interface SidePanelProps {
  selected: ErNode | ErEdge | null
  onClose: () => void
}

function isErNode(x: ErNode | ErEdge): x is ErNode {
  return 'columnCount' in x
}

const originVariant: Record<string, 'default' | 'accent' | 'info'> = {
  'pbit-bootstrap': 'default',
  'manual-upload': 'accent',
  'derived-view': 'info',
  '': 'default',
}

export function SidePanel({ selected, onClose }: SidePanelProps) {
  if (!selected) return null

  const isNode = isErNode(selected)

  return (
    <div className="w-72 flex-shrink-0 border-l border-border bg-white flex flex-col overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <span className="font-mono text-[10px] font-semibold tracking-[2px] text-ink-muted">
          ▼// {isNode ? 'TABLE DETAIL' : 'RELATIONSHIP DETAIL'}
        </span>
        <button onClick={onClose} className="text-ink-ghost hover:text-ink">
          <X className="h-4 w-4" />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        {isNode ? (
          <NodeDetail node={selected} />
        ) : (
          <EdgeDetail edge={selected} />
        )}
      </div>
    </div>
  )
}

function NodeDetail({ node }: { node: ErNode }) {
  const colData = (node.columns || []).map((c) => ({
    name: c.name,
    dataType: c.dataType,
  }))

  const columns = [
    {
      key: 'name',
      title: 'COLUMN',
      render: (_: unknown, row: typeof colData[0]) => (
        <span className="font-mono text-[10px] font-semibold">{row.name}</span>
      ),
    },
    {
      key: 'dataType',
      title: 'TYPE',
      width: '80px',
      render: (_: unknown, row: typeof colData[0]) => (
        <span className="font-mono text-[9px] text-ink-muted uppercase">{row.dataType}</span>
      ),
    },
  ]

  return (
    <>
      <div>
        <div className="font-display text-sm font-bold text-ink">{node.label}</div>
        {node.warning && (
          <div className="mt-1 font-mono text-[10px] text-amber-600">⚠ {node.warning}</div>
        )}
      </div>

      <div className="grid grid-cols-2 gap-2">
        <div className="border border-border p-2 text-center">
          <div className="font-mono text-[9px] text-ink-ghost">ROWS</div>
          <div className="font-mono text-lg font-bold">
            {node.rowCount == null ? '—' : node.rowCount.toLocaleString()}
          </div>
        </div>
        <div className="border border-border p-2 text-center">
          <div className="font-mono text-[9px] text-ink-ghost">COLS</div>
          <div className="font-mono text-lg font-bold">{node.columnCount}</div>
        </div>
      </div>

      <div className="flex items-center gap-2">
        <span className="font-mono text-[10px] text-ink-ghost">ORIGIN</span>
        <Badge variant={originVariant[node.origin || ''] || 'default'}>
          {node.origin || 'legacy'}
        </Badge>
      </div>

      {colData.length > 0 && (
        <div>
          <div className="font-mono text-[10px] font-semibold tracking-[2px] text-ink-muted mb-2">
            ▼// COLUMNS
          </div>
          <DataTable
            data={colData}
            columns={columns}
            rowKey="name"
            searchable
            searchPlaceholder=">_ FILTER..."
          />
        </div>
      )}
    </>
  )
}

function EdgeDetail({ edge }: { edge: ErEdge }) {
  return (
    <>
      <div className="font-display text-sm font-bold text-ink">Relationship</div>

      <div className="border border-border divide-y divide-border">
        {[
          { label: 'FROM TABLE', value: edge.fromTable },
          { label: 'FROM COLUMN', value: edge.fromColumn },
          { label: 'TO TABLE', value: edge.toTable },
          { label: 'TO COLUMN', value: edge.toColumn },
          { label: 'CARDINALITY', value: edge.cardinality },
        ].map((row) => (
          <div key={row.label} className="flex items-center justify-between px-3 py-2">
            <span className="font-mono text-[9px] text-ink-ghost">{row.label}</span>
            <span className="font-mono text-xs font-semibold">{row.value}</span>
          </div>
        ))}
        <div className="flex items-center justify-between px-3 py-2">
          <span className="font-mono text-[9px] text-ink-ghost">STATUS</span>
          <Badge variant={edge.isActive ? 'success' : 'warning'}>
            {edge.isActive ? 'ACTIVE' : 'INACTIVE'}
          </Badge>
        </div>
      </div>
    </>
  )
}
