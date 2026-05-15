'use client'

import { useState } from 'react'
import { Target, Check, X, Plus } from 'lucide-react'
import { api } from '@/lib/api'
import { useProject } from '@/lib/project'
import { useTranslations } from 'next-intl'

interface FilterSpec { prop: string; op: string; value: string }

interface ProposeIntentResult {
  intentId?: string
  objectId?: string
  name?: string
  canonicalMetric?: string
  canonicalFilters?: FilterSpec[]
  autoGroupBy?: string[]
  pivotOn?: string
  pivotValues?: string[]
  pivotColumnLabels?: string[]
  pivotTotalLabel?: string
  pivotWithPercent?: boolean
  pivotAppendGrandTotal?: boolean
  triggerKeywords?: string[]
  pending_confirmation?: boolean
  error?: string
}

interface FunctionCall {
  name: string
  arguments: Record<string, unknown>
  result?: ProposeIntentResult & Record<string, unknown>
}

type Status = 'pending' | 'loading' | 'activated' | 'failed' | 'abandoned'

export function BuilderProposeIntentCard({ fc, projectId }: { fc: FunctionCall; projectId?: string }) {
  const r = (fc.result || {}) as ProposeIntentResult
  const t = useTranslations('builder')

  const [name, setName] = useState(r.name || '')
  const [canonicalMetric, setCanonicalMetric] = useState(r.canonicalMetric || '')
  const [filtersJson, setFiltersJson] = useState(
    r.canonicalFilters && r.canonicalFilters.length > 0
      ? JSON.stringify(r.canonicalFilters, null, 2)
      : '[]'
  )
  const [autoGroupBy, setAutoGroupBy] = useState((r.autoGroupBy || []).join(', '))
  const [pivotOn, setPivotOn] = useState(r.pivotOn || '')
  const [pivotValues, setPivotValues] = useState((r.pivotValues || []).join('\n'))
  const [pivotColumnLabels, setPivotColumnLabels] = useState((r.pivotColumnLabels || []).join('\n'))
  const [pivotTotalLabel, setPivotTotalLabel] = useState(r.pivotTotalLabel || 'Total')
  const [pivotWithPercent, setPivotWithPercent] = useState(r.pivotWithPercent ?? false)
  const [pivotAppendGrandTotal, setPivotAppendGrandTotal] = useState(r.pivotAppendGrandTotal ?? false)
  const [keywords, setKeywords] = useState<string[]>(r.triggerKeywords || [])
  const [kwInput, setKwInput] = useState('')

  const [status, setStatus] = useState<Status>('pending')
  const [errorMsg, setErrorMsg] = useState('')
  const [successMsg, setSuccessMsg] = useState('')

  const intentId = r.intentId

  function addKeyword() {
    const kw = kwInput.trim()
    if (kw && !keywords.includes(kw)) {
      setKeywords(prev => [...prev, kw])
    }
    setKwInput('')
  }

  async function activate() {
    setStatus('loading')
    setErrorMsg('')
    let parsedFilters: FilterSpec[] = []
    try {
      parsedFilters = JSON.parse(filtersJson)
      if (!Array.isArray(parsedFilters)) throw new Error('not array')
    } catch {
      setErrorMsg(t('filters_json_invalid'))
      setStatus('pending')
      return
    }

    try {
      const res = await api<{ keywordsRegistered?: number }>('/ontology/builder/activate-intent', {
        method: 'POST',
        body: {
          intentId,
          projectId,
          edits: {
            name,
            canonicalMetric,
            canonicalFilters: parsedFilters,
            autoGroupBy: autoGroupBy.split(',').map(s => s.trim()).filter(Boolean),
            pivotOn,
            pivotValues: pivotValues.split('\n').map(s => s.trim()).filter(Boolean),
            pivotColumnLabels: pivotColumnLabels.split('\n').map(s => s.trim()).filter(Boolean),
            pivotTotalLabel,
            pivotWithPercent,
            pivotAppendGrandTotal,
          },
          triggerKeywords: keywords,
        },
      })
      const n = res?.keywordsRegistered ?? keywords.length
      setSuccessMsg(t('intent_activated', { count: n }))
      setStatus('activated')
    } catch (e) {
      setErrorMsg(e instanceof Error ? e.message : t('activate_failed'))
      setStatus('failed')
    }
  }

  async function abandon() {
    if (!intentId) { setStatus('abandoned'); return }
    setStatus('loading')
    try {
      await api(`/ontology/metric-intents/${intentId}?projectId=${projectId || ''}`, {
        method: 'DELETE',
      })
      setStatus('abandoned')
    } catch (e) {
      setErrorMsg(e instanceof Error ? e.message : t('abandon_failed'))
      setStatus('failed')
    }
  }

  const isActive = status === 'pending' || status === 'failed'

  return (
    <div className="border border-gray-300 bg-white p-3 space-y-3">
      {/* Header */}
      <div className="flex items-center gap-2">
        <Target className="h-4 w-4 text-gray-600" />
        <span className="text-xs font-semibold text-gray-800">{t('intent_proposal_header')}</span>
        {r.error && (
          <span className="text-[10px] text-red-500 font-mono border border-red-200 px-1.5 py-0.5">{r.error}</span>
        )}
      </div>

      {/* Fields */}
      <div className="space-y-2 bg-gray-50 border border-gray-200 px-3 py-2">
        {/* name */}
        <div className="flex items-center gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0">name</label>
          <input
            type="text"
            value={name}
            onChange={e => setName(e.target.value)}
            disabled={!isActive}
            className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 disabled:text-gray-400 outline-none focus:border-gray-500"
          />
        </div>

        {/* canonicalMetric */}
        <div className="flex items-center gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0">metric</label>
          <input
            type="text"
            value={canonicalMetric}
            onChange={e => setCanonicalMetric(e.target.value)}
            disabled={!isActive}
            placeholder="e.g. sum(Order_Quantity)"
            className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 disabled:text-gray-400 outline-none focus:border-gray-500"
          />
        </div>

        {/* canonicalFilters */}
        <div className="flex items-start gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0 pt-1">{t('filters_json_label')}</label>
          <textarea
            value={filtersJson}
            onChange={e => setFiltersJson(e.target.value)}
            disabled={!isActive}
            rows={3}
            className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 disabled:text-gray-400 outline-none focus:border-gray-500 resize-y"
          />
        </div>

        {/* autoGroupBy */}
        <div className="flex items-center gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0">autoGroupBy</label>
          <input
            type="text"
            value={autoGroupBy}
            onChange={e => setAutoGroupBy(e.target.value)}
            disabled={!isActive}
            placeholder={t('comma_separated')}
            className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 disabled:text-gray-400 outline-none focus:border-gray-500"
          />
        </div>

        {/* Pivot config — collapsible */}
        <details>
          <summary className="text-[10px] text-gray-400 cursor-pointer hover:text-gray-600 uppercase tracking-wider select-none">
            {t('pivot_config')}
          </summary>
          <div className="mt-2 space-y-2 pl-1">
            <div className="flex items-center gap-2">
              <label className="text-[10px] text-gray-500 w-24 shrink-0">pivotOn</label>
              <input type="text" value={pivotOn} onChange={e => setPivotOn(e.target.value)} disabled={!isActive}
                className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 outline-none focus:border-gray-500" />
            </div>
            <div className="flex items-start gap-2">
              <label className="text-[10px] text-gray-500 w-24 shrink-0 pt-1">pivotValues<br />({t('one_per_line')})</label>
              <textarea value={pivotValues} onChange={e => setPivotValues(e.target.value)} disabled={!isActive} rows={3}
                className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 outline-none focus:border-gray-500 resize-y" />
            </div>
            <div className="flex items-start gap-2">
              <label className="text-[10px] text-gray-500 w-24 shrink-0 pt-1">columnLabels<br />({t('one_per_line')})</label>
              <textarea value={pivotColumnLabels} onChange={e => setPivotColumnLabels(e.target.value)} disabled={!isActive} rows={3}
                className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 outline-none focus:border-gray-500 resize-y" />
            </div>
            <div className="flex items-center gap-2">
              <label className="text-[10px] text-gray-500 w-24 shrink-0">totalLabel</label>
              <input type="text" value={pivotTotalLabel} onChange={e => setPivotTotalLabel(e.target.value)} disabled={!isActive}
                className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 outline-none focus:border-gray-500" />
            </div>
            <div className="flex items-center gap-4">
              <label className="flex items-center gap-1.5 text-[10px] text-gray-500 cursor-pointer">
                <input type="checkbox" checked={pivotWithPercent} onChange={e => setPivotWithPercent(e.target.checked)} disabled={!isActive} className="accent-gray-700" />
                withPercent
              </label>
              <label className="flex items-center gap-1.5 text-[10px] text-gray-500 cursor-pointer">
                <input type="checkbox" checked={pivotAppendGrandTotal} onChange={e => setPivotAppendGrandTotal(e.target.checked)} disabled={!isActive} className="accent-gray-700" />
                appendGrandTotal
              </label>
            </div>
          </div>
        </details>

        {/* triggerKeywords */}
        <div className="space-y-1.5">
          <label className="text-[10px] text-gray-500">triggerKeywords</label>
          <div className="flex flex-wrap gap-1">
            {keywords.map((kw, i) => (
              <span key={i} className="flex items-center gap-1 border border-gray-300 bg-white px-2 py-0.5 text-[10px] font-mono text-gray-700">
                {kw}
                {isActive && (
                  <button onClick={() => setKeywords(prev => prev.filter((_, idx) => idx !== i))} className="text-gray-400 hover:text-red-500">
                    <X className="h-2.5 w-2.5" />
                  </button>
                )}
              </span>
            ))}
          </div>
          {isActive && (
            <div className="flex gap-1">
              <input
                type="text"
                value={kwInput}
                onChange={e => setKwInput(e.target.value)}
                onKeyDown={e => e.key === 'Enter' && addKeyword()}
                placeholder={t('keyword_placeholder')}
                className="border border-gray-300 px-2 py-1 text-xs font-mono bg-white outline-none focus:border-gray-500 flex-1"
              />
              <button onClick={addKeyword} className="border border-gray-300 px-2 py-1 text-xs hover:bg-gray-100">
                <Plus className="h-3 w-3" />
              </button>
            </div>
          )}
        </div>
      </div>

      {/* Error */}
      {errorMsg && (
        <div className="text-xs text-red-600 border border-red-200 bg-red-50 px-2 py-1">{errorMsg}</div>
      )}

      {/* Action buttons */}
      {isActive && (
        <div className="flex gap-2">
          <button
            onClick={activate}
            className="flex items-center gap-1 bg-gray-900 text-white text-xs px-3 py-1.5 hover:bg-gray-700 transition-colors"
          >
            <Check className="h-3.5 w-3.5" /> {t('activate')}
          </button>
          <button
            onClick={abandon}
            className="flex items-center gap-1 border border-gray-300 text-gray-500 text-xs px-3 py-1.5 hover:border-gray-500 transition-colors"
          >
            <X className="h-3.5 w-3.5" /> {t('abandon')}
          </button>
        </div>
      )}

      {status === 'loading' && (
        <span className="text-xs text-gray-400 animate-pulse">{t('processing')}</span>
      )}

      {status === 'activated' && (
        <div className="flex items-center gap-1.5 text-xs text-white bg-gray-900 px-3 py-1.5 font-semibold w-fit">
          <Check className="h-3.5 w-3.5" /> {successMsg}
        </div>
      )}

      {status === 'abandoned' && (
        <div className="flex items-center gap-1.5 text-xs text-gray-400 border border-gray-200 px-3 py-1.5 w-fit">
          <X className="h-3.5 w-3.5" /> {t('abandoned')}
        </div>
      )}
    </div>
  )
}
