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

import { useState } from 'react'
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

export interface Mission {
  mission_id: string
  thread_id: string
  project_id?: string
  parent_mission_id?: string
  question: string
  status: MissionStatus
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

function MissionCard({ mission }: { mission: Mission }) {
  const [open, setOpen] = useState(false)
  const ms = MISSION_STYLE[mission.status] || MISSION_STYLE.active
  const tasks = mission.tasks || []

  // Trivial-mission carve-out: ≤ 1 task with no synthesis → compact line.
  const isTrivial = tasks.length <= 1 && !mission.synthesis?.output && !mission.blocked_root
  if (isTrivial) {
    return (
      <div className={`flex items-center gap-2 px-2 py-1.5 border ${ms.ring} ${ms.bg} rounded text-[11px]`}>
        <Sparkles size={12} className={ms.text} />
        <span className={`font-semibold ${ms.text} truncate`}>{mission.question.slice(0, 80)}{mission.question.length > 80 ? '…' : ''}</span>
        <span className={`ml-auto text-[10px] px-1.5 py-0.5 rounded font-mono ${ms.badge}`}>{ms.label}</span>
      </div>
    )
  }

  const passingCount = tasks.filter(t => t.status === 'passing').length
  const blockedCount = tasks.filter(t => t.status === 'blocked').length
  const activeCount  = tasks.filter(t => t.status === 'active').length
  const pendingCount = tasks.filter(t => t.status === 'pending' || t.status === 'pending_retry').length

  return (
    <div className={`border ${ms.ring} ${ms.bg} rounded`}>
      <button className="w-full flex items-center gap-2 px-2.5 py-1.5 text-left" onClick={() => setOpen(v => !v)}>
        {open ? <ChevronDown size={12} className="text-gray-400 shrink-0" /> : <ChevronRight size={12} className="text-gray-400 shrink-0" />}
        <Sparkles size={12} className={`${ms.text} shrink-0`} />
        <span className={`text-[11px] font-semibold ${ms.text} flex-1 min-w-0 truncate`}>
          {mission.question.slice(0, 80)}{mission.question.length > 80 ? '…' : ''}
        </span>
        <span className={`text-[10px] px-1.5 py-0.5 rounded font-mono shrink-0 ${ms.badge}`}>{ms.label}</span>
        {tasks.length > 0 && (
          <span className="flex items-center gap-1 shrink-0">
            {passingCount > 0 && <span className="text-[10px] px-1 rounded bg-emerald-100 text-emerald-700 font-mono">{passingCount}✓</span>}
            {blockedCount > 0 && <span className="text-[10px] px-1 rounded bg-amber-100 text-amber-700 font-mono">{blockedCount}!</span>}
            {activeCount  > 0 && <span className="text-[10px] px-1 rounded bg-sky-100 text-sky-700 font-mono">{activeCount}▶</span>}
            {pendingCount > 0 && <span className="text-[10px] px-1 rounded bg-gray-100 text-gray-500 font-mono">{pendingCount}·</span>}
          </span>
        )}
      </button>

      {open && (
        <div className="px-2.5 pb-2 space-y-1.5 border-t border-white/50">
          {mission.blocked_root && <div className="pt-1.5"><CapabilityGapBanner reason={mission.blocked_root} /></div>}
          {tasks.length > 0 && (
            <div className="space-y-1 pt-1.5">
              {tasks.map(task => <TaskRow key={task.id} task={task} />)}
            </div>
          )}
          {mission.synthesis?.output && (
            <div className="border border-emerald-200 bg-white rounded p-2 mt-1">
              <div className="text-[10px] text-emerald-700 font-semibold mb-1">综合</div>
              <pre className="text-[11px] text-gray-900 whitespace-pre-wrap font-sans leading-relaxed">{mission.synthesis.output}</pre>
            </div>
          )}
          <div className="text-[10px] text-gray-400 pt-0.5">
            <code className="bg-white/70 px-1 rounded">{mission.mission_id.slice(0, 8)}…</code>
          </div>
        </div>
      )}
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

  if (loading) {
    return (
      <div className="border-t border-indigo-100 px-4 py-2 flex items-center gap-2 text-[11px] text-gray-400 bg-indigo-50/40">
        <Loader2 size={12} className="animate-spin text-indigo-400" />
        加载 mission…
      </div>
    )
  }

  const list = missions || []

  // The panel is always present (it occupies the left column's lower
  // slot), so an empty list renders an empty-state line rather than
  // returning null.
  return (
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
            {list.map(m => <MissionCard key={m.mission_id} mission={m} />)}
          </div>
        )
      )}
    </div>
  )
}
