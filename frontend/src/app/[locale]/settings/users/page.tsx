'use client'

import { useTranslations } from 'next-intl'
import { useState, useEffect, useCallback } from 'react'
import { motion, useReducedMotion } from 'motion/react'
import { useRouter } from '@/i18n/navigation'
import { api } from '@/lib/api'
import { useAuth } from '@/lib/auth'
import { useMessage } from '@/lib/message'
import { useStyleMode } from '@/lib/style-mode'
import { AnimatedButton } from '@/components/ui/AnimatedButton'
import { Users, KeyRound, Trash2, X, ShieldCheck } from 'lucide-react'

// ─── Types ──────────────────────────────────────────────────────────

interface AdminUser {
  id: string
  username: string
  displayName: string
  role: 'user' | 'admin'
  isActive: boolean
  createdAt: string
  projectCount: number
}

function formatDate(s: string): string {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN', {
    year: '2-digit', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  })
}

// ─── Page ───────────────────────────────────────────────────────────

export default function AdminUsersPage() {
  const t = useTranslations('settings.users')
  const industrial = useStyleMode().mode === 'industrial'
  const msg = useMessage()
  const reduce = useReducedMotion()
  const router = useRouter()
  const { user } = useAuth()

  const [users, setUsers] = useState<AdminUser[]>([])
  const [allowRegistration, setAllowRegistration] = useState(false)
  const [loading, setLoading] = useState(true)
  const [busyId, setBusyId] = useState<string | null>(null)

  // Reset-password modal state.
  const [resetTarget, setResetTarget] = useState<AdminUser | null>(null)
  const [newPassword, setNewPassword] = useState('')
  const [resetting, setResetting] = useState(false)

  // Admin-only page. The backend gates every /api/admin/* call regardless, but
  // redirecting non-admins keeps them out of a page that would only 403.
  useEffect(() => {
    if (user && user.role !== 'admin') {
      router.replace('/ontology/lakehouse-agent')
    }
  }, [user, router])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [usersRes, settingsRes] = await Promise.all([
        api<{ data: AdminUser[] }>('/admin/users'),
        api<{ allowRegistration: boolean }>('/admin/settings'),
      ])
      setUsers(usersRes.data || [])
      setAllowRegistration(!!settingsRes.allowRegistration)
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('load_failed'))
    } finally {
      setLoading(false)
    }
  }, [msg, t])

  useEffect(() => { load() }, [load])

  const toggleRegistration = async () => {
    const next = !allowRegistration
    setAllowRegistration(next) // optimistic
    try {
      await api('/admin/settings', { method: 'PUT', body: { allowRegistration: next } })
      msg.success(t('saved'))
    } catch (e) {
      setAllowRegistration(!next) // revert
      msg.error(e instanceof Error ? e.message : t('save_failed'))
    }
  }

  const patchUser = async (u: AdminUser, patch: { role?: string; isActive?: boolean }) => {
    setBusyId(u.id)
    try {
      await api(`/admin/users/${u.id}`, { method: 'PATCH', body: patch })
      msg.success(t('saved'))
      await load()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('save_failed'))
    } finally {
      setBusyId(null)
    }
  }

  const deleteUser = async (u: AdminUser) => {
    if (!confirm(t('delete_confirm', { username: u.username }))) return
    setBusyId(u.id)
    try {
      await api(`/admin/users/${u.id}`, { method: 'DELETE' })
      msg.success(t('deleted'))
      await load()
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('save_failed'))
    } finally {
      setBusyId(null)
    }
  }

  const submitReset = async () => {
    if (!resetTarget) return
    if (newPassword.length < 6) { msg.error(t('new_password_placeholder')); return }
    setResetting(true)
    try {
      await api(`/admin/users/${resetTarget.id}/reset-password`, {
        method: 'POST', body: { newPassword },
      })
      msg.success(t('password_reset_done'))
      setResetTarget(null)
      setNewPassword('')
    } catch (e) {
      msg.error(e instanceof Error ? e.message : t('save_failed'))
    } finally {
      setResetting(false)
    }
  }

  const isSelf = (u: AdminUser) => user?.username === u.username

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      {/* ── Header ─────────────────────────────────────────────────── */}
      <header
        className={`flex h-14 flex-shrink-0 items-center justify-between gap-3 bg-white px-6 ${
          industrial ? 'border-b-2 border-ink' : 'border-b border-border shadow-sm'
        }`}
      >
        <div className="flex min-w-0 items-center gap-3">
          {industrial ? (
            <>
              <span className="font-mono text-[11px] tracking-[0.22em] text-ink-ghost">// USERS</span>
              <span className="font-mono text-[10px] tabular-nums tracking-[0.14em] text-ink-muted">
                {users.length} USERS
              </span>
            </>
          ) : (
            <>
              <div className="inline-flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md border border-border bg-canvas-alt">
                <Users size={14} className="text-ink" aria-hidden="true" />
              </div>
              <div className="min-w-0">
                <h1 className="text-base font-semibold tracking-tight text-ink">{t('page_title')}</h1>
                <p className="truncate text-xs text-ink-muted">{t('page_subtitle')}</p>
              </div>
            </>
          )}
        </div>
        <span className="text-xs text-ink-muted tabular-nums">{t('user_count', { count: users.length })}</span>
      </header>

      {/* ── Scroll body ────────────────────────────────────────────── */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="space-y-6 px-6 py-6">
          {/* ── Registration toggle ─────────────────────────────────── */}
          <section className={`bg-white ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}>
            <div className="flex items-center justify-between gap-4 px-5 py-4">
              <div className="min-w-0">
                <div className="text-sm font-medium text-ink">{t('allow_registration')}</div>
                <div className="mt-0.5 text-xs text-ink-muted">{t('allow_registration_hint')}</div>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={allowRegistration}
                onClick={toggleRegistration}
                className={`relative inline-flex h-6 w-11 flex-shrink-0 items-center rounded-full transition-colors duration-150 ${
                  allowRegistration ? 'bg-ink' : 'bg-border'
                }`}
              >
                <span
                  className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform duration-150 ${
                    allowRegistration ? 'translate-x-6' : 'translate-x-1'
                  }`}
                />
              </button>
            </div>
          </section>

          {/* ── Users table ─────────────────────────────────────────── */}
          <section className={`overflow-hidden bg-white ${industrial ? 'border border-ink' : 'rounded-md border border-border'}`}>
            {loading ? (
              <div className="flex items-center justify-center py-12 text-sm text-ink-muted">{t('loading')}</div>
            ) : users.length === 0 ? (
              <div className="flex items-center justify-center py-12 text-sm text-ink-muted">{t('no_users')}</div>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className={`text-left ${industrial ? 'border-b border-ink' : 'border-b border-border-light'} bg-canvas-alt`}>
                      <th className="px-4 py-2.5 font-medium text-ink-muted">{t('col_username')}</th>
                      <th className="px-4 py-2.5 font-medium text-ink-muted">{t('col_display_name')}</th>
                      <th className="px-4 py-2.5 font-medium text-ink-muted">{t('col_role')}</th>
                      <th className="px-4 py-2.5 font-medium text-ink-muted">{t('col_status')}</th>
                      <th className="px-4 py-2.5 font-medium text-ink-muted">{t('col_created')}</th>
                      <th className="px-4 py-2.5 font-medium text-ink-muted tabular-nums">{t('col_projects')}</th>
                      <th className="px-4 py-2.5 text-right font-medium text-ink-muted">{t('col_actions')}</th>
                    </tr>
                  </thead>
                  <tbody className={industrial ? 'divide-y divide-ink/15' : 'divide-y divide-border-light'}>
                    {users.map((u) => (
                      <tr key={u.id} className={busyId === u.id ? 'opacity-50' : ''}>
                        <td className="px-4 py-2.5">
                          <span className="font-medium text-ink">{u.username}</span>
                          {isSelf(u) && (
                            <span className="ml-2 rounded border border-border bg-canvas-alt px-1.5 py-0.5 text-[10px] text-ink-muted">
                              {t('you_badge')}
                            </span>
                          )}
                        </td>
                        <td className="px-4 py-2.5 text-ink-muted">{u.displayName || '—'}</td>
                        <td className="px-4 py-2.5">
                          <select
                            value={u.role}
                            disabled={busyId === u.id || isSelf(u)}
                            onChange={(e) => patchUser(u, { role: e.target.value })}
                            className="rounded border border-border bg-white px-2 py-1 text-xs text-ink outline-none focus:border-ink disabled:cursor-not-allowed disabled:opacity-50"
                          >
                            <option value="user">{t('role_user')}</option>
                            <option value="admin">{t('role_admin')}</option>
                          </select>
                        </td>
                        <td className="px-4 py-2.5">
                          <button
                            type="button"
                            disabled={busyId === u.id || isSelf(u)}
                            onClick={() => patchUser(u, { isActive: !u.isActive })}
                            className={`inline-flex items-center gap-1 rounded border px-2 py-0.5 text-[11px] transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
                              u.isActive
                                ? 'border-success/40 bg-success/5 text-success hover:border-success'
                                : 'border-border bg-canvas-alt text-ink-ghost hover:border-ink'
                            }`}
                            title={u.isActive ? t('disable') : t('enable')}
                          >
                            {u.isActive ? t('active') : t('disabled')}
                          </button>
                        </td>
                        <td className="px-4 py-2.5 text-ink-ghost tabular-nums">{formatDate(u.createdAt)}</td>
                        <td className="px-4 py-2.5 text-ink-muted tabular-nums">{u.projectCount}</td>
                        <td className="px-4 py-2.5">
                          <div className="flex items-center justify-end gap-1.5">
                            <button
                              type="button"
                              onClick={() => { setResetTarget(u); setNewPassword('') }}
                              className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-white px-2 text-[11px] text-ink-muted outline-none hover:border-ink hover:text-ink"
                              title={t('reset_password')}
                            >
                              <KeyRound size={11} aria-hidden="true" />
                              {t('reset_password')}
                            </button>
                            <button
                              type="button"
                              disabled={busyId === u.id || isSelf(u)}
                              onClick={() => deleteUser(u)}
                              className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-white px-2 text-[11px] text-ink-muted outline-none hover:border-danger hover:text-danger disabled:cursor-not-allowed disabled:opacity-40"
                              title={t('delete')}
                            >
                              <Trash2 size={11} aria-hidden="true" />
                              {t('delete')}
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </div>
      </div>

      {/* ── Reset-password modal ──────────────────────────────────── */}
      {resetTarget && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-ink/20"
          role="dialog"
          aria-modal="true"
          onClick={() => { if (!resetting) setResetTarget(null) }}
        >
          <motion.div
            initial={reduce ? undefined : { scale: 0.98, opacity: 0 }}
            animate={reduce ? undefined : { scale: 1, opacity: 1 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
            className="relative w-[440px] max-w-[calc(100vw-32px)] rounded-md border border-border bg-white shadow-lg"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center justify-between border-b border-border-light bg-canvas-alt px-4 py-2.5">
              <div className="flex items-center gap-2">
                <ShieldCheck size={14} className="text-ink-muted" aria-hidden="true" />
                <span className="text-sm font-semibold text-ink">
                  {t('reset_password_title', { username: resetTarget.username })}
                </span>
              </div>
              <button
                onClick={() => setResetTarget(null)}
                className="rounded p-1 text-ink-ghost outline-none hover:text-ink"
                aria-label={t('cancel')}
              >
                <X className="h-4 w-4" aria-hidden="true" />
              </button>
            </div>
            <div className="space-y-3 p-5">
              <div>
                <label className="mb-1 block text-xs font-medium text-ink-muted">{t('new_password')}</label>
                <input
                  type="password"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  placeholder={t('new_password_placeholder')}
                  className="h-9 w-full rounded-md border border-border bg-white px-2.5 text-sm text-ink outline-none focus:border-ink"
                  autoFocus
                />
              </div>
              <div className="flex justify-end gap-2 pt-1">
                <AnimatedButton variant="secondary" size="sm" onClick={() => setResetTarget(null)} disabled={resetting}>
                  {t('cancel')}
                </AnimatedButton>
                <AnimatedButton variant="primary" size="sm" onClick={submitReset} disabled={resetting || newPassword.length < 6}>
                  <KeyRound size={12} aria-hidden="true" />
                  {t('confirm')}
                </AnimatedButton>
              </div>
            </div>
          </motion.div>
        </div>
      )}
    </div>
  )
}
