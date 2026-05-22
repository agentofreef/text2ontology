'use client'

import { useState, useMemo, type ElementType } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { DataTable, Column } from '@/components/ui/DataTable'
import { Badge } from '@/components/ui/Badge'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { useFetch } from '@/lib/hooks'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { api, getApiBase } from '@/lib/api'
import { useStyleMode } from '@/lib/style-mode'
import type { OntObjectType } from '@/types/api'
import { Modal } from '@/components/ui/Modal'
import { BulkActionButton, ImpactRow } from '@/components/ui/BulkActionButton'
import { Pencil, Trash2, Database, Plus, Download, Upload, RefreshCw, FilePlus2, AlertTriangle, MoreHorizontal, Search, Check, X } from 'lucide-react'

type MarkFilter = 'all' | 'marked' | 'unmarked'
type ImportMode = 'merge' | 'skip' | 'replace'
type BulkEditField = 'kind' | 'description' | 'note'
type BulkTextMode = 'replace' | 'append'

interface BulkImpact {
  objects: number
  properties: number
  links: number
  keywords: number
  intents: number
  orphans: { knowledge: number; aliases: number }
}

interface CsvImportRow {
  name: string
  kind: 'entity' | 'event' | 'attribute'
  displayName?: string
  description?: string
  note?: string
}

interface BulkCreateResult {
  created: number
  total: number
  ids: string[]
  errors: { index: number; name?: string; error: string }[]
}

interface ObjectListViewProps {
  /** Narrow split-view rail layout (left column of the combined page). */
  compact?: boolean
  /** Currently selected object id (drives row highlight in compact mode). */
  selectedId?: string
  /** Row-click handler in compact mode — selects rather than navigates. */
  onSelect?: (id: string) => void
  /** Fired after any mutation so a parent can refresh sibling views (graph). */
  onMutated?: () => void
}

