'use client'

// SQLite import connector — upload a .sqlite / .db / .sqlite3 file as a
// one-shot import (NOT a live connection — SQLite is a local-file format
// with no network protocol; SaaS/containerised collectors can't directly
// "connect" to a file on the user's machine, so the file is copied to the
// server's staging volume and processed from there). After upload we route
// to the existing /settings/data-sources/wizard?id=… flow — wizard logic
// is source-type-agnostic.

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

const ACCEPTED_EXTENSIONS = ['sqlite', 'db', 'sqlite3'] as const

export default function AddSqlitePage() {
  const t = useTranslations('settings.ds.add.sqlite')
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

  const validateExt = (filename: string): boolean => {
    const ext = filename.split('.').pop()?.toLowerCase()
    return ACCEPTED_EXTENSIONS.includes(ext as typeof ACCEPTED_EXTENSIONS[number])
  }

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    setDragOver(false)
    const f = e.dataTransfer.files[0]
    if (!f) return
    if (!validateExt(f.name)) {
      setError(t('invalid_ext'))
      return
    }
    setFile(f)
    setError('')
  }, [])

  const handleFileSelect = (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]
    if (!f) return
    if (!validateExt(f.name)) {
      setError(t('invalid_ext'))
      return
    }
    setFile(f)
    setError('')
  }

  const handleUpload = async () => {
    if (!currentProject) return
    if (!file) { setError(t('select_file')); return }
    setUploading(true)
    setError('')
    const authHeaders: Record<string, string> = getToken()
      ? { Authorization: `Bearer ${getToken()}` }
      : {}
    try {
      // 1. Upload the file → creates data_source row (no staging yet).
      const fd = new FormData()
      fd.append('file', file)
      fd.append('project_id', currentProject.id)
      fd.append('label', label || file.name)
      const upRes = await fetch(`${getApiBase()}/connector/sqlite/sources`, {
        method: 'POST',
        headers: authHeaders,
        body: fd,
      })
      if (!upRes.ok) {
        const data = await upRes.json().catch(() => ({}))
        throw new Error(data.error || data.message || `HTTP ${upRes.status}`)
      }
      const upData = await upRes.json()
      const id = upData.id || upData.ID

      // 2. Discover catalog so we know which tables to sync.
      const catRes = await fetch(`${getApiBase()}/connector/sqlite/sources/${id}/catalog`, {
        headers: authHeaders,
      })
      if (!catRes.ok) {
        const data = await catRes.json().catch(() => ({}))
        throw new Error(data.error || data.message || `Catalog HTTP ${catRes.status}`)
      }
      const catData = await catRes.json()
      const tables: string[] = (catData.tables || []).map(
        (tbl: { name: string }) => tbl.name,
      )
      if (tables.length === 0) {
        throw new Error('No tables found in SQLite file')
      }

      // 3. Sync all tables into staging schema (SSE — consume until complete).
      // staging_schema is set on the data_source row inside this call;
      // without it, wizard /confirm fails with "staging_schema not set".
      const syncRes = await fetch(`${getApiBase()}/connector/sqlite/sources/${id}/sync`, {
        method: 'POST',
        headers: { ...authHeaders, 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({ tables }),
      })
      if (!syncRes.ok) {
        const data = await syncRes.json().catch(() => ({}))
        throw new Error(data.error || data.message || `Sync HTTP ${syncRes.status}`)
      }
      // Drain the SSE stream. We only need to wait for "sync_complete" or
      // "sync_failed" — actual progress UI happens on the wizard page.
      const reader = syncRes.body?.getReader()
      const decoder = new TextDecoder()
      let buf = ''
      let syncFailed: string | null = null
      while (reader) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        const lines = buf.split('\n')
        buf = lines.pop() || ''
        for (const line of lines) {
          if (!line.startsWith('data: ')) continue
          try {
            const ev = JSON.parse(line.slice(6))
            if (ev.phase === 'sync_failed') syncFailed = ev.error || 'sync failed'
            // sync_complete → falls through, loop exits when stream closes
          } catch { /* malformed event */ }
        }
      }
      if (syncFailed) throw new Error(syncFailed)

      // 4. Now staging_schema is populated → wizard /confirm will succeed.
      router.push(`/settings/data-sources/wizard?id=${id}`)
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
        <h1 className="text-base font-semibold text-ink">{t('page_title')}</h1>
      </div>

      <div className="flex-1 overflow-y-auto p-6">
        <div className="rounded-lg border border-border bg-white p-6">
          {/* Hint */}
          <p className="mb-2 text-xs text-ink-muted">
            {t('hint_main')}
          </p>
          <p className="mb-6 text-xs text-ink-ghost">
            {t('hint_sub')}
          </p>

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
              accept=".sqlite,.db,.sqlite3"
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
              {uploading ? t('importing') : t('import_parse')}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
