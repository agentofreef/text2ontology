'use client'

import { useState, useMemo, useCallback, useRef } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { DataTable, Column } from '@/components/ui/DataTable'
import { Modal } from '@/components/ui/Modal'
import { Input } from '@/components/ui/Input'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { BulkActionButton, ImpactRow } from '@/components/ui/BulkActionButton'
import { useFetch } from '@/lib/hooks'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { api } from '@/lib/api'
import { useStyleMode } from '@/lib/style-mode'
import type { OntMetricIntent, OntObjectType } from '@/types/api'
import {
  Plus, Pencil, Trash2, X, Sparkles,
  RefreshCw, FilePlus2, AlertTriangle,
} from 'lucide-react'

// ─── Types ──────────────────────────────────────────────────────────────────

type MarkFilter = 'all' | 'marked' | 'unmarked'
type BulkEditField = 'mark' | 'priority' | 'objectId'

interface BulkImpact { intents: number; keywords: number }
interface BulkCsvRow {
  name: string
  objectId: string
  canonicalMetric: string
  displayName?: string
  priority?: number
  description?: string
  mark?: boolean
}
interface BulkCreateResult {
  created: number
  total: number
  ids: string[]
  errors: { index: number; name?: string; error: string }[]
}

type TriggerRow = {
  id: string
  keyword: string
  aliases: string[]
  syncedAt: string
  usageCount?: number
}

// ─── Page ───────────────────────────────────────────────────────────────────

