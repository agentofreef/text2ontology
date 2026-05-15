'use client'

import { useState } from 'react'
import { Database, Check, X, AlertTriangle, Loader2, ChevronDown, ChevronRight } from 'lucide-react'
import { api } from '@/lib/api'
import { useTranslations } from 'next-intl'

interface PropertyDraft {
  name: string
  dataType: string
  sourceColumn: string
  isFilterable?: boolean
  isGroupable?: boolean
  isMachineCode?: boolean
}

interface ActivateResult {
  rowCount?: number
  sampleRows?: Record<string, unknown>[]
  columns?: string[]
  canonicalQuery?: string
}

interface ProposeOdResult {
  objectId?: string
  name?: string
  kind?: string
  semanticSql?: string
  description?: string
  properties?: PropertyDraft[]
  pending_confirmation?: boolean
  interview_bypassed?: boolean
  error?: string
  summary_text?: string
  userMessageCount?: number
}

interface FunctionCall {
  name: string
  arguments: Record<string, unknown>
  result?: ProposeOdResult
}

type CardStatus = 'pending' | 'loading' | 'activated' | 'failed' | 'abandoned'

function formatRowCount(n: number, rowLabel: string): string {
  return n.toLocaleString() + ' ' + rowLabel
}

export function BuilderProposeOdCard({ fc, projectId }: { fc: FunctionCall; projectId: string | undefined }) {
  const res = fc.result
  const t = useTranslations('builder')

  const [name, setName] = useState(res?.name ?? '')
  const [kind, setKind] = useState(res?.kind ?? 'entity')
  const [description, setDescription] = useState(res?.description ?? '')
  const [semanticSql, setSemanticSql] = useState(res?.semanticSql ?? '')
  const [properties, setProperties] = useState<PropertyDraft[]>(
    (res?.properties ?? []).map(p => ({ ...p }))
  )

  const [status, setStatus] = useState<CardStatus>('pending')
  const [errorMsg, setErrorMsg] = useState('')
  const [activateResult, setActivateResult] = useState<ActivateResult | null>(null)
  const [sqlOpen, setSqlOpen] = useState(false)

  const objectId = res?.objectId

  const activate = async () => {
    if (!objectId || !projectId) return
    setStatus('loading')
    setErrorMsg('')
    try {
      const data = await api<ActivateResult>('/ontology/builder/activate-od', {
        method: 'POST',
        body: {
          objectId,
          projectId,
          edits: { name, kind, description, semanticSql, properties },
        },
      })
      setActivateResult(data)
      setStatus('activated')
    } catch (e: unknown) {
      setErrorMsg(e instanceof Error ? e.message : t('activate_failed'))
      setStatus('failed')
    }
  }

  const abandon = async () => {
    if (!objectId || !projectId) return
    setStatus('loading')
    try {
      await api(`/ontology/objects/${objectId}?projectId=${encodeURIComponent(projectId)}`, {
        method: 'DELETE',
      })
    } catch {
      // best-effort
    }
    setStatus('abandoned')
  }

  const updateProp = (idx: number, field: keyof PropertyDraft, value: unknown) => {
    setProperties(prev => prev.map((p, i) => i === idx ? { ...p, [field]: value } : p))
  }

  const removeProp = (idx: number) => {
    setProperties(prev => prev.filter((_, i) => i !== idx))
  }

  const addProp = () => {
    setProperties(prev => [...prev, { name: '', dataType: 'text', sourceColumn: '', isFilterable: false, isGroupable: false, isMachineCode: false }])
  }

  if (status === 'abandoned') {
    return (
      <div className="border border-gray-200 bg-white p-3 flex items-center gap-2 text-xs text-gray-400">
        <X className="h-3.5 w-3.5" /> {t('od_proposal_abandoned')}
      </div>
    )
  }

  return (
    <div className="border border-gray-200 bg-white p-4 space-y-3 text-sm">
      {/* Header */}
      <div className="flex items-center gap-2">
        <Database className="h-4 w-4 text-gray-700 flex-shrink-0" />
        <span className="font-semibold text-gray-800 text-xs">{t('od_proposal_header')}</span>
      </div>

      {/* Interview bypassed warning */}
      {res?.interview_bypassed && (
        <div className="border border-yellow-300 bg-yellow-50 px-3 py-2 flex items-start gap-2">
          <AlertTriangle className="h-4 w-4 text-yellow-600 flex-shrink-0 mt-0.5" />
          <div className="text-xs text-yellow-800">
            <div className="font-semibold">{t('interview_insufficient')}</div>
            <div>{t('interview_bypassed_detail', { count: res.userMessageCount ?? 0 })}</div>
          </div>
        </div>
      )}

      {/* Error state from fc.result */}
      {res?.error && (
        <div className="border border-red-300 bg-red-50 px-3 py-2 text-xs text-red-700">
          <span className="font-semibold">{t('build_error_prefix')}</span>{res.error}
        </div>
      )}

      {/* Editable fields */}
      <div className="space-y-2">
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1">
            <label className="text-xs text-gray-500">{t('name')}</label>
            <input
              type="text"
              value={name}
              onChange={e => setName(e.target.value)}
              disabled={status === 'activated'}
              className="w-full border border-gray-200 px-2 py-1 text-sm focus:border-gray-500 focus:outline-none disabled:bg-gray-50 disabled:text-gray-400"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs text-gray-500">{t('kind')}</label>
            <select
              value={kind}
              onChange={e => setKind(e.target.value)}
              disabled={status === 'activated'}
              className="w-full border border-gray-200 px-2 py-1 text-sm focus:border-gray-500 focus:outline-none disabled:bg-gray-50 disabled:text-gray-400 bg-white"
            >
              <option value="entity">entity</option>
              <option value="event">event</option>
              <option value="attribute">attribute</option>
            </select>
          </div>
        </div>

        <div className="space-y-1">
          <label className="text-xs text-gray-500">{t('description')}</label>
          <textarea
            value={description}
            onChange={e => setDescription(e.target.value)}
            rows={3}
            disabled={status === 'activated'}
            className="w-full border border-gray-200 px-2 py-1 text-sm focus:border-gray-500 focus:outline-none disabled:bg-gray-50 disabled:text-gray-400 resize-none"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs text-gray-500">Semantic SQL</label>
          <textarea
            value={semanticSql}
            onChange={e => setSemanticSql(e.target.value)}
            rows={10}
            disabled={status === 'activated'}
            className="w-full border border-gray-200 px-2 py-1 font-mono text-xs focus:border-gray-500 focus:outline-none disabled:bg-gray-50 disabled:text-gray-400 resize-none"
          />
        </div>
      </div>

      {/* Properties table */}
      <div className="space-y-1">
        <label className="text-xs text-gray-500">{t('properties_list')}</label>
        <div className="border border-gray-200 overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-gray-200 bg-gray-50">
                <th className="text-left px-2 py-1.5 font-medium text-gray-600">{t('name')}</th>
                <th className="text-left px-2 py-1.5 font-medium text-gray-600">{t('kind')}</th>
                <th className="text-left px-2 py-1.5 font-medium text-gray-600">{t('source_column')}</th>
                <th className="px-2 py-1.5 font-medium text-gray-600 text-center">F</th>
                <th className="px-2 py-1.5 font-medium text-gray-600 text-center">G</th>
                <th className="px-2 py-1.5 font-medium text-gray-600 text-center">MC</th>
                <th className="w-6"></th>
              </tr>
            </thead>
            <tbody>
              {properties.map((p, idx) => (
                <tr key={idx} className="border-b border-gray-100 last:border-0">
                  <td className="px-1 py-1">
                    <input
                      type="text"
                      value={p.name}
                      onChange={e => updateProp(idx, 'name', e.target.value)}
                      disabled={status === 'activated'}
                      className="w-full border border-gray-200 px-1.5 py-0.5 focus:border-gray-500 focus:outline-none text-xs disabled:bg-gray-50"
                    />
                  </td>
                  <td className="px-1 py-1">
                    <input
                      type="text"
                      value={p.dataType}
                      onChange={e => updateProp(idx, 'dataType', e.target.value)}
                      disabled={status === 'activated'}
                      className="w-full border border-gray-200 px-1.5 py-0.5 focus:border-gray-500 focus:outline-none text-xs disabled:bg-gray-50"
                    />
                  </td>
                  <td className="px-1 py-1">
                    <input
                      type="text"
                      value={p.sourceColumn}
                      onChange={e => updateProp(idx, 'sourceColumn', e.target.value)}
                      disabled={status === 'activated'}
                      className="w-full border border-gray-200 px-1.5 py-0.5 focus:border-gray-500 focus:outline-none text-xs disabled:bg-gray-50"
                    />
                  </td>
                  <td className="px-2 py-1 text-center">
                    <input
                      type="checkbox"
                      checked={!!p.isFilterable}
                      onChange={e => updateProp(idx, 'isFilterable', e.target.checked)}
                      disabled={status === 'activated'}
                    />
                  </td>
                  <td className="px-2 py-1 text-center">
                    <input
                      type="checkbox"
                      checked={!!p.isGroupable}
                      onChange={e => updateProp(idx, 'isGroupable', e.target.checked)}
                      disabled={status === 'activated'}
                    />
                  </td>
                  <td className="px-2 py-1 text-center">
                    <input
                      type="checkbox"
                      checked={!!p.isMachineCode}
                      onChange={e => updateProp(idx, 'isMachineCode', e.target.checked)}
                      disabled={status === 'activated'}
                    />
                  </td>
                  <td className="px-1 py-1">
                    {status !== 'activated' && (
                      <button
                        onClick={() => removeProp(idx)}
                        className="text-gray-400 hover:text-red-600 px-1"
                      >
                        <X className="h-3 w-3" />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {status !== 'activated' && (
          <button
            onClick={addProp}
            className="text-xs text-gray-500 hover:text-gray-800 mt-2"
          >
            + {t('add_property')}
          </button>
        )}
      </div>

      {/* Action buttons */}
      {status === 'pending' && (
        <div className="flex gap-2 pt-1">
          <button
            onClick={activate}
            className="flex items-center gap-1.5 bg-orange-600 text-white px-4 py-2 text-sm font-semibold hover:bg-orange-700"
          >
            <Check className="h-3.5 w-3.5" /> {t('activate')}
          </button>
          <button
            onClick={abandon}
            className="flex items-center gap-1.5 border border-gray-200 text-gray-600 px-4 py-2 text-sm hover:border-gray-400"
          >
            <X className="h-3.5 w-3.5" /> {t('abandon')}
          </button>
        </div>
      )}

      {status === 'loading' && (
        <div className="flex items-center gap-2 text-xs text-gray-400 pt-1">
          <Loader2 className="h-3.5 w-3.5 animate-spin" /> {t('processing')}
        </div>
      )}

      {/* Activation success */}
      {status === 'activated' && activateResult && (
        <div className="border border-green-200 bg-green-50 p-3 space-y-2">
          <div className="flex items-center gap-1.5 text-xs text-green-700 font-semibold">
            <Check className="h-4 w-4" /> {t('activated')}
            {activateResult.rowCount != null && (
              <span className="ml-2 font-normal text-green-600">{formatRowCount(activateResult.rowCount, t('rows'))}</span>
            )}
          </div>

          {activateResult.sampleRows && activateResult.sampleRows.length > 0 && activateResult.columns && (
            <div className="border border-green-200 overflow-x-auto">
              <table className="text-xs w-full">
                <thead>
                  <tr className="border-b border-green-200 bg-green-100">
                    {activateResult.columns.map(col => (
                      <th key={col} className="px-2 py-1 text-left font-medium text-green-800">{col}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {activateResult.sampleRows.slice(0, 3).map((row, ri) => (
                    <tr key={ri} className="border-b border-green-100 last:border-0">
                      {activateResult.columns!.map(col => (
                        <td key={col} className="px-2 py-1 text-green-700">{String(row[col] ?? '')}</td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {activateResult.canonicalQuery && (
            <details open={sqlOpen} onToggle={e => setSqlOpen((e.target as HTMLDetailsElement).open)}>
              <summary className="flex items-center gap-1 text-xs text-green-700 cursor-pointer select-none">
                {sqlOpen ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
                Canonical Query
              </summary>
              <pre className="mt-1 border border-green-200 bg-white px-2 py-1.5 text-[10px] font-mono text-gray-700 whitespace-pre-wrap overflow-x-auto">
                {activateResult.canonicalQuery}
              </pre>
            </details>
          )}
        </div>
      )}

      {/* Activation failure */}
      {status === 'failed' && (
        <div className="border border-red-300 bg-red-50 p-3 space-y-2">
          <div className="text-xs text-red-700">{errorMsg || t('activate_failed')}</div>
          <button
            onClick={activate}
            className="flex items-center gap-1.5 bg-orange-600 text-white px-3 py-1.5 text-xs font-semibold hover:bg-orange-700"
          >
            <Loader2 className="h-3 w-3" /> {t('retry')}
          </button>
        </div>
      )}
    </div>
  )
}
