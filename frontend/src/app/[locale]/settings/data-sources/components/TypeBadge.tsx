'use client'

import { Database, FileSpreadsheet, BarChart3 } from 'lucide-react'

export type DataSourceType = 'postgres' | 'file' | 'pbit' | string

const TYPE_LABEL: Record<string, string> = {
  postgres: 'PostgreSQL',
  file:     '文件',
  pbit:     'Power BI',
}

const TYPE_ICON: Record<string, React.ElementType> = {
  postgres: Database,
  file:     FileSpreadsheet,
  pbit:     BarChart3,
}

interface TypeBadgeProps {
  type: DataSourceType
}

export function TypeBadge({ type }: TypeBadgeProps) {
  const label = TYPE_LABEL[type] ?? type
  const Icon = TYPE_ICON[type] ?? Database

  return (
    <span className="inline-flex items-center gap-1 rounded-full border border-gray-200 bg-gray-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-gray-600">
      <Icon className="h-2.5 w-2.5" />
      {label}
    </span>
  )
}
