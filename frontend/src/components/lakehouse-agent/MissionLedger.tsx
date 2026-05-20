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
import { ChevronDown, ChevronRight, Sparkles, Check, X, CircleSlash, Loader2, AlertTriangle } from 'lucide-react'

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

const MISSION_STYLE: Record<MissionStatus, { ring: string; bg: string; text: string; badge: string; label: string }> = {
  active:       { ring: 'border-sky-300',     bg: 'bg-sky-50/60',     text: 'text-sky-700',     badge: 'bg-sky-100 text-sky-700',         label: '进行中' },
  complete:     { ring: 'border-emerald-300', bg: 'bg-emerald-50/60', text: 'text-emerald-700', badge: 'bg-emerald-100 text-emerald-700', label: '完成' },
  partial:      { ring: 'border-amber-300',   bg: 'bg-amber-50/60',   text: 'text-amber-700',   badge: 'bg-amber-100 text-amber-700',     label: '部分完成' },
  unanswerable: { ring: 'border-rose-300',    bg: 'bg-rose-50/60',    text: 'text-rose-700',    badge: 'bg-rose-100 text-rose-700',       label: '能力缺口' },
}

const TASK_STYLE: Record<TaskStatus, { ring: string; bg: string; text: string; Icon: typeof Check; animate?: string }> = {
  passing:       { ring: 'border-emerald-300', bg: 'bg-emerald-50', text: 'text-emerald-700', Icon: Check },
  active:        { ring: 'border-sky-400',     bg: 'bg-sky-50',     text: 'text-sky-700',     Icon: Loader2, animate: 'animate-spin' },
  blocked:       { ring: 'border-amber-300',   bg: 'bg-amber-50',   text: 'text-amber-700',   Icon: CircleSlash },
  pending:       { ring: 'border-gray-200',    bg: 'bg-gray-50',    text: 'text-gray-500',    Icon: X },
  pending_retry: { ring: 'border-orange-200',  bg: 'bg-orange-50',  text: 'text-orange-600',  Icon: AlertTriangle },
}

// ─── Sub-components ──────────────────────────────────────────────────────────

