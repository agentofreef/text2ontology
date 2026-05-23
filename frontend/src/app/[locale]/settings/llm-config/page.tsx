'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback } from 'react'
import { Card } from '@/components/ui/Card'
import { Button } from '@/components/ui/Button'
import { Modal } from '@/components/ui/Modal'
import { Input } from '@/components/ui/Input'
import { Select } from '@/components/ui/Select'
import { MotionFade, MotionScale, DataLoader } from '@/lib/motion'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { useStyleMode } from '@/lib/style-mode'
import { llmDisplay, llmSubtitle } from '@/lib/llmDisplay'
import type { LLMConfig, LLMRoleBinding, ListResponse } from '@/types/api'
import { Plus, Zap, Power, Trash2, CheckCircle, AlertCircle, Loader2, Pencil, X, AlertTriangle } from 'lucide-react'

// LLMConfig.alias was added to the shared type — keep this alias for backwards
// compat with code paths that still spread `as LLMConfigWithAlias`.
type LLMConfigWithAlias = LLMConfig

const vendorOptions = [
  { value: 'openai', label: 'OpenAI' },
  { value: 'anthropic', label: 'Anthropic (Claude)' },
  { value: 'deepseek', label: 'DeepSeek' },
  { value: 'qwen', label: 'Qwen' },
  { value: 'glm', label: 'GLM (智谱)' },
  { value: 'custom', label: 'Custom' },
]

const ROLE_DEFS: { name: string; label: string; configType: 'chat' | 'embedding' }[] = [
  { name: 'tokenize', label: 'Tokenize', configType: 'chat' },
  { name: 'agent', label: 'Agent', configType: 'chat' },
  { name: 'synthesizer', label: 'Synthesizer', configType: 'chat' },
  { name: 'embedding', label: 'Embedding', configType: 'embedding' },
]

