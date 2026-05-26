'use client'

// Shared presentational bits for the dataset-testing pages.
// Extracted from the pre-refactor monolithic page.tsx so both the list page
// (not used there — kept here for completeness) and the detail page can import
// the same components without a circular dep.

import { useState } from 'react'
import { BookOpen, ChevronDown, ChevronRight, Loader2, Play } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { ResultViewer } from '@/components/ui/ResultViewer'

export type FunctionCall = {
  name: string
  arguments: Record<string, unknown>
  result: Record<string, unknown>
}

// Tag attached to a question (ont_test_case). Suite-scoped dictionary.
export interface Tag {
  id: string
  name: string
  count?: number // usage count, only present on the /tags list endpoint
}

export interface RunCase {
  id: string
  caseId: string
  sortOrder: number
  code: string
  userQuestion: string
  status: string
  finalAnswer: string
  generatedSql: string
  executionStatus: string
  executionResult: string
  executionError: string
  durationMs: number
  modelName: string
  promptTokens: number
  completionTokens: number
  totalTokens: number
  functionCalls?: FunctionCall[]
  mark?: string
  note?: string
  tags?: Tag[]
}

export const TOOL_META: Record<string, { label: string; color: string }> = {
  lookup:     { label: '探索知识', color: 'border-blue-300 bg-blue-50 text-blue-700' },
  smartquery: { label: '执行查询', color: 'border-accent/30 bg-accent/5 text-accent' },
}

export function MdContent({ content }: { content: string }) {
  return (
    <div className="font-mono text-xs text-ink-muted">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={{
        p: ({ children }) => <p className="mb-1 text-xs leading-relaxed">{children}</p>,
        h1: ({ children }) => <h1 className="text-xs font-bold mb-1 border-b border-border pb-0.5">{children}</h1>,
        h2: ({ children }) => <h2 className="text-xs font-bold mb-0.5">{children}</h2>,
        h3: ({ children }) => <h3 className="text-[10px] font-semibold text-ink-ghost mb-0.5">{children}</h3>,
        ul: ({ children }) => <ul className="ml-3 list-disc text-xs mb-1">{children}</ul>,
        li: ({ children }) => <li className="mb-0.5">{children}</li>,
        code: ({ children }) => <code className="bg-canvas-alt px-1 font-mono text-[10px] text-accent">{children}</code>,
        pre: ({ children }) => <pre className="bg-canvas-alt px-2 py-1 text-[10px] overflow-x-auto">{children}</pre>,
        strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
        hr: () => <hr className="my-1 border-border" />,
      }}>{content}</ReactMarkdown>
    </div>
  )
}

export function StatusBadge({ status }: { status: string }) {
  const cls = {
    idle: 'border-border text-ink-ghost',
    pending: 'border-border text-ink-ghost',
    queued: 'border-amber-400 bg-amber-50 text-amber-700',
    running: 'border-accent bg-accent/10 text-accent',
    completed: 'border-green-400 bg-green-50 text-green-700',
    error: 'border-red-400 bg-red-50 text-red-700',
    cancelled: 'border-zinc-400 bg-zinc-50 text-zinc-600',
  }[status] || 'border-border text-ink-ghost'

  return (
    <span className={`border px-1.5 py-0.5 font-mono text-[8px] font-bold tracking-wider ${cls}`}>
      {status === 'idle' ? 'IDLE'
        : status === 'pending' ? 'PENDING'
        : status === 'queued' ? 'QUEUED'
        : status === 'running' ? 'RUNNING'
        : status === 'completed' ? 'DONE'
        : status === 'cancelled' ? '已中止'
        : status.toUpperCase()}
    </span>
  )
}

export function CaseStatusBadge({ status, execStatus }: { status: string; execStatus: string }) {
  if (status === 'running') {
    return (
      <span className="flex items-center gap-1 font-mono text-[9px] text-accent">
        <Loader2 size={10} className="animate-spin" /> RUNNING
      </span>
    )
  }
  if (status === 'completed' && execStatus === 'success') {
    return <span className="font-mono text-[9px] text-green-600 font-bold">SUCCESS</span>
  }
  if (status === 'completed' && execStatus === 'error') {
    return <span className="font-mono text-[9px] text-red-600 font-bold">ERROR</span>
  }
  if (status === 'error') {
    return <span className="font-mono text-[9px] text-red-600 font-bold">ERROR</span>
  }
  if (status === 'cancelled') {
    return <span className="font-mono text-[9px] text-zinc-500 font-bold">已中止</span>
  }
  return <span className="font-mono text-[9px] text-ink-ghost">PENDING</span>
}

