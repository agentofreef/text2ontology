'use client'

import { useState, useEffect, useCallback } from 'react'
import { useTranslations } from 'next-intl'
import { useSearchParams } from 'next/navigation'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { ResultViewer } from '@/components/ui/ResultViewer'
import {
  Trash2, ExternalLink, ChevronDown, ChevronRight, Bot, User, Search,
  BookOpen, Play, GitBranch, Link2, FileText, Database,
  Code, FileType, History as HistoryIcon, Settings, Wrench,
} from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Link } from '@/i18n/navigation'
import { useAutoAnimate } from '@/lib/motion'

// ─── Translations ────────────────────────────────────────────────

const TOOL_META_KEYS: Record<string, { labelKey: string }> = {
  lookup:               { labelKey: 'tool_lookup' },
  smartquery:           { labelKey: 'tool_smartquery' },
  link_to_od:           { labelKey: 'tool_link_to_od' },
  create_causality:     { labelKey: 'tool_create_causality' },
  propose_learned_fact: { labelKey: 'tool_propose_learned_fact' },
}

const ROLE_LABEL_KEYS: Record<string, string> = {
  user:      'role_user',
  assistant: 'role_assistant',
  tool:      'role_tool',
  system:    'role_system',
  function:  'role_function',
}

// ─── Inline loader ───────────────────────────────────────────────

function InlineLoader({ text }: { text: string }) {
  const reduce = useReducedMotion()
  return (
    <div className="flex items-center gap-2 text-sm text-ink-muted">
      <motion.span
        animate={reduce ? undefined : { rotate: 360 }}
        transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
        className="inline-block h-3 w-3 rounded-full border-2 border-ink/20 border-t-ink"
        aria-hidden="true"
      />
      <span>{text}</span>
    </div>
  )
}

// ─── Markdown rendering ──────────────────────────────────────────

function MdContent({ content }: { content: string }) {
  return (
    <div className="text-xs text-ink-muted">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={{
        p: ({ children }) => <p className="mb-1 text-xs leading-relaxed">{children}</p>,
        h1: ({ children }) => <h1 className="mb-1 border-b border-border-light pb-0.5 text-xs font-semibold text-ink">{children}</h1>,
        h2: ({ children }) => <h2 className="mb-0.5 text-xs font-semibold text-ink">{children}</h2>,
        h3: ({ children }) => <h3 className="mb-0.5 text-[11px] font-semibold text-ink-muted">{children}</h3>,
        ul: ({ children }) => <ul className="mb-1 ml-3 list-disc text-xs">{children}</ul>,
        li: ({ children }) => <li className="mb-0.5">{children}</li>,
        code: ({ children }) => <code className="rounded bg-canvas-alt px-1 font-mono text-[11px] text-ink">{children}</code>,
        pre: ({ children }) => <pre className="overflow-x-auto rounded bg-canvas-alt px-2 py-1 font-mono text-[11px] text-ink">{children}</pre>,
        blockquote: ({ children }) => <blockquote className="border-l-2 border-border pl-2 text-xs text-ink-muted">{children}</blockquote>,
        strong: ({ children }) => <strong className="font-semibold text-ink">{children}</strong>,
        hr: () => <hr className="my-1 border-border-light" />,
      }}>{content}</ReactMarkdown>
    </div>
  )
}

// ─── Function call rendering ─────────────────────────────────────

