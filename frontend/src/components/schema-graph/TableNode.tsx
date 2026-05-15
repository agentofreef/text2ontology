'use client'

import { memo, useCallback } from 'react'
import { Handle, Position, useReactFlow, type NodeProps } from '@xyflow/react'
import { ClipboardList } from 'lucide-react'
import { useTranslations } from 'next-intl'

export interface TableNodeData {
  tableId: string
  tableName: string
  tableType: 'fact' | 'dimension' | 'bridge' | 'calculated'
  columns: { columnName: string; dataType: string; isKey: boolean }[]
  relatedColumns?: Set<string>
  annotatingTableId?: string
  onAnnotate?: (tableId: string) => void
  editable?: boolean
}

function TableNodeComponent({ data, selected }: NodeProps & { data: TableNodeData }) {
  const t = useTranslations('ui')
  const isFact = data.tableType === 'fact'
  const isAnnotating = data.annotatingTableId === data.tableId
  const editable = data.editable ?? false

  const handleAnnotateClick = useCallback(
    (e: React.MouseEvent) => {
      e.stopPropagation()
      data.onAnnotate?.(data.tableId)
    },
    [data]
  )

  return (
    <div
      className={`min-w-[160px] border-2 bg-white text-left ${
        isAnnotating ? 'border-accent ring-2 ring-accent/30' : isFact ? 'border-accent' : 'border-ink'
      } ${selected && !isAnnotating ? 'ring-2 ring-ink/20 ring-offset-1' : ''}`}
    >
      {/* Table header */}
      <div className={`flex items-center justify-between px-3 py-1.5 ${isFact ? 'bg-accent/10' : 'bg-canvas-alt'}`}>
        <div>
          <div className="font-mono text-[9px] font-bold tracking-wider text-ink-muted">
            {data.tableType.toUpperCase()}
          </div>
          <div className="text-sm font-semibold">{data.tableName}</div>
        </div>
        {editable && (
          <button
            onClick={handleAnnotateClick}
            className={`flex h-6 w-6 items-center justify-center transition-colors ${
              isAnnotating ? 'text-accent' : 'text-ink-ghost hover:text-accent'
            }`}
            title={t('table_node_annotate')}
          >
            <ClipboardList className="h-3.5 w-3.5" />
          </button>
        )}
      </div>

      {/* Columns */}
      <div className="divide-y divide-border">
        {data.columns.map((col) => {
          const isRelated = data.relatedColumns?.has(col.columnName)
          return (
            <div
              key={col.columnName}
              className={`relative flex items-center gap-1.5 px-3 py-1 ${
                isRelated ? 'bg-accent/5' : ''
              }`}
            >
              <Handle
                type="target"
                position={Position.Left}
                id={`${col.columnName}-target`}
                className={`!border-2 !border-ink !bg-white ${editable ? '!h-2.5 !w-2.5 hover:!bg-accent' : '!h-0 !w-0 !opacity-0'}`}
                style={{ top: 'auto' }}
              />
              <span className="font-mono text-[10px] text-accent">
                {col.isKey ? '●' : '\u00A0'}
              </span>
              <span className={`font-mono text-xs ${isRelated ? 'font-semibold text-ink' : 'text-ink-muted'}`}>
                {col.columnName}
              </span>
              <span className="ml-auto font-mono text-[9px] text-ink-ghost">{col.dataType}</span>
              <Handle
                type="source"
                position={Position.Right}
                id={`${col.columnName}-source`}
                className={`!border-2 !border-ink !bg-white ${editable ? '!h-2.5 !w-2.5 hover:!bg-accent' : '!h-0 !w-0 !opacity-0'}`}
                style={{ top: 'auto' }}
              />
            </div>
          )
        })}
      </div>
    </div>
  )
}

export const TableNode = memo(TableNodeComponent)
