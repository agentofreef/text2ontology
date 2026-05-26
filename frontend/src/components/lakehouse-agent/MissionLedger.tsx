'use client'

// MissionLedger renders a read-only view of the missions the lakehouse
// agent persisted for a thread (MissionAct M4, .omc/specs/mission-act.md).
//
// API source: GET /api/ontology/lakehouse-missions?thread_id={uuid}
//   returns { missions: Mission[] } where each Mission mirrors the JSON
//   tags of pkg/mission.Mission — snake_case throughout.
//
// Trivial-mission carve-out (spec §4.3): a mission with ≤ 1 task and
// no synthesis renders as a compact one-liner so the legacy single-query
// UI stays visually unchanged.

import { useState, useEffect } from 'react'
import { ChevronDown, ChevronRight, Check, X, CircleSlash, Loader2, AlertTriangle } from 'lucide-react'

// ─── API shapes (match the JSON tags of pkg/mission Go types) ───────────────

export type MissionStatus = 'active' | 'complete' | 'partial' | 'unanswerable'
export type TaskStatus = 'pending' | 'active' | 'passing' | 'blocked' | 'pending_retry'

export interface MissionTask {
  id: string
  type: string
  behavior: string
  status: TaskStatus
  result_ref?: string
  evidence?: { tool?: string; result_summary?: string; reasoning?: string }
  blocked_reason?: BlockedReason
}

export interface CandidateCheck {
  intent_name: string
  params_summary?: string
  why_insufficient?: string
}

export interface BlockedReason {
  kind: 'no_param' | 'shape_unsupported' | 'no_data'
  missing_dimension?: string
  candidates_checked?: CandidateCheck[]
  suggested_fix?: string
}

// ReachabilityVerdict — the 任务可达器 judgment. The headline of every
// mission: can the system answer this question from authorized data.
export interface RequirementCoverage {
  dimension: string
  kind: string // metric | dimension | filter
  shape?: string
  why?: string
  covered: boolean
  covered_by?: string[]
  missing_note?: string
}

export interface ReachabilityVerdict {
  feasible: boolean
  requirements?: RequirementCoverage[]
  reason: string
}

export interface Mission {
  mission_id: string
  thread_id: string
  project_id?: string
  parent_mission_id?: string
  question: string
  status: MissionStatus
  reachability?: ReachabilityVerdict
  tasks?: MissionTask[]
  synthesis?: { template?: string; caveats?: string[]; output?: string; closest_reachable?: string }
  blocked_root?: BlockedReason
  created_at?: string
  updated_at?: string
}

export interface MissionListResponse {
  missions: Mission[]
}

// ─── Style maps ──────────────────────────────────────────────────────────────

// Industrial palette: slate/zinc neutrals, the #FF4500 join-key orange as the
// single "live/active" accent (matches OntologyGraph), square 1px borders,
// mono micro-labels. Status colors stay semantic but desaturated.
const MISSION_STYLE: Record<MissionStatus, { ring: string; bg: string; text: string; badge: string; label: string }> = {
  active:       { ring: 'border-[#FF4500]/60', bg: 'bg-[#FF4500]/[0.04]', text: 'text-[#c2410c]',  badge: 'bg-[#FF4500]/10 text-[#c2410c] border border-[#FF4500]/30', label: '进行中' },
  complete:     { ring: 'border-emerald-500/50', bg: 'bg-emerald-50/40', text: 'text-emerald-700', badge: 'bg-emerald-100 text-emerald-800 border border-emerald-300', label: '完成' },
  partial:      { ring: 'border-amber-500/50',   bg: 'bg-amber-50/40',   text: 'text-amber-700',   badge: 'bg-amber-100 text-amber-800 border border-amber-300',     label: '部分完成' },
  unanswerable: { ring: 'border-red-500/50',     bg: 'bg-red-50/40',     text: 'text-red-700',     badge: 'bg-red-100 text-red-800 border border-red-300',           label: '能力缺口' },
}

