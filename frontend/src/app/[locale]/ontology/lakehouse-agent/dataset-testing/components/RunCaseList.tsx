'use client'

import { useEffect, useMemo, useState } from 'react'
import { Download, Loader2, Play, RefreshCcw, Search, Sparkles, StickyNote, X, Square } from 'lucide-react'
import { CaseStatusBadge, MarkIndicator, RunCase, Tag } from './SharedCaseBits'
import { TagChipEditor } from './TagChipEditor'
import { BatchTagBar } from './BatchTagBar'

interface RunCaseListProps {
  runId: string
  cases: RunCase[]
  runProgress: { runId: string; current: number; total: number } | null
  isRunning: boolean
  retryingCaseIds: Set<string>
  selectedCaseId: string | null
  exportingSuites: Set<string>
  // Tag integration
  suiteTags: Tag[]
  // Batch AI judge progress (null when idle).
  judgeProgress: { current: number; total: number; concurrency: number } | null
  onCaseTagsChange: (caseId: string, tagNames: string[]) => void
  onBulkTag: (caseIds: string[], add: string[], remove: string[]) => void
  onSelectCase: (id: string) => void
  onRetry: (rcId: string) => void
  onRun: () => void
  onContinue: () => void
  onRetryErrors: () => void
  // Batch re-run of a caller-supplied set of run-case ids. Wired to two paths:
  // (1) "重跑选中" — uses checkbox-selected cases (intersected with non-pending/non-running),
  // (2) "重跑标错" — uses all mark='incorrect' cases when no checkbox selection.
  onBulkRetry: (rcIds: string[]) => void
  onCancel: () => void
  // True once cancel has been requested for the current active run, but the
  // worker hasn't drained yet. Lets the button show "中止中..." instead of
  // letting the user click cancel a second time.
  cancelRequested: boolean
  onExport: (format: 'csv' | 'xlsx') => void
  // selectedCaseIds (master case_ids, not run-case ids) — when undefined the
  // parent should judge every case with finalAnswer; otherwise restrict to
  // those caseIds (intersected with cases that have finalAnswer).
  onBatchJudge: (selectedCaseIds?: string[]) => void
  onCancelBatchJudge: () => void
}

