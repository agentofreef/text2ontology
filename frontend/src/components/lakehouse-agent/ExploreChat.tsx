'use client'

// Explore mode · AI-native workspace.
//
// Layout (3-region grid below the mode-tab header):
//   ┌──────────┬──────────────────────┬──────────┐
//   │ Threads  │  Conversation stream │  Draft   │
//   │ (260px)  │  (flex-1)            │  Canvas  │
//   │          │                      │  (360px) │
//   │          │  ──────────────      │          │
//   │          │  Composer (sticky)   │          │
//   └──────────┴──────────────────────┴──────────┘
//
// The DraftCanvas (right rail) is the AI-native differentiator: it exposes
// "what the AI is currently building" — facets pulse on each tool call,
// then crystallise into a full CommitCard when the agent emits one. The user
// accepts in-place; the commit_card NEVER appears inline as a chat bubble.
//
// Wire compatibility: SSE protocol unchanged (data: {type:...}\n\n). Backend
// agent_type='explore' handler emits {thread, function_call, chunk,
// commit_card, error, done}. We dispatch by `type` field.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'
import { useTranslations, useLocale } from 'next-intl'
import { BookOpen, User, Bot, Search, Database, Sparkles, Check } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useProject } from '@/lib/project'
import { useMessage } from '@/lib/message'
import { useStyleMode } from '@/lib/style-mode'
import { useFetch } from '@/lib/hooks'
import { llmDisplay } from '@/lib/llmDisplay'
import { Button } from '@/components/ui/Button'
import type { LLMConfig, LLMRoleBinding } from '@/types/api'
import { api } from '@/lib/api'
import { streamSseEvents, type SseEvent } from '@/components/lakehouse-agent/sseEventStream'
import { ThreadList } from '@/components/lakehouse-agent/ThreadList'
import { type ValidatorRejection } from '@/components/lakehouse-agent/CommitCard'
import type { CommitCardPayload } from '@/components/lakehouse-agent/commitCardLogic'
import { Composer } from '@/components/lakehouse-agent/explore/Composer'
import { EmptyState } from '@/components/lakehouse-agent/explore/EmptyState'
import { ToolCallChip, type ToolCallStatus } from '@/components/lakehouse-agent/explore/ToolCallChip'
import { CommitCard } from '@/components/lakehouse-agent/CommitCard'

// History API response shape (subset we consume).
interface ThreadStep {
  id: string
  stepIndex: number
  role: string
  content: string
  thinking?: string
  functionCall?: { name?: string; arguments?: unknown; result?: unknown } | null
}
interface ThreadDetail {
  steps?: ThreadStep[]
}

// In-stream message kinds (commit_card NOT here — handled in right rail)
interface MetricResult {
  draftId?: string
  columns?: string[]
  rows?: Record<string, unknown>[]
  totalRows?: number
  executedSql?: string
  error?: string
}
type StreamMsg =
  | { kind: 'user'; content: string }
  | { kind: 'assistant'; content: string }
  | { kind: 'tool_call'; id: string; name: string; arguments?: unknown; result?: unknown; status: ToolCallStatus; commitPayload?: CommitCardPayload }
  | { kind: 'metric_result'; result: MetricResult }

