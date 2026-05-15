'use client'

import { motion, useReducedMotion } from 'motion/react'
import {
  Job,
  formatDuration,
  formatRelative,
  kindLabel,
  statusLabel,
} from '@/lib/jobs'
import { ProgressBar } from './ProgressBar'
import { useTranslations } from 'next-intl'

interface JobCardProps {
  job: Job
  onCancel: (id: string) => void
}

const dotClass: Record<Job['status'], string> = {
  queued: 'bg-ink-ghost',
  running: 'bg-success',
  succeeded: 'bg-ink-ghost',
  failed: 'bg-danger',
  cancelled: 'bg-ink-ghost',
}

export function JobCard({ job, onCancel }: JobCardProps) {
  const reduced = useReducedMotion()
  const t = useTranslations('jobs')
  const isActive = job.status === 'queued' || job.status === 'running'
  const isFailed = job.status === 'failed'
  const variant: 'running' | 'succeeded' | 'failed' | 'cancelled' =
    job.status === 'queued' || job.status === 'running'
      ? 'running'
      : (job.status as 'succeeded' | 'failed' | 'cancelled')

  // Title line: kind + label/source identifier from message or phase fallback
  const subtitle = job.message || job.phase || ''
  const rowsLine =
    job.rowsTotal > 0
      ? t('rows_progress', { done: job.rowsDone.toLocaleString(), total: job.rowsTotal.toLocaleString() })
      : job.rowsDone > 0
        ? t('rows_done', { done: job.rowsDone.toLocaleString() })
        : ''

  return (
    <div className="px-4 py-3">
      {/* Header row: status dot + status label + (right) created/completed time */}
      <div className="mb-1.5 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span
            className={`inline-block h-1.5 w-1.5 rounded-full ${dotClass[job.status]}`}
            aria-hidden
          />
          <span className="text-[11px] font-medium uppercase tracking-wide text-ink-muted">
            {statusLabel(job.status)}
          </span>
        </div>
        <span className="text-[11px] font-mono tabular-nums text-ink-ghost">
          {!isActive && job.completedAt
            ? formatRelative(job.completedAt)
            : formatRelative(job.createdAt)}
        </span>
      </div>

      {/* Title: kind */}
      <div className="text-sm font-medium text-ink">{kindLabel(job.kind)}</div>

      {/* Subtitle: phase / message */}
      {subtitle && (
        <div className="mt-0.5 text-xs text-ink-muted line-clamp-1">
          {subtitle}
        </div>
      )}

      {/* Progress (only for active jobs OR succeeded with row counts) */}
      {(isActive || (job.status === 'succeeded' && job.rowsTotal > 0)) && (
        <div className="mt-2.5 flex items-center gap-3">
          <div className="flex-1">
            <ProgressBar percent={isActive ? job.percent : 100} variant={variant} />
          </div>
          <span className="w-10 text-right text-[11px] font-mono tabular-nums text-ink-muted">
            {(isActive ? job.percent : 100).toString().padStart(2, ' ')}%
          </span>
        </div>
      )}

      {/* Stats line: rows + elapsed */}
      {(rowsLine || isActive) && (
        <div className="mt-1.5 flex items-center justify-between text-[11px] font-mono tabular-nums text-ink-ghost">
          <span>{rowsLine}</span>
          <span>
            {isActive
              ? t('elapsed', { duration: formatDuration(job.startedAt) })
              : job.startedAt && job.completedAt
                ? t('took', { duration: formatDuration(job.startedAt, job.completedAt) })
                : ''}
          </span>
        </div>
      )}

      {/* Failure message */}
      {isFailed && job.error && (
        <div className="mt-2 rounded-md border border-danger/30 bg-danger/5 px-2 py-1.5 text-xs text-danger line-clamp-2">
          {job.error}
        </div>
      )}

      {/* Cancel button (only running) */}
      {isActive && !job.cancelRequested && (
        <div className="mt-2">
          <motion.button
            whileHover={reduced ? undefined : { scale: 1.02 }}
            whileTap={reduced ? undefined : { scale: 0.97 }}
            transition={{ type: 'spring', stiffness: 500, damping: 30 }}
            onClick={() => onCancel(job.id)}
            className="rounded-md px-2 py-0.5 text-[11px] text-ink-muted hover:text-ink hover:bg-canvas-alt"
          >
            {t('cancel')}
          </motion.button>
        </div>
      )}
      {job.cancelRequested && isActive && (
        <div className="mt-2 text-[11px] text-ink-ghost">
          {t('cancel_requested')}
        </div>
      )}
    </div>
  )
}
