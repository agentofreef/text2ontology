'use client'

// MissionLedger renders a read-only view of ONE mission from the ont_mission
// table. Consumed by the lakehouse-agent page as a collapsible panel below
// the chat input.
//
// Spec: .omc/specs/plan-from-ontology-knowledge.md §9 (MissionAct M4).
//
// API source: GET /api/ontology/lakehouse-missions?thread_id={uuid}
//   returns MissionListResponse = { missions: MissionView[] }
// Each MissionView carries the full mission JSON plus derived counts.

import { useTranslations } from 'next-intl'
import { ChevronDown, ChevronRight, Sparkles, Check, X, CircleSlash, Loader2, AlertTriangle } from 'lucide-react'
import { useState } from 'react'

// ─── API shapes (mirrors pkg/mission JSON tags) ─────────────────────────────

export type MissionStatus = 'active' | 'complete' | 'partial' | 'unanswerable'
export type TaskStatus = 'pending' | 'active' | 'passing' | 'blocked' | 'pending_retry'

export interface MissionTask {
  taskId: string
  label: string
  description?: string
  status: TaskStatus
  toolHint?: string
}

export interface MissionView {
  missionId: string
  threadId: string
  projectId?: string
  question: string
  status: MissionStatus
  tasks: MissionTask[]
  synthesis?: { output: string }
  createdAt?: string
  updatedAt?: string
}

export interface MissionListResponse {
  missions: MissionView[]
}

// ─── Status style maps ───────────────────────────────────────────────────────

const MISSION_STATUS_STYLE: Record<MissionStatus, { ring: string; bg: string; text: string; badge: string }> = {
  active:       { ring: 'border-sky-300',     bg: 'bg-sky-50/60',     text: 'text-sky-700',     badge: 'bg-sky-100 text-sky-700' },
  complete:     { ring: 'border-emerald-300', bg: 'bg-emerald-50/60', text: 'text-emerald-700', badge: 'bg-emerald-100 text-emerald-700' },
  partial:      { ring: 'border-amber-300',   bg: 'bg-amber-50/60',   text: 'text-amber-700',   badge: 'bg-amber-100 text-amber-700' },
  unanswerable: { ring: 'border-rose-300',    bg: 'bg-rose-50/60',    text: 'text-rose-700',    badge: 'bg-rose-100 text-rose-700' },
}

const TASK_STATUS_STYLE: Record<TaskStatus, { ring: string; bg: string; text: string; Icon: typeof Check; animate?: string }> = {
  passing:       { ring: 'border-emerald-300', bg: 'bg-emerald-50', text: 'text-emerald-700', Icon: Check },
  active:        { ring: 'border-sky-400',     bg: 'bg-sky-50',     text: 'text-sky-700',     Icon: Loader2, animate: 'animate-spin' },
  blocked:       { ring: 'border-amber-300',   bg: 'bg-amber-50',   text: 'text-amber-700',   Icon: CircleSlash },
  pending:       { ring: 'border-gray-200',    bg: 'bg-gray-50',    text: 'text-gray-500',    Icon: X },
  pending_retry: { ring: 'border-orange-200',  bg: 'bg-orange-50',  text: 'text-orange-600',  Icon: AlertTriangle },
}

// ─── Sub-components ──────────────────────────────────────────────────────────

function TaskRow({ task }: { task: MissionTask }) {
  const s = TASK_STATUS_STYLE[task.status] || TASK_STATUS_STYLE.pending
  const Icon = s.Icon
  return (
    <div className={`flex items-start gap-2 px-2 py-1.5 border ${s.ring} ${s.bg} rounded`}>
      <Icon size={12} className={`${s.text} ${s.animate ?? ''} mt-0.5 shrink-0`} />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-1.5 flex-wrap">
          <code className="text-[10px] text-gray-500 bg-white/70 px-1 rounded">{task.taskId}</code>
          <span className={`text-[11px] font-semibold ${s.text}`}>{task.label}</span>
          {task.toolHint && (
            <span className="text-[10px] text-gray-400 bg-white/70 px-1 rounded font-mono">{task.toolHint}</span>
          )}
        </div>
        {task.description && (
          <div className="text-[10px] text-gray-500 mt-0.5">{task.description}</div>
        )}
      </div>
    </div>
  )
}