export function ExploreChat() {
  const t = useTranslations('agent.main.explore')
  const tHeader = useTranslations('agent.main.header')
  const router = useRouter()
  const locale = useLocale()
  const searchParams = useSearchParams()
  // Explore mode is always industrial style — per product directive:
  // the chat surface IS the AI-native workbench, not a "soft" consumer page.
  // No toggle, no theme variance. Ink + canvas-alt + mono only.
  const industrial = true
  // The HEADER (title + mode tabs + right controls) must render with the SAME
  // style mode as the query/builder header so the mode-tab switcher sits at a
  // pixel-identical x across all three modes. The body stays always-industrial
  // (`industrial` above) per product directive, but the header follows the
  // real user style mode like LakehouseAgentChat does.
  const headerIndustrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const message = useMessage()

  const urlThreadId = searchParams.get('threadId')
  const [threadId, setThreadId] = useState<string | null>(urlThreadId)
  const [messages, setMessages] = useState<StreamMsg[]>([])
  const [streaming, setStreaming] = useState(false)
  const [input, setInput] = useState('')
  const [validatorRejection, setValidatorRejection] = useState<ValidatorRejection | null>(null)
  // Thread-history drawer: lives on the right edge, collapsed by default.
  // The chat area is the primary surface; threads are a "give me back to
  // an old conversation" affordance, not an always-on rail.
  const [threadsOpen, setThreadsOpen] = useState(false)

  // Latest commit_card payload — kept so the accepted-toast message can
  // include the metric name. The right-rail Inspector renders the draft
  // off the selected chip, not off this state.
  const [canvasPayload, setCanvasPayload] = useState<CommitCardPayload | null>(null)
  const [acceptedMetricId, setAcceptedMetricId] = useState<string | null>(null)
  // Which tool_call chip is currently selected. The right rail (Inspector)
  // renders the details for THIS chip. Click any chip to switch. When a
  // commit_card chip arrives, we auto-select it so the metric draft surface
  // appears in-place without the user having to hunt.
  const [selectedToolKey, setSelectedToolKey] = useState<string | null>(null)

  const streamRef = useRef<HTMLDivElement | null>(null)

  // URL ↔ threadId sync
  useEffect(() => {
    if (urlThreadId !== threadId) {
      setThreadId(urlThreadId)
      setMessages([])
      setCanvasPayload(null)
      setAcceptedMetricId(null)
      setValidatorRejection(null)
      setSelectedToolKey(null)
    }
  }, [urlThreadId, threadId])

  // Rehydrate history when threadId is set and we have no in-memory messages.
  // GET /api/ontology/lakehouse-agent-threads/<id> returns the full step list;
  // we map (role, function_call) → StreamMsg union and detect commit_card from
  // function_call so the right rail re-materialises the draft on reload.
  useEffect(() => {
    if (!threadId) return
    if (messages.length > 0) return
    let cancelled = false
    void (async () => {
      try {
        const detail = await api<ThreadDetail>(`/ontology/lakehouse-agent-threads/${threadId}`)
        if (cancelled) return
        const steps = detail.steps ?? []
        const newMsgs: StreamMsg[] = []
        let lastCommitPayload: CommitCardPayload | null = null

        for (const s of steps) {
          if (s.role === 'user') {
            newMsgs.push({ kind: 'user', content: s.content })
            continue
          }
          if (s.role === 'assistant') {
            if (s.functionCall && (s.functionCall.name || s.functionCall.arguments)) {
              const fc = s.functionCall as { name?: string; arguments?: unknown; result?: unknown }
              const name = fc.name || 'tool'
              if (name === 'commit_card') {
                // Reconstruct the payload from args and inject it as a
                // clickable tool_call chip with commitPayload attached.
                const args = fc.arguments as Partial<CommitCardPayload> | undefined
                if (args) {
                  const reconstructed: CommitCardPayload = {
                    id: args.id || '',
                    draftId: args.draftId || '',
                    name: args.name || '',
                    displayName: args.displayName || '',
                    primaryOd: args.primaryOd || '',
                    intent: args.intent,
                    measure: args.measure,
                    dimensions: args.dimensions,
                    filters: args.filters,
                    canonicalMetric: args.canonicalMetric || '',
                    querySql: args.querySql || '',
                    autoGroupBy: args.autoGroupBy || [],
                    parameters: args.parameters || [],
                    triggerKeywords: args.triggerKeywords || [],
                    responseTemplate: args.responseTemplate || '',
                    description: args.description || '',
                  }
                  lastCommitPayload = reconstructed
                  newMsgs.push({
                    kind: 'tool_call',
                    id: s.id,
                    name,
                    arguments: fc.arguments,
                    result: reconstructed,
                    status: 'ok',
                    commitPayload: reconstructed,
                  })
                }
                continue
              }
              // Regular tool call
              newMsgs.push({
                kind: 'tool_call',
                id: s.id,
                name,
                arguments: fc.arguments,
                result: fc.result,
                status: 'ok',
              })
              continue
            }
            if (s.content) {
              newMsgs.push({ kind: 'assistant', content: s.content })
            }
            continue
          }
          // system / tool roles: skip silently (system prompt is metadata)
        }

        if (cancelled) return
        setMessages(newMsgs)
        if (lastCommitPayload) setCanvasPayload(lastCommitPayload)
        // Auto-select the latest commit_card chip on rehydration so the
        // right rail shows the metric draft immediately.
        for (let i = newMsgs.length - 1; i >= 0; i--) {
          const m = newMsgs[i]
          if (m.kind === 'tool_call' && m.name === 'commit_card') {
            setSelectedToolKey(m.id)
            break
          }
        }
      } catch (err) {
        // Thread may not exist or be inaccessible — leave empty state.
        console.warn('[explore] history fetch failed', err)
      }
    })()
    return () => { cancelled = true }
  }, [threadId, messages.length])

  // Scroll to bottom on new messages
  useEffect(() => {
    if (streamRef.current) streamRef.current.scrollTop = streamRef.current.scrollHeight
  }, [messages, streaming])

  const navigateMode = useCallback(
    (mode: 'lakehouse' | 'builder' | 'explore') => {
      if (mode === 'explore') return
      router.push(`/${locale}/ontology/lakehouse-agent?mode=${mode}`)
    },
    [router, locale],
  )

  const handleSelectThread = useCallback(
    (id: string | null) => {
      // Build the path WITHOUT the basePath prefix. Reading url.pathname
      // from window.location.href includes Next's basePath (/lakehouse),
      // and router.replace re-prepends it — causing the redirect to land
      // on a non-existent route, which falls back to query mode.
      // Fix: construct a router-canonical path manually + always pin
      // mode=explore so the dispatcher (page.tsx line 1907) routes here.
      const params = new URLSearchParams()
      params.set('mode', 'explore')
      if (id) params.set('threadId', id)
      router.replace(`/${locale}/ontology/lakehouse-agent?${params.toString()}`)
    },
    [router, locale],
  )

  // New conversation — clear in-memory state immediately + drop threadId from
  // the URL (staying in explore mode). Mirrors the chat header's 新建 button.
  const startNewThread = useCallback(() => {
    setThreadId(null)
    setMessages([])
    setCanvasPayload(null)
    setAcceptedMetricId(null)
    setValidatorRejection(null)
    setSelectedToolKey(null)
    router.replace(`/${locale}/ontology/lakehouse-agent?mode=explore`)
  }, [router, locale])

  // Agent LLM binding — same surface as the query/builder header. Lets the
  // user switch the model the explore agent uses; writes /llm-role-binding
  // (roleName=agent), effective on the next send.
  const { data: llmConfigs } = useFetch<LLMConfig>('/llm-config')
  const { data: roleBindings, refetch: refetchBindings } = useFetch<LLMRoleBinding>('/llm-role-binding')
  const chatConfigs = useMemo(() => llmConfigs.filter((c) => c.configType === 'chat'), [llmConfigs])
  const activeChatId = useMemo(() => chatConfigs.find((c) => c.isActive)?.id || '', [chatConfigs])
  const agentBindingId = useMemo(
    () => roleBindings.find((b) => b.roleName === 'agent')?.configId || '',
    [roleBindings],
  )
  const agentSelectedId = agentBindingId || activeChatId
  const onAgentLLMChange = useCallback(async (newId: string) => {
    try {
      if (!newId) {
        await api(`/llm-role-binding/agent`, { method: 'DELETE' })
        message.success(tHeader('agent_llm_unbound'))
      } else {
        await api('/llm-role-binding', { method: 'PUT', body: { roleName: 'agent', configId: newId } })
        const c = chatConfigs.find((x) => x.id === newId)
        message.success(llmDisplay(c) || c?.modelName || '')
      }
      refetchBindings()
    } catch {
      message.error('切换失败')
    }
  }, [chatConfigs, refetchBindings, message, tHeader])

  const onRejection = useCallback((r: ValidatorRejection) => {
    setValidatorRejection(r)
    message.error(t('error.stream_failed') + ' · ' + (r.code || r.error))
  }, [message, t])

  const onAccepted = useCallback((metricId: string) => {
    setAcceptedMetricId(metricId)
    message.success(t('commit_card.accepted_bubble', { name: canvasPayload?.name || metricId }))
  }, [message, t, canvasPayload])

  const send = useCallback(async (overrideText?: string) => {
    const text = (overrideText ?? input).trim()
    if (!text || streaming) return

    if (!currentProject?.id) {
      message.error('未选项目')
      return
    }

    setMessages((prev) => [...prev, { kind: 'user', content: text }])
    setInput('')
    setStreaming(true)
    // New turn — reset rejection note + clear stale canvas payload (a new
    // user prompt means the previous draft is no longer relevant)
    const rejectionForThisTurn = validatorRejection
    setValidatorRejection(null)
    if (canvasPayload && !acceptedMetricId) {
      // Previous turn's draft was not accepted; clearing canvas signals the
      // user moved on. If they want it back, they can re-prompt.
      setCanvasPayload(null)
    }
    setAcceptedMetricId(null)
    setSelectedToolKey(null)

    try {
      await streamSseEvents(
        '/ontology/lakehouse-agent-stream',
        {
          projectId: currentProject.id,
          threadId,
          mode: 'explore',
          messages: [{ role: 'user', content: text }],
          _validatorRejection: rejectionForThisTurn || undefined,
        },
        (evt: SseEvent) => handleSseEvent(evt),
      )
    } catch (err) {
      message.error(t('error.stream_failed') + ' · ' + (err as Error).message)
    } finally {
      setStreaming(false)
    }
  }, [input, streaming, currentProject, threadId, message, t, validatorRejection, canvasPayload, acceptedMetricId])

  const handleSseEvent = useCallback((evt: SseEvent) => {
    const type = (evt as { type?: string }).type
    if (!type) return

    if (type === 'thread') {
      const incomingId = (evt as { threadId?: string }).threadId
      if (incomingId && incomingId !== threadId) {
        setThreadId(incomingId)
        const url = new URL(window.location.href)
        url.searchParams.set('threadId', incomingId)
        window.history.replaceState({}, '', `${url.pathname}${url.search}`)
      }
      return
    }

    if (type === 'function_call' || type === 'tool_call') {
      const e = evt as { id?: string; name?: string; arguments?: unknown; result?: unknown; status?: ToolCallStatus }
      const name = e.name || 'tool'
      const id = e.id || `tc-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`
      const status: ToolCallStatus = e.status || (e.result !== undefined ? 'ok' : 'running')

      setMessages((prev) => {
        // If a chip with same id already exists, update it
        const ix = prev.findIndex((m) => m.kind === 'tool_call' && m.id === id)
        if (ix >= 0) {
          const next = [...prev]
          const old = prev[ix] as Extract<StreamMsg, { kind: 'tool_call' }>
          next[ix] = { ...old, name, arguments: e.arguments ?? old.arguments, result: e.result ?? old.result, status }
          return next
        }
        return [...prev, { kind: 'tool_call', id, name, arguments: e.arguments, result: e.result, status }]
      })

      return
    }

    // tool_result — backend emits this AFTER function_call to deliver the
    // tool's return value. The handler does NOT include a tool-call id in
    // this event, so we match the most-recent `running` chip with the same
    // name and flip it to `ok` + attach result. Without this, chips stay
    // stuck on "进行中" forever (the original UX bug).
    if (type === 'tool_result') {
      const e = evt as { name?: string; result?: unknown }
      const name = e.name || ''
      if (!name) return
      setMessages((prev) => {
        // Walk from the tail to find the latest running chip matching this tool name.
        for (let i = prev.length - 1; i >= 0; i--) {
          const m = prev[i]
          if (m.kind === 'tool_call' && m.name === name && m.status === 'running') {
            const next = [...prev]
            // Detect tool-error shape: {error: ...} or {code: ...}. The
            // chip then renders amber, not green — important for honesty
            // when smartquery fails but the LLM keeps going.
            const r = e.result as Record<string, unknown> | undefined
            const isErr = !!(r && (r.error || (typeof r.code === 'string' && r.code !== '')))
            next[i] = { ...m, result: e.result, status: isErr ? 'error' : 'ok' }
            return next
          }
        }
        return prev
      })
      return
    }

    if (type === 'commit_card') {
      const e = evt as unknown as { payload?: CommitCardPayload } & CommitCardPayload
      const payload = e.payload || (e as unknown as CommitCardPayload)
      setCanvasPayload(payload)
      // Find the latest commit_card tool_call chip (still 'running' since
      // the backend doesn't emit tool_result for commit_card) and bind the
      // payload + flip status to 'ok'. Also auto-select it so the right
      // rail surfaces the metric draft in-place.
      let selectId: string | null = null
      setMessages((prev) => {
        for (let i = prev.length - 1; i >= 0; i--) {
          const m = prev[i]
          if (m.kind === 'tool_call' && m.name === 'commit_card' && m.status === 'running') {
            const next = [...prev]
            next[i] = { ...m, status: 'ok', result: payload, commitPayload: payload }
            selectId = m.id
            return next
          }
        }
        return prev
      })
      // setState callbacks run async — defer the selection so it picks up
      // the new id after the messages update has committed.
      if (selectId) setSelectedToolKey(selectId)
      return
    }

    if (type === 'metric_result') {
      const e = evt as unknown as MetricResult & { type?: string }
      const result: MetricResult = {
        draftId: e.draftId,
        columns: e.columns,
        rows: e.rows,
        totalRows: e.totalRows,
        executedSql: e.executedSql,
        error: e.error,
      }
      setMessages((prev) => [...prev, { kind: 'metric_result', result }])
      return
    }

    if (type === 'chunk' || type === 'delta' || type === 'token') {
      // `token` is the explore handler's emit name for the LLM's final text
      // response — including the post-metric_result summary the prompt
      // explicitly asks for ("请用一句话总结要点"). Treat it like chunk/delta.
      const piece = (evt as { content?: string }).content || ''
      if (!piece) return
      setMessages((prev) => {
        const last = prev[prev.length - 1]
        if (last && last.kind === 'assistant') {
          const next = [...prev]
          next[next.length - 1] = { ...last, content: last.content + piece }
          return next
        }
        return [...prev, { kind: 'assistant', content: piece }]
      })
      return
    }

    if (type === 'error') {
      const msg = (evt as { message?: string }).message || 'stream error'
      message.error(msg)
      return
    }

    // done / other types — no-op
  }, [threadId, message])

  // Resolve the selected tool_call message for the right-rail inspector.
  // If nothing is explicitly selected, fall back to the latest tool_call —
  // typically the most recent commit_card or the chip the AI just emitted.
  const selectedMsg = useMemo<Extract<StreamMsg, { kind: 'tool_call' }> | null>(() => {
    if (selectedToolKey) {
      const hit = messages.find((m) => m.kind === 'tool_call' && m.id === selectedToolKey)
      if (hit && hit.kind === 'tool_call') return hit
    }
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i]
      if (m.kind === 'tool_call') return m
    }
    return null
  }, [messages, selectedToolKey])

  const hasContent = messages.length > 0

  return (
    <div className="flex h-full w-full flex-col bg-canvas-alt">
      {/* Header with mode tabs — STRUCTURALLY identical to LakehouseAgentChat's
          header (same title block, same tab group, same right controls) so the
          mode-tab switcher sits at a pixel-identical x position across query /
          builder / explore. Renders with the real user style mode
          (`headerIndustrial`), NOT the body's forced-industrial flag. */}
      <div className={`flex h-14 items-center justify-between px-4 flex-shrink-0 bg-white ${headerIndustrial ? 'border-b-2 border-ink' : 'border-b border-gray-200'}`}>
        <div className="flex items-center gap-3">
          {headerIndustrial ? (
            <span className="inline-block min-w-[15rem] font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              // {tHeader('title_explore').toString().toUpperCase()}
            </span>
          ) : (
            <>
              <BookOpen className="h-5 w-5 text-gray-600" />
              <h1 className="inline-block min-w-[10rem] text-base font-semibold text-gray-900">
                {tHeader('title_explore')}
              </h1>
            </>
          )}
          <div className={`flex overflow-hidden ${headerIndustrial ? 'border border-ink' : 'border border-gray-200'}`}>
            <button
              onClick={() => navigateMode('lakehouse')}
              className={
                headerIndustrial
                  ? `font-mono text-[10px] tracking-[0.18em] px-3 py-1 transition-colors text-ink-muted hover:text-ink`
                  : `text-xs px-3 py-1 transition-colors text-gray-500 hover:text-gray-800 hover:bg-gray-50`
              }
            >
              {headerIndustrial ? tHeader('mode_query').toString().toUpperCase() : tHeader('mode_query')}
            </button>
            <button
              onClick={() => navigateMode('builder')}
              className={
                headerIndustrial
                  ? `font-mono text-[10px] tracking-[0.18em] px-3 py-1 transition-colors border-l border-ink text-ink-muted hover:text-ink`
                  : `text-xs px-3 py-1 transition-colors border-l border-gray-200 text-gray-500 hover:text-gray-800 hover:bg-gray-50`
              }
            >
              {headerIndustrial ? tHeader('mode_builder').toString().toUpperCase() : tHeader('mode_builder')}
            </button>
            <button
              onClick={() => navigateMode('explore')}
              className={
                headerIndustrial
                  ? `font-mono text-[10px] tracking-[0.18em] px-3 py-1 transition-colors border-l border-ink bg-ink text-white`
                  : `text-xs px-3 py-1 transition-colors border-l border-gray-200 bg-gray-900 text-white font-semibold`
              }
            >
              {headerIndustrial ? tHeader('mode_explore').toString().toUpperCase() : tHeader('mode_explore')}
            </button>
          </div>
          {/* Thread id chip — breadcrumb parity with the query/builder header. */}
          {threadId && (
            <span className={headerIndustrial
              ? 'font-mono text-[10px] tracking-[0.08em] text-ink-ghost'
              : 'text-[10px] text-gray-400 font-mono'
            }>
              #{threadId.slice(0, 8)}
            </span>
          )}
        </div>

        {/* Right-side control group — mirrors the query/builder header markup
            exactly (Agent LLM selector + 新对话 + 历史记录) so the top bar is
            consistent across all three modes. The graph-fullscreen toggle was
            removed from all modes. */}
        <div className="flex items-center gap-2">
          {chatConfigs.length > 0 && (() => {
            const selectedConfig = chatConfigs.find((c) => c.id === agentSelectedId)
            const tooltip = agentBindingId
              ? tHeader('agent_llm_bound_tooltip', { vendor: selectedConfig?.vendor ?? '', modelName: selectedConfig?.modelName ?? '' })
              : tHeader('agent_llm_unbound_tooltip')
            return (
              <label className="flex items-center gap-1.5">
                <span className="text-[10px] font-semibold uppercase tracking-wider text-ink-ghost">Agent LLM</span>
                <select
                  value={agentSelectedId}
                  onChange={(e) => onAgentLLMChange(e.target.value)}
                  title={tooltip}
                  className="max-w-[220px] rounded border border-border bg-white px-2 py-1 text-xs text-ink transition-colors hover:border-ink-strong focus:border-ink focus:outline-none"
                >
                  <option value="">{tHeader('agent_llm_unbound')}</option>
                  {chatConfigs.map((c) => (
                    <option key={c.id} value={c.id}>
                      {llmDisplay(c)}{c.isActive ? ' ★' : ''}
                    </option>
                  ))}
                </select>
              </label>
            )
          })()}
          <Button variant="ghost" size="sm" onClick={startNewThread}>{tHeader('new_thread')}</Button>
          <button
            onClick={() => setThreadsOpen((v) => !v)}
            className={`border rounded px-3 py-1.5 text-sm transition-colors ${
              threadsOpen
                ? 'border-gray-400 bg-gray-50 text-gray-800'
                : 'border-gray-200 text-gray-500 hover:text-gray-800 hover:border-gray-400'
            }`}
          >
            {tHeader('history')}
          </button>
        </div>
      </div>

      {/* Body: chat + draft canvas + collapsible threads drawer */}
      <div className="flex min-h-0 flex-1">
        {/* Center: conversation + composer (now full-width primary surface) */}
        <main className="flex h-full min-w-0 flex-1 flex-col bg-white">
          <div
            ref={streamRef}
            className="flex-1 overflow-y-auto"
          >
            {!hasContent ? (
              <EmptyState onPrompt={(text) => send(text)} />
            ) : (
              <div className="mx-auto flex max-w-3xl flex-col gap-5 px-6 py-8">
                {messages.map((m, i) => (
                  <StreamRow
                    key={i}
                    msg={m}
                    industrial={industrial}
                    selectedToolKey={selectedToolKey}
                    onSelectTool={(id) => setSelectedToolKey(id)}
                  />
                ))}
                {streaming && <ThinkingDots industrial={industrial} />}
              </div>
            )}
          </div>

          {/* No outer border — the Composer card establishes its own
              visual boundary via its ink frame. Wrapping it in another
              border created a ghost 1px line above the card. */}
          <div className="bg-white">
            <Composer
              value={input}
              onChange={setInput}
              onSubmit={() => send()}
              streaming={streaming}
              placeholder={t('input_placeholder')}
            />
          </div>
        </main>

        {/* Right: tool inspector — shows the SELECTED chip's details.
            Click any chip on the left to switch. commit_card chips render
            the specially-designed metric draft view with accept button. */}
        <Inspector
          selected={selectedMsg}
          acceptedMetricId={acceptedMetricId}
          onRejection={onRejection}
          onAccepted={onAccepted}
        />

        {/* Far right: collapsible thread history drawer */}
        <ThreadDrawer
          open={threadsOpen}
          onToggle={() => setThreadsOpen((v) => !v)}
          activeThreadId={threadId}
          onSelect={handleSelectThread}
        />
      </div>
    </div>
  )
}