export function RunCaseList({
  runId, cases, runProgress, isRunning,
  retryingCaseIds, selectedCaseId, exportingSuites,
  suiteTags, judgeProgress,
  onCaseTagsChange, onBulkTag,
  onSelectCase, onRetry, onRun, onContinue, onRetryErrors, onBulkRetry, onCancel, cancelRequested, onExport,
  onBatchJudge, onCancelBatchJudge,
}: RunCaseListProps) {
  const pendingCount = cases.filter(c => c.status === 'pending').length
  const completedCount = cases.filter(c => c.status !== 'pending' && c.status !== 'running').length
  const errorCount = cases.filter(c => c.status === 'error' || (c.status === 'completed' && c.executionStatus === 'error')).length
  const markedCorrect = cases.filter(c => c.mark === 'correct').length
  const markedWrong = cases.filter(c => c.mark === 'incorrect').length
  const hasInterrupted = !isRunning && pendingCount > 0 && completedCount > 0
  const progress = runProgress?.runId === runId ? runProgress : null
  // Accuracy is denominated by *judged* cases only — correct / (correct + incorrect).
  // pendingJudge = cases that have a finalAnswer but no mark yet (judgeable but
  // not yet judged). Cases without finalAnswer aren't counted in either bucket
  // because the judge can't rule on them.
  const finalAnswerCount = cases.filter(c => (c.finalAnswer || '').trim() !== '').length
  const judgedTotal = markedCorrect + markedWrong
  const accuracyPct = judgedTotal > 0 ? (markedCorrect / judgedTotal) * 100 : 0
  const pendingJudge = cases.filter(c => !c.mark && (c.finalAnswer || '').trim() !== '').length
  const judging = judgeProgress !== null

  // ---- Filter state ----
  const [filterTagIds, setFilterTagIds] = useState<Set<string>>(new Set())
  const [searchQuery, setSearchQuery] = useState('')
  // Quick mark filter: 'all' shows everything; the others narrow by judgement state.
  // 'pending' = has finalAnswer but no mark yet (matches the ⏳ indicator + stat).
  const [markFilter, setMarkFilter] = useState<'all' | 'correct' | 'incorrect' | 'pending'>('all')

  const isPendingJudge = (c: RunCase) =>
    !c.mark && (c.finalAnswer || '').trim() !== ''

  const filteredCases = useMemo(() => {
    const q = searchQuery.trim().toLowerCase()
    return cases.filter(c => {
      if (filterTagIds.size > 0 && !(c.tags || []).some(t => filterTagIds.has(t.id))) return false
      if (q && !c.userQuestion.toLowerCase().includes(q) && !c.code.toLowerCase().includes(q)) return false
      if (markFilter === 'correct' && c.mark !== 'correct') return false
      if (markFilter === 'incorrect' && c.mark !== 'incorrect') return false
      if (markFilter === 'pending' && !isPendingJudge(c)) return false
      return true
    })
  }, [cases, filterTagIds, searchQuery, markFilter])

  // ---- Batch selection state ----
  const [checked, setChecked] = useState<Set<string>>(new Set())

  // Wipe selection when switching versions. Different Run = different run-case
  // snapshot; stale caseIds from the previous run would either act on the
  // wrong rows or (worse) silently no-op, which is what makes the user feel
  // "the selection disappeared" after clicking a version.
  useEffect(() => {
    setChecked(new Set())
  }, [runId])

  // Prune selection whenever the *filter* narrows (mark filter / search box /
  // tag chips). Previously a selection made under "全部" stayed counted after
  // switching to "✗ 错误" — header said "已选 N 条" while only M<N rows were
  // visible, and batch actions (AI 判定选中 / 重跑选中) silently operated on
  // the hidden rows too. Deps are *filter state only* — NOT `cases` — so
  // status updates from the 2.5s poll never wipe a mid-test selection.
  useEffect(() => {
    setChecked(prev => {
      if (prev.size === 0) return prev
      const q = searchQuery.trim().toLowerCase()
      const visible = new Set<string>()
      for (const c of cases) {
        if (filterTagIds.size > 0 && !(c.tags || []).some(t => filterTagIds.has(t.id))) continue
        if (q && !c.userQuestion.toLowerCase().includes(q) && !c.code.toLowerCase().includes(q)) continue
        if (markFilter === 'correct' && c.mark !== 'correct') continue
        if (markFilter === 'incorrect' && c.mark !== 'incorrect') continue
        if (markFilter === 'pending' && !(!c.mark && (c.finalAnswer || '').trim() !== '')) continue
        visible.add(c.caseId)
      }
      const pruned = new Set(Array.from(prev).filter(id => visible.has(id)))
      return pruned.size === prev.size ? prev : pruned
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [markFilter, searchQuery, filterTagIds])

  const toggleChecked = (caseId: string) => {
    setChecked(prev => {
      const next = new Set(prev)
      if (next.has(caseId)) next.delete(caseId); else next.add(caseId)
      return next
    })
  }
  const allVisibleChecked = filteredCases.length > 0 && filteredCases.every(c => checked.has(c.caseId))
  const toggleCheckAll = () => {
    if (allVisibleChecked) {
      setChecked(prev => {
        const next = new Set(prev)
        filteredCases.forEach(c => next.delete(c.caseId))
        return next
      })
    } else {
      setChecked(prev => {
        const next = new Set(prev)
        filteredCases.forEach(c => next.add(c.caseId))
        return next
      })
    }
  }
  const clearChecked = () => setChecked(new Set())

  const selectedCaseIds = Array.from(checked)
  // Cases in the current selection that have a non-empty finalAnswer (i.e.
  // judgeable). Drives the AI Judge button label + count when selection > 0.
  const selectedFinalAnswerCount = useMemo(() => {
    if (selectedCaseIds.length === 0) return 0
    return cases.filter(c => checked.has(c.caseId) && (c.finalAnswer || '').trim() !== '').length
  }, [cases, checked, selectedCaseIds.length])

  // Tags present across the currently-checked cases (union) — shown in batch bar for bulk remove.
  const batchUnionTags = useMemo<Tag[]>(() => {
    if (selectedCaseIds.length === 0) return []
    const pool = new Map<string, Tag>()
    cases.forEach(c => {
      if (!checked.has(c.caseId)) return
      (c.tags || []).forEach(t => pool.set(t.id, t))
    })
    return Array.from(pool.values()).sort((a, b) => a.name.localeCompare(b.name))
  }, [cases, checked, selectedCaseIds.length])

  const toggleFilterTag = (id: string) => {
    setFilterTagIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id); else next.add(id)
      return next
    })
  }

  return (
    <>
      {progress && (
        <div className="px-4 py-2 bg-canvas-alt border-b border-border">
          <div className="flex items-center gap-2 mb-1">
            <Loader2 size={12} className="animate-spin text-accent" />
            <span className="font-mono text-[10px] text-ink">
              测试中 {progress.current}/{progress.total}
            </span>
          </div>
          <div className="h-1 bg-border">
            <div className="h-full bg-accent transition-all duration-300"
              style={{ width: `${progress.total > 0 ? (progress.current / progress.total) * 100 : 0}%` }} />
          </div>
        </div>
      )}

      {judgeProgress && (
        <div className="px-4 py-2 bg-violet-50 border-b border-violet-200">
          <div className="flex items-center gap-2 mb-1">
            <Sparkles size={12} className="text-violet-600 animate-pulse" />
            <span className="font-mono text-[10px] text-violet-800 font-bold">
              AI 判定中 {judgeProgress.current}/{judgeProgress.total}
            </span>
            <span className="font-mono text-[9px] text-violet-600">
              · 并发 {judgeProgress.concurrency}
            </span>
            <span className="flex-1" />
            <button
              onClick={onCancelBatchJudge}
              className="font-mono text-[9px] text-violet-700 hover:text-violet-900 underline"
            >
              停止
            </button>
          </div>
          <div className="h-1 bg-violet-100">
            <div className="h-full bg-violet-500 transition-all duration-300"
              style={{ width: `${judgeProgress.total > 0 ? (judgeProgress.current / judgeProgress.total) * 100 : 0}%` }} />
          </div>
        </div>
      )}

      {/* Filter bar: search + mark-filter segmented buttons + tag chips. Always
         shown when cases exist so the search box is reachable even without tags. */}
      {cases.length > 0 && (
        <div className="flex items-center gap-2 px-4 py-1.5 border-b border-border bg-canvas-alt/30 flex-wrap">
          <div className="flex items-center gap-1 border border-border bg-white px-2 py-0.5">
            <Search size={11} className="text-ink-ghost" />
            <input
              value={searchQuery}
              onChange={e => setSearchQuery(e.target.value)}
              placeholder="搜索问题"
              className="font-mono text-[10px] outline-none w-32 bg-transparent"
            />
            {searchQuery && (
              <button onClick={() => setSearchQuery('')} className="text-ink-ghost hover:text-accent">
                <X size={10} />
              </button>
            )}
          </div>
          {/* Quick mark-status filter — segmented control. Counts come from the
             unfiltered case set so users see the absolute scope, not whatever
             other filters are stacked. */}
          {(() => {
            const totalAll = cases.length
            const totalCorrect = cases.filter(c => c.mark === 'correct').length
            const totalIncorrect = cases.filter(c => c.mark === 'incorrect').length
            const totalPending = cases.filter(c => isPendingJudge(c)).length
            const opts: Array<{ key: typeof markFilter; label: string; count: number; active: string; idle: string }> = [
              { key: 'all',       label: '全部',    count: totalAll,       active: 'bg-ink text-white border-ink',                 idle: 'bg-white text-ink border-border hover:border-ink' },
              { key: 'correct',   label: '✓ 正确',  count: totalCorrect,   active: 'bg-green-600 text-white border-green-600',     idle: 'bg-white text-green-700 border-green-300 hover:border-green-600' },
              { key: 'incorrect', label: '✗ 错误',  count: totalIncorrect, active: 'bg-red-600 text-white border-red-600',         idle: 'bg-white text-red-700 border-red-300 hover:border-red-600' },
              { key: 'pending',   label: '⏳ 待判定', count: totalPending,   active: 'bg-amber-500 text-white border-amber-500',     idle: 'bg-white text-amber-700 border-amber-300 hover:border-amber-600' },
            ]
            return (
              <div className="flex items-center">
                {opts.map((o, i) => {
                  const isActive = markFilter === o.key
                  return (
                    <button
                      key={o.key}
                      onClick={() => setMarkFilter(o.key)}
                      className={`flex items-center gap-1 border px-2 py-0.5 font-mono text-[10px] font-bold leading-none ${
                        isActive ? o.active : o.idle
                      } ${i > 0 ? '-ml-px' : ''}`}
                      title={`筛选：${o.label}（${o.count}）`}
                    >
                      {o.label}
                      <span className={isActive ? 'opacity-70' : 'text-ink-ghost'}>{o.count}</span>
                    </button>
                  )
                })}
              </div>
            )
          })()}
          {suiteTags.length > 0 && (
            <>
              <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">标签:</span>
              {suiteTags.map(t => {
                const active = filterTagIds.has(t.id)
                return (
                  <button
                    key={t.id}
                    onClick={() => toggleFilterTag(t.id)}
                    className={`inline-flex items-center gap-1 border-2 px-1.5 py-0.5 font-mono text-[10px] font-bold leading-none ${
                      active
                        ? 'bg-ink text-white border-ink'
                        : 'bg-white text-ink border-ink hover:bg-accent/10'
                    }`}
                  >
                    <span className={active ? 'text-accent' : 'text-accent'}>#</span>
                    {t.name}
                    {typeof t.count === 'number' && (
                      <span className={active ? 'text-white/60' : 'text-ink-ghost font-semibold'}>{t.count}</span>
                    )}
                  </button>
                )
              })}
              {filterTagIds.size > 0 && (
                <button
                  onClick={() => setFilterTagIds(new Set())}
                  className="font-mono text-[9px] text-accent hover:underline"
                >
                  清除
                </button>
              )}
            </>
          )}
          <span className="flex-1" />
          {(filterTagIds.size > 0 || searchQuery || markFilter !== 'all') && (
            <span className="font-mono text-[9px] text-ink-ghost">
              {filteredCases.length}/{cases.length}
            </span>
          )}
        </div>
      )}

      <div className="flex items-center gap-2 px-4 py-2 border-b border-border">
        {hasInterrupted ? (
          <button onClick={onContinue}
            disabled={isRunning}
            className="flex items-center gap-1 border border-accent bg-accent text-white px-3 py-1 font-mono text-[10px] font-bold disabled:opacity-30 disabled:cursor-not-allowed">
            <Play size={10} /> 继续 ({pendingCount})
          </button>
        ) : (
          <button onClick={onRun}
            disabled={isRunning || cases.length === 0 || pendingCount === 0}
            className="flex items-center gap-1 border border-accent bg-accent text-white px-3 py-1 font-mono text-[10px] font-bold disabled:opacity-30 disabled:cursor-not-allowed">
            <Play size={10} /> 运行 {pendingCount > 0 && `(${pendingCount})`}
          </button>
        )}
        {/* Cancel button — visible while the run is queued/running. Clicking
            once flips to a disabled "中止中…" state because cancellation is
            cooperative on the backend (in-flight cases finish their LLM call
            before being drained). */}
        {isRunning && (
          <button onClick={onCancel}
            disabled={cancelRequested}
            className="flex items-center gap-1 border border-zinc-500 text-zinc-700 px-3 py-1 font-mono text-[10px] font-bold hover:bg-zinc-100 disabled:opacity-50 disabled:cursor-not-allowed"
            title={cancelRequested ? '已发出中止请求，正在等待 worker 退出' : '协作式中止：等待正在执行的用例完成后停止'}>
            <Square size={10} /> {cancelRequested ? '中止中…' : '中止'}
          </button>
        )}
        {!isRunning && errorCount > 0 && (
          <button onClick={onRetryErrors}
            className="flex items-center gap-1 border border-red-400 text-red-600 px-3 py-1 font-mono text-[10px] font-bold hover:bg-red-50">
            <RefreshCcw size={10} /> 重试错误 ({errorCount})
          </button>
        )}
        {(() => {
          // Selection-aware bulk re-run. With a checkbox selection it targets
          // the intersect of (selected ∩ retryable); without selection it falls
          // back to every mark='incorrect' run-case. Cases already pending or
          // running are skipped either way — the worker is (about to) pick them
          // up and re-resetting would race its UPDATE.
          const retryable = (c: RunCase) => c.status !== 'pending' && c.status !== 'running'
          const hasSelection = selectedCaseIds.length > 0
          const targets = hasSelection
            ? cases.filter(c => checked.has(c.caseId) && retryable(c))
            : cases.filter(c => c.mark === 'incorrect' && retryable(c))
          const label = hasSelection ? '重跑选中' : '重跑标错'
          const disabledReason =
            isRunning ? '运行中' :
            targets.length === 0
              ? (hasSelection ? '所选用例都已在排队或运行中' : '没有标注为错误的用例')
              : ''
          const tip = hasSelection
            ? `重置所选 ${selectedCaseIds.length} 条中可重跑的 ${targets.length} 条并重新排队`
            : `重置标注为错误的 ${targets.length} 条并重新排队`
          return (
            <button
              onClick={() => onBulkRetry(targets.map(c => c.id))}
              disabled={!!disabledReason}
              className="flex items-center gap-1 border border-amber-500 text-amber-700 px-3 py-1 font-mono text-[10px] font-bold hover:bg-amber-50 disabled:opacity-30 disabled:cursor-not-allowed"
              title={disabledReason || tip}
            >
              <RefreshCcw size={10} /> {label} {targets.length > 0 && `(${targets.length})`}
            </button>
          )
        })()}
        {(() => {
          // Selection-aware AI Judge button. With nothing checked it judges
          // every case with finalAnswer; with a checkbox selection it narrows
          // to those (still skipping selected cases that lack finalAnswer).
          const hasSelection = selectedCaseIds.length > 0
          const targetCount = hasSelection ? selectedFinalAnswerCount : finalAnswerCount
          const disabledReason =
            isRunning ? '运行中' :
            judging ? '正在判定' :
            targetCount === 0 ? (hasSelection ? '所选用例都没有 finalAnswer' : '没有包含 finalAnswer 的用例') :
            ''
          const label = hasSelection ? 'AI 判定选中' : 'AI 全部判定'
          const tip = hasSelection
            ? `对所选 ${selectedCaseIds.length} 条中含 finalAnswer 的 ${targetCount} 条调用 AI 判定`
            : `对 ${finalAnswerCount} 条有 finalAnswer 的用例批量调用 AI 判定`
          return (
            <button
              onClick={() => onBatchJudge(hasSelection ? selectedCaseIds : undefined)}
              disabled={!!disabledReason}
              className="flex items-center gap-1 border border-violet-500 text-violet-700 px-3 py-1 font-mono text-[10px] font-bold hover:bg-violet-50 disabled:opacity-30 disabled:cursor-not-allowed"
              title={disabledReason || tip}
            >
              {judging ? <Loader2 size={10} className="animate-spin" /> : <Sparkles size={10} />}
              {label} {targetCount > 0 && `(${targetCount})`}
            </button>
          )
        })()}
        <button onClick={() => onExport('csv')}
          disabled={exportingSuites.has(`run:${runId}:csv`) || cases.length === 0}
          className="flex items-center gap-1 border border-border px-3 py-1 font-mono text-[10px] text-ink-ghost hover:text-ink disabled:opacity-30"
          title="导出 CSV">
          {exportingSuites.has(`run:${runId}:csv`) ? <Loader2 size={10} className="animate-spin" /> : <Download size={10} />}
          CSV
        </button>
        <button onClick={() => onExport('xlsx')}
          disabled={exportingSuites.has(`run:${runId}:xlsx`) || cases.length === 0}
          className="flex items-center gap-1 border border-border px-3 py-1 font-mono text-[10px] text-ink-ghost hover:text-ink disabled:opacity-30"
          title="导出 Excel">
          {exportingSuites.has(`run:${runId}:xlsx`) ? <Loader2 size={10} className="animate-spin" /> : <Download size={10} />}
          Excel
        </button>
        <span className="flex-1" />
        <span className="font-mono text-[9px] text-ink-ghost">
          {completedCount}/{cases.length} 已完成
        </span>
        {(markedCorrect > 0 || markedWrong > 0 || pendingJudge > 0) && (
          <span className="font-mono text-[9px] text-ink-ghost ml-2">
            <span className="text-green-600 font-bold">✓ {markedCorrect}</span>
            {' · '}
            <span className="text-red-600 font-bold">✗ {markedWrong}</span>
            {' · '}
            <span
              className="text-amber-600 font-bold"
              title={`pending = 有 finalAnswer 但尚未判定的用例（${pendingJudge}）`}
            >⏳ {pendingJudge}</span>
          </span>
        )}
        {judgedTotal > 0 && (
          <span
            className="font-mono text-[9px] text-violet-700 ml-2"
            title={`正确率 = 正确 / (正确 + 错误) = ${markedCorrect}/${judgedTotal}；未判定的 ${pendingJudge} 条不计入分母`}
          >
            正确率 <span className="font-bold">{markedCorrect}/{judgedTotal}</span>
            {' · '}
            <span className="font-bold">{accuracyPct.toFixed(1)}%</span>
          </span>
        )}
      </div>

      {filteredCases.length === 0 ? (
        <div className="flex h-24 items-center justify-center font-mono text-xs text-ink-ghost">
          {cases.length === 0 ? '暂无用例' : '当前筛选下无匹配用例'}
        </div>
      ) : (
        <>
          {/* Column header — shows batch "全选" checkbox for visible filter */}
          <div className="flex items-center gap-3 px-4 py-1 bg-canvas-alt/40 border-b border-border sticky top-0 z-[5]">
            <input
              type="checkbox"
              className="accent-ink cursor-pointer"
              checked={allVisibleChecked}
              onChange={toggleCheckAll}
              title="全选当前视图"
            />
            <span className="font-mono text-[9px] text-ink-ghost">
              {checked.size > 0 ? `已选 ${checked.size} 条` : `共 ${filteredCases.length} 条`}
            </span>
          </div>

          <div className="divide-y divide-border">
            {filteredCases.map(tc => {
              const isSelected = selectedCaseId === tc.id
              const isRetrying = retryingCaseIds.has(tc.id)
              const isChecked = checked.has(tc.caseId)
              return (
                <div key={tc.id}
                  className={`flex items-center gap-3 px-4 py-2 cursor-pointer transition-colors ${
                    isSelected ? 'bg-accent/5 border-l-2 border-l-accent' : 'hover:bg-canvas-alt/30'
                  }`}
                  onClick={() => tc.status === 'completed' || tc.status === 'error' || tc.status === 'cancelled' ? onSelectCase(tc.id) : undefined}
                >
                  <input
                    type="checkbox"
                    className="accent-ink cursor-pointer flex-shrink-0"
                    checked={isChecked}
                    onClick={e => e.stopPropagation()}
                    onChange={() => toggleChecked(tc.caseId)}
                  />
                  <span className="font-mono text-[10px] text-ink-ghost w-10 flex-shrink-0">{tc.code}</span>
                  <div className="w-4 flex-shrink-0 text-center">
                    <MarkIndicator
                      mark={tc.mark}
                      pendingJudge={!tc.mark && (tc.finalAnswer || '').trim() !== ''}
                    />
                  </div>
                  <span className="font-mono text-xs text-ink flex-1 truncate min-w-0">{tc.userQuestion}</span>
                  {/* Inline tag editor */}
                  <div
                    className="flex-shrink-0 max-w-[40%]"
                    onClick={e => e.stopPropagation()}
                  >
                    <TagChipEditor
                      tags={tc.tags || []}
                      suiteTags={suiteTags}
                      onCommit={names => onCaseTagsChange(tc.caseId, names)}
                      compact
                    />
                  </div>
                  {tc.note && (
                    <StickyNote size={11} className="text-amber-600 flex-shrink-0" aria-label="有备注" />
                  )}
                  <CaseStatusBadge status={tc.status} execStatus={tc.executionStatus} />
                  {tc.durationMs > 0 && (
                    <span className="font-mono text-[9px] text-ink-ghost w-12 text-right">
                      {(tc.durationMs / 1000).toFixed(1)}s
                    </span>
                  )}
                  <button
                    disabled={isRetrying || tc.status === 'running' || tc.status === 'pending'}
                    onClick={e => { e.stopPropagation(); onRetry(tc.id) }}
                    className="flex items-center gap-1 border border-border px-1.5 py-0.5 font-mono text-[9px] text-ink-muted hover:border-accent hover:text-accent disabled:opacity-30 disabled:cursor-not-allowed"
                    title="重新运行此用例"
                  >
                    {isRetrying ? <Loader2 size={10} className="animate-spin" /> : <RefreshCcw size={10} />}
                    重试
                  </button>
                </div>
              )
            })}
          </div>
        </>
      )}

      {/* Floating batch tag toolbar — sticky at bottom when rows selected */}
      {selectedCaseIds.length > 0 && (
        <BatchTagBar
          count={selectedCaseIds.length}
          suiteTags={suiteTags}
          unionTags={batchUnionTags}
          onAdd={names => onBulkTag(selectedCaseIds, names, [])}
          onRemove={names => onBulkTag(selectedCaseIds, [], names)}
          onClear={clearChecked}
        />
      )}
    </>
  )
}

