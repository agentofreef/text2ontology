'use client'

// MetricEditor — minimal authoring surface for a "simple" metric.
//
// Mental model: a metric DESCRIBES ITSELF — the bare measure SQL on a single
// OD — and nothing else. Cross-OD JOINs, additional dimensions, and filters
// are NOT authored here; they come from the function-call JSON at runtime,
// exercised in the standalone Function-Call Simulator page (reached from the
// edit page's top bar).
//
// The author writes 4 things, in total:
//   1. Metadata: name, displayName, description, priority, mark
//   2. Primary OD (the FROM target of the bare SQL — single-OD by construction)
//   3. Bare measure SQL: `select <dim>, ..., agg(...) from "<OD>" [group by ...]`
//      The save handler parses it server-side → canonical_metric + auto_group_by
//      + object_id. Exotic shapes (nested/window/JOIN) use the legacy
//      `level='sql'` + `{sys.req/opt.NAME}` escape hatch (deprecation banner).
//   4. Trigger keywords (≥1; how the metric is recalled by the LLM)

import { useState, useMemo } from 'react'
import { SQLEditor } from '@/components/ui/SQLEditor'
import { useProject } from '@/lib/project'
import { useMessage } from '@/lib/message'
import type {
  OntMetricIntentFilter, OntMetricParameter, OntObjectType,
} from '@/types/api'
import { Plus, AlertCircle, Search, AlertTriangle } from 'lucide-react'

// ── Form model ──────────────────────────────────────────────────────────────
// The new editor writes only a few fields; the rest are kept on the form type
// for backward-compat loading + saving of legacy rows (level='sql', pivot,
// canonical_filters, parameters) which the new surface does not foreground.
export type MetricEditorForm = {
  name: string
  displayName: string
  objectId: string
  odIds: string[]
  description: string
  level: 'simple' | 'sql'
  canonicalMetric: string
  querySql: string
  canonicalFilters: OntMetricIntentFilter[]
  autoGroupBy: string[]
  replaceGroupBy: boolean
  defaultOrderByLabel: string
  defaultOrderByDir: 'ASC' | 'DESC' | ''
  defaultLimit: string
  pivotOn: string
  pivotValues: string[]
  pivotColumnLabels: string[]
  pivotTotalLabel: string
  pivotPercentAxis: string
  pivotPercentScope: string
  pivotPercentSuffix: string
  pivotWithPercent: boolean
  pivotAppendGrandTotal: boolean
  parameters: OntMetricParameter[]
  triggerKeywords: string[]
  responseTemplate: string
  priority: number
  mark: boolean
}

export const blankMetricEditorForm: MetricEditorForm = {
  name: '', displayName: '', objectId: '', odIds: [], description: '', level: 'simple',
  canonicalMetric: '', querySql: '',
  canonicalFilters: [], autoGroupBy: [], replaceGroupBy: false,
  defaultOrderByLabel: '', defaultOrderByDir: '', defaultLimit: '',
  pivotOn: '', pivotValues: [], pivotColumnLabels: [],
  pivotTotalLabel: 'Total', pivotPercentAxis: 'row', pivotPercentScope: 'filtered',
  pivotPercentSuffix: '占比', pivotWithPercent: false, pivotAppendGrandTotal: false,
  parameters: [], triggerKeywords: [], responseTemplate: '',
  priority: 0, mark: true,
}

// buildMetricEditorPayload — the new editor sends the bare SQL + meta + triggers.
// The backend save handler auto-parses querySql server-side → populates
// canonical_metric + auto_group_by + object_id (sqlrewrite.ParseBareMetricSQL).
// Legacy fields are passed through unchanged so editing an old row preserves
// data we don't foreground.
export function buildMetricEditorPayload(f: MetricEditorForm): Record<string, unknown> {
  return {
    name: f.name.trim(),
    displayName: f.displayName,
    description: f.description,
    odIds: f.odIds,
    level: f.level,
    querySql: f.querySql,
    canonicalMetric: f.canonicalMetric.trim(),
    canonicalFilters: f.canonicalFilters,
    autoGroupBy: f.autoGroupBy,
    replaceGroupBy: f.replaceGroupBy,
    defaultOrderByLabel: f.defaultOrderByLabel,
    defaultOrderByDir: f.defaultOrderByDir,
    defaultLimit: f.defaultLimit.trim() === '' ? null : (parseInt(f.defaultLimit, 10) || null),
    pivotOn: f.pivotOn,
    pivotValues: f.pivotValues,
    pivotColumnLabels: f.pivotColumnLabels,
    pivotTotalLabel: f.pivotTotalLabel,
    pivotWithPercent: f.pivotWithPercent,
    pivotAppendGrandTotal: f.pivotAppendGrandTotal,
    pivotPercentAxis: f.pivotPercentAxis,
    pivotPercentScope: f.pivotPercentScope,
    pivotPercentSuffix: f.pivotPercentSuffix,
    parameters: f.parameters,
    responseTemplate: f.responseTemplate,
    priority: f.priority,
    mark: f.mark,
    triggerKeywords: f.triggerKeywords,
  }
}

