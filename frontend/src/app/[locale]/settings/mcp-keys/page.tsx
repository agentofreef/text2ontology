'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback } from 'react'
import { motion, AnimatePresence, useReducedMotion } from 'motion/react'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useStyleMode } from '@/lib/style-mode'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import {
  KeyRound, Plus, Trash2, Copy, X, AlertTriangle, CheckCircle2,
  Terminal, BookOpen, ChevronDown, ChevronRight, ShieldCheck, Clock,
} from 'lucide-react'
import { useAutoAnimate } from '@/lib/motion'

// ─── Types ──────────────────────────────────────────────────────────

interface MCPKey {
  id: string
  label: string
  allowedTools: string[] | null
  createdAt: string
  lastUsedAt: string | null
}

interface CreateResponse {
  id: string
  label: string
  rawKey: string
  allowedTools: string[] | null
}

// ─── Tool catalog ──────────────────────────────────────────────────
// Mirrors services/mcp-tools-server/tools/tools.go. Keep in sync when
// new MCP tools are added.

// TOOL_CATALOG is built inside the component with t() — see MCPKeysPage

// ─── Helpers ────────────────────────────────────────────────────────

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

function formatDate(s: string | null): string {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN', {
    year: '2-digit', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  })
}

// ─── Page ───────────────────────────────────────────────────────────