function TaskRow({ task }: { task: MissionTask }) {
  const s = TASK_STYLE[task.status] || TASK_STYLE.pending
  const Icon = s.Icon
  return (
    <div className={`flex items-start gap-2 px-2 py-1.5 border ${s.ring} ${s.bg} rounded`}>
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
    <div className="border border-rose-300 bg-rose-50 rounded p-2 space-y-1">
      <div className="flex items-center gap-1.5">
        <AlertTriangle size={12} className="text-rose-600" />
        <span className="text-[11px] font-semibold text-rose-700">能力缺口</span>
        <code className="text-[10px] text-rose-600 bg-white/70 px-1 rounded ml-auto">{reason.kind}</code>
      </div>
      {reason.missing_dimension && (
        <div className="text-[11px] text-rose-700">
          需要按【{reason.missing_dimension}】筛选,但没有任何已授权指标提供该维度
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
  metric:    { label: '指标', chip: 'bg-sky-100 text-sky-700' },
  dimension: { label: '维度', chip: 'bg-indigo-100 text-indigo-700' },
  filter:    { label: '筛选', chip: 'bg-amber-100 text-amber-700' },
}

// RequirementRow renders one decomposed element of the question — its
// kind, name, shape, why, and (for dimension/filter) whether an
// authorized Intent reaches it.
function RequirementRow({ r }: { r: RequirementCoverage }) {
  const ks = REQ_KIND_STYLE[r.kind] || { label: r.kind, chip: 'bg-gray-100 text-gray-600' }
  const isMetric = r.kind === 'metric'
  return (
    <div className="border border-gray-200 bg-white rounded px-2 py-1.5 space-y-0.5">
      <div className="flex items-center gap-1.5 flex-wrap">
        <span className={`text-[9px] px-1 py-0.5 rounded font-semibold ${ks.chip}`}>{ks.label}</span>
        <code className="text-[11px] font-semibold text-gray-800">{r.dimension}</code>
        {r.shape && <span className="text-[9px] text-gray-400">· {r.shape}</span>}
        {!isMetric && (
          r.covered
            ? <span className="ml-auto text-[9px] px-1 py-0.5 rounded bg-emerald-100 text-emerald-700 flex items-center gap-0.5"><Check size={9} />可达</span>
            : <span className="ml-auto text-[9px] px-1 py-0.5 rounded bg-rose-100 text-rose-700 flex items-center gap-0.5"><X size={9} />不可达</span>
        )}
      </div>
      {r.why && <div className="text-[10px] text-gray-500 leading-relaxed">{r.why}</div>}
      {!isMetric && (
        <div className="text-[10px]">
          {r.covered
            ? <span className="text-emerald-600">由 {(r.covered_by || []).join('、') || '已授权指标'} 覆盖</span>
            : <span className="text-rose-600">{r.missing_note || '无授权指标覆盖'}</span>}
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
    <div className={`border rounded overflow-hidden ${ok ? 'border-emerald-300' : 'border-rose-300'}`}>
      <div className={`flex items-center gap-1.5 px-2 py-1.5 ${ok ? 'bg-emerald-50' : 'bg-rose-50'}`}>
        {ok ? <Check size={12} className="text-emerald-600" /> : <AlertTriangle size={12} className="text-rose-600" />}
        <span className={`text-[11px] font-semibold ${ok ? 'text-emerald-700' : 'text-rose-700'}`}>
          任务可达器:{ok ? '可行' : '不可行'}
        </span>
      </div>
      <div className={`px-2 py-1 text-[11px] leading-relaxed ${ok ? 'text-emerald-800 bg-emerald-50/50' : 'text-rose-800 bg-rose-50/50'}`}>
        {v.reason}
      </div>
      {reqs.length > 0 && (
        <div className="px-2 py-1.5 space-y-1 bg-white">
          <div className="text-[10px] font-semibold text-gray-500 tracking-wider">问题拆解({reqs.length})</div>
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
      className={`w-full flex items-center gap-2 px-2 py-1.5 border ${ms.ring} ${ms.bg} rounded text-left hover:brightness-95 transition-all cursor-pointer`}
    >
      <Sparkles size={12} className={`${ms.text} shrink-0`} />
      <span className={`text-[11px] font-semibold ${ms.text} flex-1 min-w-0 truncate`}>
        {mission.question.slice(0, 80)}{mission.question.length > 80 ? '…' : ''}
      </span>
      {reach && (
        <span className={`text-[9px] px-1 py-0.5 rounded font-semibold shrink-0 ${reach.feasible ? 'bg-emerald-100 text-emerald-700' : 'bg-rose-100 text-rose-700'}`}>
          {reach.feasible ? '可行' : '不可行'}
        </span>
      )}
      <span className={`text-[10px] px-1.5 py-0.5 rounded font-mono shrink-0 ${ms.badge}`}>{ms.label}</span>
    </button>
  )
}

// MissionModal is the full-screen overlay showing one mission's full
// reachability detail — verdict + decomposition + (if present) tasks +
// synthesis answer + capability gap. Click outside or ✕ to close.
function MissionModal({ mission, onClose }: { mission: Mission; onClose: () => void }) {
  const ms = MISSION_STYLE[mission.status] || MISSION_STYLE.active
  const tasks = mission.tasks || []

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
        className="bg-white rounded-lg shadow-2xl max-w-2xl w-full max-h-[85vh] flex flex-col overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center gap-2 px-4 py-3 border-b border-gray-200 bg-indigo-50/60 shrink-0">
          <Sparkles size={16} className="text-indigo-500" />
          <span className="text-sm font-semibold text-indigo-700 tracking-wide">任务可达器</span>
          <span className={`text-[10px] px-1.5 py-0.5 rounded font-mono ${ms.badge}`}>{ms.label}</span>
          <button
            onClick={onClose}
            className="ml-auto text-gray-400 hover:text-gray-700 transition-colors p-1 rounded hover:bg-gray-100"
            aria-label="关闭"
          >
            <X size={16} />
          </button>
        </div>
        {/* Body */}
        <div className="flex-1 overflow-y-auto p-4 space-y-3">
          <div>
            <div className="text-[10px] font-semibold text-gray-500 tracking-wider mb-1">问题</div>
            <div className="text-[13px] text-gray-800 leading-relaxed">{mission.question}</div>
          </div>
          {mission.reachability && <ReachabilityBlock v={mission.reachability} />}
          {mission.blocked_root && <CapabilityGapBanner reason={mission.blocked_root} />}
          {tasks.length > 0 && (
            <div className="space-y-1">
              <div className="text-[10px] font-semibold text-gray-500 tracking-wider">任务({tasks.length})</div>
              {tasks.map((task) => <TaskRow key={task.id} task={task} />)}
            </div>
          )}
          {mission.synthesis?.output && (
            <div className="border border-emerald-200 bg-emerald-50/30 rounded p-3">
              <div className="text-[10px] text-emerald-700 font-semibold mb-1 tracking-wider">综合答复</div>
              <pre className="text-[12px] text-gray-900 whitespace-pre-wrap font-sans leading-relaxed">{mission.synthesis.output}</pre>
            </div>
          )}
          <div className="text-[10px] text-gray-400 pt-2 border-t border-gray-100">
            <code className="bg-gray-50 px-1 rounded">mission_id={mission.mission_id}</code>
          </div>
        </div>
      </div>
    </div>
  )
}

// ─── Public component ────────────────────────────────────────────────────────

interface MissionLedgerProps {
  missions: Mission[]
  loading?: boolean
}

export function MissionLedger({ missions, loading }: MissionLedgerProps) {
  const [collapsed, setCollapsed] = useState(false)
  const [selected, setSelected] = useState<Mission | null>(null)

  if (loading) {
    return (
      <div className="border-t border-indigo-100 px-4 py-2 flex items-center gap-2 text-[11px] text-gray-400 bg-indigo-50/40">
        <Loader2 size={12} className="animate-spin text-indigo-400" />
        加载 mission…
      </div>
    )
  }

  const list = missions || []

  return (
    <>
      <div className="border-t border-indigo-200 bg-indigo-50/40 flex-shrink-0 flex flex-col min-h-0">
        <button className="w-full flex items-center gap-1.5 px-3 py-1.5 text-left hover:bg-indigo-50/70 transition-colors shrink-0" onClick={() => setCollapsed(v => !v)}>
          {collapsed ? <ChevronRight size={12} className="text-indigo-400" /> : <ChevronDown size={12} className="text-indigo-400" />}
          <Sparkles size={12} className="text-indigo-500" />
          <span className="text-[11px] font-semibold text-indigo-700 tracking-wider">任务可达器</span>
          <span className="text-[10px] text-indigo-400 ml-1">({list.length})</span>
        </button>

        {!collapsed && (
          list.length === 0 ? (
            <div className="px-3 py-3 text-[11px] text-gray-400">当前对话暂无任务记录(mission)。</div>
          ) : (
            <div className="px-3 pb-3 space-y-1.5 overflow-y-auto">
              {list.map(m => (
                <MissionSummary key={m.mission_id} mission={m} onOpen={() => setSelected(m)} />
              ))}
              <div className="text-[10px] text-gray-400 text-center pt-1">点击任一条查看可达性详情</div>
            </div>
          )
        )}
      </div>

      {selected && <MissionModal mission={selected} onClose={() => setSelected(null)} />}
    </>
  )
}