// validateMetricEditorForm — minimal: name, primary OD, querySql, ≥1 trigger.
export function validateMetricEditorForm(
  f: MetricEditorForm,
  t: (key: string) => string,
): string | null {
  if (!f.name.trim()) return t('v_name')
  if (f.odIds.length === 0) return t('v_od')
  if (!f.querySql.trim()) return t('v_query_sql')
  if (f.triggerKeywords.length === 0) return t('v_triggers')
  return null
}

// ── Structural frame helpers (industrial: square, no shadow) ────────────────
function Panel({ title, hint, children }: {
  title: string
  hint?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="border border-border">
      <div className="border-b border-border px-3 py-1.5 bg-canvas-alt flex items-center justify-between gap-2">
        <span className="font-mono text-[9px] font-semibold tracking-wider text-ink-ghost">
          {title}
          {hint && <span className="ml-2 font-normal normal-case text-ink-ghost">{hint}</span>}
        </span>
      </div>
      <div className="p-3 space-y-2">{children}</div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <label className="font-mono text-[9px] font-semibold tracking-wider text-ink-muted">{label}</label>
      {children}
    </div>
  )
}

const inputCls = 'w-full border border-border bg-canvas px-2 py-1.5 font-mono text-xs text-ink outline-none placeholder:text-ink-ghost focus:border-ink'

// ── Props ───────────────────────────────────────────────────────────────────
interface Props {
  form: MetricEditorForm
  setForm: React.Dispatch<React.SetStateAction<MetricEditorForm>>
  objects: OntObjectType[]
  // Kept for prop-interface parity with the route pages (the simulator moved to
  // its own page; these are no longer used here).
  sampleValues: Record<string, string>
  setSampleValues: React.Dispatch<React.SetStateAction<Record<string, string>>>
  t: (key: string, vars?: Record<string, string | number>) => string
}

