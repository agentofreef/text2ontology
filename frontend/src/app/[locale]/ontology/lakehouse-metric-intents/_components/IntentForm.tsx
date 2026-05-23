'use client'

// Shared form for "新建" and "编辑" 指标（Metric）. Pure presentational —
// state lives in the route pages so they can wire submit/cancel to their
// own router.push and api calls. Sections mirror the data model:
//
//   1. 基本信息   — name / displayName / objectId / priority / mark
//   2. 口径定义   — canonicalMetric + canonicalFilters + autoGroupBy
//   3. Pivot 配置 — pivotOn + pivotValues + percent axis/scope + total/grand-total
//   4. 文案       — responseTemplate + description
//
// Design language: SV Minimal (docs/design/design-system.md v2)
//   黑/白/灰/绿/红 only · ink-muted labels · subtle border-light section dividers ·
//   Square chips with bg-canvas-alt · No amber/blue/violet decorations.

import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { Input, Textarea } from '@/components/ui/Input'
import type { OntMetricIntentFilter, OntObjectType } from '@/types/api'
import { Filter as FilterIcon, Hash, Layers, X, AlertCircle } from 'lucide-react'

export type IntentForm = {
  name: string
  displayName: string
  objectId: string
  canonicalMetric: string
  canonicalFilters: OntMetricIntentFilter[]
  autoGroupBy: string[]
  pivotOn: string
  pivotValues: string[]
  pivotColumnLabels: string[]
  pivotTotalLabel: string
  pivotPercentAxis: string
  pivotPercentScope: string
  pivotPercentSuffix: string
  pivotWithPercent: boolean
  pivotAppendGrandTotal: boolean
  responseTemplate: string
  description: string
  priority: number
  mark: boolean
}

export const blankIntentForm: IntentForm = {
  name: '', displayName: '', objectId: '', canonicalMetric: '',
  canonicalFilters: [], autoGroupBy: [], pivotOn: '', pivotValues: [], pivotColumnLabels: [],
  pivotTotalLabel: 'Total', pivotPercentAxis: 'row', pivotPercentScope: 'filtered',
  pivotPercentSuffix: '占比', pivotWithPercent: false, pivotAppendGrandTotal: false,
  responseTemplate: '', description: '', priority: 0, mark: true,
}

interface Props {
  form: IntentForm
  setForm: React.Dispatch<React.SetStateAction<IntentForm>>
  objects: OntObjectType[]
  gbInput: string
  setGbInput: (v: string) => void
}

// Section frame — soft border + heading tag, mirrors the sectioned look of
// the sql-passthrough sidebar without being a "Card" component.
function Section({
  title, icon, hint, children,
}: {
  title: string
  icon?: React.ReactNode
  hint?: string
  children: React.ReactNode
}) {
  return (
    <section className="rounded-md border border-border bg-white">
      <header className="flex flex-wrap items-center justify-between gap-2 border-b border-border-light bg-canvas-alt px-4 py-2">
        <div className="flex items-center gap-1.5 text-xs font-semibold tracking-tight text-ink">
          {icon}
          {title}
        </div>
        {hint && <span className="text-[11px] text-ink-ghost">{hint}</span>}
      </header>
      <div className="space-y-4 px-4 py-4">{children}</div>
    </section>
  )
}

