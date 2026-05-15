'use client'

import { useState } from 'react'
import { Link2, Check, X } from 'lucide-react'
import { api } from '@/lib/api'
import { useTranslations } from 'next-intl'

interface ProposeLinkResult {
  linkId?: string
  fromObjectId?: string
  toObjectId?: string
  fromPropertyId?: string
  toPropertyId?: string
  fkColumn?: string
  cardinality?: string
  linkName?: string
  description?: string
  pending_confirmation?: boolean
  error?: string
}

interface FunctionCall {
  name: string
  arguments: Record<string, unknown>
  result?: ProposeLinkResult & Record<string, unknown>
}

type Status = 'pending' | 'loading' | 'activated' | 'failed' | 'abandoned'

export function BuilderProposeLinkCard({ fc, projectId }: { fc: FunctionCall; projectId?: string }) {
  const r = (fc.result || {}) as ProposeLinkResult
  const t = useTranslations('builder')

  const [fkColumn, setFkColumn] = useState(r.fkColumn || '')
  const [cardinality, setCardinality] = useState(r.cardinality || 'many_to_one')
  const [linkName, setLinkName] = useState(r.linkName || '')
  const [description, setDescription] = useState(r.description || '')

  const [status, setStatus] = useState<Status>('pending')
  const [errorMsg, setErrorMsg] = useState('')
  const [successMsg, setSuccessMsg] = useState('')

  const linkId = r.linkId

  async function activate() {
    setStatus('loading')
    setErrorMsg('')
    try {
      await api('/ontology/builder/activate-link', {
        method: 'POST',
        body: {
          linkId,
          projectId,
          fromPropertyId: r.fromPropertyId,
          toPropertyId: r.toPropertyId,
          edits: { fkColumn, cardinality, linkName, description },
        },
      })
      setSuccessMsg(t('link_activated'))
      setStatus('activated')
    } catch (e) {
      setErrorMsg(e instanceof Error ? e.message : t('activate_failed'))
      setStatus('failed')
    }
  }

  async function abandon() {
    if (!linkId) { setStatus('abandoned'); return }
    setStatus('loading')
    try {
      await api(`/ontology/links/${linkId}?projectId=${projectId || ''}`, {
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
        <Link2 className="h-4 w-4 text-gray-600" />
        <span className="text-xs font-semibold text-gray-800">{t('link_proposal_header')}</span>
        {r.error && (
          <span className="text-[10px] text-red-500 font-mono border border-red-200 px-1.5 py-0.5">{r.error}</span>
        )}
      </div>

      <div className="space-y-2 bg-gray-50 border border-gray-200 px-3 py-2">
        {/* Read-only IDs */}
        <div className="flex items-start gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0 pt-0.5">from</label>
          <div className="space-y-0.5">
            <div className="text-[10px] font-mono text-gray-600">
              <span className="text-gray-400">object: </span>{r.fromObjectId || '—'}
            </div>
            <div className="text-[10px] font-mono text-gray-600">
              <span className="text-gray-400">property: </span>{r.fromPropertyId || '—'}
            </div>
          </div>
        </div>

        <div className="flex items-start gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0 pt-0.5">to</label>
          <div className="space-y-0.5">
            <div className="text-[10px] font-mono text-gray-600">
              <span className="text-gray-400">object: </span>{r.toObjectId || '—'}
            </div>
            <div className="text-[10px] font-mono text-gray-600">
              <span className="text-gray-400">property: </span>{r.toPropertyId || '—'}
            </div>
          </div>
        </div>

        <hr className="border-gray-200" />

        {/* fkColumn */}
        <div className="flex items-center gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0">fkColumn</label>
          <input
            type="text"
            value={fkColumn}
            onChange={e => setFkColumn(e.target.value)}
            disabled={!isActive}
            className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 disabled:text-gray-400 outline-none focus:border-gray-500"
          />
        </div>

        {/* cardinality */}
        <div className="flex items-center gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0">cardinality</label>
          <select
            value={cardinality}
            onChange={e => setCardinality(e.target.value)}
            disabled={!isActive}
            className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 disabled:text-gray-400 outline-none focus:border-gray-500"
          >
            <option value="many_to_one">many_to_one</option>
            <option value="one_to_many">one_to_many</option>
            <option value="many_to_many">many_to_many</option>
          </select>
        </div>

        {/* linkName */}
        <div className="flex items-center gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0">linkName</label>
          <input
            type="text"
            value={linkName}
            onChange={e => setLinkName(e.target.value)}
            disabled={!isActive}
            className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 disabled:text-gray-400 outline-none focus:border-gray-500"
          />
        </div>

        {/* description */}
        <div className="flex items-start gap-2">
          <label className="text-[10px] text-gray-500 w-20 shrink-0 pt-1">description</label>
          <textarea
            value={description}
            onChange={e => setDescription(e.target.value)}
            disabled={!isActive}
            rows={2}
            className="flex-1 border border-gray-300 px-2 py-1 text-xs font-mono bg-white disabled:bg-gray-100 disabled:text-gray-400 outline-none focus:border-gray-500 resize-y"
          />
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
