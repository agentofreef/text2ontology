'use client'

// DraftCanvas — the right-rail Living Draft surface (industrial only).
//
// Materializes "what the AI is currently building" as a structured artifact.
// Facets light up as tool calls arrive. On commit_card, crystallises into an
// editable metric draft with accept/dryRun flow. No theme variance — explore
// tab is always industrial.
//
// Four states:
//   1. Idle:    "AI 还没开始 — 提个问题"
//   2. Building: facet badges pulse as tool_call events arrive
//   3. Ready:   commit_card payload rendered + accept button
//   4. Accepted: success state with metric link

import { useMemo } from 'react'
import { Sparkles, Database, Layers, Filter, Code2, Tag, MessageSquare, FileText, Link2, Check } from 'lucide-react'
import { CommitCard, type ValidatorRejection } from '@/components/lakehouse-agent/CommitCard'
import type { CommitCardPayload } from '@/components/lakehouse-agent/commitCardLogic'

export type DraftBuildEvent =
  | { kind: 'lookup_od' }
  | { kind: 'smartquery' }
  | { kind: 'other' }

interface Props {
  status: 'idle' | 'building' | 'ready' | 'accepted'
  payload?: CommitCardPayload | null
  events: DraftBuildEvent[]
  acceptedMetricId?: string | null
  onRejection: (r: ValidatorRejection) => void
  onAccepted: (metricId: string) => void
}

interface FacetBadge {
  index: number
  icon: React.ComponentType<{ className?: string }>
  label: string
  hint: string
  match: (e: DraftBuildEvent) => boolean
  filled: (p?: CommitCardPayload | null) => boolean
}

const FACETS: FacetBadge[] = [
  { index: 1, icon: Database, label: '主 OD', hint: '要分析的对象',
    match: (e) => e.kind === 'lookup_od', filled: (p) => !!p?.primaryOd },
  { index: 2, icon: Sparkles, label: '聚合度量', hint: 'canonical_metric',
    match: (e) => e.kind === 'smartquery', filled: (p) => !!p?.canonicalMetric },
  { index: 3, icon: Layers, label: '维度', hint: 'auto_group_by',
    match: (e) => e.kind === 'smartquery', filled: (p) => (p?.autoGroupBy?.length ?? 0) > 0 },
  { index: 4, icon: Filter, label: '参数', hint: 'parameters',
    match: () => false, filled: (p) => (p?.parameters?.length ?? 0) > 0 },
  { index: 5, icon: Code2, label: 'METRIC SQL', hint: 'BARE simple form',
    match: (e) => e.kind === 'smartquery', filled: (p) => !!p?.querySql },
  { index: 6, icon: Tag, label: '召回 KW', hint: 'LLM 怎么找到这条',
    match: () => false, filled: (p) => (p?.triggerKeywords?.length ?? 0) > 0 },
  { index: 7, icon: MessageSquare, label: '回答模板', hint: '出数后怎么说',
    match: () => false, filled: (p) => !!p?.responseTemplate },
  { index: 8, icon: Link2, label: 'Causality', hint: '跨 OD JOIN 路径',
    match: () => false, filled: () => false },
  { index: 9, icon: FileText, label: '释义', hint: 'curator 对齐文本',
    match: () => false, filled: (p) => (p?.description?.length ?? 0) > 10 },
]

export function DraftCanvas({ status, payload, events, acceptedMetricId, onRejection, onAccepted }: Props) {
  const facetState = useMemo(() => {
    return FACETS.map((f) => {
      const hits = events.filter(f.match).length
      const filled = f.filled(payload)
      return { ...f, state: filled ? 'filled' : hits > 0 ? 'active' : 'idle', hits }
    })
  }, [events, payload])

  return (
    <aside className="flex h-full w-[360px] flex-shrink-0 flex-col overflow-hidden border-l-2 border-ink bg-canvas-alt">
      {/* Header */}
      <div className="flex h-12 flex-shrink-0 items-center justify-between border-b-2 border-ink bg-white px-4">
        <div className="flex items-center gap-2">
          <Sparkles className="h-3.5 w-3.5 text-ink" />
          <span className="font-mono text-[11px] tracking-[0.22em] uppercase text-ink">口径草稿</span>
        </div>
        <StatusPill status={status} />
      </div>

      {/* Body */}
      <div className="flex-1 overflow-y-auto px-4 py-4">
        {status === 'idle' && <IdleHero />}

        {status === 'building' && (
          <>
            <div className="mb-3 font-mono text-[10.5px] leading-relaxed tracking-[0.04em] text-ink-muted">
              {payload
                ? <>口径已起草 · 正在执行查询以获取实际数据…</>
                : <>AI 正在探索数据 · 已观测到 <span className="text-ink">{events.length}</span> 次工具调用</>
              }
            </div>
            <FacetGrid facets={facetState} payload={payload || null} />
          </>
        )}

        {status === 'ready' && payload && (
          <div className="space-y-4">
            <FacetGrid facets={facetState} payload={payload} />
            <div className="h-px bg-ink" />
            <CommitCard payload={payload} onRejection={onRejection} onAccepted={onAccepted} />
          </div>
        )}

        {status === 'accepted' && <AcceptedState metricId={acceptedMetricId} payload={payload} />}
      </div>
    </aside>
  )
}