export default function MCPKeysPage() {
  const t = useTranslations('settings.mcp')
  const industrial = useStyleMode().mode === 'industrial'
  const msg = useMessage()
  const reduce = useReducedMotion()

  const TOOL_CATALOG: Array<{ name: string; label: string; hint: string }> = [
    { name: 'lookup_od',          label: t('tool_lookup_label'),   hint: t('tool_lookup_hint') },
    { name: 'execute_smartquery', label: t('tool_smartquery_label'), hint: t('tool_smartquery_hint') },
    { name: 'recall_tokens',      label: t('tool_recall_label'),   hint: t('tool_recall_hint') },
  ]

  const [keys, setKeys] = useState<MCPKey[]>([])
  const [loading, setLoading] = useState(true)

  const [createOpen, setCreateOpen] = useState(false)
  const [newlyMinted, setNewlyMinted] = useState<CreateResponse | null>(null)
  const [createLabel, setCreateLabel] = useState('')
  const [createRestrictTools, setCreateRestrictTools] = useState(false)
  const [createAllowed, setCreateAllowed] = useState<Set<string>>(new Set(['lookup_od', 'execute_smartquery', 'recall_tokens']))
  const [creating, setCreating] = useState(false)

  const [origin, setOrigin] = useState<string>('http://localhost:10020')
  const [docsOpen, setDocsOpen] = useState(true)

  const listRef = useAutoAnimate<HTMLDivElement>()

  useEffect(() => {
    if (typeof window !== 'undefined') setOrigin(window.location.origin)
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api<{ data: MCPKey[] }>('/ontology/mcp-keys')
      setKeys(res.data || [])
    } catch { setKeys([]) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => { load() }, [load])

  const handleCreate = async () => {
    if (!createLabel.trim()) { msg.error(t('label_required')); return }
    if (createRestrictTools && createAllowed.size === 0) {
      msg.error(t('tool_required')); return
    }
    setCreating(true)
    try {
      const body: Record<string, unknown> = { label: createLabel.trim() }
      if (createRestrictTools) body.allowedTools = Array.from(createAllowed)
      const res = await api<CreateResponse>('/ontology/mcp-keys', {
        method: 'POST',
        body,
      })
      setNewlyMinted(res)
      setCreateLabel('')
      setCreateRestrictTools(false)
      setCreateAllowed(new Set(['lookup_od', 'execute_smartquery', 'recall_tokens']))
      await load()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('create_failed'))
    } finally {
      setCreating(false)
    }
  }

  const handleRevoke = async (id: string, label: string) => {
    if (!confirm(t('revoke_confirm', { label }))) return
    try {
      await api(`/ontology/mcp-keys/${id}`, { method: 'DELETE' })
      msg.success(t('revoked'))
      load()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('revoke_failed'))
    }
  }

  const copyToClipboard = async (text: string, label: string) => {
    try {
      await navigator.clipboard.writeText(text)
      msg.success(t('copied', { label }))
    } catch {
      msg.error(t('copy_failed'))
    }
  }

  const claudeDesktopConfig = JSON.stringify({
    mcpServers: {
      lakehouse2ontology: {
        type: 'streamable-http',
        url: `${origin}/mcp`,
        headers: {
          Authorization: `Bearer <${t('your_api_key')}>`,
        },
      },
    },
  }, null, 2)

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* ── Header ─────────────────────────────────────────────────── */}
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
            <>
              <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
                // MCP KEYS
              </span>
              <span className="font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-muted">
                {keys.length} KEYS
              </span>
            </>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <KeyRound size={14} className="text-ink" aria-hidden="true" />
              </div>
              <div className="min-w-0">
                <h1 className="text-base font-semibold tracking-tight text-ink">{t('page_title')}</h1>
                <p className="truncate text-xs text-ink-muted">
                  {t('page_subtitle')}
                </p>
              </div>
            </>
          )}
        </div>
        <div className="flex flex-shrink-0 items-center gap-4">
          {!industrial && (
            <div className="hidden items-baseline gap-x-3 text-xs text-ink-muted md:flex">
              <span>
                {t('key_count', { count: keys.length })}
              </span>
            </div>
          )}
          <AnimatedButton
            variant="primary"
            size="sm"
            onClick={() => { setCreateOpen(true); setNewlyMinted(null) }}
          >
            <Plus size={12} aria-hidden="true" />
            {t('new_key')}
          </AnimatedButton>
        </div>
      </motion.header>

      {/* ── Scroll body ────────────────────────────────────────────── */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="space-y-6 px-6 py-6">
          {/* ── Connection docs ───────────────────────────────────── */}
          <section className={`overflow-hidden bg-white ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}>
            <motion.button
              type="button"
              onClick={() => setDocsOpen(v => !v)}
              whileTap={reduce ? undefined : { scale: 0.995 }}
              transition={{ type: 'spring', stiffness: 500, damping: 30 }}
              aria-expanded={docsOpen}
              className={`flex w-full items-center justify-between gap-2 bg-canvas-alt px-4 py-2.5 text-left outline-none cursor-pointer focus-visible:ring-1 focus-visible:ring-ink ${
                industrial ? 'border-b border-ink' : 'border-b border-border-light'
              }`}
            >
              <div className="flex items-center gap-2">
                <BookOpen size={14} className="text-ink-muted" aria-hidden="true" />
                <span className={industrial ? 'font-mono text-[11px] uppercase tracking-[0.18em] text-ink font-medium' : 'text-sm font-medium text-ink'}>
                  {industrial ? `// ${t('conn_docs')}` : t('conn_docs')}
                </span>
                <span className={industrial ? 'font-mono text-[10px] tracking-[0.06em] text-ink-ghost' : 'text-xs text-ink-ghost'}>{t('conn_docs_hint')}</span>
              </div>
              {docsOpen
                ? <ChevronDown size={14} className="text-ink-ghost" aria-hidden="true" />
                : <ChevronRight size={14} className="text-ink-ghost" aria-hidden="true" />}
            </motion.button>
            <AnimatePresence initial={false}>
              {docsOpen && (
                <motion.div
                  initial={reduce ? undefined : { height: 0, opacity: 0 }}
                  animate={reduce ? undefined : { height: 'auto', opacity: 1 }}
                  exit={reduce ? undefined : { height: 0, opacity: 0 }}
                  transition={{ duration: 0.2, ease: 'easeOut' }}
                  style={{ overflow: 'hidden' }}
                >
                  <div className="space-y-5 px-5 py-4">
                    {/* ── Endpoint overview ─────────────────── */}
                    <div>
                      <div className="mb-2 text-xs font-medium text-ink">{t('endpoint_label')}</div>
                      <div className="grid gap-2">
                        <DocEndpointRow
                          label={t('endpoint_mcp')}
                          method="POST"
                          url={`${origin}/mcp`}
                          onCopy={copyToClipboard}
                        />
                        <DocEndpointRow
                          label={t('endpoint_rest')}
                          method="POST"
                          url={`${origin}/api/mcp/v1/tools/<tool_name>`}
                          onCopy={copyToClipboard}
                        />
                      </div>
                    </div>

                    {/* ── Auth ─────────────────────────────── */}
                    <div>
                      <div className="mb-2 text-xs font-medium text-ink">{t('auth_label')}</div>
                      <pre className="overflow-x-auto rounded-md border border-border bg-canvas-alt px-3 py-2 font-mono text-xs leading-relaxed text-ink">
{t('auth_code_example')}
                      </pre>
                      <p className="mt-1.5 text-[11px] text-ink-muted">
                        {t('auth_note')}
                      </p>
                    </div>

                    {/* ── Claude Desktop / Code config ─── */}
                    <div>
                      <div className="mb-2 flex items-center justify-between gap-2">
                        <div className="text-xs font-medium text-ink">{t('claude_desktop_config')}</div>
                        <motion.button
                          onClick={() => copyToClipboard(claudeDesktopConfig, t('claude_config_json'))}
                          whileTap={reduce ? undefined : { scale: 0.97 }}
                          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                          className="inline-flex items-center gap-1 rounded border border-border bg-white px-1.5 py-0.5 text-[11px] text-ink-muted outline-none hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                        >
                          <Copy size={10} aria-hidden="true" />
                          {t('copy')}
                        </motion.button>
                      </div>
                      <pre className="overflow-x-auto rounded-md border border-border bg-canvas-alt px-3 py-2 font-mono text-xs leading-relaxed text-ink">
                        {claudeDesktopConfig}
                      </pre>
                      <p className="mt-1.5 text-[11px] text-ink-muted">
                        {t('claude_config_paste_hint')}
                      </p>
                    </div>

                    {/* ── curl example ─────────────────────── */}
                    <div>
                      <div className="mb-2 text-xs font-medium text-ink">{t('curl_example')}</div>
                      <pre className="overflow-x-auto rounded-md border border-border bg-canvas-alt px-3 py-2 font-mono text-xs leading-relaxed text-ink">
{`curl -X POST '${origin}/api/mcp/v1/tools/lookup_od' \\
  -H 'Authorization: Bearer mcp_XXXX…' \\
  -H 'Content-Type: application/json' \\
  -d '{"project_id":"<uuid>","od_name":"Order"}'`}
                      </pre>
                    </div>

                    {/* ── Tool catalog ─────────────────────── */}
                    <div>
                      <div className="mb-2 text-xs font-medium text-ink">{t('available_tools', { count: TOOL_CATALOG.length })}</div>
                      <div className="divide-y divide-border-light rounded-md border border-border">
                        {TOOL_CATALOG.map(tool => (
                          <div key={tool.name} className="flex items-start gap-3 px-3 py-2">
                            <Terminal size={12} className="mt-1 flex-shrink-0 text-ink-muted" aria-hidden="true" />
                            <div className="min-w-0 flex-1">
                              <div className="flex items-baseline gap-2">
                                <span className="font-mono text-xs font-semibold text-ink">{tool.name}</span>
                                <span className="text-xs text-ink-muted">{tool.label}</span>
                              </div>
                              <div className="mt-0.5 text-[11px] text-ink-ghost">{tool.hint}</div>
                            </div>
                          </div>
                        ))}
                      </div>
                    </div>

                    {/* ── Security note ──────────────────── */}
                    <div className="rounded-md border border-border bg-canvas-alt px-3 py-2.5">
                      <div className="mb-1 flex items-center gap-1.5 text-xs font-medium text-ink">
                        <ShieldCheck size={12} className="text-ink-muted" aria-hidden="true" />
                        {t('security_title')}
                      </div>
                      <ul className="space-y-1 text-[11px] leading-relaxed text-ink-muted">
                        <li>· {t('security_1')}</li>
                        <li>· {t('security_2')}</li>
                        <li>· {t('security_3')}</li>
                        <li>· {t('security_4')}</li>
                        <li>· {t('security_5')}</li>
                      </ul>
                    </div>
                  </div>
                </motion.div>
              )}
            </AnimatePresence>
          </section>

          {/* ── Keys list ─────────────────────────────────────────── */}
          <section className={`overflow-hidden bg-white ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}>
            <div className={`flex items-center justify-between gap-2 bg-canvas-alt px-4 py-2.5 ${industrial ? 'border-b border-ink' : 'border-b border-border-light'}`}>
              <div className="flex items-center gap-2">
                <KeyRound size={14} className="text-ink-muted" aria-hidden="true" />
                <span className={industrial ? 'font-mono text-[11px] uppercase tracking-[0.18em] text-ink font-medium' : 'text-sm font-medium text-ink'}>
                  {industrial ? `// ${t('my_keys')}` : t('my_keys')}
                </span>
                <span className={industrial ? 'font-mono text-[10px] tracking-[0.1em] text-ink-ghost' : 'text-xs text-ink-ghost'}>
                  <span className="tabular-nums">{keys.length}</span> {t('active_count')}
                </span>
              </div>
            </div>
            {loading ? (
              <div className="flex items-center justify-center py-12">
                <InlineLoader text={t('loading')} />
              </div>
            ) : keys.length === 0 ? (
              <div className="flex flex-col items-center justify-center gap-1.5 px-4 py-12 text-center">
                <span className="text-sm text-ink-muted">{t('no_keys')}</span>
                <span className="text-xs text-ink-ghost">{t('no_keys_hint')}</span>
              </div>
            ) : (
              <div ref={listRef} className={industrial ? 'divide-y divide-ink/15' : 'divide-y divide-border-light'}>
                {keys.map(k => {
                  const restricted = k.allowedTools != null
                  return (
                    <div key={k.id} className="flex items-start gap-3 px-4 py-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className={`font-semibold ${
                            industrial ? 'font-mono text-[12px] tracking-[0.04em] text-ink' : 'text-sm text-ink'
                          }`}>{k.label}</span>
                          {restricted ? (
                            <span className={`inline-flex items-center gap-1 bg-white px-1.5 py-0.5 text-ink-muted ${
                              industrial
                                ? 'border border-ink font-mono text-[10px] uppercase tracking-[0.12em]'
                                : 'rounded border border-border text-[11px]'
                            }`}>
                              <ShieldCheck size={10} aria-hidden="true" />
                              {t('limited_tools', { count: k.allowedTools!.length })}
                            </span>
                          ) : (
                            <span className={`inline-flex items-center gap-1 bg-canvas-alt px-1.5 py-0.5 text-ink-ghost ${
                              industrial
                                ? 'border border-dashed border-ink/40 font-mono text-[10px] uppercase tracking-[0.12em]'
                                : 'rounded border border-dashed border-border text-[11px]'
                            }`}>
                              {t('all_tools')}
                            </span>
                          )}
                        </div>
                        {restricted && (
                          <div className="mt-1 flex flex-wrap gap-1">
                            {k.allowedTools!.map(t => (
                              <span key={t} className={`bg-canvas-alt px-1.5 py-0.5 font-mono text-ink-muted ${
                                industrial
                                  ? 'border border-ink/40 text-[10px] tracking-[0.04em]'
                                  : 'rounded border border-border text-[11px]'
                              }`}>
                                {t}
                              </span>
                            ))}
                          </div>
                        )}
                        <div className={`mt-1.5 flex flex-wrap items-center gap-3 text-ink-ghost ${
                          industrial ? 'font-mono text-[10px] tracking-[0.06em]' : 'text-[11px]'
                        }`}>
                          <span className="inline-flex items-center gap-1">
                            <Clock size={10} aria-hidden="true" />
                            {t('created_at')} <span className="tabular-nums">{formatDate(k.createdAt)}</span>
                          </span>
                          <span>·</span>
                          <span>
                            {t('last_used')} <span className={k.lastUsedAt ? 'tabular-nums text-ink-muted' : 'text-ink-ghost'}>
                              {formatDate(k.lastUsedAt)}
                            </span>
                          </span>
                        </div>
                      </div>
                      <motion.button
                        onClick={() => handleRevoke(k.id, k.label)}
                        whileHover={reduce ? undefined : { scale: 1.05 }}
                        whileTap={reduce ? undefined : { scale: 0.95 }}
                        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                        aria-label={t('revoke_key')}
                        title={t('revoke')}
                        className={`inline-flex h-7 items-center gap-1 bg-white px-2 text-ink-muted outline-none hover:border-danger hover:text-danger focus-visible:ring-1 focus-visible:ring-danger ${
                          industrial
                            ? 'border border-ink font-mono text-[10px] uppercase tracking-[0.14em]'
                            : 'rounded-md border border-border text-[11px]'
                        }`}
                      >
                        <Trash2 size={11} aria-hidden="true" />
                        {t('revoke')}
                      </motion.button>
                    </div>
                  )
                })}
              </div>
            )}
          </section>
        </div>
      </div>

      {/* ── Create / newly-minted modal ───────────────────────────── */}
      <AnimatePresence>
        {createOpen && (
          <motion.div
            initial={reduce ? undefined : { opacity: 0 }}
            animate={reduce ? undefined : { opacity: 1 }}
            exit={reduce ? undefined : { opacity: 0 }}
            transition={{ duration: 0.15 }}
            className="fixed inset-0 z-50 flex items-center justify-center bg-ink/20"
            role="dialog"
            aria-modal="true"
            aria-label={t('new_key_dialog')}
            onClick={() => {
              if (!creating && !newlyMinted) setCreateOpen(false)
              // Once raw key is shown, force user to explicitly close via button
            }}
          >
            <motion.div
              initial={reduce ? undefined : { scale: 0.98, opacity: 0 }}
              animate={reduce ? undefined : { scale: 1, opacity: 1 }}
              exit={reduce ? undefined : { scale: 0.98, opacity: 0 }}
              transition={{ duration: 0.15, ease: 'easeOut' }}
              className="relative w-[520px] max-w-[calc(100vw-32px)] rounded-md border border-border bg-white shadow-lg"
              onClick={e => e.stopPropagation()}
            >
              <div className="flex items-center justify-between border-b border-border-light bg-canvas-alt px-4 py-2.5">
                <div className="flex items-center gap-2">
                  {newlyMinted ? (
                    <>
                      <CheckCircle2 size={14} className="text-success" aria-hidden="true" />
                      <span className="text-sm font-semibold text-ink">{t('key_generated')}</span>
                    </>
                  ) : (
                    <>
                      <KeyRound size={14} className="text-ink-muted" aria-hidden="true" />
                      <span className="text-sm font-semibold text-ink">{t('new_key')}</span>
                    </>
                  )}
                </div>
                <motion.button
                  onClick={() => { setCreateOpen(false); setNewlyMinted(null) }}
                  whileHover={reduce ? undefined : { scale: 1.15 }}
                  whileTap={reduce ? undefined : { scale: 0.9 }}
                  transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                  aria-label={t('close')}
                  title={t('close')}
                  className="rounded p-1 text-ink-ghost outline-none hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                >
                  <X className="h-4 w-4" aria-hidden="true" />
                </motion.button>
              </div>

              {newlyMinted ? (
                <div className="space-y-3 p-5">
                  <div className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-2.5">
                    <AlertTriangle size={14} className="mt-0.5 flex-shrink-0 text-warning" aria-hidden="true" />
                    <div className="text-[11px] leading-relaxed text-ink">
                      {t('key_one_time_warning')}
                    </div>
                  </div>
                  <div>
                    <div className="mb-1 text-xs font-medium text-ink-muted">Label</div>
                    <div className="rounded-md border border-border bg-canvas-alt px-3 py-2 text-sm text-ink">
                      {newlyMinted.label}
                    </div>
                  </div>
                  <div>
                    <div className="mb-1 flex items-center justify-between">
                      <span className="text-xs font-medium text-ink-muted">{t('raw_key_label')}</span>
                      <motion.button
                        onClick={() => copyToClipboard(newlyMinted.rawKey, t('key_word'))}
                        whileTap={reduce ? undefined : { scale: 0.97 }}
                        transition={{ type: 'spring', stiffness: 500, damping: 30 }}
                        className="inline-flex items-center gap-1 rounded border border-ink bg-ink px-2 py-0.5 text-[11px] text-white outline-none focus-visible:ring-1 focus-visible:ring-ink"
                      >
                        <Copy size={10} aria-hidden="true" />
                        {t('copy')}
                      </motion.button>
                    </div>
                    <div className="break-all rounded-md border border-border bg-canvas-alt px-3 py-2 font-mono text-sm text-ink">
                      {newlyMinted.rawKey}
                    </div>
                  </div>
                  {newlyMinted.allowedTools != null && newlyMinted.allowedTools.length > 0 && (
                    <div>
                      <div className="mb-1 text-xs font-medium text-ink-muted">{t('tool_whitelist')}</div>
                      <div className="flex flex-wrap gap-1">
                        {newlyMinted.allowedTools.map(t => (
                          <span key={t} className="rounded border border-border bg-white px-1.5 py-0.5 font-mono text-[11px] text-ink">
                            {t}
                          </span>
                        ))}
                      </div>
                    </div>
                  )}
                  <div className="flex justify-end pt-1">
                    <AnimatedButton
                      variant="primary"
                      size="sm"
                      onClick={() => { setCreateOpen(false); setNewlyMinted(null) }}
                    >
                      <CheckCircle2 size={12} aria-hidden="true" />
                      {t('saved_close')}
                    </AnimatedButton>
                  </div>
                </div>
              ) : (
                <div className="space-y-3 p-5">
                  <div>
                    <label className="mb-1 block text-xs font-medium text-ink-muted">
                      {t('label_hint')}
                    </label>
                    <input
                      value={createLabel}
                      onChange={e => setCreateLabel(e.target.value)}
                      placeholder={t('label_placeholder')}
                      className="h-8 w-full rounded-md border border-border bg-white px-2.5 text-sm text-ink outline-none focus:border-ink"
                      autoFocus
                      maxLength={64}
                      aria-label="label"
                    />
                  </div>

                  <div>
                    <label className="flex cursor-pointer items-start gap-2">
                      <input
                        type="checkbox"
                        checked={createRestrictTools}
                        onChange={e => setCreateRestrictTools(e.target.checked)}
                        className="mt-0.5 accent-ink"
                      />
                      <div>
                        <div className="text-xs font-medium text-ink">{t('restrict_tools')}</div>
                        <div className="text-[11px] leading-relaxed text-ink-muted">
                          {t('restrict_tools_hint')}
                        </div>
                      </div>
                    </label>
                  </div>

                  {createRestrictTools && (
                    <div className="ml-6 space-y-1.5 rounded-md border border-border bg-canvas-alt px-3 py-2.5">
                      {TOOL_CATALOG.map(tool => {
                        const checked = createAllowed.has(tool.name)
                        return (
                          <label key={tool.name} className="flex cursor-pointer items-start gap-2">
                            <input
                              type="checkbox"
                              checked={checked}
                              onChange={e => {
                                setCreateAllowed(prev => {
                                  const n = new Set(prev)
                                  if (e.target.checked) n.add(tool.name); else n.delete(tool.name)
                                  return n
                                })
                              }}
                              className="mt-0.5 accent-ink"
                            />
                            <div className="min-w-0 flex-1">
                              <div className="flex items-baseline gap-2">
                                <span className="font-mono text-[11px] font-semibold text-ink">{tool.name}</span>
                                <span className="text-[11px] text-ink-muted">{tool.label}</span>
                              </div>
                              <div className="text-[11px] text-ink-ghost">{tool.hint}</div>
                            </div>
                          </label>
                        )
                      })}
                    </div>
                  )}

                  <div className="flex justify-end gap-2 pt-2">
                    <AnimatedButton
                      variant="secondary"
                      size="sm"
                      onClick={() => setCreateOpen(false)}
                      disabled={creating}
                    >
                      {t('cancel')}
                    </AnimatedButton>
                    <AnimatedButton
                      variant="primary"
                      size="sm"
                      onClick={handleCreate}
                      disabled={creating || !createLabel.trim()}
                    >
                      <Plus size={12} aria-hidden="true" />
                      {creating ? t('generating') : t('generate_key')}
                    </AnimatedButton>
                  </div>
                </div>
              )}
            </motion.div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

// ─── Sub-component: doc endpoint row ────────────────────────────────

function DocEndpointRow({ label, method, url, onCopy }: {
  label: string
  method: 'POST' | 'GET'
  url: string
  onCopy: (text: string, label: string) => void
}) {
  const t = useTranslations('settings.mcp')
  const reduce = useReducedMotion()
  return (
    <div className="rounded-md border border-border bg-canvas-alt px-3 py-2">
      <div className="mb-1 text-[11px] text-ink-muted">{label}</div>
      <div className="flex items-center gap-2">
        <span className="rounded border border-ink bg-ink px-1.5 py-0.5 font-mono text-[11px] text-white">
          {method}
        </span>
        <code className="min-w-0 flex-1 truncate font-mono text-xs text-ink">{url}</code>
        <motion.button
          onClick={() => onCopy(url, 'URL')}
          whileTap={reduce ? undefined : { scale: 0.97 }}
          transition={{ type: 'spring', stiffness: 500, damping: 30 }}
          className="inline-flex flex-shrink-0 items-center gap-1 rounded border border-border bg-white px-1.5 py-0.5 text-[11px] text-ink-muted outline-none hover:border-ink hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
          aria-label={t('copy_btn')}
          title={t('copy_btn')}
        >
          <Copy size={10} aria-hidden="true" />
          {t('copy_btn')}
        </motion.button>
      </div>
    </div>
  )
}
