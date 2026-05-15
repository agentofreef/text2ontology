'use client'

import { useState, useEffect, useMemo, useRef } from 'react'
import { Download, Loader2, Search, StickyNote, X } from 'lucide-react'
import { api, getApiBase, getApiBaseFor } from '@/lib/api'
import { CaseStatusBadge, CaseDetailBody, MarkIndicator, RunCase, Tag } from './SharedCaseBits'
import type { TestRun } from './VersionSidebar'

/**
 * CompareRow.results carry only the SUMMARY shape returned by /compare:
 *   { id, status, executionStatus, mark?, note?, durationMs, modelName }
 * The heavy fields (functionCalls / executionResult / generatedSql /
 * finalAnswer / executionError) live behind /compare/case/{caseId}?runs=...
 * and are fetched lazily when the user picks a row.
 *
 * Type stays `RunCase` so the question-list badges can read fields without
 * branching; missing big fields are simply undefined / empty string.
 */
export interface CompareRow {
  caseId: string
  question: string
  code: string
  sortOrder: number
  tags?: Tag[]
  results: (RunCase | null)[]   // aligned 1:1 with data.runs
}

interface CompareViewProps {
  suiteId: string
  data: { runs: TestRun[]; rows: CompareRow[] }
  suiteTags?: Tag[]
}