function StatusPill({ status }: { status: Props['status'] }) {
  const map = {
    idle:    { label: '空闲',   cls: 'border-ink text-ink-muted' },
    building:{ label: '进行中', cls: 'border-ink bg-white text-ink animate-pulse' },
    ready:   { label: '待采纳', cls: 'border-ink bg-ink text-white' },
    accepted:{ label: '已采纳', cls: 'border-ink bg-ink text-white' },
  } as const
  const m = map[status]
  return (
    <span className={`inline-flex items-center gap-1 border px-1.5 py-[1px] font-mono text-[10px] tracking-[0.12em] uppercase ${m.cls}`}>
      {m.label}
    </span>
  )
}

function IdleHero() {
  return (
    <div className="flex flex-col items-center gap-3 px-2 py-10 text-center">
      <div className="flex h-12 w-12 items-center justify-center border border-ink bg-white">
        <Sparkles className="h-5 w-5 text-ink" strokeWidth={1.5} />
      </div>
      <div className="font-mono text-[12px] tracking-[0.18em] uppercase text-ink">AI 草稿区</div>
      <p className="max-w-[220px] font-mono text-[10.5px] leading-relaxed text-ink-muted">
        提个问题,AI 开始探索后,这一格会逐渐浮现出 metric 的轮廓
      </p>
    </div>
  )
}

function FacetGrid({ facets, payload }: { facets: ReturnType<typeof useMemo<{ index:number; icon: React.ComponentType<{ className?: string }>; label:string; hint:string; state: string; hits: number }[]>>; payload: CommitCardPayload | null }) {
  return (
    <div className="space-y-1.5">
      {facets.map((f) => {
        const Icon = f.icon
        const value = payload ? facetValue(f.index, payload) : null
        const wrapperCls =
          f.state === 'filled' ? 'border-ink bg-ink text-white' :
          f.state === 'active' ? 'border-ink bg-white text-ink animate-pulse' :
          'border-ink bg-white text-ink-muted'
        const iconBoxCls =
          f.state === 'filled' ? 'bg-white text-ink' :
          f.state === 'active' ? 'bg-canvas-alt text-ink' :
          'bg-canvas-alt text-ink-muted'
        return (
          <div key={f.index} className={`group flex items-start gap-2.5 border px-2.5 py-2 transition-all ${wrapperCls}`}>
            <div className={`mt-[2px] flex h-5 w-5 flex-shrink-0 items-center justify-center ${iconBoxCls}`}>
              {f.state === 'filled' ? <Check className="h-3 w-3" /> : <Icon className="h-3 w-3" />}
            </div>
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline justify-between gap-2">
                <span className="font-mono text-[10.5px] tracking-[0.08em] uppercase">
                  <span className="mr-1 text-[10px] opacity-60">0{f.index}</span>
                  {f.label}
                </span>
                {f.state === 'idle' && (
                  <span className="font-mono text-[9.5px] text-ink-muted">{f.hint}</span>
                )}
              </div>
              {value && (
                <div className="mt-1 truncate font-mono text-[10.5px] opacity-90">
                  {value}
                </div>
              )}
            </div>
          </div>
        )
      })}
    </div>
  )
}

function facetValue(idx: number, p: CommitCardPayload): string | null {
  switch (idx) {
    case 1: return p.primaryOd || null
    case 2: return p.canonicalMetric || null
    case 3: return (p.autoGroupBy && p.autoGroupBy.length > 0) ? p.autoGroupBy.join(', ') : null
    case 4: return (p.parameters && p.parameters.length > 0) ? p.parameters.map(x => x.name).join(', ') : null
    case 5: return p.querySql ? p.querySql.replace(/\s+/g, ' ').slice(0, 60) + (p.querySql.length > 60 ? '…' : '') : null
    case 6: return (p.triggerKeywords && p.triggerKeywords.length > 0) ? p.triggerKeywords.join(' / ') : null
    case 7: return p.responseTemplate ? p.responseTemplate.slice(0, 50) + (p.responseTemplate.length > 50 ? '…' : '') : null
    case 9: return p.description ? p.description.slice(0, 50) + (p.description.length > 50 ? '…' : '') : null
    default: return null
  }
}

function AcceptedState({ metricId, payload }: { metricId?: string | null; payload?: CommitCardPayload | null }) {
  return (
    <div className="flex flex-col items-center gap-3 px-2 py-8 text-center">
      <div className="flex h-12 w-12 items-center justify-center border border-ink bg-ink">
        <Check className="h-6 w-6 text-white" strokeWidth={2} />
      </div>
      <div>
        <div className="font-mono text-[12px] tracking-[0.18em] uppercase text-ink">已采纳为 METRIC</div>
        {payload && (
          <div className="mt-1 font-mono text-[11px] text-ink-muted">
            {payload.name}
          </div>
        )}
      </div>
      <p className="max-w-[240px] font-mono text-[10.5px] leading-relaxed text-ink-muted">
        查询模式可直接召回这条口径 — 你或其他人都能用
      </p>
      {metricId && (
        <a
          href={`./lakehouse-metrics/edit?id=${encodeURIComponent(metricId)}`}
          className="inline-flex items-center gap-1 border border-ink bg-white px-2.5 py-1 font-mono text-[10.5px] tracking-[0.06em] uppercase text-ink hover:bg-ink hover:text-white"
        >
          在 METRIC REGISTRY 中打开
        </a>
      )}
    </div>
  )
}
