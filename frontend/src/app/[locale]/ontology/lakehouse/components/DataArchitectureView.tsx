'use client'

import { useState, useEffect, useCallback, useRef, type ReactNode } from 'react'
import {
  type Node,
  type NodeTypes,
  useNodesState,
} from '@xyflow/react'
import { useTranslations } from 'next-intl'
import { ErCanvas, layoutGraph } from '@/components/er-canvas/ErCanvas'
import { useProject } from '@/lib/project'
import { api, getApiBase } from '@/lib/api'
import { useStyleMode } from '@/lib/style-mode'
import { useMessage } from '@/lib/message'
import { Button } from '@/components/ui/Button'
import { Modal } from '@/components/ui/Modal'
import { Upload, FileText, X, RefreshCw, Database } from 'lucide-react'
import type { OntObjectType } from '@/types/api'
import { SourceNode, type SourceGroupData } from './SourceNode'

const nodeTypes: NodeTypes = {
  sourceNode: SourceNode as unknown as NodeTypes['sourceNode'],
}

// A connector data source as returned by GET /connector/sources.
interface DataSource {
  id: string
  type: string
  label: string
}

const CSV_FOLDER_ID = '__csv_folder__'
const PBI_LOOSE_ID = '__pbi_loose__'

// File-ish source types that must all collapse into the single "CSV 文件夹" node.
const FILE_SOURCE_TYPES = new Set(['file', 'csv', 'excel', 'xlsx', 'xls'])
// Object sourceType values that route an object into the CSV folder.
const FILE_OBJECT_SOURCE_TYPES = new Set(['excel', 'csv', 'file'])

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