export function CompareView({ suiteId, data, suiteTags = [] }: CompareViewProps) {
  // Filter state — carried into left question list only; the right split view
  // continues to show the selected row's full results regardless of filter.
  const [searchQuery, setSearchQuery] = useState('')
  const [filterTagIds, setFilterTagIds] = useState<Set<string>>(new Set())
  // Per-run mark filter. Map<runId, Set<'correct'|'incorrect'|'unmarked'>>.
  // Within a single run, multiple selections are OR'd (e.g. ✓+✗ = "judged for
  // this run"). Across runs they're AND'd (e.g. {A:✓, B:✗} = "regressions
  // where A was correct but B was wrong"). Empty entries are deleted to keep
  // the "any active filter" check trivial.
  type MarkKey = 'correct' | 'incorrect' | 'unmarked'
  const [markFilters, setMarkFilters] = useState<Record<string, Set<MarkKey>>>({})

  const markKeyOf = (mark?: string): MarkKey =>
    mark === 'correct' || mark === 'incorrect' ? mark : 'unmarked'

  const filteredRows = useMemo(() => {
    const q = searchQuery.trim().toLowerCase()
    const activeFilters = Object.entries(markFilters).filter(([, s]) => s.size > 0)
    return data.rows.filter(r => {
      if (filterTagIds.size > 0 && !(r.tags || []).some(t => filterTagIds.has(t.id))) return false
      if (q && !r.question.toLowerCase().includes(q) && !r.code.toLowerCase().includes(q)) return false
      for (const [runId, set] of activeFilters) {
        const runIdx = data.runs.findIndex(rn => rn.id === runId)
        if (runIdx < 0) continue
        const result = r.results[runIdx]
        if (!set.has(markKeyOf(result?.mark))) return false
      }
      return true
    })
  }, [data.rows, data.runs, filterTagIds, searchQuery, markFilters])

  const toggleMarkFilter = (runId: string, key: MarkKey) => {
    setMarkFilters(prev => {
      const cur = new Set(prev[runId] || [])
      if (cur.has(key)) cur.delete(key); else cur.add(key)
      const next = { ...prev }
      if (cur.size === 0) delete next[runId]
      else next[runId] = cur
      return next
    })
  }
  const hasMarkFilter = Object.values(markFilters).some(s => s.size > 0)

  const toggleFilterTag = (id: string) => {
    setFilterTagIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id); else next.add(id)
      return next
    })
  }
  const [selectedCaseId, setSelectedCaseId] = useState<string | null>(null)
  // caseId → detail array aligned 1:1 with data.runs
  const [detailCache, setDetailCache] = useState<Record<string, (RunCase | null)[]>>({})
  const [loadingCaseId, setLoadingCaseId] = useState<string | null>(null)
  const [exporting, setExporting] = useState(false)
  const inflight = useRef<Set<string>>(new Set())

  const selected = useMemo(
    () => data.rows.find(r => r.caseId === selectedCaseId) || null,
    [selectedCaseId, data.rows],
  )
  const labels = useMemo(() => labelsFor(data.runs.length), [data.runs.length])
  const runIdsKey = useMemo(() => data.runs.map(r => r.id).join(','), [data.runs])

  // Reset cache + inflight when the run set changes (different N-way compare).
  useEffect(() => {
    setDetailCache({})
    inflight.current = new Set()
  }, [runIdsKey])

  // Auto-select first row when compare data loads or filter changes wipes the current selection.
  useEffect(() => {
    if (filteredRows.length === 0) return
    if (!selectedCaseId || !filteredRows.some(r => r.caseId === selectedCaseId)) {
      setSelectedCaseId(filteredRows[0].caseId)
    }
  }, [filteredRows, selectedCaseId])

  // Lazy-fetch detail for selected row.
  useEffect(() => {
    if (!selectedCaseId) return
    if (detailCache[selectedCaseId]) return     // cache hit
    if (inflight.current.has(selectedCaseId)) return   // already fetching
    inflight.current.add(selectedCaseId)
    setLoadingCaseId(selectedCaseId)
    let cancelled = false
    api<{ caseId: string; results: (RunCase | null)[] }>(
      `/ontology/lh-test-suites/${suiteId}/compare/case/${selectedCaseId}?runs=${runIdsKey}`,
    )
      .then(res => {
        if (cancelled) return
        setDetailCache(prev => ({ ...prev, [res.caseId]: res.results }))
      })
      .catch(() => { /* silent — PanelBody falls back to "无结果" */ })
      .finally(() => {
        inflight.current.delete(selectedCaseId)
        if (!cancelled && loadingCaseId === selectedCaseId) {
          setLoadingCaseId(null)
        }
      })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedCaseId, runIdsKey, suiteId])

  const detailForSelected = selectedCaseId ? detailCache[selectedCaseId] : undefined
  const isLoadingDetail = loadingCaseId === selectedCaseId && !detailForSelected

  const downloadCompare = async (format: 'csv' | 'xlsx') => {
    setExporting(true)
    try {
      const token = localStorage.getItem('lakehouse2ontology_token')
      const res = await fetch(
        `${getApiBaseFor('/ontology/lh-test-suites')}/ontology/lh-test-suites/${suiteId}/compare/export?runs=${runIdsKey}&format=${format}`,
        { headers: token ? { Authorization: `Bearer ${token}` } : {} },
      )
      if (!res.ok) { return }
      let filename = `compare.${format}`
      const cd = res.headers.get('Content-Disposition') || ''
      const star = cd.match(/filename\*=UTF-8''([^;]+)/i)
      if (star) {
        try { filename = decodeURIComponent(star[1]) } catch { /* keep fallback */ }
      } else {
        const plain = cd.match(/filename="([^"]+)"/i)
        if (plain) filename = plain[1]
      }
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url; a.download = filename
      document.body.appendChild(a); a.click()
      document.body.removeChild(a); URL.revokeObjectURL(url)
    } catch { /* silent */ }
    finally { setExporting(false) }
  }

  return (
    <div className="flex h-full min-h-0">
      {/* Left: question list (uses summary fields only) */}
      <div className="w-80 border-r border-border flex flex-col min-h-0">
        <div className="px-3 py-2 border-b border-border bg-canvas-alt flex items-center gap-2">
          <span className="font-mono text-[9px] font-bold tracking-wider text-ink-ghost flex-1">
            QUESTIONS · {filteredRows.length}/{data.rows.length} · {data.runs.length} RUNS
          </span>
          <button onClick={() => downloadCompare('csv')} disabled={exporting}
            className="flex items-center gap-1 border border-border px-2 py-1 font-mono text-[10px] text-ink-ghost hover:text-ink hover:border-ink disabled:opacity-30">
            {exporting ? <Loader2 size={10} className="animate-spin" /> : <Download size={10} />} CSV
          </button>
          <button onClick={() => downloadCompare('xlsx')} disabled={exporting}
            className="flex items-center gap-1 border border-border px-2 py-1 font-mono text-[10px] text-ink-ghost hover:text-ink hover:border-ink disabled:opacity-30">
            {exporting ? <Loader2 size={10} className="animate-spin" /> : <Download size={10} />} Excel
          </button>
        </div>
        {/* Search + tag filter */}
        <div className="px-2 py-1.5 border-b border-border bg-canvas-alt/30 flex items-center gap-1 flex-wrap">
          <div className="flex items-center gap-1 border border-border bg-white px-1.5 py-0.5 flex-1 min-w-0">
            <Search size={10} className="text-ink-ghost flex-shrink-0" />
            <input
              value={searchQuery}
              onChange={e => setSearchQuery(e.target.value)}
              placeholder="搜索问题"
              className="font-mono text-[10px] outline-none bg-transparent flex-1 min-w-0"
            />
            {searchQuery && (
              <button onClick={() => setSearchQuery('')} className="text-ink-ghost hover:text-accent flex-shrink-0">
                <X size={9} />
              </button>
            )}
          </div>
        </div>
        {/* Per-run mark filter — quick way to bisect "what did A get wrong that
           B got right" by stacking ✗ on one run with ✓ on another. */}
        <div className="px-2 py-1.5 border-b border-border bg-violet-50/60 space-y-0.5">
          <div className="flex items-center gap-1 mb-1">
            <span className="font-mono text-[8px] font-bold tracking-wider text-violet-700">
              按版本标注筛选
            </span>
            <span className="font-mono text-[8px] text-ink-ghost">
              · 同行 OR · 跨行 AND
            </span>
            <span className="flex-1" />
            {hasMarkFilter && (
              <button
                onClick={() => setMarkFilters({})}
                className="font-mono text-[8px] text-accent hover:underline"
              >
                清除
              </button>
            )}
          </div>
          {data.runs.map((run, idx) => {
            const set: Set<MarkKey> = markFilters[run.id] || new Set()
            const counts = {
              correct: data.rows.filter(r => r.results[idx]?.mark === 'correct').length,
              incorrect: data.rows.filter(r => r.results[idx]?.mark === 'incorrect').length,
              unmarked: data.rows.filter(r => markKeyOf(r.results[idx]?.mark) === 'unmarked').length,
            }
            const label = labels[idx]
            return (
              <div key={run.id} className="flex items-center gap-1">
                <span
                  className="font-mono text-[9px] font-bold text-ink-ghost w-7 shrink-0 truncate"
                  title={`${run.title}${run.modelName ? ' · ' + run.modelName : ''}`}
                >
                  [{label}]
                </span>
                <button
                  onClick={() => toggleMarkFilter(run.id, 'correct')}
                  className={`flex-1 border px-1 py-0.5 font-mono text-[9px] font-bold leading-none ${
                    set.has('correct')
                      ? 'bg-green-600 text-white border-green-600'
                      : 'bg-white text-green-700 border-green-300 hover:border-green-600'
                  }`}
                  title={`只看 ${label}（${run.title}）标为正确的（${counts.correct}）`}
                >
                  ✓ {counts.correct}
                </button>
                <button
                  onClick={() => toggleMarkFilter(run.id, 'incorrect')}
                  className={`flex-1 border px-1 py-0.5 font-mono text-[9px] font-bold leading-none ${
                    set.has('incorrect')
                      ? 'bg-red-600 text-white border-red-600'
                      : 'bg-white text-red-700 border-red-300 hover:border-red-600'
                  }`}
                  title={`只看 ${label}（${run.title}）标为错误的（${counts.incorrect}）`}
                >
                  ✗ {counts.incorrect}
                </button>
                <button
                  onClick={() => toggleMarkFilter(run.id, 'unmarked')}
                  className={`flex-1 border px-1 py-0.5 font-mono text-[9px] font-bold leading-none ${
                    set.has('unmarked')
                      ? 'bg-amber-500 text-white border-amber-500'
                      : 'bg-white text-amber-700 border-amber-300 hover:border-amber-600'
                  }`}
                  title={`只看 ${label}（${run.title}）尚未标注的（${counts.unmarked}）`}
                >
                  ○ {counts.unmarked}
                </button>
              </div>
            )
          })}
        </div>
        {suiteTags.length > 0 && (
          <div className="px-2 py-1.5 border-b border-border bg-canvas-alt/30 flex items-center gap-1 flex-wrap">
            {suiteTags.map(t => {
              const active = filterTagIds.has(t.id)
              return (
                <button
                  key={t.id}
                  onClick={() => toggleFilterTag(t.id)}
                  className={`inline-flex items-center gap-0.5 border-2 px-1 py-0.5 font-mono text-[9px] font-bold leading-none ${
                    active ? 'bg-ink text-white border-ink' : 'bg-white text-ink border-ink hover:bg-accent/10'
                  }`}
                >
                  <span className="text-accent">#</span>
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
                className="font-mono text-[8px] text-accent hover:underline"
              >
                清除
              </button>
            )}
          </div>
        )}
        <div className="flex-1 overflow-y-auto">
          {filteredRows.length === 0 ? (
            <div className="flex h-24 items-center justify-center font-mono text-xs text-ink-ghost">
              {data.rows.length === 0 ? '无对比数据' : '当前筛选下无匹配问题'}
            </div>
          ) : (
            filteredRows.map(row => {
              const isActive = row.caseId === selectedCaseId
              return (
                <button
                  key={row.caseId}
                  onClick={() => setSelectedCaseId(row.caseId)}
                  className={`block w-full text-left border-b border-border px-3 py-2 hover:bg-canvas-alt/40 transition-colors ${
                    isActive ? 'bg-canvas-alt' : ''
                  }`}
                >
                  <div className="flex items-start gap-2">
                    <span className="font-mono text-[9px] text-ink-ghost shrink-0 w-8 mt-0.5">
                      {row.code}
                    </span>
                    <span className="font-mono text-[11px] text-ink line-clamp-2 flex-1">
                      {row.question}
                    </span>
                  </div>
                  <div className="flex items-center flex-wrap gap-1 mt-1.5 ml-10">
                    {data.runs.map((_, i) => {
                      const result = row.results[i] ?? null
                      const status = result?.executionStatus ?? ''
                      const win = isLoneSuccess(i, status, row.results)
                      const loss = isLoneFailure(i, status, row.results)
                      return (
                        <RunSlot key={i} label={labels[i]} result={result} win={win} loss={loss} />
                      )
                    })}
                  </div>
                </button>
              )
            })
          )}
        </div>
      </div>

      {/* Right: Excel-style split — N panels, lazy-loaded detail */}
      <div className="flex-1 min-w-0">
        {selected ? (
          <SplitCompare
            row={selected}
            runs={data.runs}
            labels={labels}
            detail={detailForSelected}
            loading={isLoadingDetail}
          />
        ) : (
          <div className="flex h-full items-center justify-center font-mono text-xs text-ink-ghost">
            选择左侧问题查看对比
          </div>
        )}
      </div>
    </div>
  )
}

/* ───────────────────────────── Helpers ─────────────────────────────────── */

// A, B, C, ..., Z, R27, R28, ... (>26 runs is unrealistic but cheap to handle).
function labelsFor(n: number): string[] {
  const out: string[] = []
  for (let i = 0; i < n; i++) {
    out.push(i < 26 ? String.fromCharCode(65 + i) : `R${i + 1}`)
  }
  return out
}

function isLoneSuccess(idx: number, status: string, results: (RunCase | null)[]): boolean {
  if (status !== 'success') return false
  return results.every((r, j) => j === idx || r?.executionStatus === 'error')
}

function isLoneFailure(idx: number, status: string, results: (RunCase | null)[]): boolean {
  if (status !== 'error') return false
  return results.every((r, j) => j === idx || r?.executionStatus === 'success')
}

/* ───────────────────────── Question-list run badge ───────────────────────── */

function RunSlot({
  label,
  result,
  win,
  loss,
}: {
  label: string
  result: RunCase | null
  win: boolean
  loss: boolean
}) {
  return (
    <span
      className={`inline-flex items-center gap-1 border px-1.5 py-0.5 ${
        win ? 'border-green-500' : loss ? 'border-red-500' : 'border-border'
      }`}
    >
      <span className="font-mono text-[8px] font-bold tracking-wider text-ink-ghost">
        {label}
      </span>
      {result ? (
        <>
          <CaseStatusBadge status={result.status} execStatus={result.executionStatus} />
          <MarkIndicator mark={result.mark} />
          {result.note && <StickyNote size={9} className="text-amber-600" />}
        </>
      ) : (
        <span className="font-mono text-[8px] text-ink-ghost">—</span>
      )}
    </span>
  )
}

/* ───────────────────────── Excel-style split compare ────────────────────────
 * N panels rendered SIDE-BY-SIDE inside a horizontally scrollable container.
 * Each panel's body uses the lazily-fetched detail entry when available; until
 * detail arrives, a centered loader is shown (summary status is already on the
 * question-list badge — no need to duplicate it here).
 * ─────────────────────────────────────────────────────────────────────── */

const PANEL_MIN_PX = 640

function SplitCompare({
  row,
  runs,
  labels,
  detail,
  loading,
}: {
  row: CompareRow
  runs: TestRun[]
  labels: string[]
  detail: (RunCase | null)[] | undefined
  loading: boolean
}) {
  const totalMin = runs.length * PANEL_MIN_PX
  return (
    <div className="flex flex-col h-full min-h-0">
      {/* Header: question — enlarged for readability across the N-way split */}
      <div className="border-b-2 border-ink bg-white px-4 py-3 flex items-center gap-3">
        <span className="font-mono text-[10px] text-ink-ghost font-bold tracking-wider shrink-0 border border-ink/40 px-1.5 py-0.5">
          {row.code}
        </span>
        <h2 className="font-display text-lg font-bold text-ink flex-1 leading-snug">
          {row.question}
        </h2>
        <span className="font-mono text-[10px] text-ink-ghost shrink-0">
          ← 横向滚动对比 {runs.length} 个 Run →
        </span>
      </div>

      {/* Excel-style split */}
      <div className="flex-1 overflow-x-auto overflow-y-hidden bg-canvas">
        <div className="flex h-full" style={{ minWidth: `${totalMin}px` }}>
          {runs.map((run, i) => {
            const summary = row.results[i] ?? null
            const full = detail?.[i] ?? null
            // Detail wins when present; otherwise summary still gives status/mark.
            const merged: RunCase | null = full ?? summary
            const status = merged?.executionStatus ?? ''
            const win = isLoneSuccess(i, status, row.results)
            const loss = isLoneFailure(i, status, row.results)
            const isLast = i === runs.length - 1
            return (
              <div
                key={run.id}
                className={`basis-0 grow min-w-[640px] flex flex-col ${
                  isLast ? '' : 'border-r-2 border-border'
                }`}
              >
                <PanelHeader run={run} label={labels[i]} win={win} loss={loss} />
                <div className="flex-1 overflow-y-auto">
                  <PanelBody result={merged} loading={loading && !full} />
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

function PanelHeader({
  run,
  label,
  win,
  loss,
}: {
  run: TestRun
  label: string
  win: boolean
  loss: boolean
}) {
  return (
    <div className="flex items-center gap-2 px-4 py-2 border-b border-border bg-canvas-alt sticky top-0 z-10">
      <span className="font-mono text-[9px] font-bold tracking-wider text-ink-ghost">
        [{label}]
      </span>
      <span className="font-display text-sm font-bold text-ink truncate">{run.title}</span>
      <span className="font-mono text-[10px] text-ink-ghost truncate">· {run.modelName}</span>
      <span className="flex-1" />
      {win && (
        <span className="font-mono text-[8px] font-bold text-green-700 border border-green-500 px-1 shrink-0">
          WIN
        </span>
      )}
      {loss && (
        <span className="font-mono text-[8px] font-bold text-red-700 border border-red-500 px-1 shrink-0">
          LOSS
        </span>
      )}
    </div>
  )
}

function PanelBody({ result, loading }: { result: RunCase | null; loading: boolean }) {
  if (loading) {
    return (
      <div className="px-5 py-12 flex flex-col items-center gap-2 text-ink-ghost">
        <Loader2 size={18} className="animate-spin text-accent" />
        <span className="font-mono text-[10px]">加载详情中...</span>
      </div>
    )
  }
  return (
    <div className="px-5 py-4">
      {result && (result.status === 'completed' || result.status === 'error' || result.status === 'cancelled') ? (
        <div className="space-y-3">
          <div className="flex items-center gap-3 flex-wrap">
            <CaseStatusBadge status={result.status} execStatus={result.executionStatus} />
            <MarkIndicator mark={result.mark} />
            {result.durationMs > 0 && (
              <span className="font-mono text-[10px] text-ink-ghost">
                {(result.durationMs / 1000).toFixed(1)}s
              </span>
            )}
          </div>
          {(result.mark || result.note) && (
            <div className="border border-border bg-canvas-alt px-3 py-2 space-y-1.5">
              {result.mark && (
                <div className="flex items-center gap-1.5">
                  <span className="font-mono text-[8px] text-ink-ghost font-bold tracking-wider">
                    标注:
                  </span>
                  {result.mark === 'correct' ? (
                    <span className="font-mono text-[10px] text-green-700 font-bold">✓ 正确</span>
                  ) : (
                    <span className="font-mono text-[10px] text-red-700 font-bold">✗ 错误</span>
                  )}
                </div>
              )}
              {result.note && (
                <div>
                  <div className="font-mono text-[8px] text-ink-ghost font-bold tracking-wider mb-1">
                    备注:
                  </div>
                  <div className="font-mono text-[11px] text-ink whitespace-pre-wrap">
                    {result.note}
                  </div>
                </div>
              )}
            </div>
          )}
          <CaseDetailBody tc={result} />
        </div>
      ) : (
        <div className="font-mono text-xs text-ink-ghost py-8 text-center border border-dashed border-border">
          无结果
        </div>
      )}
    </div>
  )
}
