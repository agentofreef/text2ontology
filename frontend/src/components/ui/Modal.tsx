'use client'

import { ReactNode, useEffect, useRef, useId } from 'react'
import { X } from 'lucide-react'
import { MotionScale, AnimatePresence } from '@/lib/motion'
import { useTranslations } from 'next-intl'

interface ModalProps {
  open: boolean
  onClose: () => void
  title: string
  /**
   * Optional one-line subtitle rendered below the title in muted ink.
   * Use for context like "8 个对象" or a short description; keep it brief.
   */
  subtitle?: string
  children: ReactNode
  /** Width as CSS string (default 560px). Capped at 90vw on small screens. */
  width?: string
  /**
   * When false, the modal cannot be dismissed by ANY means: × button is hidden,
   * ESC is ignored, backdrop click is ignored. Use only for truly forced flows
   * (e.g. mid-flight delete that must complete). Default true.
   */
  dismissable?: boolean
  /**
   * When false, clicking the backdrop does NOT close the modal — but × and ESC
   * still work. Use for forms where accidental backdrop clicks would lose user
   * input. Default true.
   */
  closeOnBackdrop?: boolean
  /** When true, hides the title-row × button entirely (rare, e.g. forced confirm). */
  hideCloseButton?: boolean
  /**
   * Optional content rendered inside a sticky footer at the bottom of the modal,
   * separated from the body by a thin border. Use this for action buttons so the
   * footer stays visible while the body scrolls.
   */
  footer?: ReactNode
}

export function Modal({
  open,
  onClose,
  title,
  subtitle,
  children,
  width = '560px',
  dismissable = true,
  closeOnBackdrop = true,
  hideCloseButton = false,
  footer,
}: ModalProps) {
  const titleId = useId()
  const t = useTranslations('ui')
  // Stash the latest onClose / dismissable on a ref so the ESC effect doesn't
  // re-bind (and accidentally miss events) when the parent re-renders with a
  // fresh closure. This was the root cause of "× sometimes doesn't close":
  // a stale `() => !busy && close()` closure was captured by an event handler
  // that never refreshed.
  const onCloseRef = useRef(onClose)
  const dismissableRef = useRef(dismissable)
  useEffect(() => {
    onCloseRef.current = onClose
    dismissableRef.current = dismissable
  })

  useEffect(() => {
    if (!open) return
    document.body.style.overflow = 'hidden'
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && dismissableRef.current) {
        e.stopPropagation()
        onCloseRef.current()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.body.style.overflow = ''
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const handleClose = () => {
    if (!dismissable) return
    onClose()
  }

  return (
    <AnimatePresence>
      {open && (
        <div
          className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 sm:items-center sm:p-8"
          onClick={dismissable && closeOnBackdrop ? handleClose : undefined}
          role="presentation"
        >
          <MotionScale className="w-full" style={{ maxWidth: width }}>
            <div
              role="dialog"
              aria-modal="true"
              aria-labelledby={titleId}
              className="flex max-h-[calc(100vh-2rem)] w-full flex-col overflow-hidden rounded-lg border border-border bg-white shadow-xl"
              style={{ maxWidth: width }}
              onClick={(e) => e.stopPropagation()}
            >
              {/* Header — sticky at the top of the scrolling body */}
              <div className="flex flex-shrink-0 items-start justify-between gap-4 border-b border-border-light bg-white px-6 py-4">
                <div className="min-w-0 flex-1">
                  <h2 id={titleId} className="truncate text-base font-semibold text-ink">
                    {title}
                  </h2>
                  {subtitle && (
                    <p className="mt-0.5 truncate text-xs text-ink-muted">{subtitle}</p>
                  )}
                </div>
                {!hideCloseButton && dismissable && (
                  <button
                    type="button"
                    onClick={handleClose}
                    aria-label={t('close')}
                    className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-md text-ink-ghost outline-none transition-colors hover:bg-canvas-alt hover:text-ink focus-visible:ring-1 focus-visible:ring-ink"
                  >
                    <X className="h-4 w-4" aria-hidden="true" />
                  </button>
                )}
              </div>

              {/* Body — the only scroll container; header & footer stay fixed */}
              <div className="flex-1 overflow-y-auto px-6 py-5">{children}</div>

              {/* Optional footer — sticky at bottom */}
              {footer && (
                <div className="flex flex-shrink-0 items-center justify-end gap-2 border-t border-border-light bg-white px-6 py-3">
                  {footer}
                </div>
              )}
            </div>
          </MotionScale>
        </div>
      )}
    </AnimatePresence>
  )
}