// DataArchitectureView is the default lakehouse-objects view: one node per
// non-file data source plus collapsing folders (CSV/PowerBI), rendered on the
// shared ER canvas with no edges. Clicking a node drills into its ER diagram.
export function DataArchitectureView({ onOpenSource, headerSlot }: { onOpenSource: (group: SourceGroupData) => void; headerSlot?: ReactNode }) {
  const t = useTranslations('lakehouse')
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const msg = useMessage()
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  // Upload modal
  const [uploadOpen, setUploadOpen] = useState(false)
  const [uploadFile, setUploadFile] = useState<File | null>(null)
  const [uploadLabel, setUploadLabel] = useState('')
  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState('')
  const [dragOver, setDragOver] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const getToken = () =>
    typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null

  const buildGroups = useCallback((sources: DataSource[], objects: OntObjectType[]): SourceGroupData[] => {
    const groups: SourceGroupData[] = []

    // CSV folder collects: null/empty dataSourceId objects, file-typed sources'
    // objects, and objects whose sourceType is file-ish. Every file data_source
    // folds into this single node (never one node per file source).
    const csvMemberIds: string[] = []
    const pbiLooseMemberIds: string[] = []

    // Map dataSourceId → its source type, so objects can route by their source.
    const sourceById = new Map<string, DataSource>()
    for (const s of sources) sourceById.set(s.id, s)

    // Buckets for the explicit pg/sqlite/pbi instance nodes.
    const instanceMembers = new Map<string, string[]>()
    const fileSourceIds = new Set<string>()
    for (const s of sources) {
      const type = (s.type || '').toLowerCase()
      if (FILE_SOURCE_TYPES.has(type)) {
        fileSourceIds.add(s.id)
        continue // folded into CSV folder, no per-source node
      }
      instanceMembers.set(s.id, [])
    }

    for (const o of objects) {
      const dsid = o.dataSourceId && o.dataSourceId.trim() ? o.dataSourceId : null
      const objSourceType = (o.sourceType || '').toLowerCase()
      const linkedSource = dsid ? sourceById.get(dsid) : undefined
      const linkedType = (linkedSource?.type || '').toLowerCase()

      // Route to CSV folder: no dataSourceId, OR linked to a file source,
      // OR the object's own sourceType is file-ish.
      if (!dsid || (dsid && fileSourceIds.has(dsid)) || FILE_OBJECT_SOURCE_TYPES.has(objSourceType)) {
        csvMemberIds.push(o.id)
        continue
      }

      // PowerBI object not linked to a known pg/sqlite/pbi instance → PowerBI node.
      if (linkedSource && linkedType === 'pbi') {
        const bucket = instanceMembers.get(dsid)
        if (bucket) bucket.push(o.id)
        continue
      }

      if (instanceMembers.has(dsid)) {
        instanceMembers.get(dsid)!.push(o.id)
      } else if (objSourceType === 'pbix' || objSourceType === 'pbi' || objSourceType === 'powerbi') {
        // PowerBI object with a dataSourceId that doesn't resolve to an instance.
        pbiLooseMemberIds.push(o.id)
      } else {
        // dataSourceId points at an unknown source — keep visible in CSV folder
        // rather than dropping the object entirely.
        csvMemberIds.push(o.id)
      }
    }

    // Instance nodes (pg/sqlite/pbi). Show even with count 0.
    for (const s of sources) {
      if (fileSourceIds.has(s.id)) continue
      const type = (s.type || '').toLowerCase()
      const nodeType: SourceGroupData['type'] =
        type === 'postgres' || type === 'postgresql' ? 'postgres'
          : type === 'sqlite' ? 'sqlite'
            : type === 'pbi' || type === 'powerbi' ? 'pbi'
              : 'postgres'
      const members = instanceMembers.get(s.id) || []
      groups.push({
        id: s.id,
        label: s.label || s.type,
        type: nodeType,
        count: members.length,
        memberIds: members,
      })
    }

    // PowerBI loose node — only when there are unlinked PowerBI objects.
    if (pbiLooseMemberIds.length > 0) {
      groups.push({
        id: PBI_LOOSE_ID,
        label: 'PowerBI',
        type: 'pbi',
        count: pbiLooseMemberIds.length,
        memberIds: pbiLooseMemberIds,
      })
    }

    // The single CSV folder — always present so file objects have a home.
    groups.push({
      id: CSV_FOLDER_ID,
      label: 'CSV 文件夹',
      type: 'csv',
      count: csvMemberIds.length,
      memberIds: csvMemberIds,
    })

    return groups
  }, [])

  const fetchArchitecture = useCallback(async () => {
    if (!currentProject) return
    setLoading(true)
    setError('')
    try {
      const token = getToken()
      const [sourcesData, objectsRes] = await Promise.all([
        fetch(`${getApiBase()}/connector/sources?project_id=${currentProject.id}`, {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
        }).then(async (r) => {
          if (!r.ok) throw new Error(`HTTP ${r.status}`)
          const d = await r.json()
          return (Array.isArray(d) ? d : []) as DataSource[]
        }),
        api<{ data: OntObjectType[] }>(`/ontology/objects?projectId=${currentProject.id}`),
      ])

      const objects = objectsRes.data || []
      const groups = buildGroups(sourcesData, objects)

      const flowNodes: Node[] = groups.map((g) => ({
        id: (g as SourceGroupData & { id: string }).id,
        type: 'sourceNode',
        position: { x: 0, y: 0 },
        data: g as unknown as Record<string, unknown>,
      }))

      const laid = layoutGraph(flowNodes, [], { width: 200, height: 90 })
      setNodes(laid)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load data architecture')
    } finally {
      setLoading(false)
    }
  }, [currentProject, buildGroups, setNodes])

  useEffect(() => { fetchArchitecture() }, [fetchArchitecture])

  const handleNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    onOpenSource(node.data as unknown as SourceGroupData)
  }, [onOpenSource])

  // ─── Upload (Excel/CSV) — reuses the connector/file/upload POST ───
  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    setDragOver(false)
    const f = e.dataTransfer.files[0]
    if (f) {
      const ext = f.name.split('.').pop()?.toLowerCase()
      if (!['csv', 'xlsx', 'xls'].includes(ext ?? '')) {
        setUploadError('Unsupported file type. Use .csv, .xlsx or .xls.')
        return
      }
      setUploadFile(f)
      setUploadError('')
    }
  }, [])

  const handleUpload = async () => {
    if (!currentProject || !uploadFile) { setUploadError('Select a file first.'); return }
    setUploading(true)
    setUploadError('')
    try {
      const fd = new FormData()
      fd.append('file', uploadFile)
      fd.append('project_id', currentProject.id)
      fd.append('label', uploadLabel || uploadFile.name)
      const token = getToken()
      const res = await fetch(`${getApiBase()}/connector/file/upload`, {
        method: 'POST',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        body: fd,
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(data.error || data.message || `HTTP ${res.status}`)
      }
      msg.success('Uploaded')
      setUploadOpen(false)
      setUploadFile(null)
      setUploadLabel('')
      fetchArchitecture()
    } catch (e) {
      setUploadError(e instanceof Error ? e.message : 'Upload failed')
    } finally {
      setUploading(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      {/* Single page header (standard h-14, matches sibling pages) — title left,
          view toggle + actions right. */}
      <div className={`flex h-14 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${
        industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'
      }`}>
        <div className="flex min-w-0 items-center gap-3">
          {industrial ? (
            <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">// LAKEHOUSE</span>
          ) : (
            <>
              <Database size={18} className="text-ink" aria-hidden="true" />
              <h1 className="text-base font-semibold tracking-tight text-ink whitespace-nowrap">{t('title')}</h1>
            </>
          )}
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
          {headerSlot}
          <button
            onClick={fetchArchitecture}
            disabled={loading}
            className={`inline-flex h-7 w-7 items-center justify-center bg-white text-ink-muted outline-none hover:border-ink hover:text-ink disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink ${
              industrial ? 'border border-ink' : 'rounded-md border border-border'
            }`}
            title="Refresh"
          >
            <RefreshCw className={`h-3 w-3 ${loading ? 'animate-spin' : ''}`} />
          </button>
          <Button variant="primary" size="sm" onClick={() => { setUploadFile(null); setUploadLabel(''); setUploadError(''); setUploadOpen(true) }} disabled={!currentProject}>
            <Upload size={14} />
            上传 Excel/CSV
          </Button>
        </div>
      </div>

      {error && (
        <div
          className={`px-6 py-2 text-xs text-red-600 ${
            industrial ? 'border-b-2 border-danger bg-danger/5 font-mono tracking-[0.06em]' : 'border-b border-red-100 bg-red-50'
          }`}
        >
          {industrial ? `// ERROR · ${error}` : error}
        </div>
      )}

      <div className="relative flex flex-1 min-h-0">
        <ErCanvas
          nodes={nodes}
          edges={[]}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onNodeClick={handleNodeClick}
          miniMapNodeColor={(n) => {
            const type = (n.data as unknown as SourceGroupData).type
            if (type === 'postgres') return '#BFDBFE'
            if (type === 'sqlite') return '#A7F3D0'
            if (type === 'pbi') return '#FDE68A'
            return '#E5E7EB'
          }}
        />
        {loading && (
          <div className="absolute inset-0 z-10 flex items-center justify-center bg-white/70 text-sm text-ink-ghost">
            Loading…
          </div>
        )}
      </div>

      {/* Upload Excel/CSV Modal — reuses /connector/file/upload */}
      <Modal open={uploadOpen} onClose={() => !uploading && setUploadOpen(false)} title="上传 Excel/CSV" width="560px">
        <div className="space-y-4">
          <div>
            <label className="mb-1.5 block text-xs font-medium text-ink-muted">标签（可选）</label>
            <input
              type="text"
              value={uploadLabel}
              onChange={(e) => setUploadLabel(e.target.value)}
              placeholder="数据源名称"
              className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
            />
          </div>
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
              accept=".csv,.xlsx,.xls"
              onChange={(e) => { const f = e.target.files?.[0]; if (f) { setUploadFile(f); setUploadError('') } }}
              className="hidden"
            />
            {uploadFile ? (
              <div className="flex items-center justify-center gap-3">
                <FileText className="h-5 w-5 text-ink-muted" />
                <div className="text-left">
                  <p className="text-sm font-medium text-ink">{uploadFile.name}</p>
                  <p className="text-xs text-ink-ghost">{formatBytes(uploadFile.size)}</p>
                </div>
                <button
                  onClick={(e) => { e.stopPropagation(); setUploadFile(null) }}
                  className="ml-2 text-ink-ghost transition-colors hover:text-ink"
                >
                  <X className="h-4 w-4" />
                </button>
              </div>
            ) : (
              <div>
                <Upload className="mx-auto mb-3 h-8 w-8 text-ink-ghost opacity-50" />
                <p className="text-sm font-medium text-ink-muted">拖拽文件到此处，或点击选择</p>
                <p className="mt-1 text-xs text-ink-ghost">支持 .csv / .xlsx / .xls</p>
              </div>
            )}
          </div>

          {uploadError && (
            <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
              {uploadError}
            </div>
          )}

          <div className="flex justify-end gap-2 border-t border-border-light pt-3">
            <Button variant="ghost" size="sm" onClick={() => setUploadOpen(false)} disabled={uploading}>取消</Button>
            <Button variant="primary" size="sm" onClick={handleUpload} disabled={uploading || !uploadFile}>
              {uploading ? '上传中…' : '上传并解析'}
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  )
}