const TASK_STYLE: Record<TaskStatus, { ring: string; bg: string; text: string; Icon: typeof Check; animate?: string }> = {
  passing:       { ring: 'border-emerald-500/40', bg: 'bg-emerald-50/50', text: 'text-emerald-700', Icon: Check },
  active:        { ring: 'border-[#FF4500]/50',   bg: 'bg-[#FF4500]/[0.04]', text: 'text-[#c2410c]', Icon: Loader2, animate: 'animate-spin' },
  blocked:       { ring: 'border-red-400/50',     bg: 'bg-red-50/50',     text: 'text-red-700',     Icon: CircleSlash },
  pending:       { ring: 'border-gray-300',       bg: 'bg-gray-50',       text: 'text-gray-400',    Icon: X },
  pending_retry: { ring: 'border-amber-400/50',   bg: 'bg-amber-50/50',   text: 'text-amber-700',   Icon: AlertTriangle },
}

// ─── Sub-components ──────────────────────────────────────────────────────────

function TaskRow({ task }: { task: MissionTask }) {
  const s = TASK_STYLE[task.status] || TASK_STYLE.pending
  const Icon = s.Icon
  return (
    <div className={`flex items-start gap-2 px-2 py-1.5 border ${s.ring} ${s.bg} rounded-sm`}>
      <Icon size={12} className={`${s.text} ${s.animate ?? ''} mt-0.5 shrink-0`} />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-1.5 flex-wrap">
          <code className="text-[10px] text-gray-500 bg-white/70 px-1 rounded">{task.id}</code>
          <span className={`text-[11px] font-semibold ${s.text}`}>{task.behavior || task.type}</span>
          {task.type && task.behavior && task.type !== task.behavior && (
            <span className="text-[10px] text-gray-400 bg-white/70 px-1 rounded font-mono">{task.type}</span>
          )}
        </div>
        {task.evidence?.result_summary && (
          <div className="text-[10px] text-gray-500 mt-0.5">{task.evidence.result_summary}</div>
        )}
        {task.blocked_reason && (
          <div className="text-[10px] text-amber-700 mt-0.5">
            <code className="bg-white/70 px-1 rounded mr-1">{task.blocked_reason.kind}</code>
            {task.blocked_reason.missing_dimension && <>缺失:{task.blocked_reason.missing_dimension}</>}
          </div>
        )}
      </div>
    </div>
  )
}

function CapabilityGapBanner({ reason }: { reason: BlockedReason }) {
  return (
    <div className="border border-red-400/50 border-l-[3px] border-l-red-600 bg-red-50 rounded-sm p-2 space-y-1">
      <div className="flex items-center gap-1.5">
        <AlertTriangle size={12} className="text-red-600" />
        <span className="text-[11px] font-mono font-semibold tracking-wide text-red-700">能力缺口</span>
        <code className="text-[10px] text-red-700 bg-white/70 px-1 rounded-sm ml-auto font-mono">{reason.kind}</code>
      </div>
      {reason.missing_dimension && (
        <div className="text-[11px] text-red-700">
          需要按【{reason.missing_dimension}】筛选,但没有任何已授权口径提供该维度
        </div>
      )}
      {reason.candidates_checked && reason.candidates_checked.length > 0 && (
        <ul className="text-[10px] text-gray-600 list-disc list-inside space-y-0.5">
          {reason.candidates_checked.map((c, i) => (
            <li key={i}>
              <code className="text-gray-700">{c.intent_name}</code>
              {c.params_summary && <>:{c.params_summary}</>}
              {c.why_insufficient && <> —— {c.why_insufficient}</>}
            </li>
          ))}
        </ul>
      )}
      {reason.suggested_fix && (
        <div className="text-[10px] text-gray-600">
          <span className="font-semibold">修复方向:</span> {reason.suggested_fix}
        </div>
      )}
    </div>
  )
}

// ReachabilityBlock is the headline of a mission card — the 任务可达器
// verdict: can the question be answered from authorized data, and why.
const REQ_KIND_STYLE: Record<string, { label: string; chip: string }> = {
  metric:    { label: '口径', chip: 'bg-slate-700 text-white' },
  dimension: { label: '维度', chip: 'bg-slate-200 text-slate-700 border border-slate-300' },
  filter:    { label: '筛选', chip: 'bg-amber-100 text-amber-800 border border-amber-300' },
}

