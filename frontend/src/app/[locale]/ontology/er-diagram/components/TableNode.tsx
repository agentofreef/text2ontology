'use client'

import { Handle, Position } from '@xyflow/react'
import type { ErNode } from '@/types/api'

const originLabel: Record<string, string> = {
  'pbit-bootstrap': 'Excel',
  'pbix-data': 'PBIX',
  'manual-upload': 'Manual',
  'derived-view': 'Derived',
  '': 'Unknown',
}

const originStyle: Record<string, string> = {
  'pbit-bootstrap': 'bg-green-50 text-green-700 border-green-200',
  'pbix-data': 'bg-blue-50 text-blue-700 border-blue-200',
  'manual-upload': 'bg-gray-900 text-white border-gray-900',
  'derived-view': 'bg-gray-100 text-gray-600 border-gray-200',
  '': 'bg-gray-50 text-gray-400 border-gray-200',
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function TableNode({ data, selected }: any) {
  const d = data as ErNode
  const rowCountDisplay = d.rowCount == null ? '—' : d.rowCount.toLocaleString()

  return (
    <div
      className={`min-w-[180px] max-w-[240px] rounded-xl border bg-white shadow-sm transition-all duration-150 ${
        selected ? 'border-gray-800 shadow-md' : 'border-gray-200'
      }`}
    >
      <Handle type="target" position={Position.Left} style={{ background: '#0A0A0A', border: 'none', width: 8, height: 8, borderRadius: '50%' }} />
      <Handle type="source" position={Position.Right} style={{ background: '#0A0A0A', border: 'none', width: 8, height: 8, borderRadius: '50%' }} />

      {/* Header */}
      <div className={`rounded-t-xl border-b px-2.5 py-2 ${selected ? 'border-gray-800 bg-gray-50' : 'border-gray-100 bg-gray-50'}`}>
        <div className="flex items-center gap-1.5">
          {d.warning && <span className="text-amber-500 text-xs flex-shrink-0">⚠</span>}
          <span className="font-sans text-[11px] font-semibold truncate text-gray-900 leading-tight">
            {d.label || '(unnamed)'}
          </span>
        </div>
      </div>

      {/* Stats */}
      <div className="flex border-b border-gray-100">
        <div className="flex-1 border-r border-gray-100 px-2 py-1.5 text-center">
          <div className="text-[8px] text-gray-400 uppercase tracking-wide">Rows</div>
          <div className="text-xs font-semibold text-gray-800">{rowCountDisplay}</div>
        </div>
        <div className="flex-1 px-2 py-1.5 text-center">
          <div className="text-[8px] text-gray-400 uppercase tracking-wide">Cols</div>
          <div className="text-xs font-semibold text-gray-800">{d.columnCount ?? 0}</div>
        </div>
      </div>

      {/* Origin badge */}
      <div className="px-2 py-1.5 rounded-b-xl">
        <span
          className={`inline-flex items-center rounded border px-1.5 py-0.5 text-[8px] font-medium uppercase tracking-wider ${
            originStyle[d.origin || ''] || originStyle['']
          }`}
        >
          {originLabel[d.origin || ''] || 'Unknown'}
        </span>
      </div>
    </div>
  )
}
