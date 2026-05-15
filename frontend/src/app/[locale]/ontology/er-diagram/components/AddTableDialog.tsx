'use client'

import { useState, useRef } from 'react'
import { Modal } from '@/components/ui/Modal'
import { Button } from '@/components/ui/Button'
import { getApiBase } from '@/lib/api'
import { Upload } from 'lucide-react'

interface AddTableDialogProps {
  open: boolean
  projectId: string
  onClose: () => void
  onSuccess: () => void
}

export function AddTableDialog({ open, projectId, onClose, onSuccess }: AddTableDialogProps) {
  const [tableName, setTableName] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState('')
  const [dragOver, setDragOver] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  const handleSubmit = async () => {
    if (!tableName.trim() || !file) {
      setError('Table name and file are required')
      return
    }
    setUploading(true)
    setError('')
    try {
      const form = new FormData()
      form.append('projectId', projectId)
      form.append('tableName', tableName.trim())
      form.append('file', file)
      const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
      const res = await fetch(`${getApiBase()}/connector/pbit/add-table`, {
        method: 'POST',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        body: form,
      })
      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: `Upload failed: ${res.status}` }))
        throw new Error(err.error || `Upload failed: ${res.status}`)
      }
      setTableName('')
      setFile(null)
      onSuccess()
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Upload failed')
    } finally {
      setUploading(false)
    }
  }

  const handleClose = () => {
    if (uploading) return
    setTableName('')
    setFile(null)
    setError('')
    onClose()
  }

  return (
    <Modal open={open} onClose={handleClose} title="Add Table" width="480px">
      <div className="space-y-4">
        <div>
          <label className="block font-mono text-[10px] font-semibold tracking-wider text-ink-muted mb-1.5">
            TABLE NAME
          </label>
          <input
            type="text"
            value={tableName}
            onChange={(e) => setTableName(e.target.value)}
            placeholder="e.g. DimProduct"
            className="w-full border border-border px-3 py-2 font-mono text-sm outline-none focus:border-ink"
          />
        </div>

        <div>
          <label className="block font-mono text-[10px] font-semibold tracking-wider text-ink-muted mb-1.5">
            DATA FILE (.xlsx or .csv)
          </label>
          <div
            className={`border-2 border-dashed p-6 text-center cursor-pointer transition-colors duration-100 ${
              dragOver ? 'border-accent bg-accent/5' : 'border-border hover:border-ink-muted'
            }`}
            onDragOver={(e) => { e.preventDefault(); setDragOver(true) }}
            onDragLeave={() => setDragOver(false)}
            onDrop={(e) => {
              e.preventDefault()
              setDragOver(false)
              const f = e.dataTransfer.files[0]
              if (f) setFile(f)
            }}
            onClick={() => inputRef.current?.click()}
          >
            <input
              ref={inputRef}
              type="file"
              accept=".xlsx,.csv"
              className="hidden"
              onChange={(e) => { if (e.target.files?.[0]) setFile(e.target.files[0]) }}
            />
            <Upload className="mx-auto h-6 w-6 text-ink-ghost mb-2" />
            {file ? (
              <div className="font-mono text-xs font-semibold text-ink">{file.name}</div>
            ) : (
              <div className="font-mono text-xs text-ink-muted">DROP .XLSX OR .CSV</div>
            )}
          </div>
        </div>

        {error && (
          <div className="border border-red-200 bg-red-50 px-3 py-2 font-mono text-xs text-red-700">
            ERROR: {error}
          </div>
        )}

        <div className="flex items-center justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={handleClose} disabled={uploading}>
            CANCEL
          </Button>
          <Button
            variant="primary"
            onClick={handleSubmit}
            disabled={uploading || !tableName.trim() || !file}
          >
            {uploading ? 'UPLOADING...' : 'ADD TABLE'}
          </Button>
        </div>
      </div>
    </Modal>
  )
}
