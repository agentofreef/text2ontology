'use client'

// Left-rail thread list for explore mode.
//
// Redesigned for AI-native feel: day-grouped (今天/昨天/本周/更早), active
// thread marked with left accent stripe, "新对话" promoted to a soft pill at
// top. Backend scope unchanged — GET filtered by agent_type=explore, 20 rows.

import { useMemo } from 'react'
import { useTranslations } from 'next-intl'
import { Plus, MessageSquare, ChevronRight } from 'lucide-react'
import { useFetch } from '@/lib/hooks'

interface ThreadSummary {
  id: string
  title: string
  agentType: string
  updatedAt: string
  createdAt: string
}

export interface ThreadListProps {
  activeThreadId: string | null
  onSelect: (id: string | null) => void
  onClose?: () => void
}

interface Group {
  label: string
  rows: ThreadSummary[]
}

export function ThreadList({ activeThreadId, onSelect, onClose }: ThreadListProps) {
  const t = useTranslations('agent.main.explore.thread_list')
  const { data, loading, error, refetch } = useFetch<ThreadSummary>(
    '/ontology/lakehouse-agent-threads?agent_type=explore',
  )

  const groups = useMemo<Group[]>(() => {
    const rows = (data ?? []).slice(0, 20)
    const todayKey = dayKey(new Date())
    const yesterdayKey = dayKey(new Date(Date.now() - 24 * 3600_000))
    const weekAgo = Date.now() - 7 * 24 * 3600_000

    const today: ThreadSummary[] = []
    const yesterday: ThreadSummary[] = []
    const thisWeek: ThreadSummary[] = []
    const older: ThreadSummary[] = []

    for (const r of rows) {
      const ts = Date.parse(r.updatedAt)
      if (Number.isNaN(ts)) { older.push(r); continue }
      const k = dayKey(new Date(ts))
      if (k === todayKey) today.push(r)
      else if (k === yesterdayKey) yesterday.push(r)
      else if (ts >= weekAgo) thisWeek.push(r)
      else older.push(r)
    }

    const out: Group[] = []
    if (today.length) out.push({ label: '今天', rows: today })
    if (yesterday.length) out.push({ label: '昨天', rows: yesterday })
    if (thisWeek.length) out.push({ label: '本周', rows: thisWeek })
    if (older.length) out.push({ label: '更早', rows: older })
    return out
  }, [data])

  const hasRows = groups.length > 0

  return (
    <aside className="flex h-full w-[260px] flex-shrink-0 flex-col overflow-hidden border-l-2 border-ink bg-canvas-alt">
      {/* Top action */}
      <div className="flex flex-shrink-0 items-center gap-2 border-b border-ink bg-white px-3 py-3">
        <button
          type="button"
          onClick={() => { onSelect(null); refetch() }}
          className="group flex flex-1 items-center justify-center gap-1.5 border border-ink bg-white px-3 py-2 font-mono text-[10.5px] tracking-[0.12em] uppercase text-ink transition-all hover:bg-ink hover:text-white"
        >
          <Plus className="h-3.5 w-3.5" />
          {t('new_button')}
        </button>
        {onClose && (
          <button
            type="button"
            onClick={onClose}
            title="收起"
            className="inline-flex h-[34px] w-[34px] flex-shrink-0 items-center justify-center border border-ink bg-white text-ink transition-colors hover:bg-ink hover:text-white"
          >
            <ChevronRight className="h-3.5 w-3.5" />
          </button>
        )}
      </div>

      {/* Threads */}
      <div className="flex-1 overflow-y-auto px-2 pb-3">
        {loading && (
          <div className="px-2 py-3 font-mono text-[10.5px] text-ink-muted">{t('loading')}</div>
        )}
        {error !== null && (
          <div className="px-2 py-3 font-mono text-[10.5px] text-ink">{String(error)}</div>
        )}
        {!loading && !error && !hasRows && (
          <div className="flex flex-col items-center gap-2 px-2 py-10 text-center">
            <MessageSquare className="h-5 w-5 text-ink-muted" strokeWidth={1.5} />
            <span className="font-mono text-[10.5px] text-ink-muted">{t('empty')}</span>
          </div>
        )}

        {groups.map((g) => (
          <div key={g.label} className="mb-3">
            <div className="px-2 pb-1 pt-2 font-mono text-[9.5px] tracking-[0.22em] uppercase text-ink-muted">
              {g.label}
            </div>
            <ul className="space-y-0">
              {g.rows.map((thread) => {
                const active = thread.id === activeThreadId
                const title =
                  thread.title && thread.title.trim().length > 0
                    ? thread.title
                    : t('title_fallback')
                return (
                  <li key={thread.id}>
                    <button
                      type="button"
                      onClick={() => onSelect(thread.id)}
                      title={title}
                      className={
                        'relative block w-full truncate border px-2.5 py-2 text-left transition-colors ' +
                        (active
                          ? 'border-ink bg-ink text-white'
                          : 'border-transparent text-ink hover:border-ink hover:bg-white')
                      }
                    >
                      <div className="truncate font-mono text-[11.5px]">{title}</div>
                      <div className={`mt-0.5 font-mono text-[9.5px] ${active ? 'text-white opacity-70' : 'text-ink-muted'}`}>
                        {formatRelative(thread.updatedAt)}
                      </div>
                    </button>
                  </li>
                )
              })}
            </ul>
          </div>
        ))}
      </div>
    </aside>
  )
}

function dayKey(d: Date): string {
  return `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`
}

function formatRelative(iso: string): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso.slice(0, 10)
  const diff = Date.now() - t
  const m = Math.floor(diff / 60000)
  if (m < 1) return '刚刚'
  if (m < 60) return `${m} 分钟前`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} 小时前`
  const d = Math.floor(h / 24)
  if (d < 7) return `${d} 天前`
  return iso.slice(0, 10)
}