function MissionCard({ mission }: { mission: MissionView }) {
  const t = useTranslations('mission_ledger')
  const [open, setOpen] = useState(false)
  const ms = MISSION_STATUS_STYLE[mission.status] || MISSION_STATUS_STYLE.active

  // Trivial-mission carve-out: 0 or 1 task with no synthesis → compact line
  const isTrivial = mission.tasks.length <= 1 && !mission.synthesis?.output
  if (isTrivial) {
    return (
      <div className={`flex items-center gap-2 px-2 py-1.5 border ${ms.ring} ${ms.bg} rounded text-[11px]`}>
        <Sparkles size={12} className={ms.text} />
        <span className={`font-semibold ${ms.text}`}>{mission.question.slice(0, 80)}{mission.question.length > 80 ? '…' : ''}</span>
        <span className={`ml-auto text-[10px] px-1.5 py-0.5 rounded font-mono ${ms.badge}`}>{t(`status.${mission.status}`)}</span>
      </div>
    )
  }

  const passingCount = mission.tasks.filter(t => t.status === 'passing').length
  const blockedCount = mission.tasks.filter(t => t.status === 'blocked').length
  const activeCount  = mission.tasks.filter(t => t.status === 'active').length
  const pendingCount = mission.tasks.filter(t => t.status === 'pending' || t.status === 'pending_retry').length

  return (
    <div className={`border ${ms.ring} ${ms.bg} rounded`}>
      {/* Header row — always visible */}
      <button
        className="w-full flex items-center gap-2 px-2.5 py-1.5 text-left"
        onClick={() => setOpen(v => !v)}
      >
        {open ? <ChevronDown size={12} className="text-gray-400 shrink-0" /> : <ChevronRight size={12} className="text-gray-400 shrink-0" />}
        <Sparkles size={12} className={`${ms.text} shrink-0`} />
        <span className={`text-[11px] font-semibold ${ms.text} flex-1 min-w-0 truncate`}>
          {mission.question.slice(0, 80)}{mission.question.length > 80 ? '…' : ''}
        </span>
        <span className={`text-[10px] px-1.5 py-0.5 rounded font-mono shrink-0 ${ms.badge}`}>
          {t(`status.${mission.status}`)}
        </span>
        {mission.tasks.length > 0 && (
          <span className="flex items-center gap-1 shrink-0">
            {passingCount > 0 && <span className="text-[10px] px-1 rounded bg-emerald-100 text-emerald-700 font-mono">{passingCount}✓</span>}
            {blockedCount > 0 && <span className="text-[10px] px-1 rounded bg-amber-100 text-amber-700 font-mono">{blockedCount}!</span>}
            {activeCount  > 0 && <span className="text-[10px] px-1 rounded bg-sky-100 text-sky-700 font-mono">{activeCount}▶</span>}
            {pendingCount > 0 && <span className="text-[10px] px-1 rounded bg-gray-100 text-gray-500 font-mono">{pendingCount}·</span>}
          </span>
        )}
      </button>

      {/* Expanded body */}
      {open && (
        <div className="px-2.5 pb-2 space-y-1.5 border-t border-white/50">
          {mission.tasks.length > 0 && (
            <div className="space-y-1 pt-1.5">
              {mission.tasks.map(task => (
                <TaskRow key={task.taskId} task={task} />
              ))}
            </div>
          )}
          {mission.synthesis?.output && (
            <div className="border border-emerald-200 bg-white rounded p-2 mt-1">
              <div className="text-[10px] text-emerald-700 font-semibold mb-1">{t('synthesis_label')}</div>
              <pre className="text-[11px] text-gray-900 whitespace-pre-wrap font-sans leading-relaxed">{mission.synthesis.output}</pre>
            </div>
          )}
          <div className="text-[10px] text-gray-400 pt-0.5">
            <code className="bg-white/70 px-1 rounded">{mission.missionId.slice(0, 8)}…</code>
          </div>
        </div>
      )}
    </div>
  )
}

// ─── Public component ────────────────────────────────────────────────────────

interface MissionLedgerProps {
  missions: MissionView[]
  loading?: boolean
}

export function MissionLedger({ missions, loading }: MissionLedgerProps) {
  const t = useTranslations('mission_ledger')
  const [collapsed, setCollapsed] = useState(false)

  if (loading) {
    return (
      <div className="border-t border-indigo-100 px-4 py-2 flex items-center gap-2 text-[11px] text-gray-400 bg-indigo-50/40">
        <Loader2 size={12} className="animate-spin text-indigo-400" />
        {t('loading')}
      </div>
    )
  }

  if (!missions || missions.length === 0) return null

  return (
    <div className="border-t border-indigo-200 bg-indigo-50/30 flex-shrink-0">
      {/* Panel header */}
      <button
        className="w-full flex items-center gap-1.5 px-4 py-1.5 text-left hover:bg-indigo-50/60 transition-colors"
        onClick={() => setCollapsed(v => !v)}
      >
        {collapsed ? <ChevronRight size={12} className="text-indigo-400" /> : <ChevronDown size={12} className="text-indigo-400" />}
        <Sparkles size={12} className="text-indigo-500" />
        <span className="text-[11px] font-semibold text-indigo-700">{t('panel_title')}</span>
        <span className="text-[10px] text-indigo-400 ml-1">({missions.length})</span>
      </button>

      {!collapsed && (
        <div className="px-4 pb-3 space-y-1.5 max-h-48 overflow-y-auto">
          {missions.map(m => (
            <MissionCard key={m.missionId} mission={m} />
          ))}
        </div>
      )}
    </div>
  )
}
