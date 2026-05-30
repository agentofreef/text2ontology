'use client'

// Tool call chip — industrial-only renderer for an AI tool invocation.
// Three regions:
//   ┌─ HEADER ─ icon + verb + status pill ─────────────────────────┐
//   │ INPUT  · per-tool summary + raw JSON                         │
//   │ OUTPUT · per-tool summary + raw JSON                         │
//   └───────────────────────────────────────────────────────────────┘
//
// Per the explore-tab style directive, this chip is always industrial:
// ink borders, canvas-alt headers, monospace labels, sharp corners.
// Both INPUT and OUTPUT show the raw JSON below the structured summary
// so the user can inspect the actual data — not just a pretty preview.

import { Search, Database, Sparkles, Check, Loader2, X, AlertCircle } from 'lucide-react'

export type ToolCallStatus = 'running' | 'ok' | 'error'

interface Props {
  name: string
  arguments?: unknown
  result?: unknown
  status?: ToolCallStatus
  // When true, the chip renders with a thicker / inverted border so the user
  // can see at a glance which chip is driving the right-rail Inspector.
  selected?: boolean
  // Click anywhere on the chip body to select it for the Inspector.
  // The expand-JSON <details> still works because we don't preventDefault.
  onClick?: () => void
}

const TOOL_META: Record<string, { icon: React.ComponentType<{ className?: string }>; verb: string }> = {
  lookup: { icon: Search, verb: '探查 OD' },
  lookup_od: { icon: Search, verb: '探查 OD' },
  inspect: { icon: Search, verb: '探查数据' },
  smartquery: { icon: Database, verb: '执行查询' },
  execute_smartquery: { icon: Database, verb: '执行查询' },
  commit_card: { icon: Sparkles, verb: '汇总成草稿' },
}

export function ToolCallChip({ name, arguments: args, result, status = 'ok', selected = false, onClick }: Props) {
  const meta = TOOL_META[name] || { icon: Database, verb: '调用' }
  const Icon = meta.icon
  const isLookup = name === 'lookup' || name === 'lookup_od'
  const isSmartQuery = name === 'smartquery' || name === 'execute_smartquery'

  // Selected = right-rail Inspector is showing this chip. Thicker border +
  // canvas-alt fill so the link between chat and right rail is visible.
  const wrapperCls = selected
    ? 'mt-1 first:mt-0 border-2 border-ink bg-canvas-alt'
    : 'mt-1 first:mt-0 border border-ink bg-white'

  return (
    <div
      onClick={onClick}
      className={`${wrapperCls} ${onClick ? 'cursor-pointer hover:bg-canvas-alt' : ''}`}
      role={onClick ? 'button' : undefined}
      tabIndex={onClick ? 0 : undefined}
      onKeyDown={onClick ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onClick() } } : undefined}
    >
      {/* Header */}
      <div className="flex items-center gap-2 border-b border-ink bg-canvas-alt px-3 py-1.5">
        <Icon className="h-3.5 w-3.5 flex-shrink-0 text-ink" />
        <span className="font-mono text-[11px] tracking-[0.18em] uppercase text-ink">
          {meta.verb} <span className="text-ink-muted">· {name}</span>
        </span>
        <StatusPill status={status} />
      </div>

      <div className="flex flex-col divide-y divide-ink">
        <Section label="INPUT">
          {isLookup ? <LookupInput args={args} />
            : isSmartQuery ? <SmartQueryInput args={args} />
            : <GenericFormatted value={args} fallback="无参数" />}
          <JsonView value={args} />
        </Section>

        {status !== 'running' && (
          <Section label="OUTPUT">
            {status === 'error' ? <ErrorOutput result={result} />
              : isLookup ? <LookupOutput result={result} />
              : isSmartQuery ? <SmartQueryOutput result={result} />
              : <GenericFormatted value={result} fallback="—" />}
            <JsonView value={result} />
          </Section>
        )}

        {status === 'running' && (
          <div className="px-3 py-2 font-mono text-[11px] text-ink-muted">
            <Loader2 className="mr-1.5 inline h-3 w-3 animate-spin" />
            等待返回…
          </div>
        )}
      </div>
    </div>
  )
}

function StatusPill({ status }: { status: ToolCallStatus }) {
  const base = 'ml-auto inline-flex items-center gap-1 border px-1.5 py-[1px] font-mono text-[10px] tracking-[0.12em] uppercase'
  if (status === 'running') {
    return (
      <span className={`${base} border-ink text-ink`}>
        <Loader2 className="h-2.5 w-2.5 animate-spin" />
        进行中
      </span>
    )
  }
  if (status === 'error') {
    // Failure is the one place strict-ink is broken: red signals risk and
    // is universally legible across the industrial palette. Per product
    // directive: 失败应该是红色的.
    return (
      <span className={`${base} border-red-600 bg-red-600 text-white`}>
        <X className="h-2.5 w-2.5" />
        失败
      </span>
    )
  }
  return (
    <span className={`${base} border-ink bg-ink text-white`}>
      <Check className="h-2.5 w-2.5" />
      已完成
    </span>
  )
}

