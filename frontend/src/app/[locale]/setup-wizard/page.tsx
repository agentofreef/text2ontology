'use client'

import { useTranslations } from 'next-intl'
import { useState } from 'react'
import { getApiBase } from '@/lib/api'
import { useRouter } from '@/i18n/navigation'
import { Database, BarChart3, FileSpreadsheet, HardDrive, ChevronRight, ArrowRight } from 'lucide-react'
import { useProject } from '@/lib/project'

type SourceId = 'postgres' | 'sqlite' | 'pbi' | 'file'

// ── Page ────────────────────────────────────────────────────────────────────

export default function SetupWizardPage() {
  const t = useTranslations('setup_wizard')
  const router = useRouter()
  const { switchProject, refetchProjects } = useProject()

  const SOURCE_TYPES = [
    {
      id: 'postgres' as const,
      icon: Database,
      title: t('postgres_title'),
      description: t('postgres_desc'),
      destPath: '/settings/data-sources/add/postgres',
    },
    {
      id: 'sqlite' as const,
      icon: HardDrive,
      title: t('sqlite_title'),
      description: t('sqlite_desc'),
      destPath: '/settings/data-sources/add/sqlite',
    },
    {
      id: 'pbi' as const,
      icon: BarChart3,
      title: t('pbi_title'),
      description: t('pbi_desc'),
      destPath: '/settings/data-sources/add/pbi',
    },
    {
      id: 'file' as const,
      icon: FileSpreadsheet,
      title: t('file_title'),
      description: t('file_desc'),
      destPath: '/settings/data-sources/add/file',
    },
  ]

  const [name, setName] = useState('')
  const [sourceId, setSourceId] = useState<SourceId | null>(null)
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')

  const selected = SOURCE_TYPES.find((s) => s.id === sourceId) ?? null
  const canSubmit = name.trim().length > 0 && sourceId !== null && !creating

  const handleCreate = async () => {
    if (!canSubmit || !selected) return
    setCreating(true)
    setCreateError('')
    try {
      const token =
        typeof window !== 'undefined'
          ? localStorage.getItem('lakehouse2ontology_token')
          : null
      const res = await fetch(`${getApiBase()}/projects`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify({
          name: name.trim(),
          sourceType: selected.id,
        }),
      })
      const data = await res.json()
      if (!data.success || !data.data?.id) {
        setCreateError(data.error || t('create_failed'))
        return
      }

      // Switch to the new project, then navigate to the matching add-source page.
      await refetchProjects()
      try {
        const r2 = await fetch(`${getApiBase()}/projects`, {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
        })
        const res2 = await r2.json()
        const newProject = (res2.data || []).find(
          (p: { id: string }) => p.id === data.data.id,
        )
        if (newProject) switchProject(newProject)
      } catch {
        // Best-effort; proceed even if the project list refresh fails.
      }

      router.push(selected.destPath)
    } catch (err) {
      setCreateError(err instanceof Error ? err.message : t('network_error'))
    } finally {
      setCreating(false)
    }
  }

  return (
    <div className="flex min-h-screen flex-col bg-canvas">
      {/* Top bar */}
      <div className="flex h-12 items-center gap-2 border-b border-border bg-white px-6">
        <span className="font-mono text-[12px] font-bold tracking-[0.06em] text-ink">TEXT2ONTOLOGY</span>
        <span className="text-xs text-ink-ghost">/ {t('init_project')}</span>
      </div>

      {/* Centered card */}
      <div className="flex flex-1 items-start justify-center px-4 py-12">
        <div className="w-full max-w-xl">
          {/* Heading */}
          <div className="mb-8">
            <h1 className="text-xl font-semibold text-ink">{t('welcome_title')}</h1>
            <p className="mt-1 text-sm text-ink-muted">
              {t('welcome_hint')}
            </p>
          </div>

          {/* Card */}
          <div className="rounded-lg border border-border bg-white p-6 space-y-6">
            {/* Project name */}
            <div>
              <label className="mb-1.5 block text-xs font-medium text-ink-muted">
                {t('project_name')} <span className="text-red-500">*</span>
              </label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('project_name_placeholder')}
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
                onKeyDown={(e) => {
                  if (e.key === 'Enter') handleCreate()
                }}
              />
            </div>

            {/* Source type tiles */}
            <div>
              <label className="mb-2 block text-xs font-medium text-ink-muted">
                {t('source_type')} <span className="text-red-500">*</span>
              </label>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
                {SOURCE_TYPES.map((src) => {
                  const Icon = src.icon
                  const isSelected = sourceId === src.id
                  return (
                    <button
                      key={src.id}
                      type="button"
                      onClick={() => setSourceId(src.id)}
                      className={[
                        'group flex flex-col items-start gap-2.5 rounded-md border p-4 text-left transition-colors duration-150',
                        isSelected
                          ? 'border-ink bg-ink text-white'
                          : 'border-border bg-white text-ink hover:border-ink',
                      ].join(' ')}
                    >
                      <div className="flex w-full items-start justify-between">
                        <div
                          className={[
                            'flex h-8 w-8 items-center justify-center rounded-md border',
                            isSelected
                              ? 'border-white/30 bg-white/10'
                              : 'border-border bg-canvas-alt',
                          ].join(' ')}
                        >
                          <Icon
                            className={[
                              'h-4 w-4',
                              isSelected ? 'text-white' : 'text-ink-muted',
                            ].join(' ')}
                          />
                        </div>
                        <ChevronRight
                          className={[
                            'h-3.5 w-3.5 transition-colors duration-150',
                            isSelected
                              ? 'text-white/70'
                              : 'text-ink-ghost group-hover:text-ink',
                          ].join(' ')}
                        />
                      </div>
                      <div>
                        <p
                          className={[
                            'text-xs font-semibold',
                            isSelected ? 'text-white' : 'text-ink',
                          ].join(' ')}
                        >
                          {src.title}
                        </p>
                        <p
                          className={[
                            'mt-0.5 text-xs leading-relaxed',
                            isSelected ? 'text-white/70' : 'text-ink-ghost',
                          ].join(' ')}
                        >
                          {src.description}
                        </p>
                      </div>
                    </button>
                  )
                })}
              </div>
            </div>

            {/* Error */}
            {createError && (
              <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
                {createError}
              </div>
            )}

            {/* Submit */}
            <div className="flex justify-end">
              <button
                type="button"
                onClick={handleCreate}
                disabled={!canSubmit}
                className="inline-flex items-center gap-1.5 rounded-md bg-ink px-4 py-2 text-sm font-medium text-white transition-opacity duration-150 disabled:opacity-40 hover:opacity-80"
              >
                {creating ? t('creating') : t('create_project')}
                {!creating && <ArrowRight className="h-3.5 w-3.5" />}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
