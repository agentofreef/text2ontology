'use client'

import { useTranslations } from 'next-intl'
import { useState, useRef, useCallback } from 'react'
import { useRouter } from '@/i18n/navigation'
import { useProject } from '@/lib/project'
import { Button } from '@/components/ui/Button'
import { ChevronLeft, Upload, FileText, X } from 'lucide-react'
import { getApiBase } from '@/lib/api'

type TabMode = 'upload' | 'url'

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export default function AddFilePage() {
  const t = useTranslations('settings.ds.add.file')
  const router = useRouter()
  const { currentProject } = useProject()
  const [tab, setTab] = useState<TabMode>('upload')
  const [label, setLabel] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [url, setUrl] = useState('')
  const [dragOver, setDragOver] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState('')
  const fileInputRef = useRef<HTMLInputElement>(null)

  const getToken = () =>
    typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    setDragOver(false)
    const f = e.dataTransfer.files[0]
    if (f) {
      const ext = f.name.split('.').pop()?.toLowerCase()
      if (!['csv', 'xlsx', 'xls'].includes(ext ?? '')) {
        setError(t('invalid_ext'))
        return
      }
      setFile(f)
      setError('')
    }
  }, [])

  const handleFileSelect = (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]
    if (f) {
      setFile(f)
      setError('')
    }
  }

  const handleUpload = async () => {
    if (!currentProject) return
    setUploading(true)
    setError('')
    try {
      if (tab === 'upload') {
        if (!file) { setError(t('select_file')); return }
        const fd = new FormData()
        fd.append('file', file)
        fd.append('project_id', currentProject.id)
        fd.append('label', label || file.name)
        const res = await fetch(`${getApiBase()}/connector/file/upload`, {
          method: 'POST',
          headers: getToken() ? { Authorization: `Bearer ${getToken()}` } : {},
          body: fd,
        })
        if (!res.ok) {
          const data = await res.json().catch(() => ({}))
          throw new Error(data.error || data.message || `HTTP ${res.status}`)
        }
        const data = await res.json()
        const id = data.id || data.ID
        router.push(`/settings/data-sources/wizard?id=${id}`)
      } else {
        if (!url) { setError(t('enter_url')); return }
        const res = await fetch(`${getApiBase()}/connector/file/url`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            ...(getToken() ? { Authorization: `Bearer ${getToken()}` } : {}),
          },
          body: JSON.stringify({ url, project_id: currentProject.id, label: label || url }),
        })
        if (!res.ok) {
          const data = await res.json().catch(() => ({}))
          throw new Error(data.error || data.message || `HTTP ${res.status}`)
        }
        const data = await res.json()
        const id = data.id || data.ID
        router.push(`/settings/data-sources/wizard?id=${id}`)
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : t('op_failed'))
    } finally {
      setUploading(false)
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header */}
      <div className="flex items-center gap-3 border-b border-border px-6 py-4">
        <button
          onClick={() => router.push('/settings/data-sources/add')}
          className="flex items-center gap-1 text-xs text-ink-ghost transition-colors duration-150 hover:text-ink"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          {t('back')}
        </button>
        <span className="text-ink-ghost">/</span>
        <span
          className="cursor-pointer text-xs text-ink-ghost transition-colors duration-150 hover:text-ink"
          onClick={() => router.push('/settings/data-sources/add')}
        >
          {t('add_source')}
        </span>
        <span className="text-ink-ghost">/</span>
        <h1 className="text-base font-semibold text-ink">{t('page_title')}</h1>
      </div>

      <div className="flex-1 overflow-y-auto p-6">
        <div className="rounded-lg border border-border bg-white p-6">
          {/* Tab switcher */}
          <div className="mb-6 flex gap-0 border-b border-border">
            {(['upload', 'url'] as const).map((tabKey) => (
              <button
                key={tabKey}
                onClick={() => { setTab(tabKey); setError('') }}
                className={`px-4 pb-2.5 text-sm font-medium transition-colors duration-150 ${
                  tab === tabKey
                    ? 'border-b-2 border-ink text-ink'
                    : 'text-ink-muted hover:text-ink'
                }`}
              >
                {tabKey === 'upload' ? t('tab_upload') : t('tab_url')}
              </button>
            ))}
          </div>

          {/* Label */}
          <div className="mb-4">
            <label className="mb-1.5 block text-xs font-medium text-ink-muted">{t('label_optional')}</label>
            <input
              type="text"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={t('label_placeholder')}
              className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
            />
          </div>

          {tab === 'upload' ? (
            <div>
              {/* Dropzone */}
              <div
                onDragOver={(e) => { e.preventDefault(); setDragOver(true) }}
                onDragLeave={() => setDragOver(false)}
                onDrop={handleDrop}
                onClick={() => fileInputRef.current?.click()}
                className={`cursor-pointer rounded-lg border-2 border-dashed px-6 py-10 text-center transition-colors duration-150 ${
                  dragOver
                    ? 'border-ink bg-gray-50'
                    : 'border-border hover:border-gray-400'
                }`}
              >
                <input
                  ref={fileInputRef}
                  type="file"
                  accept=".csv,.xlsx,.xls"
                  onChange={handleFileSelect}
                  className="hidden"
                />
                {file ? (
                  <div className="flex items-center justify-center gap-3">
                    <FileText className="h-5 w-5 text-ink-muted" />
                    <div className="text-left">
                      <p className="text-sm font-medium text-ink">{file.name}</p>
                      <p className="text-xs text-ink-ghost">{formatBytes(file.size)}</p>
                    </div>
                    <button
                      onClick={(e) => { e.stopPropagation(); setFile(null) }}
                      className="ml-2 text-ink-ghost transition-colors hover:text-ink"
                    >
                      <X className="h-4 w-4" />
                    </button>
                  </div>
                ) : (
                  <div>
                    <Upload className="mx-auto mb-3 h-8 w-8 text-ink-ghost opacity-50" />
                    <p className="text-sm font-medium text-ink-muted">{t('drop_hint')}</p>
                    <p className="mt-1 text-xs text-ink-ghost">{t('supported_formats')}</p>
                  </div>
                )}
              </div>
            </div>
          ) : (
            <div>
              <label className="mb-1.5 block text-xs font-medium text-ink-muted">
                {t('file_url')} <span className="text-red-500">*</span>
              </label>
              <input
                type="url"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://example.com/data.csv"
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
              />
            </div>
          )}

          {error && (
            <div className="mt-4 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
              {error}
            </div>
          )}

          <div className="mt-6">
            <Button
              variant="primary"
              size="sm"
              onClick={handleUpload}
              disabled={uploading || (tab === 'upload' ? !file : !url)}
            >
              {uploading ? t('processing') : tab === 'upload' ? t('upload_parse') : t('fetch_parse')}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