function ConfigRow({ config, onActivate, onDelete, onEdit }: {
  config: LLMConfigWithAlias
  onActivate: () => void
  onDelete: () => void
  onEdit: () => void
}) {
  const t = useTranslations('settings.llm')
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{
    success?: boolean
    latencyMs?: number
    response?: string
    dimension?: number
    error?: string
  } | null>(null)

  const runTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await api<typeof testResult>(`/llm-config/${config.id}/test`, { method: 'POST' })
      setTestResult(res)
    } catch (e) {
      setTestResult({ error: e instanceof Error ? e.message : 'Unknown error' })
    } finally {
      setTesting(false)
    }
  }

  return (
    <div
      className="border-b border-border-light last:border-b-0 cursor-pointer hover:bg-gray-50 transition-colors duration-150"
      onClick={(e) => {
        if ((e.target as HTMLElement).closest('button')) return
        onEdit()
      }}
    >
      <div className="flex items-center justify-between px-4 py-3">
        <div className="flex items-center gap-4">
          <div className="flex items-center gap-2">
            {/* SV Minimal: only 黑/白/灰/绿/红 — inactive uses neutral gray, not amber */}
            <span className={`inline-block h-2 w-2 rounded-full ${config.isActive ? 'bg-success' : 'bg-ink-ghost'}`} />
            <span className="text-xs text-ink-muted">{config.isActive ? 'Active' : 'Inactive'}</span>
          </div>
          <div>
            <div className="flex items-center gap-2">
              <span className="text-xs font-semibold text-gray-500 uppercase">{config.vendor}</span>
              <div className="flex flex-col gap-0.5">
                <span className="text-sm font-medium text-ink">
                  {config.alias || config.modelName}
                </span>
                {config.alias && (
                  <span className="text-[11px] font-mono text-ink-ghost">
                    {config.modelName}
                  </span>
                )}
              </div>
              {config.configType === 'chat' && config.isThinking && (
                <span className="rounded border border-border bg-canvas-muted px-1.5 py-0.5 text-[10px] font-medium text-ink-muted">Thinking</span>
              )}
              {config.configType === 'chat' && config.isToolCall && (
                <span className="rounded border border-border bg-canvas-muted px-1.5 py-0.5 text-[10px] font-medium text-ink-muted">Tool Call</span>
              )}
              {config.configType === 'embedding' && config.vectorDim && (
                <span className="rounded border border-border bg-canvas-muted px-1.5 py-0.5 text-[10px] text-ink-muted">dim:{config.vectorDim}</span>
              )}
            </div>
            <div className="text-xs text-gray-400 mt-0.5">
              {config.baseUrl}
              {config.proxyUrl && <span className="ml-2 text-blue-400">via {config.proxyUrl}</span>}
            </div>
          </div>
        </div>
        <div className="flex items-center gap-2" onClick={(e) => e.stopPropagation()}>
          {testResult && !testing && (
            <span
              className={`inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs ${
                testResult.success
                  ? 'border-emerald-200 bg-emerald-50 text-emerald-700'
                  : 'border-red-200 bg-red-50 text-red-700'
              }`}
              title={testResult.error || testResult.response || ''}
            >
              {testResult.success ? (
                <>
                  <CheckCircle className="h-3 w-3" />
                  {testResult.latencyMs != null && <span>{testResult.latencyMs}ms</span>}
                  {testResult.dimension != null && <span>dim:{testResult.dimension}</span>}
                </>
              ) : (
                <>
                  <AlertCircle className="h-3 w-3" />
                  <span className="max-w-[260px] truncate">{testResult.error || 'Failed'}</span>
                </>
              )}
            </span>
          )}
          <Button variant="ghost" size="sm" onClick={runTest} disabled={testing}>
            {testing ? <Loader2 className="h-3 w-3 animate-spin" /> : <Zap className="h-3 w-3" />}
            {t('test')}
          </Button>
          {!config.isActive && (
            <Button variant="ghost" size="sm" onClick={onActivate}>
              <Power className="h-3 w-3" /> {t('activate')}
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={onEdit}>
            <Pencil className="h-3 w-3" />
          </Button>
          <Button variant="ghost" size="sm" onClick={onDelete}>
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      </div>
    </div>
  )
}

interface ChatFormState {
  vendor: string
  baseUrl: string
  apiKey: string
  modelName: string
  isThinking: boolean
  isToolCall: boolean
  proxyUrl: string
  alias: string
  note: string
}

interface EmbeddingFormState {
  vendor: string
  baseUrl: string
  apiKey: string
  modelName: string
  proxyUrl: string
  alias: string
  note: string
}

function ChatConfigModal({ open, onClose, onSaved, editConfig }: {
  open: boolean
  onClose: () => void
  onSaved: () => void
  editConfig?: LLMConfig
}) {
  const t = useTranslations('settings.llm')
  const [form, setForm] = useState<ChatFormState>({ vendor: 'openai', baseUrl: '', apiKey: '', modelName: '', isThinking: false, isToolCall: true, proxyUrl: '', alias: '', note: '' })
  const [models, setModels] = useState<string[]>([])
  const [loadingModels, setLoadingModels] = useState(false)
  const [manualModelInput, setManualModelInput] = useState(false)
  const [testResult, setTestResult] = useState<{ success?: boolean; latencyMs?: number; response?: string; error?: string } | null>(null)
  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false)
  const [loadingConfig, setLoadingConfig] = useState(false)
  const msg = useMessage()

  useEffect(() => {
    const loadFullConfig = async () => {
      if (editConfig) {
        setLoadingConfig(true)
        try {
          const fullConfig = await api<LLMConfig>(`/llm-config/${editConfig.id}`)
          setForm({
            vendor: fullConfig.vendor,
            baseUrl: fullConfig.baseUrl,
            apiKey: fullConfig.apiKey || '',
            modelName: fullConfig.modelName,
            isThinking: fullConfig.isThinking || false,
            isToolCall: fullConfig.isToolCall || false,
            proxyUrl: fullConfig.proxyUrl || '',
            alias: (fullConfig as LLMConfigWithAlias).alias || '',
            note: fullConfig.note || '',
          })
        } catch {
          setForm({
            vendor: editConfig.vendor,
            baseUrl: editConfig.baseUrl,
            apiKey: '',
            modelName: editConfig.modelName,
            isThinking: editConfig.isThinking || false,
            isToolCall: editConfig.isToolCall || false,
            proxyUrl: editConfig.proxyUrl || '',
            alias: (editConfig as LLMConfigWithAlias).alias || '',
            note: editConfig.note || '',
          })
        } finally {
          setLoadingConfig(false)
        }
        setTestResult(null)
      } else if (open) {
        setForm({ vendor: 'openai', baseUrl: '', apiKey: '', modelName: '', isThinking: false, isToolCall: true, proxyUrl: '', alias: '', note: '' })
        setTestResult(null)
      }
    }
    loadFullConfig()
  }, [editConfig, open])

  const fetchModels = async () => {
    setLoadingModels(true)
    setModels([])
    try {
      const params = new URLSearchParams({ baseUrl: form.baseUrl, apiKey: form.apiKey, vendor: form.vendor })
      const res = await api<{ data?: { id: string }[] }>(`/llm-config/models?${params}`)
      const ids = (res.data || []).map((m) => m.id)
      setModels(ids)
      if (ids.length > 0 && !form.modelName) {
        setForm((f) => ({ ...f, modelName: ids[0] }))
      }
    } catch {
      setModels([])
    } finally {
      setLoadingModels(false)
    }
  }

  const testChat = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await api<{ success?: boolean; latencyMs?: number; response?: string; error?: string }>('/llm-config/test-chat', {
        method: 'POST',
        body: { baseUrl: form.baseUrl, apiKey: form.apiKey, modelName: form.modelName, isThinking: form.isThinking, proxyUrl: form.proxyUrl, vendor: form.vendor },
      })
      setTestResult(res)
    } catch (e) {
      setTestResult({ error: e instanceof Error ? e.message : 'Unknown error' })
    } finally {
      setTesting(false)
    }
  }

  const save = async () => {
    setSaving(true)
    try {
      if (editConfig) {
        await api(`/llm-config/${editConfig.id}`, { method: 'PUT', body: { ...form } })
        msg.success(t('chat_updated'))
      } else {
        await api('/llm-config', { method: 'POST', body: { configType: 'chat', ...form } })
        msg.success(t('chat_saved'))
      }
      onSaved()
      onClose()
    } catch {
      msg.error(t('save_failed'))
    } finally {
      setSaving(false)
    }
  }

  const canTest = form.baseUrl && form.modelName
  const canSave = testResult?.success

  return (
    <Modal open={open} onClose={onClose} title={editConfig ? t('edit_chat_config') : t('new_chat_config')} width="620px">
      <div className="space-y-4">
        {loadingConfig && <div className="text-sm text-gray-400 text-center py-2">{t('loading_config')}</div>}
        <div className="grid grid-cols-2 gap-4">
          <Select label="Vendor" options={vendorOptions} value={form.vendor} onChange={(e) => setForm({ ...form, vendor: e.target.value })} />
          <Input label="Base URL" placeholder="http://localhost:8132" value={form.baseUrl} onChange={(e) => setForm({ ...form, baseUrl: e.target.value })} />
        </div>
        <Input label="API Key" placeholder="sk-..." type="password" value={form.apiKey} onChange={(e) => setForm({ ...form, apiKey: e.target.value })} />
        <Input label={t('proxy_optional')} placeholder="http://proxy:port 或 socks5://proxy:port" value={form.proxyUrl} onChange={(e) => setForm({ ...form, proxyUrl: e.target.value })} />

        <div className="flex items-end gap-2">
          <div className="flex-1">
            {models.length > 0 && !manualModelInput ? (
              <Select label="Model" options={models.map((m) => ({ value: m, label: m }))} value={form.modelName} onChange={(e) => setForm({ ...form, modelName: e.target.value })} />
            ) : (
              <Input label="Model" placeholder={t('model_placeholder')} value={form.modelName} onChange={(e) => setForm({ ...form, modelName: e.target.value })} />
            )}
          </div>
          {models.length > 0 && (
            <Button variant="ghost" size="sm" onClick={() => setManualModelInput(!manualModelInput)}>
              <Pencil className="h-3 w-3" />
              {manualModelInput ? t('list') : t('manual')}
            </Button>
          )}
          <Button onClick={fetchModels} disabled={!form.baseUrl || loadingModels} size="sm">
            {loadingModels ? <Loader2 className="h-3 w-3 animate-spin" /> : <Zap className="h-3 w-3" />}
            {t('fetch_models')}
          </Button>
        </div>

        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={form.isThinking} onChange={(e) => setForm({ ...form, isThinking: e.target.checked })} className="h-4 w-4 rounded accent-blue-500" />
          <span className="text-sm text-gray-700">{t('thinking_model')}</span>
        </label>

        <label className="flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={form.isToolCall} onChange={(e) => setForm({ ...form, isToolCall: e.target.checked })} className="h-4 w-4 rounded accent-blue-500" />
          <span className="text-sm text-gray-700">{t('tool_call')}</span>
        </label>

        <Input label={t('alias')} placeholder={t('alias_placeholder')} value={form.alias} onChange={(e) => setForm({ ...form, alias: e.target.value })} />
        <Input label="Note" placeholder={t('note_placeholder')} value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} />

        {testResult && (
          <div className={`rounded-lg border p-3 text-sm ${testResult.success ? 'border-emerald-200 bg-emerald-50' : 'border-red-200 bg-red-50'}`}>
            <div className="flex items-center gap-1.5 mb-1">
              {testResult.success ? <CheckCircle className="h-3.5 w-3.5 text-emerald-600" /> : <AlertCircle className="h-3.5 w-3.5 text-red-500" />}
              <span className="font-semibold">{testResult.success ? t('conn_ok') : t('conn_fail')}</span>
              {testResult.latencyMs != null && <span className="text-gray-500 ml-2">{testResult.latencyMs}ms</span>}
            </div>
            {testResult.response && <div className="text-gray-600 mt-1 line-clamp-3">{testResult.response}</div>}
            {testResult.error && <div className="text-red-600 mt-1">{testResult.error}</div>}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-2 border-t border-border-light">
          <Button onClick={testChat} disabled={!canTest || testing} size="sm">
            {testing ? <Loader2 className="h-3 w-3 animate-spin" /> : <Zap className="h-3 w-3" />}
            {t('test_conn')}
          </Button>
          <Button variant="primary" onClick={save} disabled={!canSave || saving} size="sm">
            {t('save')}
          </Button>
        </div>
      </div>
    </Modal>
  )
}