// RequirementRow renders one decomposed element of the question — its
// kind, name, shape, why, and (for dimension/filter) whether an
// authorized Intent reaches it.
function RequirementRow({ r }: { r: RequirementCoverage }) {
  const ks = REQ_KIND_STYLE[r.kind] || { label: r.kind, chip: 'bg-gray-100 text-gray-600' }
  const isMetric = r.kind === 'metric'
  return (
    <div className="border border-gray-200 bg-white rounded-sm px-2 py-1.5 space-y-0.5">
      <div className="flex items-center gap-1.5 flex-wrap">
        <span className={`text-[9px] px-1 py-0.5 rounded-sm font-mono font-semibold tracking-wide ${ks.chip}`}>{ks.label}</span>
        <code className="text-[11px] font-mono font-semibold text-gray-900">{r.dimension}</code>
        {r.shape && <span className="text-[9px] text-gray-400 font-mono">· {r.shape}</span>}
        {!isMetric && (
          r.covered
            ? <span className="ml-auto text-[9px] px-1 py-0.5 rounded-sm bg-emerald-100 text-emerald-800 border border-emerald-300 font-mono flex items-center gap-0.5"><Check size={9} />可达</span>
            : <span className="ml-auto text-[9px] px-1 py-0.5 rounded-sm bg-red-100 text-red-800 border border-red-300 font-mono flex items-center gap-0.5"><X size={9} />不可达</span>
        )}
      </div>
      {r.why && <div className="text-[10px] text-gray-500 leading-relaxed">{r.why}</div>}
      {!isMetric && (
        <div className="text-[10px]">
          {r.covered
            ? <span className="text-emerald-700">由 {(r.covered_by || []).join('、') || '已授权口径'} 覆盖</span>
            : <span className="text-red-700">{r.missing_note || '无授权口径覆盖'}</span>}
        </div>
      )}
    </div>
  )
}

// ReachabilityBlock is the headline of a mission card — the 任务可达器
// verdict plus the full question decomposition that produced it.
function ReachabilityBlock({ v }: { v: ReachabilityVerdict }) {
  const ok = v.feasible
  const reqs = v.requirements || []
  return (
    <div className={`border rounded-sm overflow-hidden border-l-[3px] ${ok ? 'border-emerald-500/50 border-l-emerald-500' : 'border-red-500/50 border-l-red-600'}`}>
      <div className={`flex items-center gap-1.5 px-2 py-1.5 ${ok ? 'bg-emerald-50' : 'bg-red-50'}`}>
        {ok ? <Check size={12} className="text-emerald-600" /> : <AlertTriangle size={12} className="text-red-600" />}
        <span className={`text-[11px] font-mono font-semibold tracking-wide ${ok ? 'text-emerald-700' : 'text-red-700'}`}>
          任务可达器 · {ok ? '可行' : '不可行'}
        </span>
      </div>
      <div className={`px-2 py-1 text-[11px] leading-relaxed ${ok ? 'text-emerald-800 bg-emerald-50/40' : 'text-red-800 bg-red-50/40'}`}>
        {v.reason}
      </div>
      {reqs.length > 0 && (
        <div className="px-2 py-1.5 space-y-1 bg-white">
          <div className="text-[9px] font-mono font-semibold text-gray-400 uppercase tracking-[0.15em]">问题拆解 ({reqs.length})</div>
          {reqs.map((r, i) => <RequirementRow key={i} r={r} />)}
        </div>
      )}
    </div>
  )
}

// MissionSummary is the compact row shown in the panel — click to open
// the detail modal. Headline only: question + reachability verdict
// badge (when present) + status.
function MissionSummary({ mission, onOpen }: { mission: Mission; onOpen: () => void }) {
  const ms = MISSION_STYLE[mission.status] || MISSION_STYLE.active
  const reach = mission.reachability
  return (
    <button
      onClick={onOpen}
      className={`w-full flex items-center gap-2 px-2 py-1.5 border-l-2 ${ms.ring} ${ms.bg} border border-gray-200 rounded-sm text-left hover:bg-gray-50 transition-colors cursor-pointer`}
    >
      <span className={`text-[11px] ${ms.text} flex-1 min-w-0 truncate`}>
        {mission.question.slice(0, 80)}{mission.question.length > 80 ? '…' : ''}
      </span>
      {reach && (
        <span className={`text-[9px] px-1 py-0.5 rounded-sm font-mono font-semibold shrink-0 border ${reach.feasible ? 'bg-emerald-100 text-emerald-800 border-emerald-300' : 'bg-red-100 text-red-800 border-red-300'}`}>
          {reach.feasible ? '可行' : '不可行'}
        </span>
      )}
      <span className={`text-[9px] px-1.5 py-0.5 rounded-sm font-mono shrink-0 ${ms.badge}`}>{ms.label}</span>
    </button>
  )
}

