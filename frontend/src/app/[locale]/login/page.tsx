'use client'

import { useState, useEffect } from 'react'
import { useTranslations } from 'next-intl'
import { useRouter } from '@/i18n/navigation'
import { useAuth } from '@/lib/auth'
import { getApiBase } from '@/lib/api'
import { MotionFade } from '@/lib/motion'
import { LocaleSwitcher } from '@/components/LocaleSwitcher'

// Industrial login: two-pane on desktop (left poster, right form), single-pane
// on mobile. Sharp corners, no shadows, mono trace marks — same visual system
// as docs/cover.svg.
export default function LoginPageMinimal() {
  const t = useTranslations('login')
  const [mode, setMode] = useState<'signin' | 'signup'>('signin')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [registrationAllowed, setRegistrationAllowed] = useState(false)
  const { login, register } = useAuth()
  const router = useRouter()

  // Whether the sign-up entry shows is server-controlled (admin toggle). The
  // backend re-checks on every register call, so this is purely a UI hint.
  useEffect(() => {
    let cancelled = false
    fetch(`${getApiBase()}/auth/registration-status`)
      .then((r) => r.json())
      .then((d) => { if (!cancelled) setRegistrationAllowed(!!d.allowed) })
      .catch(() => { /* keep sign-up hidden on failure */ })
    return () => { cancelled = true }
  }, [])

  const switchMode = (next: 'signin' | 'signup') => {
    setMode(next)
    setError('')
    setPassword('')
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    if (mode === 'signup' && password.length < 6) {
      setError(t('password_too_short'))
      return
    }

    setLoading(true)
    // Registration is intentionally minimal: username + password only. The
    // display name defaults to the username server-side.
    const result = mode === 'signin'
      ? await login(username, password)
      : await register(username, password, '')
    setLoading(false)

    if (result.success) {
      router.push('/ontology/lakehouse-agent?mode=lakehouse')
    } else {
      setError(result.error || (mode === 'signin' ? t('failed_default') : t('register_failed')))
    }
  }

  const submitDisabled = loading || !username || !password

  return (
    <div className="grid min-h-screen grid-cols-1 bg-canvas lg:grid-cols-[1.1fr_1fr]">
      {/* ────────── Left poster pane (desktop only) ────────── */}
      <aside className="relative hidden flex-col justify-between border-r border-ink bg-white p-12 lg:flex">
        {/* Top trace */}
        <div className="flex items-start justify-between">
          <div className="font-mono text-[11px] tracking-[0.18em] text-ink-ghost">
            // OPEN-SOURCE COMMUNITY EDITION&nbsp;&nbsp;/&nbsp;&nbsp;v0
          </div>
        </div>

        {/* Center: large logo + brand block */}
        <div className="flex flex-col gap-8">
          {/* Inline scaled logo (matches docs/cover.svg) */}
          <svg viewBox="0 0 32 32" fill="none" className="h-32 w-32">
            {/* Ontology block (back, soft neutral gray) */}
            <rect x="3" y="3" width="18" height="18" fill="#D4D4D4" />
            <line x1="9" y1="9" x2="16" y2="9" stroke="#171717" strokeWidth="0.9" />
            <line x1="9" y1="9" x2="12" y2="16" stroke="#171717" strokeWidth="0.9" />
            <line x1="16" y1="9" x2="12" y2="16" stroke="#171717" strokeWidth="0.9" />
            <circle cx="9" cy="9" r="1.6" fill="#171717" />
            <circle cx="16" cy="9" r="1.6" fill="#171717" />
            <circle cx="12" cy="16" r="1.6" fill="#171717" />
            {/* Data block (front, near-black) */}
            <rect x="13" y="13" width="16" height="16" fill="#171717" />
            <line x1="16" y1="18" x2="26" y2="18" stroke="#D4D4D4" strokeWidth="0.9" />
            <line x1="16" y1="22" x2="26" y2="22" stroke="#D4D4D4" strokeWidth="0.9" />
            <line x1="16" y1="26" x2="26" y2="26" stroke="#D4D4D4" strokeWidth="0.9" />
          </svg>

          <div>
            <div className="mb-3 font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              A REFERENCE IMPLEMENTATION
            </div>
            <h1 className="text-5xl font-bold tracking-tight text-ink">TEXT2ONTOLOGY</h1>
            <div className="mt-5 h-[2px] w-12 bg-ink" />
            <p className="mt-5 text-lg font-medium text-ink">Ontology before query.</p>
            <p className="mt-1 text-sm text-ink-muted">Build the meaning before the SQL.</p>
          </div>
        </div>

        {/* Bottom: pipeline trace */}
        <div className="space-y-2 font-mono text-[11px] leading-relaxed tracking-[0.18em]">
          <div className="flex flex-wrap gap-x-3 text-ink">
            <span>QUESTION</span>
            <span className="text-ink-ghost">→</span>
            <span>RECALL</span>
            <span className="text-ink-ghost">→</span>
            <span>INTENT</span>
            <span className="text-ink-ghost">→</span>
            <span>POSTGRES&nbsp;SQL</span>
            <span className="text-ink-ghost">→</span>
            <span>ANSWER</span>
          </div>
          <div className="text-ink-ghost">// 7 GO SERVICES · NEXT.JS 16 · POSTGRES + PGVECTOR</div>
        </div>
      </aside>

      {/* ────────── Right form pane ────────── */}
      <main className="relative flex items-center justify-center px-6 py-10 sm:px-10">
        {/* Locale switcher top-right */}
        <div className="absolute right-4 top-4">
          <LocaleSwitcher />
        </div>

        {/* Mobile-only mini brand top-left */}
        <div className="absolute left-6 top-6 flex items-center gap-2 lg:hidden">
          <svg viewBox="0 0 32 32" fill="none" className="h-5 w-5">
            <rect x="3" y="3" width="18" height="18" fill="#D4D4D4" />
            <circle cx="9" cy="9" r="1.6" fill="#171717" />
            <circle cx="16" cy="9" r="1.6" fill="#171717" />
            <circle cx="12" cy="16" r="1.6" fill="#171717" />
            <line x1="9" y1="9" x2="16" y2="9" stroke="#171717" strokeWidth="0.9" />
            <line x1="9" y1="9" x2="12" y2="16" stroke="#171717" strokeWidth="0.9" />
            <line x1="16" y1="9" x2="12" y2="16" stroke="#171717" strokeWidth="0.9" />
            <rect x="13" y="13" width="16" height="16" fill="#171717" />
          </svg>
          <span className="text-sm font-semibold text-ink">TEXT2ONTOLOGY</span>
        </div>

        <MotionFade className="w-full max-w-sm">
          {/* Header */}
          <div className="mb-8">
            <div className="mb-3 font-mono text-[11px] tracking-[0.22em] text-ink-ghost">
              {mode === 'signin' ? '// SIGN IN' : '// SIGN UP'}
            </div>
            <h2 className="text-2xl font-semibold text-ink">
              {mode === 'signin' ? t('page_title') : t('register_title')}
            </h2>
            <div className="mt-3 h-[2px] w-8 bg-ink" />
            <p className="mt-3 text-sm text-ink-muted">
              {mode === 'signin' ? t('subtitle') : t('register_subtitle')}
            </p>
          </div>

          {/* Form */}
          <form onSubmit={handleSubmit} className="space-y-5">
            <div className="space-y-2">
              <label className="block font-mono text-[10px] uppercase tracking-[0.18em] text-ink-muted">
                {t('username_label')}
              </label>
              <input
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder={t('username_placeholder')}
                className="w-full border border-border bg-white px-3 py-2.5 text-sm text-ink outline-none placeholder:text-ink-ghost transition-colors duration-150 focus:border-ink"
                autoFocus
              />
            </div>

            <div className="space-y-2">
              <label className="block font-mono text-[10px] uppercase tracking-[0.18em] text-ink-muted">
                {t('password_label')}
              </label>
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t('password_placeholder')}
                className="w-full border border-border bg-white px-3 py-2.5 text-sm text-ink outline-none placeholder:text-ink-ghost transition-colors duration-150 focus:border-ink"
              />
            </div>

            {error && (
              <div className="border border-danger/40 bg-danger/5 px-3 py-2.5">
                <span className="text-sm text-danger">{error}</span>
              </div>
            )}

            <button
              type="submit"
              disabled={submitDisabled}
              className="group flex w-full items-center justify-between bg-ink px-4 py-3 text-sm font-medium tracking-wide text-white transition-opacity duration-150 hover:opacity-80 disabled:cursor-not-allowed disabled:opacity-30"
            >
              <span>
                {loading
                  ? (mode === 'signin' ? t('submitting') : t('registering'))
                  : (mode === 'signin' ? t('submit') : t('register_submit'))}
              </span>
              <span className="font-mono text-[13px] transition-transform duration-150 group-hover:translate-x-0.5">
                →
              </span>
            </button>
          </form>

          {/* Sign-in / sign-up toggle. The sign-up entry only appears when the
              admin has enabled registration server-side. */}
          {(registrationAllowed || mode === 'signup') && (
            <div className="mt-6">
              <button
                type="button"
                onClick={() => switchMode(mode === 'signin' ? 'signup' : 'signin')}
                className="font-mono text-[11px] tracking-[0.06em] text-ink-muted underline-offset-4 transition-colors duration-150 hover:text-ink hover:underline"
              >
                {mode === 'signin' ? t('no_account') : t('have_account')}
              </button>
            </div>
          )}

        </MotionFade>
      </main>
    </div>
  )
}