function EmbeddingConfigModal({ open, onClose, onSaved, editConfig }: {
  open: boolean
  onClose: () => void
  onSaved: () => void
  editConfig?: LLMConfig
}) {
  const t = useTranslations('settings.llm')
  const [form, setForm] = useState<EmbeddingFormState>({ vendor: 'openai', baseUrl: '', apiKey: '', modelName: '', proxyUrl: '', alias: '', note: '' })
  const [testResult, setTestResult] = useState<{ success?: boolean; latencyMs?: number; dimension?: number; error?: string } | null>(null)
  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false)
  const [loadingConfig, setLoadingConfig] = useState(false)
  const msg = useMessage()

  useEffect(() => {
    const loadFullConfig = async () => {
      if (editConfig) {
        setLoadingConfig(true)
        try {
          const fullConfig = await api<LLMConfig>(`/llm-config/${editConfig.id}`)
          setForm({
            vendor: fullConfig.vendor,
            baseUrl: fullConfig.baseUrl,
            apiKey: fullConfig.apiKey || '',
            modelName: fullConfig.modelName,
            proxyUrl: fullConfig.proxyUrl || '',
            alias: (fullConfig as LLMConfigWithAlias).alias || '',
            note: fullConfig.note || '',
          })
        } catch {
          setForm({
            vendor: editConfig.vendor,
            baseUrl: editConfig.baseUrl,
            apiKey: '',
            modelName: editConfig.modelName,
            proxyUrl: editConfig.proxyUrl || '',
            alias: (editConfig as LLMConfigWithAlias).alias || '',
            note: editConfig.note || '',
          })
        } finally {
          setLoadingConfig(false)
        }
        setTestResult(null)
      } else if (open) {
        setForm({ vendor: 'openai', baseUrl: '', apiKey: '', modelName: '', proxyUrl: '', alias: '', note: '' })
        setTestResult(null)
      }
    }
    loadFullConfig()
  }, [editConfig, open])

  const testEmbedding = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await api<{ success?: boolean; latencyMs?: number; dimension?: number; error?: string }>('/llm-config/test-embedding', {
        method: 'POST',
        body: { baseUrl: form.baseUrl, apiKey: form.apiKey, modelName: form.modelName, proxyUrl: form.proxyUrl },
      })
      setTestResult(res)
    } catch (e) {
      setTestResult({ error: e instanceof Error ? e.message : 'Unknown error' })
    } finally {
      setTesting(false)
    }
  }

  const save = async () => {
    setSaving(true)
    try {
      if (editConfig) {
        await api(`/llm-config/${editConfig.id}`, { method: 'PUT', body: { ...form } })
        msg.success(t('emb_updated'))
      } else {
        await api('/llm-config', { method: 'POST', body: { configType: 'embedding', ...form, vectorDim: testResult?.dimension ?? null } })
        msg.success(t('emb_saved'))
      }
      onSaved()
      onClose()
    } catch {
      msg.error(t('save_failed'))
    } finally {
      setSaving(false)
    }
  }

  const canTest = form.baseUrl && form.modelName
  const canSave = testResult?.success

  return (
    <Modal open={open} onClose={onClose} title={editConfig ? t('edit_emb_config') : t('new_emb_config')} width="620px">
      <div className="space-y-4">
        {loadingConfig && <div className="text-sm text-gray-400 text-center py-2">{t('loading_config')}</div>}
        <div className="grid grid-cols-2 gap-4">
          <Select label="Vendor" options={vendorOptions} value={form.vendor} onChange={(e) => setForm({ ...form, vendor: e.target.value })} />
          <Input label="Base URL" placeholder="http://localhost:8132" value={form.baseUrl} onChange={(e) => setForm({ ...form, baseUrl: e.target.value })} />
        </div>
        <Input label="API Key" placeholder="sk-..." type="password" value={form.apiKey} onChange={(e) => setForm({ ...form, apiKey: e.target.value })} />
        <Input label={t('proxy_optional')} placeholder="http://proxy:port 或 socks5://proxy:port" value={form.proxyUrl} onChange={(e) => setForm({ ...form, proxyUrl: e.target.value })} />
        <Input label="Model Name" placeholder="bge-large-zh-v1.5" value={form.modelName} onChange={(e) => setForm({ ...form, modelName: e.target.value })} />
        <Input label={t('alias')} placeholder={t('alias_placeholder')} value={form.alias} onChange={(e) => setForm({ ...form, alias: e.target.value })} />
        <Input label="Note" placeholder={t('note_placeholder')} value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} />

        {testResult && (
          <div className={`rounded-lg border p-3 text-sm ${testResult.success ? 'border-emerald-200 bg-emerald-50' : 'border-red-200 bg-red-50'}`}>
            <div className="flex items-center gap-1.5 mb-1">
              {testResult.success ? <CheckCircle className="h-3.5 w-3.5 text-emerald-600" /> : <AlertCircle className="h-3.5 w-3.5 text-red-500" />}
              <span className="font-semibold">{testResult.success ? t('conn_ok') : t('conn_fail')}</span>
              {testResult.latencyMs != null && <span className="text-gray-500 ml-2">{testResult.latencyMs}ms</span>}
            </div>
            {testResult.success && testResult.dimension && (
              <div className="text-gray-600 mt-1">{t('dimension')}: <span className="font-semibold text-gray-900">{testResult.dimension}</span></div>
            )}
            {testResult.error && <div className="text-red-600 mt-1">{testResult.error}</div>}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-2 border-t border-border-light">
          <Button onClick={testEmbedding} disabled={!canTest || testing} size="sm">
            {testing ? <Loader2 className="h-3 w-3 animate-spin" /> : <Zap className="h-3 w-3" />}
            {t('test_conn')}
          </Button>
          <Button variant="primary" onClick={save} disabled={!canSave || saving} size="sm">
            {t('save')}
          </Button>
        </div>
      </div>
    </Modal>
  )
}

