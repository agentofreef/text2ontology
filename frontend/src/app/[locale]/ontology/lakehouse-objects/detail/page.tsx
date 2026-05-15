'use client'

import { useState, useEffect, useCallback, useMemo } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { useSearchParams } from 'next/navigation'
import { Button } from '@/components/ui/Button'
import { Input, Textarea } from '@/components/ui/Input'
import { Modal } from '@/components/ui/Modal'
import { SQLEditor } from '@/components/ui/SQLEditor'
import { useProject } from '@/lib/project'
import { useMessage } from '@/lib/message'
import { api } from '@/lib/api'
import type { OntObjectType, OntProperty, ImportProgressResponse, PbitTablePreview } from '@/types/api'
import { ArrowLeft, Plus, Trash2, Pencil, Play, CheckCircle, Save, ChevronDown, ChevronRight, Search, RefreshCw } from 'lucide-react'

interface ColumnMeta {
  name: string
  dataType: string
}

interface ValidateResult {
  valid: boolean
  error?: string
  query: string
  columns?: string[]
  columnMeta?: ColumnMeta[]
  sampleRows?: Record<string, unknown>[]
  rowCount?: number
}

// Normalize identifier input: collapse all whitespace to underscore and uppercase.
// Applies to object NAME / DISPLAY NAME and property NAME / DISPLAY NAME fields.
// Non-Latin chars (Chinese, etc.) are left untouched by toUpperCase().
const normalizeIdent = (s: string) => s.replace(/\s+/g, '_').toUpperCase()

