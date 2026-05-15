'use client'

import { motion, useReducedMotion } from 'motion/react'

interface ProgressBarProps {
  percent: number
  // visual variant — running uses bg-ink (filled), succeeded uses bg-ink-ghost
  // (calm), failed uses bg-danger.
  variant?: 'running' | 'succeeded' | 'failed' | 'cancelled'
}

const fillClass = {
  running: 'bg-ink',
  succeeded: 'bg-ink-ghost',
  failed: 'bg-danger',
  cancelled: 'bg-ink-ghost',
}

// Per design-system v2: filled bar uses bg-ink (not green); track is canvas-alt;
// height 1.5 (6px); rounded-full so the bar feels precise.
export function ProgressBar({ percent, variant = 'running' }: ProgressBarProps) {
  const reduced = useReducedMotion()
  const clamped = Math.max(0, Math.min(100, percent))
  return (
    <div
      className="relative h-1.5 w-full overflow-hidden rounded-full bg-canvas-alt"
      role="progressbar"
      aria-valuenow={clamped}
      aria-valuemin={0}
      aria-valuemax={100}
    >
      <motion.div
        className={`h-full ${fillClass[variant]}`}
        initial={false}
        animate={{ width: `${clamped}%` }}
        transition={
          reduced ? { duration: 0.08 } : { duration: 0.3, ease: 'easeOut' }
        }
      />
    </div>
  )
}