// MissionDetailBody renders one mission's full detail — question, the
// 任务可达器 verdict + decomposition, capability gap, the sub-question task
// list, and the synthesis answer. Shared by the inline panel view and the
// history modal so the two never drift.
function MissionDetailBody({ mission, tokens }: { mission: Mission; tokens?: QuestionToken[] }) {
  const tasks = mission.tasks || []
  return (
    <div className="space-y-3">
      <div>
        <div className="text-[9px] font-mono font-semibold text-gray-400 uppercase tracking-[0.15em] mb-1">问题</div>
        <div className="text-[13px] text-gray-800 leading-relaxed">{mission.question}</div>
      </div>
      {/* 分词 — the LLM's tokenization of this question, shown right under it. */}
      {tokens && tokens.length > 0 && (
        <div>
          <div className="text-[9px] font-mono font-semibold text-gray-400 uppercase tracking-[0.15em] mb-1">分词</div>
          <div className="flex flex-wrap gap-1">
            {tokens.map((tk, i) => (
              <span
                key={i}
                title={tk.strongHit ? 'STRONG' : 'WEAK'}
                className={`rounded-sm border px-1.5 py-0.5 font-mono text-[11px] ${
                  tk.strongHit ? 'border-gray-700 bg-gray-700 text-white' : 'border-gray-300 bg-gray-50 text-gray-600'
                }`}
              >
                {tk.token}
              </span>
            ))}
          </div>
        </div>
      )}
      {mission.reachability && <ReachabilityBlock v={mission.reachability} />}
      {mission.blocked_root && <CapabilityGapBanner reason={mission.blocked_root} />}
      {tasks.length > 0 && (
        <div className="space-y-1">
          <div className="text-[9px] font-mono font-semibold text-gray-400 uppercase tracking-[0.15em]">任务 ({tasks.length})</div>
          {tasks.map((task) => <TaskRow key={task.id} task={task} />)}
        </div>
      )}
      {mission.synthesis?.output && (
        <div className="border border-emerald-300 border-l-[3px] border-l-emerald-500 bg-emerald-50/40 rounded-sm p-3">
          <div className="text-[9px] text-emerald-700 font-mono font-semibold mb-1 uppercase tracking-[0.15em]">综合答复</div>
          <pre className="text-[12px] text-gray-900 whitespace-pre-wrap font-sans leading-relaxed">{mission.synthesis.output}</pre>
        </div>
      )}
      <div className="text-[10px] text-gray-400 pt-2 border-t border-gray-100">
        <code className="bg-gray-50 px-1 rounded-sm font-mono">mission_id={mission.mission_id}</code>
      </div>
    </div>
  )
}

// MissionModal is the full-screen overlay showing one (history) mission's
// full detail. Click outside or ✕ to close.
function MissionModal({ mission, onClose }: { mission: Mission; onClose: () => void }) {
  const ms = MISSION_STYLE[mission.status] || MISSION_STYLE.active

  // ESC to close.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div
      className="fixed inset-0 z-[60] flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
    >
      <div
        className="bg-white rounded-sm shadow-2xl border border-gray-300 max-w-2xl w-full max-h-[85vh] flex flex-col overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center gap-2 px-4 py-3 border-b border-gray-200 bg-gray-100 shrink-0">
          <span className="inline-block w-2 h-2 bg-[#FF4500] shrink-0" />
          <span className="text-sm font-mono font-semibold text-gray-700 uppercase tracking-[0.15em]">任务可达器</span>
          <span className={`text-[10px] px-1.5 py-0.5 rounded-sm font-mono ${ms.badge}`}>{ms.label}</span>
          <button
            onClick={onClose}
            className="ml-auto text-gray-400 hover:text-gray-700 transition-colors p-1 rounded hover:bg-gray-100"
            aria-label="关闭"
          >
            <X size={16} />
          </button>
        </div>
        {/* Body */}
        <div className="flex-1 overflow-y-auto p-4">
          <MissionDetailBody mission={mission} />
        </div>
      </div>
    </div>
  )
}

