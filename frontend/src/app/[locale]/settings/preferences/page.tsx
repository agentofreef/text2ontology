'use client'

// SaaS-level account preferences: UI style toggle, language, account info,
// password change. Lives under the SYSTEM secondary menu — the project's
// design avoids a default global top header, so anything that previously felt
// like it belonged in a top bar (theme/locale/account) collects here.

import { useState } from 'react'
import { useTranslations } from 'next-intl'
import { useAuth } from '@/lib/auth'
import { useStyleMode } from '@/lib/style-mode'
import { useMessage } from '@/lib/message'
import { LocaleSwitcher } from '@/components/LocaleSwitcher'
import { StyleModeToggle } from '@/components/StyleModeToggle'
import { getApiBase } from '@/lib/api'

export default function PreferencesPage() {
  const t = useTranslations('settings.preferences')
  const { user } = useAuth()
  const { mode } = useStyleMode()
  const industrial = mode === 'industrial'
  const msg = useMessage()

  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    if (next.length < 6) {
      setError(t('min_length_error'))
      return
    }
    if (next !== confirm) {
      setError(t('mismatch_error'))
      return
    }
    if (next === cur) {
      setError(t('same_as_current_error'))
      return
    }
    setSubmitting(true)
    try {
      const token =
        typeof window !== 'undefined'
          ? localStorage.getItem('lakehouse2ontology_token')
          : null
      const res = await fetch(`${getApiBase()}/auth/change-password`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify({ currentPassword: cur, newPassword: next }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok || !data.success) {
        setError(data.error || t('unknown_error'))
        return
      }
      setCur('')
      setNext('')
      setConfirm('')
      msg.success(t('success_msg'))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('unknown_error'))
    } finally {
      setSubmitting(false)
    }
  }

  // Section wrapper — adapts to industrial vs classic
  const sectionCls = industrial
    ? 'border border-ink bg-white p-6'
    : 'rounded-lg border border-border bg-white p-6 shadow-sm'
  const inputCls = industrial
    ? 'w-full border border-border bg-white px-3 py-2.5 text-sm text-ink outline-none placeholder:text-ink-ghost transition-colors duration-150 focus:border-ink'
    : 'w-full rounded-md border border-border bg-canvas px-3 py-2.5 text-sm text-ink outline-none placeholder:text-ink-ghost transition-colors duration-150 focus:border-ink focus:ring-1 focus:ring-ink/10'
  const labelCls = industrial
    ? 'block font-mono text-[10px] uppercase tracking-[0.18em] text-ink-muted mb-2'
    : 'block text-sm font-medium text-ink mb-1.5'
  const submitCls = industrial
    ? 'inline-flex items-center gap-2 bg-ink px-5 py-2.5 text-sm font-medium tracking-wide text-white transition-opacity duration-150 hover:opacity-80 disabled:cursor-not-allowed disabled:opacity-30'
    : 'inline-flex items-center gap-2 rounded-md bg-ink px-5 py-2.5 text-sm font-medium text-white transition-colors duration-150 hover:bg-ink/90 disabled:cursor-not-allowed disabled:opacity-50'

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      {/* Page header */}
      <div>
        {industrial && (
          <div className="mb-2 font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
            // SYSTEM / PREFERENCES
          </div>
        )}
        <h1 className={industrial ? 'text-2xl font-bold tracking-tight text-ink' : 'text-2xl font-semibold text-ink'}>
          {t('page_title')}
        </h1>
        {industrial && <div className="mt-3 h-[2px] w-10 bg-ink" />}
        <p className={industrial ? 'mt-3 text-sm text-ink-muted' : 'mt-1 text-sm text-ink-muted'}>
          {t('page_subtitle')}
        </p>
      </div>

      {/* Section: UI Style */}
      <section className={sectionCls}>
        <div className="mb-4">
          <h2 className={industrial ? 'font-mono text-[11px] tracking-[0.22em] text-ink-ghost' : 'text-sm font-semibold uppercase tracking-wider text-ink-muted'}>
            {industrial ? `// ${t('section_style').toUpperCase()}` : t('section_style')}
          </h2>
          <p className="mt-2 text-xs text-ink-muted">{t('section_style_hint')}</p>
        </div>
        <StyleModeToggle />
      </section>

      {/* Section: Language */}
      <section className={sectionCls}>
        <div className="mb-4">
          <h2 className={industrial ? 'font-mono text-[11px] tracking-[0.22em] text-ink-ghost' : 'text-sm font-semibold uppercase tracking-wider text-ink-muted'}>
            {industrial ? `// ${t('section_language').toUpperCase()}` : t('section_language')}
          </h2>
          <p className="mt-2 text-xs text-ink-muted">{t('section_language_hint')}</p>
        </div>
        <LocaleSwitcher />
      </section>

      {/* Section: Account */}
      <section className={sectionCls}>
        <div className="mb-4">
          <h2 className={industrial ? 'font-mono text-[11px] tracking-[0.22em] text-ink-ghost' : 'text-sm font-semibold uppercase tracking-wider text-ink-muted'}>
            {industrial ? `// ${t('section_account').toUpperCase()}` : t('section_account')}
          </h2>
        </div>
        <dl className="grid grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-3">
          <div>
            <dt className={industrial ? 'font-mono text-[10px] tracking-[0.18em] text-ink-ghost' : 'text-xs text-ink-ghost'}>
              {t('account_username_label')}
            </dt>
            <dd className="mt-1 text-sm text-ink">{user?.username ?? '—'}</dd>
          </div>
          <div>
            <dt className={industrial ? 'font-mono text-[10px] tracking-[0.18em] text-ink-ghost' : 'text-xs text-ink-ghost'}>
              {t('account_display_name_label')}
            </dt>
            <dd className="mt-1 text-sm text-ink">{user?.displayName ?? '—'}</dd>
          </div>
          <div>
            <dt className={industrial ? 'font-mono text-[10px] tracking-[0.18em] text-ink-ghost' : 'text-xs text-ink-ghost'}>
              {t('account_role_label')}
            </dt>
            <dd className="mt-1 text-sm text-ink">{user?.role ?? '—'}</dd>
          </div>
        </dl>
      </section>

      {/* Section: Change Password */}
      <section className={sectionCls}>
        <div className="mb-4">
          <h2 className={industrial ? 'font-mono text-[11px] tracking-[0.22em] text-ink-ghost' : 'text-sm font-semibold uppercase tracking-wider text-ink-muted'}>
            {industrial ? `// ${t('section_password').toUpperCase()}` : t('section_password')}
          </h2>
        </div>
        <form onSubmit={onSubmit} className="space-y-4">
          <div>
            <label className={labelCls}>{t('current_password_label')}</label>
            <input
              type="password"
              value={cur}
              onChange={(e) => setCur(e.target.value)}
              className={inputCls}
              autoComplete="current-password"
            />
          </div>
          <div>
            <label className={labelCls}>{t('new_password_label')}</label>
            <input
              type="password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              className={inputCls}
              autoComplete="new-password"
            />
          </div>
          <div>
            <label className={labelCls}>{t('confirm_password_label')}</label>
            <input
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              className={inputCls}
              autoComplete="new-password"
            />
          </div>

          {error && (
            <div className={industrial ? 'border border-danger/40 bg-danger/5 px-3 py-2.5' : 'rounded-md border border-danger/20 bg-danger/5 px-3 py-2.5'}>
              <span className="text-sm text-danger">{error}</span>
            </div>
          )}

          <button
            type="submit"
            disabled={submitting || !cur || !next || !confirm}
            className={submitCls}
          >
            <span>{submitting ? t('submitting') : t('submit_btn')}</span>
            {industrial && <span className="font-mono text-[12px]">→</span>}
          </button>
        </form>
      </section>
    </div>
  )
}
