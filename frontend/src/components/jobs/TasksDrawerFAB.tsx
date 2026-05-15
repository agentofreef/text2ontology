'use client'

import { motion, useReducedMotion } from 'motion/react'
import { ListChecks } from 'lucide-react'
import { useTranslations } from 'next-intl'

interface TasksDrawerFABProps {
  runningCount: number
  totalCount: number
  open: boolean
  onClick: () => void
}

// Pill-style FAB. Always rendered (so the user can also see "0 任务" via
// completed history when totalCount > 0). When zero jobs exist, the FAB is
// hidden by the parent — see TasksDrawer.tsx.
export function TasksDrawerFAB({
  runningCount,
  totalCount,
  open,
  onClick,
}: TasksDrawerFABProps) {
  const reduced = useReducedMotion()
  const isRunning = runningCount > 0
  const t = useTranslations('jobs')

  return (
    <motion.button
      onClick={onClick}
      whileHover={reduced ? undefined : { scale: 1.03 }}
      whileTap={reduced ? undefined : { scale: 0.96 }}
      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
      className="fixed bottom-4 right-4 z-30 inline-flex h-9 items-center gap-2 rounded-full bg-ink px-3 text-xs font-medium text-canvas shadow-md"
      aria-label={open ? t('close_task_center') : t('open_task_center')}
      aria-expanded={open}
    >
      <span className="relative flex h-1.5 w-1.5">
        {/* Pulsing dot when running */}
        {isRunning && !reduced && (
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-success/60" />
        )}
        <span
          className={`relative inline-flex h-1.5 w-1.5 rounded-full ${
            isRunning ? 'bg-success' : 'bg-ink-ghost'
          }`}
        />
      </span>
      <ListChecks size={14} aria-hidden />
      <span>{t('tasks')}</span>
      <span className="font-mono tabular-nums">
        {isRunning ? runningCount : totalCount}
      </span>
    </motion.button>
  )
}