function HistoryFunctionCall({ fc }: { fc: unknown }) {
  const t = useTranslations('agent.history')
  const roleLabel = (role: string) => ROLE_LABEL_KEYS[role] ? t(ROLE_LABEL_KEYS[role] as Parameters<typeof t>[0]) : role
  const f = fc as Record<string, unknown>
  const name = String(f.name || '')
  const args = (f.arguments as Record<string, unknown>) || {}
  const result = (f.result as Record<string, unknown>) || {}
  const meta = TOOL_META_KEYS[name] || { labelKey: name }

  return (
    <div className="space-y-1.5">
      <div className="inline-flex items-center gap-1.5 rounded-md border border-border bg-canvas-alt px-2 py-0.5 text-[11px] font-semibold text-ink">
        {name === 'lookup' && <BookOpen className="h-3 w-3" aria-hidden="true" />}
        {name === 'smartquery' && <Play className="h-3 w-3" aria-hidden="true" />}
        {name === 'create_causality' && <GitBranch className="h-3 w-3" aria-hidden="true" />}
        {name === 'link_to_od' && <Link2 className="h-3 w-3" aria-hidden="true" />}
        {name === 'propose_learned_fact' && <FileText className="h-3 w-3" aria-hidden="true" />}
        {!['lookup','smartquery','create_causality','link_to_od','propose_learned_fact'].includes(name) && <Database className="h-3 w-3" aria-hidden="true" />}
        {meta.labelKey in TOOL_META_KEYS ? t(meta.labelKey as Parameters<typeof t>[0]) : meta.labelKey}
        <span className="font-mono text-[11px] text-ink-ghost">· {name}</span>
      </div>

      {name === 'lookup' && (
        <div className="space-y-1">
          {((args.ontology_name as string[]) || []).length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="text-[11px] text-ink-ghost">{t('lookup_body_label')}</span>
              {((args.ontology_name as string[]) || []).map((n, i) => (
                <span key={i} className="rounded-md bg-ink px-1.5 py-0.5 font-mono text-[11px] text-white">{n}</span>
              ))}
            </div>
          )}
          {((args.keyword as string[]) || []).length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="text-[11px] text-ink-ghost">{t('lookup_kw_label')}</span>
              {((args.keyword as string[]) || []).map((k, i) => (
                <span key={i} className="rounded border border-success/40 bg-success/5 px-1.5 py-0.5 text-[11px] text-success">{k}</span>
              ))}
            </div>
          )}
        </div>
      )}

      {name === 'smartquery' && (
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-[11px] text-ink-ghost">{t('smartquery_obj_label')}</span>
            {((args.objects as string[]) || []).map((o, i) => (
              <span key={i} className="rounded-md bg-ink px-1.5 py-0.5 font-mono text-[11px] text-white">{o}</span>
            ))}
            {!!args.metric && (
              <>
                <span className="ml-1 text-[11px] text-ink-ghost">{t('smartquery_metric_label')}</span>
                <span className="rounded border border-border bg-white px-1.5 py-0.5 font-mono text-[11px] text-ink">{String(args.metric)}</span>
              </>
            )}
          </div>
          {((args.groupBy as string[]) || []).length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="text-[11px] text-ink-ghost">{t('smartquery_group_label')}</span>
              {((args.groupBy as string[]) || []).map((g, i) => (
                <span key={i} className="rounded border border-border bg-canvas-alt px-1.5 py-0.5 font-mono text-[11px] text-ink-muted">{g}</span>
              ))}
            </div>
          )}
        </div>
      )}

      {name === 'propose_learned_fact' && (
        <div className="space-y-1">
          {!!args.summary && <div className="text-xs font-semibold text-ink">{String(args.summary)}</div>}
          {((args.keywords as string[]) || []).length > 0 && (
            <div className="flex flex-wrap gap-1">
              {((args.keywords as string[]) || []).map((k, i) => (
                <span key={i} className="rounded border border-success/40 bg-success/5 px-1.5 py-0.5 text-[11px] text-success">{k}</span>
              ))}
            </div>
          )}
        </div>
      )}

      {result.error != null && result.error !== '' && (
        <div className="rounded border border-danger/40 bg-danger/5 px-2 py-1 text-[11px] text-danger">{String(result.error)}</div>
      )}
      {result.content != null && result.content !== '' && (
        <div className="max-h-48 overflow-y-auto rounded border border-border bg-white px-2 py-1.5">
          <MdContent content={String(result.content)} />
        </div>
      )}
      {result.execution_status === 'success' && result.execution_result != null && (
        <ResultViewer data={String(result.execution_result)} initialMode={(result.display_mode as 'table' | 'bar' | 'pie' | 'line') || 'table'} />
      )}
      {result.execution_status === 'error' && result.execution_error != null && (
        <div className="rounded border border-danger/40 bg-danger/5 px-2 py-1 text-[11px] text-danger">{String(result.execution_error)}</div>
      )}
      {!['lookup','smartquery','propose_learned_fact'].includes(name) && Object.keys(args).length > 0 && (
        <pre className="max-h-24 overflow-y-auto whitespace-pre-wrap rounded border border-border bg-canvas-alt px-2 py-1 font-mono text-[11px] text-ink">
          {JSON.stringify(args, null, 2)}
        </pre>
      )}
    </div>
  )
}