export function ObjectListView({ compact, selectedId, onSelect, onMutated }: ObjectListViewProps = {}) {
  const t = useTranslations('objects')
  const industrial = useStyleMode().mode === 'industrial'
  const router = useRouter()
  const { currentProject } = useProject()
  const reduce = useReducedMotion()
  const msg = useMessage()

  // Default to 'all' so freshly proposed Ods (mark=false) show up immediately.
  // Otherwise users land on a "0 active" empty page after builder-agent flows
  // and assume the proposal failed.
  const [markFilter, setMarkFilter] = useState<MarkFilter>('all')
  // Compact-mode local search (the wide DataTable owns its own searchbox).
  const [compactSearch, setCompactSearch] = useState('')
  // Compact-mode multi-select for batch actions (set of object ids).
  const [compactSelected, setCompactSelected] = useState<Set<string>>(new Set())
  // Compact-mode overflow "⋯" menu open state.
  const [overflowOpen, setOverflowOpen] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [createForm, setCreateForm] = useState({ name: '', kind: 'entity', description: '' })

  const [exporting, setExporting] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [importFile, setImportFile] = useState<File | null>(null)
  const [importPreview, setImportPreview] = useState<Record<string, number> | null>(null)
  const [importNewVersionTag, setImportNewVersionTag] = useState('')
  const [importMode, setImportMode] = useState<ImportMode>('merge')
  const [importing, setImporting] = useState(false)
  const [importResult, setImportResult] = useState<unknown>(null)

  // ─── Bulk operations state ─────────────────────────────────
  const [bulkBusy, setBulkBusy] = useState(false)
  const [bulkEditOpen, setBulkEditOpen] = useState(false)
  const [bulkEditTarget, setBulkEditTarget] = useState<{ ids: string[]; clear: () => void } | null>(null)
  const [bulkEditField, setBulkEditField] = useState<BulkEditField>('kind')
  const [bulkEditKind, setBulkEditKind] = useState<'entity' | 'event' | 'attribute'>('entity')
  const [bulkEditText, setBulkEditText] = useState('')
  const [bulkEditMode, setBulkEditMode] = useState<BulkTextMode>('replace')

  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false)
  const [bulkDeleteTarget, setBulkDeleteTarget] = useState<{ ids: string[]; clear: () => void } | null>(null)
  const [bulkDeleteImpact, setBulkDeleteImpact] = useState<BulkImpact | null>(null)
  const [bulkDeleteConfirm, setBulkDeleteConfirm] = useState('')

  // CSV/JSON bulk-create
  const [csvOpen, setCsvOpen] = useState(false)
  const [csvText, setCsvText] = useState('')
  const [csvParsed, setCsvParsed] = useState<CsvImportRow[]>([])
  const [csvParseError, setCsvParseError] = useState<string | null>(null)
  const [csvResult, setCsvResult] = useState<BulkCreateResult | null>(null)

  const { data: objects, refetch, loading } = useFetch<OntObjectType>('/ontology/objects')

  const filteredObjects = useMemo(() => {
    if (!objects) return []
    if (markFilter === 'marked') return objects.filter(o => o.mark)
    if (markFilter === 'unmarked') return objects.filter(o => !o.mark)
    return objects
  }, [objects, markFilter])

  const markedCount = objects?.filter(o => o.mark).length || 0
  const unmarkedCount = (objects?.length || 0) - markedCount
  const totalCount = objects?.length || 0

  // Compact rail also applies a free-text search on top of the mark filter.
  const compactObjects = useMemo(() => {
    const q = compactSearch.trim().toLowerCase()
    if (!q) return filteredObjects
    return filteredObjects.filter(o =>
      o.name.toLowerCase().includes(q) ||
      (o.displayName || '').toLowerCase().includes(q) ||
      (o.description || '').toLowerCase().includes(q)
    )
  }, [filteredObjects, compactSearch])

  // Compact-mode multi-select helpers. The "select all" affordance operates on
  // the currently-filtered+searched rows only (compactObjects).
  const clearCompactSelection = () => setCompactSelected(new Set())
  const toggleCompactSelected = (id: string) => {
    setCompactSelected(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }
  const allCompactSelected = compactObjects.length > 0 && compactObjects.every(o => compactSelected.has(o.id))
  const toggleSelectAllCompact = () => {
    if (allCompactSelected) {
      clearCompactSelection()
    } else {
      setCompactSelected(new Set(compactObjects.map(o => o.id)))
    }
  }

  const toggleMark = async (id: string, newValue: boolean) => {
    await api(`/ontology/objects/${id}/mark?projectId=${currentProject?.id}`, {
      method: 'PUT', body: { mark: newValue },
    })
    refetch()
    onMutated?.()
  }

  const deleteObject = async (id: string) => {
    if (!confirm(t('confirm_delete'))) return
    await api(`/ontology/objects/${id}?projectId=${currentProject?.id}`, { method: 'DELETE' })
    msg.success(t('deleted'))
    refetch()
    onMutated?.()
  }

  const handleCreate = async () => {
    if (!createForm.name.trim()) { msg.error(t('name_required')); return }
    try {
      const res = await api<{ id: string }>('/ontology/objects', {
        method: 'POST',
        body: {
          name: createForm.name.trim(),
          kind: createForm.kind,
          description: createForm.description,
          projectId: currentProject?.id,
          sourceType: 'manual',
          origin: 'manual-create',
        },
      })
      msg.success(t('created'))
      setCreateOpen(false)
      setCreateForm({ name: '', kind: 'entity', description: '' })
      router.push(`/ontology/lakehouse-objects/detail?id=${res.id}`)
    } catch (e: unknown) {
      msg.error(e instanceof Error ? e.message : t('create_failed'))
    }
  }

  const handleExport = async () => {
    if (!currentProject) { msg.error(t('no_project')); return }
    setExporting(true)
    try {
      const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
      const url = `${getApiBase()}/ontology/export?projectId=${currentProject.id}`
      const res = await fetch(url, {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      })
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }
      let filename = 'ontology-export.json'
      const cd = res.headers.get('Content-Disposition') || ''
      const mUtf = cd.match(/filename\*=UTF-8''([^;]+)/i)
      const mAscii = cd.match(/filename="([^"]+)"/i)
      if (mUtf) filename = decodeURIComponent(mUtf[1])
      else if (mAscii) filename = mAscii[1]
      const blob = await res.blob()
      const a = document.createElement('a')
      a.href = URL.createObjectURL(blob)
      a.download = filename
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(a.href)
      msg.success(t('export_success'))
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('export_failed'))
    } finally {
      setExporting(false)
    }
  }

  const handleFilePick = async (file: File | null) => {
    setImportFile(file)
    setImportPreview(null)
    setImportResult(null)
    if (!file) return
    try {
      const text = await file.text()
      const parsed = JSON.parse(text) as { meta?: { counts?: Record<string, number>, versionTag?: string }, version?: { versionTag?: string } }
      setImportPreview(parsed.meta?.counts || {})
      if (!importNewVersionTag) {
        const suggested = parsed.meta?.versionTag || parsed.version?.versionTag || ''
        setImportNewVersionTag(suggested ? `${suggested}-imported` : `imported-${new Date().toISOString().slice(0, 10)}`)
      }
    } catch (e) {
      msg.error(t('file_parse_failed') + (e instanceof Error ? e.message : 'invalid JSON'))
      setImportFile(null)
    }
  }

  // ─── Bulk handlers ─────────────────────────────────────────────
  const handleBulkMark = async (ids: string[], mark: boolean, clear: () => void) => {
    setBulkBusy(true)
    try {
      const res = await api<{ updated: number }>(
        `/ontology/objects/bulk-update?projectId=${currentProject?.id}`,
        { method: 'POST', body: { ids, mark } },
      )
      msg.success(t('bulk_mark_success', { action: mark ? t('bulk_mark_action_marked') : t('bulk_mark_action_unmarked'), count: res.updated }))
      clear()
      refetch()
      onMutated?.()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_mark_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  const openBulkEdit = (ids: string[], clear: () => void) => {
    setBulkEditTarget({ ids, clear })
    setBulkEditField('kind')
    setBulkEditKind('entity')
    setBulkEditText('')
    setBulkEditMode('replace')
    setBulkEditOpen(true)
  }

  const submitBulkEdit = async () => {
    if (!bulkEditTarget) return
    const { ids, clear } = bulkEditTarget
    setBulkBusy(true)
    try {
      let body: Record<string, unknown>
      if (bulkEditField === 'kind') {
        body = { ids, kind: bulkEditKind }
      } else if (bulkEditField === 'description') {
        body = { ids, description: bulkEditText, descriptionMode: bulkEditMode }
      } else {
        body = { ids, note: bulkEditText, noteMode: bulkEditMode }
      }
      const res = await api<{ updated: number }>(
        `/ontology/objects/bulk-update?projectId=${currentProject?.id}`,
        { method: 'POST', body },
      )
      msg.success(t('bulk_update_success', { count: res.updated }))
      setBulkEditOpen(false)
      setBulkEditTarget(null)
      clear()
      refetch()
      onMutated?.()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_edit_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  const openBulkDelete = async (ids: string[], clear: () => void) => {
    setBulkDeleteTarget({ ids, clear })
    setBulkDeleteImpact(null)
    setBulkDeleteConfirm('')
    setBulkDeleteOpen(true)
    try {
      const impact = await api<BulkImpact>(
        `/ontology/objects/bulk-impact?projectId=${currentProject?.id}`,
        { method: 'POST', body: { ids } },
      )
      setBulkDeleteImpact(impact)
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('load_impact_failed'))
      setBulkDeleteOpen(false)
    }
  }

  const submitBulkDelete = async () => {
    if (!bulkDeleteTarget) return
    if (bulkDeleteConfirm.trim() !== 'DELETE') {
      msg.error(t('delete_confirm_required'))
      return
    }
    const { ids, clear } = bulkDeleteTarget
    setBulkBusy(true)
    try {
      const res = await api<{ deleted: number }>(
        `/ontology/objects/bulk-delete?projectId=${currentProject?.id}`,
        { method: 'POST', body: { ids } },
      )
      msg.success(t('bulk_delete_success', { count: res.deleted }))
      setBulkDeleteOpen(false)
      setBulkDeleteTarget(null)
      clear()
      refetch()
      onMutated?.()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_delete_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  // ─── CSV / JSON bulk-create ────────────────────────────────────
  const parseCsvText = (text: string): CsvImportRow[] => {
    const trimmed = text.trim()
    if (!trimmed) return []
    // JSON path: array of objects or { items: [...] }
    if (trimmed.startsWith('[') || trimmed.startsWith('{')) {
      const parsed = JSON.parse(trimmed)
      const rawRows: unknown[] = Array.isArray(parsed)
        ? parsed
        : (parsed as { items?: unknown[] }).items || []
      return rawRows.map((r, i) => {
        const o = r as Record<string, unknown>
        const name = String(o.name || '').trim()
        if (!name) throw new Error(t('csv_row_name_empty', { row: i + 1 }))
        const kindRaw = String(o.kind || 'entity').trim().toLowerCase()
        if (!['entity', 'event', 'attribute'].includes(kindRaw)) {
          throw new Error(t('csv_row_kind_invalid', { row: i + 1 }))
        }
        return {
          name,
          kind: kindRaw as 'entity' | 'event' | 'attribute',
          displayName: o.displayName ? String(o.displayName) : undefined,
          description: o.description ? String(o.description) : undefined,
          note: o.note ? String(o.note) : undefined,
        }
      })
    }
    // CSV path: header row + data rows. Comma-separated, quote-aware.
    const lines = trimmed.split(/\r?\n/).filter(l => l.trim())
    if (lines.length < 2) throw new Error(t('csv_min_rows'))
    const split = (line: string): string[] => {
      const out: string[] = []
      let cur = ''
      let inQ = false
      for (let i = 0; i < line.length; i++) {
        const c = line[i]
        if (c === '"') {
          if (inQ && line[i + 1] === '"') { cur += '"'; i++ }
          else inQ = !inQ
        } else if (c === ',' && !inQ) {
          out.push(cur); cur = ''
        } else {
          cur += c
        }
      }
      out.push(cur)
      return out.map(s => s.trim())
    }
    const headers = split(lines[0]).map(h => h.toLowerCase())
    const idxName = headers.indexOf('name')
    const idxKind = headers.indexOf('kind')
    if (idxName < 0) throw new Error(t('csv_missing_name_col'))
    const idxDisplay = headers.indexOf('displayname')
    const idxDesc = headers.indexOf('description')
    const idxNote = headers.indexOf('note')
    return lines.slice(1).map((line, i) => {
      const cols = split(line)
      const name = (cols[idxName] || '').trim()
      if (!name) throw new Error(t('csv_row_name_empty', { row: i + 2 }))
      const kindRaw = idxKind >= 0 ? (cols[idxKind] || 'entity').trim().toLowerCase() : 'entity'
      if (!['entity', 'event', 'attribute'].includes(kindRaw)) {
        throw new Error(t('csv_row_kind_invalid', { row: i + 2 }))
      }
      return {
        name,
        kind: kindRaw as 'entity' | 'event' | 'attribute',
        displayName: idxDisplay >= 0 ? cols[idxDisplay] || undefined : undefined,
        description: idxDesc >= 0 ? cols[idxDesc] || undefined : undefined,
        note: idxNote >= 0 ? cols[idxNote] || undefined : undefined,
      }
    })
  }

  const tryParseCsv = (text: string) => {
    setCsvText(text)
    setCsvParseError(null)
    setCsvResult(null)
    if (!text.trim()) { setCsvParsed([]); return }
    try {
      setCsvParsed(parseCsvText(text))
    } catch (e) {
      setCsvParsed([])
      setCsvParseError(e instanceof Error ? e.message : t('parse_failed'))
    }
  }

  const submitBulkCreate = async () => {
    if (!currentProject || csvParsed.length === 0) return
    setBulkBusy(true)
    try {
      const res = await api<BulkCreateResult>(
        `/ontology/objects/bulk-create?projectId=${currentProject.id}`,
        { method: 'POST', body: { items: csvParsed } },
      )
      setCsvResult(res)
      if (res.errors.length === 0) {
        msg.success(t('bulk_create_success', { count: res.created }))
      } else {
        msg.warning(t('bulk_create_partial', { created: res.created, failed: res.errors.length }))
      }
      refetch()
      onMutated?.()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_create_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  const handleImport = async () => {
    if (!currentProject || !importFile) return
    if (!importNewVersionTag.trim()) { msg.error(t('version_tag_required')); return }
    setImporting(true)
    setImportResult(null)
    try {
      const text = await importFile.text()
      const payload = JSON.parse(text)
      const res = await api<{ success: boolean; summary: Record<string, unknown> }>(
        '/ontology/import',
        {
          method: 'POST',
          body: {
            projectId: currentProject.id,
            newVersionTag: importNewVersionTag.trim(),
            mode: importMode,
            payload,
          },
        },
      )
      setImportResult(res.summary)
      msg.success(t('import_success'))
      refetch()
      onMutated?.()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('import_failed'))
    } finally {
      setImporting(false)
    }
  }

  // ─── DataTable columns ─────────────────────────────────────────
  const columns: Column<OntObjectType>[] = [
    {
      key: 'mark', title: '', width: '32px',
      render: (_, row) => (
        <motion.button
          onClick={(e) => { e.stopPropagation(); toggleMark(row.id, !row.mark) }}
          whileHover={reduce ? undefined : { scale: 1.15 }}
          whileTap={reduce ? undefined : { scale: 0.9 }}
          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
          className="flex h-5 w-5 items-center justify-center outline-none focus-visible:ring-1 focus-visible:ring-ink rounded-full"
          aria-pressed={row.mark}
          aria-label={row.mark ? t('toggle_mark_off') : t('toggle_mark_on')}
          title={row.mark ? t('toggle_mark_off') : t('toggle_mark_on')}
        >
          <span
            className={`inline-block h-2 w-2 rounded-full transition-colors ${
              row.mark ? 'bg-ink' : 'bg-border'
            }`}
          />
        </motion.button>
      ),
    },
    {
      key: 'sql', title: '', width: '24px',
      render: (_, row) => {
        const validated = !!row.validatedAt
        const hasSql = !!row.semanticSql
        const cls = validated ? 'bg-success' : hasSql ? 'bg-ink-ghost' : 'bg-border'
        const title = validated
          ? t('sql_validated', { date: row.validatedAt })
          : hasSql ? t('sql_unvalidated') : t('sql_none')
        return (
          <span
            className={`inline-block h-2 w-2 rounded-full ${cls}`}
            title={title}
            aria-label={title}
          />
        )
      },
    },
    {
      key: 'name', title: t('col_name'), sortable: true,
      render: (_, row) => (
        <div>
          <div className="text-sm font-medium text-ink">{row.name}</div>
          {row.displayName && row.displayName !== row.name && (
            <div className="text-xs text-ink-ghost">{row.displayName}</div>
          )}
        </div>
      ),
    },
    {
      key: 'kind', title: t('col_kind'), width: '96px', sortable: true,
      render: (_, row) => <Badge>{row.kind}</Badge>,
    },
    {
      key: 'properties', title: t('col_properties'), width: '72px', sortable: true,
      render: (_, row) => (
        <span className="text-sm tabular-nums text-ink-muted">{row.properties?.length || 0}</span>
      ),
    },
    {
      key: 'mapping', title: t('col_mapped'), width: '96px', sortable: true,
      render: (_, row) => {
        const total = row.properties?.length || 0
        const mapped = row.properties?.filter(p => p.sourceColumn).length || 0
        const allMapped = total > 0 && mapped === total
        const tone = allMapped ? 'text-success' : mapped > 0 ? 'text-ink' : 'text-ink-ghost'
        return (
          <span className={`text-sm font-medium tabular-nums ${tone}`}>
            {mapped}/{total}
          </span>
        )
      },
    },
    {
      key: 'description', title: t('col_description'), sortable: true,
      render: (_, row) => (
        <span className="block max-w-[360px] truncate text-sm text-ink-muted">
          {row.description || '—'}
        </span>
      ),
    },
    {
      key: 'actions', title: '', width: '88px',
      render: (_, row) => (
        <div className="flex items-center gap-1">
          <AnimatedButton
            variant="ghost"
            size="xs"
            onClick={(e) => { e.stopPropagation(); router.push(`/ontology/lakehouse-objects/detail?id=${row.id}`) }}
            aria-label={t('edit_aria', { name: row.name })}
            title={t('edit_title')}
          >
            <Pencil size={12} aria-hidden="true" />
          </AnimatedButton>
          <AnimatedButton
            variant="ghost"
            size="xs"
            onClick={(e) => { e.stopPropagation(); deleteObject(row.id) }}
            aria-label={t('delete_aria', { name: row.name })}
            title={t('delete_title')}
            className="hover:text-danger"
          >
            <Trash2 size={12} aria-hidden="true" />
          </AnimatedButton>
        </div>
      ),
    },
  ]

  // ─────────────────────────────────────────────────────────────
  // SV Minimal · list-create-detail archetype
  // ─────────────────────────────────────────────────────────────
  // ─── Compact rail (split-view left column) ─────────────────────
  // Narrow alternative to the wide DataTable. Search + mark filter stay
  // visible; all toolbar actions collapse into an overflow "⋯" menu. Rows
  // SELECT (onSelect) rather than navigate; edit/create still navigate. The
  // shared modals (declared once below) are reused via the same open/close
  // state, triggered from the overflow menu.
  const compactRail = (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Toolbar: select-all + search + overflow menu */}
      <div className="flex flex-shrink-0 items-center gap-2 border-b border-border bg-white px-3 py-2">
        <button
          type="button"
          onClick={toggleSelectAllCompact}
          aria-pressed={allCompactSelected}
          aria-label={t('select_all')}
          title={t('select_all')}
          disabled={compactObjects.length === 0}
          className={`flex h-[18px] w-[18px] flex-shrink-0 items-center justify-center border outline-none transition-colors focus-visible:ring-1 focus-visible:ring-ink disabled:cursor-not-allowed disabled:opacity-40 ${
            allCompactSelected ? 'border-ink bg-ink text-white' : 'border-border bg-white text-transparent hover:border-ink'
          }`}
        >
          <Check size={12} strokeWidth={3} aria-hidden="true" />
        </button>
        <div className="relative flex-1">
          <Search size={13} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-ink-ghost" aria-hidden="true" />
          <input
            value={compactSearch}
            onChange={e => setCompactSearch(e.target.value)}
            placeholder={t('search_placeholder')}
            aria-label={t('search_placeholder')}
            className="w-full rounded-md border border-border bg-white py-1.5 pl-8 pr-2 text-sm text-ink outline-none placeholder:text-ink-ghost focus:border-ink focus:ring-1 focus:ring-ink/10"
          />
        </div>
        <div className="relative flex-shrink-0">
          <button
            type="button"
            onClick={() => setOverflowOpen(v => !v)}
            aria-label={t('more_actions')}
            title={t('more_actions')}
            aria-expanded={overflowOpen}
            className="inline-flex h-7 w-7 items-center justify-center rounded-md border border-border bg-white text-ink-muted outline-none hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
          >
            <MoreHorizontal size={14} aria-hidden="true" />
          </button>
          <AnimatePresence>
            {overflowOpen && (
              <>
                <button
                  type="button"
                  aria-hidden="true"
                  tabIndex={-1}
                  onClick={() => setOverflowOpen(false)}
                  className="fixed inset-0 z-40 cursor-default"
                />
                <MotionFadeMenu>
                  <OverflowItem icon={Plus} label={t('new_btn')} onClick={() => { setOverflowOpen(false); setCreateForm({ name: '', kind: 'entity', description: '' }); setCreateOpen(true) }} />
                  <OverflowItem icon={FilePlus2} label={t('bulk_create_btn')} onClick={() => { setOverflowOpen(false); setCsvText(''); setCsvParsed([]); setCsvParseError(null); setCsvResult(null); setCsvOpen(true) }} />
                  <OverflowItem icon={Upload} label={t('import_btn')} onClick={() => { setOverflowOpen(false); setImportFile(null); setImportPreview(null); setImportResult(null); setImportOpen(true) }} />
                  <OverflowItem icon={Download} label={exporting ? t('exporting') : t('export_btn')} onClick={() => { setOverflowOpen(false); handleExport() }} disabled={exporting} />
                  <div className="my-1 h-px bg-border-light" />
                  <OverflowItem icon={RefreshCw} label={t('refresh_title')} onClick={() => { setOverflowOpen(false); refetch() }} disabled={loading} />
                </MotionFadeMenu>
              </>
            )}
          </AnimatePresence>
        </div>
      </div>

      {/* Mark filter */}
      <div className="flex flex-shrink-0 items-center border-b border-border bg-white px-3 py-1.5">
        <SegmentedFilter
          value={markFilter}
          onChange={setMarkFilter}
          options={[
            ['all', t('filter_all', { count: totalCount })],
            ['marked', t('filter_marked', { count: markedCount })],
            ['unmarked', t('filter_unmarked', { count: unmarkedCount })],
          ]}
        />
      </div>

      {/* Batch action bar — only when rows are selected */}
      {compactSelected.size > 0 && (
        <div className="flex flex-shrink-0 flex-wrap items-center gap-1.5 border-b border-border bg-canvas-alt px-3 py-2">
          <span className="mr-auto text-[11px] font-medium tabular-nums text-ink">
            {t('batch_selected', { count: compactSelected.size })}
          </span>
          <BulkActionButton
            onClick={() => handleBulkMark([...compactSelected], true, clearCompactSelection)}
            disabled={bulkBusy}
            title={t('batch_mark')}
          >
            {t('batch_mark')}
          </BulkActionButton>
          <BulkActionButton
            onClick={() => handleBulkMark([...compactSelected], false, clearCompactSelection)}
            disabled={bulkBusy}
            title={t('batch_unmark')}
          >
            {t('batch_unmark')}
          </BulkActionButton>
          <BulkActionButton
            onClick={() => openBulkDelete([...compactSelected], clearCompactSelection)}
            disabled={bulkBusy}
            danger
            title={t('batch_delete')}
          >
            <Trash2 size={11} aria-hidden="true" />
            {t('batch_delete')}
          </BulkActionButton>
          <button
            type="button"
            onClick={clearCompactSelection}
            aria-label={t('clear_selection')}
            title={t('clear_selection')}
            className="inline-flex h-6 w-6 flex-shrink-0 items-center justify-center border border-border bg-white text-ink-ghost outline-none hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
          >
            <X size={12} aria-hidden="true" />
          </button>
        </div>
      )}

      {/* Rows */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        {loading && !objects ? (
          <div className="flex h-full items-center justify-center">
            <InlineLoader text={t('loading')} />
          </div>
        ) : compactObjects.length === 0 ? (
          <div className="flex h-full items-center justify-center px-4 text-center text-xs text-ink-ghost">
            {t('empty_list')}
          </div>
        ) : (
          <ul>
            {compactObjects.map(row => {
              const total = row.properties?.length || 0
              const mapped = row.properties?.filter(p => p.sourceColumn).length || 0
              const validated = !!row.validatedAt
              const hasSql = !!row.semanticSql
              const sqlCls = validated ? 'bg-success' : hasSql ? 'bg-ink-ghost' : 'bg-border'
              const isSelected = !!selectedId && row.id === selectedId
              const isChecked = compactSelected.has(row.id)
              return (
                <li key={row.id}>
                  <div
                    role="button"
                    tabIndex={0}
                    onClick={() => onSelect?.(row.id)}
                    onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelect?.(row.id) } }}
                    className={`flex w-full cursor-pointer items-center gap-2 border-b border-l-2 border-border-light px-3 py-2 text-left outline-none transition-colors focus-visible:ring-1 focus-visible:ring-ink ${
                      isSelected ? 'border-l-ink bg-canvas-alt' : 'border-l-transparent hover:bg-canvas-alt'
                    }`}
                  >
                    <button
                      type="button"
                      onClick={(e) => { e.stopPropagation(); toggleCompactSelected(row.id) }}
                      aria-pressed={isChecked}
                      aria-label={isChecked ? t('clear_selection') : t('select_all')}
                      className={`flex h-[18px] w-[18px] flex-shrink-0 items-center justify-center border outline-none transition-colors focus-visible:ring-1 focus-visible:ring-ink ${
                        isChecked ? 'border-ink bg-ink text-white' : 'border-border bg-white text-transparent hover:border-ink'
                      }`}
                    >
                      <Check size={12} strokeWidth={3} aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      onClick={(e) => { e.stopPropagation(); toggleMark(row.id, !row.mark) }}
                      className="flex h-5 w-5 flex-shrink-0 items-center justify-center rounded-full outline-none focus-visible:ring-1 focus-visible:ring-ink"
                      aria-pressed={row.mark}
                      aria-label={row.mark ? t('toggle_mark_off') : t('toggle_mark_on')}
                      title={row.mark ? t('toggle_mark_off') : t('toggle_mark_on')}
                    >
                      <span className={`inline-block h-2 w-2 rounded-full transition-colors ${row.mark ? 'bg-ink' : 'bg-border'}`} />
                    </button>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm font-medium text-ink">{row.name}</span>
                        <span className={`inline-block h-1.5 w-1.5 flex-shrink-0 rounded-full ${sqlCls}`} aria-hidden="true" />
                      </div>
                      {row.displayName && row.displayName !== row.name && (
                        <div className="truncate text-[11px] text-ink-ghost">{row.displayName}</div>
                      )}
                    </div>
                    <span className="flex-shrink-0 text-[10px] tabular-nums text-ink-ghost">{mapped}/{total}</span>
                    <Badge className="flex-shrink-0">{row.kind}</Badge>
                    <button
                      type="button"
                      onClick={(e) => { e.stopPropagation(); router.push(`/ontology/lakehouse-objects/detail?id=${row.id}`) }}
                      aria-label={t('edit_aria', { name: row.name })}
                      title={t('edit_title')}
                      className="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      <Pencil size={12} aria-hidden="true" />
                    </button>
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {compact ? compactRail : (
      <>
      {/* Header — h-14 to align with Sidebar brand row; industrial uses 2px ink rule */}
      <motion.header
        initial={reduce ? undefined : { opacity: 0, y: -4 }}
        animate={reduce ? undefined : { opacity: 1, y: 0 }}
        transition={{ duration: 0.2, ease: 'easeOut' }}
        className={`flex h-14 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'}`}
      >
        <div className="flex min-w-0 items-center gap-3">
          {industrial ? (
            <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // {t('page_title').toString().toUpperCase()}
            </span>
          ) : (
            <>
              <Database size={18} className="text-ink" aria-hidden="true" />
              <h1 className="text-base font-semibold tracking-tight text-ink whitespace-nowrap">
                {t('page_title')}
              </h1>
            </>
          )}
          <span className={industrial ? 'font-mono text-[10px] tracking-[0.14em] text-ink-muted truncate tabular-nums' : 'text-xs text-ink-ghost truncate'}>
            {industrial
              ? `${totalCount} OBJECTS`
              : t('page_subtitle', { count: totalCount })}
          </span>
        </div>

        <div className="flex flex-shrink-0 flex-wrap items-center gap-2">
          <SegmentedFilter
            value={markFilter}
            onChange={setMarkFilter}
            options={[
              ['all', t('filter_all', { count: totalCount })],
              ['marked', t('filter_marked', { count: markedCount })],
              ['unmarked', t('filter_unmarked', { count: unmarkedCount })],
            ]}
          />
          <motion.button
            onClick={refetch}
            disabled={loading}
            whileHover={reduce || loading ? undefined : { scale: 1.05 }}
            whileTap={reduce || loading ? undefined : { scale: 0.95 }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            aria-label={t('refresh_aria')}
            title={t('refresh_title')}
            className={`inline-flex h-7 w-7 items-center justify-center bg-white text-ink-muted outline-none hover:border-ink hover:text-ink disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-ink ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}
          >
            <motion.span
              animate={reduce ? undefined : loading ? { rotate: 360 } : { rotate: 0 }}
              transition={loading ? { repeat: Infinity, duration: 1, ease: 'linear' } : { duration: 0 }}
              className="inline-flex"
            >
              <RefreshCw size={12} aria-hidden="true" />
            </motion.span>
          </motion.button>
          <AnimatedButton
            variant="secondary"
            size="sm"
            onClick={handleExport}
            disabled={exporting}
            aria-label={t('export_aria')}
          >
            <Download size={12} aria-hidden="true" />
            {exporting ? t('exporting') : t('export_btn')}
          </AnimatedButton>
          <AnimatedButton
            variant="secondary"
            size="sm"
            onClick={() => { setImportFile(null); setImportPreview(null); setImportResult(null); setImportOpen(true) }}
            aria-label={t('import_aria')}
          >
            <Upload size={12} aria-hidden="true" />
            {t('import_btn')}
          </AnimatedButton>
          <AnimatedButton
            variant="secondary"
            size="sm"
            onClick={() => { setCsvText(''); setCsvParsed([]); setCsvParseError(null); setCsvResult(null); setCsvOpen(true) }}
            aria-label={t('bulk_create_aria')}
            title={t('bulk_create_title')}
          >
            <FilePlus2 size={12} aria-hidden="true" />
            {t('bulk_create_btn')}
          </AnimatedButton>
          <AnimatedButton
            variant="primary"
            size="sm"
            onClick={() => { setCreateForm({ name: '', kind: 'entity', description: '' }); setCreateOpen(true) }}
            aria-label={t('new_aria')}
          >
            <Plus size={12} aria-hidden="true" />
            {t('new_btn')}
          </AnimatedButton>
        </div>
      </motion.header>

      {/* Content — DataTable inside flex-1 scrollable container */}
      <div className="flex flex-1 min-h-0 flex-col">
        {loading && !objects ? (
          <div className="flex h-full items-center justify-center">
            <InlineLoader text={t('loading')} />
          </div>
        ) : (
          <div className="flex-1 min-h-0 overflow-y-auto">
            <DataTable
              columns={columns}
              data={filteredObjects}
              rowKey="id"
              searchable
              batchActions={(selectedIds, clearSelection) => (
                <div className="flex flex-wrap items-center gap-1.5">
                  <BulkActionButton
                    onClick={() => handleBulkMark(selectedIds, true, clearSelection)}
                    disabled={bulkBusy}
                    title={t('batch_mark_title')}
                  >
                    {t('batch_mark_btn')}
                  </BulkActionButton>
                  <BulkActionButton
                    onClick={() => handleBulkMark(selectedIds, false, clearSelection)}
                    disabled={bulkBusy}
                    title={t('batch_unmark_title')}
                  >
                    {t('batch_unmark_btn')}
                  </BulkActionButton>
                  <span className="h-3.5 w-px bg-border" aria-hidden="true" />
                  <BulkActionButton
                    onClick={() => openBulkEdit(selectedIds, clearSelection)}
                    disabled={bulkBusy}
                    title={t('batch_edit_title')}
                  >
                    <Pencil size={11} aria-hidden="true" />
                    {t('batch_edit_btn')}
                  </BulkActionButton>
                  <BulkActionButton
                    onClick={() => openBulkDelete(selectedIds, clearSelection)}
                    disabled={bulkBusy}
                    danger
                    title={t('batch_delete_title')}
                  >
                    <Trash2 size={11} aria-hidden="true" />
                    {t('batch_delete_btn')}
                  </BulkActionButton>
                </div>
              )}
            />
          </div>
        )}
      </div>
      </>
      )}

      {/* Import Modal */}
      <Modal open={importOpen} onClose={() => !importing && setImportOpen(false)} title={t('import_modal_title')} width="640px">
        <div className="space-y-4">
          <p className="text-sm text-ink-muted leading-relaxed">
            {t('import_modal_desc')}
          </p>
          <div>
            <label htmlFor="import-file" className="mb-1.5 block text-sm font-medium text-ink">{t('import_file_label')}</label>
            <input
              id="import-file"
              type="file"
              accept=".json,application/json"
              onChange={(e) => handleFilePick(e.target.files?.[0] || null)}
              aria-label={t('import_file_aria')}
              className="block w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink focus:ring-1 focus:ring-ink/10"
            />
            <AnimatePresence>
              {importPreview && (
                <motion.div
                  initial={reduce ? undefined : { opacity: 0, y: -4 }}
                  animate={reduce ? undefined : { opacity: 1, y: 0 }}
                  exit={reduce ? undefined : { opacity: 0, y: -4 }}
                  transition={{ duration: 0.15 }}
                  className="mt-2 grid grid-cols-4 gap-1 rounded-md border border-border bg-canvas-alt p-2"
                >
                  {Object.entries(importPreview).map(([k, v]) => (
                    <div key={k} className="text-xs">
                      <span className="text-ink-ghost">{k}:</span>{' '}
                      <span className="font-semibold tabular-nums text-ink">{v}</span>
                    </div>
                  ))}
                </motion.div>
              )}
            </AnimatePresence>
          </div>
          <div>
            <label htmlFor="import-version" className="mb-1.5 block text-sm font-medium text-ink">{t('import_version_label')}</label>
            <input
              id="import-version"
              value={importNewVersionTag}
              onChange={(e) => setImportNewVersionTag(e.target.value)}
              placeholder="new-version-tag"
              aria-label={t('import_version_aria')}
              className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none placeholder:text-ink-ghost focus:border-ink focus:ring-1 focus:ring-ink/10"
            />
          </div>
          <div>
            <label className="mb-1.5 block text-sm font-medium text-ink">{t('import_mode_label')}</label>
            <SegmentedFilter<ImportMode>
              value={importMode}
              onChange={setImportMode}
              options={[
                ['merge', 'Merge'],
                ['skip', 'Skip'],
                ['replace', 'Replace'],
              ]}
            />
            <p className="mt-1.5 text-xs text-ink-ghost">
              {importMode === 'merge' && t('import_mode_merge')}
              {importMode === 'skip' && t('import_mode_skip')}
              {importMode === 'replace' && t('import_mode_replace')}
            </p>
          </div>
          {importResult != null && (
            <motion.div
              initial={reduce ? undefined : { opacity: 0 }}
              animate={reduce ? undefined : { opacity: 1 }}
              transition={{ duration: 0.15 }}
              className="rounded-md border border-success/30 bg-success/10 p-3 font-mono text-xs text-success whitespace-pre-wrap"
            >
              {JSON.stringify(importResult, null, 2)}
            </motion.div>
          )}
          <div className="flex justify-end gap-2 border-t border-border-light pt-3">
            <AnimatedButton
              variant="ghost"
              size="md"
              onClick={() => setImportOpen(false)}
              disabled={importing}
              aria-label={t('import_cancel_aria')}
            >
              {t('import_cancel_btn')}
            </AnimatedButton>
            <AnimatedButton
              variant="primary"
              size="md"
              onClick={handleImport}
              disabled={importing || !importFile || !importNewVersionTag.trim()}
              aria-label={t('import_submit_aria')}
            >
              {importing && (
                <motion.span
                  animate={reduce ? undefined : { rotate: 360 }}
                  transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
                  className="inline-flex"
                >
                  <RefreshCw size={12} aria-hidden="true" />
                </motion.span>
              )}
              {importing ? t('importing') : t('import_submit_btn')}
            </AnimatedButton>
          </div>
        </div>
      </Modal>

      {/* Create Modal */}
      <Modal open={createOpen} onClose={() => setCreateOpen(false)} title={t('create_modal_title')}>
        <div className="space-y-3">
          <div>
            <label htmlFor="create-name" className="mb-1.5 block text-sm font-medium text-ink">{t('create_name_label')}</label>
            <input
              id="create-name"
              value={createForm.name}
              onChange={e => setCreateForm(f => ({ ...f, name: e.target.value }))}
              placeholder={t('create_name_placeholder')}
              aria-label={t('create_name_aria')}
              className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none placeholder:text-ink-ghost focus:border-ink focus:ring-1 focus:ring-ink/10"
            />
          </div>
          <div>
            <label htmlFor="create-kind" className="mb-1.5 block text-sm font-medium text-ink">{t('create_kind_label')}</label>
            <select
              id="create-kind"
              value={createForm.kind}
              onChange={e => setCreateForm(f => ({ ...f, kind: e.target.value }))}
              aria-label={t('create_kind_aria')}
              className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink focus:ring-1 focus:ring-ink/10"
            >
              <option value="entity">{t('create_kind_entity')}</option>
              <option value="event">{t('create_kind_event')}</option>
              <option value="attribute">{t('create_kind_attribute')}</option>
            </select>
          </div>
          <div>
            <label htmlFor="create-desc" className="mb-1.5 block text-sm font-medium text-ink">{t('create_desc_label')}</label>
            <textarea
              id="create-desc"
              value={createForm.description}
              onChange={e => setCreateForm(f => ({ ...f, description: e.target.value }))}
              rows={3}
              placeholder={t('create_desc_placeholder')}
              aria-label={t('create_desc_aria')}
              className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none placeholder:text-ink-ghost focus:border-ink focus:ring-1 focus:ring-ink/10 resize-none"
            />
          </div>
          <div className="flex justify-end gap-2 pt-1">
            <AnimatedButton
              variant="ghost"
              size="md"
              onClick={() => setCreateOpen(false)}
              aria-label={t('cancel')}
            >
              {t('cancel')}
            </AnimatedButton>
            <AnimatedButton
              variant="primary"
              size="md"
              onClick={handleCreate}
              aria-label={t('create_submit_aria')}
            >
              {t('create_submit_btn')}
            </AnimatedButton>
          </div>
        </div>
      </Modal>

      {/* Bulk Edit Modal */}
      <Modal
        open={bulkEditOpen}
        onClose={() => !bulkBusy && setBulkEditOpen(false)}
        title={t('bulk_edit_modal_title', { count: bulkEditTarget?.ids.length || 0 })}
        width="520px"
      >
        <div className="space-y-4">
          <div>
            <label className="mb-1.5 block text-sm font-medium text-ink">{t('bulk_edit_field_label')}</label>
            <SegmentedFilter<BulkEditField>
              value={bulkEditField}
              onChange={setBulkEditField}
              options={[
                ['kind', t('bulk_edit_field_kind')],
                ['description', t('bulk_edit_field_description')],
                ['note', t('bulk_edit_field_note')],
              ]}
            />
          </div>
          {bulkEditField === 'kind' ? (
            <div>
              <label htmlFor="bulk-kind" className="mb-1.5 block text-sm font-medium text-ink">{t('bulk_edit_new_kind_label')}</label>
              <select
                id="bulk-kind"
                value={bulkEditKind}
                onChange={e => setBulkEditKind(e.target.value as 'entity' | 'event' | 'attribute')}
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink focus:ring-1 focus:ring-ink/10"
              >
                <option value="entity">{t('create_kind_entity')}</option>
                <option value="event">{t('create_kind_event')}</option>
                <option value="attribute">{t('create_kind_attribute')}</option>
              </select>
              <p className="mt-1.5 text-xs text-ink-ghost">{t('bulk_edit_kind_hint', { kind: bulkEditKind })}</p>
            </div>
          ) : (
            <>
              <div>
                <label className="mb-1.5 block text-sm font-medium text-ink">{t('bulk_edit_write_mode_label')}</label>
                <SegmentedFilter<BulkTextMode>
                  value={bulkEditMode}
                  onChange={setBulkEditMode}
                  options={[
                    ['replace', t('bulk_edit_mode_replace')],
                    ['append', t('bulk_edit_mode_append')],
                  ]}
                />
                <p className="mt-1.5 text-xs text-ink-ghost">
                  {bulkEditMode === 'replace'
                    ? t('bulk_edit_mode_replace_hint')
                    : t('bulk_edit_mode_append_hint')}
                </p>
              </div>
              <div>
                <label htmlFor="bulk-text" className="mb-1.5 block text-sm font-medium text-ink">
                  {bulkEditField === 'description' ? t('bulk_edit_text_label_description') : t('bulk_edit_text_label_note')}
                </label>
                <textarea
                  id="bulk-text"
                  value={bulkEditText}
                  onChange={e => setBulkEditText(e.target.value)}
                  rows={4}
                  placeholder={bulkEditField === 'description' ? t('bulk_edit_placeholder_description') : t('bulk_edit_placeholder_note')}
                  className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none placeholder:text-ink-ghost focus:border-ink focus:ring-1 focus:ring-ink/10 resize-none"
                />
              </div>
            </>
          )}
          <div className="flex justify-end gap-2 border-t border-border-light pt-3">
            <AnimatedButton variant="ghost" size="md" onClick={() => setBulkEditOpen(false)} disabled={bulkBusy}>{t('bulk_edit_cancel')}</AnimatedButton>
            <AnimatedButton
              variant="primary"
              size="md"
              onClick={submitBulkEdit}
              disabled={bulkBusy || (bulkEditField !== 'kind' && bulkEditText.trim() === '')}
            >
              {bulkBusy ? t('bulk_edit_submitting') : t('bulk_edit_submit')}
            </AnimatedButton>
          </div>
        </div>
      </Modal>

      {/* Bulk Delete Modal — cascade preview + DELETE confirm */}
      <Modal
        open={bulkDeleteOpen}
        onClose={() => !bulkBusy && setBulkDeleteOpen(false)}
        title={t('bulk_delete_modal_title')}
        width="560px"
      >
        <div className="space-y-4">
          <div className="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/5 p-3">
            <AlertTriangle size={16} className="mt-0.5 flex-shrink-0 text-danger" aria-hidden="true" />
            <div className="text-sm text-ink leading-relaxed">
              {t('bulk_delete_warning')}
            </div>
          </div>

          {bulkDeleteImpact === null ? (
            <div className="flex items-center gap-2 text-sm text-ink-muted">
              <motion.span
                animate={reduce ? undefined : { rotate: 360 }}
                transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
                className="inline-flex"
              >
                <RefreshCw size={12} aria-hidden="true" />
              </motion.span>
              {t('bulk_delete_loading')}
            </div>
          ) : (
            <>
              <div className="grid grid-cols-2 gap-2 text-sm">
                <ImpactRow label={`${t('col_name')} (ont_object_type)`} value={bulkDeleteImpact.objects} primary />
                <ImpactRow label={`${t('col_properties')} (ont_property)`} value={bulkDeleteImpact.properties} />
                <ImpactRow label="Link (ont_link_type)" value={bulkDeleteImpact.links} />
                <ImpactRow label="Keyword (lakehouse_keyword)" value={bulkDeleteImpact.keywords} />
                <ImpactRow label="Intent (lakehouse_metric_intent)" value={bulkDeleteImpact.intents} />
              </div>
              {(bulkDeleteImpact.orphans.knowledge > 0 || bulkDeleteImpact.orphans.aliases > 0) && (
                <div className="rounded-md border border-warning/30 bg-warning/5 p-3 text-xs leading-relaxed text-ink-muted">
                  <span className="font-medium text-ink">{t('bulk_delete_orphan_note')}</span>
                  {' '}{t('bulk_delete_orphan_desc')}
                  <ul className="mt-1 list-disc pl-4">
                    {bulkDeleteImpact.orphans.knowledge > 0 && <li>{t('bulk_delete_orphan_knowledge', { count: bulkDeleteImpact.orphans.knowledge })}</li>}
                    {bulkDeleteImpact.orphans.aliases > 0 && <li>{t('bulk_delete_orphan_aliases', { count: bulkDeleteImpact.orphans.aliases })}</li>}
                  </ul>
                </div>
              )}
              <div>
                <label htmlFor="bulk-del-confirm" className="mb-1.5 block text-sm font-medium text-ink">
                  {t('bulk_delete_confirm_label')}
                </label>
                <input
                  id="bulk-del-confirm"
                  value={bulkDeleteConfirm}
                  onChange={e => setBulkDeleteConfirm(e.target.value)}
                  placeholder="DELETE"
                  autoComplete="off"
                  spellCheck={false}
                  className="w-full rounded-md border border-border bg-white px-3 py-2 font-mono text-sm text-ink outline-none placeholder:text-ink-ghost focus:border-danger focus:ring-1 focus:ring-danger/20"
                />
              </div>
            </>
          )}

          <div className="flex justify-end gap-2 border-t border-border-light pt-3">
            <AnimatedButton variant="ghost" size="md" onClick={() => setBulkDeleteOpen(false)} disabled={bulkBusy}>{t('bulk_delete_cancel')}</AnimatedButton>
            <button
              type="button"
              onClick={submitBulkDelete}
              disabled={bulkBusy || bulkDeleteImpact === null || bulkDeleteConfirm.trim() !== 'DELETE'}
              className="inline-flex h-9 items-center gap-1.5 rounded-md border border-danger bg-danger px-3 text-sm font-medium text-white outline-none hover:bg-danger/90 disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-2 focus-visible:ring-danger/40"
            >
              {bulkBusy ? t('bulk_delete_deleting') : t('bulk_delete_submit', { count: bulkDeleteTarget?.ids.length || 0 })}
            </button>
          </div>
        </div>
      </Modal>

      {/* CSV/JSON Bulk Create Modal */}
      <Modal
        open={csvOpen}
        onClose={() => !bulkBusy && setCsvOpen(false)}
        title={t('csv_modal_title')}
        width="720px"
      >
        <div className="space-y-4">
          <div className="rounded-md border border-border-light bg-canvas-alt p-3 text-xs leading-relaxed text-ink-muted">
            <div className="mb-1 font-medium text-ink">{t('csv_format_header')}</div>
            <code className="block font-mono text-[11px] text-ink">name,kind,displayName,description,note</code>
            <div className="mt-1.5">{t('csv_or_json')}<code className="font-mono text-[11px] text-ink">[{`{"name":"Order","kind":"entity","description":"..."}`}]</code></div>
            <div className="mt-1.5">{t('csv_required_note')}<code className="font-mono text-ink">name</code> · default kind=<code className="font-mono text-ink">entity</code></div>
          </div>
          <div>
            <label htmlFor="csv-text" className="mb-1.5 block text-sm font-medium text-ink">{t('csv_data_label')}</label>
            <textarea
              id="csv-text"
              value={csvText}
              onChange={e => tryParseCsv(e.target.value)}
              rows={8}
              spellCheck={false}
              placeholder={"name,kind,description\nOrder,entity,Order Entity\nShipment,event,Shipment Event"}
              className="w-full rounded-md border border-border bg-white px-3 py-2 font-mono text-xs text-ink outline-none placeholder:text-ink-ghost focus:border-ink focus:ring-1 focus:ring-ink/10 resize-none"
            />
            {csvParseError && (
              <div className="mt-1.5 text-xs text-danger">{csvParseError}</div>
            )}
          </div>
          {csvParsed.length > 0 && !csvResult && (
            <div className="rounded-md border border-border-light bg-canvas-alt">
              <div className="border-b border-border-light px-3 py-1.5 text-xs font-medium text-ink">
                {t('csv_preview_header', { count: csvParsed.length })}
              </div>
              <div className="max-h-48 overflow-y-auto">
                <table className="w-full text-left text-xs">
                  <thead className="bg-canvas-alt text-ink-muted">
                    <tr>
                      <th className="px-3 py-1.5 font-medium">name</th>
                      <th className="px-3 py-1.5 font-medium">kind</th>
                      <th className="px-3 py-1.5 font-medium">description</th>
                    </tr>
                  </thead>
                  <tbody>
                    {csvParsed.slice(0, 50).map((r, i) => (
                      <tr key={i} className="border-t border-border-light">
                        <td className="px-3 py-1 font-mono text-ink">{r.name}</td>
                        <td className="px-3 py-1 text-ink-muted">{r.kind}</td>
                        <td className="px-3 py-1 text-ink-muted truncate max-w-[280px]">{r.description || '—'}</td>
                      </tr>
                    ))}
                    {csvParsed.length > 50 && (
                      <tr><td colSpan={3} className="px-3 py-1 text-center text-[11px] text-ink-ghost">{t('csv_more_rows', { count: csvParsed.length - 50 })}</td></tr>
                    )}
                  </tbody>
                </table>
              </div>
            </div>
          )}
          {csvResult && (
            <div className="space-y-2">
              <div className="flex items-center gap-3 rounded-md border border-border-light bg-canvas-alt p-3 text-sm">
                <span className="font-mono tabular-nums text-success">✓ {csvResult.created}</span>
                <span className="text-ink-muted">{t('csv_success_label')}</span>
                <span className="h-3.5 w-px bg-border" />
                <span className="font-mono tabular-nums text-danger">✗ {csvResult.errors.length}</span>
                <span className="text-ink-muted">{t('csv_failed_label')}</span>
                <span className="h-3.5 w-px bg-border" />
                <span className="font-mono tabular-nums text-ink-ghost">/ {csvResult.total}</span>
                <span className="text-ink-ghost">{t('csv_total_label')}</span>
              </div>
              {csvResult.errors.length > 0 && (
                <div className="rounded-md border border-danger/30 bg-danger/5">
                  <div className="border-b border-danger/30 px-3 py-1.5 text-xs font-medium text-danger">{t('csv_errors_title')}</div>
                  <ul className="max-h-40 overflow-y-auto divide-y divide-danger/20 text-xs">
                    {csvResult.errors.map(e => (
                      <li key={e.index} className="px-3 py-1.5">
                        <span className="font-mono text-ink-muted">#{e.index + 1}</span>{' '}
                        {e.name && <span className="font-medium text-ink">{e.name}</span>}{' '}
                        <span className="text-danger">{e.error}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          )}
          <div className="flex justify-end gap-2 border-t border-border-light pt-3">
            <AnimatedButton variant="ghost" size="md" onClick={() => setCsvOpen(false)} disabled={bulkBusy}>
              {csvResult ? t('csv_close') : t('csv_cancel')}
            </AnimatedButton>
            {!csvResult && (
              <AnimatedButton
                variant="primary"
                size="md"
                onClick={submitBulkCreate}
                disabled={bulkBusy || csvParsed.length === 0 || !!csvParseError}
              >
                {bulkBusy ? t('csv_creating') : t('csv_create_submit', { count: csvParsed.length })}
              </AnimatedButton>
            )}
          </div>
        </div>
      </Modal>
    </div>
  )
}

// BulkActionButton and ImpactRow are imported from @/components/ui/BulkActionButton

// ─────────────────────────────────────────────────────────────
// Sub-components
// ─────────────────────────────────────────────────────────────

/** Right-aligned overflow dropdown panel for the compact rail toolbar. */
function MotionFadeMenu({ children }: { children: React.ReactNode }) {
  const reduce = useReducedMotion()
  return (
    <motion.div
      initial={reduce ? undefined : { opacity: 0, y: -4 }}
      animate={reduce ? undefined : { opacity: 1, y: 0 }}
      exit={reduce ? undefined : { opacity: 0, y: -4 }}
      transition={{ duration: 0.12, ease: 'easeOut' }}
      role="menu"
      className="absolute right-0 z-50 mt-1 w-44 rounded-md border border-border bg-white py-1 shadow-sm"
    >
      {children}
    </motion.div>
  )
}

/** Single row inside the overflow menu. */
function OverflowItem({
  icon: Icon, label, onClick, disabled,
}: {
  icon: ElementType
  label: string
  onClick: () => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      disabled={disabled}
      className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-ink-muted outline-none transition-colors hover:bg-canvas-alt hover:text-ink disabled:cursor-not-allowed disabled:opacity-40 focus-visible:bg-canvas-alt"
    >
      <Icon size={13} aria-hidden="true" className="flex-shrink-0 text-ink-ghost" />
      <span className="truncate">{label}</span>
    </button>
  )
}

/**
 * Segmented filter with motion sliding background (SV layoutId pattern).
 * In industrial mode the chrome flips to mono labels + square ink fill.
 */
function SegmentedFilter<T extends string>({
  value, onChange, options,
}: {
  value: T
  onChange: (v: T) => void
  options: [T, string][]
}) {
  const reduce = useReducedMotion()
  const industrial = useStyleMode().mode === 'industrial'
  // Unique per-instance layoutId so multiple Segmented can coexist
  const [layoutId] = useState(() => `seg-${Math.random().toString(36).slice(2, 9)}`)
  return (
    <div
      role="radiogroup"
      className={`relative flex h-7 items-center p-0.5 ${
        industrial ? 'border border-ink bg-white' : 'rounded-md border border-border bg-canvas-alt'
      }`}
    >
      {options.map(([v, label]) => {
        const selected = value === v
        return (
          <button
            key={v}
            role="radio"
            aria-checked={selected}
            onClick={() => onChange(v)}
            className={`relative z-10 px-2.5 py-0.5 outline-none focus-visible:ring-1 focus-visible:ring-ink transition-colors ${
              industrial
                ? 'font-mono text-[10px] uppercase tracking-[0.14em]'
                : 'rounded-[5px] text-[11px] font-medium'
            }`}
          >
            {selected && (
              <motion.span
                layoutId={layoutId}
                className={`absolute inset-0 ${industrial ? 'bg-ink' : 'rounded-[5px] bg-white shadow-sm'}`}
                transition={reduce ? { duration: 0 } : { type: 'spring', stiffness: 500, damping: 35 }}
              />
            )}
            <span
              className={`relative ${
                selected
                  ? industrial
                    ? 'text-white'
                    : 'text-ink'
                  : 'text-ink-muted hover:text-ink'
              }`}
            >
              {label}
            </span>
          </button>
        )
      })}
    </div>
  )
}

function InlineLoader({ text }: { text: string }) {
  const reduce = useReducedMotion()
  return (
    <div className="flex items-center gap-2 text-sm text-ink-muted">
      <motion.span
        animate={reduce ? undefined : { rotate: 360 }}
        transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
        className="inline-flex"
      >
        <RefreshCw size={14} aria-hidden="true" />
      </motion.span>
      <span>{text}</span>
    </div>
  )
}