export function IntentFormFields({ form, setForm, objects, gbInput, setGbInput }: Props) {
  const reduce = useReducedMotion()

  const addFilter = () =>
    setForm(f => ({ ...f, canonicalFilters: [...f.canonicalFilters, { prop: '', op: '=', value: '' }] }))
  const removeFilter = (i: number) =>
    setForm(f => ({ ...f, canonicalFilters: f.canonicalFilters.filter((_, idx) => idx !== i) }))
  const updateFilter = (i: number, field: keyof OntMetricIntentFilter, value: string) =>
    setForm(f => ({
      ...f,
      canonicalFilters: f.canonicalFilters.map((x, idx) => idx === i ? { ...x, [field]: value } : x),
    }))

  const addGroupBy = () => {
    const v = gbInput.trim()
    if (!v) return
    setForm(f => ({ ...f, autoGroupBy: f.autoGroupBy.includes(v) ? f.autoGroupBy : [...f.autoGroupBy, v] }))
    setGbInput('')
  }
  const removeGroupBy = (i: number) =>
    setForm(f => ({ ...f, autoGroupBy: f.autoGroupBy.filter((_, idx) => idx !== i) }))

  return (
    <div className="space-y-5">
      {/* ── 1. 基本信息 ─────────────────────────────────── */}
      <Section title="基本信息" hint="* 必填">
        <div className="grid grid-cols-2 gap-3">
          <Input
            label="Name (系统标识) *"
            value={form.name}
            onChange={e => setForm({ ...form, name: e.target.value })}
            placeholder="Order.Total"
          />
          <Input
            label="显示名"
            value={form.displayName}
            onChange={e => setForm({ ...form, displayName: e.target.value })}
            placeholder="订单总量口径"
          />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="mb-1 block text-xs font-medium text-ink-muted">归属 Od *</label>
            <select
              className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink outline-none focus:border-ink"
              value={form.objectId}
              onChange={e => setForm({ ...form, objectId: e.target.value })}
            >
              <option value="">选择对象</option>
              {objects.map(o => <option key={o.id} value={o.id}>{o.name}</option>)}
            </select>
            <div className="mt-1 inline-flex items-start gap-1 text-[11px] text-ink-ghost">
              <AlertCircle size={10} className="mt-0.5 flex-shrink-0" aria-hidden="true" />
              <span>每个指标仅可归属单个 Od（暂不支持多 Od 组合触发）</span>
            </div>
          </div>
          <Input
            label="Priority"
            type="number"
            value={String(form.priority)}
            onChange={e => setForm({ ...form, priority: parseInt(e.target.value) || 0 })}
            placeholder="0"
          />
        </div>
        <label className="inline-flex cursor-pointer items-center gap-2">
          <input
            type="checkbox"
            checked={form.mark}
            onChange={e => setForm({ ...form, mark: e.target.checked })}
            className="h-3.5 w-3.5 accent-ink"
          />
          <span className="text-sm text-ink">启用（mark）</span>
          <span className="text-[11px] text-ink-ghost">— 关闭后 recall 会跳过此指标</span>
        </label>
      </Section>

      {/* ── 2. 口径定义 ─────────────────────────────────── */}
      <Section title="口径定义" icon={<FilterIcon size={11} className="text-ink-muted" aria-hidden="true" />}>
        <Input
          label="Canonical Metric *"
          value={form.canonicalMetric}
          onChange={e => setForm({ ...form, canonicalMetric: e.target.value })}
          placeholder="sum(Order_Quantity)"
        />

        {/* Filters */}
        <div>
          <div className="mb-1.5 flex items-center justify-between">
            <label className="text-xs font-medium text-ink-muted">Canonical Filters</label>
            <motion.button
              type="button"
              onClick={addFilter}
              whileHover={reduce ? undefined : { x: 1 }}
              whileTap={reduce ? undefined : { scale: 0.97 }}
              transition={{ duration: 0.12 }}
              className="cursor-pointer text-xs text-ink-muted underline outline-none focus-visible:ring-1 focus-visible:ring-ink"
            >
              + 添加
            </motion.button>
          </div>
          {form.canonicalFilters.length === 0 ? (
            <div className="rounded border border-dashed border-border px-3 py-2 text-xs text-ink-ghost">
              无过滤条件 — Intent 仅按 auto_group_by 分组
            </div>
          ) : (
            <AnimatePresence initial={false}>
              {form.canonicalFilters.map((f, i) => (
                <motion.div
                  key={i}
                  layout
                  initial={reduce ? undefined : { opacity: 0 }}
                  animate={reduce ? undefined : { opacity: 1 }}
                  exit={reduce ? undefined : { opacity: 0 }}
                  transition={{ duration: 0.12 }}
                  className="mb-1 flex gap-1"
                >
                  <input
                    className="flex-1 rounded-md border border-border bg-white px-2 py-1 text-sm text-ink outline-none focus:border-ink"
                    placeholder="prop"
                    value={f.prop}
                    onChange={e => updateFilter(i, 'prop', e.target.value)}
                  />
                  <select
                    className="w-16 rounded-md border border-border bg-white px-1 py-1 text-sm text-ink outline-none focus:border-ink"
                    value={f.op}
                    onChange={e => updateFilter(i, 'op', e.target.value)}
                  >
                    <option value="=">=</option><option value="!=">!=</option>
                    <option value=">">&gt;</option><option value=">=">&gt;=</option>
                    <option value="<">&lt;</option><option value="<=">&lt;=</option>
                    <option value="in">in</option><option value="like">like</option>
                  </select>
                  <input
                    className="flex-1 rounded-md border border-border bg-white px-2 py-1 text-sm text-ink outline-none focus:border-ink"
                    placeholder="value"
                    value={f.value}
                    onChange={e => updateFilter(i, 'value', e.target.value)}
                  />
                  <motion.button
                    type="button"
                    onClick={() => removeFilter(i)}
                    whileHover={reduce ? undefined : { scale: 1.05 }}
                    whileTap={reduce ? undefined : { scale: 0.95 }}
                    transition={{ duration: 0.12 }}
                    aria-label="移除过滤"
                    className="cursor-pointer rounded-md border border-border px-2 text-ink-ghost outline-none hover:border-danger hover:text-danger focus-visible:ring-1 focus-visible:ring-ink"
                  >
                    <X className="h-3 w-3" aria-hidden="true" />
                  </motion.button>
                </motion.div>
              ))}
            </AnimatePresence>
          )}
        </div>

        {/* Auto GroupBy */}
        <div>
          <label className="mb-1.5 flex items-center gap-1.5 text-xs font-medium text-ink-muted">
            <Layers size={11} aria-hidden="true" />
            Auto GroupBy
            <span className="font-normal text-ink-ghost">— 指标触发时强制注入这些列</span>
          </label>
          <div className="mb-1.5 flex gap-1">
            <input
              className="flex-1 rounded-md border border-border bg-white px-2 py-1 text-sm text-ink outline-none focus:border-ink"
              placeholder="列名（回车添加）"
              value={gbInput}
              onChange={e => setGbInput(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addGroupBy() } }}
            />
            <motion.button
              type="button"
              onClick={addGroupBy}
              whileHover={reduce ? undefined : { scale: 1.03 }}
              whileTap={reduce ? undefined : { scale: 0.95 }}
              transition={{ duration: 0.12 }}
              aria-label="添加 group by"
              className="cursor-pointer rounded-md border border-border px-3 text-sm text-ink-muted outline-none hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
            >
              +
            </motion.button>
          </div>
          <div className="flex flex-wrap gap-1">
            {form.autoGroupBy.length === 0 ? (
              <span className="text-xs text-ink-ghost">—</span>
            ) : (
              <AnimatePresence initial={false}>
                {form.autoGroupBy.map((g, i) => (
                  <motion.span
                    key={g}
                    layout
                    initial={reduce ? undefined : { opacity: 0, scale: 0.9 }}
                    animate={reduce ? undefined : { opacity: 1, scale: 1 }}
                    exit={reduce ? undefined : { opacity: 0, scale: 0.9 }}
                    transition={{ duration: 0.12 }}
                    className="inline-flex items-center gap-1 rounded-md border border-border bg-canvas-alt px-1.5 py-0.5 font-mono text-xs text-ink-muted"
                  >
                    {g}
                    <motion.button
                      type="button"
                      onClick={() => removeGroupBy(i)}
                      whileHover={reduce ? undefined : { scale: 1.2 }}
                      whileTap={reduce ? undefined : { scale: 0.9 }}
                      transition={{ duration: 0.12 }}
                      aria-label="移除"
                      className="cursor-pointer text-ink-ghost outline-none hover:text-danger focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      ×
                    </motion.button>
                  </motion.span>
                ))}
              </AnimatePresence>
            )}
          </div>
        </div>
      </Section>

      {/* ── 3. Pivot 配置 ─────────────────────────────────── */}
      <Section
        title="Pivot 配置"
        icon={<Hash size={11} className="text-ink-muted" aria-hidden="true" />}
        hint="留空 Pivot On 即不做 pivot"
      >
        <div className="grid grid-cols-2 gap-3">
          <Input
            label="Pivot On"
            value={form.pivotOn}
            onChange={e => setForm({ ...form, pivotOn: e.target.value })}
            placeholder="e.g. Order_Type"
          />
          <Input
            label="Total Label"
            value={form.pivotTotalLabel}
            onChange={e => setForm({ ...form, pivotTotalLabel: e.target.value })}
            placeholder="Total"
          />
        </div>

        <AnimatePresence initial={false}>
          {form.pivotOn && (
            <motion.div
              key="pivot-extra"
              initial={reduce ? undefined : { opacity: 0, height: 0 }}
              animate={reduce ? undefined : { opacity: 1, height: 'auto' }}
              exit={reduce ? undefined : { opacity: 0, height: 0 }}
              transition={{ duration: 0.2 }}
              className="space-y-3 overflow-hidden"
            >
              <div className="grid grid-cols-3 gap-3">
                <div>
                  <label className="mb-1 block text-[11px] font-medium text-ink-muted">Percent Axis</label>
                  <select
                    className="w-full rounded-md border border-border bg-white px-2 py-1.5 text-sm text-ink outline-none focus:border-ink"
                    value={form.pivotPercentAxis}
                    onChange={e => setForm({ ...form, pivotPercentAxis: e.target.value })}
                  >
                    <option value="row">row · 本行比例</option>
                    <option value="column">column · 跨行份额</option>
                  </select>
                </div>
                <div>
                  <label className="mb-1 block text-[11px] font-medium text-ink-muted">Percent Scope</label>
                  <select
                    className="w-full rounded-md border border-border bg-white px-2 py-1.5 text-sm text-ink outline-none focus:border-ink"
                    value={form.pivotPercentScope}
                    onChange={e => setForm({ ...form, pivotPercentScope: e.target.value })}
                  >
                    <option value="filtered">filtered（默认）</option>
                    <option value="global">global · 忽略用户过滤</option>
                  </select>
                </div>
                <div>
                  <label className="mb-1 block text-[11px] font-medium text-ink-muted">Percent Suffix</label>
                  <input
                    className="w-full rounded-md border border-border bg-white px-2 py-1.5 text-sm text-ink outline-none focus:border-ink"
                    value={form.pivotPercentSuffix}
                    onChange={e => setForm({ ...form, pivotPercentSuffix: e.target.value })}
                    placeholder="占比"
                  />
                </div>
              </div>

              <div>
                <label className="mb-1 block text-[11px] font-medium text-ink-muted">Pivot Values（逗号分隔）</label>
                <input
                  className="w-full rounded-md border border-border bg-white px-2 py-1.5 text-sm text-ink outline-none focus:border-ink"
                  value={form.pivotValues.join(', ')}
                  onChange={e => setForm({
                    ...form,
                    pivotValues: e.target.value.split(',').map(s => s.trim()).filter(Boolean),
                  })}
                  placeholder="未转换的Real Order, Real Order"
                />
              </div>
              <div>
                <label className="mb-1 block text-[11px] font-medium text-ink-muted">Pivot Column Labels（逗号分隔）</label>
                <input
                  className="w-full rounded-md border border-border bg-white px-2 py-1.5 text-sm text-ink outline-none focus:border-ink"
                  value={form.pivotColumnLabels.join(', ')}
                  onChange={e => setForm({
                    ...form,
                    pivotColumnLabels: e.target.value.split(',').map(s => s.trim()).filter(Boolean),
                  })}
                  placeholder="留空则使用 Pivot Values"
                />
              </div>

              <div className="flex flex-wrap gap-x-6 gap-y-2 pt-1">
                <label className="inline-flex cursor-pointer items-center gap-2">
                  <input
                    type="checkbox"
                    checked={form.pivotWithPercent}
                    onChange={e => setForm({ ...form, pivotWithPercent: e.target.checked })}
                    className="h-3.5 w-3.5 accent-ink"
                  />
                  <span className="text-sm text-ink">每个值追加占比列</span>
                </label>
                <label className="inline-flex cursor-pointer items-center gap-2">
                  <input
                    type="checkbox"
                    checked={form.pivotAppendGrandTotal}
                    onChange={e => setForm({ ...form, pivotAppendGrandTotal: e.target.checked })}
                    className="h-3.5 w-3.5 accent-ink"
                  />
                  <span className="text-sm text-ink">追加合计行</span>
                </label>
              </div>
            </motion.div>
          )}
        </AnimatePresence>
      </Section>

      {/* ── 4. 文案 ─────────────────────────────────── */}
      <Section title="文案与说明">
        <Textarea
          label="Response Template"
          value={form.responseTemplate}
          onChange={e => setForm({ ...form, responseTemplate: e.target.value })}
          placeholder="共 {total} pcs，其中 {real} 已转 Real Order"
        />
        <Textarea
          label="Description"
          value={form.description}
          onChange={e => setForm({ ...form, description: e.target.value })}
          placeholder="说明触发词、使用场景..."
        />
      </Section>
    </div>
  )
}

// validateIntentForm — single source of truth for "is this submittable?"
// Both the Save button (disabled state) and the submit handler use this so
// users can't sneak past the button by hitting Enter.
export function validateIntentForm(f: IntentForm): string | null {
  if (!f.name.trim()) return '请填写 Name'
  if (!f.canonicalMetric.trim()) return '请填写 Canonical Metric'
  if (!f.objectId) return '请选择归属 Od'
  return null
}