function RoleBindingSection({ configs, bindings, onBindingChange }: {
  configs: LLMConfig[]
  bindings: LLMRoleBinding[]
  onBindingChange: () => void
}) {
  const t = useTranslations('settings.llm')
  const msg = useMessage()
  const bindingMap = new Map(bindings.map((b) => [b.roleName, b]))

  const handleBind = async (roleName: string, configId: string) => {
    if (!configId) {
      try {
        await api(`/llm-role-binding/${roleName}`, { method: 'DELETE' })
        msg.success(t('role_unbound', { role: roleName }))
        onBindingChange()
      } catch {
        msg.error(t('unbind_failed'))
      }
      return
    }
    try {
      await api('/llm-role-binding', { method: 'PUT', body: { roleName, configId } })
      msg.success(t('role_bound', { role: roleName }))
      onBindingChange()
    } catch {
      msg.error(t('bind_failed'))
    }
  }

  return (
    <Card title={t('role_binding')} titlePrefix="Role Binding">
      <div className="rounded-lg border border-border-light overflow-hidden">
        <div className="flex items-center border-b border-border-light bg-gray-50 px-4 py-2 text-xs text-gray-500">
          <div className="w-36">{t('role')}</div>
          <div className="flex-1">{t('model_binding')}</div>
          <div className="w-48">{t('capability')}</div>
        </div>
        {ROLE_DEFS.map((role) => {
          const binding = bindingMap.get(role.name)
          const filteredConfigs = configs.filter((c) => c.configType === role.configType)
          const selectedConfig = binding ? configs.find((c) => c.id === binding.configId) : undefined

          // Subtitle = vendor/model when alias is the primary label. Inline below
          // the select so users who only recognise the model name still see it.
          const subtitle = llmSubtitle(selectedConfig)
          return (
            <div key={role.name} className="flex items-start border-b border-border-light px-4 py-2.5 last:border-b-0">
              <div className="w-36 pt-1.5 text-sm font-medium text-ink">{role.label}</div>
              <div className="flex-1">
                <select
                  className="w-full max-w-xs rounded-md border border-border bg-white px-3 py-1.5 text-sm text-ink focus:border-ink focus:outline-none focus:ring-1 focus:ring-ink/10"
                  value={binding?.configId ?? ''}
                  onChange={(e) => handleBind(role.name, e.target.value)}
                >
                  <option value="">{t('unbound_default')}</option>
                  {filteredConfigs.map((c) => (
                    <option key={c.id} value={c.id}>
                      {llmDisplay(c)}
                    </option>
                  ))}
                </select>
                {subtitle && (
                  <div className="mt-1 max-w-xs truncate font-mono text-[11px] text-ink-ghost" title={subtitle}>
                    {subtitle}
                  </div>
                )}
              </div>
              <div className="flex w-48 flex-wrap items-center gap-1.5 pt-1.5">
                {selectedConfig?.isThinking && (
                  <span className="rounded border border-border bg-canvas-muted px-1.5 py-0.5 text-[10px] font-medium text-ink-muted">Thinking</span>
                )}
                {selectedConfig?.isToolCall && (
                  <span className="rounded border border-border bg-canvas-muted px-1.5 py-0.5 text-[10px] font-medium text-ink-muted">Tool Call</span>
                )}
                {selectedConfig?.configType === 'embedding' && selectedConfig?.vectorDim && (
                  <span className="rounded border border-border bg-canvas-muted px-1.5 py-0.5 text-[10px] text-ink-muted">dim:{selectedConfig.vectorDim}</span>
                )}
                {!binding && <span className="text-[10px] text-ink-ghost">default</span>}
              </div>
            </div>
          )
        })}
      </div>
    </Card>
  )
}