function Section({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="px-3 py-2">
      <div className="mb-1 font-mono text-[9.5px] tracking-[0.22em] text-ink-muted">
        {label}
      </div>
      <div className="space-y-2 text-[12px] leading-relaxed">
        {children}
      </div>
    </div>
  )
}

// ── INPUT renderers ───────────────────────────────────────────────────────

function LookupInput({ args }: { args: unknown }) {
  const a = (args || {}) as Record<string, unknown>
  const query = (a.query || a.keyword || a.q || a.name || a.ontology_name || '') as string
  const odHint = (a.ontology_name || a.od || a.odName || '') as string
  if (!query && !odHint) return <Empty>—</Empty>
  return (
    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
      <Label>关键词</Label>
      <Strong>{query || '(无)'}</Strong>
      {odHint && odHint !== query && (
        <>
          <Label>范围</Label>
          <code className="font-mono text-[11px] text-ink">{odHint}</code>
        </>
      )}
    </div>
  )
}

function SmartQueryInput({ args }: { args: unknown }) {
  const a = (args || {}) as Record<string, unknown>
  const metric = (a.metric || a.metricName || a.name || '') as string
  const od = (a.od || a.ontology_name || a.primaryOd || '') as string
  return (
    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
      {metric && (<><Label>口径</Label><Strong>{metric}</Strong></>)}
      {od && (<><Label>OD</Label><code className="font-mono text-[11px] text-ink">{od}</code></>)}
      {!metric && !od && <Empty>—</Empty>}
    </div>
  )
}

// ── OUTPUT renderers ──────────────────────────────────────────────────────

interface ODBrief { name?: string; displayName?: string; display_name?: string; properties?: unknown }
interface KWBrief { label?: string; keyword?: string; display_name?: string; name?: string }

function LookupOutput({ result }: { result: unknown }) {
  const r = (result || {}) as Record<string, unknown>
  const ods = (r.ods || r.objectDefinitions || r.candidates || r.results || []) as ODBrief[]
  const kws = (r.keywords || r.intents || []) as KWBrief[]
  if (!Array.isArray(ods) && !Array.isArray(kws)) {
    return <GenericFormatted value={result} fallback="无匹配" />
  }
  const hasOds = Array.isArray(ods) && ods.length > 0
  const hasKws = Array.isArray(kws) && kws.length > 0
  if (!hasOds && !hasKws) return <Empty>无匹配</Empty>
  return (
    <div className="space-y-2">
      {hasOds && (
        <div>
          <Label>命中 OD · {ods.length}</Label>
          <div className="mt-1 flex flex-wrap gap-1.5">
            {ods.slice(0, 12).map((o, i) => {
              const nm = (o.name || o.displayName || o.display_name || '?').toString()
              const propCount = Array.isArray(o.properties) ? o.properties.length : null
              return <Pill key={i}>{nm}{propCount != null && <span className="ml-1 text-ink-muted">·{propCount}</span>}</Pill>
            })}
            {ods.length > 12 && <span className="font-mono text-[10px] text-ink-muted">…+{ods.length - 12}</span>}
          </div>
        </div>
      )}
      {hasKws && (
        <div>
          <Label>命中关键词 · {kws.length}</Label>
          <div className="mt-1 flex flex-wrap gap-1.5">
            {kws.slice(0, 16).map((k, i) => {
              const lbl = (k.label || k.keyword || k.display_name || k.name || '?').toString()
              return <Pill key={i} variant="accent">{lbl}</Pill>
            })}
            {kws.length > 16 && <span className="font-mono text-[10px] text-ink-muted">…+{kws.length - 16}</span>}
          </div>
        </div>
      )}
    </div>
  )
}

