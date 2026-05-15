'use client'

// Polls /api/jobs at an adaptive interval for the global Tasks Drawer:
//   - 2s   while a job is queued / running (user wants live progress)
//   - 30s  when only recently-finished jobs are showing
//   - 60s  when there is nothing recent at all (just sniffing for new work)
// Pauses entirely while the document is hidden so a backgrounded tab does
// not keep hitting the API.
//
// Backend contract: GET /api/jobs?projectId=<uuid>&status=running,recent
// returns { jobs: Job[] }, ordered created_at DESC.

import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from './api'
import { tStatic } from './i18nLite'

export type JobStatus =
  | 'queued'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'cancelled'

export type JobKind =
  | 'file_upload'
  | 'postgres_sync'
  | 'pbit_ingest'
  | 'wizard_confirm'

export interface Job {
  id: string
  dataSourceId?: string
  projectId: string
  kind: JobKind
  status: JobStatus
  phase?: string
  percent: number
  rowsDone: number
  rowsTotal: number
  bytesDone: number
  message?: string
  workerId?: string
  heartbeatAt?: string
  startedAt?: string
  completedAt?: string
  error?: string
  retryCount: number
  cancelRequested: boolean
  createdAt: string
}

export interface UseJobsResult {
  jobs: Job[]
  runningCount: number
  refresh: () => Promise<void>
  cancel: (jobId: string) => Promise<void>
  loading: boolean
  error: string | null
}

const POLL_FAST_MS = 2000   // a job is running or queued
const POLL_SLOW_MS = 30000  // only recently-finished jobs visible
const POLL_IDLE_MS = 60000  // nothing recent — just sniffing for new work

export function useJobs(projectId: string | null | undefined): UseJobsResult {
  const [jobs, setJobs] = useState<Job[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Latest projectId in a ref so the polling timer can read the current value
  // without restarting on every render.
  const projectIdRef = useRef(projectId ?? null)
  projectIdRef.current = projectId ?? null

  const fetchOnce = useCallback(async () => {
    const pid = projectIdRef.current
    if (!pid) {
      setJobs([])
      return
    }
    try {
      const res = await api<{ jobs: Job[] }>(
        `/jobs?projectId=${encodeURIComponent(pid)}&status=running,recent&limit=50`,
      )
      setJobs(res.jobs ?? [])
      setError(null)
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      setError(msg)
    }
  }, [])

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      await fetchOnce()
    } finally {
      setLoading(false)
    }
  }, [fetchOnce])

  const cancel = useCallback(
    async (jobId: string) => {
      try {
        await api(`/jobs/${jobId}/cancel`, { method: 'POST' })
        await fetchOnce()
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e)
        setError(msg)
      }
    },
    [fetchOnce],
  )

  const runningCount = jobs.filter(
    (j) => j.status === 'queued' || j.status === 'running',
  ).length

  // Adaptive interval: fast while a job is live, slow when only recent
  // history is visible, idle when there is nothing recent at all.
  const pollMs =
    runningCount > 0 ? POLL_FAST_MS : jobs.length > 0 ? POLL_SLOW_MS : POLL_IDLE_MS

  // Polling lifecycle. Restarted when projectId or pollMs changes.
  useEffect(() => {
    if (!projectId) return
    let timer: ReturnType<typeof setInterval> | null = null

    const tick = () => {
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
        return
      }
      void fetchOnce()
    }

    void fetchOnce()
    timer = setInterval(tick, pollMs)

    const onVisibility = () => {
      if (document.visibilityState === 'visible') {
        void fetchOnce()
      }
    }
    document.addEventListener('visibilitychange', onVisibility)

    return () => {
      if (timer) clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [projectId, pollMs, fetchOnce])

  return { jobs, runningCount, refresh, cancel, loading, error }
}

// Helpers for the drawer UI — formatting is centralised here so JobCard
// doesn't reinvent date math per render.

export function formatRelative(ts: string | undefined): string {
  if (!ts) return ''
  const t = new Date(ts).getTime()
  const diffSec = Math.max(0, Math.round((Date.now() - t) / 1000))
  if (diffSec < 60) return tStatic('jobs.seconds_ago', { count: diffSec })
  if (diffSec < 3600) return tStatic('jobs.minutes_ago', { count: Math.round(diffSec / 60) })
  if (diffSec < 86400) return tStatic('jobs.hours_ago', { count: Math.round(diffSec / 3600) })
  return tStatic('jobs.days_ago', { count: Math.round(diffSec / 86400) })
}

export function formatDuration(startISO: string | undefined, endISO?: string): string {
  if (!startISO) return ''
  const start = new Date(startISO).getTime()
  const end = endISO ? new Date(endISO).getTime() : Date.now()
  const sec = Math.max(0, Math.round((end - start) / 1000))
  if (sec < 60) return `${sec}s`
  const m = Math.floor(sec / 60)
  const s = sec % 60
  if (m < 60) return `${m}m ${s}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

export function kindLabel(k: JobKind): string {
  switch (k) {
    case 'file_upload':
      return tStatic('jobs.kind_file_upload')
    case 'postgres_sync':
      return 'Postgres Sync'
    case 'pbit_ingest':
      return tStatic('jobs.kind_pbit_ingest')
    case 'wizard_confirm':
      return tStatic('jobs.kind_wizard_confirm')
  }
}

export function statusLabel(s: JobStatus): string {
  switch (s) {
    case 'queued':
      return tStatic('jobs.status_queued')
    case 'running':
      return tStatic('jobs.status_running')
    case 'succeeded':
      return tStatic('jobs.status_succeeded')
    case 'failed':
      return tStatic('jobs.status_failed')
    case 'cancelled':
      return tStatic('jobs.status_cancelled')
  }
}
