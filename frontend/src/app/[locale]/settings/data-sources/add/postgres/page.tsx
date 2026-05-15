'use client'

import { useTranslations } from 'next-intl'
import { useState } from 'react'
import { useRouter } from '@/i18n/navigation'
import { useProject } from '@/lib/project'
import { Button } from '@/components/ui/Button'
import { ChevronLeft, CheckCircle2, XCircle } from 'lucide-react'
import { getApiBase } from '@/lib/api'

interface FormState {
  label: string
  host: string
  port: string
  database: string
  user: string
  password: string
}

export default function AddPostgresPage() {
  const t = useTranslations('settings.ds.add.postgres')
  const router = useRouter()
  const { currentProject } = useProject()

  const [form, setForm] = useState<FormState>({
    label: '',
    host: '',
    port: '5432',
    database: '',
    user: '',
    password: '',
  })
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; message?: string } | null>(null)
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')

  const getToken = () =>
    typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null

  const handleChange = (field: keyof FormState) =>
    (e: React.ChangeEvent<HTMLInputElement>) => {
      setForm((f) => ({ ...f, [field]: e.target.value }))
      setTestResult(null)
    }

  const handleTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await fetch(`${getApiBase()}/connector/postgres/test-connection`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(getToken() ? { Authorization: `Bearer ${getToken()}` } : {}),
        },
        body: JSON.stringify({
          host: form.host,
          port: parseInt(form.port) || 5432,
          database: form.database,
          user: form.user,
          password: form.password,
        }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()
      setTestResult({ ok: data.ok ?? data.OK ?? false, message: data.message })
    } catch (e) {
      setTestResult({ ok: false, message: e instanceof Error ? e.message : t('conn_failed') })
    } finally {
      setTesting(false)
    }
  }

  const handleCreate = async () => {
    if (!currentProject) return
    if (!form.label || !form.host || !form.database || !form.user) {
      setError(t('required_fields'))
      return
    }
    setCreating(true)
    setError('')
    try {
      const res = await fetch(`${getApiBase()}/connector/postgres/sources`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(getToken() ? { Authorization: `Bearer ${getToken()}` } : {}),
        },
        body: JSON.stringify({
          project_id: currentProject.id,
          label: form.label,
          config_json: {
            host: form.host,
            port: parseInt(form.port) || 5432,
            database: form.database,
            user: form.user,
            password: form.password,
          },
        }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(data.message || data.error || `HTTP ${res.status}`)
      }
      const data = await res.json()
      const id = data.id || data.ID
      router.push(`/settings/data-sources/wizard?id=${id}`)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('create_failed'))
    } finally {
      setCreating(false)
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* Header */}
      <div className="flex items-center gap-3 border-b border-border px-6 py-4">
        <button
          onClick={() => router.push('/settings/data-sources/add')}
          className="flex items-center gap-1 text-xs text-ink-ghost transition-colors duration-150 hover:text-ink"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          {t('back')}
        </button>
        <span className="text-ink-ghost">/</span>
        <span
          className="cursor-pointer text-xs text-ink-ghost transition-colors duration-150 hover:text-ink"
          onClick={() => router.push('/settings/data-sources/add')}
        >
          {t('add_source')}
        </span>
        <span className="text-ink-ghost">/</span>
        <h1 className="text-base font-semibold text-ink">{t('page_title')}</h1>
      </div>

      {/* Form */}
      <div className="flex-1 overflow-y-auto p-6">
        <div className="rounded-lg border border-border bg-white p-6">
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            {/* Label - full width */}
            <div className="md:col-span-2">
              <label className="mb-1.5 block text-xs font-medium text-ink-muted">
                {t('label')} <span className="text-red-500">*</span>
              </label>
              <input
                type="text"
                value={form.label}
                onChange={handleChange('label')}
                placeholder={t('label_placeholder')}
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
              />
            </div>

            {/* Host */}
            <div>
              <label className="mb-1.5 block text-xs font-medium text-ink-muted">
                {t('host')} <span className="text-red-500">*</span>
              </label>
              <input
                type="text"
                value={form.host}
                onChange={handleChange('host')}
                placeholder={t('host_placeholder')}
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
              />
            </div>

            {/* Port */}
            <div>
              <label className="mb-1.5 block text-xs font-medium text-ink-muted">{t('port')}</label>
              <input
                type="number"
                value={form.port}
                onChange={handleChange('port')}
                placeholder="5432"
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
              />
            </div>

            {/* Database */}
            <div>
              <label className="mb-1.5 block text-xs font-medium text-ink-muted">
                {t('database')} <span className="text-red-500">*</span>
              </label>
              <input
                type="text"
                value={form.database}
                onChange={handleChange('database')}
                placeholder="my_database"
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
              />
            </div>

            {/* User */}
            <div>
              <label className="mb-1.5 block text-xs font-medium text-ink-muted">
                {t('user')} <span className="text-red-500">*</span>
              </label>
              <input
                type="text"
                value={form.user}
                onChange={handleChange('user')}
                placeholder="postgres"
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
              />
            </div>

            {/* Password - full width */}
            <div className="md:col-span-2">
              <label className="mb-1.5 block text-xs font-medium text-ink-muted">{t('password')}</label>
              <input
                type="password"
                value={form.password}
                onChange={handleChange('password')}
                placeholder={t('password_placeholder')}
                className="w-full rounded-md border border-border bg-white px-3 py-2 text-sm text-ink placeholder:text-ink-ghost focus:border-ink focus:outline-none transition-colors duration-150"
              />
            </div>
          </div>

          {/* Test result */}
          {testResult && (
            <div className={`mt-4 flex items-center gap-2 rounded-md px-3 py-2 text-sm ${
              testResult.ok
                ? 'border border-green-200 bg-green-50 text-green-700'
                : 'border border-red-200 bg-red-50 text-red-700'
            }`}>
              {testResult.ok
                ? <CheckCircle2 className="h-4 w-4 flex-shrink-0" />
                : <XCircle className="h-4 w-4 flex-shrink-0" />
              }
              {testResult.ok ? t('conn_ok') : (testResult.message || t('conn_failed'))}
            </div>
          )}

          {/* Error */}
          {error && (
            <div className="mt-4 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
              {error}
            </div>
          )}

          {/* Actions */}
          <div className="mt-6 flex items-center gap-3">
            <Button
              variant="default"
              size="sm"
              onClick={handleTest}
              disabled={testing || !form.host || !form.database || !form.user}
            >
              {testing ? t('testing') : t('test_conn')}
            </Button>
            <Button
              variant="primary"
              size="sm"
              onClick={handleCreate}
              disabled={creating || !form.label || !form.host || !form.database || !form.user}
            >
              {creating ? t('creating') : t('create_and_continue')}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
