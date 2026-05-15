'use client'

import { Loader2 } from 'lucide-react'

export type DataSourceStatus =
  | 'pending'
  | 'connecting'
  | 'syncing'
  | 'ready'
  | 'wizard_in_progress'
  | 'completed'
  | 'failed'
  | 'failed_resumable'

export const STATUS_LABEL: Record<DataSourceStatus, string> = {
  pending:           '待连接',
  connecting:        '连接中',
  syncing:           '同步中',
  ready:             '已就绪',
  wizard_in_progress:'配置中',
  completed:         '已完成',
  failed:            '失败',
  failed_resumable:  '可恢复',
}

// Minimal SV palette: only black/white/gray/green/red
const STATUS_CLASS: Record<DataSourceStatus, string> = {
  pending:           'bg-gray-100 text-gray-500 border-gray-200',
  connecting:        'bg-gray-100 text-gray-700 border-gray-200',
  syncing:           'bg-gray-100 text-gray-700 border-gray-200',
  ready:             'bg-gray-100 text-gray-700 border-gray-200',
  wizard_in_progress:'bg-gray-100 text-gray-700 border-gray-200',
  completed:         'bg-green-50 text-green-700 border-green-200',
  failed:            'bg-red-50 text-red-700 border-red-200',
  failed_resumable:  'bg-red-50 text-red-700 border-red-200',
}

const ANIMATED: Set<DataSourceStatus> = new Set(['connecting', 'syncing'])

interface StatusBadgeProps {
  status: DataSourceStatus | string
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const s = status as DataSourceStatus
  const label = STATUS_LABEL[s] ?? status
  const cls = STATUS_CLASS[s] ?? 'bg-gray-100 text-gray-500 border-gray-200'
  const animated = ANIMATED.has(s)

  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider ${cls}`}
    >
      {animated ? (
        <Loader2 className="h-2.5 w-2.5 animate-spin" />
      ) : (
        <span className={`h-1.5 w-1.5 rounded-full ${
          s === 'completed' ? 'bg-green-500'
          : s === 'failed' || s === 'failed_resumable' ? 'bg-red-500'
          : 'bg-gray-400'
        }`} />
      )}
      {label}
    </span>
  )
}