// ── Stream rendering ──────────────────────────────────────────────────────

function StreamRow({ msg, industrial, selectedToolKey, onSelectTool }: {
  msg: StreamMsg
  industrial: boolean
  selectedToolKey: string | null
  onSelectTool: (id: string) => void
}) {
  if (msg.kind === 'user') {
    // User on the RIGHT (chat convention): row reversed so the avatar sits
    // on the right edge and the bubble flows from the right.
    return (
      <div className="flex flex-row-reverse items-start gap-3">
        <div className={industrial
          ? 'flex h-7 w-7 flex-shrink-0 items-center justify-center border border-ink bg-ink'
          : 'flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-lg bg-gray-100 ring-1 ring-gray-200'
        }>
          <User className={industrial ? 'h-3.5 w-3.5 text-white' : 'h-3.5 w-3.5 text-gray-600'} />
        </div>
        <div className={industrial
          ? 'min-w-0 max-w-[80%] border border-ink bg-canvas-alt px-4 py-2.5 font-mono text-[13px] leading-relaxed text-ink'
          : 'min-w-0 max-w-[80%] rounded-2xl rounded-tl-md bg-gray-50 px-4 py-2.5 text-[14px] leading-relaxed text-gray-900'
        }>
          {msg.content}
        </div>
      </div>
    )
  }

  if (msg.kind === 'assistant') {
    return (
      <div className="flex items-start gap-3">
        <div className={industrial
          ? 'flex h-7 w-7 flex-shrink-0 items-center justify-center border border-ink bg-white'
          : 'flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-lg bg-violet-50 ring-1 ring-violet-100'
        }>
          <Bot className={industrial ? 'h-3.5 w-3.5 text-ink' : 'h-3.5 w-3.5 text-violet-600'} />
        </div>
        <div className={industrial
          ? 'min-w-0 flex-1 pt-1 text-[13.5px] leading-[1.7] text-ink'
          : 'min-w-0 flex-1 pt-1 text-[14px] leading-[1.65] text-gray-900'
        }>
          <AssistantMarkdown content={msg.content} />
        </div>
      </div>
    )
  }

  if (msg.kind === 'tool_call') {
    return (
      <div className="flex items-start gap-3">
        <div className="w-7 flex-shrink-0" />
        <div className="min-w-0 flex-1">
          <ToolCallChip
            name={msg.name}
            arguments={msg.arguments}
            result={msg.result}
            status={msg.status}
            selected={msg.id === selectedToolKey}
            onClick={() => onSelectTool(msg.id)}
          />
        </div>
      </div>
    )
  }

  if (msg.kind === 'metric_result') {
    return <MetricResultBubble result={msg.result} industrial={industrial} />
  }

  return null
}