export default function LakehouseMetricIntentsPage() {
  const industrial = useStyleMode().mode === 'industrial'
  const t = useTranslations('intent')
  const router = useRouter()
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()

  // ── Mark filter ────────────────────────────────────────────────
  const [markFilter, setMarkFilter] = useState<MarkFilter>('marked')

  // ── Bulk operations state ──────────────────────────────────────
  const [bulkBusy, setBulkBusy] = useState(false)

  const [bulkEditOpen, setBulkEditOpen] = useState(false)
  const [bulkEditTarget, setBulkEditTarget] = useState<{ ids: string[]; clear: () => void } | null>(null)
  const [bulkEditField, setBulkEditField] = useState<BulkEditField>('mark')
  const [bulkEditMark, setBulkEditMark] = useState(true)
  const [bulkEditPriority, setBulkEditPriority] = useState(0)
  const [bulkEditObjectId, setBulkEditObjectId] = useState('')

  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false)
  const [bulkDeleteTarget, setBulkDeleteTarget] = useState<{ ids: string[]; clear: () => void } | null>(null)
  const [bulkDeleteImpact, setBulkDeleteImpact] = useState<BulkImpact | null>(null)
  const [bulkDeleteConfirm, setBulkDeleteConfirm] = useState('')

  // ── CSV bulk-create state ──────────────────────────────────────
  const [csvOpen, setCsvOpen] = useState(false)
  const [csvText, setCsvText] = useState('')
  const [csvParsed, setCsvParsed] = useState<BulkCsvRow[]>([])
  const [csvParseError, setCsvParseError] = useState<string | null>(null)
  const [csvResult, setCsvResult] = useState<BulkCreateResult | null>(null)

  // ── Data ───────────────────────────────────────────────────────
  const { data: intents, refetch, loading } = useFetch<OntMetricIntent>('/ontology/metric-intents')
  const { data: objects } = useFetch<OntObjectType>('/ontology/objects')

  const filteredIntents = useMemo(() => {
    if (!intents) return []
    if (markFilter === 'marked') return intents.filter(i => i.mark)
    if (markFilter === 'unmarked') return intents.filter(i => !i.mark)
    return intents
  }, [intents, markFilter])

  const markedCount = intents?.filter(i => i.mark).length ?? 0
  const totalCount = intents?.length ?? 0
  const triggerCount = useMemo(() => {
    // sum from triggersMap if loaded, else just show 0 placeholder
    return 0
  }, [])

  // ── Single-row handlers — navigate to dedicated form pages ─────
  // Old modal flow (createOpen / editOpen / form state) was extracted into
  // /lakehouse-metric-intents/new and /[id]/edit so users get full-page form
  // real estate, deep-linkable URLs, and consistent SV Minimal chrome.

  const handleEdit = useCallback((r: OntMetricIntent) => {
    router.push(`/ontology/lakehouse-metric-intents/edit?id=${encodeURIComponent(r.id)}`)
  }, [router])

  const handleDelete = useCallback(async (id: string) => {
    try {
      await api(`/ontology/metric-intents/${id}`, { method: 'DELETE' })
      msg.success(t('deleted'))
      refetch()
    } catch { msg.error(t('delete_failed')) }
  }, [msg, refetch])

  // ── Trigger management ─────────────────────────────────────────

  const [triggersMap, setTriggersMap] = useState<Map<string, TriggerRow[]>>(new Map())
  const [triggersExpanded, setTriggersExpanded] = useState<Set<string>>(new Set())
  const [triggerAddOpen, setTriggerAddOpen] = useState<Set<string>>(new Set())
  const [triggerInputs, setTriggerInputs] = useState<Map<string, string>>(new Map())
  const triggerLoadedRef = useRef<Set<string>>(new Set())

  const loadTriggers = useCallback(async (intentId: string) => {
    if (triggerLoadedRef.current.has(intentId)) return
    triggerLoadedRef.current.add(intentId)
    try {
      const res = await api<{ data: TriggerRow[]; total: number }>(`/ontology/metric-intents/${intentId}/triggers`)
      setTriggersMap(m => new Map(m).set(intentId, res.data))
    } catch { /* leave undefined */ }
  }, [])

  const refetchTriggers = useCallback(async (intentId: string) => {
    try {
      const res = await api<{ data: TriggerRow[]; total: number }>(`/ontology/metric-intents/${intentId}/triggers`)
      setTriggersMap(m => new Map(m).set(intentId, res.data))
    } catch { /* ignore */ }
  }, [])

  const deleteTrigger = useCallback(async (intentId: string, kwId: string) => {
    try {
      await api(`/ontology/metric-intents/${intentId}/triggers/${kwId}`, { method: 'DELETE' })
      setTriggersMap(m => {
        const next = new Map(m)
        const list = next.get(intentId) || []
        next.set(intentId, list.filter(t => t.id !== kwId))
        return next
      })
    } catch { msg.error(t('delete_trigger_failed')) }
  }, [msg])

  const addTrigger = useCallback(async (intentId: string) => {
    const token = (triggerInputs.get(intentId) || '').trim()
    if (!token) return
    try {
      await api(`/ontology/keyword-triage/assign?projectId=${currentProject?.id}`, {
        method: 'POST',
        body: { projectId: currentProject?.id, token, bindings: [{ kind: 'intent_trigger', intentId }] },
      })
      setTriggerInputs(m => { const n = new Map(m); n.set(intentId, ''); return n })
      setTriggerAddOpen(s => { const n = new Set(s); n.delete(intentId); return n })
      await refetchTriggers(intentId)
    } catch (e) { msg.error(t('add_trigger_failed', { error: (e as Error).message })) }
  }, [triggerInputs, currentProject, refetchTriggers, msg])

  // ── Bulk handlers ──────────────────────────────────────────────

  const handleBulkMark = async (ids: string[], mark: boolean, clear: () => void) => {
    setBulkBusy(true)
    try {
      const res = await api<{ updated: number }>(
        `/ontology/metric-intents/bulk-update?projectId=${currentProject?.id}`,
        { method: 'POST', body: { ids, mark } },
      )
      msg.success(t('bulk_mark_success', { action: mark ? t('bulk_mark_action_marked') : t('bulk_mark_action_unmarked'), count: res.updated }))
      clear()
      refetch()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_mark_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  const openBulkEdit = (ids: string[], clear: () => void) => {
    setBulkEditTarget({ ids, clear })
    setBulkEditField('mark')
    setBulkEditMark(true)
    setBulkEditPriority(0)
    setBulkEditObjectId('')
    setBulkEditOpen(true)
  }

  const submitBulkEdit = async () => {
    if (!bulkEditTarget) return
    const { ids, clear } = bulkEditTarget
    setBulkBusy(true)
    try {
      let body: Record<string, unknown>
      if (bulkEditField === 'mark') {
        body = { ids, mark: bulkEditMark }
      } else if (bulkEditField === 'priority') {
        body = { ids, priority: bulkEditPriority }
      } else {
        body = { ids, objectId: bulkEditObjectId }
      }
      const res = await api<{ updated: number }>(
        `/ontology/metric-intents/bulk-update?projectId=${currentProject?.id}`,
        { method: 'POST', body },
      )
      msg.success(t('bulk_update_success', { count: res.updated }))
      setBulkEditOpen(false)
      setBulkEditTarget(null)
      clear()
      refetch()
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
        `/ontology/metric-intents/bulk-impact?projectId=${currentProject?.id}`,
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
        `/ontology/metric-intents/bulk-delete?projectId=${currentProject?.id}`,
        { method: 'POST', body: { ids } },
      )
      msg.success(t('bulk_delete_success', { count: res.deleted }))
      setBulkDeleteOpen(false)
      setBulkDeleteTarget(null)
      clear()
      refetch()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_delete_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  // ── CSV / JSON bulk-create ─────────────────────────────────────

  const parseCsvText = (text: string): BulkCsvRow[] => {
    const trimmed = text.trim()
    if (!trimmed) return []
    // JSON path
    if (trimmed.startsWith('[') || trimmed.startsWith('{')) {
      const parsed = JSON.parse(trimmed)
      const rawRows: unknown[] = Array.isArray(parsed)
        ? parsed
        : (parsed as { items?: unknown[] }).items || []
      return rawRows.map((r, i) => {
        const o = r as Record<string, unknown>
        const name = String(o.name || '').trim()
        if (!name) throw new Error(t('csv_row_name_empty', { row: i + 1 }))
        const objectId = String(o.objectId || o.objectName || '').trim()
        if (!objectId) throw new Error(t('csv_row_objectid_empty', { row: i + 1 }))
        const canonicalMetric = String(o.canonicalMetric || '').trim()
        if (!canonicalMetric) throw new Error(t('csv_row_metric_empty', { row: i + 1 }))
        // resolve objectName -> objectId
        const resolvedId = resolveObjectId(objectId)
        return {
          name,
          objectId: resolvedId,
          canonicalMetric,
          displayName: o.displayName ? String(o.displayName) : undefined,
          priority: o.priority !== undefined ? Number(o.priority) : undefined,
          description: o.description ? String(o.description) : undefined,
          mark: o.mark !== undefined ? Boolean(o.mark) : undefined,
        }
      })
    }
    // CSV path
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
    const idxObject = headers.indexOf('objectname') >= 0 ? headers.indexOf('objectname') : headers.indexOf('objectid')
    const idxMetric = headers.indexOf('canonicalmetric')
    if (idxName < 0) throw new Error(t('csv_missing_name_col'))
    if (idxObject < 0) throw new Error(t('csv_missing_object_col'))
    if (idxMetric < 0) throw new Error(t('csv_missing_metric_col'))
    const idxDisplay = headers.indexOf('displayname')
    const idxPriority = headers.indexOf('priority')
    const idxDesc = headers.indexOf('description')
    return lines.slice(1).map((line, i) => {
      const cols = split(line)
      const name = (cols[idxName] || '').trim()
      if (!name) throw new Error(t('csv_row_name_empty', { row: i + 2 }))
      const objectRaw = (cols[idxObject] || '').trim()
      if (!objectRaw) throw new Error(t('csv_row_objectid_empty', { row: i + 2 }))
      const canonicalMetric = (cols[idxMetric] || '').trim()
      if (!canonicalMetric) throw new Error(t('csv_row_metric_empty', { row: i + 2 }))
      const resolvedId = resolveObjectId(objectRaw)
      return {
        name,
        objectId: resolvedId,
        canonicalMetric,
        displayName: idxDisplay >= 0 ? cols[idxDisplay] || undefined : undefined,
        priority: idxPriority >= 0 && cols[idxPriority] ? Number(cols[idxPriority]) : undefined,
        description: idxDesc >= 0 ? cols[idxDesc] || undefined : undefined,
      }
    })
  }

  // Resolve an objectName or uuid to an object id
  const resolveObjectId = useCallback((nameOrId: string): string => {
    if (!objects) return nameOrId
    // exact id match
    const byId = objects.find(o => o.id === nameOrId)
    if (byId) return byId.id
    // name match (case-insensitive)
    const byName = objects.find(o => o.name.toLowerCase() === nameOrId.toLowerCase())
    if (byName) return byName.id
    return nameOrId // pass through; backend will validate
  }, [objects])

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
        `/ontology/metric-intents/bulk-create?projectId=${currentProject.id}`,
        { method: 'POST', body: { items: csvParsed } },
      )
      setCsvResult(res)
      if (res.errors.length === 0) {
        msg.success(t('bulk_create_success', { count: res.created }))
      } else {
        msg.warning(t('bulk_create_partial', { created: res.created, failed: res.errors.length }))
      }
      refetch()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_create_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  // ── Columns ────────────────────────────────────────────────────

  const columns: Column<OntMetricIntent>[] = useMemo(() => [
    {
      key: 'name', title: 'Name', sortable: true,
      render: (_, r) => (
        <div>
          <span className="font-sans text-sm font-semibold text-ink">{r.name}</span>
          {r.displayName && r.displayName !== r.name && (
            <span className="ml-2 text-xs text-ink-ghost">{r.displayName}</span>
          )}
        </div>
      ),
    },
    {
      key: 'objectName', title: 'Od', width: '120px',
      render: (_, r) => (
        <span className="inline-flex items-center border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] text-ink-muted font-mono">
          {r.objectName}
        </span>
      ),
    },
    {
      key: 'canonicalMetric', title: 'Metric',
      render: (_, r) => <span className="font-mono text-xs font-semibold text-ink">{r.canonicalMetric}</span>,
    },
    {
      key: 'autoGroupBy', title: 'Auto GroupBy',
      render: (_, r) => (
        <div className="flex flex-wrap gap-1">
          {r.autoGroupBy.length === 0 ? (
            <span className="text-xs text-ink-ghost">—</span>
          ) : (
            r.autoGroupBy.map((g, i) => (
              <span key={i} className="border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] text-ink-muted font-mono">{g}</span>
            ))
          )}
        </div>
      ),
    },
    {
      key: 'pivotOn', title: 'Pivot',
      render: (_, r) => r.pivotOn ? (
        <div className="flex flex-wrap items-center gap-1">
          <span className="text-[11px] text-ink-ghost">on</span>
          <span className="border border-ink bg-white px-1.5 py-0.5 text-[11px] text-ink font-mono font-semibold">{r.pivotOn}</span>
          {r.pivotValues.map((v, i) => (
            <span key={i} className="border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] text-ink-muted font-mono">{v}</span>
          ))}
        </div>
      ) : <span className="text-xs text-ink-ghost">—</span>,
    },
    {
      key: 'triggers' as keyof OntMetricIntent, title: 'Triggers',
      render: (_, r) => {
        const loaded = triggersMap.has(r.id)
        const triggers = triggersMap.get(r.id) || []
        const expanded = triggersExpanded.has(r.id)
        const addOpen = triggerAddOpen.has(r.id)
        const addInput = triggerInputs.get(r.id) || ''
        const SHOW = 5
        const visible = expanded ? triggers : triggers.slice(0, SHOW)
        const hiddenCount = triggers.length - SHOW

        if (!loaded) {
          return (
            <motion.button
              whileHover={reduce ? {} : { x: 1 }}
              whileTap={reduce ? {} : { scale: 0.97 }}
              transition={{ duration: 0.12 }}
              className="text-xs text-ink-muted underline cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink"
              onClick={() => loadTriggers(r.id)}
            >
              Load
            </motion.button>
          )
        }

        return (
          <div className="flex flex-wrap gap-1 items-center min-w-0">
            <AnimatePresence initial={false}>
              {triggers.length === 0 && !addOpen && <span className="text-xs text-ink-ghost">—</span>}
              {visible.map(trig => {
                const used = trig.usageCount ?? 0
                return (
                  <motion.span
                    key={trig.id}
                    layout
                    initial={reduce ? {} : { opacity: 0, scale: 0.9 }}
                    animate={reduce ? {} : { opacity: 1, scale: 1 }}
                    exit={reduce ? {} : { opacity: 0, scale: 0.9 }}
                    transition={{ duration: 0.12 }}
                    className={`group inline-flex items-center gap-0.5 border px-1.5 py-0.5 leading-tight ${
                      industrial ? 'font-mono text-[10px] tracking-[0.04em]' : 'rounded-md text-[11px]'
                    } ${
                      used > 0 ? 'border-ink text-ink bg-white' : 'border-border text-ink-ghost bg-canvas-alt'
                    }`}
                    title={t('trigger_usage_title', { keyword: trig.keyword, count: used })}
                  >
                    {trig.keyword}
                    <span className="ml-0.5 text-ink-ghost">×{used}</span>
                    <motion.button
                      onClick={() => deleteTrigger(r.id, trig.id)}
                      whileHover={reduce ? {} : { scale: 1.2 }}
                      whileTap={reduce ? {} : { scale: 0.9 }}
                      transition={{ duration: 0.12 }}
                      aria-label={t('trigger_delete_aria')}
                      className="opacity-0 group-hover:opacity-100 hover:text-danger ml-0.5 flex-shrink-0 cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink focus-visible:opacity-100"
                    >
                      <X className="h-2.5 w-2.5" />
                    </motion.button>
                  </motion.span>
                )
              })}
            </AnimatePresence>
            {!expanded && hiddenCount > 0 && (
              <motion.button
                onClick={() => setTriggersExpanded(s => { const n = new Set(s); n.add(r.id); return n })}
                whileHover={reduce ? {} : { x: 1 }}
                whileTap={reduce ? {} : { scale: 0.97 }}
                transition={{ duration: 0.12 }}
                className={`border border-border px-1.5 py-0.5 text-ink-muted cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink ${industrial ? 'font-mono text-[10px] tracking-[0.04em] uppercase' : 'rounded-md text-[11px]'}`}
              >
                {t('trigger_more', { count: hiddenCount })}
              </motion.button>
            )}
            {addOpen ? (
              <span className="inline-flex items-center gap-0.5">
                <input
                  autoFocus
                  value={addInput}
                  onChange={e => setTriggerInputs(m => { const n = new Map(m); n.set(r.id, e.target.value); return n })}
                  onKeyDown={e => {
                    if (e.key === 'Enter') { e.preventDefault(); addTrigger(r.id) }
                    if (e.key === 'Escape') setTriggerAddOpen(s => { const n = new Set(s); n.delete(r.id); return n })
                  }}
                  className="border border-border px-1.5 py-0.5 text-[11px] w-24 outline-none focus:border-ink"
                  placeholder={t('trigger_placeholder')}
                />
                <motion.button
                  onClick={() => addTrigger(r.id)}
                  disabled={!addInput.trim()}
                  whileHover={reduce ? {} : { scale: 1.03 }}
                  whileTap={reduce ? {} : { scale: 0.95 }}
                  transition={{ duration: 0.12 }}
                  className={`bg-ink text-white px-2 py-0.5 disabled:opacity-40 cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink ${industrial ? 'font-mono text-[10px] uppercase tracking-[0.12em]' : 'rounded-md text-[11px]'}`}
                >
                  {t('trigger_add_btn')}
                </motion.button>
                <motion.button
                  onClick={() => setTriggerAddOpen(s => { const n = new Set(s); n.delete(r.id); return n })}
                  whileHover={reduce ? {} : { scale: 1.15 }}
                  whileTap={reduce ? {} : { scale: 0.9 }}
                  transition={{ duration: 0.12 }}
                  aria-label={t('trigger_cancel_aria')}
                  className="border border-border px-1.5 py-0.5 text-[11px] text-ink-ghost cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink"
                >
                  ✕
                </motion.button>
              </span>
            ) : (
              <motion.button
                onClick={() => setTriggerAddOpen(s => new Set(s).add(r.id))}
                whileHover={reduce ? {} : { x: 1 }}
                whileTap={reduce ? {} : { scale: 0.97 }}
                transition={{ duration: 0.12 }}
                className="inline-flex items-center gap-0.5 border border-dashed border-border px-1.5 py-0.5 text-[11px] text-ink-ghost hover:border-ink hover:text-ink-muted cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink"
              >
                <Plus className="h-2 w-2" /> {t('trigger_add_icon')}
              </motion.button>
            )}
          </div>
        )
      },
    },
    {
      key: 'canonicalFilters', title: 'Filters',
      render: (_, r) => (
        <div className="flex flex-wrap gap-1">
          {r.canonicalFilters.length === 0 ? (
            <span className="text-xs text-ink-ghost">—</span>
          ) : (
            r.canonicalFilters.map((f, i) => (
              <span key={i} className="border border-ink-muted bg-white px-1.5 py-0.5 text-[11px] text-ink-muted font-mono">
                {f.prop} {f.op} {f.value}
              </span>
            ))
          )}
        </div>
      ),
    },
    {
      key: 'priority', title: 'P', width: '50px',
      render: (_, r) => <span className="text-xs text-ink-ghost tabular-nums">{r.priority}</span>,
    },
    {
      key: 'actions', title: '', width: '80px',
      render: (_, r) => (
        <div className="flex gap-1">
          <motion.button
            onClick={() => handleEdit(r)}
            whileHover={reduce ? {} : { scale: 1.15 }}
            whileTap={reduce ? {} : { scale: 0.9 }}
            transition={{ duration: 0.12 }}
            aria-label={t('edit_aria')}
            className="p-1 text-ink-ghost hover:text-ink cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink"
          >
            <Pencil className="h-3 w-3" />
          </motion.button>
          <motion.button
            onClick={() => handleDelete(r.id)}
            whileHover={reduce ? {} : { scale: 1.15 }}
            whileTap={reduce ? {} : { scale: 0.9 }}
            transition={{ duration: 0.12 }}
            aria-label={t('delete_aria')}
            className="p-1 text-ink-ghost hover:text-danger cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink"
          >
            <Trash2 className="h-3 w-3" />
          </motion.button>
        </div>
      ),
    },
  ], [triggersMap, triggersExpanded, triggerAddOpen, triggerInputs, loadTriggers, deleteTrigger, addTrigger, handleEdit, handleDelete, reduce])


  // ── Render ─────────────────────────────────────────────────────

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header — h-14 to align with Sidebar; industrial uses 2px ink rule */}
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
              <Sparkles size={18} className="text-ink flex-shrink-0" aria-hidden="true" />
              <h1 className="text-base font-semibold tracking-tight text-ink whitespace-nowrap">
                {t('page_title')}
              </h1>
            </>
          )}
          <span className={industrial ? 'font-mono text-[10px] tracking-[0.14em] text-ink-muted truncate tabular-nums' : 'text-xs text-ink-ghost truncate'}>
            {industrial
              ? `${totalCount} TOTAL · ${markedCount} MARKED`
              : t('page_subtitle', { total: totalCount, marked: markedCount })}
          </span>
        </div>

        <div className="flex flex-shrink-0 flex-wrap items-center gap-2">
          <SegmentedFilter
            value={markFilter}
            onChange={setMarkFilter}
            options={[
              ['all', t('filter_all', { count: totalCount })],
              ['marked', t('filter_marked', { count: markedCount })],
              ['unmarked', t('filter_unmarked', { count: totalCount - markedCount })],
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
            onClick={() => router.push('/ontology/lakehouse-metric-intents/new')}
            aria-label={t('new_aria')}
          >
            <Plus size={12} aria-hidden="true" />
            {t('new_btn')}
          </AnimatedButton>
        </div>
      </motion.header>

      {/* Content */}
      <div className="flex flex-1 min-h-0 flex-col">
        {loading && !intents ? (
          <div className="flex h-full items-center justify-center">
            <InlineLoader text={t('loading')} />
          </div>
        ) : (
          <div className="flex-1 min-h-0 overflow-y-auto">
            <DataTable
              columns={columns}
              data={filteredIntents}
              rowKey="id"
              searchable
              searchPlaceholder={t('search_placeholder')}
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

      {/* Single-row Create / Edit moved to /new and /[id]/edit dedicated routes
          so the form gets full-page real estate, deep-link URLs, and matches
          the SV Minimal layout convention used by sql-passthrough / lakehouse-sql. */}

      {/* ── Bulk Edit Modal ──────────────────────────────────────── */}
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
                ['mark', t('bulk_edit_field_mark')],
                ['priority', t('bulk_edit_field_priority')],
                ['objectId', t('bulk_edit_field_objectid')],
              ]}
            />
          </div>
          {bulkEditField === 'mark' && (
            <div>
              <label className="mb-1.5 block text-sm font-medium text-ink">{t('bulk_edit_mark_label')}</label>
              <SegmentedFilter<string>
                value={bulkEditMark ? 'true' : 'false'}
                onChange={v => setBulkEditMark(v === 'true')}
                options={[
                  ['true', t('bulk_edit_mark_true')],
                  ['false', t('bulk_edit_mark_false')],
                ]}
              />
              <p className="mt-1.5 text-xs text-ink-ghost">
                {t('bulk_edit_mark_hint', { value: bulkEditMark ? 'true' : 'false' })}
              </p>
            </div>
          )}
          {bulkEditField === 'priority' && (
            <div>
              <label htmlFor="bulk-priority" className="mb-1.5 block text-sm font-medium text-ink">{t('bulk_edit_priority_label')}</label>
              <input
                id="bulk-priority"
                type="number"
                min={0}
                max={100}
                value={bulkEditPriority}
                onChange={e => setBulkEditPriority(parseInt(e.target.value) || 0)}
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink focus:ring-1 focus:ring-ink/10"
              />
              <p className="mt-1.5 text-xs text-ink-ghost">{t('bulk_edit_priority_hint', { value: bulkEditPriority })}</p>
            </div>
          )}
          {bulkEditField === 'objectId' && (
            <div>
              <label htmlFor="bulk-object" className="mb-1.5 block text-sm font-medium text-ink">{t('bulk_edit_object_label')}</label>
              <select
                id="bulk-object"
                value={bulkEditObjectId}
                onChange={e => setBulkEditObjectId(e.target.value)}
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink focus:ring-1 focus:ring-ink/10"
              >
                <option value="">{t('bulk_edit_object_placeholder')}</option>
                {objects.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}
              </select>
              <p className="mt-1.5 text-xs text-ink-ghost">{t('bulk_edit_object_hint')}</p>
            </div>
          )}
          <div className="flex justify-end gap-2 border-t border-border-light pt-3">
            <AnimatedButton variant="ghost" size="md" onClick={() => setBulkEditOpen(false)} disabled={bulkBusy}>{t('bulk_edit_cancel')}</AnimatedButton>
            <AnimatedButton
              variant="primary"
              size="md"
              onClick={submitBulkEdit}
              disabled={bulkBusy || (bulkEditField === 'objectId' && !bulkEditObjectId)}
            >
              {bulkBusy ? t('bulk_edit_submitting') : t('bulk_edit_submit')}
            </AnimatedButton>
          </div>
        </div>
      </Modal>

      {/* ── Bulk Delete Modal ────────────────────────────────────── */}
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
                <ImpactRow label="Metric (lakehouse_metric_intent)" value={bulkDeleteImpact.intents} primary />
                <ImpactRow label="Keyword (lakehouse_keyword)" value={bulkDeleteImpact.keywords} />
              </div>
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

      {/* ── CSV/JSON Bulk Create Modal ───────────────────────────── */}
      <Modal
        open={csvOpen}
        onClose={() => !bulkBusy && setCsvOpen(false)}
        title={t('csv_modal_title')}
        width="720px"
      >
        <div className="space-y-4">
          <div className="rounded-md border border-border-light bg-canvas-alt p-3 text-xs leading-relaxed text-ink-muted">
            <div className="mb-1 font-medium text-ink">{t('csv_format_header')}</div>
            <code className="block font-mono text-[11px] text-ink">name,objectName,canonicalMetric,displayName,priority,description</code>
            <div className="mt-1.5">{t('csv_or_json')}<code className="font-mono text-[11px] text-ink">[{`{"name":"Order.Total","objectId":"...","canonicalMetric":"sum(qty)"}`}]</code></div>
            <div className="mt-1.5 space-y-0.5">
              <div>{t('csv_required_note')}<code className="font-mono text-ink">name</code>、<code className="font-mono text-ink">objectName</code>、<code className="font-mono text-ink">canonicalMetric</code></div>
              <div className="text-ink-ghost">{t('csv_object_note')}</div>
            </div>
          </div>
          <div>
            <label htmlFor="csv-text" className="mb-1.5 block text-sm font-medium text-ink">{t('csv_data_label')}</label>
            <textarea
              id="csv-text"
              value={csvText}
              onChange={e => tryParseCsv(e.target.value)}
              rows={8}
              spellCheck={false}
              placeholder={`name,objectName,canonicalMetric,priority\nOrder.Total,Order,sum(Order_Quantity),10\nOrder.Count,Order,count(*),5`}
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
                      <th className="px-3 py-1.5 font-medium">objectId</th>
                      <th className="px-3 py-1.5 font-medium">canonicalMetric</th>
                    </tr>
                  </thead>
                  <tbody>
                    {csvParsed.slice(0, 50).map((r, i) => (
                      <tr key={i} className="border-t border-border-light">
                        <td className="px-3 py-1 font-mono text-ink">{r.name}</td>
                        <td className="px-3 py-1 text-ink-muted font-mono truncate max-w-[160px]">{r.objectId}</td>
                        <td className="px-3 py-1 text-ink-muted font-mono truncate max-w-[200px]">{r.canonicalMetric}</td>
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

// ─── Sub-components ──────────────────────────────────────────────────────────

function SegmentedFilter<T extends string>({
  value, onChange, options,
}: {
  value: T
  onChange: (v: T) => void
  options: [T, string][]
}) {
  const reduce = useReducedMotion()
  const industrial = useStyleMode().mode === 'industrial'
  const [layoutId] = useState(() => `seg-${Math.random().toString(36).slice(2, 9)}`)
  return (
    <div
      role="radiogroup"
      className={`relative flex h-7 items-center p-0.5 ${industrial ? 'border border-ink bg-white' : 'rounded-md border border-border bg-canvas-alt'}`}
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
              industrial ? 'font-mono text-[10px] uppercase tracking-[0.14em]' : 'rounded-[5px] text-[11px] font-medium'
            }`}
          >
            {selected && (
              <motion.span
                layoutId={layoutId}
                className={`absolute inset-0 ${industrial ? 'bg-ink' : 'rounded-[5px] bg-white shadow-sm'}`}
                transition={reduce ? { duration: 0 } : { type: 'spring', stiffness: 500, damping: 35 }}
              />
            )}
            <span className={`relative ${selected ? (industrial ? 'text-white' : 'text-ink') : 'text-ink-muted hover:text-ink'}`}>
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
