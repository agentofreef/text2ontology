'use client'

import { useRef, useState } from 'react'
import { Download, Upload } from 'lucide-react'
import { Button } from '@/components/ui/Button'
import { useProject } from '@/lib/project'
import { useMessage } from '@/lib/message'
import { apiSSE, getApiBase, type SSEEvent } from '@/lib/api'
import { useTranslations } from 'next-intl'

type UploadPhase = 'idle' | 'uploading' | 'inserting' | 'vectorizing' | 'complete' | 'error'

interface ProgressState {
  phase: UploadPhase
  done: number
  total: number
  inserted: number
  vectorized: number
  message: string
}

interface UploadBarProps {
  templateUrl: string
  uploadUrl: string
  onUploadComplete?: () => void
  entityName: string
}

function ProgressBar({ done, total }: { done: number; total: number }) {
  const ratio = total > 0 ? Math.min(done / total, 1) : 0
  const pct = Math.round(ratio * 100)

  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-32 rounded-full bg-gray-200 overflow-hidden">
        <div
          className="h-full rounded-full bg-ink transition-all duration-200"
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="font-sans text-xs text-ink-muted whitespace-nowrap">{done}/{total} ({pct}%)</span>
    </div>
  )
}

export function UploadBar({ templateUrl, uploadUrl, onUploadComplete, entityName }: UploadBarProps) {
  const fileRef = useRef<HTMLInputElement>(null)
  const { currentProject } = useProject()
  const msg = useMessage()
  const t = useTranslations('ui')
  const [progress, setProgress] = useState<ProgressState>({
    phase: 'idle',
    done: 0,
    total: 0,
    inserted: 0,
    vectorized: 0,
    message: '',
  })

  const handleDownload = async () => {
    const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
    const url = currentProject ? `${getApiBase()}${templateUrl.replace(/^\/api/, '')}?projectId=${currentProject.id}` : `${getApiBase()}${templateUrl.replace(/^\/api/, '')}`
    const res = await fetch(url, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
    const blob = await res.blob()
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = templateUrl.split('/').pop() + '.csv'
    a.click()
  }

  const handleUpload = async (file: File) => {
    setProgress({ phase: 'uploading', done: 0, total: 0, inserted: 0, vectorized: 0, message: '' })

    const formData = new FormData()
    formData.append('file', file)

    // Use the stream endpoint if available (append -stream to upload URL)
    const streamUrl = uploadUrl.replace(/\/upload$/, '/upload-stream')
    const path = currentProject ? `${streamUrl}?projectId=${currentProject.id}` : streamUrl
    // Strip the /api prefix since apiSSE adds it
    const apiPath = path.replace(/^\/api/, '')

    try {
      let finalInserted = 0
      let finalVectorized = 0

      await apiSSE(apiPath, formData, (event: SSEEvent) => {
        switch (event.phase) {
          case 'inserting':
            setProgress(prev => ({ ...prev, phase: 'inserting', total: event.total || 0 }))
            break
          case 'inserted':
            finalInserted = event.inserted || 0
            setProgress(prev => ({
              ...prev,
              phase: 'inserting',
              inserted: event.inserted || 0,
            }))
            break
          case 'vectorizing':
            setProgress(prev => ({
              ...prev,
              phase: 'vectorizing',
              done: event.done || 0,
              total: event.total || 0,
            }))
            break
          case 'complete':
            finalInserted = event.inserted || finalInserted
            finalVectorized = event.vectorized || 0
            setProgress({
              phase: 'complete',
              done: event.vectorized || 0,
              total: event.vectorized || 0,
              inserted: event.inserted || finalInserted,
              vectorized: event.vectorized || 0,
              message: '',
            })
            break
        }
      })

      const detail = `${finalInserted} rows` + (finalVectorized ? ` / ${finalVectorized} vectors` : '')
      setProgress(prev => ({ ...prev, phase: 'complete', message: detail }))
      msg.success(t('upload_success', { detail }))
      onUploadComplete?.()
    } catch (err) {
      const errMsg = err instanceof Error ? err.message : 'Network error'
      setProgress(prev => ({ ...prev, phase: 'error', message: errMsg }))
      msg.error(t('upload_failed', { error: errMsg }))
    }

    if (fileRef.current) fileRef.current.value = ''
  }

  return (
    <div className="flex items-center gap-2">
      {progress.phase === 'uploading' && (
        <span className="font-sans text-xs text-ink-muted">Uploading...</span>
      )}
      {progress.phase === 'inserting' && (
        <span className="font-sans text-xs text-ink-muted">Inserting...</span>
      )}
      {progress.phase === 'vectorizing' && (
        <ProgressBar done={progress.done} total={progress.total} />
      )}
      {progress.phase === 'complete' && (
        <span className="font-sans text-xs text-success">
          {`Done: ${progress.message}`}
        </span>
      )}
      {progress.phase === 'error' && (
        <span className="font-sans text-xs text-danger">
          {`Error: ${progress.message}`}
        </span>
      )}

      <Button variant="ghost" size="sm" onClick={handleDownload}>
        <Download className="h-3.5 w-3.5" />
        {t('download_template')}
      </Button>

      <Button
        variant="default"
        size="sm"
        onClick={() => fileRef.current?.click()}
        disabled={progress.phase === 'uploading' || progress.phase === 'inserting' || progress.phase === 'vectorizing'}
      >
        <Upload className="h-3.5 w-3.5" />
        {t('upload_entity', { entityName })}
      </Button>

      <input
        ref={fileRef}
        type="file"
        accept=".csv,.xlsx,.xls"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0]
          if (file) handleUpload(file)
        }}
      />
    </div>
  )
}
