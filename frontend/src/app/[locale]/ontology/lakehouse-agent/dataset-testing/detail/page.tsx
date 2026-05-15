'use client'

import { Suspense, useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { useSearchParams } from 'next/navigation'
import { api, getApiBase, getApiBaseFor } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { CyberLoader } from '@/components/ui/CyberLoader'
import { ArrowLeft, FlaskConical, GitBranch, ListChecks } from 'lucide-react'
import { StatusBadge, RunCase, FunctionCall, Tag } from '../components/SharedCaseBits'
import { SuiteQuestionsBar } from '../components/SuiteQuestionsBar'
import { VersionSidebar, TestRun } from '../components/VersionSidebar'
import { RunCaseList } from '../components/RunCaseList'
import { CompareView, CompareRow } from '../components/CompareView'
import { CaseDetailPanel } from '../components/CaseDetailPanel'
import { QuestionsEditor } from '../components/QuestionsEditor'

type TabKey = 'runs' | 'questions'

interface TestSuite {
  id: string
  name: string
  status: string
  total: number
  passed: number
  failed: number
  concurrency: number
  caseCount: number
  createdAt: string
  lastRunAt?: string
}

interface RunDetail extends TestRun {
  cases: RunCase[]
  cancelRequested?: boolean
}

// Reference-preserving merge of a freshly-fetched RunDetail over the currently-
// held one. Rows whose substantive fields haven't changed keep their previous
// object reference, so React's reconciler can skip re-rendering that row's
// (potentially heavy) children — no more "click retry → whole list flashes".
//
// Different run id (or no prior state) → full replace. Within the same run,
// only rows with actual field drift get a new reference. Scalar fields (status,
// mark, durationMs, ...) are cheap to compare every tick.
//
// The fresh payload is typically the lite listing (no functionCalls /
// generatedSql / executionResult / executionError) — when the prior case has
// those fields cached from a single-case detail fetch, preserve them onto the
// merged row so the right-hand panel doesn't flicker to empty on every poll.
// Only kept when the scalar fingerprint matches; if the case was re-run, the
// stale heavy fields must be dropped so the detail panel re-fetches.
function mergeRunDetail(prev: RunDetail | null, fresh: RunDetail): RunDetail {
  if (!prev || prev.id !== fresh.id) return fresh
  const byId = new Map<string, RunCase>()
  for (const pc of prev.cases) byId.set(pc.id, pc)
  const mergedCases: RunCase[] = fresh.cases.map(fc => {
    const pc = byId.get(fc.id)
    if (!pc) return fc
    const sameRun =
      pc.status === fc.status &&
      pc.executionStatus === fc.executionStatus &&
      pc.finalAnswer === fc.finalAnswer &&
      pc.durationMs === fc.durationMs &&
      pc.modelName === fc.modelName &&
      pc.totalTokens === fc.totalTokens
    // Preserve heavy fields already fetched for the detail panel across lite
    // listing refreshes — empty string / undefined on `fc` means "not included
    // in lite payload", not "wiped on the server".
    const hydrated: RunCase = { ...fc }
    if (sameRun) {
      if (!fc.functionCalls && pc.functionCalls) hydrated.functionCalls = pc.functionCalls
      if (!fc.generatedSql && pc.generatedSql) hydrated.generatedSql = pc.generatedSql
      if (!fc.executionResult && pc.executionResult) hydrated.executionResult = pc.executionResult
      if (!fc.executionError && pc.executionError) hydrated.executionError = pc.executionError
    }
    if (
      sameRun &&
      pc.mark === fc.mark &&
      pc.note === fc.note &&
      pc.functionCalls === hydrated.functionCalls &&
      pc.generatedSql === hydrated.generatedSql &&
      pc.executionResult === hydrated.executionResult &&
      pc.executionError === hydrated.executionError
    ) {
      return pc
    }
    return hydrated
  })
  return { ...fresh, cases: mergedCases }
}

// Lightweight payload returned by GET /api/ontology/lh-test-runs/{id}/progress.
// Polled every couple seconds during an active run; full RunDetail only loads
// when the status changes or completedCount jumps so we don't pull the case
// array every tick.
interface RunProgressLite {
  status: string
  totalCount: number
  completedCount: number
  errorCount: number
  cancelRequested: boolean
}

export default function DatasetDetailPage() {
  return (
    <Suspense fallback={<div className="flex h-64 items-center justify-center"><CyberLoader /></div>}>
      <DetailInner />
    </Suspense>
  )
}

function DetailInner() {
  const t = useTranslations('agent.dataset_testing')
  const searchParams = useSearchParams()
  const router = useRouter()
  const suiteId = searchParams.get('suiteId') || ''
  const { currentProject } = useProject()
  const msg = useMessage()

  // Suite + cases
  const [suite, setSuite] = useState<TestSuite | null>(null)
  const [suiteLoading, setSuiteLoading] = useState(true)

  // Run state
  const [runs, setRuns] = useState<TestRun[]>([])
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null)
  const [runDetail, setRunDetail] = useState<RunDetail | null>(null)

  // New run form
  const [showNewRun, setShowNewRun] = useState(false)
  const [newRunTitle, setNewRunTitle] = useState('')
  const [newRunConcurrency, setNewRunConcurrency] = useState(3)

  // Run execution (polling-based)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const [retryingCaseIds, setRetryingCaseIds] = useState<Set<string>>(new Set())
  const [selectedCaseId, setSelectedCaseId] = useState<string | null>(null)

  // Batch AI judge (runs the per-case /ai-judge endpoint with limited concurrency
  // for every case that has a non-empty finalAnswer). Tracked here (not in
  // RunCaseList) because handleCaseUpdate lives at this level so the right-hand
  // CaseDetailPanel reflects each verdict the moment the API returns.
  // `inFlight` doubles as a per-case lock — both the batch worker and the
  // single-case AI Judge button consult it so the same rcID is never judged
  // twice in parallel (which would race the note + mark UPDATE on the server).
  const [judgeProgress, setJudgeProgress] = useState<{ current: number; total: number; concurrency: number } | null>(null)
  const judgeCancelRef = useRef(false)
  const judgeInFlightRef = useRef<Set<string>>(new Set())

  // Compare — N-way (≥2)
  const [compareMode, setCompareMode] = useState(false)
  const [compareRunIds, setCompareRunIds] = useState<string[]>([])
  const [compareData, setCompareData] = useState<{ runs: TestRun[]; rows: CompareRow[] } | null>(null)

  // Export tracking
  const [exportingSuites, setExportingSuites] = useState<Set<string>>(new Set())

  // Suite tag dictionary (suite-scoped, shared across runs for this suite)
  const [suiteTags, setSuiteTags] = useState<Tag[]>([])

  // Active top-level tab. Persisted in the URL so deep-links / refresh keep state.
  const initialTab: TabKey = (searchParams.get('tab') as TabKey) === 'questions' ? 'questions' : 'runs'
  const [activeTab, setActiveTab] = useState<TabKey>(initialTab)
  const switchTab = (t: TabKey) => {
    setActiveTab(t)
    const params = new URLSearchParams(searchParams.toString())
    if (t === 'questions') params.set('tab', 'questions'); else params.delete('tab')
    router.replace(`/ontology/lakehouse-agent/dataset-testing/detail?${params.toString()}`)
  }

  // ======================== Loaders ========================

  const loadSuite = useCallback(async () => {
    if (!suiteId) return
    setSuiteLoading(true)
    try {
      const res = await api<{ data: TestSuite[] }>(`/ontology/lh-test-suites?projectId=${currentProject?.id || ''}`)
      const found = (res.data || []).find(s => s.id === suiteId)
      if (!found) { msg.error(t('detail.suite_not_found')); return }
      setSuite(found)
    } catch { msg.error(t('detail.suite_load_fail')) }
    finally { setSuiteLoading(false) }
  }, [suiteId, currentProject, msg])

  const stopPolling = useCallback(() => {
    if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }
  }, [])

  // lastProgressRef holds the most recent progress snapshot so the polling
  // loop can detect when full RunDetail re-fetch is actually needed (status
  // changed, completedCount advanced, or cancel flag flipped). Without this,
  // every 2.5s tick pulled the entire case array — wasted bandwidth on big
  // suites + UI churn on every poll.
  const lastProgressRef = useRef<RunProgressLite | null>(null)

  const loadRunDetail = useCallback(async (runId: string, opts?: { silent?: boolean }) => {
    try {
      // lite=1 drops the heavy per-case payload (functionCalls/SQL/result) so
      // the list view loads ~200 KB instead of ~2.6 MB. CaseDetailPanel pulls
      // the heavy fields on demand via the single-case endpoint.
      const res = await api<RunDetail>(`/ontology/lh-test-suites/${suiteId}/runs/${runId}?lite=1`)
      setRunDetail(prev => mergeRunDetail(prev, res))
      // Seed the progress baseline so the polling loop's first comparison
      // against the lightweight endpoint reflects what we just rendered.
      lastProgressRef.current = {
        status: res.status,
        totalCount: res.total,
        completedCount: res.completedCount,
        errorCount: res.errorCount,
        cancelRequested: res.cancelRequested || false,
      }
      // Auto-start polling if the run is active. Polling hits the lightweight
      // /progress endpoint (~100B JSON) and only re-fetches the full RunDetail
      // when something interesting changes.
      if (res.status === 'queued' || res.status === 'running') {
        if (!pollRef.current) {
          pollRef.current = setInterval(async () => {
            try {
              const poll = await api<RunProgressLite>(`/ontology/lh-test-runs/${runId}/progress`)
              const prev = lastProgressRef.current
              const statusChanged = !prev || prev.status !== poll.status
              const completedChanged = !prev || prev.completedCount !== poll.completedCount
              const cancelChanged = !prev || prev.cancelRequested !== poll.cancelRequested
              lastProgressRef.current = poll

              // Mirror cancelRequested + counts onto runDetail without a fetch
              // so the cancel button transitions to "中止中…" immediately.
              setRunDetail(prevDetail => prevDetail ? {
                ...prevDetail,
                status: poll.status,
                completedCount: poll.completedCount,
                errorCount: poll.errorCount,
                total: poll.totalCount,
                cancelRequested: poll.cancelRequested,
              } : prevDetail)

              if (poll.status !== 'queued' && poll.status !== 'running') {
                stopPolling()
                // Final state: pull the full detail one more time so the case
                // array reflects the terminal status of every row. Merge (not
                // replace) so un-changed rows keep their references and skip
                // re-render — otherwise the whole list flashes at run-end.
                try {
                  const final = await api<RunDetail>(`/ontology/lh-test-suites/${suiteId}/runs/${runId}?lite=1`)
                  setRunDetail(prev => mergeRunDetail(prev, final))
                } catch { /* keep last polled view */ }
                loadRuns(false)
                if (poll.status === 'cancelled') {
                  msg.success(t('detail.test_cancel_success', { completed: poll.completedCount, total: poll.totalCount }))
                } else {
                  msg.success(t('detail.test_complete_success', { completed: poll.completedCount, total: poll.totalCount }))
                }
                return
              }

              // Mid-run refresh of full detail only when something the cases
              // grid cares about changed — not on every tick. Merged so rows
              // that didn't actually change skip re-render; only the 1-2 cases
              // whose status just flipped get a new reference.
              if (statusChanged || completedChanged || cancelChanged) {
                try {
                  const fresh = await api<RunDetail>(`/ontology/lh-test-suites/${suiteId}/runs/${runId}?lite=1`)
                  setRunDetail(prev => mergeRunDetail(prev, fresh))
                } catch { /* keep prior detail */ }
              }
            } catch { /* polling error, keep trying */ }
          }, 2500)
        }
      } else {
        stopPolling()
      }
    } catch { if (!opts?.silent) msg.error(t('detail.run_load_fail')) }
  }, [suiteId, msg, stopPolling]) // eslint-disable-line react-hooks/exhaustive-deps

  const loadRuns = useCallback(async (autoSelect: boolean) => {
    if (!suiteId) return
    try {
      const res = await api<{ data: TestRun[] }>(`/ontology/lh-test-suites/${suiteId}/runs`)
      const runList = res.data || []
      setRuns(runList)
      if (autoSelect) {
        const defaultRun = runList.find(r => r.isDefault) || runList[0]
        if (defaultRun) {
          setSelectedRunId(defaultRun.id)
          loadRunDetail(defaultRun.id)
        } else {
          setSelectedRunId(null)
          setRunDetail(null)
        }
      }
    } catch { /* silent */ }
  }, [suiteId, loadRunDetail])

  // Tag dictionary loader — refreshed on mount and after any mutation so
  // chip autocomplete + filter bar counts stay consistent.
  const loadSuiteTags = useCallback(async () => {
    if (!suiteId) return
    try {
      const res = await api<{ data: Tag[] }>(`/ontology/lh-test-suites/${suiteId}/tags`)
      setSuiteTags(res.data || [])
    } catch { /* silent */ }
  }, [suiteId])

  // Per-case tag replace. Backend accepts `tagNames` and upserts into the dict.
  const onCaseTagsChange = useCallback(async (caseId: string, tagNames: string[]) => {
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/cases/${caseId}/tags`, {
        method: 'PUT',
        body: { tagNames },
      })
      // Reload suite tags (new names may have been created) + current run detail to refresh case.tags
      loadSuiteTags()
      if (selectedRunId) loadRunDetail(selectedRunId, { silent: true })
    } catch { msg.error(t('detail.tag_save_fail')) }
  }, [suiteId, selectedRunId, loadSuiteTags, loadRunDetail, msg])

  // Bulk add/remove across multiple cases.
  const onBulkTag = useCallback(async (caseIds: string[], add: string[], remove: string[]) => {
    if (caseIds.length === 0 || (add.length === 0 && remove.length === 0)) return
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/cases/bulk-tag`, {
        method: 'POST',
        body: { caseIds, add, remove },
      })
      loadSuiteTags()
      if (selectedRunId) loadRunDetail(selectedRunId, { silent: true })
      const parts: string[] = []
      if (add.length > 0) parts.push(`+${add.length}`)
      if (remove.length > 0) parts.push(`-${remove.length}`)
      msg.success(t('detail.bulk_tag_success', { count: caseIds.length, parts: parts.join(' ') }))
    } catch { msg.error(t('detail.bulk_tag_fail')) }
  }, [suiteId, selectedRunId, loadSuiteTags, loadRunDetail, msg])

  useEffect(() => { loadSuite() }, [loadSuite])
  useEffect(() => { loadRuns(true) }, [loadRuns])
  useEffect(() => { loadSuiteTags() }, [loadSuiteTags])
  useEffect(() => () => stopPolling(), [stopPolling])

  // ======================== Question management ========================

  const reloadAfterQuestionChange = () => {
    loadSuite()
    // If a run is selected, also reload its detail (question count shown in header uses suite, but this keeps them in sync)
    if (selectedRunId) loadRunDetail(selectedRunId)
  }

  const onUploadFile = async (file: File) => {
    const formData = new FormData()
    formData.append('file', file)
    const token = localStorage.getItem('lakehouse2ontology_token')
    try {
      const res = await fetch(`${getApiBaseFor('/ontology/lh-test-suites')}/ontology/lh-test-suites/${suiteId}/upload`, {
        method: 'POST',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        body: formData,
      })
      const data = await res.json()
      msg.success(t('detail.import_success', { count: data.inserted }))
      reloadAfterQuestionChange()
    } catch { msg.error(t('detail.import_fail')) }
  }

  const onAddPasted = async (questions: string[]) => {
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/add-questions`, {
        method: 'POST',
        body: { questions },
      })
      msg.success(t('detail.add_success', { count: questions.length }))
      reloadAfterQuestionChange()
    } catch { msg.error(t('detail.add_fail')) }
  }

  // ======================== Run actions ========================

  const createRun = async () => {
    if (!newRunTitle.trim()) return
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/runs`, {
        method: 'POST',
        body: {
          title: newRunTitle.trim(),
          concurrency: newRunConcurrency,
        },
      })
      msg.success(t('detail.run_created'))
      setNewRunTitle('')
      setShowNewRun(false)
      loadRuns(false)
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('detail.create_run_fail'))
    }
  }

  const setDefaultRun = async (runId: string) => {
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/runs/${runId}/default`, { method: 'PUT' })
      loadRuns(false)
    } catch { msg.error(t('detail.set_default_fail')) }
  }

  const deleteRun = async (runId: string) => {
    if (!confirm(t('detail.delete_run_confirm'))) return
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/runs/${runId}`, { method: 'DELETE' })
      if (selectedRunId === runId) {
        setSelectedRunId(null)
        setRunDetail(null)
      }
      loadRuns(false)
    } catch { msg.error(t('detail.delete_run_fail')) }
  }

  const selectRun = (runId: string) => {
    setSelectedRunId(runId)
    setSelectedCaseId(null)
    setCompareMode(false)
    setCompareData(null)
    loadRunDetail(runId)
  }

  // ======================== Run execution (queue + polling) ========================

  const runTestRun = async () => {
    if (!selectedRunId) return
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/runs/${selectedRunId}/run`, { method: 'POST' })
      loadRunDetail(selectedRunId)
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('detail.launch_fail'))
    }
  }

  const retryErrors = async () => {
    if (!selectedRunId) return
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/runs/${selectedRunId}/retry-errors`, { method: 'POST' })
      loadRunDetail(selectedRunId)
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('detail.retry_fail'))
    }
  }

  // bulkRetry — reset a caller-supplied set of run-case ids back to pending and
  // re-queue this run. Powers the "重跑选中" button (checkbox-driven) and the
  // "重跑标错" shortcut (mark='incorrect' driven). Selection is resolved in
  // RunCaseList; this layer just POSTs and refreshes.
  const bulkRetry = async (rcIds: string[]) => {
    if (!selectedRunId || rcIds.length === 0) return
    try {
      const res = await api<{ status: string; resetCount: number }>(
        `/ontology/lh-test-suites/${suiteId}/runs/${selectedRunId}/bulk-retry`,
        { method: 'POST', body: { rcIds } },
      )
      msg.success(t('detail.bulk_reset_success', { count: res.resetCount }))
      loadRunDetail(selectedRunId)
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('detail.bulk_retry_fail'))
    }
  }

  const continueRun = async () => {
    // Same as runTestRun — re-queue a run that has remaining pending cases
    await runTestRun()
  }

  // cancelRun — cooperative stop. Backend flips cancel_requested=true; the
  // worker drains in-flight cases (LLM calls aren't interruptible) and marks
  // remaining pending cases as 'cancelled'. Polling notices the status change
  // and updates the UI without an explicit reload here.
  const cancelRun = async () => {
    if (!selectedRunId) return
    try {
      await api(`/ontology/lh-test-runs/${selectedRunId}/cancel`, { method: 'POST' })
      // Optimistically reflect the cancel-requested state so the button shows
      // 中止中… immediately even before the next progress poll.
      setRunDetail(prev => prev ? { ...prev, cancelRequested: true } : prev)
      msg.success(t('detail.cancel_success'))
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('detail.cancel_fail'))
    }
  }

  // ======================== Retry ========================

  const retryRunCase = async (rcId: string) => {
    if (!selectedRunId) return
    setRetryingCaseIds(prev => { const n = new Set(prev); n.add(rcId); return n })
    setRunDetail(prev => prev ? {
      ...prev,
      cases: prev.cases.map(c => c.id === rcId ? { ...c, status: 'running' } : c),
    } : null)
    try {
      const res = await api<{
        status: string; generatedSql: string; executionStatus: string;
        executionResult: string; executionError: string; finalAnswer: string;
        functionCalls?: FunctionCall[]; durationMs: number; modelName: string;
      }>(`/ontology/lh-test-suites/${suiteId}/runs/${selectedRunId}/cases/${rcId}/retry`, { method: 'POST' })
      setRunDetail(prev => prev ? {
        ...prev,
        cases: prev.cases.map(c => c.id === rcId ? {
          ...c,
          status: res.status || 'completed',
          generatedSql: res.generatedSql || '',
          executionStatus: res.executionStatus || '',
          executionResult: res.executionResult || '',
          executionError: res.executionError || '',
          finalAnswer: res.finalAnswer || '',
          functionCalls: res.functionCalls || [],
          durationMs: res.durationMs || 0,
          modelName: res.modelName || '',
        } : c),
      } : null)
      msg.success(t('detail.retry_case_success'))
      loadRuns(false)
    } catch {
      msg.error(t('detail.retry_case_fail'))
      if (selectedRunId) loadRunDetail(selectedRunId)
    } finally {
      setRetryingCaseIds(prev => { const n = new Set(prev); n.delete(rcId); return n })
    }
  }

  // ======================== Compare ========================

  const loadCompare = async () => {
    if (compareRunIds.length < 2) return
    try {
      const res = await api<{ runs: TestRun[]; rows: CompareRow[] }>(
        `/ontology/lh-test-suites/${suiteId}/compare?runs=${compareRunIds.join(',')}`,
      )
      setCompareData(res)
    } catch { msg.error(t('detail.compare_load_fail')) }
  }

  const toggleCompareRun = (runId: string) => {
    setCompareRunIds(prev =>
      prev.includes(runId) ? prev.filter(id => id !== runId) : [...prev, runId],
    )
  }

  // ======================== Export ========================

  const downloadRunExport = async (format: 'csv' | 'xlsx') => {
    if (!selectedRunId || !runDetail) return
    const key = `run:${selectedRunId}:${format}`
    setExportingSuites(prev => new Set(prev).add(key))
    try {
      const token = localStorage.getItem('lakehouse2ontology_token')
      const res = await fetch(
        `${getApiBaseFor('/ontology/lh-test-suites')}/ontology/lh-test-suites/${suiteId}/runs/${selectedRunId}/export?format=${format}`,
        { headers: token ? { Authorization: `Bearer ${token}` } : {} },
      )
      if (!res.ok) { msg.error(t('detail.download_fail', { status: res.status })); return }
      let filename = `${runDetail.title}.${format}`
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
    } catch { msg.error(t('detail.download_fail_generic')) }
    finally {
      setExportingSuites(prev => { const n = new Set(prev); n.delete(key); return n })
    }
  }

  // ======================== Case update (annotation) ========================

  const handleCaseUpdate = (rcId: string, patch: Partial<RunCase>) => {
    setRunDetail(prev => prev ? {
      ...prev,
      cases: prev.cases.map(c => c.id === rcId ? { ...c, ...patch } : c),
    } : null)
  }

  // ======================== Batch AI judge ========================

  // Single-case judge gate, shared with CaseDetailPanel so the per-case AI Judge
  // button can't race the batch worker on the same rcID. Returns false if the
  // caller should skip (already in flight or batch is busy with this case).
  const claimJudge = useCallback((rcId: string) => {
    if (judgeInFlightRef.current.has(rcId)) return false
    judgeInFlightRef.current.add(rcId)
    return true
  }, [])
  const releaseJudge = useCallback((rcId: string) => {
    judgeInFlightRef.current.delete(rcId)
  }, [])

  // Iterates over every run-case that has a non-empty finalAnswer and asks the
  // backend to judge it. AI judge is a single lightweight LLM call per case
  // (no smartquery / lookup loop), so the default parallelism is intentionally
  // higher than the test runner's suite.concurrency. Tweak BATCH_JUDGE_CONCURRENCY
  // here if your judge endpoint needs throttling. Each case is claimed via
  // judgeInFlightRef so the single-case button can't double-fire.
  const BATCH_JUDGE_CONCURRENCY = 5
  // selectedCaseIds — when supplied (non-empty) only those master case_ids are
  // considered. Cases without finalAnswer are still filtered out either way
  // because the judge can't rule on them. RunCaseList passes the user's
  // checkbox selection here; an empty/undefined list means "judge everything".
  const runBatchJudge = async (selectedCaseIds?: string[]) => {
    if (!selectedRunId || !runDetail) return
    if (judgeProgress) return
    const useSelection = selectedCaseIds && selectedCaseIds.length > 0
    const selectionSet = useSelection ? new Set(selectedCaseIds) : null
    const candidates = runDetail.cases.filter(c => {
      if (selectionSet && !selectionSet.has(c.caseId)) return false
      return (c.finalAnswer || '').trim() !== ''
    })
    if (candidates.length === 0) {
      msg.error(useSelection
        ? t('detail.judge_no_answer_selection')
        : t('detail.judge_no_answer_all'))
      return
    }
    const concurrency = Math.max(1, Math.min(10, BATCH_JUDGE_CONCURRENCY))
    judgeCancelRef.current = false
    setJudgeProgress({ current: 0, total: candidates.length, concurrency })

    let cursor = 0
    let done = 0
    let failed = 0
    let skipped = 0

    const worker = async () => {
      while (true) {
        if (judgeCancelRef.current) return
        const idx = cursor++
        if (idx >= candidates.length) return
        const tc = candidates[idx]
        // Skip cases the per-case AI Judge button is already running. The
        // single-case path will update mark/note on its own; double-firing
        // would race the same rcID's UPDATE.
        if (!claimJudge(tc.id)) {
          skipped++
          done++
          setJudgeProgress({ current: done, total: candidates.length, concurrency })
          continue
        }
        try {
          const res = await api<{ verdict: 'correct' | 'incorrect' | 'unknown'; reason: string; mark: string; note: string }>(
            `/ontology/lh-test-suites/${suiteId}/runs/${selectedRunId}/cases/${tc.id}/ai-judge`,
            { method: 'POST' },
          )
          const patch: Partial<RunCase> = { note: res.note || undefined }
          if (res.mark === 'correct' || res.mark === 'incorrect') {
            patch.mark = res.mark
          } else if (res.verdict === 'unknown') {
            // AI 无法判断 → 清空 mark，回落到 ⏳ 待判定；与后端保持一致
            patch.mark = undefined
          }
          handleCaseUpdate(tc.id, patch)
        } catch {
          failed++
        } finally {
          releaseJudge(tc.id)
        }
        done++
        setJudgeProgress({ current: done, total: candidates.length, concurrency })
      }
    }

    await Promise.all(Array.from({ length: Math.min(concurrency, candidates.length) }, () => worker()))
    setJudgeProgress(null)
    const succeeded = done - failed - skipped
    if (judgeCancelRef.current) {
      msg.success(t('detail.judge_stopped', { succeeded, total: candidates.length, failed, skipped }))
    } else if (failed > 0 || skipped > 0) {
      msg.error(t('detail.judge_partial_fail', { succeeded, failed, skipped }))
    } else {
      msg.success(t('detail.judge_success', { succeeded, total: candidates.length }))
    }
    // Reload run summary so VersionSidebar counts (passed/failed) refresh too.
    loadRuns(false)
  }

  const cancelBatchJudge = () => {
    judgeCancelRef.current = true
  }

  // ======================== Derived ========================

  const selectedCase = useMemo<RunCase | null>(() => {
    if (!selectedCaseId || !runDetail) return null
    return runDetail.cases.find(c => c.id === selectedCaseId) || null
  }, [selectedCaseId, runDetail])

  const runProgress = useMemo(() => {
    if (!runDetail || (runDetail.status !== 'queued' && runDetail.status !== 'running')) return null
    const completed = runDetail.cases.filter(c => c.status !== 'pending' && c.status !== 'running').length
    return { runId: runDetail.id, current: completed, total: runDetail.cases.length }
  }, [runDetail])

  const isRunning = runProgress !== null

  // ======================== Render ========================

  if (!currentProject) {
    return <div className="p-8 font-mono text-sm text-ink-muted">No project selected.</div>
  }
  if (!suiteId) {
    return <div className="p-8 font-mono text-sm text-ink-muted">{t('detail.missing_suite_id')}</div>
  }
  if (suiteLoading) {
    return <div className="flex h-64 items-center justify-center"><CyberLoader /></div>
  }
  if (!suite) {
    return <div className="p-8 font-mono text-sm text-ink-muted">{t('detail.suite_not_exist')}</div>
  }

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center gap-3 px-6 py-3 border-b border-border bg-white">
        <button
          onClick={() => router.push('/ontology/lakehouse-agent/dataset-testing')}
          className="flex items-center gap-1 border border-border px-2 py-1 font-mono text-[10px] text-ink-ghost hover:text-ink hover:border-ink"
        >
          <ArrowLeft size={11} /> {t('detail.back')}
        </button>
        <FlaskConical size={16} className="text-ink-ghost" />
        <h1 className="font-display text-sm font-bold">{suite.name}</h1>
        <span className="font-mono text-[10px] text-ink-ghost">
          {t('detail.case_count', { count: suite.caseCount, concurrency: suite.concurrency })}
        </span>
        <StatusBadge status={suite.status} />
        {/* Tab toggle */}
        <div className="flex border border-border ml-4">
          <button
            onClick={() => switchTab('runs')}
            className={`flex items-center gap-1 px-3 py-1 font-mono text-[10px] font-bold tracking-wider ${
              activeTab === 'runs' ? 'bg-ink text-white' : 'text-ink-ghost hover:text-ink'
            }`}
          >
            <GitBranch size={11} /> {t('detail.tab_runs')}
          </button>
          <button
            onClick={() => switchTab('questions')}
            className={`flex items-center gap-1 px-3 py-1 font-mono text-[10px] font-bold tracking-wider border-l border-border ${
              activeTab === 'questions' ? 'bg-ink text-white' : 'text-ink-ghost hover:text-ink'
            }`}
          >
            <ListChecks size={11} /> {t('detail.tab_questions')}
          </button>
        </div>
        <span className="flex-1" />
        <span className="font-mono text-[9px] text-ink-ghost">
          {t('detail.created_at', { date: new Date(suite.createdAt).toLocaleDateString('zh-CN') })}
        </span>
      </div>

      {activeTab === 'questions' ? (
        <>
          {/* Upload/paste still useful on the template page */}
          <SuiteQuestionsBar onUpload={onUploadFile} onAddPasted={onAddPasted} />
          <QuestionsEditor
            suiteId={suiteId}
            suiteTags={suiteTags}
            onMutated={() => {
              loadSuiteTags()
              loadSuite()
              if (selectedRunId) loadRunDetail(selectedRunId, { silent: true })
            }}
          />
        </>
      ) : (
      /* Main area: versions | cases | case detail (3-pane) */
      <div className="flex-1 flex min-h-0">
        {/* Left: version list */}
        <VersionSidebar
          runs={runs}
          selectedRunId={selectedRunId}
          runProgress={runProgress}
          compareMode={compareMode}
          compareRunIds={compareRunIds}
          showNewRun={showNewRun}
          newRunTitle={newRunTitle}
          newRunConcurrency={newRunConcurrency}
          onSelectRun={selectRun}
          onToggleCompare={() => {
            setCompareMode(!compareMode)
            setCompareData(null)
            setCompareRunIds([])
          }}
          onToggleCompareRun={toggleCompareRun}
          onRunCompare={loadCompare}
          onToggleNewRun={() => {
            setShowNewRun(!showNewRun)
            setNewRunTitle(''); setNewRunConcurrency(3)
          }}
          onNewRunTitleChange={setNewRunTitle}
          onNewRunConcurrencyChange={setNewRunConcurrency}
          onCreateRun={createRun}
          onSetDefault={setDefaultRun}
          onDeleteRun={deleteRun}
        />

        {/* Middle: case list (or compare view) */}
        <div className="flex-1 flex flex-col min-w-0 overflow-y-auto">
          {compareMode && compareData ? (
            <CompareView suiteId={suiteId} data={compareData} suiteTags={suiteTags} />
          ) : compareMode ? (
            <div className="flex h-48 items-center justify-center font-mono text-xs text-ink-ghost">
              {t('detail.compare_placeholder')}
            </div>
          ) : selectedRunId && runDetail ? (
            <RunCaseList
              runId={selectedRunId}
              cases={runDetail.cases}
              runProgress={runProgress}
              isRunning={isRunning}
              retryingCaseIds={retryingCaseIds}
              selectedCaseId={selectedCaseId}
              exportingSuites={exportingSuites}
              suiteTags={suiteTags}
              judgeProgress={judgeProgress}
              onCaseTagsChange={onCaseTagsChange}
              onBulkTag={onBulkTag}
              onSelectCase={id => setSelectedCaseId(prev => prev === id ? null : id)}
              onRetry={retryRunCase}
              onRun={runTestRun}
              onContinue={continueRun}
              onRetryErrors={retryErrors}
              onBulkRetry={bulkRetry}
              onCancel={cancelRun}
              cancelRequested={runDetail?.cancelRequested || false}
              onExport={downloadRunExport}
              onBatchJudge={runBatchJudge}
              onCancelBatchJudge={cancelBatchJudge}
            />
          ) : runs.length === 0 ? (
            <div className="flex h-48 items-center justify-center font-mono text-xs text-ink-ghost">
              {suite.caseCount === 0 ? t('detail.empty_suite_hint') : t('detail.empty_runs_hint')}
            </div>
          ) : (
            <div className="flex h-48 items-center justify-center font-mono text-xs text-ink-ghost">
              {t('detail.no_run_selected')}
            </div>
          )}
        </div>

        {/* Right: case detail */}
        {selectedCase && !compareMode && selectedRunId && (
          <div className="w-[45%] min-w-[420px] border-l border-border bg-white overflow-y-auto flex-shrink-0">
            <CaseDetailPanel
              suiteId={suiteId}
              runId={selectedRunId}
              runCase={selectedCase}
              onClose={() => setSelectedCaseId(null)}
              onCaseUpdate={handleCaseUpdate}
              claimJudge={claimJudge}
              releaseJudge={releaseJudge}
            />
          </div>
        )}
      </div>
      )}
    </div>
  )
}