// ─── Public component ────────────────────────────────────────────────────────

// QuestionToken is the current question's 分词 (tokenization), shown under the
// question inside the live mission card. strongHit mirrors the recall ledger.
export interface QuestionToken {
  token: string
  strongHit?: boolean
}

interface MissionLedgerProps {
  missions: Mission[]
  loading?: boolean
  // The current question's tokenization, rendered under the question of the
  // newest (inline) mission. Optional — history missions don't carry it.
  tokens?: QuestionToken[]
  // Called when the user clicks a mission to open the modal — gives the
  // parent a chance to refetch so the modal sees the freshest reachability /
  // synthesis instead of the snapshot taken at threadId-mount time.
  onRefresh?: () => void | Promise<void>
}

export function MissionLedger({ missions, loading, tokens, onRefresh }: MissionLedgerProps) {
  const [collapsed, setCollapsed] = useState(false)
  // Track by id, not object, so the modal sees the freshest mission
  // version each render (the parent refetches as the turn progresses).
  const [selectedId, setSelectedId] = useState<string | null>(null)

  if (loading) {
    return (
      <div className="border-t border-gray-300 px-4 py-2 flex items-center gap-2 text-[11px] text-gray-400 bg-gray-50">
        <Loader2 size={12} className="animate-spin text-[#FF4500]" />
        加载 mission…
      </div>
    )
  }

  const list = missions || []
  // Newest-first (API orders by created_at DESC), so list[0] is the current
  // turn's mission — shown inline in full detail. The rest are history,
  // rendered as compact rows that open the modal.
  const current = list.length > 0 ? list[0] : null
  const history = list.slice(1)
  const selected = selectedId ? (list.find(m => m.mission_id === selectedId) || null) : null

  return (
    <>
      <div className="border-t border-gray-300 bg-gray-50 flex-1 min-h-0 flex flex-col">
        <button className="w-full flex items-center gap-1.5 px-3 py-1.5 text-left bg-gray-100 border-b border-gray-200 hover:bg-gray-200/60 transition-colors shrink-0" onClick={() => setCollapsed(v => !v)}>
          {collapsed ? <ChevronRight size={12} className="text-gray-400" /> : <ChevronDown size={12} className="text-gray-400" />}
          <span className="inline-block w-1.5 h-1.5 bg-[#FF4500] shrink-0" />
          <span className="text-[11px] font-mono font-semibold text-gray-700 uppercase tracking-[0.15em]">任务可达器</span>
          <span className="text-[10px] text-gray-400 ml-1 font-mono">({list.length})</span>
        </button>

        {!collapsed && (
          list.length === 0 ? (
            <div className="px-3 py-3 text-[11px] text-gray-400">当前对话暂无任务记录(mission)。</div>
          ) : (
            <div className="flex-1 min-h-0 overflow-y-auto px-3 py-3 space-y-2">
              {/* Current mission — full detail inline. */}
              {current && (
                <div className="bg-white border border-gray-200 rounded-sm p-2.5 shadow-sm">
                  <MissionDetailBody mission={current} tokens={tokens} />
                </div>
              )}
              {/* History — compact rows, click to open the modal. */}
              {history.length > 0 && (
                <div className="space-y-1.5 pt-1">
                  <div className="text-[9px] font-mono font-semibold text-gray-400 uppercase tracking-[0.15em]">历史 ({history.length})</div>
                  {history.map(m => (
                    <MissionSummary
                      key={m.mission_id}
                      mission={m}
                      onOpen={() => {
                        if (onRefresh) void onRefresh()
                        setSelectedId(m.mission_id)
                      }}
                    />
                  ))}
                </div>
              )}
            </div>
          )
        )}
      </div>

      {selected && <MissionModal mission={selected} onClose={() => setSelectedId(null)} />}
    </>
  )
}