function MetricResultBubble({ result, industrial }: { result: MetricResult; industrial: boolean }) {
  const { rows = [], columns = [], totalRows, error, executedSql } = result
  if (error) {
    // Industrial-mode failure: red. The system is honest about the rollback
    // so the user doesn't think a broken draft landed in the registry.
    return (
      <div className="flex items-start gap-3">
        <div className="flex h-7 w-7 flex-shrink-0 items-center justify-center border border-red-600 bg-red-600">
          <span className="font-mono text-[12px] text-white">!</span>
        </div>
        <div className="min-w-0 flex-1 border border-red-600 bg-red-50 px-3 py-2 font-mono text-[11.5px] text-red-700">
          <div className="tracking-[0.1em] font-semibold">SQL 执行失败 · 草稿已回滚 · LLM 将重试</div>
          <div className="mt-1 text-[11px] text-red-700/80">{error}</div>
          {executedSql && (
            <details className="mt-1.5">
              <summary className="cursor-pointer text-[10.5px] text-red-700/80">show SQL</summary>
              <pre className="mt-1 overflow-x-auto border border-red-600 bg-white px-2 py-1 font-mono text-[10.5px] text-red-700">{executedSql}</pre>
            </details>
          )}
        </div>
      </div>
    )
  }
  const previewRows = rows.slice(0, 10)
  if (industrial) {
    return (
      <div className="flex items-start gap-3">
        <div className="flex h-7 w-7 flex-shrink-0 items-center justify-center border border-ink bg-ink">
          <span className="font-mono text-[11px] text-white">✓</span>
        </div>
        <div className="min-w-0 flex-1 border border-ink bg-white">
          <div className="flex items-baseline justify-between border-b border-ink bg-canvas-alt px-3 py-1.5">
            <span className="font-mono text-[10px] tracking-[0.22em] uppercase text-ink">查询结果</span>
            <span className="font-mono text-[10.5px] text-ink-muted">
              {rows.length} 行{typeof totalRows === 'number' && totalRows > rows.length ? ` · 已截断 (共 ${totalRows})` : ''}
            </span>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full border-separate border-spacing-0 text-[12px]">
              <thead>
                <tr>
                  {columns.map((c) => (
                    <th key={c} className="sticky top-0 border-b border-ink bg-canvas-alt px-3 py-1.5 text-left font-mono text-[10.5px] text-ink-muted">
                      {c}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {previewRows.map((row, i) => (
                  <tr key={i}>
                    {columns.map((c) => (
                      <td key={c} className="border-b border-ink px-3 py-1.5 font-mono text-[11.5px] tabular-nums text-ink">
                        {formatCell(row[c])}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {executedSql && (
            <details className="border-t border-ink bg-canvas-alt px-3 py-1.5">
              <summary className="cursor-pointer font-mono text-[10.5px] text-ink-muted">SHOW SQL</summary>
              <pre className="mt-1 overflow-x-auto bg-white px-2 py-1 font-mono text-[10.5px] text-ink">{executedSql}</pre>
            </details>
          )}
        </div>
      </div>
    )
  }
  return (
    <div className="flex items-start gap-3">
      <div className="flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-lg bg-emerald-50 ring-1 ring-emerald-100">
        <span className="text-[12px] text-emerald-600">✓</span>
      </div>
      <div className="min-w-0 flex-1 overflow-hidden rounded-xl border border-emerald-100 bg-emerald-50/30">
        <div className="flex items-baseline justify-between border-b border-emerald-100 bg-emerald-50/50 px-3 py-1.5">
          <span className="text-[11px] font-medium text-emerald-900">查询结果</span>
          <span className="font-mono text-[10.5px] text-emerald-700">
            {rows.length} 行{typeof totalRows === 'number' && totalRows > rows.length ? ` · 已截断(共 ${totalRows})` : ''}
          </span>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full border-separate border-spacing-0 text-[12px]">
            <thead>
              <tr>
                {columns.map((c) => (
                  <th key={c} className="sticky top-0 border-b border-emerald-100 bg-white/80 px-3 py-1.5 text-left font-mono text-[10.5px] font-medium text-gray-500">
                    {c}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {previewRows.map((row, i) => (
                <tr key={i} className={i % 2 === 0 ? 'bg-white/40' : ''}>
                  {columns.map((c) => (
                    <td key={c} className="border-b border-emerald-50 px-3 py-1.5 font-mono text-[11.5px] tabular-nums text-gray-800">
                      {formatCell(row[c])}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {executedSql && (
          <details className="border-t border-emerald-100 bg-white/40 px-3 py-1.5">
            <summary className="cursor-pointer text-[10.5px] text-emerald-700">show SQL</summary>
            <pre className="mt-1 overflow-x-auto rounded bg-white px-2 py-1 font-mono text-[10.5px] text-gray-700">{executedSql}</pre>
          </details>
        )}
      </div>
    </div>
  )
}

function formatCell(v: unknown): string {
  if (v === null || v === undefined) return '—'
  if (typeof v === 'number') {
    // Decimal: show with thousand separators; integers untouched
    if (Number.isInteger(v)) return v.toLocaleString()
    return v.toLocaleString(undefined, { maximumFractionDigits: 2 })
  }
  if (typeof v === 'string') {
    // Numeric strings from pg's NUMERIC type
    const n = Number(v)
    if (!Number.isNaN(n) && v.trim() !== '' && /^-?\d/.test(v)) {
      if (Number.isInteger(n)) return n.toLocaleString()
      return n.toLocaleString(undefined, { maximumFractionDigits: 2 })
    }
    return v
  }
  return String(v)
}

// Industrial-styled markdown renderer for assistant content.
// Inline code monospace + canvas-alt fill; block code uses an ink frame;
// headings + lists keep tight rhythm; tables get ink borders. Limits
// background colors to canvas-alt so it doesn't fight the chat surface.
function AssistantMarkdown({ content }: { content: string }) {
  return (
    <div className="prose prose-sm max-w-none font-mono text-[13.5px] leading-[1.7] text-ink prose-headings:font-mono prose-headings:text-ink prose-strong:text-ink prose-a:text-ink prose-a:underline">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          code: ({ className, children, ...props }) => {
            const inline = !className?.includes('language-')
            if (inline) {
              return (
                <code className="border border-ink bg-canvas-alt px-1 py-[1px] font-mono text-[12.5px] text-ink" {...props}>
                  {children}
                </code>
              )
            }
            return (
              <code className={`${className || ''} block`} {...props}>
                {children}
              </code>
            )
          },
          pre: ({ children, ...props }) => (
            <pre
              className="overflow-x-auto border border-ink bg-canvas-alt px-3 py-2 font-mono text-[12px] leading-relaxed text-ink"
              {...props}
            >
              {children}
            </pre>
          ),
          table: ({ children, ...props }) => (
            <div className="overflow-x-auto border border-ink">
              <table className="w-full border-separate border-spacing-0 text-[12px]" {...props}>
                {children}
              </table>
            </div>
          ),
          th: ({ children, ...props }) => (
            <th className="border-b border-ink bg-canvas-alt px-2 py-1 text-left font-mono text-[10.5px] tracking-[0.06em] uppercase text-ink-muted" {...props}>
              {children}
            </th>
          ),
          td: ({ children, ...props }) => (
            <td className="border-b border-ink px-2 py-1 font-mono text-[11.5px] text-ink" {...props}>
              {children}
            </td>
          ),
          blockquote: ({ children, ...props }) => (
            <blockquote className="border-l-2 border-ink bg-canvas-alt px-3 py-1 font-mono text-[12.5px] text-ink-muted" {...props}>
              {children}
            </blockquote>
          ),
        }}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}

// Inspector — the right-rail tool detail panel.
//
// Replaces the old DraftCanvas state machine (idle/building/ready/accepted)
// with a simple selection-driven view: it renders whatever tool_call chip
// is currently selected in the chat stream. For commit_card chips it
// renders the specially-designed CommitCard editor (with accept button);
// for everything else it renders an expanded INPUT + OUTPUT view (the
// same data the chip shows compactly in the chat, just bigger).
//
// No internal status logic — the chip's status drives what's shown.
const TOOL_VERB: Record<string, { icon: React.ComponentType<{ className?: string }>; label: string }> = {
  lookup: { icon: Search, label: '探查 OD' },
  lookup_od: { icon: Search, label: '探查 OD' },
  inspect: { icon: Search, label: '探查数据' },
  smartquery: { icon: Database, label: '执行查询' },
  execute_smartquery: { icon: Database, label: '执行查询' },
  commit_card: { icon: Sparkles, label: '口径草稿' },
}

function Inspector({
  selected, acceptedMetricId, onRejection, onAccepted,
}: {
  selected: Extract<StreamMsg, { kind: 'tool_call' }> | null
  acceptedMetricId: string | null
  onRejection: (r: ValidatorRejection) => void
  onAccepted: (metricId: string) => void
}) {
  // No tool selected at all — show an idle hint.
  if (!selected) {
    return (
      <aside className="flex h-full w-[360px] flex-shrink-0 flex-col overflow-hidden border-l-2 border-ink bg-canvas-alt">
        <div className="flex h-12 flex-shrink-0 items-center gap-2 border-b-2 border-ink bg-white px-4">
          <Sparkles className="h-3.5 w-3.5 text-ink" />
          <span className="font-mono text-[11px] tracking-[0.22em] uppercase text-ink">详情面板</span>
        </div>
        <div className="flex-1 overflow-y-auto px-4 py-10">
          <div className="flex flex-col items-center gap-3 text-center">
            <div className="flex h-12 w-12 items-center justify-center border border-ink bg-white">
              <Sparkles className="h-5 w-5 text-ink-muted" strokeWidth={1.5} />
            </div>
            <div className="font-mono text-[12px] tracking-[0.18em] uppercase text-ink">空</div>
            <p className="max-w-[220px] font-mono text-[10.5px] leading-relaxed text-ink-muted">
              点击聊天里任意工具气泡 — 这一栏会显示该工具的详细输入与输出
            </p>
          </div>
        </div>
      </aside>
    )
  }

  const isCommit = selected.name === 'commit_card' && !!selected.commitPayload
  const verb = TOOL_VERB[selected.name] || { icon: Database, label: selected.name }
  const Icon = verb.icon

  return (
    <aside className="flex h-full w-[360px] flex-shrink-0 flex-col overflow-hidden border-l-2 border-ink bg-canvas-alt">
      {/* Header — shows what's currently being inspected */}
      <div className="flex h-12 flex-shrink-0 items-center gap-2 border-b-2 border-ink bg-white px-4">
        <Icon className="h-3.5 w-3.5 text-ink" />
        <span className="font-mono text-[11px] tracking-[0.22em] uppercase text-ink">
          {verb.label}
        </span>
        <span className="ml-auto font-mono text-[10px] tracking-[0.06em] text-ink-muted">
          {selected.status === 'running' ? '运行中' : selected.status === 'error' ? '失败' : '已完成'}
        </span>
      </div>

      {/* Body */}
      <div className="flex-1 overflow-y-auto px-4 py-4">
        {isCommit && acceptedMetricId && selected.commitPayload ? (
          <AcceptedMetricView metricId={acceptedMetricId} payload={selected.commitPayload} />
        ) : isCommit && selected.commitPayload ? (
          <div className="space-y-4">
            <TestRunPanel querySql={selected.commitPayload.querySql} />
            <CommitCard
              payload={selected.commitPayload}
              onRejection={onRejection}
              onAccepted={onAccepted}
            />
          </div>
        ) : (
          <ToolDetailView selected={selected} />
        )}
      </div>
    </aside>
  )
}

// TestRunPanel — "测试运行" affordance for the metric draft.
// Calls the editor preview endpoint (POST /ontology/lakehouse-metric-preview
// with level=sql) which is the same path the metric editor's Run button uses.
// Shows columns + first 10 rows on success, red error otherwise.
function TestRunPanel({ querySql }: { querySql: string }) {
  const { currentProject } = useProject()
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<{ rows?: Record<string, unknown>[]; columns?: string[]; error?: string; sql?: string } | null>(null)

  const onTest = async () => {
    if (!currentProject?.id || !querySql) return
    setRunning(true)
    setResult(null)
    try {
      // The api() helper already stringifies — pass the plain object.
      // Pre-stringifying produced a double-encoded string body and the
      // server returned "invalid JSON body".
      const r = await api<{ ok: boolean; rows?: Record<string, unknown>[]; columns?: string[]; sql?: string; error?: string }>(
        '/ontology/lakehouse-metric-preview',
        {
          method: 'POST',
          body: {
            projectId: currentProject.id,
            level: 'sql',
            querySql,
            sampleParams: {},
          },
        },
      )
      if (!r.ok) {
        setResult({ error: r.error || 'unknown error', sql: r.sql })
      } else {
        const rows = (r.rows || []) as Record<string, unknown>[]
        const cols = (r.columns && r.columns.length > 0)
          ? r.columns
          : (rows[0] ? Object.keys(rows[0]) : [])
        setResult({ rows, columns: cols, sql: r.sql })
      }
    } catch (e) {
      setResult({ error: (e as Error).message })
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="border border-ink bg-white">
      <div className="flex items-center justify-between border-b border-ink bg-canvas-alt px-3 py-1.5">
        <span className="font-mono text-[10px] tracking-[0.22em] uppercase text-ink">测试运行</span>
        <button
          type="button"
          onClick={onTest}
          disabled={running}
          className={`inline-flex items-center gap-1.5 border border-ink px-2.5 py-1 font-mono text-[10.5px] tracking-[0.12em] uppercase transition-colors ${
            running ? 'bg-canvas-alt text-ink-muted' : 'bg-ink text-white hover:bg-white hover:text-ink'
          }`}
        >
          {running ? '运行中…' : '执行 SQL'}
        </button>
      </div>
      {result && (
        <div className="px-3 py-2">
          {result.error ? (
            <div className="border border-red-600 bg-red-50 px-2 py-1.5 font-mono text-[11px] text-red-700">
              <div className="font-semibold tracking-[0.06em]">SQL 失败</div>
              <div className="mt-1 break-all">{result.error}</div>
            </div>
          ) : (result.rows && result.rows.length > 0) ? (
            <div className="space-y-1.5">
              <div className="font-mono text-[10px] tracking-[0.14em] uppercase text-ink-muted">
                {result.rows.length} 行 · 预览前 {Math.min(result.rows.length, 10)}
              </div>
              <div className="overflow-x-auto border border-ink">
                <table className="w-full border-separate border-spacing-0 text-[11px]">
                  <thead>
                    <tr>
                      {(result.columns || []).map((c) => (
                        <th key={c} className="border-b border-ink bg-canvas-alt px-2 py-1 text-left font-mono text-[10px] text-ink-muted">{c}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {result.rows.slice(0, 10).map((row, i) => (
                      <tr key={i}>
                        {(result.columns || []).map((c) => (
                          <td key={c} className="border-b border-ink px-2 py-1 font-mono text-[10.5px] tabular-nums text-ink">
                            {formatCell(row[c])}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          ) : (
            <div className="font-mono text-[11px] text-ink-muted">0 行</div>
          )}
        </div>
      )}
    </div>
  )
}

function AcceptedMetricView({ metricId, payload }: { metricId: string; payload: CommitCardPayload }) {
  return (
    <div className="flex flex-col items-center gap-3 px-2 py-8 text-center">
      <div className="flex h-12 w-12 items-center justify-center border border-ink bg-ink">
        <Check className="h-6 w-6 text-white" strokeWidth={2} />
      </div>
      <div>
        <div className="font-mono text-[12px] tracking-[0.18em] uppercase text-ink">已采纳为 METRIC</div>
        <div className="mt-1 font-mono text-[11px] text-ink-muted">{payload.name}</div>
      </div>
      <p className="max-w-[240px] font-mono text-[10.5px] leading-relaxed text-ink-muted">
        查询模式可直接召回这条口径
      </p>
      <a
        href={`./lakehouse-metrics/edit?id=${encodeURIComponent(metricId)}`}
        className="inline-flex items-center gap-1 border border-ink bg-white px-2.5 py-1 font-mono text-[10.5px] tracking-[0.08em] uppercase text-ink hover:bg-ink hover:text-white"
      >
        编辑此 METRIC
      </a>
    </div>
  )
}

function ToolDetailView({ selected }: { selected: Extract<StreamMsg, { kind: 'tool_call' }> }) {
  const argsText = safeJson(selected.arguments)
  const resultText = safeJson(selected.result)
  return (
    <div className="space-y-4">
      <DetailSection label="INPUT (arguments)">
        <pre className="overflow-x-auto border border-ink bg-white px-3 py-2 font-mono text-[11px] leading-[1.55] text-ink">
          {argsText}
        </pre>
      </DetailSection>
      {selected.status === 'running' ? (
        <div className="border border-ink bg-white px-3 py-2 font-mono text-[11px] text-ink-muted">
          等待返回…
        </div>
      ) : (
        <DetailSection label="OUTPUT (result)">
          <pre className="overflow-x-auto border border-ink bg-white px-3 py-2 font-mono text-[11px] leading-[1.55] text-ink">
            {resultText}
          </pre>
        </DetailSection>
      )}
    </div>
  )
}

function DetailSection({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1.5 font-mono text-[10px] tracking-[0.22em] uppercase text-ink-muted">{label}</div>
      {children}
    </div>
  )
}

function safeJson(v: unknown): string {
  if (v === undefined) return '(无)'
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

function ThreadDrawer({
  open, onToggle, activeThreadId, onSelect,
}: {
  open: boolean
  onToggle: () => void
  activeThreadId: string | null
  onSelect: (id: string | null) => void
}) {
  // Header-button-driven now (parity with query/builder). Collapsed = hidden,
  // not an always-visible vertical rail. The 历史记录 button in the header
  // toggles `open`; when closed we render nothing so the workspace reclaims
  // the full width.
  if (!open) return null
  return (
    <div className="flex h-full w-[260px] flex-shrink-0 flex-col">
      <ThreadList
        activeThreadId={activeThreadId}
        onSelect={onSelect}
        onClose={onToggle}
      />
    </div>
  )
}

function ThinkingDots({ industrial }: { industrial: boolean }) {
  return (
    <div className="flex items-start gap-3">
      <div className={industrial
        ? 'flex h-7 w-7 flex-shrink-0 items-center justify-center border border-ink bg-ink'
        : 'flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-lg bg-violet-50 ring-1 ring-violet-100'
      }>
        <Bot className={industrial ? 'h-3.5 w-3.5 text-white' : 'h-3.5 w-3.5 text-violet-600'} />
      </div>
      <div className="flex h-7 items-center gap-1 pt-1">
        <span className={industrial
          ? 'h-1.5 w-1.5 animate-bounce bg-ink [animation-delay:0ms]'
          : 'h-1.5 w-1.5 animate-bounce rounded-full bg-violet-400 [animation-delay:0ms]'
        } />
        <span className={industrial
          ? 'h-1.5 w-1.5 animate-bounce bg-ink [animation-delay:120ms]'
          : 'h-1.5 w-1.5 animate-bounce rounded-full bg-violet-400 [animation-delay:120ms]'
        } />
        <span className={industrial
          ? 'h-1.5 w-1.5 animate-bounce bg-ink [animation-delay:240ms]'
          : 'h-1.5 w-1.5 animate-bounce rounded-full bg-violet-400 [animation-delay:240ms]'
        } />
      </div>
    </div>
  )
}
