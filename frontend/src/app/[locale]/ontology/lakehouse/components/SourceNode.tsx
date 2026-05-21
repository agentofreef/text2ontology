'use client'

import { Handle, Position } from '@xyflow/react'
import { Database, FileSpreadsheet, FolderOpen, BarChart3, HardDrive } from 'lucide-react'

// SourceGroupData is the shape carried on each data-architecture node. memberIds
// are the ont_object ids folded into this source/folder so the drill-down ER view
// can filter to exactly those tables.
export interface SourceGroupData {
  label: string
  type: 'postgres' | 'sqlite' | 'pbi' | 'csv' | 'file'
  count: number
  memberIds: string[]
  [key: string]: unknown
}

const iconByType: Record<SourceGroupData['type'], typeof Database> = {
  postgres: Database,
  sqlite: HardDrive,
  pbi: BarChart3,
  csv: FolderOpen,
  file: FolderOpen,
}

const accentByType: Record<SourceGroupData['type'], string> = {
  postgres: 'text-blue-600 bg-blue-50 border-blue-200',
  sqlite: 'text-emerald-600 bg-emerald-50 border-emerald-200',
  pbi: 'text-amber-600 bg-amber-50 border-amber-200',
  csv: 'text-gray-600 bg-gray-50 border-gray-200',
  file: 'text-gray-600 bg-gray-50 border-gray-200',
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function SourceNode({ data, selected }: any) {
  const d = data as SourceGroupData
  const Icon = iconByType[d.type] ?? FileSpreadsheet
  const accent = accentByType[d.type] ?? accentByType.file

  return (
    <div
      className={`flex h-[90px] w-[200px] items-center gap-3 rounded-xl border bg-white px-3 shadow-sm transition-all duration-150 ${
        selected ? 'border-gray-800 shadow-md' : 'border-gray-200'
      }`}
    >
      <Handle type="target" position={Position.Left} style={{ background: '#0A0A0A', border: 'none', width: 8, height: 8, borderRadius: '50%' }} />
      <Handle type="source" position={Position.Right} style={{ background: '#0A0A0A', border: 'none', width: 8, height: 8, borderRadius: '50%' }} />

      <div className={`flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-lg border ${accent}`}>
        <Icon size={18} aria-hidden="true" />
      </div>

      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-semibold leading-tight text-gray-900">
          {d.label || '(unnamed)'}
        </div>
        <div className="mt-0.5 text-[10px] uppercase tracking-wide text-gray-400">
          {d.type}
        </div>
        <div className="mt-1 text-xs font-semibold tabular-nums text-gray-700">
          {d.count} {d.count === 1 ? 'table' : 'tables'}
        </div>
      </div>
    </div>
  )
}
