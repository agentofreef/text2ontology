'use client'

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { useMessage } from '@/lib/message'
import { useProject } from '@/lib/project'
import { useStyleMode } from '@/lib/style-mode'
import { api } from '@/lib/api'
import { Play, RotateCcw, Cpu, Keyboard, Telescope } from 'lucide-react'
import {
  RecallResultView,
  type RecallResult,
  type VectorCandidate,
  type TokenizeDebug,
} from '@/components/lakehouse-agent/RecallDiagnostics'

// ─── Page ────────────────────────────────────────────────────

export default function LakehouseTokenRecallPage() {
  const t = useTranslations('agent.token_recall')
  const industrial = useStyleMode().mode === 'industrial'
  const { currentProject } = useProject()
  const msg = useMessage()
  const reduce = useReducedMotion()
  const [tab, setTab] = useState<'tokens' | 'question'>('question')

  const [tokenInput, setTokenInput] = useState('')
  const [tokenLoading, setTokenLoading] = useState(false)
  const [tokenResult, setTokenResult] = useState<RecallResult | null>(null)
  const [tokenVectorCandidates, setTokenVectorCandidates] = useState<Record<string, VectorCandidate[]>>({})

  const [question, setQuestion] = useState('')
  const [questionLoading, setQuestionLoading] = useState(false)
  const [questionTokens, setQuestionTokens] = useState<string[]>([])
  const [questionResult, setQuestionResult] = useState<RecallResult | null>(null)
  const [tokenizeDebug, setTokenizeDebug] = useState<TokenizeDebug | null>(null)
  const [vectorCandidates, setVectorCandidates] = useState<Record<string, VectorCandidate[]>>({})

  const handleTokenRecall = async () => {
    const tokenList = tokenInput.split(/[\n,|;]/).map(t => t.trim()).filter(Boolean)
    if (tokenList.length === 0) { msg.error(t('recall_error_no_token')); return }
    setTokenLoading(true)
    try {
      const res = await api<{
        recall: RecallResult
        vectorCandidates?: Record<string, VectorCandidate[]>
      }>(`/ontology/lakehouse-token-recall-debug?projectId=${currentProject?.id}`, {
        method: 'POST',
        body: { tokens: tokenList },
      })
      setTokenResult(res.recall)
      setTokenVectorCandidates(res.vectorCandidates || {})
      msg.success(t('recall_success', { status: res.recall.hasMatches ? t('recall_hit') : t('recall_miss') }))
    } catch { msg.error(t('recall_fail')) }
    finally { setTokenLoading(false) }
  }

  const handleQuestionRecall = async () => {
    if (!question.trim()) { msg.error(t('recall_error_no_question')); return }
    setQuestionLoading(true)
    try {
      const res = await api<{
        question: string
        tokens: string[]
        recall: RecallResult
        tokenizeDebug: TokenizeDebug
        vectorCandidates?: Record<string, VectorCandidate[]>
      }>(
        `/ontology/lakehouse-token-recall-tokenize?projectId=${currentProject?.id}`,
        { method: 'POST', body: { question: question.trim() } },
      )
      setQuestionTokens(res.tokens || [])
      setQuestionResult(res.recall)
      setTokenizeDebug(res.tokenizeDebug || null)
      setVectorCandidates(res.vectorCandidates || {})
      msg.success(t('tokenize_success', { count: res.tokens?.length || 0, status: res.recall?.hasMatches ? t('recall_hit') : t('recall_miss') }))
    } catch { msg.error(t('question_fail')) }
    finally { setQuestionLoading(false) }
  }

  const handleResetQuestion = () => {
    setQuestion(''); setQuestionResult(null); setQuestionTokens([])
    setTokenizeDebug(null); setVectorCandidates({})
  }
  const handleResetTokens = () => { setTokenInput(''); setTokenResult(null) }

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header strip */}
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
              // TOKEN RECALL
            </span>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <Telescope size={14} className="text-ink" aria-hidden="true" />
              </div>
              <div className="min-w-0">
                <h1 className="text-base font-semibold tracking-tight text-ink">{t('page_title')}</h1>
                <p className="truncate text-xs text-ink-muted">
                  {t('page_desc')}
                </p>
              </div>
            </>
          )}
        </div>
      </motion.header>

      {/* Tab strip */}
      <nav role="tablist" aria-label={t('tab_aria')} className={`flex flex-shrink-0 items-center gap-0 bg-white px-6 ${industrial ? 'border-b border-ink' : 'border-b border-border'}`}>
        {([
          ['question', t('tab_question'), Cpu],
          ['tokens', t('tab_tokens'), Keyboard],
        ] as const).map(([key, label, Icon]) => {
          const active = tab === key
          return (
            <motion.button
              key={key}
              role="tab"
              aria-selected={active}
              onClick={() => setTab(key)}
              whileHover={reduce ? undefined : { y: -1 }}
              whileTap={reduce ? undefined : { scale: 0.98 }}
              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
              className={`-mb-px flex h-10 items-center gap-1.5 border-b-2 px-4 outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                industrial ? 'font-mono text-[11px] uppercase tracking-[0.14em]' : 'text-sm'
              } ${
                active
                  ? 'border-ink font-semibold text-ink'
                  : 'border-transparent text-ink-muted hover:text-ink'
              }`}
            >
              <Icon size={14} aria-hidden="true" />
              {label}
            </motion.button>
          )
        })}
      </nav>

      {/* Scrollable content */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <AnimatePresence mode="wait">
          {/* Tab: 分词 + 召回 */}
          {tab === 'question' && (
            <motion.div
              key="question"
              initial={reduce ? undefined : { opacity: 0, y: 4 }}
              animate={reduce ? undefined : { opacity: 1, y: 0 }}
              exit={reduce ? undefined : { opacity: 0 }}
              transition={{ duration: 0.15 }}
              className="space-y-4 p-6"
            >
              <div className="overflow-hidden rounded-md border border-border bg-white">
                <div className="flex items-center justify-between gap-2 border-b border-border-light bg-canvas-alt px-4 py-2">
                  <span className="text-xs font-medium text-ink">{t('question_input_label')}</span>
                  <div className="flex items-center gap-2">
                    <motion.button
                      onClick={handleResetQuestion}
                      whileHover={reduce ? undefined : { scale: 1.05 }}
                      whileTap={reduce ? undefined : { scale: 0.95 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      aria-label={t('question_clear_aria')}
                      className="inline-flex items-center gap-1 rounded-md border border-transparent px-1.5 py-0.5 text-[11px] text-ink-ghost outline-none hover:border-border hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      <RotateCcw size={11} aria-hidden="true" />
                      {t('question_clear_btn')}
                    </motion.button>
                    <AnimatedButton
                      variant="primary"
                      size="sm"
                      onClick={handleQuestionRecall}
                      disabled={questionLoading || !question.trim()}
                    >
                      <Cpu size={12} aria-hidden="true" />
                      {questionLoading ? t('question_submit_loading') : t('question_submit_btn')}
                    </AnimatedButton>
                  </div>
                </div>
                <textarea
                  className="w-full bg-white p-4 font-mono text-sm text-ink outline-none placeholder:text-ink-ghost focus:bg-canvas-alt/50"
                  rows={3}
                  value={question}
                  onChange={e => setQuestion(e.target.value)}
                  placeholder={t('question_placeholder')}
                  spellCheck={false}
                  aria-label={t('question_aria')}
                />
              </div>

              {tokenizeDebug && (
                <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-canvas-alt px-4 py-2">
                  <span className="text-xs font-medium text-ink-muted">{t('tokenize_debug_label')}</span>
                  <span className="rounded border border-border bg-white px-1.5 py-0.5 text-[11px] font-mono text-ink">
                    path: {tokenizeDebug.path}
                  </span>
                  <span className="text-[11px] text-ink-muted">{tokenizeDebug.reason}</span>
                </div>
              )}

              {questionResult && (
                <RecallResultView
                  result={questionResult}
                  tokens={questionTokens}
                  vectorCandidates={vectorCandidates}
                />
              )}
            </motion.div>
          )}

          {/* Tab: Manual Token */}
          {tab === 'tokens' && (
            <motion.div
              key="tokens"
              initial={reduce ? undefined : { opacity: 0, y: 4 }}
              animate={reduce ? undefined : { opacity: 1, y: 0 }}
              exit={reduce ? undefined : { opacity: 0 }}
              transition={{ duration: 0.15 }}
              className="space-y-4 p-6"
            >
              <div className="overflow-hidden rounded-md border border-border bg-white">
                <div className="flex items-center justify-between gap-2 border-b border-border-light bg-canvas-alt px-4 py-2">
                  <span className="text-xs font-medium text-ink">{t('token_input_label')}</span>
                  <span className="text-[11px] text-ink-ghost">{t('token_input_hint')}</span>
                  <div className="ml-auto flex items-center gap-2">
                    <motion.button
                      onClick={handleResetTokens}
                      whileHover={reduce ? undefined : { scale: 1.05 }}
                      whileTap={reduce ? undefined : { scale: 0.95 }}
                      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                      aria-label={t('token_clear_aria')}
                      className="inline-flex items-center gap-1 rounded-md border border-transparent px-1.5 py-0.5 text-[11px] text-ink-ghost outline-none hover:border-border hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                    >
                      <RotateCcw size={11} aria-hidden="true" />
                      {t('token_clear_btn')}
                    </motion.button>
                    <AnimatedButton
                      variant="primary"
                      size="sm"
                      onClick={handleTokenRecall}
                      disabled={tokenLoading || !tokenInput.trim()}
                    >
                      <Play size={12} aria-hidden="true" />
                      {tokenLoading ? t('token_submit_loading') : t('token_submit_btn')}
                    </AnimatedButton>
                  </div>
                </div>
                <textarea
                  className="w-full bg-white p-4 font-mono text-sm text-ink outline-none placeholder:text-ink-ghost focus:bg-canvas-alt/50"
                  rows={5}
                  value={tokenInput}
                  onChange={e => setTokenInput(e.target.value)}
                  placeholder={t('token_input_placeholder_example')}
                  spellCheck={false}
                  aria-label={t('token_aria')}
                />
              </div>

              {tokenResult && (
                <RecallResultView
                  result={tokenResult}
                  vectorCandidates={tokenVectorCandidates}
                />
              )}
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  )
}