export default function LLMConfigPage() {
  const t = useTranslations('settings.llm')
  const industrial = useStyleMode().mode === 'industrial'
  const [configs, setConfigs] = useState<LLMConfig[]>([])
  const [bindings, setBindings] = useState<LLMRoleBinding[]>([])
  const [loading, setLoading] = useState(true)
  const [chatModalOpen, setChatModalOpen] = useState(false)
  const [embeddingModalOpen, setEmbeddingModalOpen] = useState(false)
  const [editConfig, setEditConfig] = useState<LLMConfig | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<{ id: string; label: string } | null>(null)
  const [deleteImpact, setDeleteImpact] = useState<{
    testRuns: number
    roleBindings: number
    roleNames: string[]
    canDelete: boolean
  } | null>(null)
  const [deleteConfirmText, setDeleteConfirmText] = useState('')
  const [deleteBusy, setDeleteBusy] = useState(false)
  const msg = useMessage()

  const fetchConfigs = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api<ListResponse<LLMConfig>>('/llm-config')
      setConfigs(res.data)
    } catch {
      setConfigs([])
    } finally {
      setLoading(false)
    }
  }, [])

  const fetchBindings = useCallback(async () => {
    try {
      const res = await api<ListResponse<LLMRoleBinding>>('/llm-role-binding')
      setBindings(res.data)
    } catch {
      setBindings([])
    }
  }, [])

  useEffect(() => { fetchConfigs(); fetchBindings() }, [fetchConfigs, fetchBindings])

  const chatConfigs = configs.filter((c) => c.configType === 'chat')
  const embeddingConfigs = configs.filter((c) => c.configType === 'embedding')
  const activeChat = chatConfigs.find((c) => c.isActive)
  const activeEmbedding = embeddingConfigs.find((c) => c.isActive)

  const handleActivate = async (id: string) => {
    try {
      await api(`/llm-config/${id}/activate`, { method: 'POST' })
      msg.success(t('model_activated'))
      fetchConfigs()
    } catch {
      msg.error(t('activate_failed'))
    }
  }

  const handleDelete = async (id: string) => {
    const all = [...(chatConfigs || []), ...(embeddingConfigs || [])]
    const cfg = all.find((c) => c.id === id)
    // Reuse the global display rule (alias if set, else vendor/modelName) so
    // the delete dialog title matches every other surface that names a config.
    const label = llmDisplay(cfg) || id
    setDeleteTarget({ id, label })
    setDeleteImpact(null)
    setDeleteConfirmText('')
    setDeleteOpen(true)
    try {
      const impact = await api<{ testRuns: number; roleBindings: number; roleNames: string[]; canDelete: boolean }>(
        `/llm-config/${id}/impact`,
      )
      setDeleteImpact(impact)
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('load_impact_failed'))
      setDeleteOpen(false)
    }
  }

  const submitDelete = async () => {
    if (!deleteTarget) return
    if (deleteConfirmText.trim() !== 'DELETE') {
      msg.error(t('enter_delete_confirm'))
      return
    }
    setDeleteBusy(true)
    try {
      await api(`/llm-config/${deleteTarget.id}`, { method: 'DELETE' })
      msg.success(t('deleted'))
      setDeleteOpen(false)
      setDeleteTarget(null)
      fetchConfigs()
      fetchBindings()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('delete_failed'))
    } finally {
      setDeleteBusy(false)
    }
  }

  const handleEdit = (config: LLMConfig) => setEditConfig(config)
  // Close handler shared by both config modals. Must clear BOTH the open flags
  // and the edit target: a modal opened via the "new" button has
  // chatModalOpen/embeddingModalOpen=true (editConfig=null), so clearing only
  // editConfig left the modal open — the X / backdrop / cancel did nothing.
  const closeEditModal = () => { setEditConfig(null); setChatModalOpen(false); setEmbeddingModalOpen(false) }
  const handleConfigsChanged = () => { fetchConfigs(); fetchBindings() }

  return (
    <MotionFade className="space-y-6">
      <div className={industrial ? 'border-b-2 border-ink pb-4' : ''}>
        {industrial ? (
          <>
            <div className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">// LLM CONFIG</div>
            <p className="mt-1 font-mono text-[11px] tracking-[0.04em] text-ink-muted">{t('page_subtitle')}</p>
          </>
        ) : (
          <>
            <h1 className="text-xl font-semibold">{t('page_title')}</h1>
            <p className="mt-1 text-sm text-gray-500">{t('page_subtitle')}</p>
          </>
        )}
      </div>

      <RoleBindingSection configs={configs} bindings={bindings} onBindingChange={fetchBindings} />

      {/* Chat Model Config */}
      <Card
        title={t('chat_models')}
        titlePrefix="Chat"
        extra={
          <Button size="sm" onClick={() => { setEditConfig(null); setChatModalOpen(true) }}>
            <Plus className="h-3 w-3" /> {t('new_config')}
          </Button>
        }
      >
        {activeChat && (
          <div className={`mb-4 p-3 ${industrial ? 'border border-success bg-white' : 'rounded-lg border border-success/30 bg-success/5'}`}>
            <div className={`mb-1.5 font-medium text-success ${
              industrial ? 'font-mono text-[10px] uppercase tracking-[0.18em]' : 'text-xs'
            }`}>{industrial ? `// ${t('current_active')}` : t('current_active')}</div>
            <div className="flex flex-wrap items-center gap-3">
              <span className="inline-block h-2 w-2 rounded-full bg-success" />
              <div className="flex flex-col">
                <span className="text-sm font-medium text-ink">{llmDisplay(activeChat)}</span>
                {llmSubtitle(activeChat) && (
                  <span className="font-mono text-[11px] text-ink-ghost">{llmSubtitle(activeChat)}</span>
                )}
              </div>
              {activeChat.isThinking && (
                <span className="rounded border border-border bg-canvas-muted px-1.5 py-0.5 text-[10px] font-medium text-ink-muted">Thinking</span>
              )}
              {activeChat.isToolCall && (
                <span className="rounded border border-border bg-canvas-muted px-1.5 py-0.5 text-[10px] font-medium text-ink-muted">Tool Call</span>
              )}
              <span className="ml-auto truncate font-mono text-[11px] text-ink-ghost">{activeChat.baseUrl}</span>
            </div>
          </div>
        )}

        <DataLoader loading={loading} message={t('loading')} minHeight={120}>
          {chatConfigs.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-400">{t('no_chat_configs')}</div>
          ) : (
            <div className="rounded-lg border border-border-light overflow-hidden">
              {chatConfigs.map((c) => (
                <ConfigRow key={c.id} config={c} onActivate={() => handleActivate(c.id)} onDelete={() => handleDelete(c.id)} onEdit={() => handleEdit(c)} />
              ))}
            </div>
          )}
        </DataLoader>
      </Card>

      {/* Embedding Model Config */}
      <Card
        title={t('emb_models')}
        titlePrefix="Embedding"
        extra={
          <Button size="sm" onClick={() => { setEditConfig(null); setEmbeddingModalOpen(true) }}>
            <Plus className="h-3 w-3" /> {t('new_config')}
          </Button>
        }
      >
        {activeEmbedding && (
          <div className={`mb-4 p-3 ${industrial ? 'border border-success bg-white' : 'rounded-lg border border-success/30 bg-success/5'}`}>
            <div className={`mb-1.5 font-medium text-success ${
              industrial ? 'font-mono text-[10px] uppercase tracking-[0.18em]' : 'text-xs'
            }`}>{industrial ? `// ${t('current_active')}` : t('current_active')}</div>
            <div className="flex flex-wrap items-center gap-3">
              <span className="inline-block h-2 w-2 rounded-full bg-success" />
              <div className="flex flex-col">
                <span className="text-sm font-medium text-ink">{llmDisplay(activeEmbedding)}</span>
                {llmSubtitle(activeEmbedding) && (
                  <span className="font-mono text-[11px] text-ink-ghost">{llmSubtitle(activeEmbedding)}</span>
                )}
              </div>
              {activeEmbedding.vectorDim && (
                <span className="rounded border border-border bg-canvas-muted px-1.5 py-0.5 text-[10px] text-ink-muted">dim:{activeEmbedding.vectorDim}</span>
              )}
              <span className="ml-auto truncate font-mono text-[11px] text-ink-ghost">{activeEmbedding.baseUrl}</span>
            </div>
          </div>
        )}

        <DataLoader loading={loading} message={t('loading')} minHeight={120}>
          {embeddingConfigs.length === 0 ? (
            <div className="py-8 text-center text-sm text-gray-400">{t('no_emb_configs')}</div>
          ) : (
            <div className="rounded-lg border border-border-light overflow-hidden">
              {embeddingConfigs.map((c) => (
                <ConfigRow key={c.id} config={c} onActivate={() => handleActivate(c.id)} onDelete={() => handleDelete(c.id)} onEdit={() => handleEdit(c)} />
              ))}
            </div>
          )}
        </DataLoader>
      </Card>

      <ChatConfigModal open={chatModalOpen || editConfig?.configType === 'chat'} onClose={closeEditModal} onSaved={handleConfigsChanged} editConfig={editConfig?.configType === 'chat' ? editConfig : undefined} />
      <EmbeddingConfigModal open={embeddingModalOpen || editConfig?.configType === 'embedding'} onClose={closeEditModal} onSaved={handleConfigsChanged} editConfig={editConfig?.configType === 'embedding' ? editConfig : undefined} />

      <Modal
        open={deleteOpen}
        onClose={() => !deleteBusy && setDeleteOpen(false)}
        title={t('delete_config_title', { label: deleteTarget?.label || '' })}
        width="520px"
      >
        <div className="space-y-4">
          <div className="flex items-start gap-2 rounded-md border border-danger/30 bg-danger/5 p-3">
            <AlertTriangle size={16} className="mt-0.5 flex-shrink-0 text-danger" aria-hidden="true" />
            <div className="text-sm text-ink leading-relaxed">
              {t('delete_warning')}
            </div>
          </div>

          {deleteImpact === null ? (
            <div className="text-sm text-ink-muted">{t('loading_impact')}</div>
          ) : (
            <>
              <div className="grid grid-cols-1 gap-2 text-sm">
                <div className="flex items-center justify-between rounded-md border border-border-light bg-white px-3 py-1.5">
                  <span className="text-ink-muted">
                    {t('test_runs')}
                    <span className="ml-1.5 text-[11px] text-ink-ghost">{t('snapshot_kept')}</span>
                  </span>
                  <span className={`font-mono tabular-nums ${deleteImpact.testRuns > 0 ? 'text-ink' : 'text-ink-ghost'}`}>
                    {deleteImpact.testRuns}
                  </span>
                </div>
                <div className="flex items-center justify-between rounded-md border border-border-light bg-white px-3 py-1.5">
                  <span className="text-ink-muted">
                    {t('role_bindings')}
                    <span className="ml-1.5 text-[11px] text-ink-ghost">{t('cascade_unload')}</span>
                  </span>
                  <span className={`font-mono tabular-nums ${deleteImpact.roleBindings > 0 ? 'text-ink' : 'text-ink-ghost'}`}>
                    {deleteImpact.roleBindings}
                  </span>
                </div>
              </div>

              {deleteImpact.testRuns > 0 && (
                <div className="rounded-md border border-border-light bg-canvas-alt p-3 text-xs leading-relaxed text-ink-muted">
                  <span className="font-medium text-ink">{t('note')}：</span>
                  {t('test_runs_note')}
                </div>
              )}

              {deleteImpact.roleNames.length > 0 && (
                <div className="rounded-md border border-warning/30 bg-warning/5 p-3 text-xs text-ink-muted">
                  <span className="font-medium text-ink">{t('roles_to_unload')}：</span>
                  <ul className="mt-1 list-disc pl-4">
                    {deleteImpact.roleNames.map((n) => (
                      <li key={n} className="font-mono">{n}</li>
                    ))}
                  </ul>
                </div>
              )}

              <div>
                <label className="mb-1.5 block text-sm font-medium text-ink">
                  {t('type_delete_confirm')} <code className="rounded bg-canvas-alt px-1 font-mono text-danger">DELETE</code>：
                </label>
                <input
                  value={deleteConfirmText}
                  onChange={(e) => setDeleteConfirmText(e.target.value)}
                  placeholder="DELETE"
                  autoComplete="off"
                  spellCheck={false}
                  className="w-full rounded-md border border-border bg-white px-3 py-2 font-mono text-sm text-ink outline-none placeholder:text-ink-ghost focus:border-danger focus:ring-1 focus:ring-danger/20"
                />
              </div>
            </>
          )}

          <div className="flex justify-end gap-2 border-t border-border-light pt-3">
            <Button variant="ghost" onClick={() => setDeleteOpen(false)} disabled={deleteBusy}>
              {t('cancel')}
            </Button>
            <button
              type="button"
              onClick={submitDelete}
              disabled={deleteBusy || !deleteImpact?.canDelete || deleteConfirmText.trim() !== 'DELETE'}
              className="inline-flex h-9 items-center gap-1.5 rounded-md border border-danger bg-danger px-3 text-sm font-medium text-white outline-none hover:bg-danger/90 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {deleteBusy ? t('deleting') : t('delete')}
            </button>
          </div>
        </div>
      </Modal>
    </MotionFade>
  )
}