export default function LakehouseObjectDetailPage() {
  const t = useTranslations('objects.detail')
  const searchParams = useSearchParams()
  const router = useRouter()
  const { currentProject } = useProject()
  const msg = useMessage()
  const objectId = searchParams.get('id') || ''

  const [obj, setObj] = useState<OntObjectType | null>(null)
  const [loading, setLoading] = useState(true)

  // Form fields
  const [name, setName] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [kind, setKind] = useState('entity')
  const [description, setDescription] = useState('')
  const [semanticSql, setSemanticSql] = useState('')

  // Table browser
  const [lakehouseTables, setLakehouseTables] = useState<PbitTablePreview[]>([])
  const [tableSearch, setTableSearch] = useState('')
  const [expandedTables, setExpandedTables] = useState<Set<string>>(new Set())
  const [sqlExpanded, setSqlExpanded] = useState(false)

  // Property modal
  const [propOpen, setPropOpen] = useState(false)
  const [propEditOpen, setPropEditOpen] = useState(false)
  const [propEditTarget, setPropEditTarget] = useState<OntProperty | null>(null)
  const [propForm, setPropForm] = useState({ name: '', displayName: '', dataType: 'text', sourceColumn: '', description: '', shortDescription: '' })

  // Validate
  const [validating, setValidating] = useState(false)
  const [validateResult, setValidateResult] = useState<ValidateResult | null>(null)
  const [solidifying, setSolidifying] = useState(false)
  const [sqlOutputCols, setSqlOutputCols] = useState<string[]>([]) // columns from last successful validate
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<ValidateResult | null>(null)

  // Lakehouse schema name for SQL insertion
  const lakehouseSchema = useMemo(() => {
    if (!currentProject) return ''
    const proj = currentProject as unknown as Record<string, unknown>
    return (proj.lakehouseSchema as string) || ''
  }, [currentProject])

  // Build CodeMirror autocomplete schema from lakehouse tables
  const editorSchema = useMemo(() => {
    const schema: Record<string, string[]> = {}
    for (const t of lakehouseTables) {
      const key = lakehouseSchema ? `${lakehouseSchema}."${t.name}"` : `"${t.name}"`
      schema[key] = (t.columns || []).map(c => c.name || (c as Record<string, string>)['name'] || '')
    }
    return schema
  }, [lakehouseTables, lakehouseSchema])

  // Od aliases (ont_alias rows where target_kind='object_type' and target_id=this Od)
  // Recall path: BuildLakehouseContext -> fallbackDirectOd matches token vs alias_text.
  // mark=true is required for recall to pick them up.
  interface OdAlias { id: string; aliasText: string; mark: boolean }
  const [odAliases, setOdAliases] = useState<OdAlias[]>([])
  const [newOdAlias, setNewOdAlias] = useState('')

  const [initialLoad, setInitialLoad] = useState(true)
  const fetchObject = useCallback(async () => {
    if (!currentProject || !objectId) return
    if (initialLoad) setLoading(true)
    try {
      const found = await api<OntObjectType>(`/ontology/objects/${objectId}?projectId=${currentProject.id}`)
      if (found) {
        setObj(found)
        setName(found.name)
        setDisplayName(found.displayName || '')
        setKind(found.kind)
        setDescription(found.description || '')
        setSemanticSql(found.semanticSql || '')
        if (found.semanticSql) setSqlExpanded(true)
      }
    } catch {
      msg.error('Failed to load object')
    } finally {
      setLoading(false)
      setInitialLoad(false)
    }
  }, [currentProject, objectId, initialLoad])

  // Load lakehouse tables for browser + autocomplete
  const fetchTables = useCallback(async () => {
    if (!currentProject) return
    try {
      const progress = await api<ImportProgressResponse>(`/connector/pbit/progress?projectId=${currentProject.id}`)
      if (progress.pbitConfig?.tables) {
        setLakehouseTables(progress.pbitConfig.tables)
      }
    } catch { /* ignore */ }
  }, [currentProject])

  // Load Od aliases for this Od (filter ont_alias by target_kind + target_id).
  const fetchOdAliases = useCallback(async () => {
    if (!currentProject || !objectId) return
    try {
      const res = await api<{ data: Array<{ id: string; aliasText: string; targetId: string | null; targetKind: string; mark: boolean }> }>(
        `/ontology/aliases?projectId=${currentProject.id}&objectTypeId=${objectId}`,
      )
      const mine = (res.data || []).filter(a => a.targetKind === 'object_type' && a.targetId === objectId)
      setOdAliases(mine.map(a => ({ id: a.id, aliasText: a.aliasText, mark: a.mark })))
    } catch { /* ignore */ }
  }, [currentProject, objectId])

  const addOdAlias = async () => {
    const text = newOdAlias.trim()
    if (!text || !currentProject || !objectId) return
    if (odAliases.some(a => a.aliasText.toLowerCase() === text.toLowerCase())) {
      msg.error(t('alias_exists')); return
    }
    try {
      await api(`/ontology/aliases?projectId=${currentProject.id}`, {
        method: 'POST',
        body: {
          aliasText: text,
          aliasType: 'business',
          targetId: objectId,
          targetKind: 'object_type',
          isExactMatch: true,
          mark: true,
        },
      })
      setNewOdAlias('')
      msg.success(t('alias_added'))
      fetchOdAliases()
    } catch (e) { msg.error(e instanceof Error ? e.message : t('alias_add_failed')) }
  }

  const deleteOdAlias = async (id: string) => {
    try {
      await api(`/ontology/aliases/${id}?projectId=${currentProject?.id}`, { method: 'DELETE' })
      fetchOdAliases()
    } catch { msg.error(t('alias_delete_failed')) }
  }

  const toggleOdAliasMark = async (a: OdAlias) => {
    try {
      await api(`/ontology/aliases/${a.id}/mark?projectId=${currentProject?.id}`, {
        method: 'POST', body: { mark: !a.mark },
      })
      fetchOdAliases()
    } catch { msg.error(t('alias_toggle_failed')) }
  }

  useEffect(() => { fetchObject(); fetchTables() }, [fetchObject, fetchTables])
  useEffect(() => { fetchOdAliases() }, [fetchOdAliases])

  const handleSave = async () => {
    if (!obj) return
    const normName = normalizeIdent(name)
    const normDisplay = normalizeIdent(displayName)
    // Reflect the normalized values in the UI so users see what actually gets saved.
    if (normName !== name) setName(normName)
    if (normDisplay !== displayName) setDisplayName(normDisplay)
    try {
      await api(`/ontology/objects/${obj.id}?projectId=${currentProject?.id}`, {
        method: 'PUT',
        body: { name: normName, displayName: normDisplay, kind, description, sourceTable: obj.sourceTable, note: obj.note, semanticSql },
      })
      msg.success('Saved')
      fetchObject()
    } catch (e) { msg.error(e instanceof Error ? e.message : 'Save failed') }
  }

  const handleValidate = async () => {
    await handleSave()
    setValidating(true)
    setValidateResult(null)
    try {
      const result = await api<ValidateResult>('/connector/pbit/validate-sql', {
        method: 'POST', body: { objectId, projectId: currentProject?.id },
      })
      setValidateResult(result)
      if (result.valid) {
        msg.success(t('validate_success', { rows: result.rowCount ?? 0, cols: result.columns?.length ?? 0 }))
        if (result.columns) setSqlOutputCols(result.columns)
      } else {
        msg.error(t('validate_failed', { error: result.error ?? '' }))
      }
    } catch (e) { msg.error(e instanceof Error ? e.message : 'Validate failed') }
    finally { setValidating(false) }
  }

  const handleSolidify = async () => {
    setSolidifying(true)
    try {
      await api('/connector/pbit/solidify-sql', { method: 'POST', body: { objectId, projectId: currentProject?.id } })
      msg.success(t('solidify_success'))
      fetchObject()
    } catch (e) { msg.error(e instanceof Error ? e.message : 'Solidify failed') }
    finally { setSolidifying(false) }
  }

  // Insert text into SQL editor at the end
  const insertSql = (text: string) => {
    setSemanticSql(prev => prev ? prev + ' ' + text : text)
    if (!sqlExpanded) setSqlExpanded(true)
  }

  // Property CRUD
  const openAddProp = () => { setPropForm({ name: '', displayName: '', dataType: 'text', sourceColumn: '', description: '', shortDescription: '' }); setPropOpen(true) }
  const openEditProp = (p: OntProperty) => { setPropEditTarget(p); setPropForm({ name: p.name, displayName: p.displayName || '', dataType: p.dataType || 'text', sourceColumn: p.sourceColumn || '', description: p.description || '', shortDescription: p.shortDescription || '' }); setPropEditOpen(true) }

  const saveProp = async () => {
    const body = {
      ...propForm,
      name: normalizeIdent(propForm.name),
      displayName: normalizeIdent(propForm.displayName),
      isFilterable: true,
      isGroupable: true,
    }
    try {
      await api(`/ontology/objects/${objectId}/properties?projectId=${currentProject?.id}`, { method: 'POST', body })
      msg.success('Property added'); setPropOpen(false); fetchObject()
    } catch (e) { msg.error(e instanceof Error ? e.message : 'Failed') }
  }

  const updateProp = async () => {
    if (!propEditTarget) return
    const body = {
      ...propForm,
      name: normalizeIdent(propForm.name),
      displayName: normalizeIdent(propForm.displayName),
      isFilterable: true,
      isGroupable: true,
    }
    try {
      await api(`/ontology/properties/${propEditTarget.id}?projectId=${currentProject?.id}`, { method: 'PUT', body })
      msg.success('Property updated'); setPropEditOpen(false); fetchObject()
    } catch (e) { msg.error(e instanceof Error ? e.message : 'Failed') }
  }

  // Inline update source_column for a property — also infer data_type from columnMeta
  const updatePropSourceCol = async (propId: string, sourceColumn: string) => {
    const prop = props.find(p => p.id === propId)
    if (!prop) return
    // Infer data_type from validate result columnMeta
    let dataType = prop.dataType || 'text'
    if (validateResult?.columnMeta) {
      const meta = validateResult.columnMeta.find(c => c.name === sourceColumn)
      if (meta) dataType = meta.dataType
    }
    try {
      await api(`/ontology/properties/${propId}?projectId=${currentProject?.id}`, {
        method: 'PUT',
        body: { name: prop.name, displayName: prop.displayName || '', dataType, sourceColumn, description: prop.description || '', shortDescription: prop.shortDescription || '', isFilterable: true, isGroupable: true },
      })
      fetchObject()
    } catch { /* silent */ }
  }

  const handleTestCanonical = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const result = await api<ValidateResult>('/connector/pbit/test-canonical', {
        method: 'POST', body: { objectId, projectId: currentProject?.id },
      })
      setTestResult(result)
      if (result.valid) msg.success(`TEST PASSED — ${result.rowCount} rows`)
      else msg.error(`TEST FAILED: ${result.error}`)
    } catch (e) { msg.error(e instanceof Error ? e.message : 'Test failed') }
    finally { setTesting(false) }
  }

  // Import a SQL output column as a property (auto-create with source_column + inferred type).
  // SQL column names may contain spaces/mixed case; normalize name + displayName but keep
  // sourceColumn verbatim so the mapping against the raw SQL output stays intact.
  const importColAsProp = async (colMeta: ColumnMeta) => {
    const identName = normalizeIdent(colMeta.name)
    try {
      await api(`/ontology/objects/${objectId}/properties?projectId=${currentProject?.id}`, {
        method: 'POST',
        body: { name: identName, displayName: identName, dataType: colMeta.dataType || 'text', sourceColumn: colMeta.name, description: '', isFilterable: true, isGroupable: true },
      })
      msg.success(`Imported: ${identName}`)
      fetchObject()
    } catch (e) { msg.error(e instanceof Error ? e.message : 'Failed') }
  }

  // Batch import all unlinked columns as properties
  const importAllCols = async () => {
    if (!validateResult?.columnMeta) return
    const existingNames = new Set((obj?.properties || []).map(p => p.sourceColumn?.toLowerCase() || p.name.toLowerCase()))
    const toImport = validateResult.columnMeta.filter(c => !existingNames.has(c.name.toLowerCase()))
    if (toImport.length === 0) { msg.success('All columns already imported'); return }
    for (const col of toImport) {
      await importColAsProp(col)
    }
    msg.success(`Imported ${toImport.length} columns`)
  }

  const deleteProp = async (propId: string) => {
    if (!confirm(t('confirm_delete_prop'))) return
    await api(`/ontology/properties/${propId}?projectId=${currentProject?.id}`, { method: 'DELETE' })
    fetchObject()
  }

  // Toggle machine code flag
  const toggleMC = async (prop: OntProperty) => {
    try {
      await api(`/ontology/properties/${prop.id}?projectId=${currentProject?.id}`, {
        method: 'PUT',
        body: { name: prop.name, displayName: prop.displayName || '', dataType: prop.dataType || 'text', sourceColumn: prop.sourceColumn || '', description: prop.description || '', shortDescription: prop.shortDescription || '', isFilterable: true, isGroupable: true, isMachineCode: !prop.isMachineCode },
      })
      fetchObject()
    } catch { /* silent */ }
  }

  // Sync property keywords
  const [syncingProp, setSyncingProp] = useState<string | null>(null)
  const syncKeywords = async (propId: string, force = false) => {
    setSyncingProp(propId)
    try {
      const res = await api<{ success?: boolean; keywordCount?: number; error?: string; needsConfirmation?: boolean; distinctCount?: number; propertyName?: string }>('/connector/pbit/sync-property-keywords', {
        method: 'POST', body: { propertyId: propId, projectId: currentProject?.id, force },
      })
      if (res.needsConfirmation) {
        setSyncingProp(null)
        if (confirm(t('confirm_sync_keywords', { name: res.propertyName ?? '', count: res.distinctCount ?? 0 }))) {
          syncKeywords(propId, true)
        }
        return
      }
      if (res.success) {
        msg.success(`Synced ${res.keywordCount} keywords`)
        fetchObject()
      } else {
        msg.error(res.error || 'Sync failed')
      }
    } catch (e) { msg.error(e instanceof Error ? e.message : 'Sync failed') }
    finally { setSyncingProp(null) }
  }

  // Column aliases for edit modal
  const [propAliases, setPropAliases] = useState<{ id: string; keyword: string }[]>([])
  const [newAlias, setNewAlias] = useState('')

  // Column aliases live in one of two places on lakehouse_keyword:
  //   (a) canonical row's aliases[] array — the post-triage model (one row per
  //       property, synonyms in aliases[]);
  //   (b) legacy standalone rows where keyword=<alias> (pre-canonical imports,
  //       or rows added via the old POST /lakehouse-keywords endpoint).
  // The loader flattens both into a single list so the dialog shows every
  // user-visible synonym regardless of how it was persisted. Canonical ids
  // are prefixed with "alias::<rowId>::" so deleteAlias can dispatch to the
  // right removal path. The canonical keyword itself (= property.name) is
  // never rendered as an "alias" — that would be tautological.
  type AliasItem = { id: string; keyword: string }
  const loadAliases = useCallback(async (propId: string, propName?: string) => {
    if (!currentProject) return
    try {
      const res = await api<{ data: { id: string; keyword: string; aliases?: string[] }[] }>(
        `/connector/pbit/lakehouse-keywords?projectId=${currentProject.id}&propertyId=${propId}&type=column`
      )
      const canonical = (propName ?? propEditTarget?.name ?? '').toLowerCase()
      const items: AliasItem[] = []
      for (const row of res.data || []) {
        if (row.keyword && row.keyword.toLowerCase() !== canonical) {
          items.push({ id: `row::${row.id}`, keyword: row.keyword })
        }
        for (const a of row.aliases || []) {
          items.push({ id: `alias::${row.id}::${a}`, keyword: a })
        }
      }
      setPropAliases(items)
    } catch { setPropAliases([]) }
  }, [currentProject, propEditTarget])

  const addAlias = async () => {
    if (!newAlias.trim() || !propEditTarget || !currentProject) return
    await api('/connector/pbit/lakehouse-keywords/column-alias', {
      method: 'POST',
      body: { projectId: currentProject.id, propertyId: propEditTarget.id, alias: newAlias.trim() },
    })
    setNewAlias('')
    loadAliases(propEditTarget.id)
  }

  const deleteAlias = async (compoundId: string) => {
    if (!currentProject || !propEditTarget) return
    if (compoundId.startsWith('row::')) {
      const id = compoundId.slice('row::'.length)
      await api(`/connector/pbit/lakehouse-keywords?id=${id}&projectId=${currentProject.id}`, { method: 'DELETE' })
    } else if (compoundId.startsWith('alias::')) {
      const rest = compoundId.slice('alias::'.length)
      const sep = rest.indexOf('::')
      const alias = sep >= 0 ? rest.slice(sep + 2) : rest
      const qs = new URLSearchParams({
        projectId: currentProject.id,
        propertyId: propEditTarget.id,
        alias,
      })
      await api(`/connector/pbit/lakehouse-keywords/column-alias?${qs.toString()}`, { method: 'DELETE' })
    }
    loadAliases(propEditTarget.id)
  }

  const openEditPropWithAliases = (p: OntProperty) => {
    openEditProp(p)
    loadAliases(p.id, p.name)
    setNewAlias('')
  }

  // Filtered tables for browser
  const filteredTables = useMemo(() => {
    if (!tableSearch) return lakehouseTables
    const q = tableSearch.toLowerCase()
    return lakehouseTables.filter(t =>
      t.name.toLowerCase().includes(q) ||
      (t.columns || []).some(c => (c.name || '').toLowerCase().includes(q))
    )
  }, [lakehouseTables, tableSearch])

  // Column mapping status (after validation)
  const columnMapping = useMemo(() => {
    if (!validateResult?.valid || !validateResult.columns || !obj) return null
    const sqlCols = new Set(validateResult.columns.map(c => c.toLowerCase()))
    const props = obj.properties || []
    const propCols = new Set(props.map(p => (p.sourceColumn || '').toLowerCase()).filter(Boolean))

    return {
      matched: props.filter(p => sqlCols.has((p.sourceColumn || '').toLowerCase())),
      missing: props.filter(p => p.sourceColumn && !sqlCols.has(p.sourceColumn.toLowerCase())),
      unmapped: validateResult.columns.filter(c => !propCols.has(c.toLowerCase())),
    }
  }, [validateResult, obj])

  if (loading) return <div className="p-8 font-mono text-xs text-ink-muted animate-pulse">LOADING...</div>
  if (!obj) return <div className="p-8 font-mono text-sm text-ink-muted">Object not found.</div>

  const props = obj.properties || []

  return (
    <div className="flex h-full flex-col">
      {/* Top bar */}
      <div className="flex items-center justify-between border-b border-border px-6 py-3">
        <div className="flex items-center gap-3">
          <Button variant="ghost" size="sm" onClick={() => router.push('/ontology/lakehouse-objects')}>
            <ArrowLeft size={14} />
          </Button>
          <h1 className="font-display text-base font-bold">{obj.name}</h1>
          {obj.displayName && obj.displayName !== obj.name && (
            <span className="font-mono text-[10px] text-ink-ghost">{obj.displayName}</span>
          )}
          <span className={`inline-block h-2.5 w-2.5 ${obj.validatedAt ? 'bg-emerald-500' : obj.semanticSql ? 'bg-amber-400' : 'bg-border'}`} />
        </div>
        <Button variant="primary" size="sm" onClick={handleSave}>
          <Save size={14} /> SAVE
        </Button>
      </div>

      {/* Main content: left-right split */}
      <div className="flex flex-1 overflow-hidden">
        {/* LEFT PANEL: Object + Properties */}
        <div className="w-1/2 border-r border-border overflow-y-auto p-5 space-y-4">
          {/* Object Definition */}
          <div className="border border-border">
            <div className="border-b border-border px-3 py-1.5 bg-canvas-alt">
              <span className="font-mono text-[9px] font-semibold tracking-wider text-ink-ghost">OBJECT DEFINITION</span>
            </div>
            <div className="p-3 space-y-2">
              <div className="grid grid-cols-2 gap-3">
                <Input label="NAME" value={name} onChange={(e) => setName(normalizeIdent(e.target.value))} />
                <Input label="DISPLAY NAME" value={displayName} onChange={(e) => setDisplayName(normalizeIdent(e.target.value))} />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="mb-1 block font-mono text-[9px] font-semibold tracking-wider text-ink-muted">KIND</label>
                  <select value={kind} onChange={(e) => setKind(e.target.value)} className="w-full border border-border bg-canvas px-2 py-1.5 font-mono text-xs text-ink">
                    <option value="entity">entity</option><option value="event">event</option><option value="attribute">attribute</option>
                  </select>
                </div>
                <div />
              </div>
              <Textarea label="DESCRIPTION" value={description} onChange={(e) => setDescription(e.target.value)} rows={2} />
            </div>
          </div>

          {/* Od Aliases — recall path: ont_alias(target_kind='object_type') → fallbackDirectOd */}
          <div className="border border-border">
            <div className="border-b border-border px-3 py-1.5 bg-canvas-alt flex items-center justify-between">
              <span className="font-mono text-[9px] font-semibold tracking-wider text-ink-ghost">
                OD ALIASES ({odAliases.length})
                <span className="ml-2 font-normal text-ink-ghost normal-case">
                  {t('alias_section_hint')}
                </span>
              </span>
            </div>
            <div className="p-3 space-y-2">
              <div className="flex flex-wrap gap-1.5">
                {odAliases.map(a => (
                  <span
                    key={a.id}
                    className={`inline-flex items-center gap-1 border px-1.5 py-0.5 font-mono text-[11px] ${
                      a.mark
                        ? 'border-emerald-400 bg-emerald-50 text-emerald-700'
                        : 'border-amber-400 bg-amber-50 text-amber-700'
                    }`}
                    title={a.mark ? t('alias_active_title') : t('alias_inactive_title')}
                  >
                    <button
                      onClick={() => toggleOdAliasMark(a)}
                      className="hover:underline"
                    >
                      {a.aliasText}
                    </button>
                    {!a.mark && <span className="text-[9px] font-bold">·OFF</span>}
                    <button onClick={() => deleteOdAlias(a.id)} className="text-ink-ghost hover:text-red-600">
                      <Trash2 size={10} />
                    </button>
                  </span>
                ))}
                {odAliases.length === 0 && (
                  <span className="font-mono text-[10px] text-ink-ghost">
                    {t('alias_empty')}
                  </span>
                )}
              </div>
              <div className="flex items-center gap-1.5">
                <input
                  value={newOdAlias}
                  onChange={e => setNewOdAlias(e.target.value)}
                  onKeyDown={e => e.key === 'Enter' && (e.preventDefault(), addOdAlias())}
                  placeholder={t('alias_placeholder')}
                  className="flex-1 border border-border px-2 py-1.5 font-mono text-xs text-ink outline-none placeholder:text-ink-ghost"
                />
                <Button variant="ghost" size="sm" onClick={addOdAlias} disabled={!newOdAlias.trim()}>
                  <Plus size={11} /> {t('alias_add_btn')}
                </Button>
              </div>
            </div>
          </div>

          {/* Properties */}
          <div className="border border-border">
            <div className="border-b border-border px-3 py-1.5 bg-canvas-alt flex items-center justify-between">
              <span className="font-mono text-[9px] font-semibold tracking-wider text-ink-ghost">
                PROPERTIES ({props.length})
                {props.length > 0 && (
                  <span className={`ml-2 ${props.every(p => p.sourceColumn) ? 'text-emerald-600' : 'text-amber-600'}`}>
                    {props.filter(p => p.sourceColumn).length}/{props.length} mapped
                  </span>
                )}
              </span>
              <Button variant="ghost" size="sm" onClick={openAddProp}><Plus size={11} /> ADD</Button>
            </div>
            {props.length > 0 ? (
              <div className="max-h-[400px] overflow-y-auto">
                <div className="flex border-b border-border px-3 py-1 font-mono text-[8px] font-semibold tracking-wider text-ink-ghost bg-canvas-alt/50">
                  <div className="w-5" />
                  <div className="w-[110px]">NAME</div>
                  <div className="w-[50px]">TYPE</div>
                  <div className="w-[30px]">MC</div>
                  <div className="flex-1">SRC_COL</div>
                  <div className="w-[70px]" />
                </div>
                {props.map((p) => {
                  const isMapped = !!p.sourceColumn
                  return (
                    <div key={p.id} className={`border-b border-border last:border-b-0 hover:bg-canvas-alt/30 ${!isMapped ? 'bg-red-50/40' : ''}`}>
                      <div className="flex items-center px-3 py-1.5">
                        <div className="w-5 flex-shrink-0">
                          <span className={`inline-block h-2 w-2 ${isMapped ? 'bg-emerald-500' : 'bg-red-400'}`}
                            title={isMapped ? `mapped → ${p.sourceColumn}` : 'unmapped'} />
                        </div>
                        <div className={`w-[110px] font-mono text-[10px] font-bold truncate ${isMapped ? 'text-ink' : 'text-red-600'}`}>{p.name}</div>
                        <div className="w-[50px] font-mono text-[9px] text-ink-muted">{p.dataType}</div>
                        <div className="w-[30px]">
                          <button
                            onClick={() => toggleMC(p)}
                            className={`inline-block h-3 w-3 border ${p.isMachineCode ? 'bg-amber-400 border-amber-500' : 'bg-white border-border'}`}
                            title={p.isMachineCode ? 'Machine Code (click to unset)' : 'Not MC (click to set)'}
                          />
                        </div>
                        <div className="flex-1">
                          {sqlOutputCols.length > 0 ? (
                            <select
                              value={p.sourceColumn || ''}
                              onChange={(e) => updatePropSourceCol(p.id, e.target.value)}
                              className={`w-full border px-1 py-0.5 font-mono text-[9px] ${isMapped ? 'border-emerald-300 bg-emerald-50 text-emerald-800' : 'border-red-300 bg-red-50 text-red-700'}`}
                            >
                              <option value="">— unmapped —</option>
                              {sqlOutputCols.map((col) => <option key={col} value={col}>{col}</option>)}
                            </select>
                          ) : (
                            <span className={`font-mono text-[9px] truncate block ${isMapped ? 'text-emerald-700' : 'text-red-400'}`}>
                              {p.sourceColumn || '— unmapped —'}
                            </span>
                          )}
                        </div>
                        <div className="w-[70px] flex items-center justify-end gap-0.5">
                          {p.keywordsSyncedAt && (
                            <span className="font-mono text-[7px] text-emerald-600 mr-0.5" title={p.keywordsSyncedAt}>
                              {p.keywordsSyncedAt.slice(5, 16).replace('T', ' ')}
                            </span>
                          )}
                          <button
                            onClick={() => syncKeywords(p.id)}
                            disabled={!isMapped || !obj?.canonicalQuery || syncingProp === p.id}
                            className={`p-0.5 ${isMapped && obj?.canonicalQuery ? 'text-accent hover:text-accent/80' : 'text-ink-ghost/30 cursor-not-allowed'}`}
                            title={p.keywordsSyncedAt ? `Synced: ${p.keywordsSyncedAt.slice(0,10)}` : 'Sync keywords'}
                          >
                            <RefreshCw size={10} className={syncingProp === p.id ? 'animate-spin' : ''} />
                          </button>
                          <button onClick={() => openEditPropWithAliases(p)} className="text-ink-ghost hover:text-ink p-0.5"><Pencil size={10} /></button>
                          <button onClick={() => deleteProp(p.id)} className="text-ink-ghost hover:text-red-600 p-0.5"><Trash2 size={10} /></button>
                        </div>
                      </div>
                    </div>
                  )
                })}
              </div>
            ) : (
              <div className="px-3 py-4 text-center font-mono text-[10px] text-ink-ghost">No properties</div>
            )}
          </div>
        </div>

        {/* RIGHT PANEL: SQL Semantic Layer */}
        <div className="w-1/2 overflow-y-auto p-5 space-y-4">
          {/* SQL Editor Section */}
          <div className="border border-border">
            <button
              className="w-full border-b border-border px-3 py-1.5 bg-canvas-alt flex items-center justify-between cursor-pointer hover:bg-canvas-alt/80"
              onClick={() => setSqlExpanded(!sqlExpanded)}
            >
              <span className="font-mono text-[9px] font-semibold tracking-wider text-ink-ghost">SQL SEMANTIC LAYER</span>
              {sqlExpanded ? <ChevronDown size={12} className="text-ink-ghost" /> : <ChevronRight size={12} className="text-ink-ghost" />}
            </button>

            {sqlExpanded && (
              <div className="p-0">
                {/* Table browser + Editor side by side */}
                <div className="flex">
                  {/* Table Browser */}
                  <div className="w-[180px] border-r border-border bg-canvas-alt/30 flex flex-col" style={{ maxHeight: '350px' }}>
                    <div className="px-2 py-1.5 border-b border-border">
                      <div className="flex items-center gap-1 border border-border bg-white px-1.5 py-1">
                        <Search size={10} className="text-ink-ghost flex-shrink-0" />
                        <input
                          value={tableSearch}
                          onChange={(e) => setTableSearch(e.target.value)}
                          placeholder="Search..."
                          className="w-full bg-transparent font-mono text-[9px] text-ink outline-none placeholder:text-ink-ghost"
                        />
                      </div>
                    </div>
                    <div className="overflow-y-auto flex-1 py-1">
                      {filteredTables.map((tbl) => {
                        const isOpen = expandedTables.has(tbl.name)
                        return (
                          <div key={tbl.name}>
                            <button
                              className="w-full flex items-center gap-1 px-2 py-0.5 hover:bg-canvas-alt text-left group"
                              onClick={() => setExpandedTables(prev => {
                                const next = new Set(prev)
                                isOpen ? next.delete(tbl.name) : next.add(tbl.name)
                                return next
                              })}
                              onDoubleClick={() => insertSql('"' + tbl.name + '"')}
                              title={t('table_expand_title')}
                            >
                              {isOpen ? <ChevronDown size={9} className="text-ink-ghost flex-shrink-0" /> : <ChevronRight size={9} className="text-ink-ghost flex-shrink-0" />}
                              <span className="font-mono text-[9px] font-semibold text-ink truncate">{tbl.name}</span>
                              <span className="font-mono text-[8px] text-ink-ghost ml-auto">{tbl.columnCount}</span>
                            </button>
                            {isOpen && (tbl.columns || []).map((c, ci) => (
                              <button
                                key={ci}
                                className="w-full flex items-center gap-1 pl-5 pr-2 py-0.5 hover:bg-blue-50 text-left"
                                onClick={() => insertSql('"' + (c.name || (c as Record<string, string>)['name']) + '"')}
                                title={`Click to insert. Type: ${c.dataType || (c as Record<string, string>)['dataType']}`}
                              >
                                <span className="font-mono text-[9px] text-ink-muted truncate">{c.name || (c as Record<string, string>)['name']}</span>
                                <span className="font-mono text-[7px] text-ink-ghost ml-auto">{(c.dataType || (c as Record<string, string>)['dataType'] || '').slice(0, 4)}</span>
                              </button>
                            ))}
                          </div>
                        )
                      })}
                    </div>
                  </div>

                  {/* SQL CodeMirror Editor */}
                  <div className="flex-1">
                    <SQLEditor
                      value={semanticSql}
                      onChange={setSemanticSql}
                      schema={editorSchema}
                      height="300px"
                    />
                  </div>
                </div>

                {/* Action buttons */}
                <div className="flex items-center gap-2 px-3 py-2 border-t border-border bg-canvas-alt/30">
                  <Button variant="primary" size="sm" onClick={handleValidate} disabled={validating || !semanticSql.trim()}>
                    <Play size={11} /> {validating ? 'VALIDATING...' : 'VALIDATE'}
                  </Button>
                  {validateResult?.valid && (
                    <Button variant="primary" size="sm" onClick={handleSolidify} disabled={solidifying}>
                      <CheckCircle size={11} /> {solidifying ? '...' : 'SOLIDIFY'}
                    </Button>
                  )}
                  <span className="font-mono text-[8px] text-ink-ghost ml-2">
                    {t('sql_validate_note')}
                  </span>
                </div>
              </div>
            )}
          </div>

          {/* Preview / Validation Result */}
          {validateResult && (
            <div className={`border px-4 py-3 space-y-2 ${validateResult.valid ? 'border-emerald-200 bg-emerald-50' : 'border-red-200 bg-red-50'}`}>
              <div className={`font-mono text-[10px] font-semibold ${validateResult.valid ? 'text-emerald-700' : 'text-red-700'}`}>
                {validateResult.valid ? `VALID — ${validateResult.rowCount} rows // ${validateResult.columns?.length} columns` : `ERROR: ${validateResult.error}`}
              </div>

              {/* SQL output columns with import buttons */}
              {validateResult.valid && validateResult.columnMeta && (
                <div>
                  <div className="flex items-center justify-between mb-1">
                    <span className="font-mono text-[9px] font-semibold text-ink-ghost">SQL OUTPUT COLUMNS ({validateResult.columnMeta.length})</span>
                    <button onClick={importAllCols} className="font-mono text-[9px] text-accent hover:underline">
                      IMPORT ALL
                    </button>
                  </div>
                  <div className="flex flex-wrap gap-1">
                    {validateResult.columnMeta.map((col) => {
                      const existingProps = (obj?.properties || [])
                      const isImported = existingProps.some(p => p.sourceColumn === col.name || p.name === col.name)
                      return (
                        <button
                          key={col.name}
                          onClick={() => !isImported && importColAsProp(col)}
                          disabled={isImported}
                          className={`inline-flex items-center gap-1 border px-1.5 py-0.5 font-mono text-[9px] transition-colors ${
                            isImported
                              ? 'border-emerald-300 bg-emerald-50 text-emerald-700'
                              : 'border-border bg-white text-ink hover:border-accent hover:bg-accent/5 cursor-pointer'
                          }`}
                          title={`${col.dataType}${isImported ? ' (imported)' : ' — click to import'}`}
                        >
                          {isImported && <span className="text-emerald-500">+</span>}
                          {col.name}
                          <span className="text-ink-ghost">{col.dataType}</span>
                        </button>
                      )
                    })}
                  </div>
                </div>
              )}

              {/* Sample rows table */}
              {validateResult.valid && validateResult.sampleRows && validateResult.sampleRows.length > 0 && (
                <div className="max-h-48 overflow-auto border border-border bg-white">
                  <table className="w-full font-mono text-[10px]">
                    <thead>
                      <tr className="bg-canvas-alt border-b border-border">
                        {validateResult.columns?.map((col) => (
                          <th key={col} className="px-2 py-1 text-left font-semibold text-ink-ghost whitespace-nowrap">{col}</th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {validateResult.sampleRows.map((row, i) => (
                        <tr key={i} className="border-b border-border last:border-b-0">
                          {validateResult.columns?.map((col) => (
                            <td key={col} className="px-2 py-1 text-ink truncate max-w-[150px]">
                              {row[col] != null ? String(row[col]) : <span className="text-ink-ghost">NULL</span>}
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}

              <div className="font-mono text-[8px] text-ink-ghost break-all">Query: {validateResult.query}</div>
            </div>
          )}

          {/* Canonical query + test */}
          {obj.canonicalQuery && (
            <div className="border border-border">
              <div className="border-b border-border px-3 py-1.5 bg-canvas-alt flex items-center justify-between">
                <span className="font-mono text-[9px] font-semibold tracking-wider text-ink-ghost">CANONICAL QUERY</span>
                <Button variant="ghost" size="sm" onClick={handleTestCanonical} disabled={testing}>
                  <Play size={10} /> {testing ? 'TESTING...' : 'RUN TEST'}
                </Button>
              </div>
              <pre className="px-3 py-2 font-mono text-[10px] text-ink overflow-x-auto whitespace-pre-wrap bg-canvas-alt/20">
                {obj.canonicalQuery}
              </pre>
              {testResult && (
                <div className={`border-t px-3 py-2 space-y-1 ${testResult.valid ? 'bg-emerald-50' : 'bg-red-50'}`}>
                  <div className={`font-mono text-[9px] font-semibold ${testResult.valid ? 'text-emerald-700' : 'text-red-700'}`}>
                    {testResult.valid ? `PASS — ${testResult.rowCount} rows // ${testResult.columns?.length} columns` : `FAIL: ${testResult.error}`}
                  </div>
                  {testResult.valid && testResult.sampleRows && testResult.sampleRows.length > 0 && (
                    <div className="max-h-36 overflow-auto border border-border bg-white">
                      <table className="w-full font-mono text-[9px]">
                        <thead>
                          <tr className="bg-canvas-alt border-b border-border">
                            {testResult.columns?.map((col) => (
                              <th key={col} className="px-1.5 py-0.5 text-left font-semibold text-ink-ghost whitespace-nowrap">{col}</th>
                            ))}
                          </tr>
                        </thead>
                        <tbody>
                          {testResult.sampleRows.map((row, i) => (
                            <tr key={i} className="border-b border-border last:border-b-0">
                              {testResult.columns?.map((col) => (
                                <td key={col} className="px-1.5 py-0.5 text-ink truncate max-w-[120px]">
                                  {row[col] != null ? String(row[col]) : <span className="text-ink-ghost">NULL</span>}
                                </td>
                              ))}
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      {/* Property Modals — shared source column picker */}
      <Modal open={propOpen} onClose={() => setPropOpen(false)} title="ADD PROPERTY">
        <div className="space-y-3">
          <Input label="NAME" value={propForm.name} onChange={(e) => setPropForm({ ...propForm, name: normalizeIdent(e.target.value) })} />
          <Input label="DISPLAY NAME" value={propForm.displayName} onChange={(e) => setPropForm({ ...propForm, displayName: normalizeIdent(e.target.value) })} />
          <div>
            <label className="mb-1.5 block font-mono text-[10px] font-semibold tracking-wider text-ink-muted">DATA TYPE</label>
            <select value={propForm.dataType} onChange={(e) => setPropForm({ ...propForm, dataType: e.target.value })} className="w-full border border-border bg-canvas px-3 py-2 font-mono text-sm text-ink">
              <option value="text">text</option><option value="bigint">bigint</option><option value="double precision">double precision</option><option value="timestamp">timestamp</option><option value="boolean">boolean</option>
            </select>
          </div>
          <div>
            <label className="mb-1.5 block font-mono text-[10px] font-semibold tracking-wider text-ink-muted">
              SOURCE COLUMN {sqlOutputCols.length > 0 && <span className="text-ink-ghost font-normal">(from SQL output)</span>}
            </label>
            {sqlOutputCols.length > 0 ? (
              <select value={propForm.sourceColumn} onChange={(e) => setPropForm({ ...propForm, sourceColumn: e.target.value })} className="w-full border border-border bg-canvas px-3 py-2 font-mono text-sm text-ink">
                <option value="">— select column —</option>
                {sqlOutputCols.map((col) => <option key={col} value={col}>{col}</option>)}
              </select>
            ) : (
              <Input value={propForm.sourceColumn} onChange={(e) => setPropForm({ ...propForm, sourceColumn: e.target.value })} placeholder="Run VALIDATE first to see columns" />
            )}
          </div>
          <Input label="SHORT DESCRIPTION" value={propForm.shortDescription} onChange={(e) => setPropForm({ ...propForm, shortDescription: e.target.value })} placeholder={t('prop_short_desc_placeholder')} />
          <Textarea label="DESCRIPTION" value={propForm.description} onChange={(e) => setPropForm({ ...propForm, description: e.target.value })} rows={2} />
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="ghost" onClick={() => setPropOpen(false)}>CANCEL</Button>
            <Button variant="primary" onClick={saveProp}>ADD</Button>
          </div>
        </div>
      </Modal>

      <Modal open={propEditOpen} onClose={() => setPropEditOpen(false)} title={`EDIT — ${propEditTarget?.name || ''}`}>
        <div className="space-y-3">
          <Input label="NAME" value={propForm.name} onChange={(e) => setPropForm({ ...propForm, name: normalizeIdent(e.target.value) })} />
          <Input label="DISPLAY NAME" value={propForm.displayName} onChange={(e) => setPropForm({ ...propForm, displayName: normalizeIdent(e.target.value) })} />
          <div>
            <label className="mb-1.5 block font-mono text-[10px] font-semibold tracking-wider text-ink-muted">DATA TYPE</label>
            <select value={propForm.dataType} onChange={(e) => setPropForm({ ...propForm, dataType: e.target.value })} className="w-full border border-border bg-canvas px-3 py-2 font-mono text-sm text-ink">
              <option value="text">text</option><option value="bigint">bigint</option><option value="double precision">double precision</option><option value="timestamp">timestamp</option><option value="boolean">boolean</option>
            </select>
          </div>
          <div>
            <label className="mb-1.5 block font-mono text-[10px] font-semibold tracking-wider text-ink-muted">
              SOURCE COLUMN {sqlOutputCols.length > 0 && <span className="text-ink-ghost font-normal">(from SQL output)</span>}
            </label>
            {sqlOutputCols.length > 0 ? (
              <select value={propForm.sourceColumn} onChange={(e) => setPropForm({ ...propForm, sourceColumn: e.target.value })} className="w-full border border-border bg-canvas px-3 py-2 font-mono text-sm text-ink">
                <option value="">— select column —</option>
                {sqlOutputCols.map((col) => <option key={col} value={col}>{col}</option>)}
              </select>
            ) : (
              <Input value={propForm.sourceColumn} onChange={(e) => setPropForm({ ...propForm, sourceColumn: e.target.value })} placeholder="Run VALIDATE first to see columns" />
            )}
          </div>
          <Input label="SHORT DESCRIPTION" value={propForm.shortDescription} onChange={(e) => setPropForm({ ...propForm, shortDescription: e.target.value })} placeholder={t('prop_short_desc_placeholder')} />
          <Textarea label="DESCRIPTION" value={propForm.description} onChange={(e) => setPropForm({ ...propForm, description: e.target.value })} rows={2} />

          {/* Column aliases */}
          <div>
            <label className="mb-1.5 block font-mono text-[10px] font-semibold tracking-wider text-ink-muted">
              {t('prop_link_values_label')} <span className="text-ink-ghost font-normal">{t('prop_link_values_hint')}</span>
            </label>
            <div className="flex flex-wrap gap-1 mb-2">
              {propAliases.map(a => (
                <span key={a.id} className="inline-flex items-center gap-1 border border-blue-400 bg-blue-50 text-blue-700 px-1.5 py-0.5 font-mono text-[10px]">
                  {a.keyword}
                  <button onClick={() => deleteAlias(a.id)} className="text-blue-400 hover:text-red-600">
                    <Trash2 size={9} />
                  </button>
                </span>
              ))}
              {propAliases.length === 0 && <span className="font-mono text-[10px] text-ink-ghost">{t('prop_link_values_empty')}</span>}
            </div>
            <div className="flex items-center gap-1">
              <input
                value={newAlias}
                onChange={e => setNewAlias(e.target.value)}
                onKeyDown={e => e.key === 'Enter' && (e.preventDefault(), addAlias())}
                placeholder={t('prop_alias_placeholder')}
                className="flex-1 border border-border px-2 py-1.5 font-mono text-xs text-ink outline-none placeholder:text-ink-ghost"
              />
              <Button variant="ghost" size="sm" onClick={addAlias} disabled={!newAlias.trim()}>
                <Plus size={11} /> {t('prop_alias_add_btn')}
              </Button>
            </div>
          </div>

          <div className="flex justify-end gap-2 pt-2">
            <Button variant="ghost" onClick={() => setPropEditOpen(false)}>CANCEL</Button>
            <Button variant="primary" onClick={updateProp}>SAVE</Button>
          </div>
        </div>
      </Modal>
    </div>
  )
}