// ─── Types ───────────────────────────────────────────────────────

interface ThreadItem {
  id: string
  title: string
  agentType?: 'lakehouse' | 'builder'
  createdAt: string
  updatedAt: string
}

interface StepItem {
  id: string
  stepIndex: number
  role: string
  content: string
  thinking: string
  functionCall?: unknown
  systemPrompt?: string
  llmMessages?: unknown[]
  durationMs: number
  promptTokens?: number
  completionTokens?: number
  totalTokens?: number
  createdAt: string
}

// ─── Role avatar ─────────────────────────────────────────────────

function RoleAvatar({ role }: { role: string }) {
  const Icon = role === 'user' ? User
    : role === 'assistant' ? Bot
    : role === 'tool' || role === 'function' ? Wrench
    : role === 'system' ? Settings
    : Bot
  return (
    <div className="inline-flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full border border-border bg-white">
      <Icon className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />
    </div>
  )
}

// ─── Page ────────────────────────────────────────────────────────

export default function LakehouseAgentHistoryPageMinimal() {
  const t = useTranslations('agent.history')
  const industrial = useStyleMode().mode === 'industrial'
  const roleLabel = (role: string) => ROLE_LABEL_KEYS[role] ? t(ROLE_LABEL_KEYS[role] as Parameters<typeof t>[0]) : role
  // Default the filter to whatever mode the user came from. Main page links
  // here with `?mode=builder` or `?mode=lakehouse`; without that param, fall
  // back to "all" so direct/bookmarked links still show everything.
  const searchParams = useSearchParams()
  const initialFilter: 'all' | 'lakehouse' | 'builder' = (() => {
    const m = searchParams.get('mode')
    return m === 'builder' || m === 'lakehouse' ? m : 'all'
  })()

  const [threads, setThreads] = useState<ThreadItem[]>([])
  const [agentTypeFilter, setAgentTypeFilter] = useState<'all' | 'lakehouse' | 'builder'>(initialFilter)
  const [selectedThread, setSelectedThread] = useState<string>('')
  const [steps, setSteps] = useState<StepItem[]>([])
  const [loading, setLoading] = useState(false)
  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const [threadsLoaded, setThreadsLoaded] = useState(false)
  const [expandedSteps, setExpandedSteps] = useState<Set<number>>(new Set())
  const [rawSteps, setRawSteps] = useState<Set<number>>(new Set())
  const [rawLlmMessages, setRawLlmMessages] = useState<Set<string>>(new Set())
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()

  const threadListRef = useAutoAnimate<HTMLDivElement>()
  const stepListRef = useAutoAnimate<HTMLDivElement>()

  useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search), 250)
    return () => clearTimeout(t)
  }, [search])

  const projectId = currentProject?.id
  const loadThreads = useCallback(async () => {
    if (!projectId) return
    try {
      let url = `/ontology/lakehouse-agent-threads?projectId=${projectId}`
      if (debouncedSearch) url += `&search=${encodeURIComponent(debouncedSearch)}`
      if (agentTypeFilter !== 'all') url += `&agent_type=${agentTypeFilter}`
      const res = await api<{ data: ThreadItem[] }>(url)
      setThreads(res.data || [])
    } catch { setThreads([]) }
    finally { setThreadsLoaded(true) }
  }, [projectId, debouncedSearch, agentTypeFilter])

  useEffect(() => { loadThreads() }, [loadThreads])

  const loadSteps = async (threadId: string) => {
    setSelectedThread(threadId)
    setLoading(true)
    setExpandedSteps(new Set())
    setRawSteps(new Set())
    setRawLlmMessages(new Set())
    try {
      const res = await api<{ steps: StepItem[] }>(`/ontology/lakehouse-agent-threads/${threadId}`)
      const loadedSteps = res.steps || []
      setSteps(loadedSteps)
      if (loadedSteps.length > 0) {
        setExpandedSteps(new Set([loadedSteps.length - 1]))
      }
    } catch {
      msg.error(t('load_fail'))
    } finally {
      setLoading(false)
    }
  }

  const deleteThread = async (id: string) => {
    if (!confirm(t('delete_confirm'))) return
    try {
      await api(`/ontology/lakehouse-agent-threads/${id}`, { method: 'DELETE' })
      msg.success(t('delete_success'))
      if (selectedThread === id) { setSelectedThread(''); setSteps([]) }
      loadThreads()
    } catch { msg.error(t('delete_fail')) }
  }

  const toggleStep = (idx: number) => setExpandedSteps(prev => {
    const n = new Set(prev); n.has(idx) ? n.delete(idx) : n.add(idx); return n
  })
  const toggleRaw = (idx: number) => setRawSteps(prev => {
    const n = new Set(prev); n.has(idx) ? n.delete(idx) : n.add(idx); return n
  })
  const toggleRawLlm = (stepIdx: number, msgIdx: number) => {
    const key = `${stepIdx}-${msgIdx}`
    setRawLlmMessages(prev => {
      const n = new Set(prev); n.has(key) ? n.delete(key) : n.add(key); return n
    })
  }

  const selectedTitle = threads.find(t => t.id === selectedThread)?.title

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* ── Header ────────────────────────────────────────────── */}
      <motion.header
        initial={reduce ? undefined : { opacity: 0, y: -4 }}
        animate={reduce ? undefined : { opacity: 1, y: 0 }}
        transition={{ duration: 0.2, ease: 'easeOut' }}
        className={`flex h-14 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${
          industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'
        }`}
      >
        <div className="flex min-w-0 items-center gap-3">
          {industrial ? (
            <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // CHAT HISTORY
            </span>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <HistoryIcon size={14} className="text-ink" aria-hidden="true" />
              </div>
              <div className="min-w-0">
                <h1 className="text-base font-semibold tracking-tight text-ink">{t('page_title')}</h1>
                <p className="truncate text-xs text-ink-muted">
                  {t('page_desc')}
                </p>
              </div>
            </>
          )}
          {industrial && (
            <span className="font-mono text-[10px] tracking-[0.14em] text-ink-muted tabular-nums">
              {threads.length} THREADS{selectedThread && steps.length > 0 ? ` · ${steps.length} STEPS` : ''}
            </span>
          )}
        </div>
        <div className="flex flex-shrink-0 items-center gap-4">
          {!industrial && (
            <div className="hidden items-baseline gap-x-3 text-xs text-ink-muted md:flex">
              <span>
                {t('thread_count', { count: threads.length })}
              </span>
              {selectedThread && steps.length > 0 && (
                <>
                  <span aria-hidden="true" className="text-ink-ghost">·</span>
                  <span>
                    {t('step_count', { count: steps.length })}
                  </span>
                </>
              )}
            </div>
          )}
          <Link
            href="/ontology/lakehouse-agent"
            className={`inline-flex h-7 items-center gap-1.5 bg-white px-2.5 text-xs text-ink-muted outline-none hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink ${
              industrial
                ? 'border border-ink font-mono tracking-[0.12em] uppercase text-[10px]'
                : 'rounded-md border border-border'
            }`}
          >
            <ExternalLink className="h-3 w-3" aria-hidden="true" />
            {industrial ? 'BACK TO CHAT' : t('back_to_chat')}
          </Link>
        </div>
      </motion.header>

      {/* ── Main: 2-pane ─────────────────────────────────────── */}
      <div className="flex flex-1 min-h-0 overflow-hidden">
        {/* Left: thread list */}
        <aside className={`flex w-80 flex-shrink-0 flex-col min-h-0 bg-white ${industrial ? 'border-r-2 border-ink' : 'border-r border-border'}`}>
          <div className={`flex-shrink-0 px-3 py-2.5 space-y-2 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
            <div className={`flex h-8 items-center gap-1.5 bg-white px-2.5 ${
              industrial ? 'border border-ink' : 'rounded-md border border-border focus-within:border-ink'
            }`}>
              <Search className="h-3.5 w-3.5 flex-shrink-0 text-ink-ghost" aria-hidden="true" />
              <input
                className={`min-w-0 flex-1 bg-transparent outline-none placeholder:text-ink-ghost ${
                  industrial ? 'font-mono text-[12px] text-ink tracking-[0.04em]' : 'text-sm text-ink'
                }`}
                placeholder={industrial ? 'SEARCH...' : t('search_placeholder')}
                aria-label={t('search_aria')}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </div>
            <div className={`flex overflow-hidden ${industrial ? 'border border-ink' : 'border border-border'}`}>
              {(['all', 'lakehouse', 'builder'] as const).map(filter => (
                <button
                  key={filter}
                  onClick={() => setAgentTypeFilter(filter)}
                  className={`flex-1 px-3 py-1.5 transition-colors ${
                    filter !== 'all' ? (industrial ? 'border-l border-ink' : 'border-l border-border') : ''
                  } ${
                    industrial ? 'font-mono text-[10px] uppercase tracking-[0.14em]' : 'text-xs'
                  } ${
                    agentTypeFilter === filter
                      ? 'bg-ink text-white font-semibold'
                      : 'text-ink-muted hover:text-ink hover:bg-canvas-alt'
                  }`}
                >
                  {filter === 'all' ? t('filter_all') : filter === 'lakehouse' ? t('filter_query') : t('filter_builder')}
                </button>
              ))}
            </div>
          </div>
          <div className="flex-1 min-h-0 overflow-y-auto">
            {!threadsLoaded ? (
              <div className="flex h-full items-center justify-center">
                <InlineLoader text={t('loading')} />
              </div>
            ) : threads.length === 0 ? (
              <div className="flex h-full flex-col items-center justify-center gap-1 px-4 text-center">
                <span className="text-sm text-ink-muted">{t('empty_thread_title')}</span>
                <span className="text-xs text-ink-ghost">
                  {search ? t('empty_thread_hint_search') : t('empty_thread_hint_default')}
                </span>
              </div>
            ) : (
              <div ref={threadListRef} className={industrial ? 'divide-y divide-ink/15' : 'divide-y divide-border-light'}>
                {threads.map(thread => {
                  const selected = selectedThread === thread.id
                  return (
                    <div
                      key={thread.id}
                      role="button"
                      tabIndex={0}
                      onClick={() => loadSteps(thread.id)}
                      onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); loadSteps(thread.id) } }}
                      className={`flex cursor-pointer items-center gap-2 border-l-2 px-4 py-3 outline-none focus-visible:ring-1 focus-visible:ring-ink ${
                        selected
                          ? (industrial ? 'border-l-ink bg-ink text-white' : 'border-l-ink bg-canvas-alt')
                          : (industrial ? 'border-l-transparent hover:border-l-ink hover:bg-canvas-alt' : 'border-l-transparent')
                      }`}
                    >
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-1.5 min-w-0">
                          <span className={`truncate font-medium ${
                            industrial
                              ? `font-mono text-[12px] tracking-[0.04em] ${selected ? 'text-white' : 'text-ink'}`
                              : 'text-sm text-ink'
                          }`}>{thread.title || t('no_title')}</span>
                          {thread.agentType === 'builder' && (
                            <span className={`flex-shrink-0 inline-flex items-center px-1.5 py-0.5 font-semibold ${
                              industrial
                                ? `border font-mono text-[9px] tracking-[0.14em] uppercase ${selected ? 'border-white/60 text-white' : 'border-ink text-ink'}`
                                : 'text-[10px] border border-accent/30 bg-accent/10 text-accent'
                            }`}>
                              {industrial ? 'BUILDER' : t('builder_badge')}
                            </span>
                          )}
                        </div>
                        <div className={`mt-0.5 tabular-nums ${
                          industrial
                            ? `font-mono text-[10px] tracking-[0.06em] ${selected ? 'text-white/70' : 'text-ink-ghost'}`
                            : 'text-[11px] text-ink-ghost'
                        }`}>
                          {new Date(thread.updatedAt).toLocaleString('zh-CN')}
                        </div>
                      </div>
                      <div className="flex flex-shrink-0 gap-1">
                        <Link
                          href={`/ontology/lakehouse-agent?mode=${thread.agentType === 'builder' ? 'builder' : 'lakehouse'}&threadId=${thread.id}`}
                          onClick={(e) => e.stopPropagation()}
                          className="p-1 text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                          title={t('open_in_chat_title')}
                          aria-label={t('open_in_chat_aria')}
                        >
                          <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
                        </Link>
                        <motion.button
                          onClick={(e) => { e.stopPropagation(); deleteThread(thread.id) }}
                          whileHover={reduce ? undefined : { scale: 1.15 }}
                          whileTap={reduce ? undefined : { scale: 0.9 }}
                          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                          className="p-1 text-ink-ghost outline-none hover:text-danger focus-visible:ring-1 focus-visible:ring-ink"
                          title={t('delete_confirm')}
                          aria-label={t('delete_confirm')}
                        >
                          <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                        </motion.button>
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </aside>

        {/* Right: steps detail */}
        <div className="flex flex-1 min-h-0 flex-col overflow-hidden bg-canvas">
          {selectedThread && selectedTitle && (
            <div className={`flex flex-shrink-0 items-center gap-2 bg-white px-6 py-2.5 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
              {industrial && (
                <span className="font-mono text-[10px] tracking-[0.22em] text-ink-ghost">// THREAD ·</span>
              )}
              <span className={`truncate ${industrial ? 'font-mono text-[12px] font-semibold tracking-[0.04em] text-ink' : 'text-sm font-medium text-ink'}`}>{selectedTitle}</span>
              {steps.length > 0 && (
                <span className={industrial ? 'font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-muted' : 'text-[11px] text-ink-muted'}>
                  {industrial ? `${steps.length} STEPS` : t('step_total_steps', { count: steps.length })}
                </span>
              )}
            </div>
          )}
          <div className="flex-1 min-h-0 overflow-y-auto">
            {!selectedThread && (
              <div className="flex h-full flex-col items-center justify-center gap-1 text-center">
                <span className="text-sm text-ink-muted">{t('select_hint_title')}</span>
                <span className="text-xs text-ink-ghost">{t('select_hint_desc')}</span>
              </div>
            )}
            {selectedThread && loading && (
              <div className="flex h-full items-center justify-center">
                <InlineLoader text={t('loading')} />
              </div>
            )}
            {selectedThread && !loading && (() => {
              const totalDurationMs = steps.reduce((sum, s) => sum + (s.durationMs || 0), 0)
              const lastAssistantIdx = steps.reduce((acc, s, i) => s.role === 'assistant' ? i : acc, -1)
              return (
                <div ref={stepListRef} className="space-y-3 p-6">
                  {steps.length === 0 && (
                    <div className="flex h-32 items-center justify-center text-sm text-ink-muted">
                      {t('empty_steps')}
                    </div>
                  )}
                  {steps.map((s, i) => {
                    const expanded = expandedSteps.has(i)
                    const fcName = s.functionCall ? String((s.functionCall as Record<string, unknown>).name || '') : ''
                    const fcMeta = fcName ? (TOOL_META_KEYS[fcName] || { labelKey: fcName }) : null
                    return (
                      <div key={s.id} className={`overflow-hidden bg-white ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}>
                        {/* Step header row */}
                        <motion.button
                          type="button"
                          onClick={() => toggleStep(i)}
                          whileTap={reduce ? undefined : { scale: 0.995 }}
                          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                          aria-expanded={expanded}
                          className={`flex w-full items-center gap-2 bg-canvas-alt px-4 py-2.5 text-left outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                            industrial ? 'border-b border-ink' : 'border-b border-border-light'
                          }`}
                        >
                          {expanded
                            ? <ChevronDown className="h-4 w-4 flex-shrink-0 text-ink-ghost" aria-hidden="true" />
                            : <ChevronRight className="h-4 w-4 flex-shrink-0 text-ink-ghost" aria-hidden="true" />}
                          <RoleAvatar role={s.role} />
                          <span className={industrial ? 'font-mono text-[12px] font-semibold uppercase tracking-[0.12em] text-ink' : 'text-sm font-semibold text-ink'}>{roleLabel(s.role)}</span>
                          <span className={industrial ? 'font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-ghost' : 'text-[11px] tabular-nums text-ink-ghost'}>{t('step_index', { index: s.stepIndex })}</span>
                          {fcMeta && (
                            <span className={`inline-flex items-center gap-1 bg-white px-1.5 py-0.5 text-ink-muted ${
                              industrial
                                ? 'border border-ink font-mono text-[10px] uppercase tracking-[0.1em]'
                                : 'rounded border border-border text-[11px]'
                            }`}>
                              {fcMeta.labelKey in TOOL_META_KEYS ? t(fcMeta.labelKey as Parameters<typeof t>[0]) : fcMeta.labelKey}
                              <span className="font-mono text-ink-ghost">· {fcName}</span>
                            </span>
                          )}
                          <span className={`ml-auto inline-flex items-center gap-2 text-ink-ghost ${
                            industrial ? 'font-mono text-[10px] tracking-[0.14em]' : 'text-[11px]'
                          }`}>
                            {s.durationMs > 0 && <span><span className="tabular-nums">{s.durationMs}</span>ms</span>}
                            {s.totalTokens != null && s.totalTokens > 0 && (
                              <span><span className="tabular-nums">{s.totalTokens}</span> tokens</span>
                            )}
                          </span>
                        </motion.button>

                        {/* Expanded body */}
                        <AnimatePresence initial={false}>
                          {expanded && (
                            <motion.div
                              initial={reduce ? undefined : { height: 0, opacity: 0 }}
                              animate={reduce ? undefined : { height: 'auto', opacity: 1 }}
                              exit={reduce ? undefined : { height: 0, opacity: 0 }}
                              transition={{ duration: 0.2, ease: 'easeOut' }}
                              style={{ overflow: 'hidden' }}
                            >
                              <div className="space-y-2.5 px-4 py-3">
                                {s.content && (
                                  <div className="border-l-2 border-border pl-3">
                                    <div className="mb-1 flex justify-end">
                                      <motion.button
                                        onClick={() => toggleRaw(i)}
                                        whileTap={reduce ? undefined : { scale: 0.97 }}
                                        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                                        aria-pressed={rawSteps.has(i)}
                                        title={rawSteps.has(i) ? t('toggle_raw_title_to_render') : t('toggle_raw_title_to_raw')}
                                        className={`inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px] outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                                          rawSteps.has(i)
                                            ? 'border-ink bg-ink text-white'
                                            : 'border-border text-ink-muted hover:border-ink hover:text-ink'
                                        }`}
                                      >
                                        {rawSteps.has(i) ? <Code className="h-3 w-3" aria-hidden="true" /> : <FileType className="h-3 w-3" aria-hidden="true" />}
                                        {rawSteps.has(i) ? t('raw_label') : t('render_label')}
                                      </motion.button>
                                    </div>
                                    {rawSteps.has(i) ? (
                                      <pre className="whitespace-pre-wrap font-mono text-sm text-ink">{s.content}</pre>
                                    ) : (
                                      <MdContent content={s.content} />
                                    )}
                                  </div>
                                )}

                                {s.thinking && (
                                  <details>
                                    <summary className="cursor-pointer select-none text-xs text-ink-muted outline-none focus-visible:ring-1 focus-visible:ring-ink">
                                      {t('thinking_label')}
                                    </summary>
                                    <pre className="mt-1 max-h-32 overflow-y-auto whitespace-pre-wrap rounded border border-border-light bg-canvas-alt px-2 py-1.5 font-mono text-[11px] leading-relaxed text-ink-muted">
                                      {s.thinking}
                                    </pre>
                                  </details>
                                )}

                                {!!s.functionCall && (
                                  <details open>
                                    <summary className="mb-1 cursor-pointer select-none text-xs text-ink-muted outline-none focus-visible:ring-1 focus-visible:ring-ink">
                                      {t('tool_call_label')}
                                    </summary>
                                    <div className="mt-1 rounded-md border border-border bg-white px-3 py-2">
                                      <HistoryFunctionCall fc={s.functionCall} />
                                    </div>
                                  </details>
                                )}

                                {s.llmMessages && s.llmMessages.length > 0 && (
                                  <details>
                                    <summary className="cursor-pointer select-none text-xs text-ink-muted outline-none focus-visible:ring-1 focus-visible:ring-ink">
                                      {t('llm_messages_label')}
                                      <span className="ml-1 text-ink-ghost">
                                        {t('llm_messages_count', { count: s.llmMessages.length })}
                                      </span>
                                    </summary>
                                    <div className="mt-1 max-h-[40rem] space-y-2 overflow-y-auto">
                                      {s.llmMessages.map((lmsg, mi) => {
                                        const m = lmsg as { role: string; content?: unknown }
                                        const role = m.role
                                        const content = typeof m.content === 'string'
                                          ? m.content
                                          : JSON.stringify(m.content, null, 2)
                                        const isRaw = rawLlmMessages.has(`${i}-${mi}`)
                                        // 黑白灰绿红 ：system=中灰加粗边框；user=深黑；assistant=默认；tool=虚线
                                        const borderColor =
                                          role === 'system' ? 'border-ink-muted'
                                            : role === 'user' ? 'border-ink'
                                            : role === 'tool' || role === 'function' ? 'border-dashed border-border'
                                            : 'border-border'
                                        const labelColor =
                                          role === 'system' ? 'text-ink-muted'
                                            : role === 'user' ? 'text-ink'
                                            : role === 'tool' || role === 'function' ? 'text-ink-ghost'
                                            : 'text-ink-muted'
                                        return (
                                          <div key={mi} className={`border-l-2 pl-2 ${borderColor}`}>
                                            <div className="mb-0.5 flex items-center gap-2">
                                              <span className={`text-[11px] font-semibold ${labelColor}`}>
                                                <span className="tabular-nums">[{mi}]</span> {roleLabel(role)}
                                                <span className="ml-1 font-mono text-ink-ghost">· {role}</span>
                                              </span>
                                              <motion.button
                                                onClick={() => toggleRawLlm(i, mi)}
                                                whileTap={reduce ? undefined : { scale: 0.97 }}
                                                transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                                                aria-pressed={isRaw}
                                                title={isRaw ? t('toggle_raw_title_to_render') : t('toggle_raw_title_to_raw')}
                                                className={`ml-auto inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px] outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                                                  isRaw
                                                    ? 'border-ink bg-ink text-white'
                                                    : 'border-border text-ink-muted hover:border-ink hover:text-ink'
                                                }`}
                                              >
                                                {isRaw ? <Code className="h-3 w-3" aria-hidden="true" /> : <FileType className="h-3 w-3" aria-hidden="true" />}
                                                {isRaw ? t('raw_label') : t('render_label')}
                                              </motion.button>
                                            </div>
                                            {isRaw ? (
                                              <pre className="max-h-64 overflow-y-auto whitespace-pre-wrap rounded border border-border-light bg-canvas-alt px-2 py-1 font-mono text-[11px] text-ink">
                                                {content}
                                              </pre>
                                            ) : (
                                              <MdContent content={content} />
                                            )}
                                          </div>
                                        )
                                      })}
                                    </div>
                                  </details>
                                )}

                                {s.systemPrompt && (
                                  <details>
                                    <summary className="cursor-pointer select-none text-xs text-ink-muted outline-none focus-visible:ring-1 focus-visible:ring-ink">
                                      {t('system_prompt_label')}
                                    </summary>
                                    <pre className="mt-1 max-h-64 overflow-y-auto whitespace-pre-wrap rounded border border-border-light bg-canvas-alt px-2 py-1.5 font-mono text-[11px] leading-relaxed text-ink-muted">
                                      {s.systemPrompt}
                                    </pre>
                                  </details>
                                )}

                                {i === lastAssistantIdx && totalDurationMs > 0 && (
                                  <div className="mt-1 flex items-center gap-2 border-t border-border-light pt-2 text-[11px] text-ink-ghost">
                                    <span>{t('step_duration_total')}</span>
                                    <span className="font-semibold tabular-nums text-ink">
                                      {(totalDurationMs / 1000).toFixed(1)}s
                                    </span>
                                    <span>
                                      {t('step_total_steps', { count: steps.length })}
                                    </span>
                                  </div>
                                )}
                              </div>
                            </motion.div>
                          )}
                        </AnimatePresence>
                      </div>
                    )
                  })}
                </div>
              )
            })()}
          </div>
        </div>
      </div>
    </div>
  )
}
