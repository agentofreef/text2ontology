'use client'

import { useTranslations } from 'next-intl'
import { useState, useRef, useCallback } from 'react'
import { useRouter } from '@/i18n/navigation'
import { useProject } from '@/lib/project'
import { Button } from '@/components/ui/Button'
import { ChevronLeft, Upload, FileText, X } from 'lucide-react'
import { getApiBase } from '@/lib/api'

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export default function AddPBIPage() {
  const t = useTranslations('settings.ds.add.pbi')
  const router = useRouter()
  const { currentProject } = useProject()
  const [label, setLabel] = useState('')
  const [file, setFile] = useState<File | null>(null)
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
      if (!['pbit'].includes(ext ?? '')) {
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
    if (!currentProject || !file) {
      setError(t('select_file'))
      return
    }
    setUploading(true)
    setError('')
    try {
      const fd = new FormData()
      fd.append('file', file)
      fd.append('projectId', currentProject.id)
      if (label) fd.append('label', label)
      const res = await fetch(`${getApiBase()}/connector/pbit/upload`, {
        method: 'POST',
        headers: getToken() ? { Authorization: `Bearer ${getToken()}` } : {},
        body: fd,
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(data.error || data.message || `HTTP ${res.status}`)
      }
      const data = await res.json()
      const id = data.id || data.ID || data.datasourceId
      if (id) {
        router.push(`/settings/data-sources/wizard?id=${id}`)
      } else {
        // PBI upload may navigate to pbit-import legacy flow
        router.push('/settings/pbit-import')
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : t('upload_failed'))
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
        <h1 className="text-base font-semibold text-ink">Power BI Template</h1>
      </div>

      <div className="flex-1 overflow-y-auto p-6">
        <div className="rounded-lg border border-border bg-white p-6">
          {/* Label */}
          <div className="mb-5">
            <label className="mb-1.5 block text-xs font-medium text-ink-muted">{t('label_optional')}</label>
            <input
              type="text"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={t('label_placeholder')}
              className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
            />
          </div>

          {/* Dropzone */}
          <div
            onDragOver={(e) => { e.preventDefault(); setDragOver(true) }}
            onDragLeave={() => setDragOver(false)}
            onDrop={handleDrop}
            onClick={() => fileInputRef.current?.click()}
            className={`cursor-pointer rounded-lg border-2 border-dashed px-6 py-10 text-center transition-colors duration-150 ${
              dragOver ? 'border-ink bg-gray-50' : 'border-border hover:border-gray-400'
            }`}
          >
            <input
              ref={fileInputRef}
              type="file"
              accept=".pbit"
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
              disabled={uploading || !file}
            >
              {uploading ? t('uploading') : t('upload_parse')}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
