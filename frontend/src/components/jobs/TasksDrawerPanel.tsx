'use client'

import { motion, useReducedMotion, AnimatePresence } from 'motion/react'
import { X } from 'lucide-react'
import { Job } from '@/lib/jobs'
import { JobCard } from './JobCard'
import { useTranslations } from 'next-intl'

interface TasksDrawerPanelProps {
  jobs: Job[]
  runningCount: number
  onClose: () => void
  onCancel: (id: string) => void
}

export function TasksDrawerPanel({
  jobs,
  runningCount,
  onClose,
  onCancel,
}: TasksDrawerPanelProps) {
  const reduced = useReducedMotion()
  const completedCount = jobs.length - runningCount
  const t = useTranslations('jobs')

  return (
    <motion.div
      initial={{ opacity: 0, y: reduced ? 0 : 12, scale: reduced ? 1 : 0.98 }}
      animate={{ opacity: 1, y: 0, scale: 1 }}
      exit={{ opacity: 0, y: reduced ? 0 : 12, scale: reduced ? 1 : 0.98 }}
      transition={
        reduced
          ? { duration: 0.08 }
          : { type: 'spring', stiffness: 320, damping: 30 }
      }
      className="fixed bottom-16 right-4 z-40 flex w-96 max-w-[calc(100vw-2rem)] flex-col overflow-hidden rounded-lg border border-border bg-canvas shadow-lg"
      style={{ maxHeight: 'min(70vh, 640px)' }}
      role="dialog"
      aria-label={t('task_center')}
    >
      {/* Header */}
      <div className="flex items-center justify-between border-b border-border-light px-4 py-3">
        <div>
          <div className="text-sm font-semibold text-ink">{t('task_center')}</div>
          <div className="mt-0.5 text-[11px] font-mono tabular-nums text-ink-ghost">
            {t('task_summary', { total: jobs.length, running: runningCount, completed: completedCount })}
          </div>
        </div>
        <button
          onClick={onClose}
          aria-label={t('close')}
          className="inline-flex h-7 w-7 items-center justify-center rounded-md text-ink-muted hover:bg-canvas-alt hover:text-ink"
        >
          <X size={14} />
        </button>
      </div>

      {/* Body — scroll container */}
      <div className="flex-1 overflow-y-auto">
        {jobs.length === 0 ? (
          <div className="flex h-32 flex-col items-center justify-center gap-1 text-center">
            <div className="text-sm text-ink-muted">{t('no_tasks')}</div>
            <div className="text-[11px] text-ink-ghost">
              {t('no_tasks_hint')}
            </div>
          </div>
        ) : (
          <AnimatePresence initial={false}>
            {jobs.map((j, i) => (
              <motion.div
                key={j.id}
                initial={{ opacity: 0, y: reduced ? 0 : 4 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: reduced ? 0 : -4 }}
                transition={{ duration: 0.18, ease: 'easeOut' }}
                layout
                className={
                  i > 0 ? 'border-t border-border-light' : undefined
                }
              >
                <JobCard job={j} onCancel={onCancel} />
              </motion.div>
            ))}
          </AnimatePresence>
        )}
      </div>
    </motion.div>
  )
}
