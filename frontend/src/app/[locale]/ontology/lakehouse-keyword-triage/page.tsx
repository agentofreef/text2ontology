'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback, useRef } from 'react'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { Filter, Check, X, Search, Tags, Database, Hash, Tag, Sparkles, Ban, Inbox, AlertTriangle, RefreshCw } from 'lucide-react'
import type { OntObjectType, OntProperty, OntMetricIntent } from '@/types/api'

// ─── Types ────────────────────────────────────────────────────────────────────

type BadgeType = 'orphan' | 'floating' | 'partial' | 'ignored'

interface QueueItem {
  token: string
  count: number
  anchorCount: number
  floatingCount: number
  badge: BadgeType
}

interface QueueResponse {
  data: QueueItem[]
  total: number
  counts: { orphan: number; floating: number; partial: number; ignored: number }
}

type BindingKind = 'od_alias' | 'column_alias' | 'value_alias' | 'intent_trigger' | 'ignore' | 'floating'

interface Binding {
  id: string
  keyword: string
  aliases: string[]
  kind: BindingKind
  propertyId: string | null
  propertyName: string | null
  propertyOd: string | null
  objectId: string | null
  objectName: string | null
  intentId: string | null
  intentName: string | null
  intentOd: string | null
  isColumnName: boolean
  isStopword: boolean
}

interface TokenMapping {
  token: string
  keyword: string
  odName: string
  propName: string
  mappedTable: string
  mappedField: string
  tier: string
}

interface Question {
  id: string
  question: string
  tokens: string[]
  tokenMappings: TokenMapping[]
  status: string
  createdAt: string
}

interface TokenDetail {
  token: string
  bindings: Binding[]
  questions: Question[]
}

interface OntLink {
  id: string
  fromObjectId: string
  fromObjectName: string
  toObjectId: string
  toObjectName: string
  cardinality: string
  name: string
}

interface AddOdAlias { kind: 'od_alias'; objectId: string }
interface AddColumnAlias { kind: 'column_alias'; propertyId: string; isColumnName: true }
interface AddValueAlias { kind: 'value_alias'; propertyId: string; value?: string }
interface AddIntentTrigger { kind: 'intent_trigger'; intentId: string }
type NewBinding = AddOdAlias | AddColumnAlias | AddValueAlias | AddIntentTrigger

// ─── Helpers ─────────────────────────────────────────────────────────────────

type TFunc = (key: string) => string

function badgeLabel(badge: BadgeType, t: TFunc) {
  if (badge === 'orphan') return t('badge_orphan')
  if (badge === 'floating') return t('badge_floating')
  if (badge === 'partial') return t('badge_partial')
  return t('badge_ignored')
}

function badgeStyle(badge: BadgeType) {
  // orphan = 待处理 需要关注 → danger (红)
  // floating = 有未归属绑定 → ink (加粗边框)
  // partial = 已部分完成 → success (绿)
  // ignored = 停用词 → ink-ghost (灰)
  if (badge === 'orphan') return 'border-danger/50 text-danger bg-danger/5'
  if (badge === 'floating') return 'border-ink text-ink bg-white'
  if (badge === 'partial') return 'border-success/50 text-success bg-success/5'
  return 'border-border text-ink-ghost bg-canvas-alt'
}

function kindLabel(kind: BindingKind, t: TFunc) {
  if (kind === 'od_alias') return t('kind_od_alias')
  if (kind === 'column_alias') return t('kind_col_alias')
  if (kind === 'value_alias') return t('kind_val_alias')
  if (kind === 'intent_trigger') return t('kind_intent_trigger')
  if (kind === 'ignore') return t('kind_ignore')
  return t('kind_floating')
}

function kindStyle(kind: BindingKind) {
  // 黑白灰 + 少量绿色。用 实心/空心/虚线 + 图标 区分 kind
  if (kind === 'od_alias') return 'border-ink bg-ink text-white'            // 实心黑（最强）
  if (kind === 'column_alias') return 'border-ink text-ink bg-white'        // 空心黑
  if (kind === 'value_alias') return 'border-ink-muted text-ink-muted bg-white' // 空心中灰
  if (kind === 'intent_trigger') return 'border-success/60 text-success bg-success/5' // 绿（语义：canonical）
  if (kind === 'ignore') return 'border-border text-ink-ghost bg-canvas-alt'
  return 'border-border border-dashed text-ink-ghost bg-white'
}

function tierStyle(tier: string) {
  if (tier === 'EXACT') return 'border-success/60 text-success bg-success/5'
  if (tier === 'FUZZY') return 'border-ink-muted text-ink-muted bg-white'
  if (tier === 'VEC') return 'border-ink-ghost text-ink-muted bg-canvas-alt'
  return 'border-border border-dashed text-ink-ghost bg-white'
}

function highlightToken(text: string, token: string) {
  if (!token) return text
  const escaped = token.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const parts = text.split(new RegExp(`(${escaped})`, 'gi'))
  return parts.map((part, i) =>
    part.toLowerCase() === token.toLowerCase()
      ? <span key={i} className="font-semibold text-ink underline decoration-ink/40 underline-offset-2">{part}</span>
      : part
  )
}

