'use client'

import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { Database, FileSpreadsheet, BarChart3, HardDrive, ChevronLeft, ChevronRight } from 'lucide-react'

export default function AddSourcePage() {
  const t = useTranslations('settings.ds.add')
  const router = useRouter()

  const SOURCE_TYPES = [
    {
      id: 'postgres',
      icon: Database,
      title: t('postgres_title'),
      description: t('postgres_desc'),
      href: '/settings/data-sources/add/postgres',
    },
    {
      id: 'sqlite',
      icon: HardDrive,
      title: t('sqlite_title'),
      description: t('sqlite_desc'),
      href: '/settings/data-sources/add/sqlite',
    },
    {
      id: 'file',
      icon: FileSpreadsheet,
      title: t('file_title'),
      description: t('file_desc'),
      href: '/settings/data-sources/add/file',
    },
    {
      id: 'pbi',
      icon: BarChart3,
      title: t('pbi_title'),
      description: t('pbi_desc'),
      href: '/settings/data-sources/add/pbi',
    },
  ]

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header */}
      <div className="flex items-center gap-3 border-b border-border px-6 py-4">
        <button
          onClick={() => router.push('/settings/data-sources')}
          className="flex items-center gap-1 text-xs text-ink-ghost transition-colors duration-150 hover:text-ink"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          {t('back')}
        </button>
        <span className="text-ink-ghost">/</span>
        <h1 className="text-base font-semibold text-ink">{t('page_title')}</h1>
      </div>

      {/* Cards */}
      <div className="flex-1 overflow-y-auto p-6">
        <p className="mb-6 text-sm text-ink-muted">{t('select_type')}</p>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
          {SOURCE_TYPES.map((src) => {
            const Icon = src.icon
            return (
              <button
                key={src.id}
                onClick={() => router.push(src.href)}
                className="group flex flex-col items-start gap-3 rounded-lg border border-border bg-white p-5 text-left transition-colors duration-150 hover:border-gray-400"
              >
                <div className="flex w-full items-start justify-between">
                  <div className="flex h-9 w-9 items-center justify-center rounded-md border border-border bg-canvas-alt">
                    <Icon className="h-4.5 w-4.5 text-ink-muted" />
                  </div>
                  <ChevronRight className="h-4 w-4 text-ink-ghost transition-colors duration-150 group-hover:text-ink" />
                </div>
                <div>
                  <p className="mb-1 text-sm font-semibold text-ink">{src.title}</p>
                  <p className="text-xs text-ink-ghost">{src.description}</p>
                </div>
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )
}