export function MetricEditor({ form, setForm, objects, t }: Props) {
  const { currentProject } = useProject()
  const msg = useMessage()
  void currentProject; void msg // reserved (kept stable across the simulator extraction)

  // ── Primary OD picker (single OD — cross-OD is JSON-driven at runtime) ───
  const [odSearch, setOdSearch] = useState('')
  const pickerOds = useMemo(() => {
    if (!odSearch.trim()) return objects
    const q = odSearch.toLowerCase()
    return objects.filter(o => o.name.toLowerCase().includes(q) || (o.displayName || '').toLowerCase().includes(q))
  }, [objects, odSearch])
  const primaryOdId = form.odIds[0] || form.objectId || ''
  const setPrimaryOd = (odId: string) =>
    setForm(f => ({ ...f, odIds: [odId], objectId: odId }))

  // ── Triggers ─────────────────────────────────────────────────────────────
  const [kwInput, setKwInput] = useState('')
  const addKeyword = () => {
    const v = kwInput.trim(); if (!v) return
    setForm(f => ({
      ...f,
      triggerKeywords: f.triggerKeywords.includes(v) ? f.triggerKeywords : [...f.triggerKeywords, v],
    }))
    setKwInput('')
  }
  const removeKeyword = (i: number) =>
    setForm(f => ({ ...f, triggerKeywords: f.triggerKeywords.filter((_, idx) => idx !== i) }))
  const triggersEmpty = form.triggerKeywords.length === 0

  const isLegacySQL = form.level === 'sql'

  return (
    <div className="flex flex-1 overflow-hidden">
      {/* ── LEFT PANE: 基本信息 · 主 OD · 触发词 ────────────────────────── */}
      <div className="w-1/2 border-r border-border overflow-y-auto p-5 space-y-4">
        {/* 基本信息 */}
        <Panel title={t('basic_info')} hint={t('basic_info_hint')}>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('f_name')}>
              <input className={inputCls} value={form.name} placeholder="Order.Total"
                onChange={e => setForm({ ...form, name: e.target.value })} />
            </Field>
            <Field label={t('f_display_name')}>
              <input className={inputCls} value={form.displayName} placeholder={t('f_display_name_ph')}
                onChange={e => setForm({ ...form, displayName: e.target.value })} />
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('f_priority')}>
              <input className={inputCls} type="number" value={String(form.priority)} placeholder="0"
                onChange={e => setForm({ ...form, priority: parseInt(e.target.value) || 0 })} />
            </Field>
            <div />
          </div>
          <Field label={t('f_description')}>
            <textarea className={`${inputCls} resize-y`} rows={2} value={form.description}
              placeholder={t('f_description_ph')}
              onChange={e => setForm({ ...form, description: e.target.value })} />
          </Field>
          <label className="flex cursor-pointer items-center gap-2">
            <input type="checkbox" checked={form.mark} className="h-3.5 w-3.5 accent-ink"
              onChange={e => setForm({ ...form, mark: e.target.checked })} />
            <span className="font-mono text-[11px] text-ink">{t('f_mark')}</span>
            <span className="font-mono text-[10px] text-ink-ghost">{t('f_mark_hint')}</span>
          </label>
        </Panel>

        {/* 主 OD — 单选(跨 OD 由运行时基于 ont_causality 自动 JOIN) */}
        <Panel title={t('ods_primary_title')} hint={t('ods_primary_hint')}>
          <div className="flex items-center gap-1 border border-border bg-white px-1.5 py-1">
            <Search size={10} className="text-ink-ghost flex-shrink-0" />
            <input
              value={odSearch}
              onChange={e => setOdSearch(e.target.value)}
              placeholder={t('ods_search')}
              className="w-full bg-transparent font-mono text-[10px] text-ink outline-none placeholder:text-ink-ghost"
            />
          </div>
          <div className="border border-border max-h-44 overflow-y-auto">
            {pickerOds.length === 0 ? (
              <div className="px-2 py-2 font-mono text-[10px] text-ink-ghost">{t('od_browser_empty')}</div>
            ) : pickerOds.map(o => {
              const checked = o.id === primaryOdId
              return (
                <label key={o.id} className="flex cursor-pointer items-center gap-2 px-2 py-1 hover:bg-canvas-alt">
                  <input type="radio" name="primaryOd" checked={checked} className="h-3 w-3 accent-ink"
                    onChange={() => setPrimaryOd(o.id)} />
                  <span className={`font-mono text-[10px] truncate ${checked ? 'font-semibold text-ink' : 'text-ink-muted'}`}>{o.name}</span>
                  {o.displayName && <span className="font-mono text-[9px] text-ink-ghost truncate">· {o.displayName}</span>}
                </label>
              )
            })}
          </div>
          {!primaryOdId && (
            <span className="inline-flex items-center gap-1 font-mono text-[10px] text-danger">
              <AlertCircle size={11} /> {t('ods_empty_warn')}
            </span>
          )}
        </Panel>

        {/* 触发词 — 必填 */}
        <Panel title={t('triggers')} hint={t('triggers_hint')}>
          <div className="flex items-center gap-1.5">
            <input className={inputCls} value={kwInput} placeholder={t('triggers_ph')}
              onChange={e => setKwInput(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addKeyword() } }} />
            <button onClick={addKeyword} disabled={!kwInput.trim()}
              className="inline-flex items-center gap-1 border border-border px-2 py-1.5 font-mono text-[10px] text-ink-muted hover:border-ink hover:text-ink disabled:opacity-40">
              <Plus size={11} /> {t('add')}
            </button>
          </div>
          <div className="flex flex-wrap gap-1">
            {triggersEmpty ? (
              <span className="inline-flex items-center gap-1 font-mono text-[10px] text-danger">
                <AlertCircle size={11} /> {t('triggers_empty_warn')}
              </span>
            ) : form.triggerKeywords.map((k, i) => (
              <span key={k} className="inline-flex items-center gap-1 border border-ink bg-white px-1.5 py-0.5 font-mono text-[11px] text-ink">
                {k}
                <button onClick={() => removeKeyword(i)} className="text-ink-ghost hover:text-danger">×</button>
              </span>
            ))}
          </div>
        </Panel>
      </div>

      {/* ── RIGHT PANE: bare metric SQL ─────────────────────────────────── */}
      <div className="w-1/2 overflow-y-auto p-5 space-y-4">
        {isLegacySQL && (
          <div className="flex items-start gap-2 border border-warn/40 bg-warn/5 px-3 py-2">
            <AlertTriangle size={14} className="flex-shrink-0 text-warn" />
            <div className="font-mono text-[10px] text-ink-muted leading-relaxed">{t('legacy_sql_banner')}</div>
          </div>
        )}

        {/* 指标 SQL — 只描述指标本身,单 OD,无 token、无 JOIN、无过滤 */}
        <Panel title={t('metric_sql')} hint={t('metric_sql_hint')}>
          <SQLEditor
            value={form.querySql}
            onChange={v => setForm({ ...form, querySql: v })}
            height="220px"
            schema={{}}
          />
          <p className="font-mono text-[10px] text-ink-ghost">{t('metric_sql_note')}</p>
        </Panel>

        {/* Pointer to the standalone simulator (lives on the edit page top bar;
            this hint tells the author where runtime behavior is tested). */}
        <div className="border border-dashed border-border px-3 py-2">
          <p className="font-mono text-[10px] text-ink-ghost leading-relaxed">{t('simulator_pointer')}</p>
        </div>
      </div>
    </div>
  )
}