function useFetchLocal<T>(fetcher: () => Promise<T>, deps: unknown[]): { data: T | null; loading: boolean; error: string | null } {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const memoFetcher = useCallback(fetcher, deps)
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    memoFetcher().then(d => { if (!cancelled) setData(d) })
      .catch(e => { if (!cancelled) setError(e instanceof Error ? e.message : 'Error') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [memoFetcher])
  return { data, loading, error }
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

// ─── Sub-components ───────────────────────────────────────────────────────────

function OdAliasVisualizer({ token, projectId, objects, selectedOdId, onSelectOd }: {
  token: string; projectId: string; objects: OntObjectType[]
  selectedOdId: string; onSelectOd: (id: string) => void
}) {
  const t = useTranslations('triage')
  const selectedOd = objects.find(o => o.id === selectedOdId)
  const { data: linksData } = useFetchLocal<{ data: OntLink[] }>(
    () => selectedOdId ? api<{ data: OntLink[] }>(`/ontology/links?projectId=${projectId}`) : Promise.resolve({ data: [] }),
    [selectedOdId, projectId]
  )
  const links = (linksData?.data ?? []).filter(l => l.fromObjectId === selectedOdId || l.toObjectId === selectedOdId)

  return (
    <div className="ml-6 mt-2 space-y-2">
      <select value={selectedOdId} onChange={e => onSelectOd(e.target.value)}
        className="w-full border border-border bg-white px-2 py-1.5 text-xs text-ink outline-none focus:border-ink">
        <option value="">— Od —</option>
        {objects.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}
      </select>
      {!selectedOd ? (
        <div className="border border-dashed border-border px-3 py-2 text-center text-xs text-ink-ghost">{t('select_od_hint')}</div>
      ) : (
        <div className="border border-border bg-white p-3 space-y-2">
          <div className="flex items-center gap-2">
            <Database size={12} className="text-ink-muted flex-shrink-0" />
            <span className="font-sans font-semibold text-sm text-ink">{selectedOd.name}</span>
            {selectedOd.displayName && selectedOd.displayName !== selectedOd.name && (
              <span className="text-[11px] text-ink-ghost">({selectedOd.displayName})</span>
            )}
          </div>
          <div className="text-[11px] text-ink-ghost">
            <span className="font-mono">{selectedOd.kind}</span>
            <span className="mx-1">·</span>
            <span className="tabular-nums">{selectedOd.properties?.length ?? 0}</span> {t('prop_count_suffix')}
          </div>
          {selectedOd.properties && selectedOd.properties.filter(p => p.mark !== false).slice(0, 5).length > 0 && (
            <div className="flex flex-wrap gap-1">
              {selectedOd.properties.filter(p => p.mark !== false).slice(0, 5).map(p => (
                <span key={p.id} className="border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] text-ink-muted font-mono">{p.name}</span>
              ))}
            </div>
          )}
          {links.length > 0 && (
            <div className="border-t border-border-light pt-2 space-y-0.5">
              {links.map(l => {
                const isFrom = l.fromObjectId === selectedOdId
                return (
                  <div key={l.id} className="text-[11px] text-ink-ghost">
                    <span aria-hidden="true">↔ </span>
                    {isFrom ? l.toObjectName : l.fromObjectName}
                    <span className="ml-1 text-ink-ghost/60">({l.cardinality})</span>
                  </div>
                )
              })}
            </div>
          )}
          {token && <div className="border-t border-border-light pt-2 text-[11px] text-ink">"{token}" → {selectedOd.name} {t('od_alias_suffix')}</div>}
        </div>
      )}
    </div>
  )
}

function ColAliasVisualizer({ token, projectId, objects, selectedOdId, onSelectOd, selectedPropertyId, onSelectProperty }: {
  token: string; projectId: string; objects: OntObjectType[]
  selectedOdId: string; onSelectOd: (id: string) => void
  selectedPropertyId: string; onSelectProperty: (id: string) => void
}) {
  const t = useTranslations('triage')
  const selectedOd = objects.find(o => o.id === selectedOdId)
  const properties = (selectedOd?.properties ?? []).filter(p => p.mark !== false)
  const selectedProp = properties.find(p => p.id === selectedPropertyId)
  const [previewCache, setPreviewCache] = useState<Map<string, Record<string, unknown>>>(new Map())
  const [previewLoading, setPreviewLoading] = useState(false)

  useEffect(() => {
    if (!selectedOdId || previewCache.has(selectedOdId) || !projectId) return
    setPreviewLoading(true)
    api<{ data: unknown }>(`/ontology/cache/preview?projectId=${projectId}&objectTypeId=${selectedOdId}`)
      .then(res => {
        const rows = typeof res.data === 'string' ? JSON.parse(res.data as string) : res.data
        const firstRow: Record<string, unknown> = Array.isArray(rows) && rows.length > 0 ? rows[0] : {}
        setPreviewCache(prev => new Map(prev).set(selectedOdId, firstRow))
      }).catch(() => {}).finally(() => setPreviewLoading(false))
  }, [selectedOdId, projectId, previewCache])

  const previewRow = previewCache.get(selectedOdId) ?? null

  return (
    <div className="ml-6 mt-2">
      <div className="flex border border-border bg-white overflow-hidden" style={{ minHeight: 140 }}>
        <div className="border-r border-border overflow-y-auto" style={{ width: '35%', maxHeight: 220 }}>
          {objects.map(o => (
            <button key={o.id} onClick={() => { onSelectOd(o.id); onSelectProperty('') }}
              className={`w-full text-left px-2 py-1.5 text-xs border-l-2 cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                selectedOdId === o.id ? 'border-l-ink bg-canvas-alt font-semibold text-ink' : 'border-l-transparent text-ink-muted'
              }`}>{o.name}</button>
          ))}
        </div>
        <div className="flex-1 overflow-y-auto" style={{ maxHeight: 220 }}>
          {!selectedOd ? (
            <div className="px-2 py-3 text-[11px] text-ink-ghost text-center">{t('select_od_arrow')}</div>
          ) : properties.map(p => {
            const sampleVal = previewRow ? previewRow[p.sourceColumn ?? p.name] : undefined
            return (
              <button key={p.id} onClick={() => onSelectProperty(p.id)}
                className={`w-full text-left px-2 py-1.5 border-l-2 border-b border-border-light cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                  selectedPropertyId === p.id ? 'border-l-ink bg-canvas-alt' : 'border-l-transparent'
                }`}>
                <div className="flex items-center gap-1.5 flex-wrap">
                  <span className="font-sans font-semibold text-xs text-ink">{p.name}</span>
                  {p.dataType && <span className="border border-border px-1 text-[11px] text-ink-ghost font-mono">{p.dataType}</span>}
                  {sampleVal !== undefined && sampleVal !== null && (
                    <span className="text-[11px] text-ink-ghost truncate max-w-[80px]">e.g. {String(sampleVal)}</span>
                  )}
                </div>
              </button>
            )
          })}
          {previewLoading && <div className="px-2 py-1 text-[11px] text-ink-ghost">{t('loading')}</div>}
        </div>
      </div>
      {selectedOd && selectedProp && (
        <div className="mt-2 text-[11px] text-ink border-t border-border-light pt-2">
          "{token}" → {selectedOd.name}.{selectedProp.name} {t('col_alias_suffix')}
        </div>
      )}
    </div>
  )
}

function ValAliasVisualizer({ token, projectId, objects, selectedOdId, onSelectOd, selectedPropertyId, onSelectProperty, selectedValue, onSelectValue }: {
  token: string; projectId: string; objects: OntObjectType[]
  selectedOdId: string; onSelectOd: (id: string) => void
  selectedPropertyId: string; onSelectProperty: (id: string) => void
  selectedValue: string; onSelectValue: (v: string) => void
}) {
  const t = useTranslations('triage')
  const selectedOd = objects.find(o => o.id === selectedOdId)
  const properties = (selectedOd?.properties ?? []).filter(p => p.mark !== false)
  const selectedProp = properties.find(p => p.id === selectedPropertyId)
  const [distinctCache, setDistinctCache] = useState<Map<string, string[]>>(new Map())
  const [distinctLoading, setDistinctLoading] = useState(false)
  const [showCustom, setShowCustom] = useState(false)
  const [customInput, setCustomInput] = useState('')

  type RichProp = OntProperty & { existingValues?: string[]; enumValues?: string[] }
  const richProp = selectedProp as RichProp | undefined
  const propExistingValues = richProp?.existingValues ?? []
  const propEnumValues = richProp?.enumValues ?? []
  const preloadedValues = propExistingValues.length > 0 ? propExistingValues : propEnumValues.length > 0 ? propEnumValues : null
  const cacheKey = selectedProp ? `${selectedOdId}:${selectedProp.sourceColumn ?? selectedProp.name}` : null

  useEffect(() => {
    if (preloadedValues) return
    if (!cacheKey || !selectedOdId || !selectedProp || !projectId) return
    if (distinctCache.has(cacheKey)) return
    const col = selectedProp.sourceColumn ?? selectedProp.name
    setDistinctLoading(true)
    api<{ values: string[]; count: number; truncated: boolean }>(
      `/ontology/cache/distinct?projectId=${projectId}&objectTypeId=${selectedOdId}&column=${encodeURIComponent(col)}&limit=100`
    ).then(res => { setDistinctCache(prev => new Map(prev).set(cacheKey, res.values ?? [])) })
      .catch(() => { setDistinctCache(prev => new Map(prev).set(cacheKey, [])) })
      .finally(() => setDistinctLoading(false))
  }, [preloadedValues, cacheKey, selectedOdId, selectedProp, projectId, distinctCache])

  const distinctValues = preloadedValues ?? (cacheKey ? (distinctCache.get(cacheKey) ?? null) : null)

  return (
    <div className="ml-6 mt-2">
      <div className="flex border border-border bg-white overflow-hidden" style={{ minHeight: 150 }}>
        <div className="border-r border-border overflow-y-auto" style={{ width: '30%', maxHeight: 220 }}>
          {objects.map(o => (
            <button key={o.id} onClick={() => { onSelectOd(o.id); onSelectProperty(''); onSelectValue('') }}
              className={`w-full text-left px-2 py-1.5 text-xs border-l-2 cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                selectedOdId === o.id ? 'border-l-ink bg-canvas-alt font-semibold text-ink' : 'border-l-transparent text-ink-muted'
              }`}>{o.name}</button>
          ))}
        </div>
        <div className="border-r border-border overflow-y-auto" style={{ width: '33%', maxHeight: 220 }}>
          {!selectedOd ? (
            <div className="px-2 py-3 text-[11px] text-ink-ghost text-center">← Od</div>
          ) : properties.map(p => (
            <button key={p.id} onClick={() => { onSelectProperty(p.id); onSelectValue(''); setShowCustom(false) }}
              className={`w-full text-left px-2 py-1.5 border-l-2 border-b border-border-light cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                selectedPropertyId === p.id ? 'border-l-ink bg-canvas-alt' : 'border-l-transparent'
              }`}>
              <div className="font-sans font-semibold text-xs text-ink truncate">{p.name}</div>
              {p.dataType && <div className="text-[11px] text-ink-ghost font-mono">{p.dataType}</div>}
            </button>
          ))}
        </div>
        <div className="flex-1 overflow-y-auto" style={{ maxHeight: 220 }}>
          {!selectedProp ? (
            <div className="px-2 py-3 text-[11px] text-ink-ghost text-center">{t('select_prop_arrow')}</div>
          ) : distinctLoading ? (
            <div className="px-2 py-2 text-[11px] text-ink-ghost">{t('loading')}</div>
          ) : distinctValues === null || distinctValues.length === 0 ? (
            <div className="px-2 py-2">
              <div className="text-[11px] text-ink-ghost mb-1">{t('no_candidates')}</div>
              <input value={selectedValue} onChange={e => onSelectValue(e.target.value)} placeholder={t('value_placeholder')}
                className="w-full border border-border px-2 py-1 text-[11px] text-ink outline-none focus:border-ink" />
            </div>
          ) : (
            <div>
              {distinctValues.map(v => (
                <button key={v} onClick={() => { onSelectValue(v); setShowCustom(false) }}
                  className={`w-full text-left px-2 py-1 border-b border-border-light text-xs cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                    selectedValue === v ? 'bg-canvas-alt font-semibold text-ink border-l-2 border-l-ink' : 'text-ink-muted'
                  }`}>{v}</button>
              ))}
              <button onClick={() => setShowCustom(v => !v)} className="w-full text-left px-2 py-1 text-xs text-ink-ghost italic cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink">
                + {t('custom_value')}
              </button>
              {showCustom && (
                <div className="px-2 py-1">
                  <input value={customInput} onChange={e => { setCustomInput(e.target.value); onSelectValue(e.target.value) }} placeholder={t('custom_value_placeholder')}
                    autoFocus className="w-full border border-border px-2 py-1 text-[11px] text-ink outline-none focus:border-ink" />
                </div>
              )}
            </div>
          )}
        </div>
      </div>
      {selectedOd && selectedProp && selectedValue && (
        <div className="mt-2 text-[11px] text-ink border-t border-border-light pt-2">
          "{token}" → {selectedOd.name}.{selectedProp.name} = '{selectedValue}'
        </div>
      )}
    </div>
  )
}

function IntentVisualizer({ token, intents, selectedIntentId, onSelectIntent }: {
  token: string; intents: OntMetricIntent[]; selectedIntentId: string; onSelectIntent: (id: string) => void
}) {
  const t = useTranslations('triage')
  const selectedIntent = intents.find(i => i.id === selectedIntentId)
  return (
    <div className="ml-6 mt-2 space-y-2">
      <select value={selectedIntentId} onChange={e => onSelectIntent(e.target.value)}
        className="w-full border border-border bg-white px-2 py-1.5 text-xs text-ink outline-none focus:border-ink">
        <option value="">— Intent —</option>
        {intents.map(intent => <option key={intent.id} value={intent.id}>{intent.name}{intent.objectName ? ` (${intent.objectName})` : ''}</option>)}
      </select>
      {!selectedIntent ? (
        <div className="border border-dashed border-border px-3 py-2 text-center text-xs text-ink-ghost">{t('select_intent_hint')}</div>
      ) : (
        <div className="border border-border bg-white p-3 space-y-2">
          <div className="flex items-center gap-2">
            <Sparkles size={12} className="text-success flex-shrink-0" />
            <span className="font-sans font-semibold text-sm text-ink">{selectedIntent.name}</span>
          </div>
          {selectedIntent.canonicalMetric && (
            <div className="text-[11px] text-ink-muted">
              <span>{t('metric_label')}</span>
              <span className="font-mono font-semibold text-ink">{selectedIntent.canonicalMetric}</span>
            </div>
          )}
          {selectedIntent.autoGroupBy && selectedIntent.autoGroupBy.length > 0 && (
            <div className="flex flex-wrap gap-1 items-center">
              <span className="text-[11px] text-ink-ghost">group_by:</span>
              {selectedIntent.autoGroupBy.map((g, i) => <span key={i} className="border border-border bg-canvas-alt px-1.5 py-0.5 text-[11px] text-ink-muted font-mono">{g}</span>)}
            </div>
          )}
          {token && <div className="border-t border-border-light pt-2 text-[11px] text-ink">"{token}" {t('intent_trigger_suffix')}</div>}
        </div>
      )}
    </div>
  )
}

// ─── Left Sidebar ─────────────────────────────────────────────────────────────

function LeftSidebar({ counts, total, selectedBadge, onSelectBadge, search, onSearchChange }: {
  counts: QueueResponse['counts'] | null; total: number
  selectedBadge: BadgeType | 'all'; onSelectBadge: (b: BadgeType | 'all') => void
  search: string; onSearchChange: (v: string) => void
}) {
  const t = useTranslations('triage')
  const reduce = useReducedMotion()
  const industrial = useStyleMode().mode === 'industrial'
  const filters: { key: BadgeType | 'all'; label: string; count: number; kind: 'all' | BadgeType }[] = [
    { key: 'all', label: t('filter_all'), count: total, kind: 'all' },
    { key: 'orphan', label: t('badge_orphan'), count: counts?.orphan ?? 0, kind: 'orphan' },
    { key: 'floating', label: t('badge_floating'), count: counts?.floating ?? 0, kind: 'floating' },
    { key: 'partial', label: t('badge_partial'), count: counts?.partial ?? 0, kind: 'partial' },
    { key: 'ignored', label: t('badge_ignored'), count: counts?.ignored ?? 0, kind: 'ignored' },
  ]

  return (
    <div className={`w-[220px] flex-shrink-0 bg-canvas-alt flex flex-col min-h-0 ${industrial ? 'border-r-2 border-ink' : 'border-r border-border'}`}>
      <div className={`px-3 py-2.5 flex items-center gap-2 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
        {industrial ? (
          <span className="font-mono text-[10px] tracking-[0.22em] text-ink-ghost">// {t('filter_label').toString().toUpperCase()}</span>
        ) : (
          <>
            <Filter size={12} className="text-ink-muted" aria-hidden="true" />
            <span className="text-xs font-semibold text-ink-muted font-medium">{t('filter_label')}</span>
          </>
        )}
        {total > 0 && (
          <span className={`ml-auto px-1.5 py-0.5 tabular-nums ${industrial ? 'font-mono text-[10px] tracking-[0.06em] border border-ink text-ink' : 'rounded-md border border-border bg-white text-[11px] text-ink-ghost'}`}>
            {total}
          </span>
        )}
      </div>
      <div className={`px-3 py-2 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
        <div className={`flex items-center bg-white px-2 py-1.5 focus-within:border-ink ${industrial ? 'border border-ink/60' : 'border border-border'}`}>
          <Search size={12} className="text-ink-ghost flex-shrink-0 mr-1.5" aria-hidden="true" />
          <input value={search} onChange={e => onSearchChange(e.target.value)} placeholder={t('search_placeholder')}
            className={`w-full bg-transparent text-xs text-ink outline-none placeholder:text-ink-ghost ${industrial ? 'font-mono tracking-[0.02em]' : ''}`} />
        </div>
      </div>
      <div className="flex-1 overflow-y-auto py-1.5 px-1.5 space-y-0.5">
        {filters.map(f => {
          const selected = selectedBadge === f.key
          return (
            <motion.button
              key={f.key}
              onClick={() => onSelectBadge(f.key)}
              whileHover={reduce ? {} : { x: 2 }}
              whileTap={reduce ? {} : { scale: 0.97 }}
              transition={{ duration: 0.12 }}
              aria-pressed={selected}
              className={`group w-full flex items-center justify-between px-2.5 py-1.5 text-left cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                industrial
                  ? `font-mono text-[11px] tracking-[0.06em] uppercase ${selected ? 'bg-ink text-white' : 'text-ink-muted hover:bg-canvas/40 hover:text-ink'}`
                  : `text-xs ${selected ? 'bg-white border border-border text-ink font-semibold' : 'text-ink-muted'}`
              }`}
            >
              <span>{f.label}</span>
              <span
                className={`px-1.5 py-0.5 text-[11px] tabular-nums ${
                  industrial
                    ? `font-mono tracking-[0.04em] ${selected ? 'border border-white/60 text-white' : 'border border-ink/30 text-ink-ghost'}`
                    : `rounded-md border ${selected ? 'border-ink text-ink' : 'border-border text-ink-ghost'}`
                }`}
              >
                {f.count}
              </span>
            </motion.button>
          )
        })}
      </div>
    </div>
  )
}

// ─── Token Queue ──────────────────────────────────────────────────────────────

function TokenQueue({ items, loading, selectedToken, onSelect }: {
  items: QueueItem[]; loading: boolean; selectedToken: string | null; onSelect: (token: string) => void
}) {
  const t = useTranslations('triage')
  const reduce = useReducedMotion()
  const industrial = useStyleMode().mode === 'industrial'
  return (
    <div className={`w-[220px] flex-shrink-0 flex flex-col min-h-0 bg-white ${industrial ? 'border-r-2 border-ink' : 'border-r border-border'}`}>
      <div className={`px-3 py-2.5 bg-canvas-alt flex items-center gap-2 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
        {industrial ? (
          <span className="font-mono text-[10px] tracking-[0.22em] text-ink-ghost">// {t('queue_label').toString().toUpperCase()}</span>
        ) : (
          <>
            <Inbox size={12} className="text-ink-muted" aria-hidden="true" />
            <span className="text-[11px] font-semibold text-ink-muted font-medium">{t('queue_label')}</span>
          </>
        )}
        <span className={`ml-auto tabular-nums ${industrial ? 'font-mono text-[10px] tracking-[0.06em] text-ink' : 'text-[11px] text-ink-ghost'}`}>{items.length}</span>
      </div>
      <div className="flex-1 overflow-y-auto">
        {loading ? (
          <div className="flex h-48 items-center justify-center"><InlineLoader text={t('loading')} /></div>
        ) : items.length === 0 ? (
          <div className="flex h-48 flex-col items-center justify-center gap-2">
            {industrial ? (
              <>
                <div className="font-mono text-[10px] tracking-[0.22em] text-ink-ghost">// QUEUE CLEAR</div>
                <span className="font-mono text-xs text-ink-muted">{t('queue_empty')}</span>
              </>
            ) : (
              <>
                <Check size={16} className="text-ink-ghost" aria-hidden="true" />
                <span className="text-xs text-ink-ghost">{t('queue_empty')}</span>
              </>
            )}
          </div>
        ) : (
          <div className={industrial ? 'divide-y divide-ink/15' : 'divide-y divide-border-light'}>
            <AnimatePresence initial={false}>
              {items.map(item => {
                const selected = selectedToken === item.token
                return (
                  <motion.button
                    key={item.token}
                    onClick={() => onSelect(item.token)}
                    layout
                    initial={reduce ? {} : { opacity: 0 }}
                    animate={reduce ? {} : { opacity: 1 }}
                    exit={reduce ? {} : { opacity: 0 }}
                    whileHover={reduce ? {} : { x: 2 }}
                    whileTap={reduce ? {} : { scale: 0.995 }}
                    transition={{ duration: 0.12 }}
                    aria-pressed={selected}
                    className={`w-full flex items-center gap-2 px-3 py-2 text-left cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink border-l-2 ${
                      selected
                        ? industrial ? 'border-l-ink bg-ink text-white' : 'border-l-ink bg-canvas-alt'
                        : 'border-l-transparent'
                    }`}
                  >
                    <span className={`flex-1 truncate ${industrial ? `font-mono text-sm font-bold tracking-[0.02em] ${selected ? 'text-white' : 'text-ink'}` : 'font-sans text-sm font-semibold text-ink'}`}>
                      {item.token}
                    </span>
                    <span
                      className={`flex-shrink-0 px-1.5 py-0.5 text-[11px] tabular-nums ${
                        industrial
                          ? `font-mono tracking-[0.04em] ${selected ? 'border border-white/60 text-white' : 'border border-ink/30 text-ink-ghost'}`
                          : 'rounded-md border border-border text-ink-ghost'
                      }`}
                    >
                      {item.count}
                    </span>
                    <span
                      className={`flex-shrink-0 px-1.5 py-0.5 text-[11px] ${
                        industrial
                          ? `font-mono uppercase tracking-[0.12em] ${selected ? 'border border-white/60 text-white' : `border ${badgeStyle(item.badge).replace(/^bg-\S+\s+/, '')}`}`
                          : `rounded-md border ${badgeStyle(item.badge)}`
                      }`}
                    >
                      {badgeLabel(item.badge, t)}
                    </span>
                  </motion.button>
                )
              })}
            </AnimatePresence>
          </div>
        )}
      </div>
    </div>
  )
}

// ─── Assignment Panel ─────────────────────────────────────────────────────────

function AssignmentPanel({ token, detail, projectId, objects, intents, pendingDeleteIds, onSaved }: {
  token: string | null; tokenCount?: number; detail: TokenDetail | null
  projectId: string; objects: OntObjectType[]; intents: OntMetricIntent[]
  pendingDeleteIds: Set<string>; onSaved: (advance: boolean) => void
}) {
  const t = useTranslations('triage')
  const msg = useMessage()
  const [saving, setSaving] = useState(false)
  const [checkOd, setCheckOd] = useState(false)
  const [checkCol, setCheckCol] = useState(false)
  const [checkVal, setCheckVal] = useState(false)
  const [checkIntent, setCheckIntent] = useState(false)
  const [checkIgnore, setCheckIgnore] = useState(false)
  const [odObjectId, setOdObjectId] = useState('')
  const [colObjectId, setColObjectId] = useState('')
  const [colPropertyId, setColPropertyId] = useState('')
  const [valObjectId, setValObjectId] = useState('')
  const [valPropertyId, setValPropertyId] = useState('')
  const [valValue, setValValue] = useState('')
  const [intentId, setIntentId] = useState('')

  useEffect(() => { if (checkIgnore) { setCheckOd(false); setCheckCol(false); setCheckVal(false); setCheckIntent(false) } }, [checkIgnore])
  useEffect(() => { if (checkOd || checkCol || checkVal || checkIntent) setCheckIgnore(false) }, [checkOd, checkCol, checkVal, checkIntent])

  const resetForm = () => {
    setCheckOd(false); setCheckCol(false); setCheckVal(false)
    setCheckIntent(false); setCheckIgnore(false)
    setOdObjectId(''); setColObjectId(''); setColPropertyId('')
    setValObjectId(''); setValPropertyId(''); setValValue(''); setIntentId('')
  }

  const buildNewBindings = (): NewBinding[] => {
    const result: NewBinding[] = []
    if (checkOd && odObjectId) result.push({ kind: 'od_alias', objectId: odObjectId })
    if (checkCol && colPropertyId) result.push({ kind: 'column_alias', propertyId: colPropertyId, isColumnName: true })
    if (checkVal && valPropertyId) result.push({ kind: 'value_alias', propertyId: valPropertyId, value: valValue || undefined })
    if (checkIntent && intentId) result.push({ kind: 'intent_trigger', intentId })
    return result
  }

  const doSave = async (advance: boolean) => {
    if (!token) return
    if (!projectId) { msg.error(t('err_no_project')); return }
    setSaving(true)
    try {
      const newBindings = buildNewBindings()
      const existingBindings: NewBinding[] = (detail?.bindings ?? [])
        .filter(b => !pendingDeleteIds.has(b.id))
        .map(b => {
          if (b.kind === 'od_alias' && b.objectId) return { kind: 'od_alias' as const, objectId: b.objectId }
          if (b.kind === 'column_alias' && b.propertyId) return { kind: 'column_alias' as const, propertyId: b.propertyId, isColumnName: true as const }
          if (b.kind === 'value_alias' && b.propertyId) return { kind: 'value_alias' as const, propertyId: b.propertyId }
          if (b.kind === 'intent_trigger' && b.intentId) return { kind: 'intent_trigger' as const, intentId: b.intentId }
          return null
        }).filter((b): b is NewBinding => b !== null)
      const allBindings = [...existingBindings, ...newBindings]
      await api('/ontology/keyword-triage/assign', {
        method: 'POST',
        body: { projectId, token, ignore: checkIgnore, bindings: checkIgnore ? [] : allBindings },
      })
      msg.success(t('msg_saved', { token }))
      resetForm()
      onSaved(advance)
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('err_save_failed'))
    } finally { setSaving(false) }
  }

  if (!token) {
    return (
      <div className="flex-1 min-w-[380px] flex flex-col min-h-0 border-r border-border bg-white">
        <div className="px-4 py-2.5 bg-canvas-alt border-b border-border">
          <span className="text-[11px] font-semibold text-ink-muted font-medium">{t('assign_label')}</span>
        </div>
        <div className="flex flex-1 items-center justify-center text-sm text-ink-ghost">{t('select_token_hint')}</div>
      </div>
    )
  }

  const hasNew = buildNewBindings().length > 0
  const hasDeletes = pendingDeleteIds.size > 0
  const noOp = !checkIgnore && !hasNew && !hasDeletes
  const disable = saving || noOp

  const kindRow = (active: boolean, setActive: (v: boolean) => void, icon: React.ReactNode, label: string, visualizer: React.ReactNode | null) => (
    <div>
      <label className={`flex items-center gap-2 cursor-pointer ${checkIgnore ? 'opacity-50 pointer-events-none' : ''}`}>
        <input type="checkbox" checked={active} onChange={e => setActive(e.target.checked)} disabled={checkIgnore}
          className="h-3.5 w-3.5 accent-ink" />
        {icon}
        <span className="text-sm font-medium text-ink">{label}</span>
      </label>
      {active && visualizer}
    </div>
  )

  return (
    <div className="flex-1 min-w-[380px] flex flex-col min-h-0 border-r border-border bg-white overflow-y-auto">
      <div className="px-4 py-2.5 bg-canvas-alt border-b border-border flex-shrink-0 flex items-center gap-2">
        <span className="text-[11px] font-semibold text-ink-muted font-medium">{t('assign_label')}</span>
        <span className="ml-auto font-sans text-xs font-semibold text-ink">{token}</span>
      </div>

      {objects.length === 0 && (
        <div className="mx-4 mt-3 flex items-start gap-2 border border-danger/40 bg-danger/5 px-3 py-2 text-xs text-danger">
          <AlertTriangle size={14} className="flex-shrink-0 mt-0.5" aria-hidden="true" />
          <span>{t('warn_no_od')}</span>
        </div>
      )}

      <div className="px-4 py-3 space-y-4">
        {kindRow(checkOd, setCheckOd, <Database size={12} className="text-ink-muted flex-shrink-0" aria-hidden="true" />, t('kind_od_alias'),
          checkOd ? <OdAliasVisualizer token={token} projectId={projectId} objects={objects} selectedOdId={odObjectId} onSelectOd={setOdObjectId} /> : null)}

        {kindRow(checkCol, setCheckCol, <Hash size={12} className="text-ink-muted flex-shrink-0" aria-hidden="true" />, t('kind_col_alias'),
          checkCol ? <ColAliasVisualizer token={token} projectId={projectId} objects={objects} selectedOdId={colObjectId} onSelectOd={setColObjectId} selectedPropertyId={colPropertyId} onSelectProperty={setColPropertyId} /> : null)}

        {kindRow(checkVal, setCheckVal, <Tag size={12} className="text-ink-muted flex-shrink-0" aria-hidden="true" />, t('kind_val_alias'),
          checkVal ? <ValAliasVisualizer token={token} projectId={projectId} objects={objects} selectedOdId={valObjectId} onSelectOd={setValObjectId} selectedPropertyId={valPropertyId} onSelectProperty={setValPropertyId} selectedValue={valValue} onSelectValue={setValValue} /> : null)}

        {kindRow(checkIntent, setCheckIntent, <Sparkles size={12} className="text-success flex-shrink-0" aria-hidden="true" />, t('kind_intent_label'),
          checkIntent ? <IntentVisualizer token={token} intents={intents} selectedIntentId={intentId} onSelectIntent={setIntentId} /> : null)}

        <div>
          <label className="flex items-center gap-2 cursor-pointer">
            <input type="checkbox" checked={checkIgnore} onChange={e => setCheckIgnore(e.target.checked)}
              className="h-3.5 w-3.5 accent-ink" />
            <Ban size={12} className="text-ink-ghost flex-shrink-0" aria-hidden="true" />
            <span className="text-sm font-medium text-ink">{t('kind_ignore_label')}</span>
          </label>
        </div>

        <div className="flex gap-2 pt-3 border-t border-border">
          <AnimatedButton variant="primary" size="sm" onClick={() => doSave(true)} disabled={disable}
            className="flex-1 justify-center">
            <Check size={12} />
            {saving ? t('saving') : t('save_and_next')}
          </AnimatedButton>
          <AnimatedButton variant="secondary" size="sm" onClick={() => doSave(false)} disabled={disable}>
            <Check size={12} />
            {t('save_only')}
          </AnimatedButton>
        </div>
      </div>
    </div>
  )
}

// ─── Context Panel ────────────────────────────────────────────────────────────

function ContextPanel({ token, detail, detailLoading, pendingDeleteIds, onToggleDelete }: {
  token: string | null; detail: TokenDetail | null; detailLoading: boolean
  pendingDeleteIds: Set<string>; onToggleDelete: (id: string) => void
}) {
  const t = useTranslations('triage')
  const reduce = useReducedMotion()

  if (detailLoading) {
    return (
      <div className="flex-1 min-w-[320px] flex flex-col min-h-0 bg-white">
        <div className="px-4 py-2.5 bg-canvas-alt border-b border-border">
          <span className="text-[11px] font-semibold text-ink-muted font-medium">{t('context_label')}</span>
        </div>
        <div className="flex flex-1 items-center justify-center"><InlineLoader text={t('loading')} /></div>
      </div>
    )
  }

  if (!token || !detail) {
    return (
      <div className="flex-1 min-w-[320px] flex flex-col min-h-0 bg-white">
        <div className="px-4 py-2.5 bg-canvas-alt border-b border-border">
          <span className="text-[11px] font-semibold text-ink-muted font-medium">{t('context_label')}</span>
        </div>
        <div className="flex flex-1 items-center justify-center text-sm text-ink-ghost">{t('select_token_hint')}</div>
      </div>
    )
  }

  const activeBindings = detail.bindings.filter(b => !pendingDeleteIds.has(b.id))
  const pendingCount = pendingDeleteIds.size

  return (
    <div className="flex-1 min-w-[320px] flex flex-col min-h-0 bg-white">
      <div className="px-4 py-2.5 border-b border-border bg-canvas-alt flex items-center gap-2 flex-shrink-0">
        <Tags size={12} className="text-ink-muted" aria-hidden="true" />
        <span className="font-sans text-sm font-semibold text-ink">{token}</span>
        <span className="ml-auto rounded-md border border-border bg-white px-1.5 py-0.5 text-[11px] text-ink-ghost">
          <span className="tabular-nums">{detail.questions.length}</span> {t('questions_count_suffix')}
        </span>
      </div>

      {/* Existing bindings */}
      <div className="flex-shrink-0 border-b border-border">
        <div className="px-4 py-2 bg-canvas-alt border-b border-border flex items-center">
          <span className="text-[11px] font-semibold text-ink-muted font-medium flex-1">{t('current_bindings', { count: activeBindings.length })}</span>
          {pendingCount > 0 && (
            <span className="rounded-md border border-danger/40 bg-danger/5 px-1.5 py-0.5 text-[11px] text-danger">
              <span className="tabular-nums">{pendingCount}</span> {t('pending_delete_suffix')}
            </span>
          )}
        </div>
        {detail.bindings.length === 0 ? (
          <div className="px-4 py-3 text-xs text-ink-ghost">{t('no_bindings')}</div>
        ) : (
          <div className="divide-y divide-border-light">
            <AnimatePresence initial={false}>
              {detail.bindings.map(b => {
                const isPendingDelete = pendingDeleteIds.has(b.id)
                return (
                  <motion.div
                    key={b.id}
                    layout
                    initial={reduce ? {} : { opacity: 0 }}
                    animate={reduce ? {} : { opacity: 1 }}
                    exit={reduce ? {} : { opacity: 0 }}
                    transition={{ duration: 0.15 }}
                    className={`flex items-center gap-2 px-4 py-1.5 ${isPendingDelete ? 'opacity-40 bg-danger/5' : ''}`}
                  >
                    <span className={`flex-shrink-0 rounded-md border px-1.5 py-0.5 text-[11px] ${kindStyle(b.kind)}`}>{kindLabel(b.kind, t)}</span>
                    <span className={`text-xs flex-1 truncate ${isPendingDelete ? 'line-through text-ink-ghost' : 'text-ink'}`}>
                      {b.kind === 'od_alias' && (b.objectName || b.objectId)}
                      {b.kind === 'column_alias' && `${b.propertyOd || ''}.${b.propertyName || b.propertyId}`}
                      {b.kind === 'value_alias' && `${b.propertyOd || ''}.${b.propertyName || b.propertyId}`}
                      {b.kind === 'intent_trigger' && (b.intentName || b.intentId)}
                      {b.kind === 'ignore' && `(${t('kind_ignore')})`}
                      {b.kind === 'floating' && `(${t('kind_floating')})`}
                    </span>
                    <motion.button
                      onClick={() => onToggleDelete(b.id)}
                      whileHover={reduce ? {} : { scale: 1.15 }}
                      whileTap={reduce ? {} : { scale: 0.9 }}
                      transition={{ duration: 0.12 }}
                      aria-label={isPendingDelete ? t('restore') : t('delete')}
                      className={`flex-shrink-0 p-0.5 cursor-pointer outline-none focus-visible:ring-1 focus-visible:ring-ink ${isPendingDelete ? 'text-ink-muted' : 'text-ink-ghost hover:text-danger'}`}
                    >
                      <X size={12} aria-hidden="true" />
                    </motion.button>
                  </motion.div>
                )
              })}
            </AnimatePresence>
          </div>
        )}
      </div>

      {/* Context questions */}
      <div className="flex flex-col min-h-0 flex-1">
        <div className="px-4 py-2 bg-canvas-alt border-b border-border flex-shrink-0">
          <span className="text-[11px] font-semibold text-ink-muted font-medium">{t('context_questions')}</span>
        </div>
        {detail.questions.length === 0 ? (
          <div className="px-4 py-4 text-xs text-ink-ghost">{t('no_questions')}</div>
        ) : (
          <div className="flex-1 overflow-y-auto divide-y divide-border-light">
            {detail.questions.slice(0, 10).map(q => (
              <div key={q.id} className="px-4 py-2.5">
                <div className="text-xs text-ink mb-1.5">{highlightToken(q.question, token)}</div>
                {q.tokens && q.tokens.length > 0 && (
                  <div className="flex flex-wrap gap-1 ml-2">
                    {q.tokens.map((t, ti) => {
                      const mapping = (q.tokenMappings || []).find(m => m.token === t)
                      const isSelected = t.toLowerCase() === token.toLowerCase()
                      const tier = mapping?.tier || 'MISS'
                      return (
                        <div key={ti} className="flex items-center gap-0.5">
                          <span className={`rounded-md border px-1.5 py-0.5 text-[11px] ${isSelected ? 'border-ink text-ink bg-canvas-alt font-semibold' : tierStyle(tier)}`}>{t}</span>
                          {mapping && !isSelected && (
                            <span className="text-[11px] text-ink-ghost">{mapping.odName && `→ ${mapping.odName}`}{mapping.propName && `.${mapping.propName}`}</span>
                          )}
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

// ─── Page ─────────────────────────────────────────────────────────────────────

export default function KeywordTriagePageMinimal() {
  const t = useTranslations('triage')
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()

  const [queueData, setQueueData] = useState<QueueResponse | null>(null)
  const [queueLoading, setQueueLoading] = useState(false)
  const [selectedBadge, setSelectedBadge] = useState<BadgeType | 'all'>('orphan')
  const [search, setSearch] = useState('')
  const searchDebounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const [selectedToken, setSelectedToken] = useState<string | null>(null)
  const [tokenDetail, setTokenDetail] = useState<TokenDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [pendingDeleteIds, setPendingDeleteIds] = useState<Set<string>>(new Set())
  const [objects, setObjects] = useState<OntObjectType[]>([])
  const [intents, setIntents] = useState<OntMetricIntent[]>([])

  useEffect(() => {
    clearTimeout(searchDebounceRef.current)
    searchDebounceRef.current = setTimeout(() => setDebouncedSearch(search), 300)
    return () => clearTimeout(searchDebounceRef.current)
  }, [search])

  useEffect(() => {
    if (!currentProject) return
    Promise.all([
      api<{ data: OntObjectType[] }>(`/ontology/keyword-triage/objects-tree?projectId=${currentProject.id}`),
      api<{ data: OntMetricIntent[] }>(`/ontology/metric-intents?projectId=${currentProject.id}`),
    ]).then(([objRes, intRes]) => {
      setObjects(objRes.data || [])
      setIntents(intRes.data || [])
    }).catch(() => {})
  }, [currentProject])

  const loadQueue = useCallback(async () => {
    if (!currentProject) return
    setQueueData(prev => { if (prev === null) setQueueLoading(true); return prev })
    try {
      const params = new URLSearchParams({ projectId: currentProject.id })
      if (selectedBadge !== 'all') params.set('badge', selectedBadge)
      if (debouncedSearch) params.set('search', debouncedSearch)
      const res = await api<QueueResponse>(`/ontology/keyword-triage/queue?${params.toString()}`)
      setQueueData(res)
    } catch { msg.error(t('err_load_queue_failed')) }
    finally { setQueueLoading(false) }
  }, [currentProject, selectedBadge, debouncedSearch, msg])

  useEffect(() => { loadQueue() }, [loadQueue])

  const loadTokenDetail = useCallback(async (token: string) => {
    if (!currentProject) return
    setDetailLoading(true)
    setTokenDetail(null)
    setPendingDeleteIds(new Set())
    try {
      const res = await api<TokenDetail>(
        `/ontology/keyword-triage/token?projectId=${currentProject.id}&token=${encodeURIComponent(token)}`
      )
      setTokenDetail(res)
    } catch { msg.error(t('err_load_detail_failed')) }
    finally { setDetailLoading(false) }
  }, [currentProject, msg])

  const handleSelectToken = (token: string) => { setSelectedToken(token); loadTokenDetail(token) }
  const handleToggleDelete = (id: string) => {
    setPendingDeleteIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const handleSaved = (advance: boolean) => {
    setPendingDeleteIds(new Set())
    loadQueue()
    if (advance && queueData) {
      const items = queueData.data
      const idx = items.findIndex(i => i.token === selectedToken)
      const next = items[idx + 1]
      if (next) { setSelectedToken(next.token); loadTokenDetail(next.token) }
      else { setSelectedToken(null); setTokenDetail(null) }
    } else if (selectedToken) {
      loadTokenDetail(selectedToken)
    }
  }

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
        <div className="text-sm text-ink-muted">{t('no_project')}</div>
        <div className="text-xs text-ink-ghost">{t('no_project_hint')}</div>
      </div>
    )
  }

  const selectedQueueItem = queueData?.data.find(i => i.token === selectedToken)

  return (
    <motion.div
      initial={reduce ? false : { opacity: 0 }}
      animate={reduce ? {} : { opacity: 1 }}
      transition={{ duration: 0.2 }}
      className="flex flex-col h-full bg-white"
    >
      <div className={`flex h-14 flex-shrink-0 items-center gap-3 bg-white px-6 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
        {industrial ? (
          <>
            <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // {t('title').toString().toUpperCase()}
            </span>
            <span className="font-mono text-[10px] tracking-[0.14em] text-ink-muted truncate">
              {t('subtitle')}
            </span>
          </>
        ) : (
          <>
            <div className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-border bg-canvas-alt">
              <Tags size={14} className="text-ink" aria-hidden="true" />
            </div>
            <div className="min-w-0">
              <h1 className="text-base font-semibold tracking-tight text-ink">{t('title')}</h1>
              <p className="text-xs text-ink-muted">{t('subtitle')}</p>
            </div>
          </>
        )}
      </div>
      <div className="flex-1 flex min-h-0">
        <LeftSidebar
          counts={queueData?.counts ?? null}
          total={queueData?.total ?? 0}
          selectedBadge={selectedBadge}
          onSelectBadge={setSelectedBadge}
          search={search}
          onSearchChange={setSearch}
        />
        <TokenQueue
          items={queueData?.data ?? []}
          loading={queueLoading}
          selectedToken={selectedToken}
          onSelect={handleSelectToken}
        />
        <AssignmentPanel
          token={selectedToken}
          tokenCount={selectedQueueItem?.count}
          detail={tokenDetail}
          projectId={currentProject.id}
          objects={objects}
          intents={intents}
          pendingDeleteIds={pendingDeleteIds}
          onSaved={handleSaved}
        />
        <ContextPanel
          token={selectedToken}
          detail={tokenDetail}
          detailLoading={detailLoading}
          pendingDeleteIds={pendingDeleteIds}
          onToggleDelete={handleToggleDelete}
        />
      </div>
    </motion.div>
  )
}
