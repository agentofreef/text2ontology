'use client'

import { useEffect, useRef, useState } from 'react'
import { Check, Loader2, Sparkles, X } from 'lucide-react'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { CaseDetailBody, RunCase } from './SharedCaseBits'

interface CaseDetailPanelProps {
  suiteId: string
  runId: string
  runCase: RunCase
  onClose: () => void
  onCaseUpdate: (rcId: string, patch: Partial<RunCase>) => void
  // Shared judge lock — claim before calling /ai-judge, release in finally.
  // Returning false means the batch worker (or another panel) is already
  // judging this rcID, so we should bail instead of racing the same UPDATE.
  claimJudge: (rcId: string) => boolean
  releaseJudge: (rcId: string) => void
}

export function CaseDetailPanel({ suiteId, runId, runCase, onClose, onCaseUpdate, claimJudge, releaseJudge }: CaseDetailPanelProps) {
  const msg = useMessage()
  // Local note state so typing doesn't fight the parent re-renders.
  const [noteDraft, setNoteDraft] = useState(runCase.note ?? '')
  const [noteSaving, setNoteSaving] = useState(false)
  const [markSaving, setMarkSaving] = useState<'' | 'correct' | 'incorrect' | 'clear'>('')
  const [judging, setJudging] = useState(false)
  const debounceRef = useRef<number | null>(null)
  const currentCaseIdRef = useRef(runCase.id)

  // When the selected case changes, reset draft from the new case's note.
  useEffect(() => {
    if (currentCaseIdRef.current !== runCase.id) {
      currentCaseIdRef.current = runCase.id
      setNoteDraft(runCase.note ?? '')
      if (debounceRef.current) {
        window.clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [runCase.id, runCase.note])

  // Lazy-hydrate the heavy fields (functionCalls / generatedSql /
  // executionResult / executionError) that the list endpoint now strips when
  // loaded with ?lite=1. Fires once per case selection AND again if the case
  // was re-run (status/durationMs drift) so stale SQL/result doesn't stick
  // around after a retry.
  const hydratedVerRef = useRef<string | null>(null)
  useEffect(() => {
    const ver = `${runCase.id}:${runCase.status}:${runCase.durationMs}`
    if (hydratedVerRef.current === ver) return
    // Skip when the case hasn't executed yet — nothing to fetch.
    if (runCase.status !== 'completed' && runCase.status !== 'error' && runCase.status !== 'cancelled') {
      hydratedVerRef.current = ver
      return
    }
    // Already have heavy fields locally (e.g. filled by /retry response or a
    // prior hydration this session): skip the round-trip.
    const hasHeavy =
      (runCase.functionCalls && runCase.functionCalls.length > 0) ||
      (runCase.generatedSql && runCase.generatedSql !== '') ||
      (runCase.executionResult && runCase.executionResult !== '') ||
      (runCase.executionError && runCase.executionError !== '')
    if (hasHeavy) {
      hydratedVerRef.current = ver
      return
    }
    let cancelled = false
    hydratedVerRef.current = ver
    api<Partial<RunCase>>(`/ontology/lh-test-suites/${suiteId}/runs/${runId}/cases/${runCase.id}`)
      .then(full => {
        if (cancelled) return
        onCaseUpdate(runCase.id, {
          functionCalls: full.functionCalls,
          generatedSql: full.generatedSql,
          executionResult: full.executionResult,
          executionError: full.executionError,
        })
      })
      .catch(() => {
        // Let the user click again — reset so a retry will try once more.
        hydratedVerRef.current = null
      })
    return () => { cancelled = true }
    // runCase identity beyond these scalars doesn't matter — we want to
    // re-hydrate only when the execution itself changed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runCase.id, runCase.status, runCase.durationMs])

  const saveMark = async (newMark: '' | 'correct' | 'incorrect') => {
    setMarkSaving(newMark === '' ? 'clear' : newMark)
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/runs/${runId}/cases/${runCase.id}/mark`, {
        method: 'PUT',
        body: { mark: newMark },
      })
      onCaseUpdate(runCase.id, { mark: newMark || undefined })
    } catch {
      msg.error('标注保存失败')
    } finally {
      setMarkSaving('')
    }
  }

  const saveNote = async (note: string) => {
    setNoteSaving(true)
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/runs/${runId}/cases/${runCase.id}/note`, {
        method: 'PUT',
        body: { note },
      })
      onCaseUpdate(runCase.id, { note: note || undefined })
    } catch {
      msg.error('备注保存失败')
    } finally {
      setNoteSaving(false)
    }
  }

  const onNoteChange = (v: string) => {
    setNoteDraft(v)
    if (debounceRef.current) window.clearTimeout(debounceRef.current)
    debounceRef.current = window.setTimeout(() => {
      saveNote(v)
      debounceRef.current = null
    }, 800)
  }

  const onNoteBlur = () => {
    // Flush any pending debounced save immediately on blur.
    if (debounceRef.current) {
      window.clearTimeout(debounceRef.current)
      debounceRef.current = null
      if (noteDraft !== (runCase.note ?? '')) {
        saveNote(noteDraft)
      }
    }
  }

  // Trigger backend AI judge — compares final_answer vs the question template's
  // expected_answer with Od/Ok/Ol context. Server writes mark + note for us;
  // we just mirror those into local state so the UI updates without a refetch.
  // Claims the parent's per-case judge lock first so the batch worker can't
  // race the same rcID's UPDATE.
  const runAIJudge = async () => {
    if (!claimJudge(runCase.id)) {
      msg.error('该用例正在被批量判定中，请稍后再试')
      return
    }
    setJudging(true)
    // Cancel any pending debounced note save so it doesn't race the server-side write.
    if (debounceRef.current) {
      window.clearTimeout(debounceRef.current)
      debounceRef.current = null
    }
    try {
      const res = await api<{
        verdict: 'correct' | 'incorrect' | 'unknown'
        reason: string
        mark: string
        note: string
      }>(`/ontology/lh-test-suites/${suiteId}/runs/${runId}/cases/${runCase.id}/ai-judge`, {
        method: 'POST',
      })
      const patch: Partial<RunCase> = { note: res.note || undefined }
      if (res.mark === 'correct' || res.mark === 'incorrect') {
        patch.mark = res.mark
      } else if (res.verdict === 'unknown') {
        // Clear any prior mark so the case falls back to ⏳ pending — mirrors
        // the backend which now NULLs mark on an unknown verdict.
        patch.mark = undefined
      }
      onCaseUpdate(runCase.id, patch)
      setNoteDraft(res.note || '')
      if (res.verdict === 'unknown') {
        msg.success('AI 判定：无法判断（保留为 ⏳ 待判定）')
      } else if (res.verdict === 'correct') {
        msg.success('AI 判定：正确')
      } else {
        msg.success('AI 判定：错误')
      }
    } catch (e) {
      msg.error(e instanceof Error ? e.message : 'AI 判定失败')
    } finally {
      releaseJudge(runCase.id)
      setJudging(false)
    }
  }

  const currentMark = runCase.mark

  return (
    <div className="p-4 space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">CASE DETAIL</span>
          <span className="font-mono text-[9px] text-ink-ghost">{runCase.code}</span>
        </div>
        <button onClick={onClose} className="p-1 text-ink-ghost hover:text-ink">
          <X size={14} />
        </button>
      </div>

      <div className="border border-border px-3 py-2 bg-canvas-alt">
        <div className="font-mono text-[9px] text-ink-ghost font-bold mb-1">QUESTION</div>
        <div className="font-mono text-sm text-ink whitespace-pre-wrap">{runCase.userQuestion}</div>
      </div>

      {/* Case body (rounds, generated SQL, result, final answer, etc.) */}
      <CaseDetailBody tc={runCase} />

      {/* Annotation controls — pinned at the bottom so it doesn't shove the
          actual case content downward. mark + note both round-trip to
          ont_test_run_case (mark VARCHAR(20), note TEXT — see schema.sql). */}
      <div className="border border-accent/30 bg-accent/5 px-3 py-2 space-y-2 mt-4">
        <div className="flex items-center gap-2">
          <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">标注</span>
          <div className="flex items-center gap-1">
            <button
              onClick={() => saveMark('correct')}
              disabled={markSaving !== ''}
              className={`flex items-center gap-1 border px-2 py-1 font-mono text-[10px] font-bold transition-colors ${
                currentMark === 'correct'
                  ? 'border-green-500 bg-green-500 text-white'
                  : 'border-green-500 text-green-600 hover:bg-green-50'
              } disabled:opacity-30`}
            >
              {markSaving === 'correct' ? <Loader2 size={10} className="animate-spin" /> : <Check size={10} />}
              正确
            </button>
            <button
              onClick={() => saveMark('incorrect')}
              disabled={markSaving !== ''}
              className={`flex items-center gap-1 border px-2 py-1 font-mono text-[10px] font-bold transition-colors ${
                currentMark === 'incorrect'
                  ? 'border-red-500 bg-red-500 text-white'
                  : 'border-red-500 text-red-600 hover:bg-red-50'
              } disabled:opacity-30`}
            >
              {markSaving === 'incorrect' ? <Loader2 size={10} className="animate-spin" /> : <X size={10} />}
              错误
            </button>
            {currentMark && (
              <button
                onClick={() => saveMark('')}
                disabled={markSaving !== ''}
                className="border border-border px-2 py-1 font-mono text-[10px] text-ink-ghost hover:text-ink disabled:opacity-30"
              >
                {markSaving === 'clear' ? <Loader2 size={10} className="animate-spin" /> : '清除'}
              </button>
            )}
            <button
              onClick={runAIJudge}
              disabled={judging || markSaving !== ''}
              className="flex items-center gap-1 border border-accent bg-white text-accent px-2 py-1 font-mono text-[10px] font-bold hover:bg-accent hover:text-white transition-colors disabled:opacity-30 disabled:cursor-not-allowed ml-1"
              title="基于问题模板的正确答案示例 + Od/Ok/Ol 上下文，让 agent LLM 自动判定本次回答正确/错误"
            >
              {judging ? <Loader2 size={10} className="animate-spin" /> : <Sparkles size={10} />}
              AI 判定
            </button>
          </div>
        </div>
        <div>
          <div className="flex items-center gap-2 mb-1">
            <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">备注</span>
            {noteSaving && <Loader2 size={10} className="animate-spin text-accent" />}
            <span className="font-mono text-[8px] text-ink-ghost">（失焦或停顿 0.8s 自动保存）</span>
          </div>
          <textarea
            className="w-full min-h-[60px] border border-border bg-white px-2 py-1.5 font-mono text-[11px] focus:outline-none focus:border-accent resize-y"
            placeholder="为什么这次回答对/错？期望结果是什么？"
            value={noteDraft}
            onChange={e => onNoteChange(e.target.value)}
            onBlur={onNoteBlur}
          />
        </div>
      </div>
    </div>
  )
}
