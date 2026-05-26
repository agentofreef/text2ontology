'use client'

// Function Call 模拟器 — standalone smartquery playground for a SAVED metric.
//
// Reached from the metric edit page (?id=<metricId>). The metric LOCKS the
// function call's `intent` (= metric name), `odName` (= primary OD) and the
// measure — shown read-only. The user freely configures the rest of the
// smartquery surface and sees the engine's assembled SQL + result.
//
// smartquery function-call surface (handler_agent_lakehouse.go:927):
//   odName* (locked) · intent (locked) · filters[{property,op,value}] ·
//   groupBy[] · orderBy[{label,dir}] · limit
// Three-state per property row: value set = filter + dimension; value empty =
// dimension only; row absent = not involved. A property on another OD
// ("OD.Column") triggers the engine's cross-OD JOIN. A view toggle shows the
// exact function-call JSON behind the friendly form.

import { useState, useEffect, useMemo, Suspense } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { useSearchParams } from 'next/navigation'
import { ChevronLeft, FlaskConical, Loader2, Play, Plus, Trash2, Code2, Table2 } from 'lucide-react'
import { SQLEditor } from '@/components/ui/SQLEditor'
import { ResultViewer } from '@/components/ui/ResultViewer'
import { useFetchSingle, useFetch } from '@/lib/hooks'
import { useProject } from '@/lib/project'
import { useMessage } from '@/lib/message'
import { api } from '@/lib/api'
import type { OntMetric, OntObjectType, OntProperty } from '@/types/api'

const OPS = ['=', '!=', '>', '<', '>=', '<=', 'in', 'not_in', 'like', 'between'] as const
// Base input/select classes — width is NOT baked in. In Tailwind v4 a baked-in
// `w-full` would generate after `w-16`/`w-20`/`w-32` in the stylesheet and
// override every explicit width (causing the property-row op select to stretch
// across the whole row and push the value input + badge off-screen). Width is
// applied at each call site via `w-N` or `flex-N`.
const inputCls = 'border border-border bg-canvas px-2 py-1.5 font-mono text-xs text-ink outline-none placeholder:text-ink-ghost focus:border-ink'

type PropRow = { od: string; prop: string; op: string; value: string }
type OrderRow = { label: string; dir: 'ASC' | 'DESC' }

function Panel({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="border border-border">
      <div className="border-b border-border bg-canvas-alt px-3 py-1.5">
        <span className="font-mono text-[9px] font-semibold tracking-wider text-ink-ghost">
          {title}
          {hint && <span className="ml-2 font-normal normal-case text-ink-ghost">{hint}</span>}
        </span>
      </div>
      <div className="space-y-2 p-3">{children}</div>
    </div>
  )
}