export function MarkIndicator({ mark, pendingJudge }: { mark?: string; pendingJudge?: boolean }) {
  if (mark === 'correct') {
    return <span className="font-mono text-[10px] text-green-600" title="已标注：正确">✓</span>
  }
  if (mark === 'incorrect') {
    return <span className="font-mono text-[10px] text-red-600" title="已标注：错误">✗</span>
  }
  if (pendingJudge) {
    return <span className="font-mono text-[10px] text-amber-600" title="有 finalAnswer 但尚未判定">⏳</span>
  }
  return null
}

export function FunctionCallRound({ fc, defaultOpen }: { fc: FunctionCall; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen)
  const meta = TOOL_META[fc.name] || { label: fc.name, color: 'border-border bg-canvas-alt text-ink-ghost' }
  const result = fc.result || {}

  return (
    <div className="border border-border">
      <button
        onClick={() => setOpen(!open)}
        className={`flex items-center gap-2 w-full px-3 py-1.5 text-left hover:bg-canvas-alt ${meta.color} border-b border-border`}
      >
        {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        {fc.name === 'lookup' && <BookOpen className="h-3 w-3" />}
        {fc.name === 'smartquery' && <Play className="h-3 w-3" />}
        <span className="font-mono text-[10px] font-bold">{meta.label}</span>
        {fc.name === 'smartquery' && Array.isArray(fc.arguments.objects) && (
          <span className="font-mono text-[9px] text-ink-ghost ml-1">
            [{(fc.arguments.objects as string[]).join(', ')}]
          </span>
        )}
      </button>
      {open && (
        <div className="px-3 py-2 space-y-2">
          {fc.name === 'lookup' && (
            <div className="space-y-1">
              {((fc.arguments.ontology_name as string[]) || []).length > 0 && (
                <div className="flex items-center gap-1.5 flex-wrap">
                  <span className="font-mono text-[10px] text-ink-ghost">本体:</span>
                  {((fc.arguments.ontology_name as string[]) || []).map((n, i) => (
                    <span key={i} className="bg-ink text-white px-1.5 py-0.5 font-mono text-[10px] font-bold">{n}</span>
                  ))}
                </div>
              )}
              {((fc.arguments.keyword as string[]) || []).length > 0 && (
                <div className="flex items-center gap-1.5 flex-wrap">
                  <span className="font-mono text-[10px] text-ink-ghost">关键词:</span>
                  {((fc.arguments.keyword as string[]) || []).map((k, i) => (
                    <span key={i} className="border border-accent/50 text-accent px-1.5 py-0.5 font-mono text-[10px]">{k}</span>
                  ))}
                </div>
              )}
            </div>
          )}
          {fc.name === 'smartquery' && (
            <div className="space-y-1">
              <div className="flex items-center gap-1.5 flex-wrap">
                <span className="font-mono text-[10px] text-ink-ghost">对象:</span>
                {((fc.arguments.objects as string[]) || []).map((o, i) => (
                  <span key={i} className="bg-ink text-white px-1.5 py-0.5 font-mono text-[10px] font-bold">{o}</span>
                ))}
                {!!fc.arguments.metric && (
                  <>
                    <span className="font-mono text-[10px] text-ink-ghost ml-1">口径:</span>
                    <span className="border border-accent text-accent px-1.5 py-0.5 font-mono text-[10px]">
                      {String(fc.arguments.metric)}
                    </span>
                  </>
                )}
              </div>
              {((fc.arguments.groupBy as string[]) || []).length > 0 && (
                <div className="flex items-center gap-1.5 flex-wrap">
                  <span className="font-mono text-[10px] text-ink-ghost">分组:</span>
                  {((fc.arguments.groupBy as string[]) || []).map((g, i) => (
                    <span key={i} className="border border-border px-1.5 py-0.5 font-mono text-[10px] text-ink-muted">{g}</span>
                  ))}
                </div>
              )}
            </div>
          )}
          {!['lookup', 'smartquery'].includes(fc.name) && Object.keys(fc.arguments).length > 0 && (
            <pre className="bg-canvas-alt border border-border px-2 py-1 font-mono text-[10px] whitespace-pre-wrap max-h-24 overflow-y-auto">
              {JSON.stringify(fc.arguments, null, 2)}
            </pre>
          )}

          {result.error != null && result.error !== '' && (
            <div className="border border-red-300 bg-red-50 px-2 py-1 font-mono text-[10px] text-red-700">
              {String(result.error)}
            </div>
          )}
          {result.content != null && result.content !== '' && (
            <div className="border border-border bg-white px-2 py-1.5 max-h-56 overflow-y-auto">
              <MdContent content={String(result.content)} />
            </div>
          )}
          {result.ontology_sql != null && result.ontology_sql !== '' && (
            <div>
              <div className="font-mono text-[8px] text-emerald-700 mb-0.5 font-bold tracking-wider">▼ ONTOLOGY SQL（语义层 · Od/Property 名称）</div>
              <pre className="bg-[#1e1e1e] px-2 py-1 font-mono text-[9px] text-emerald-400 whitespace-pre-wrap max-h-40 overflow-y-auto border border-emerald-900/50">
                {String(result.ontology_sql)}
              </pre>
            </div>
          )}
          {result.generated_sql != null && result.generated_sql !== '' && (
            <details className="mt-1">
              <summary className="font-mono text-[9px] text-ink-ghost cursor-pointer hover:text-ink select-none">
                ▶ 湖仓 SQL（物理层 · PostgreSQL · {String(result.generated_sql).split('\n').length} 行，点击展开）
              </summary>
              <pre className="mt-1 bg-[#1e1e1e] px-2 py-1 font-mono text-[9px] text-sky-400 whitespace-pre-wrap max-h-40 overflow-y-auto border border-sky-900/50">
                {String(result.generated_sql)}
              </pre>
            </details>
          )}
          {result.execution_status === 'success' && result.execution_result != null && (
            <ResultViewer
              data={String(result.execution_result)}
              initialMode={(result.display_mode as 'table' | 'bar' | 'pie' | 'line') || 'table'}
            />
          )}
          {result.execution_status === 'error' && result.execution_error != null && (
            <div className="border border-red-300 bg-red-50 px-2 py-1 font-mono text-[10px] text-red-700">
              {String(result.execution_error)}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function CaseDetailBody({ tc }: { tc: RunCase }) {
  const hasRounds = !!(tc.functionCalls && tc.functionCalls.length > 0)
  return (
    <div className="space-y-3">
      {hasRounds && (
        <div className="space-y-2">
          <div className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">
            ROUNDS ({tc.functionCalls!.length})
          </div>
          {tc.functionCalls!.map((fc, i) => (
            // Default-collapsed: previously the last round auto-expanded, but
            // when there are 5+ rounds (or the panel is one of N in compare
            // mode) the auto-expand drowns the page in raw tool output.
            // User explicitly requested collapse-by-default — they can click
            // any round header to expand.
            <FunctionCallRound key={i} fc={fc} defaultOpen={false} />
          ))}
        </div>
      )}
      {!hasRounds && tc.generatedSql && (
        <div>
          <div className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider mb-1">GENERATED SQL</div>
          <pre className="bg-[#1e1e1e] px-3 py-2 font-mono text-[10px] text-emerald-400 whitespace-pre-wrap max-h-56 overflow-y-auto">
            {tc.generatedSql}
          </pre>
        </div>
      )}
      {!hasRounds && tc.executionStatus === 'success' && tc.executionResult && (
        <ResultViewer data={tc.executionResult} initialMode="table" />
      )}
      {!hasRounds && tc.executionError && (
        <div>
          <div className="font-mono text-[9px] text-red-600 font-bold tracking-wider mb-1">ERROR</div>
          <pre className="bg-red-50 border border-red-200 px-3 py-2 font-mono text-[10px] text-red-700 whitespace-pre-wrap">
            {tc.executionError}
          </pre>
        </div>
      )}
      {tc.finalAnswer && (
        <div>
          <div className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider mb-1">FINAL ANSWER</div>
          <div className="border border-border bg-white px-3 py-2 max-h-96 overflow-y-auto">
            <MdContent content={tc.finalAnswer} />
          </div>
        </div>
      )}
      <div className="flex items-center gap-4 font-mono text-[9px] text-ink-ghost border-t border-border pt-2">
        {tc.durationMs > 0 && <span>耗时: {(tc.durationMs / 1000).toFixed(1)}s</span>}
        {tc.totalTokens > 0 && <span>Tokens: {tc.totalTokens}</span>}
        {tc.modelName && <span>Model: {tc.modelName}</span>}
      </div>
    </div>
  )
}
