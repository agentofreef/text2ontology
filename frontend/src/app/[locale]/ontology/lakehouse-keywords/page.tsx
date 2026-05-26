'use client'

import { useTranslations } from 'next-intl'
import { useState, useMemo, useCallback, useEffect, useRef } from 'react'
import { motion, AnimatePresence, useReducedMotion, type HTMLMotionProps } from 'motion/react'
import { CyberLoader } from '@/components/ui/CyberLoader'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { DataTable, Column } from '@/components/ui/DataTable'
import { Modal } from '@/components/ui/Modal'
import { BulkActionButton, ImpactRow } from '@/components/ui/BulkActionButton'
import { useProject } from '@/lib/project'
import { useMessage } from '@/lib/message'
import { api, getApiBase } from '@/lib/api'
import { useStyleMode } from '@/lib/style-mode'
import {
  Tags, Search, ChevronDown, ChevronRight, Trash2, ArrowUpDown,
  ChevronLeft, X, Plus, Sparkles, RefreshCw, FilePlus2, AlertTriangle,
} from 'lucide-react'

interface VectorStatus {
  keywords: { total: number; withVector: number; missing: number }
  aliases: { total: number; withVector: number; missing: number }
  needsCompute: number
}

interface LakehouseKeyword {
  id: string
  keyword: string
  isMachineCode: boolean
  isColumnName: boolean
  isStopword?: boolean
  syncedAt: string
  odName: string
  propName: string
  sourceColumn: string
  dataType: string
  aliases: string[]
  isOrphaned: boolean
  // Unified-metric anchor (lakehouse_metric → lakehouse_keyword.metric_id).
  // Surfaced so triggers authored in the metric editor are visible here AND
  // hittable by recall.
  metricId?: string
  metricName?: string
  metricDisplayName?: string
}

interface Summary {
  total: number
  columnCount: number
  valueCount: number
  mcCount: number
  odNames: string[]
}

interface TreeProp {
  id: string
  name: string
  dataType: string
  isMachineCode: boolean
  colCount: number
  valCount: number
}

interface TreeOd {
  name: string
  propCount: number
  colCount: number
  valCount: number
  props: TreeProp[]
}

type TypeFilter = 'all' | 'column' | 'value'
type McFilter = 'all' | 'mc' | 'semantic'
type OntologyFilter = 'all' | 'yes' | 'no'
type MatchMode = 'fuzzy' | 'exact'
type TabType = 'grouped' | 'keyword'

// ── Bulk CRUD types ──────────────────────────────────────────────
type BulkUpdateField = 'isColumnName' | 'isStopword'
type BulkReanchorTarget = 'property' | 'intent' | 'clear'

interface BulkImpact { keywords: number; aliasVectors: number }
interface BulkCsvRow {
  keyword: string
  propertyId?: string
  metricIntentId?: string
  objectId?: string
  isColumnName?: boolean
  aliases?: string[]
}
interface BulkCreateResult {
  created: number
  total: number
  ids: string[]
  errors: { index: number; keyword?: string; error: string }[]
}
interface OdWithProps { id: string; name: string; properties: { id: string; name: string }[] }
interface MetricIntentOption { id: string; name: string }

const KW_PAGE_SIZE = 50
const TAB_UNDERLINE_ID = 'lh-keywords-tab-underline'

