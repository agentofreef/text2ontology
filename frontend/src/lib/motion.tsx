'use client'
import { ReactNode, useRef, useEffect } from 'react'
import { motion, useReducedMotion, MotionProps, AnimatePresence } from 'motion/react'
import autoAnimate from '@formkit/auto-animate'
import { tStatic } from './i18nLite'

// ─── Spring tokens (claude.ai-restraint preset) ────────────────
export const SPRING_DEFAULT = { type: 'spring' as const, stiffness: 320, damping: 30 }
export const SPRING_SOFT = { type: 'spring' as const, stiffness: 220, damping: 26 }
export const EASE_DEFAULT = { type: 'tween' as const, duration: 0.18, ease: 'easeOut' as const }
export const REDUCED = { type: 'tween' as const, duration: 0.08, ease: 'easeOut' as const }

// ─── Wrapper: MotionFade ──────────────────────────────────────
interface MotionFadeProps extends MotionProps {
  children: ReactNode
  className?: string
  delay?: number
}
export function MotionFade({ children, className, delay = 0, ...rest }: MotionFadeProps) {
  const reduced = useReducedMotion()
  const transition = reduced ? REDUCED : { ...EASE_DEFAULT, delay }
  return (
    <motion.div
      initial={{ opacity: 0, y: reduced ? 0 : 6 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: reduced ? 0 : 6 }}
      transition={transition}
      className={className}
      {...rest}
    >
      {children}
    </motion.div>
  )
}

// ─── Wrapper: MotionScale (modal / sheet enter) ───────────────
export function MotionScale({ children, className, ...rest }: { children: ReactNode; className?: string } & MotionProps) {
  const reduced = useReducedMotion()
  return (
    <motion.div
      initial={{ opacity: 0, scale: reduced ? 1 : 0.96 }}
      animate={{ opacity: 1, scale: 1 }}
      exit={{ opacity: 0, scale: reduced ? 1 : 0.96 }}
      transition={reduced ? REDUCED : SPRING_DEFAULT}
      className={className}
      {...rest}
    >
      {children}
    </motion.div>
  )
}

// ─── Wrapper: MotionSlide ─────────────────────────────────────
export function MotionSlide({ children, className, from = 'right', ...rest }: { children: ReactNode; className?: string; from?: 'right' | 'left' | 'top' | 'bottom' } & MotionProps) {
  const reduced = useReducedMotion()
  const offset = reduced ? 0 : 12
  const initial = {
    right: { opacity: 0, x: offset },
    left: { opacity: 0, x: -offset },
    top: { opacity: 0, y: -offset },
    bottom: { opacity: 0, y: offset },
  }[from]
  return (
    <motion.div
      initial={initial}
      animate={{ opacity: 1, x: 0, y: 0 }}
      exit={initial}
      transition={reduced ? REDUCED : EASE_DEFAULT}
      className={className}
      {...rest}
    >
      {children}
    </motion.div>
  )
}

// ─── MotionGroup: streaming-gate aware ────────────────────────
// When `disabled={true}`, children render WITHOUT animation wrappers.
// Used by Phase 7 to gate animations during SSE streaming.
interface MotionGroupProps {
  children: ReactNode
  disabled?: boolean
  className?: string
  staggerMs?: number
}
export function MotionGroup({ children, disabled = false, className, staggerMs = 60 }: MotionGroupProps) {
  const reduced = useReducedMotion()
  if (disabled || reduced) {
    return <div className={className}>{children}</div>
  }
  return (
    <motion.div
      className={className}
      initial="hidden"
      animate="visible"
      variants={{
        hidden: {},
        visible: { transition: { staggerChildren: staggerMs / 1000 } },
      }}
    >
      {children}
    </motion.div>
  )
}

// Item helper — pair with MotionGroup for stagger entry
export function MotionGroupItem({ children, className }: { children: ReactNode; className?: string }) {
  const reduced = useReducedMotion()
  return (
    <motion.div
      className={className}
      variants={{
        hidden: { opacity: 0, y: reduced ? 0 : 6 },
        visible: { opacity: 1, y: 0, transition: reduced ? REDUCED : EASE_DEFAULT },
      }}
    >
      {children}
    </motion.div>
  )
}

// ─── useAutoAnimate (FLIP for list add/remove) ────────────────
// Returns a ref that, when attached to a parent, animates child add/remove.
export function useAutoAnimate<T extends HTMLElement = HTMLDivElement>() {
  const ref = useRef<T>(null)
  useEffect(() => {
    if (ref.current) autoAnimate(ref.current, { duration: 180, easing: 'ease-out' })
  }, [])
  return ref
}

// ─── Re-exports for advanced use cases (escape hatch) ─────────
export { AnimatePresence, motion, useReducedMotion }

// ─── Spinner (used by DataLoader, also exported for inline use) ───
interface SpinnerProps {
  size?: number       // px diameter, default 16
  className?: string
}
export function Spinner({ size = 16, className = '' }: SpinnerProps) {
  // Tailwind animate-spin + arc via border. Theme-aware via currentColor.
  return (
    <span
      role="status"
      aria-label={tStatic('common.loading')}
      className={`inline-block animate-spin rounded-full border-2 border-current border-t-transparent ${className}`}
      style={{ width: size, height: size, opacity: 0.55 }}
    />
  )
}

// ─── DataLoader: AnimatePresence-driven loader → content fade ──
//
// Wrap any data-fetching content section:
//   <DataLoader loading={!data}>
//     {children}
//   </DataLoader>
//
// Behaviour:
//   loading=true   → centered Spinner + optional message, fades in/out
//   loading=false  → children fade in, Spinner fades out
//   reduced-motion → MotionFade wrapper degrades to ≤80ms (already baked)
//
// Uses AnimatePresence mode="wait" so the loader fully exits before
// content enters — prevents double-render flash.
interface DataLoaderProps {
  loading: boolean
  children: ReactNode
  className?: string         // applied to the OUTER container
  contentClassName?: string  // applied only to the loaded-content wrapper
  message?: string           // optional text next to spinner
  minHeight?: number | string  // optional min-height on the container so layout doesn't jump
}
export function DataLoader({
  loading,
  children,
  className = '',
  contentClassName = '',
  message,
  minHeight,
}: DataLoaderProps) {
  return (
    <div
      className={className}
      style={minHeight !== undefined ? { minHeight } : undefined}
    >
      <AnimatePresence mode="wait" initial={false}>
        {loading ? (
          <MotionFade key="loader" className="flex items-center justify-center py-12 text-ink-ghost">
            <Spinner size={18} />
            {message && <span className="ml-3 text-sm font-sans">{message}</span>}
          </MotionFade>
        ) : (
          <MotionFade key="content" className={contentClassName}>
            {children}
          </MotionFade>
        )}
      </AnimatePresence>
    </div>
  )
}