function SimulatorInner() {
  const t = useTranslations('metric.simulator')
  const router = useRouter()
  const searchParams = useSearchParams()
  const id = searchParams.get('id') || ''
  const { currentProject } = useProject()
  const msg = useMessage()

  const { data: metric, loading } = useFetchSingle<OntMetric>(
    id ? `/ontology/lakehouse-metrics/${id}` : '/ontology/lakehouse-metrics/__none__',
  )
  const { data: objects } = useFetch<OntObjectType>('/ontology/objects')

  // Locked function-call fields (from the metric).
  const lockedOd = metric?.objectName || ''
  const lockedMeasure = metric?.canonicalMetric || ''
  const lockedIntent = metric?.name || ''

  // ── OD column catalog (lazy). odName → column names. ──
  const [odCols, setOdCols] = useState<Record<string, string[]>>({})
  useEffect(() => {
    if (!objects || !currentProject) return
    let cancelled = false
    void (async () => {
      for (const o of objects) {
        if (odCols[o.name] !== undefined) continue
        try {
          const full = await api<OntObjectType>(`/ontology/objects/${o.id}?projectId=${currentProject.id}`)
          if (cancelled) return
          setOdCols(prev => ({ ...prev, [o.name]: (full?.properties ?? []).map((p: OntProperty) => p.name) }))
        } catch {
          if (!cancelled) setOdCols(prev => ({ ...prev, [o.name]: [] }))
        }
      }
    })()
    return () => { cancelled = true }
  }, [objects, currentProject]) // eslint-disable-line react-hooks/exhaustive-deps

  const odNames = useMemo(() => (objects || []).map(o => o.name), [objects])

  // ── Config state ──
  const [rows, setRows] = useState<PropRow[]>([])
  const [orders, setOrders] = useState<OrderRow[]>([])
  const [limit, setLimit] = useState('')
  const [view, setView] = useState<'form' | 'json'>('form')
  const [running, setRunning] = useState(false)
  const [sql, setSql] = useState('')
  const [resultRows, setResultRows] = useState<string | null>(null)
  const [err, setErr] = useState<string | null>(null)

  // Seed the first row with the primary OD once the metric loads.
  useEffect(() => {
    if (lockedOd && rows.length === 0) setRows([{ od: lockedOd, prop: '', op: '=', value: '' }])
  }, [lockedOd]) // eslint-disable-line react-hooks/exhaustive-deps

  // Qualified property ref: bare for the primary OD, "OD.Column" for others.
  const qualified = (r: PropRow) => (r.od && r.od !== lockedOd ? `${r.od}.${r.prop}` : r.prop)

  const updRow = (i: number, patch: Partial<PropRow>) =>
    setRows(rs => rs.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))
  const addRow = () => setRows(rs => [...rs, { od: lockedOd, prop: '', op: '=', value: '' }])
  const delRow = (i: number) => setRows(rs => rs.filter((_, idx) => idx !== i))

  const addOrder = () => setOrders(os => [...os, { label: '', dir: 'DESC' }])
  const updOrder = (i: number, patch: Partial<OrderRow>) =>
    setOrders(os => os.map((o, idx) => (idx === i ? { ...o, ...patch } : o)))
  const delOrder = (i: number) => setOrders(os => os.filter((_, idx) => idx !== i))

  // ── Function-call payload (exactly mirrors the smartquery args) ──
  const payload = useMemo(() => {
    const filters = rows.filter(r => r.prop && r.value.trim())
      .map(r => ({ property: qualified(r), op: r.op, value: r.value }))
    const groupBy = rows.filter(r => r.prop && !r.value.trim()).map(r => qualified(r))
    const orderBy = orders.filter(o => o.label.trim()).map(o => ({ label: o.label.trim(), dir: o.dir }))
    const out: Record<string, unknown> = { intent: lockedIntent, odName: lockedOd, filters, groupBy, orderBy }
    if (limit.trim()) out.limit = parseInt(limit, 10) || undefined
    return out
  }, [rows, orders, limit, lockedIntent, lockedOd]) // eslint-disable-line react-hooks/exhaustive-deps

  const run = async () => {
    if (!currentProject || !metric) return
    setErr(null); setSql(''); setResultRows(null); setRunning(true)
    try {
      const pFilters = payload.filters as Array<{ property: string; op: string; value: string }>
      const res = await api<{ ok: boolean; sql: string; rows: unknown[]; rowCount: number; error?: string }>(
        '/ontology/lakehouse-metric-preview',
        {
          method: 'POST',
          body: {
            projectId: currentProject.id,
            level: metric.level === 'sql' ? 'sql' : 'simple',
            odName: lockedOd,
            querySql: metric.querySql || '',
            canonicalMetric: lockedMeasure,
            // Valued filters keep their op via canonicalFilters; dimension-only
            // columns go through groupBy; both feed the same assembly path.
            canonicalFilters: pFilters.map(f => ({ prop: f.property, op: f.op, value: f.value })),
            autoGroupBy: metric.autoGroupBy || [],
            parameters: metric.parameters || [],
            groupBy: payload.groupBy,
            orderBy: payload.orderBy,
            defaultLimit: limit.trim() ? (parseInt(limit, 10) || 0) : 0,
            sampleParams: {},
          },
        },
      )
      setSql(res.sql || '')
      if (!res.ok) { setErr(res.error || t('run_failed')); setResultRows(null) }
      else {
        setResultRows(JSON.stringify(Array.isArray(res.rows) ? res.rows : []))
        msg.success(t('run_ok', { rows: res.rowCount ?? 0 }))
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : t('run_failed'))
    } finally {
      setRunning(false)
    }
  }

  const goBack = () => router.push(`/ontology/lakehouse-metrics/edit?id=${id}`)

  if (loading && !metric) {
    return (
      <div className="flex h-full items-center justify-center bg-canvas">
        <span className="inline-flex items-center gap-2 text-sm text-ink-muted">
          <Loader2 size={14} className="animate-spin" /> {t('loading')}
        </span>
      </div>
    )
  }
  if (!metric) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 bg-canvas">
        <span className="text-sm text-ink-muted">{t('not_found')}</span>
        <button onClick={() => router.push('/ontology/lakehouse-metrics')}
          className="text-xs text-ink underline">{t('back_list')}</button>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col bg-canvas">
      {/* Top bar */}
      <div className="flex flex-shrink-0 items-center justify-between border-b border-border bg-white px-6 py-3">
        <div className="flex min-w-0 items-center gap-3">
          <button onClick={goBack} className="inline-flex items-center gap-1 text-xs text-ink-ghost hover:text-ink">
            <ChevronLeft size={12} /> {t('back_edit')}
          </button>
          <span className="text-ink-ghost">·</span>
          <FlaskConical size={16} className="text-ink flex-shrink-0" />
          <h1 className="truncate font-display text-base font-bold tracking-tight text-ink">
            {t('page_title')} <span className="font-mono text-ink-muted">{lockedIntent}</span>
          </h1>
        </div>
        {/* View toggle — `h-8` matches the edit page's top-bar controls so the
            bar lines up with the sidebar LOGO. */}
        <div className="flex h-8 items-center border border-ink">
          {(['form', 'json'] as const).map(v => (
            <button key={v} onClick={() => setView(v)}
              className={`inline-flex h-full items-center gap-1 px-3 font-mono text-[10px] uppercase tracking-wider ${
                view === v ? 'bg-ink text-white' : 'bg-white text-ink-muted hover:text-ink'}`}>
              {v === 'form' ? <Table2 size={12} /> : <Code2 size={12} />}
              {v === 'form' ? t('view_form') : t('view_json')}
            </button>
          ))}
        </div>
      </div>

      <div className="flex flex-1 overflow-hidden">
        {/* LEFT: config (form or JSON) */}
        <div className="w-1/2 space-y-4 overflow-y-auto border-r border-border p-5">
          {/* Locked function-call header */}
          <div className="grid grid-cols-3 gap-2">
            {[
              [t('locked_intent'), lockedIntent],
              [t('locked_od'), lockedOd],
              [t('locked_measure'), lockedMeasure],
            ].map(([label, val]) => (
              <div key={label} className="min-w-0 border border-border bg-canvas-alt px-2 py-1">
                <span className="font-mono text-[8px] uppercase tracking-wider text-ink-ghost">{label}</span>
                <div className="truncate font-mono text-[11px] text-ink" title={val}>{val || '—'}</div>
              </div>
            ))}
          </div>

          {view === 'form' ? (
            <>
              {/* ① Property rows — each row is a self-contained card (two lines)
                  so nothing overflows the half-width pane. Line 1 = which column
                  (OD . property); line 2 = op + value, where an empty value means
                  dimension-only and a value means filter+dimension. */}
              <Panel title={t('props_title')} hint={t('props_hint')}>
                {rows.map((r, i) => {
                  const cols = odCols[r.od] || []
                  const hasValue = !!r.value.trim()
                  return (
                    <div key={i} className="space-y-1 border border-border bg-white p-2">
                      {/* line 1: OD . property (+ status badge + delete) */}
                      <div className="flex items-center gap-1">
                        <select className={`${inputCls} min-w-0 flex-1`} value={r.od}
                          onChange={e => updRow(i, { od: e.target.value, prop: '' })}>
                          {odNames.map(n => <option key={n} value={n}>{n}{n === lockedOd ? ` (${t('primary')})` : ''}</option>)}
                        </select>
                        <span className="flex-shrink-0 font-mono text-[11px] text-ink-ghost">.</span>
                        <select className={`${inputCls} min-w-0 flex-[2]`} value={r.prop}
                          onChange={e => updRow(i, { prop: e.target.value })}>
                          <option value="">{t('pick_prop')}</option>
                          {cols.map(c => <option key={c} value={c}>{c}</option>)}
                        </select>
                        <button onClick={() => delRow(i)} aria-label={t('remove')}
                          className="flex-shrink-0 border border-border px-1.5 py-1.5 text-ink-ghost hover:border-danger hover:text-danger">
                          <Trash2 size={11} />
                        </button>
                      </div>
                      {/* line 2: op + value + state badge. op is irrelevant when no
                          value (dimension-only), so it greys out. */}
                      <div className="flex items-center gap-1">
                        <select disabled={!hasValue} value={r.op}
                          className={`${inputCls} w-16 flex-shrink-0 disabled:opacity-40`}
                          onChange={e => updRow(i, { op: e.target.value })}>
                          {OPS.map(op => <option key={op} value={op}>{op}</option>)}
                        </select>
                        <input className={`${inputCls} min-w-0 flex-1`} placeholder={t('val_ph')} value={r.value}
                          onChange={e => updRow(i, { value: e.target.value })} />
                        <span className={`flex-shrink-0 whitespace-nowrap border px-1.5 py-1 font-mono text-[9px] ${
                          hasValue ? 'border-ink bg-ink text-white' : 'border-border text-ink-ghost'}`}>
                          {hasValue ? t('badge_filter') : t('badge_dim')}
                        </span>
                      </div>
                    </div>
                  )
                })}
                <button onClick={addRow} className="inline-flex items-center gap-1 font-mono text-[9px] text-accent hover:underline">
                  <Plus size={10} /> {t('add_prop')}
                </button>
                <p className="font-mono text-[9px] text-ink-ghost">{t('props_note')}</p>
              </Panel>

              {/* ② orderBy */}
              <Panel title={t('order_title')} hint={t('order_hint')}>
                {orders.length === 0 && <p className="font-mono text-[10px] text-ink-ghost">{t('order_empty')}</p>}
                {orders.map((o, i) => (
                  <div key={i} className="flex items-center gap-1">
                    <input className={`${inputCls} flex-1`} placeholder={t('order_label_ph')} value={o.label}
                      onChange={e => updOrder(i, { label: e.target.value })} />
                    <select className={`${inputCls} w-20 flex-shrink-0`} value={o.dir}
                      onChange={e => updOrder(i, { dir: e.target.value as 'ASC' | 'DESC' })}>
                      <option value="DESC">DESC</option>
                      <option value="ASC">ASC</option>
                    </select>
                    <button onClick={() => delOrder(i)} aria-label={t('remove')}
                      className="border border-border px-1.5 py-1.5 text-ink-ghost hover:border-danger hover:text-danger">
                      <Trash2 size={11} />
                    </button>
                  </div>
                ))}
                <button onClick={addOrder} className="inline-flex items-center gap-1 font-mono text-[9px] text-accent hover:underline">
                  <Plus size={10} /> {t('add_order')}
                </button>
              </Panel>

              {/* ③ limit */}
              <Panel title={t('limit_title')}>
                <input className={`${inputCls} w-32`} type="number" min={1} placeholder={t('limit_ph')}
                  value={limit} onChange={e => setLimit(e.target.value)} />
              </Panel>
            </>
          ) : (
            // JSON view (read-only mirror of the exact function-call payload)
            <Panel title={t('json_title')} hint={t('json_hint')}>
              <pre className="overflow-x-auto border border-border bg-canvas-alt p-2 font-mono text-[10px] leading-relaxed text-ink-muted">
{JSON.stringify(payload, null, 2)}
              </pre>
            </Panel>
          )}

          <button onClick={run} disabled={running || !currentProject}
            className="inline-flex items-center gap-1.5 border border-ink bg-ink px-4 py-2 font-mono text-[11px] uppercase tracking-wider text-white hover:bg-ink/90 disabled:opacity-40">
            {running ? <Loader2 size={13} className="animate-spin" /> : <Play size={13} />}
            {running ? t('running') : t('run')}
          </button>
        </div>

        {/* RIGHT: assembled SQL + result */}
        <div className="w-1/2 space-y-4 overflow-y-auto p-5">
          <Panel title={t('assembled_sql')}>
            {sql
              ? <SQLEditor value={sql} onChange={() => {}} height="220px" schema={{}} readOnly />
              : <p className="font-mono text-[10px] text-ink-ghost">{t('sql_empty')}</p>}
          </Panel>
          <Panel title={t('result')}>
            {err && <div className="border border-danger/30 bg-danger/5 px-2 py-1.5 font-mono text-[10px] text-danger">{err}</div>}
            {resultRows ? <ResultViewer data={resultRows} /> : !err && <p className="font-mono text-[10px] text-ink-ghost">{t('result_empty')}</p>}
          </Panel>
        </div>
      </div>
    </div>
  )
}

export default function SimulatorPage() {
  return (
    <Suspense fallback={
      <div className="flex h-full items-center justify-center bg-canvas">
        <span className="inline-flex items-center gap-2 text-sm text-ink-muted"><Loader2 size={14} className="animate-spin" /> …</span>
      </div>
    }>
      <SimulatorInner />
    </Suspense>
  )
}
