'use client'

import { GitCompare, Plus, Star, Trash2, X } from 'lucide-react'
import { StatusBadge } from './SharedCaseBits'

export interface TestRun {
  id: string
  title: string
  llmConfigId: string
  modelName: string
  status: string
  concurrency: number
  total: number
  completedCount: number
  errorCount: number
  isDefault: boolean
  startedAt?: string
  finishedAt?: string
  createdAt: string
}

interface VersionSidebarProps {
  runs: TestRun[]
  selectedRunId: string | null
  runProgress: { runId: string; current: number; total: number } | null
  compareMode: boolean
  compareRunIds: string[]                      // ids selected for N-way compare (≥2 to fire)
  showNewRun: boolean
  newRunTitle: string
  newRunConcurrency: number
  onSelectRun: (runId: string) => void
  onToggleCompare: () => void
  onToggleCompareRun: (runId: string) => void  // toggle this run in/out of compare set
  onRunCompare: () => void
  onToggleNewRun: () => void
  onNewRunTitleChange: (v: string) => void
  onNewRunConcurrencyChange: (v: number) => void
  onCreateRun: () => void
  onSetDefault: (runId: string) => void
  onDeleteRun: (runId: string) => void
}

// Vertical sidebar holding the Run versions. Replaces the old horizontal
// RunStrip so the detail page can lay out as `versions | cases | detail`.
export function VersionSidebar(p: VersionSidebarProps) {
  return (
    <div className="w-[240px] flex-shrink-0 border-r border-border bg-canvas-alt/40 flex flex-col min-h-0">
      {/* Header actions */}
      <div className="px-3 py-2 border-b border-border flex items-center gap-2">
        <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">VERSIONS</span>
        <span className="font-mono text-[9px] text-ink-ghost">{p.runs.length}</span>
        <span className="flex-1" />
        {p.runs.length >= 2 && (
          <button
            onClick={p.onToggleCompare}
            title="对比两个 Run"
            className={`flex items-center gap-1 px-1.5 py-0.5 font-mono text-[9px] border ${
              p.compareMode ? 'border-accent text-accent bg-accent/5' : 'border-border text-ink-ghost hover:text-ink'
            }`}
          >
            <GitCompare size={9} /> 对比
          </button>
        )}
        <button
          onClick={p.onToggleNewRun}
          className="flex items-center gap-1 border border-accent bg-accent text-white px-1.5 py-0.5 font-mono text-[9px] font-bold"
        >
          <Plus size={9} /> 新建
        </button>
      </div>

      {/* New run form */}
      {p.showNewRun && (
        <div className="border-b border-border bg-white px-3 py-2 space-y-2">
          <input
            className="w-full border border-border px-2 py-1 font-mono text-[10px] focus:outline-none focus:border-accent"
            placeholder="版本标题（baseline / v2-prompt）"
            value={p.newRunTitle}
            onChange={e => p.onNewRunTitleChange(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && p.onCreateRun()}
            autoFocus
          />
          <div className="flex items-center gap-2">
            <select
              className="flex-1 border border-border px-2 py-1 font-mono text-[10px]"
              value={p.newRunConcurrency}
              onChange={e => p.onNewRunConcurrencyChange(Number(e.target.value))}
            >
              {[1, 2, 3, 5, 8, 10].map(n => (
                <option key={n} value={n}>并发:{n}</option>
              ))}
            </select>
            <button
              onClick={p.onCreateRun}
              disabled={!p.newRunTitle.trim()}
              className="border border-accent bg-accent text-white px-2 py-1 font-mono text-[9px] font-bold disabled:opacity-30"
            >
              创建
            </button>
            <button onClick={p.onToggleNewRun} className="p-1 text-ink-ghost hover:text-ink">
              <X size={12} />
            </button>
          </div>
          <div className="font-mono text-[9px] text-ink-ghost leading-relaxed">
            ▸ 使用当前默认 LLM 配置（role chain: ok_workbench → ont_route → sql_generate）
            <br />创建时 snapshot，后续改默认不影响此 Run
          </div>
        </div>
      )}

      {/* Compare selector — multi-select (≥2 runs for N-way Excel compare) */}
      {p.compareMode && (
        <div className="border-b border-border bg-white px-3 py-2 space-y-2">
          <div className="flex items-center justify-between font-mono text-[9px]">
            <span className="text-ink-ghost">勾选 ≥2 个 Run</span>
            <span className="text-ink font-bold">{p.compareRunIds.length} 已选</span>
          </div>
          <div className="max-h-44 overflow-y-auto space-y-1">
            {p.runs.map(r => {
              const checked = p.compareRunIds.includes(r.id)
              const order = checked ? p.compareRunIds.indexOf(r.id) + 1 : 0
              return (
                <label
                  key={r.id}
                  className={`flex items-center gap-2 px-1.5 py-1 border cursor-pointer transition-colors ${
                    checked ? 'border-accent bg-accent/5' : 'border-border hover:bg-canvas-alt/40'
                  }`}
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={() => p.onToggleCompareRun(r.id)}
                    className="accent-accent shrink-0"
                  />
                  <span className="font-mono text-[10px] text-ink truncate flex-1">{r.title}</span>
                  {checked && (
                    <span className="font-mono text-[8px] text-accent font-bold shrink-0">
                      #{order}
                    </span>
                  )}
                </label>
              )
            })}
          </div>
          <button
            onClick={p.onRunCompare}
            disabled={p.compareRunIds.length < 2}
            className="w-full border border-accent bg-accent text-white px-2 py-1 font-mono text-[10px] font-bold disabled:opacity-30"
          >
            生成对比 ({p.compareRunIds.length})
          </button>
        </div>
      )}

      {/* Version list */}
      <div className="flex-1 overflow-y-auto">
        {p.runs.length === 0 ? (
          <div className="p-4 font-mono text-[10px] text-ink-ghost text-center">
            暂无 Run — 点「新建」创建第一个版本
          </div>
        ) : (
          p.runs.map(r => {
            const isSelected = p.selectedRunId === r.id
            const progress = p.runProgress?.runId === r.id ? p.runProgress : null
            return (
              <div
                key={r.id}
                onClick={() => p.onSelectRun(r.id)}
                className={`border-b border-border px-3 py-2 cursor-pointer transition-colors ${
                  isSelected ? 'bg-accent/5 border-l-2 border-l-accent' : 'hover:bg-canvas-alt/60'
                }`}
              >
                <div className="flex items-center gap-1 mb-1">
                  <span className="font-mono text-[10px] font-bold truncate flex-1">{r.title}</span>
                  <button
                    onClick={e => { e.stopPropagation(); p.onSetDefault(r.id) }}
                    className={`p-0.5 ${r.isDefault ? 'text-accent' : 'text-ink-ghost hover:text-accent'}`}
                    title={r.isDefault ? '默认版本' : '设为默认'}
                  >
                    <Star size={10} fill={r.isDefault ? 'currentColor' : 'none'} />
                  </button>
                  <button
                    onClick={e => { e.stopPropagation(); p.onDeleteRun(r.id) }}
                    className="p-0.5 text-ink-ghost hover:text-accent"
                    title="删除"
                  >
                    <Trash2 size={9} />
                  </button>
                </div>
                <div className="font-mono text-[8px] text-ink-ghost mb-1 truncate" title={r.modelName}>
                  {r.modelName || '—'}
                </div>
                {r.status === 'queued' && (!progress || progress.current === 0) ? (
                  <div className="flex items-center gap-2">
                    <StatusBadge status="queued" />
                    <span className="font-mono text-[8px] text-amber-600">排队中...</span>
                  </div>
                ) : progress ? (
                  <>
                    <div className="h-1 bg-border mb-1">
                      <div
                        className="h-full bg-accent transition-all duration-300"
                        style={{ width: `${progress.total > 0 ? (progress.current / progress.total) * 100 : 0}%` }}
                      />
                    </div>
                    <div className="font-mono text-[8px] text-ink-ghost">
                      {progress.current}/{progress.total}
                    </div>
                  </>
                ) : (
                  <div className="flex items-center gap-2">
                    <StatusBadge status={r.status} />
                    <span className="font-mono text-[8px] text-ink-ghost">
                      {r.completedCount}/{r.total}
                    </span>
                  </div>
                )}
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
