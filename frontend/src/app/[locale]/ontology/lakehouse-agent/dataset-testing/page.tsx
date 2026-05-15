'use client'

import { useState, useEffect, useCallback } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { CyberLoader } from '@/components/ui/CyberLoader'
import { FlaskConical, Plus, Trash2, ChevronRight, X } from 'lucide-react'
import { StatusBadge } from './components/SharedCaseBits'
import { useAutoAnimate, MotionGroup, MotionGroupItem, AnimatePresence, MotionFade, DataLoader } from '@/lib/motion'

// Lightweight list page for lakehouse dataset test suites — minimal variant.

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

export default function LakehouseDatasetTestingPageMinimal() {
  const t = useTranslations('agent.dataset_testing')
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const msg = useMessage()
  const router = useRouter()

  const [suites, setSuites] = useState<TestSuite[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [newName, setNewName] = useState('')
  const [newConcurrency, setNewConcurrency] = useState(3)

  // useAutoAnimate for FLIP add/remove on the suite list
  const suiteListRef = useAutoAnimate<HTMLDivElement>()

  const loadSuites = useCallback(async () => {
    if (!currentProject) return
    setLoading(true)
    try {
      const res = await api<{ data: TestSuite[] }>(`/ontology/lh-test-suites?projectId=${currentProject.id}`)
      setSuites(res.data || [])
    } catch { /* silent */ }
    finally { setLoading(false) }
  }, [currentProject])

  useEffect(() => { loadSuites() }, [loadSuites])

  const createSuite = async () => {
    if (!newName.trim() || !currentProject) return
    try {
      await api('/ontology/lh-test-suites', {
        method: 'POST',
        body: { name: newName.trim(), projectId: currentProject.id, concurrency: newConcurrency },
      })
      setNewName('')
      setShowCreate(false)
      msg.success(t('suite_created'))
      loadSuites()
    } catch { msg.error(t('create_fail')) }
  }

  const deleteSuite = async (id: string, name: string) => {
    if (!confirm(t('delete_confirm', { name }))) return
    try {
      await api(`/ontology/lh-test-suites/${id}`, { method: 'DELETE' })
      loadSuites()
    } catch { msg.error(t('delete_fail')) }
  }

  const openDetail = (id: string) => {
    router.push(`/ontology/lakehouse-agent/dataset-testing/detail?suiteId=${id}`)
  }

  if (!currentProject) {
    return <div className="p-8 text-sm text-gray-400">No project selected.</div>
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      {/* Header */}
      <div className={`flex h-14 items-center justify-between px-6 bg-white flex-shrink-0 ${industrial ? 'border-b-2 border-ink' : 'border-b border-gray-200'}`}>
        <div className="flex items-center gap-3">
          {industrial ? (
            <>
              <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
                // DATASET TESTING
              </span>
              <span className="font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-muted">
                {suites.length} SUITES
              </span>
            </>
          ) : (
            <>
              <FlaskConical size={18} className="text-gray-500" />
              <h1 className="text-base font-semibold text-gray-900">{t('page_title')}</h1>
              <span className="text-[10px] text-gray-400">
                {t('suite_count', { count: suites.length })}
              </span>
            </>
          )}
        </div>
        <button
          onClick={() => setShowCreate(!showCreate)}
          className={`flex items-center gap-1.5 px-3 py-1.5 font-medium hover:border-ink hover:text-ink transition-colors ${
            industrial
              ? 'border border-ink font-mono text-[10px] uppercase tracking-[0.14em] text-ink'
              : 'border border-gray-200 rounded-lg text-xs text-gray-600'
          }`}
        >
          <Plus size={12} /> {t('new_suite_btn')}
        </button>
      </div>

      {/* Create form */}
      <AnimatePresence>
        {showCreate && (
          <MotionFade className={`px-6 py-3 flex-shrink-0 ${
            industrial ? 'border-b border-ink bg-canvas-alt' : 'border-b border-blue-100 bg-blue-50'
          }`}>
            <div className="flex items-center gap-3">
              <input
                autoFocus
                className={`flex-1 px-3 py-2 focus:outline-none transition-colors ${
                  industrial
                    ? 'border border-ink font-mono text-[12px] tracking-[0.04em] focus:border-ink'
                    : 'border border-gray-200 rounded-lg text-xs focus:border-blue-400'
                }`}
                placeholder={t('create_suite_placeholder')}
                value={newName}
                onChange={e => setNewName(e.target.value)}
                onKeyDown={e => e.key === 'Enter' && createSuite()}
              />
              <div className="flex items-center gap-1.5">
                <span className={industrial ? 'font-mono text-[10px] uppercase tracking-[0.14em] text-ink-muted' : 'text-[9px] text-gray-400'}>{t('concurrency_label')}</span>
                <select
                  className={`px-2 py-2 focus:outline-none ${
                    industrial ? 'border border-ink font-mono text-[11px] bg-white' : 'border border-gray-200 rounded-lg text-[10px]'
                  }`}
                  value={newConcurrency}
                  onChange={e => setNewConcurrency(Number(e.target.value))}
                >
                  {[1, 2, 3, 5, 8, 10].map(n => (
                    <option key={n} value={n}>{n}</option>
                  ))}
                </select>
              </div>
              <button onClick={createSuite} className={`bg-ink text-white px-3 py-2 font-semibold hover:bg-ink/85 transition-colors ${
                industrial ? 'font-mono text-[10px] uppercase tracking-[0.14em]' : 'rounded-lg text-xs'
              }`}>
                {t('create_btn')}
              </button>
              <button onClick={() => { setShowCreate(false); setNewName('') }} className={`p-1.5 text-ink-ghost hover:text-ink transition-colors ${industrial ? '' : 'rounded'}`}>
                <X size={14} />
              </button>
            </div>
          </MotionFade>
        )}
      </AnimatePresence>

      {/* List */}
      <DataLoader loading={loading} message={t('loading')} minHeight={400}>
      <div className="flex-1 min-h-0 overflow-y-auto bg-gray-50">
        {suites.length === 0 && !showCreate ? (
          <MotionFade className="flex h-full items-center justify-center text-xs text-gray-400">
            {t('empty_hint')}
          </MotionFade>
        ) : (
          <div className="p-4 space-y-2">
            {/* Column header */}
            <div className={`flex items-center gap-3 px-4 py-2 font-semibold uppercase ${
              industrial
                ? 'font-mono text-[10px] tracking-[0.22em] text-ink-ghost border-b border-ink'
                : 'text-[9px] tracking-wider text-gray-400'
            }`}>
              <span className="flex-1">{industrial ? `// ${t('col_name')}` : t('col_name')}</span>
              <span className="w-16 text-right">{t('col_cases')}</span>
              <span className="w-20">{t('col_concurrency')}</span>
              <span className="w-20">{t('col_status')}</span>
              <span className="w-32">{t('col_created')}</span>
              <span className="w-12" />
            </div>
            <div ref={suiteListRef} className={industrial ? 'divide-y divide-ink/15 border border-ink' : 'space-y-1.5'}>
              {suites.map(suite => (
                <div
                  key={suite.id}
                  className={`flex items-center gap-3 px-4 py-3.5 bg-white cursor-pointer transition-all ${
                    industrial
                      ? 'hover:bg-canvas-alt border-l-2 border-l-transparent hover:border-l-ink'
                      : 'border border-gray-200 rounded-xl hover:border-gray-300 hover:shadow-sm'
                  }`}
                  onClick={() => openDetail(suite.id)}
                >
                  <div className="flex-1 flex items-center gap-2 min-w-0">
                    <span className={`truncate font-semibold ${
                      industrial ? 'font-mono text-[12px] tracking-[0.04em] text-ink' : 'text-sm text-gray-800'
                    }`}>{suite.name}</span>
                  </div>
                  <span className={`w-16 text-right ${
                    industrial ? 'font-mono text-[10px] tabular-nums tracking-[0.06em] text-ink-muted' : 'text-[10px] text-gray-500'
                  }`}>
                    {t('case_count', { count: suite.caseCount })}
                  </span>
                  <span className={`w-20 ${
                    industrial ? 'font-mono text-[10px] tracking-[0.1em] text-ink-ghost uppercase' : 'text-[9px] text-gray-400'
                  }`}>
                    {t('concurrency_value', { n: suite.concurrency })}
                  </span>
                  <span className="w-20"><StatusBadge status={suite.status} /></span>
                  <span className={`w-32 tabular-nums ${
                    industrial ? 'font-mono text-[10px] tracking-[0.06em] text-ink-ghost' : 'text-[9px] text-gray-400'
                  }`}>
                    {new Date(suite.createdAt).toLocaleDateString('zh-CN')}
                  </span>
                  <div className="w-12 flex items-center justify-end gap-1">
                    <button
                      onClick={e => { e.stopPropagation(); deleteSuite(suite.id, suite.name) }}
                      className={`p-1 transition-colors ${
                        industrial ? 'text-ink-ghost hover:text-danger' : 'text-gray-300 hover:text-red-500'
                      }`}
                      title={t('delete_confirm', { name: suite.name })}
                    >
                      <Trash2 size={12} />
                    </button>
                    <ChevronRight size={14} className={industrial ? 'text-ink-ghost' : 'text-gray-300'} />
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
      </DataLoader>
    </div>
  )
}