export default function LakehouseKeywordsPage() {
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()
  const t = useTranslations('keyword')
  const industrial = useStyleMode().mode === 'industrial'

  // ── Tab ──
  const [activeTab, setActiveTab] = useState<TabType>('grouped')

  // ── Grouped view state ──
  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const [typeFilter, setTypeFilter] = useState<TypeFilter>('all')
  const [mcFilter, setMcFilter] = useState<McFilter>('all')
  const [odFilter, setOdFilter] = useState('')
  const [summary, setSummary] = useState<Summary | null>(null)
  const [expandedOds, setExpandedOds] = useState<Set<string>>(new Set())
  const [expandedProps, setExpandedProps] = useState<Set<string>>(new Set())
  const [expandedValues, setExpandedValues] = useState<Set<string>>(new Set())

  // ── Keyword mode state ──
  const [kwSearch, setKwSearch] = useState('')
  const [kwDebouncedSearch, setKwDebouncedSearch] = useState('')
  const [kwMatchMode, setKwMatchMode] = useState<MatchMode>('fuzzy')
  const [kwHasOntology, setKwHasOntology] = useState<OntologyFilter>('all')
  const [kwOd, setKwOd] = useState('')
  const [kwProp, setKwProp] = useState('')
  const [kwPage, setKwPage] = useState(1)
  const [kwKeywords, setKwKeywords] = useState<LakehouseKeyword[]>([])
  const [kwTotal, setKwTotal] = useState(0)
  const [kwLoading, setKwLoading] = useState(false)
  const [kwPropNames, setKwPropNames] = useState<string[]>([])
  const [kwAddOpen, setKwAddOpen] = useState<Set<string>>(new Set())
  const [kwAddInput, setKwAddInput] = useState<Record<string, string>>({})
  const [kwSaving, setKwSaving] = useState<Set<string>>(new Set())

  // ── Bulk operations state ───────────────────────────────────────
  const [bulkBusy, setBulkBusy] = useState(false)

  const [bulkEditOpen, setBulkEditOpen] = useState(false)
  const [bulkEditTarget, setBulkEditTarget] = useState<{ ids: string[]; clear: () => void } | null>(null)
  const [bulkEditField, setBulkEditField] = useState<BulkUpdateField>('isColumnName')
  const [bulkEditValue, setBulkEditValue] = useState(true)

  const [bulkReanchorOpen, setBulkReanchorOpen] = useState(false)
  const [bulkReanchorTarget, setBulkReanchorTarget] = useState<{ ids: string[]; clear: () => void } | null>(null)
  const [bulkReanchorMode, setBulkReanchorMode] = useState<BulkReanchorTarget>('property')
  const [bulkReanchorOd, setBulkReanchorOd] = useState('')
  const [bulkReanchorProperty, setBulkReanchorProperty] = useState('')
  const [bulkReanchorIntent, setBulkReanchorIntent] = useState('')

  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false)
  const [bulkDeleteTarget, setBulkDeleteTarget] = useState<{ ids: string[]; clear: () => void } | null>(null)
  const [bulkDeleteImpact, setBulkDeleteImpact] = useState<BulkImpact | null>(null)
  const [bulkDeleteConfirm, setBulkDeleteConfirm] = useState('')

  const [csvOpen, setCsvOpen] = useState(false)
  const [csvText, setCsvText] = useState('')
  const [csvParsed, setCsvParsed] = useState<BulkCsvRow[]>([])
  const [csvParseError, setCsvParseError] = useState<string | null>(null)
  const [csvResult, setCsvResult] = useState<BulkCreateResult | null>(null)

  // For Re-anchor modal
  const [odList, setOdList] = useState<OdWithProps[]>([])
  const [intentList, setIntentList] = useState<MetricIntentOption[]>([])

  useEffect(() => {
    if (!bulkReanchorOpen || !currentProject) return
    api<{ data: OdWithProps[] }>(`/ontology/objects?projectId=${currentProject.id}`)
      .then(r => setOdList(r.data || []))
      .catch(() => setOdList([]))
    api<{ data: MetricIntentOption[] }>(`/ontology/metric-intents?projectId=${currentProject.id}`)
      .then(r => setIntentList(r.data || []))
      .catch(() => setIntentList([]))
  }, [bulkReanchorOpen, currentProject])

  // ── Debounce ──
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => {
    debounceRef.current = setTimeout(() => setDebouncedSearch(search), 300)
    return () => clearTimeout(debounceRef.current)
  }, [search])

  const kwDebounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => {
    kwDebounceRef.current = setTimeout(() => setKwDebouncedSearch(kwSearch), 300)
    return () => clearTimeout(kwDebounceRef.current)
  }, [kwSearch])

  useEffect(() => { setKwPage(1) }, [kwDebouncedSearch, kwMatchMode, kwHasOntology, kwOd, kwProp])

  // ── Summary + tree ──
  const [tree, setTree] = useState<TreeOd[]>([])
  const refreshSummaryTree = useCallback(() => {
    if (!currentProject) return
    api<Summary>(`/connector/pbit/lakehouse-keywords/summary?projectId=${currentProject.id}`)
      .then(setSummary).catch(() => {})
    api<{ ods: TreeOd[] }>(`/connector/pbit/lakehouse-keywords/tree?projectId=${currentProject.id}`)
      .then(res => setTree(res.ods || [])).catch(() => setTree([]))
  }, [currentProject])
  useEffect(() => { refreshSummaryTree() }, [refreshSummaryTree])

  // ── Vector status + compute ──
  const [vecStatus, setVecStatus] = useState<VectorStatus | null>(null)
  const [computing, setComputing] = useState(false)
  const [vecProgress, setVecProgress] = useState<{ done: number; total: number } | null>(null)

  const loadVecStatus = useCallback(async () => {
    if (!currentProject) return
    try {
      const res = await api<VectorStatus>(
        `/connector/pbit/lakehouse-keywords/vector-status?projectId=${currentProject.id}`,
      )
      setVecStatus(res)
    } catch {
      setVecStatus(null)
    }
  }, [currentProject])
  useEffect(() => { loadVecStatus() }, [loadVecStatus])

  // SSE stream reader.
  const startCompute = useCallback(async () => {
    if (!currentProject || computing) return
    setComputing(true)
    setVecProgress({ done: 0, total: 0 })
    try {
      const token = localStorage.getItem('lakehouse2ontology_token')
      const res = await fetch(
        `${getApiBase()}/connector/pbit/lakehouse-keywords/compute-vectors?projectId=${currentProject.id}`,
        { method: 'POST', headers: token ? { Authorization: `Bearer ${token}` } : {} },
      )
      if (!res.ok || !res.body) { msg.error(t('compute_failed_status', { status: res.status })); return }
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buf = ''
      let embedded = 0
      let failed = 0
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        const lines = buf.split('\n')
        buf = lines.pop() || ''
        for (const ln of lines) {
          if (!ln.startsWith('data: ')) continue
          try {
            const evt = JSON.parse(ln.slice(6))
            if (evt.type === 'start') setVecProgress({ done: 0, total: evt.total || 0 })
            else if (evt.type === 'progress') setVecProgress({ done: evt.done || 0, total: evt.total || 0 })
            else if (evt.type === 'error') msg.error(typeof evt.msg === 'string' ? evt.msg : t('compute_error'))
            else if (evt.type === 'done') { embedded = evt.embedded || 0; failed = evt.failed || 0 }
          } catch { /* skip */ }
        }
      }
      if (failed > 0) msg.error(t('compute_done_partial', { embedded, failed }))
      else if (embedded > 0) msg.success(t('compute_done_ok', { embedded }))
      else msg.success(t('compute_no_op'))
    } catch (e) {
      msg.error(t('compute_failed_err', { message: (e as Error).message }))
    } finally {
      setComputing(false)
      setVecProgress(null)
      loadVecStatus()
    }
  }, [currentProject, computing, loadVecStatus, msg])

  // ── Grouped view: per-property keyword cache (lazy-load) ──
  const propKwCacheRef = useRef<Record<string, LakehouseKeyword[]>>({})
  const [propKwCache, setPropKwCache] = useState<Record<string, LakehouseKeyword[]>>({})
  const [loadingProps, setLoadingProps] = useState<Set<string>>(new Set())

  useEffect(() => {
    propKwCacheRef.current = {}
    setPropKwCache({})
  }, [debouncedSearch, typeFilter, mcFilter])

  const loadPropKeywords = useCallback(async (propId: string) => {
    if (!currentProject || !propId) return
    if (propKwCacheRef.current[propId] !== undefined) return
    setLoadingProps(prev => {
      if (prev.has(propId)) return prev
      const n = new Set(prev); n.add(propId); return n
    })
    try {
      const params = new URLSearchParams({
        projectId: currentProject.id,
        propertyId: propId,
        page: '1',
        pageSize: '5000',
      })
      if (debouncedSearch) params.set('search', debouncedSearch)
      if (typeFilter !== 'all') params.set('type', typeFilter)
      if (mcFilter !== 'all') params.set('mc', mcFilter)
      const res = await api<{ data: LakehouseKeyword[] }>(
        `/connector/pbit/lakehouse-keywords?${params.toString()}`
      )
      propKwCacheRef.current = { ...propKwCacheRef.current, [propId]: res.data || [] }
      setPropKwCache(propKwCacheRef.current)
    } catch { msg.error(t('load_keywords_failed')) }
    finally {
      setLoadingProps(prev => { const n = new Set(prev); n.delete(propId); return n })
    }
  }, [currentProject, debouncedSearch, typeFilter, mcFilter, msg])

  const invalidatePropCache = useCallback((propId: string) => {
    delete propKwCacheRef.current[propId]
    setPropKwCache({ ...propKwCacheRef.current })
  }, [])

  // ── Keyword mode fetch ──
  const fetchKwData = useCallback(async () => {
    if (!currentProject || activeTab !== 'keyword') return
    setKwLoading(true)
    try {
      const params = new URLSearchParams({
        projectId: currentProject.id,
        page: String(kwPage),
        pageSize: String(KW_PAGE_SIZE),
        matchMode: kwMatchMode,
      })
      if (kwDebouncedSearch) params.set('search', kwDebouncedSearch)
      if (kwHasOntology !== 'all') params.set('hasOntology', kwHasOntology)
      if (kwOd) params.set('od', kwOd)
      if (kwProp) params.set('prop', kwProp)
      const res = await api<{ data: LakehouseKeyword[]; total: number }>(
        `/connector/pbit/lakehouse-keywords?${params.toString()}`
      )
      setKwKeywords(res.data || [])
      setKwTotal(res.total || 0)
    } catch { msg.error(t('load_failed')) }
    finally { setKwLoading(false) }
  }, [currentProject, activeTab, kwPage, kwDebouncedSearch, kwMatchMode, kwHasOntology, kwOd, kwProp, msg])

  useEffect(() => { fetchKwData() }, [fetchKwData])

  useEffect(() => {
    if (!kwOd) { setKwPropNames([]); setKwProp(''); return }
    const od = tree.find(o => o.name === kwOd)
    setKwPropNames((od?.props || []).map(p => p.name))
    setKwProp('')
  }, [tree, kwOd])

  // ── Alias operations ──
  const saveAliases = useCallback(async (id: string, aliases: string[]) => {
    setKwSaving(prev => new Set(prev).add(id))
    try {
      await api('/connector/pbit/lakehouse-keywords/aliases', {
        method: 'PUT',
        body: { id, aliases },
      })
      setKwKeywords(prev => prev.map(kw => kw.id === id ? { ...kw, aliases } : kw))
      loadVecStatus()
    } catch { msg.error(t('save_failed')) }
    finally { setKwSaving(prev => { const n = new Set(prev); n.delete(id); return n }) }
  }, [msg, loadVecStatus])

  const removeAlias = useCallback((id: string, current: string[], alias: string) => {
    saveAliases(id, current.filter(a => a !== alias))
  }, [saveAliases])

  const addAlias = useCallback((id: string, current: string[]) => {
    const val = (kwAddInput[id] || '').trim()
    if (!val || current.includes(val)) return
    saveAliases(id, [...current, val])
    setKwAddInput(prev => ({ ...prev, [id]: '' }))
    setKwAddOpen(prev => { const n = new Set(prev); n.delete(id); return n })
  }, [kwAddInput, saveAliases])

  // ── Grouped view helpers ──
  const filteredTree = useMemo(() => {
    let ods = tree
    if (odFilter) ods = ods.filter(o => o.name === odFilter)
    if (debouncedSearch) {
      const q = debouncedSearch.toLowerCase()
      ods = ods
        .map(o => {
          if (o.name.toLowerCase().includes(q)) return o
          const props = o.props.filter(p => p.name.toLowerCase().includes(q))
          return { ...o, props }
        })
        .filter(o => o.name.toLowerCase().includes(q) || o.props.length > 0)
    }
    return ods
  }, [tree, odFilter, debouncedSearch])

  const kwTotalPages = Math.max(1, Math.ceil(kwTotal / KW_PAGE_SIZE))

  const toggleOd = (name: string) =>
    setExpandedOds(prev => { const n = new Set(prev); n.has(name) ? n.delete(name) : n.add(name); return n })

  const toggleProp = (key: string, propId: string) => {
    setExpandedProps(prev => {
      const n = new Set(prev)
      if (n.has(key)) { n.delete(key); return n }
      n.add(key)
      loadPropKeywords(propId)
      return n
    })
  }

  const toggleValueExpand = (key: string) =>
    setExpandedValues(prev => { const n = new Set(prev); n.has(key) ? n.delete(key) : n.add(key); return n })

  const expandAll = () => {
    setExpandedOds(new Set(filteredTree.map(o => o.name)))
    const keys = filteredTree.flatMap(o => o.props.map(p => o.name + '::' + p.name))
    setExpandedProps(new Set(keys))
    filteredTree.forEach(o => o.props.forEach(p => loadPropKeywords(p.id)))
  }
  const collapseAll = () => { setExpandedOds(new Set()); setExpandedProps(new Set()) }

  const deleteKeyword = useCallback(async (id: string, propId: string) => {
    await api(`/connector/pbit/lakehouse-keywords?id=${id}&projectId=${currentProject?.id}`, { method: 'DELETE' })
    invalidatePropCache(propId)
    loadPropKeywords(propId)
  }, [currentProject, invalidatePropCache, loadPropKeywords])

  const deleteKeywordKw = useCallback(async (id: string) => {
    await api(`/connector/pbit/lakehouse-keywords?id=${id}&projectId=${currentProject?.id}`, { method: 'DELETE' })
    fetchKwData()
  }, [currentProject, fetchKwData])

  const toggleColumnName = useCallback(async (id: string, current: boolean, propId: string) => {
    try {
      await api('/connector/pbit/lakehouse-keywords/toggle-column-name', {
        method: 'POST',
        body: JSON.stringify({ id, isColumnName: !current }),
      })
      invalidatePropCache(propId)
      loadPropKeywords(propId)
    } catch { msg.error(t('toggle_failed')) }
  }, [invalidatePropCache, loadPropKeywords, msg])

  // ── Enable / Enable-as-MC a property's keywords ─────────────────
  // 启用 = sync the full distinct value set (with the >200 confirmation gate);
  // 启用MC = flip the property to machine-code and re-sync to ≤5 sampled values
  // + the column name. Both ride the same backend endpoint, which does a
  // DELETE-then-INSERT for the property (so excess keywords are pruned).
  const [enablingProp, setEnablingProp] = useState<string | null>(null)
  const enableProp = useCallback(async (propId: string, mc: boolean, force = false) => {
    if (!currentProject) return
    setEnablingProp(propId)
    try {
      const res = await api<{ success?: boolean; keywordCount?: number; error?: string; needsConfirmation?: boolean; distinctCount?: number; propertyName?: string }>(
        `/connector/pbit/sync-property-keywords`,
        { method: 'POST', body: { propertyId: propId, projectId: currentProject.id, machineCode: mc, force } },
      )
      if (res.needsConfirmation) {
        setEnablingProp(null)
        if (confirm(t('confirm_enable', { name: res.propertyName ?? '', count: res.distinctCount ?? 0 }))) {
          enableProp(propId, mc, true)
        }
        return
      }
      if (res.success) {
        msg.success(t('enable_done', { count: res.keywordCount ?? 0 }))
        invalidatePropCache(propId)
        loadPropKeywords(propId)
        refreshSummaryTree()
        fetchKwData()
        loadVecStatus() // MC excludes keywords from vectorization → refresh the "需要计算" count
      } else {
        msg.error(res.error || t('enable_failed'))
      }
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('enable_failed'))
    } finally {
      setEnablingProp(null)
    }
  }, [currentProject, t, msg, invalidatePropCache, loadPropKeywords, refreshSummaryTree, fetchKwData, loadVecStatus])

  // ── Bulk handlers ───────────────────────────────────────────────
  const handleBulkUpdate = async (ids: string[], field: BulkUpdateField, value: boolean, clear: () => void) => {
    setBulkBusy(true)
    try {
      const res = await api<{ updated: number }>(
        `/connector/pbit/lakehouse-keywords/bulk-update?projectId=${currentProject?.id}`,
        { method: 'POST', body: { ids, [field]: value } },
      )
      msg.success(t('bulk_updated', { count: res.updated }))
      clear()
      fetchKwData()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_update_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  const openBulkEdit = (ids: string[], clear: () => void) => {
    setBulkEditTarget({ ids, clear })
    setBulkEditField('isColumnName')
    setBulkEditValue(true)
    setBulkEditOpen(true)
  }

  const submitBulkEdit = async () => {
    if (!bulkEditTarget) return
    const { ids, clear } = bulkEditTarget
    await handleBulkUpdate(ids, bulkEditField, bulkEditValue, clear)
    setBulkEditOpen(false)
    setBulkEditTarget(null)
  }

  const openBulkReanchor = (ids: string[], clear: () => void) => {
    setBulkReanchorTarget({ ids, clear })
    setBulkReanchorMode('property')
    setBulkReanchorOd('')
    setBulkReanchorProperty('')
    setBulkReanchorIntent('')
    setBulkReanchorOpen(true)
  }

  const submitBulkReanchor = async () => {
    if (!bulkReanchorTarget) return
    const { ids, clear } = bulkReanchorTarget
    setBulkBusy(true)
    try {
      let body: Record<string, unknown> = { ids }
      if (bulkReanchorMode === 'property') {
        const od = odList.find(o => o.id === bulkReanchorOd)
        const prop = od?.properties.find(p => p.id === bulkReanchorProperty)
        if (!prop) { msg.error(t('reanchor_select_prop')); setBulkBusy(false); return }
        body = { ids, propertyId: prop.id, objectId: od?.id }
      } else if (bulkReanchorMode === 'intent') {
        if (!bulkReanchorIntent) { msg.error(t('reanchor_select_intent')); setBulkBusy(false); return }
        body = { ids, metricIntentId: bulkReanchorIntent }
      } else {
        // clear
        body = { ids, propertyId: null, metricIntentId: null, objectId: null }
      }
      const res = await api<{ reanchored: number }>(
        `/connector/pbit/lakehouse-keywords/bulk-reanchor?projectId=${currentProject?.id}`,
        { method: 'POST', body },
      )
      msg.success(t('reanchored_ok', { count: res.reanchored }))
      setBulkReanchorOpen(false)
      setBulkReanchorTarget(null)
      clear()
      fetchKwData()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('reanchor_failed'))
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
        `/connector/pbit/lakehouse-keywords/bulk-impact?projectId=${currentProject?.id}`,
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
        `/connector/pbit/lakehouse-keywords/bulk-delete?projectId=${currentProject?.id}`,
        { method: 'POST', body: { ids } },
      )
      msg.success(t('deleted_ok', { count: res.deleted }))
      setBulkDeleteOpen(false)
      setBulkDeleteTarget(null)
      clear()
      fetchKwData()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_delete_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  // ── CSV / JSON bulk-create ───────────────────────────────────────
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
        const keyword = String(o.keyword || '').trim()
        if (!keyword) throw new Error(t('csv_row_empty', { row: i + 1 }))
        const aliases: string[] = Array.isArray(o.aliases)
          ? (o.aliases as unknown[]).map(String)
          : []
        return {
          keyword,
          propertyId: o.propertyId ? String(o.propertyId) : undefined,
          metricIntentId: o.metricIntentId ? String(o.metricIntentId) : undefined,
          objectId: o.objectId ? String(o.objectId) : undefined,
          isColumnName: typeof o.isColumnName === 'boolean' ? o.isColumnName : undefined,
          aliases: aliases.length > 0 ? aliases : undefined,
        }
      })
    }
    // CSV path — header: keyword,od,property,isColumnName,aliases
    const lines = trimmed.split(/\r?\n/).filter(l => l.trim())
    if (lines.length < 2) throw new Error(t('csv_need_header'))
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
    const idxKw = headers.indexOf('keyword')
    if (idxKw < 0) throw new Error(t('csv_no_keyword_col'))
    const idxOd = headers.indexOf('od')
    const idxProp = headers.indexOf('property')
    const idxColName = headers.indexOf('iscolumnname')
    const idxAliases = headers.indexOf('aliases')
    return lines.slice(1).map((line, i) => {
      const cols = split(line)
      const keyword = (cols[idxKw] || '').trim()
      if (!keyword) throw new Error(t('csv_row_empty', { row: i + 2 }))
      // Resolve od + property names to IDs via odList
      let propertyId: string | undefined
      let objectId: string | undefined
      if (idxOd >= 0 && idxProp >= 0) {
        const odName = (cols[idxOd] || '').trim()
        const propName = (cols[idxProp] || '').trim()
        if (odName && propName) {
          const od = odList.find(o => o.name.toLowerCase() === odName.toLowerCase())
          if (od) {
            objectId = od.id
            const prop = od.properties.find(p => p.name.toLowerCase() === propName.toLowerCase())
            if (prop) propertyId = prop.id
          }
        }
      }
      const isColumnNameRaw = idxColName >= 0 ? (cols[idxColName] || '').trim().toLowerCase() : ''
      const isColumnName = isColumnNameRaw === 'true' || isColumnNameRaw === '1' ? true
        : isColumnNameRaw === 'false' || isColumnNameRaw === '0' ? false : undefined
      const aliasesRaw = idxAliases >= 0 ? (cols[idxAliases] || '').trim() : ''
      const aliases = aliasesRaw ? aliasesRaw.split('|').map(a => a.trim()).filter(Boolean) : undefined
      return { keyword, propertyId, objectId, isColumnName, aliases }
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
        `/connector/pbit/lakehouse-keywords/bulk-create?projectId=${currentProject.id}`,
        { method: 'POST', body: { items: csvParsed } },
      )
      setCsvResult(res)
      if (res.errors.length === 0) {
        msg.success(t('bulk_created_ok', { count: res.created }))
      } else {
        msg.warning(t('bulk_created_partial', { created: res.created, failed: res.errors.length }))
      }
      fetchKwData()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('bulk_create_failed'))
    } finally {
      setBulkBusy(false)
    }
  }

  // ── DataTable columns for keyword mode ──────────────────────────
  const kwColumns: Column<LakehouseKeyword>[] = [
    {
      key: 'keyword', title: t('col_keyword'), sortable: true,
      render: (_, kw) => (
        <div className="pt-0.5">
          <span className="text-sm font-semibold text-ink">{kw.keyword}</span>
          {kw.isMachineCode && (
            <span className="ml-1.5 rounded-[3px] border border-border bg-canvas-alt px-1 py-0.5 text-[10px] text-ink-muted">MC</span>
          )}
          {kw.isOrphaned && (
            <span className="ml-1.5 rounded-[3px] border border-danger/30 bg-danger/10 px-1 py-0.5 text-[10px] text-danger">{t('badge_orphan')}</span>
          )}
          {kw.isStopword && (
            <span className="ml-1.5 rounded-[3px] border border-border bg-canvas-alt px-1 py-0.5 text-[10px] text-ink-ghost">{t('badge_stopword')}</span>
          )}
        </div>
      ),
    },
    {
      key: 'anchor', title: t('col_anchor'), width: '260px',
      render: (_, kw) => (
        <div className="flex flex-wrap items-center gap-1 pt-0.5">
          {kw.odName ? (
            <>
              <span className={`bg-ink px-1.5 py-0.5 leading-none text-white ${industrial ? 'font-mono text-[10px] tracking-[0.06em]' : 'rounded-md text-[11px] font-medium'}`}>
                {kw.odName}
              </span>
              {kw.propName && (
                <>
                  <span className="text-[10px] text-ink-ghost">·</span>
                  <span className={`border border-border px-1.5 py-0.5 leading-none text-ink-muted ${industrial ? 'font-mono text-[10px] tracking-[0.04em]' : 'rounded-md text-[11px]'}`}>
                    {kw.propName}
                  </span>
                </>
              )}
              {/* Metric anchor (lakehouse_metric.metric_id). Surfaces triggers
                  authored in the metric editor — without this they'd render
                  as just an OD chip with no hint that they bind a metric. */}
              {kw.metricName && (
                <>
                  <span className="text-[10px] text-ink-ghost">·</span>
                  <span
                    className={`border px-1.5 py-0.5 leading-none ${industrial ? 'font-mono text-[10px] tracking-[0.04em]' : 'rounded-md text-[11px]'}`}
                    style={{ color: '#7C3AED', borderColor: '#C4B5FD', backgroundColor: '#F5F3FF' }}
                    title={kw.metricDisplayName || kw.metricName}
                  >
                    🎯 {kw.metricName}
                  </span>
                </>
              )}
            </>
          ) : (
            <span className={`border border-border bg-canvas-alt px-1.5 py-0.5 text-ink-ghost ${industrial ? 'font-mono text-[10px]' : 'rounded-md text-[11px]'}`}>—</span>
          )}
        </div>
      ),
    },
    {
      key: 'aliases', title: t('col_aliases'),
      render: (_, kw) => (
        <div className="flex flex-wrap items-center gap-1 min-w-0">
          {(kw.aliases || []).map(alias => (
            <motion.span
              key={alias}
              whileHover={reduce ? undefined : { y: -1 }}
              transition={{ duration: 0.1 }}
              className={`inline-flex items-center gap-0.5 border border-border bg-canvas-alt px-1.5 py-0.5 leading-none text-ink-muted ${industrial ? 'font-mono text-[10px] tracking-[0.04em]' : 'rounded-md text-[11px]'}`}
            >
              {alias}
              <motion.button
                onClick={() => removeAlias(kw.id, kw.aliases || [], alias)}
                whileHover={reduce ? undefined : { scale: 1.15 }}
                whileTap={reduce ? undefined : { scale: 0.9 }}
                aria-label={t('remove_alias_aria', { alias })}
                className="ml-0.5 flex-shrink-0 text-ink-ghost hover:text-danger"
              >
                <X size={9} aria-hidden="true" />
              </motion.button>
            </motion.span>
          ))}
          {kwAddOpen.has(kw.id) ? (
            <div className="flex items-center gap-1">
              <input
                autoFocus
                value={kwAddInput[kw.id] || ''}
                onChange={e => setKwAddInput(prev => ({ ...prev, [kw.id]: e.target.value }))}
                onKeyDown={e => {
                  if (e.key === 'Enter') addAlias(kw.id, kw.aliases || [])
                  if (e.key === 'Escape') setKwAddOpen(prev => { const n = new Set(prev); n.delete(kw.id); return n })
                }}
                aria-label={t('alias_input_aria')}
                placeholder={t('alias_input_placeholder')}
                className="h-6 w-36 rounded-md border border-ink px-1.5 text-[11px] outline-none focus:ring-1 focus:ring-ink/20"
              />
              <AnimatedButton
                variant="primary"
                size="xs"
                onClick={() => addAlias(kw.id, kw.aliases || [])}
                disabled={!(kwAddInput[kw.id] || '').trim() || kwSaving.has(kw.id)}
                aria-label={t('alias_confirm_aria')}
              >✓</AnimatedButton>
              <AnimatedButton
                variant="secondary"
                size="xs"
                onClick={() => setKwAddOpen(prev => { const n = new Set(prev); n.delete(kw.id); return n })}
                aria-label={t('alias_cancel_aria')}
              >✕</AnimatedButton>
            </div>
          ) : (
            <motion.button
              onClick={() => setKwAddOpen(prev => { const n = new Set(prev); n.has(kw.id) ? n.delete(kw.id) : n.add(kw.id); return n })}
              whileHover={reduce ? undefined : { scale: 1.02 }}
              whileTap={reduce ? undefined : { scale: 0.96 }}
              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
              aria-label={t('alias_add_aria')}
              className={`inline-flex h-6 items-center gap-0.5 border border-dashed border-border px-1.5 leading-none text-ink-ghost transition-colors hover:border-ink hover:text-ink ${industrial ? 'font-mono text-[10px] tracking-[0.04em]' : 'rounded-md text-[11px]'}`}
            >
              <Plus size={10} aria-hidden="true" /> {t('alias_add_label')}
            </motion.button>
          )}
          {kwSaving.has(kw.id) && <InlineLoader text={t('saving')} />}
        </div>
      ),
    },
    {
      key: 'isColumnName', title: t('col_type'), width: '70px', sortable: true,
      render: (_, kw) => (
        <span className={`rounded-[3px] border px-1.5 py-0.5 text-[10px] font-medium ${
          kw.isColumnName
            ? 'border-ink bg-ink text-white'
            : 'border-border bg-white text-ink-muted'
        }`}>
          {kw.isColumnName ? t('type_col') : t('type_val')}
        </span>
      ),
    },
    {
      key: 'syncedAt', title: t('col_synced_at'), width: '100px', sortable: true,
      render: (_, kw) => (
        <span className="font-mono text-[11px] text-ink-ghost">
          {kw.syncedAt ? new Date(kw.syncedAt).toLocaleDateString() : '—'}
        </span>
      ),
    },
    {
      key: 'actions', title: '', width: '40px',
      render: (_, kw) => (
        <motion.button
          onClick={() => deleteKeywordKw(kw.id)}
          whileHover={reduce ? undefined : { scale: 1.1 }}
          whileTap={reduce ? undefined : { scale: 0.9 }}
          aria-label={t('delete_keyword_aria')}
          className="text-ink-ghost hover:text-danger"
        >
          <Trash2 size={14} aria-hidden="true" />
        </motion.button>
      ),
    },
  ]

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1 text-center">
        <div className="text-sm text-ink-muted">{t('no_project')}</div>
        <div className="text-xs text-ink-ghost">{t('no_project_hint')}</div>
      </div>
    )
  }

  // ────────────────────────────────────────────────────────────────
  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* ── Header ── */}
      <motion.header
        initial={{ opacity: 0, y: -4 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.2, ease: 'easeOut' }}
        className={`flex h-14 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'}`}
      >
        <div className="flex min-w-0 items-center gap-3">
          {industrial ? (
            <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // {t('title').toString().toUpperCase()}
            </span>
          ) : (
            <>
              <Tags size={18} className="text-ink" aria-hidden="true" />
              <h1 className="text-base font-semibold tracking-tight text-ink whitespace-nowrap">
                {t('title')}
              </h1>
            </>
          )}
          <span className={industrial ? 'font-mono text-[10px] tracking-[0.14em] text-ink-muted truncate tabular-nums' : 'text-xs text-ink-ghost truncate'}>
            {industrial
              ? `${(summary?.total ?? 0).toLocaleString()} TOTAL · ${(summary?.columnCount ?? 0).toLocaleString()} COLS · ${(summary?.valueCount ?? 0).toLocaleString()} VALS`
              : t('summary', { total: (summary?.total ?? 0).toLocaleString(), cols: (summary?.columnCount ?? 0).toLocaleString(), vals: (summary?.valueCount ?? 0).toLocaleString() })}
          </span>
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
          {vecStatus && (
            <div
              className={`inline-flex h-7 items-center gap-1.5 border px-2.5 ${
                industrial ? 'font-mono text-[10px] tracking-[0.06em] uppercase' : 'rounded-md text-[11px]'
              } ${
                vecStatus.needsCompute === 0
                  ? 'border-success/30 bg-success/10 text-success'
                  : 'border-border bg-canvas-alt text-ink-muted'
              }`}
              title={t('vec_coverage_title')}
            >
              <span
                className={`inline-block h-1.5 w-1.5 ${industrial ? '' : 'rounded-full'} ${
                  vecStatus.needsCompute === 0 ? 'bg-success' : 'bg-ink-ghost'
                }`}
                aria-hidden="true"
              />
              <span className={industrial ? 'font-bold' : 'font-medium'}>
                {industrial ? t('vec_label').toString().toUpperCase() : t('vec_label')}
              </span>
              <span>{t('vec_keywords', { with: vecStatus.keywords.withVector, total: vecStatus.keywords.total })}</span>
              <span className="opacity-50">·</span>
              <span>{t('vec_aliases', { with: vecStatus.aliases.withVector, total: vecStatus.aliases.total })}</span>
            </div>
          )}
          <RefreshButton onClick={loadVecStatus} aria-label={t('refresh_vec_aria')} />
          <AnimatedButton
            variant="primary"
            size="sm"
            onClick={startCompute}
            disabled={computing || !vecStatus || vecStatus.needsCompute === 0}
            aria-label={t('compute_vec_aria')}
            title={vecStatus?.needsCompute === 0 ? t('vec_all_ready') : t('compute_vec_aria')}
          >
            <motion.span
              className="inline-flex"
              animate={computing && !reduce ? { rotate: 360 } : { rotate: 0 }}
              transition={computing ? { repeat: Infinity, duration: 1, ease: 'linear' } : { duration: 0 }}
              aria-hidden="true"
            >
              <Sparkles size={12} />
            </motion.span>
            {computing && vecProgress ? (
              <span>{t('computing', { done: vecProgress.done, total: vecProgress.total })}</span>
            ) : (
              <span>{t('compute_vec')}{vecStatus?.needsCompute ? ` (${vecStatus.needsCompute})` : ''}</span>
            )}
          </AnimatedButton>
          {activeTab === 'grouped' && (
            <>
              <AnimatedButton variant="ghost" size="sm" onClick={expandAll} aria-label={t('expand_all')}>
                {t('expand_all')}
              </AnimatedButton>
              <AnimatedButton variant="ghost" size="sm" onClick={collapseAll} aria-label={t('collapse_all')}>
                {t('collapse_all')}
              </AnimatedButton>
            </>
          )}
        </div>
      </motion.header>

      {/* ── Tab bar ── */}
      <nav className="relative flex flex-shrink-0 items-center gap-1 border-b border-border bg-white px-6" aria-label={t('tab_nav_aria')} role="tablist">
        {(['grouped', 'keyword'] as TabType[]).map(tab => {
          const selected = activeTab === tab
          return (
            <button
              key={tab}
              role="tab"
              aria-selected={selected}
              onClick={() => setActiveTab(tab)}
              className={`relative h-10 px-4 text-sm font-medium transition-colors outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                selected ? 'text-ink' : 'text-ink-ghost hover:text-ink-muted'
              }`}
            >
              {tab === 'grouped' ? t('tab_grouped') : t('tab_keyword')}
              {selected && (
                <motion.span
                  layoutId={TAB_UNDERLINE_ID}
                  className="absolute inset-x-0 bottom-[-1px] h-[2px] bg-ink"
                  transition={reduce ? { duration: 0 } : { type: 'spring', stiffness: 500, damping: 35 }}
                />
              )}
            </button>
          )
        })}
      </nav>

      {/* ── Tab content ── */}
      <AnimatePresence mode="wait">
        {activeTab === 'grouped' ? (
          <motion.section
            key="grouped"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -2 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
            className="flex flex-1 min-h-0 flex-col"
          >
            {/* Filters */}
            <div className="flex flex-shrink-0 flex-wrap items-center gap-2 border-b border-border bg-white px-6 py-3">
              <div className="flex h-8 flex-1 min-w-[200px] max-w-sm items-center gap-1.5 rounded-md border border-border bg-white px-2.5 focus-within:border-ink">
                <Search size={14} className="flex-shrink-0 text-ink-ghost" aria-hidden="true" />
                <input
                  value={search}
                  onChange={e => setSearch(e.target.value)}
                  aria-label={t('search_grouped_aria')}
                  placeholder={t('search_grouped_placeholder')}
                  className="w-full bg-transparent text-sm text-ink outline-none placeholder:text-ink-ghost"
                />
              </div>
              <SegmentedControl
                ariaLabel={t('type_filter_aria')}
                value={typeFilter}
                onChange={setTypeFilter}
                options={[
                  ['all', 'ALL'],
                  ['column', t('type_col_count', { count: summary?.columnCount || 0 })],
                  ['value', t('type_val_count', { count: summary?.valueCount || 0 })],
                ]}
              />
              <SegmentedControl
                ariaLabel={t('mc_filter_aria')}
                value={mcFilter}
                onChange={setMcFilter}
                options={[
                  ['all', 'ALL'],
                  ['semantic', 'SEMANTIC'],
                  ['mc', `MC (${summary?.mcCount || 0})`],
                ]}
              />
              {(summary?.odNames || []).length > 0 && (
                <select
                  value={odFilter}
                  onChange={e => setOdFilter(e.target.value)}
                  aria-label={t('od_filter_aria')}
                  className="h-8 rounded-md border border-border bg-white px-2.5 text-xs text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
                >
                  <option value="">ALL OBJECTS</option>
                  {(summary?.odNames || []).map(n => <option key={n} value={n}>{n}</option>)}
                </select>
              )}
            </div>

            {/* Scroll region */}
            <div className="flex-1 min-h-0 overflow-y-auto">
              {filteredTree.length === 0 ? (
                <EmptyState primary={t('no_match_od')} secondary={t('clear_filters_hint')} />
              ) : (
                <div className="divide-y divide-border bg-white">
                  {filteredTree.map(od => {
                    const odExpanded = expandedOds.has(od.name)
                    return (
                      <div key={od.name}>
                        <motion.button
                          onClick={() => toggleOd(od.name)}
                          aria-expanded={odExpanded}
                          aria-label={`${odExpanded ? t('collapse') : t('expand')} ${od.name}`}
                          className="group flex w-full items-center gap-2 bg-canvas-alt px-3 py-2.5 text-left cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink"
                        >
                          <motion.span
                            animate={{ rotate: odExpanded ? 90 : 0 }}
                            transition={reduce ? { duration: 0 } : { duration: 0.15, ease: 'easeOut' }}
                            className="inline-flex"
                          >
                            <ChevronRight size={14} className="text-ink-muted" aria-hidden="true" />
                          </motion.span>
                          <span className="text-sm font-semibold text-ink">{od.name}</span>
                          <span className="text-[11px] text-ink-ghost">({od.props.length} props)</span>
                          <span className="ml-auto text-[11px] text-ink-muted">
                            <span>{t('col_count', { count: od.colCount })}</span>
                            <span className="mx-1 text-ink-ghost">·</span>
                            <span>{t('val_count', { count: od.valCount })}</span>
                          </span>
                        </motion.button>
                        <AnimatePresence initial={false}>
                          {odExpanded && (
                            <motion.div
                              key="props"
                              initial={reduce ? {} : { height: 0, opacity: 0 }}
                              animate={reduce ? {} : { height: 'auto', opacity: 1 }}
                              exit={reduce ? {} : { height: 0, opacity: 0 }}
                              transition={{ duration: 0.2, ease: 'easeOut' }}
                              style={{ overflow: 'hidden' }}
                              className="divide-y divide-border-light border-t border-border"
                            >
                              {od.props.map(prop => {
                                const propKey = od.name + '::' + prop.name
                                const propExpanded = expandedProps.has(propKey)
                                const valuesKey = propKey + '::values'
                                const showAllValues = expandedValues.has(valuesKey)
                                const cached = propKwCache[prop.id]
                                const isLoading = loadingProps.has(prop.id)
                                const columnAliases = cached ? cached.filter(k => k.isColumnName) : []
                                const values = cached ? cached.filter(k => !k.isColumnName) : []
                                const visibleValues = showAllValues ? values : values.slice(0, 10)
                                const hiddenCount = values.length - 10
                                return (
                                  <div key={propKey}>
                                    <motion.button
                                      onClick={() => toggleProp(propKey, prop.id)}
                                      aria-expanded={propExpanded}
                                      aria-label={`${propExpanded ? t('collapse') : t('expand')} ${prop.name}`}
                                      className="group flex w-full items-center gap-2 px-3 py-2 pl-8 text-left cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink"
                                    >
                                      <motion.span
                                        animate={{ rotate: propExpanded ? 90 : 0 }}
                                        transition={reduce ? { duration: 0 } : { duration: 0.15, ease: 'easeOut' }}
                                        className="inline-flex"
                                      >
                                        <ChevronRight size={12} className="text-ink-ghost" aria-hidden="true" />
                                      </motion.span>
                                      <span className="text-[13px] font-medium text-ink">{prop.name}</span>
                                      {prop.dataType && <span className="text-[11px] text-ink-ghost">({prop.dataType})</span>}
                                      <span className="ml-auto text-[11px] text-ink-muted">
                                        <span>{t('col_count', { count: prop.colCount })}</span>
                                        <span className="mx-1 text-ink-ghost">·</span>
                                        <span>{t('val_count', { count: prop.valCount })}</span>
                                      </span>
                                    </motion.button>
                                    <AnimatePresence initial={false}>
                                      {propExpanded && (
                                        <motion.div
                                          key="body"
                                          initial={reduce ? {} : { height: 0, opacity: 0 }}
                                          animate={reduce ? {} : { height: 'auto', opacity: 1 }}
                                          exit={reduce ? {} : { height: 0, opacity: 0 }}
                                          transition={{ duration: 0.2, ease: 'easeOut' }}
                                          style={{ overflow: 'hidden' }}
                                          className="space-y-3 px-3 py-3 pl-14"
                                        >
                                          {/* Enable 语义 / MC — the active mode is INHERITED from the OD
                                              definition's is_machine_code; clicking re-syncs in that mode
                                              (启用语义 = full distinct set, 启用MC = ≤5 sampled + column name). */}
                                          <div className="flex flex-wrap items-center gap-2">
                                            <button
                                              type="button"
                                              onClick={() => enableProp(prop.id, false)}
                                              disabled={enablingProp === prop.id}
                                              aria-pressed={!prop.isMachineCode}
                                              className={`rounded-md border px-2.5 py-1 text-[11px] font-medium outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
                                                !prop.isMachineCode ? 'border-ink bg-ink text-white' : 'border-border bg-white text-ink hover:border-ink'
                                              }`}
                                            >
                                              {enablingProp === prop.id ? t('enabling') : `${t('enable_full')}${!prop.isMachineCode ? t('current_tag') : ''}`}
                                            </button>
                                            <button
                                              type="button"
                                              onClick={() => enableProp(prop.id, true)}
                                              disabled={enablingProp === prop.id}
                                              aria-pressed={prop.isMachineCode}
                                              className={`rounded-md border border-amber-500 px-2.5 py-1 text-[11px] font-medium outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
                                                prop.isMachineCode ? 'bg-amber-400 text-white' : 'bg-amber-50 text-amber-700 hover:bg-amber-100'
                                              }`}
                                            >
                                              {enablingProp === prop.id ? t('enabling') : `${t('enable_mc')}${prop.isMachineCode ? t('current_tag') : ''}`}
                                            </button>
                                            <span className="text-[11px] text-ink-ghost">{t('enable_hint')}</span>
                                          </div>
                                          {isLoading && !cached && (
                                            <InlineLoader text={t('loading')} />
                                          )}
                                          {cached && columnAliases.length > 0 && (
                                            <div>
                                              <div className="mb-1.5 text-[11px] font-medium text-ink">{t('section_col_aliases')}</div>
                                              <div className="flex flex-wrap gap-1.5">
                                                {columnAliases.map(kw => (
                                                  <KeywordTag
                                                    key={kw.id}
                                                    kw={kw}
                                                    onToggle={(id, cur) => toggleColumnName(id, cur, prop.id)}
                                                    onDelete={id => deleteKeyword(id, prop.id)}
                                                  />
                                                ))}
                                              </div>
                                            </div>
                                          )}
                                          {cached && values.length > 0 && (
                                            <div>
                                              <div className="mb-1.5 text-[11px] font-medium text-ink-muted">{t('section_values')}</div>
                                              <div className="flex flex-wrap gap-1.5">
                                                {visibleValues.map(kw => (
                                                  <KeywordTag
                                                    key={kw.id}
                                                    kw={kw}
                                                    onToggle={(id, cur) => toggleColumnName(id, cur, prop.id)}
                                                    onDelete={id => deleteKeyword(id, prop.id)}
                                                  />
                                                ))}
                                                {!showAllValues && hiddenCount > 0 && (
                                                  <AnimatedButton
                                                    variant="ghost"
                                                    size="xs"
                                                    onClick={() => toggleValueExpand(valuesKey)}
                                                    aria-label={t('show_all_count', { count: values.length })}
                                                  >
                                                    {t('show_all_count', { count: values.length })}
                                                  </AnimatedButton>
                                                )}
                                                {showAllValues && hiddenCount > 0 && (
                                                  <AnimatedButton
                                                    variant="ghost"
                                                    size="xs"
                                                    onClick={() => toggleValueExpand(valuesKey)}
                                                    aria-label={t('collapse')}
                                                  >
                                                    {t('collapse')}
                                                  </AnimatedButton>
                                                )}
                                              </div>
                                            </div>
                                          )}
                                          {cached && columnAliases.length === 0 && values.length === 0 && (
                                            <div className="text-[11px] text-ink-ghost">{t('no_keywords')}</div>
                                          )}
                                        </motion.div>
                                      )}
                                    </AnimatePresence>
                                  </div>
                                )
                              })}
                            </motion.div>
                          )}
                        </AnimatePresence>
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          </motion.section>
        ) : (
          <motion.section
            key="keyword"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -2 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
            className="flex flex-1 min-h-0 flex-col"
          >
            {/* Filters */}
            <div className="flex flex-shrink-0 flex-wrap items-center gap-2 border-b border-border bg-white px-6 py-3">
              <div className="flex h-8 items-center rounded-md border border-border bg-white focus-within:border-ink">
                <div className="flex items-center gap-1.5 px-2.5">
                  <Search size={14} className="flex-shrink-0 text-ink-ghost" aria-hidden="true" />
                  <input
                    value={kwSearch}
                    onChange={e => setKwSearch(e.target.value)}
                    aria-label={t('search_kw_aria')}
                    placeholder={t('search_kw_placeholder')}
                    className="w-44 bg-transparent text-sm text-ink outline-none placeholder:text-ink-ghost"
                  />
                </div>
                <div className="flex h-full border-l border-border" role="radiogroup" aria-label={t('match_mode_aria')}>
                  {(['fuzzy', 'exact'] as MatchMode[]).map(mode => (
                    <button
                      key={mode}
                      onClick={() => setKwMatchMode(mode)}
                      role="radio"
                      aria-checked={kwMatchMode === mode}
                      className={`px-2.5 text-[11px] font-medium transition-colors outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                        kwMatchMode === mode ? 'bg-ink text-white' : 'text-ink-muted hover:text-ink'
                      } ${mode === 'exact' ? 'rounded-r-md' : ''}`}
                    >
                      {mode === 'exact' ? t('match_exact') : t('match_fuzzy')}
                    </button>
                  ))}
                </div>
              </div>

              <SegmentedControl
                ariaLabel={t('has_ontology_filter_aria')}
                value={kwHasOntology}
                onChange={setKwHasOntology}
                options={[
                  ['all', 'ALL'],
                  ['yes', t('has_ontology_yes')],
                  ['no', t('has_ontology_no')],
                ]}
              />

              <select
                value={kwOd}
                onChange={e => setKwOd(e.target.value)}
                aria-label={t('od_filter_aria')}
                className="h-8 rounded-md border border-border bg-white px-2.5 text-xs text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
              >
                <option value="">ALL OBJECTS</option>
                {(summary?.odNames || []).map(n => <option key={n} value={n}>{n}</option>)}
              </select>

              {kwOd && kwPropNames.length > 0 && (
                <select
                  value={kwProp}
                  onChange={e => setKwProp(e.target.value)}
                  aria-label={t('prop_filter_aria')}
                  className="h-8 rounded-md border border-border bg-white px-2.5 text-xs text-ink outline-none hover:border-ink focus-visible:ring-1 focus-visible:ring-ink"
                >
                  <option value="">ALL PROPS</option>
                  {kwPropNames.map(n => <option key={n} value={n}>{n}</option>)}
                </select>
              )}

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

              <span className="ml-auto text-xs text-ink-ghost">
                {t('total_count', { count: kwTotal.toLocaleString() })}
              </span>
            </div>

            {/* DataTable scroll region */}
            <div className="flex-1 min-h-0 overflow-y-auto">
              {kwLoading ? (
                <div className="flex h-full items-center justify-center">
                  <CyberLoader text="LOADING" />
                </div>
              ) : (
                <DataTable
                  columns={kwColumns}
                  data={kwKeywords}
                  rowKey="id"
                  batchActions={(selectedIds, clearSelection) => (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <BulkActionButton
                        onClick={() => handleBulkUpdate(selectedIds, 'isColumnName', true, clearSelection)}
                        disabled={bulkBusy}
                        title={t('bulk_mark_col_title')}
                      >
                        {t('bulk_mark_col')}
                      </BulkActionButton>
                      <BulkActionButton
                        onClick={() => handleBulkUpdate(selectedIds, 'isColumnName', false, clearSelection)}
                        disabled={bulkBusy}
                        title={t('bulk_mark_val_title')}
                      >
                        {t('bulk_mark_val')}
                      </BulkActionButton>
                      <span className="h-3.5 w-px bg-border" aria-hidden="true" />
                      <BulkActionButton
                        onClick={() => handleBulkUpdate(selectedIds, 'isStopword', true, clearSelection)}
                        disabled={bulkBusy}
                        title={t('bulk_add_stopword_title')}
                      >
                        {t('bulk_add_stopword')}
                      </BulkActionButton>
                      <BulkActionButton
                        onClick={() => handleBulkUpdate(selectedIds, 'isStopword', false, clearSelection)}
                        disabled={bulkBusy}
                        title={t('bulk_remove_stopword_title')}
                      >
                        {t('bulk_remove_stopword')}
                      </BulkActionButton>
                      <span className="h-3.5 w-px bg-border" aria-hidden="true" />
                      <BulkActionButton
                        onClick={() => openBulkReanchor(selectedIds, clearSelection)}
                        disabled={bulkBusy}
                        title={t('bulk_reanchor_title')}
                      >
                        <ArrowUpDown size={11} aria-hidden="true" />
                        {t('bulk_reanchor')}
                      </BulkActionButton>
                      <BulkActionButton
                        onClick={() => openBulkDelete(selectedIds, clearSelection)}
                        disabled={bulkBusy}
                        danger
                        title={t('bulk_delete_title')}
                      >
                        <Trash2 size={11} aria-hidden="true" />
                        {t('bulk_delete')}
                      </BulkActionButton>
                    </div>
                  )}
                />
              )}
            </div>

            {/* Pagination */}
            {kwTotal > KW_PAGE_SIZE && (
              <div className="flex flex-shrink-0 items-center justify-between border-t border-border bg-white px-6 py-2.5">
                <span className="text-xs text-ink-ghost">
                  {t('page_range', { start: (kwPage - 1) * KW_PAGE_SIZE + 1, end: Math.min(kwPage * KW_PAGE_SIZE, kwTotal), total: kwTotal.toLocaleString() })}
                </span>
                <div className="flex items-center gap-2">
                  <AnimatedButton
                    variant="secondary"
                    size="sm"
                    onClick={() => setKwPage(p => Math.max(1, p - 1))}
                    disabled={kwPage <= 1}
                    aria-label={t('prev_page')}
                  >
                    <ChevronLeft size={12} aria-hidden="true" /> {t('prev_page')}
                  </AnimatedButton>
                  <span className="text-xs text-ink-muted">
                    {t('page_of', { page: kwPage, total: kwTotalPages })}
                  </span>
                  <AnimatedButton
                    variant="secondary"
                    size="sm"
                    onClick={() => setKwPage(p => Math.min(kwTotalPages, p + 1))}
                    disabled={kwPage >= kwTotalPages}
                    aria-label={t('next_page')}
                  >
                    {t('next_page')} <ChevronRight size={12} aria-hidden="true" />
                  </AnimatedButton>
                </div>
              </div>
            )}
          </motion.section>
        )}
      </AnimatePresence>

      {/* ── Bulk Edit Modal ── */}
      <Modal
        open={bulkEditOpen}
        onClose={() => !bulkBusy && setBulkEditOpen(false)}
        title={t('bulk_edit_title', { count: bulkEditTarget?.ids.length || 0 })}
        width="480px"
      >
        <div className="space-y-4">
          <div>
            <label className="mb-1.5 block text-sm font-medium text-ink">{t('bulk_edit_field_label')}</label>
            <SegmentedFilter<BulkUpdateField>
              value={bulkEditField}
              onChange={setBulkEditField}
              options={[
                ['isColumnName', t('field_is_col')],
                ['isStopword', t('field_is_stopword')],
              ]}
            />
          </div>
          <div>
            <label className="mb-1.5 block text-sm font-medium text-ink">{t('bulk_edit_value_label')}</label>
            <SegmentedFilter<string>
              value={String(bulkEditValue)}
              onChange={v => setBulkEditValue(v === 'true')}
              options={[
                ['true', bulkEditField === 'isColumnName' ? t('is_col_true') : t('add_stopword')],
                ['false', bulkEditField === 'isColumnName' ? t('is_col_false') : t('remove_stopword')],
              ]}
            />
          </div>
          <div className="flex justify-end gap-2 border-t border-border-light pt-3">
            <AnimatedButton variant="ghost" size="md" onClick={() => setBulkEditOpen(false)} disabled={bulkBusy}>{t('cancel')}</AnimatedButton>
            <AnimatedButton variant="primary" size="md" onClick={submitBulkEdit} disabled={bulkBusy}>
              {bulkBusy ? t('submitting') : t('apply_changes')}
            </AnimatedButton>
          </div>
        </div>
      </Modal>

      {/* ── Bulk Reanchor Modal ── */}
      <Modal
        open={bulkReanchorOpen}
        onClose={() => !bulkBusy && setBulkReanchorOpen(false)}
        title={t('reanchor_title', { count: bulkReanchorTarget?.ids.length || 0 })}
        width="520px"
      >
        <div className="space-y-4">
          <div>
            <label className="mb-1.5 block text-sm font-medium text-ink">{t('reanchor_target_label')}</label>
            <SegmentedFilter<BulkReanchorTarget>
              value={bulkReanchorMode}
              onChange={v => { setBulkReanchorMode(v); setBulkReanchorOd(''); setBulkReanchorProperty(''); setBulkReanchorIntent('') }}
              options={[
                ['property', t('reanchor_mode_prop')],
                ['intent', t('reanchor_mode_intent')],
                ['clear', t('reanchor_mode_clear')],
              ]}
            />
          </div>

          {bulkReanchorMode === 'property' && (
            <>
              <div>
                <label htmlFor="reanchor-od" className="mb-1.5 block text-sm font-medium text-ink">{t('reanchor_od_label')}</label>
                <select
                  id="reanchor-od"
                  value={bulkReanchorOd}
                  onChange={e => { setBulkReanchorOd(e.target.value); setBulkReanchorProperty('') }}
                  className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink focus:ring-1 focus:ring-ink/10"
                >
                  <option value="">{t('select_od')}</option>
                  {odList.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}
                </select>
              </div>
              {bulkReanchorOd && (
                <div>
                  <label htmlFor="reanchor-prop" className="mb-1.5 block text-sm font-medium text-ink">{t('reanchor_prop_label')}</label>
                  <select
                    id="reanchor-prop"
                    value={bulkReanchorProperty}
                    onChange={e => setBulkReanchorProperty(e.target.value)}
                    className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink focus:ring-1 focus:ring-ink/10"
                  >
                    <option value="">{t('select_prop')}</option>
                    {(odList.find(o => o.id === bulkReanchorOd)?.properties || []).map(p => (
                      <option key={p.id} value={p.id}>{p.name}</option>
                    ))}
                  </select>
                </div>
              )}
            </>
          )}

          {bulkReanchorMode === 'intent' && (
            <div>
              <label htmlFor="reanchor-intent" className="mb-1.5 block text-sm font-medium text-ink">{t('reanchor_intent_label')}</label>
              <select
                id="reanchor-intent"
                value={bulkReanchorIntent}
                onChange={e => setBulkReanchorIntent(e.target.value)}
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink focus:ring-1 focus:ring-ink/10"
              >
                <option value="">{t('select_intent')}</option>
                {intentList.map(i => <option key={i.id} value={i.id}>{i.name}</option>)}
              </select>
            </div>
          )}

          {bulkReanchorMode === 'clear' && (
            <div className="rounded-md border border-border-light bg-canvas-alt p-3 text-sm text-ink-muted leading-relaxed">
              {t('reanchor_clear_notice')}
            </div>
          )}

          <div className="flex justify-end gap-2 border-t border-border-light pt-3">
            <AnimatedButton variant="ghost" size="md" onClick={() => setBulkReanchorOpen(false)} disabled={bulkBusy}>{t('cancel')}</AnimatedButton>
            <AnimatedButton variant="primary" size="md" onClick={submitBulkReanchor} disabled={bulkBusy}>
              {bulkBusy ? t('submitting') : t('confirm_reanchor')}
            </AnimatedButton>
          </div>
        </div>
      </Modal>

      {/* ── Bulk Delete Modal ── */}
      <Modal
        open={bulkDeleteOpen}
        onClose={() => !bulkBusy && setBulkDeleteOpen(false)}
        title={t('delete_modal_title')}
        width="520px"
      >
        <div className="space-y-4">
          <div className="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/5 p-3">
            <AlertTriangle size={16} className="mt-0.5 flex-shrink-0 text-danger" aria-hidden="true" />
            <div className="text-sm text-ink leading-relaxed">
              {t('delete_warning')}
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
              {t('loading_impact')}
            </div>
          ) : (
            <>
              <div className="grid grid-cols-2 gap-2 text-sm">
                <ImpactRow label={t('impact_keywords')} value={bulkDeleteImpact.keywords} primary />
                <ImpactRow label={t('impact_alias_vectors')} value={bulkDeleteImpact.aliasVectors} />
              </div>
              <div>
                <label htmlFor="bulk-del-confirm" className="mb-1.5 block text-sm font-medium text-ink">
                  {t('delete_confirm_label')}
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
            <AnimatedButton variant="ghost" size="md" onClick={() => setBulkDeleteOpen(false)} disabled={bulkBusy}>{t('cancel')}</AnimatedButton>
            <button
              type="button"
              onClick={submitBulkDelete}
              disabled={bulkBusy || bulkDeleteImpact === null || bulkDeleteConfirm.trim() !== 'DELETE'}
              className="inline-flex h-9 items-center gap-1.5 rounded-md border border-danger bg-danger px-3 text-sm font-medium text-white outline-none hover:bg-danger/90 disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-2 focus-visible:ring-danger/40"
            >
              {bulkBusy ? t('deleting') : t('delete_submit', { count: bulkDeleteTarget?.ids.length || 0 })}
            </button>
          </div>
        </div>
      </Modal>

      {/* ── CSV/JSON Bulk Create Modal ── */}
      <Modal
        open={csvOpen}
        onClose={() => !bulkBusy && setCsvOpen(false)}
        title={t('csv_modal_title')}
        width="720px"
      >
        <div className="space-y-4">
          <div className="rounded-md border border-border-light bg-canvas-alt p-3 text-xs leading-relaxed text-ink-muted">
            <div className="mb-1 font-medium text-ink">{t('csv_format_header')}</div>
            <code className="block font-mono text-[11px] text-ink">keyword,od,property,isColumnName,aliases</code>
            <div className="mt-1 text-ink-ghost">{t('csv_aliases_hint')}<code className="font-mono text-[11px] text-ink">alias1|alias2</code></div>
            <div className="mt-1.5">{t('csv_json_hint')}<code className="font-mono text-[11px] text-ink">[{`{"keyword":"Order","propertyId":"<uuid>","isColumnName":false}`}]</code></div>
            <div className="mt-1.5">{t('csv_required_hint')}</div>
          </div>
          <div>
            <label htmlFor="csv-text" className="mb-1.5 block text-sm font-medium text-ink">{t('csv_data_label')}</label>
            <textarea
              id="csv-text"
              value={csvText}
              onChange={e => tryParseCsv(e.target.value)}
              rows={8}
              spellCheck={false}
              placeholder="keyword,od,property,isColumnName,aliases&#10;Order,Order,Quantity,false,order|orders"
              className="w-full rounded-md border border-border bg-white px-3 py-2 font-mono text-xs text-ink outline-none placeholder:text-ink-ghost focus:border-ink focus:ring-1 focus:ring-ink/10 resize-none"
            />
            {csvParseError && (
              <div className="mt-1.5 text-xs text-danger">{csvParseError}</div>
            )}
          </div>
          {csvParsed.length > 0 && !csvResult && (
            <div className="rounded-md border border-border-light bg-canvas-alt">
              <div className="border-b border-border-light px-3 py-1.5 text-xs font-medium text-ink">
                {t('csv_preview', { count: csvParsed.length })}
              </div>
              <div className="max-h-48 overflow-y-auto">
                <table className="w-full text-left text-xs">
                  <thead className="bg-canvas-alt text-ink-muted">
                    <tr>
                      <th className="px-3 py-1.5 font-medium">keyword</th>
                      <th className="px-3 py-1.5 font-medium">propertyId</th>
                      <th className="px-3 py-1.5 font-medium">isColumnName</th>
                      <th className="px-3 py-1.5 font-medium">aliases</th>
                    </tr>
                  </thead>
                  <tbody>
                    {csvParsed.slice(0, 50).map((r, i) => (
                      <tr key={i} className="border-t border-border-light">
                        <td className="px-3 py-1 font-mono text-ink">{r.keyword}</td>
                        <td className="px-3 py-1 font-mono text-[10px] text-ink-muted truncate max-w-[160px]">{r.propertyId || '—'}</td>
                        <td className="px-3 py-1 text-ink-muted">{r.isColumnName === undefined ? '—' : String(r.isColumnName)}</td>
                        <td className="px-3 py-1 text-ink-muted">{r.aliases?.join(' | ') || '—'}</td>
                      </tr>
                    ))}
                    {csvParsed.length > 50 && (
                      <tr><td colSpan={4} className="px-3 py-1 text-center text-[11px] text-ink-ghost">{t('csv_more_rows', { count: csvParsed.length - 50 })}</td></tr>
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
                <span className="text-ink-muted">{t('csv_result_ok')}</span>
                <span className="h-3.5 w-px bg-border" />
                <span className="font-mono tabular-nums text-danger">✗ {csvResult.errors.length}</span>
                <span className="text-ink-muted">{t('csv_result_fail')}</span>
                <span className="h-3.5 w-px bg-border" />
                <span className="font-mono tabular-nums text-ink-ghost">/ {csvResult.total}</span>
                <span className="text-ink-ghost">{t('csv_result_total')}</span>
              </div>
              {csvResult.errors.length > 0 && (
                <div className="rounded-md border border-danger/30 bg-danger/5">
                  <div className="border-b border-danger/30 px-3 py-1.5 text-xs font-medium text-danger">{t('csv_error_detail')}</div>
                  <ul className="max-h-40 overflow-y-auto divide-y divide-danger/20 text-xs">
                    {csvResult.errors.map(e => (
                      <li key={e.index} className="px-3 py-1.5">
                        <span className="font-mono text-ink-muted">#{e.index + 1}</span>{' '}
                        {e.keyword && <span className="font-medium text-ink">{e.keyword}</span>}{' '}
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
              {csvResult ? t('close') : t('cancel')}
            </AnimatedButton>
            {!csvResult && (
              <AnimatedButton
                variant="primary"
                size="md"
                onClick={submitBulkCreate}
                disabled={bulkBusy || csvParsed.length === 0 || !!csvParseError}
              >
                {bulkBusy ? t('creating') : t('csv_create_submit', { count: csvParsed.length })}
              </AnimatedButton>
            )}
          </div>
        </div>
      </Modal>
    </div>
  )
}

// ────────────────────────────────────────────────────────────────
// Sub-components
// ────────────────────────────────────────────────────────────────

function SegmentedControl<T extends string>({
  value, onChange, options, ariaLabel,
}: {
  value: T
  onChange: (v: T) => void
  options: [T, string][]
  ariaLabel: string
}) {
  const industrial = useStyleMode().mode === 'industrial'
  return (
    <div
      className={`flex h-8 items-center overflow-hidden bg-white ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}
      role="radiogroup"
      aria-label={ariaLabel}
    >
      {options.map(([v, label], i) => (
        <button
          key={v}
          onClick={() => onChange(v)}
          role="radio"
          aria-checked={value === v}
          className={`h-full px-2.5 transition-colors outline-none focus-visible:ring-1 focus-visible:ring-ink ${
            industrial ? 'font-mono text-[10px] uppercase tracking-[0.14em]' : 'text-[11px] font-medium tracking-wide'
          } ${value === v ? 'bg-ink text-white' : 'text-ink-muted hover:text-ink'} ${
            i > 0 ? (industrial ? 'border-l border-ink' : 'border-l border-border') : ''
          }`}
        >
          {label}
        </button>
      ))}
    </div>
  )
}

/**
 * Segmented filter with motion sliding background (used inside modals).
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

function RefreshButton({ onClick, ...rest }: { onClick: () => void } & Omit<HTMLMotionProps<'button'>, 'onClick'>) {
  const [spinKey, setSpinKey] = useState(0)
  const reduce = useReducedMotion()
  const industrial = useStyleMode().mode === 'industrial'
  return (
    <motion.button
      onClick={() => { setSpinKey(k => k + 1); onClick() }}
      whileHover={reduce ? undefined : { scale: 1.05 }}
      whileTap={reduce ? undefined : { scale: 0.95 }}
      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
      className={`inline-flex h-7 w-7 items-center justify-center bg-white text-ink-muted hover:border-ink hover:text-ink outline-none focus-visible:ring-1 focus-visible:ring-ink ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}
      {...rest}
    >
      <motion.span
        key={spinKey}
        animate={reduce ? undefined : { rotate: 360 }}
        transition={{ duration: 0.5, ease: 'easeOut' }}
        className="inline-flex"
      >
        <RefreshCw size={12} aria-hidden="true" />
      </motion.span>
    </motion.button>
  )
}

function InlineLoader({ text }: { text: string }) {
  const reduce = useReducedMotion()
  return (
    <div className="flex items-center gap-2 text-[11px] text-ink-ghost">
      <motion.span
        animate={reduce ? undefined : { rotate: 360 }}
        transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
        className="inline-flex"
      >
        <RefreshCw size={10} aria-hidden="true" />
      </motion.span>
      <span>{text}</span>
    </div>
  )
}

function EmptyState({ primary, secondary }: { primary: string; secondary?: string }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
      <div className="text-sm text-ink-muted">{primary}</div>
      {secondary && <div className="text-xs text-ink-ghost">{secondary}</div>}
    </div>
  )
}

// ── KeywordTag (grouped view) ──
function KeywordTag({ kw, onToggle, onDelete }: {
  kw: LakehouseKeyword
  onToggle: (id: string, current: boolean) => void
  onDelete: (id: string) => void
}) {
  const reduce = useReducedMotion()
  const t = useTranslations('keyword')
  const isCol = kw.isColumnName
  const aliases = kw.aliases || []
  return (
    <motion.span
      whileHover={reduce ? undefined : { y: -1 }}
      transition={{ duration: 0.1 }}
      className={`group inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] ${
        isCol
          ? 'bg-ink text-white'
          : 'border border-border bg-white text-ink-muted'
      }`}
    >
      {kw.isMachineCode && (
        <span
          className={`inline-block h-1.5 w-1.5 rounded-full ${isCol ? 'bg-white/70' : 'bg-ink-ghost'}`}
          title="Machine Code"
          aria-label="Machine Code"
        />
      )}
      <span className="max-w-[200px] truncate font-medium">{kw.keyword}</span>
      {aliases.length > 0 && (
        <>
          <span className={isCol ? 'text-white/50' : 'text-ink-ghost'}>→</span>
          {aliases.map(a => (
            <span
              key={a}
              title={t('alias_equiv_title', { keyword: kw.keyword })}
              className={`rounded-[3px] px-1 leading-none ${
                isCol ? 'bg-white/15 text-white' : 'border border-border-light bg-canvas-alt text-ink-muted'
              }`}
            >
              {a}
            </span>
          ))}
        </>
      )}
      <motion.button
        onClick={() => onToggle(kw.id, kw.isColumnName)}
        whileHover={reduce ? undefined : { scale: 1.1 }}
        whileTap={reduce ? undefined : { scale: 0.9 }}
        aria-label={isCol ? t('unset_col') : t('set_col')}
        title={isCol ? t('unset_col') : t('set_col')}
        className={`opacity-0 transition-opacity group-hover:opacity-100 ${
          isCol ? 'text-white/70 hover:text-white' : 'text-ink-ghost hover:text-ink'
        }`}
      >
        <ArrowUpDown size={10} aria-hidden="true" />
      </motion.button>
      <motion.button
        onClick={() => onDelete(kw.id)}
        whileHover={reduce ? undefined : { scale: 1.1 }}
        whileTap={reduce ? undefined : { scale: 0.9 }}
        aria-label={t('delete_keyword_aria')}
        title={t('delete')}
        className={`opacity-0 transition-opacity group-hover:opacity-100 ${
          isCol ? 'text-white/70 hover:text-white' : 'text-ink-ghost hover:text-danger'
        }`}
      >
        <Trash2 size={10} aria-hidden="true" />
      </motion.button>
    </motion.span>
  )
}