function SmartQueryOutput({ result }: { result: unknown }) {
  const r = (result || {}) as Record<string, unknown>
  if (r.error || (typeof r.code === 'string' && r.code && r.code !== 'OK')) {
    return <ErrorOutput result={result} />
  }
  const rows = (r.rows || r.data || []) as Record<string, unknown>[]
  const cols = (r.columns || (Array.isArray(rows) && rows[0] ? Object.keys(rows[0]) : [])) as string[]
  const rowCount = typeof r.rowCount === 'number' ? r.rowCount : (Array.isArray(rows) ? rows.length : 0)

  if (!Array.isArray(rows) || rows.length === 0) {
    return <Empty>0 行</Empty>
  }
  const previewRows = rows.slice(0, 5)
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline gap-2">
        <Label>{rowCount} 行</Label>
        {rows.length > previewRows.length && <span className="font-mono text-[10px] text-ink-muted">· 预览前 {previewRows.length}</span>}
      </div>
      <div className="overflow-x-auto border border-ink">
        <table className="w-full border-separate border-spacing-0 text-[11.5px]">
          <thead>
            <tr>
              {cols.map((c) => (
                <th key={c} className="border-b border-ink bg-canvas-alt px-2 py-1 text-left font-mono text-[10px] text-ink-muted">{c}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {previewRows.map((row, i) => (
              <tr key={i}>
                {cols.map((c) => (
                  <td key={c} className="border-b border-ink px-2 py-1 font-mono text-[11px] tabular-nums text-ink">
                    {formatCell(row[c])}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function ErrorOutput({ result }: { result: unknown }) {
  const r = (result || {}) as Record<string, unknown>
  const msg = (r.error || r.message || r.code || '工具返回错误') as string
  return (
    <div className="flex items-start gap-1.5 border border-red-600 bg-red-50 px-2 py-1.5 font-mono text-[11px] text-red-700">
      <AlertCircle className="mt-[2px] h-3 w-3 flex-shrink-0" />
      <span className="break-all">{msg}</span>
    </div>
  )
}

function GenericFormatted({ value, fallback }: { value: unknown; fallback: string }) {
  if (value == null) return <Empty>{fallback}</Empty>
  if (typeof value === 'string') return <span className="font-mono text-[11.5px] text-ink">{value}</span>
  // For an object, fall through to the JSON view alone (no second formatted box).
  return null
}

// ── JSON view ─────────────────────────────────────────────────────────────

function JsonView({ value }: { value: unknown }) {
  if (value === undefined) return null
  const text = safeStringify(value)
  // Collapsed by default — structured view above tells the story; JSON is
  // the "show me the raw bytes" affordance for power users / debugging.
  // The summary triangle flips via [open] selector.
  return (
    <details className="group">
      <summary className="cursor-pointer select-none font-mono text-[9.5px] tracking-[0.18em] uppercase text-ink-muted hover:text-ink">
        <span className="mr-1 inline-block w-3 text-center transition-transform group-open:rotate-90">▸</span>
        <span className="hidden group-open:inline">隐藏</span>
        <span className="group-open:hidden">展开</span>
        {' '}JSON · {byteSize(text)} bytes
      </summary>
      <pre className="mt-1 max-h-[280px] overflow-auto border border-ink bg-canvas-alt px-2 py-1.5 font-mono text-[10.5px] leading-[1.5] text-ink">
        <code>{highlightJson(text)}</code>
      </pre>
    </details>
  )
}

// Lightweight tokenizer: walks the JSON string and wraps strings / numbers /
// keywords in tinted spans. Avoids pulling in a syntax-highlight library.
function highlightJson(text: string): React.ReactNode[] {
  const parts: React.ReactNode[] = []
  const re = /"((?:[^"\\]|\\.)*)"(\s*:)?|\b(true|false|null)\b|-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b/g
  let last = 0
  let m: RegExpExecArray | null
  let k = 0
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) parts.push(text.slice(last, m.index))
    if (m[1] !== undefined) {
      // String literal — if followed by colon, treat as key (different shade).
      const isKey = !!m[2]
      parts.push(
        <span key={k++} className={isKey ? 'text-ink' : 'text-ink-muted'}>
          {m[0]}
        </span>
      )
    } else if (m[3] !== undefined) {
      parts.push(<span key={k++} className="text-ink font-semibold">{m[0]}</span>)
    } else {
      parts.push(<span key={k++} className="text-ink font-semibold">{m[0]}</span>)
    }
    last = m.index + m[0].length
  }
  if (last < text.length) parts.push(text.slice(last))
  return parts
}

function byteSize(s: string): number {
  // Char-count approximation — good enough for an indicator pill.
  return s.length
}

// ── primitives ────────────────────────────────────────────────────────────

function Label({ children }: { children: React.ReactNode }) {
  return <span className="font-mono text-[9.5px] tracking-[0.18em] uppercase text-ink-muted">{children}</span>
}

function Strong({ children }: { children: React.ReactNode }) {
  return <span className="font-mono text-[12px] text-ink">{children}</span>
}

function Pill({ variant = 'plain', children }: { variant?: 'plain' | 'accent'; children: React.ReactNode }) {
  const cls = variant === 'accent'
    ? 'inline-flex items-center border border-ink bg-ink px-1.5 py-[1px] font-mono text-[10px] text-white'
    : 'inline-flex items-center border border-ink bg-white px-1.5 py-[1px] font-mono text-[10px] text-ink'
  return <span className={cls}>{children}</span>
}

function Empty({ children }: { children: React.ReactNode }) {
  return <span className="font-mono text-[11px] text-ink-muted">{children}</span>
}

function safeStringify(v: unknown): string {
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

function formatCell(v: unknown): string {
  if (v === null || v === undefined) return '—'
  if (typeof v === 'number') {
    if (Number.isInteger(v)) return v.toLocaleString()
    return v.toLocaleString(undefined, { maximumFractionDigits: 2 })
  }
  if (typeof v === 'string') {
    const n = Number(v)
    if (!Number.isNaN(n) && v.trim() !== '' && /^-?\d/.test(v)) {
      if (Number.isInteger(n)) return n.toLocaleString()
      return n.toLocaleString(undefined, { maximumFractionDigits: 2 })
    }
    return v
  }
  return String(v)
}
