'use client'

import { ReactNode } from 'react'

/**
 * Compact action button used inside DataTable batch toolbars.
 * Shared between lakehouse-objects and lakehouse-keywords pages.
 */
export function BulkActionButton({
  children, onClick, disabled, danger, title,
}: {
  children: ReactNode
  onClick: () => void
  disabled?: boolean
  danger?: boolean
  title?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={title}
      className={`inline-flex h-7 items-center gap-1 rounded-md border px-2 text-[11px] font-medium outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-40 focus-visible:ring-1 ${
        danger
          ? 'border-danger/30 bg-white text-danger hover:border-danger hover:bg-danger/5 focus-visible:ring-danger/40'
          : 'border-border bg-white text-ink-muted hover:border-ink hover:text-ink focus-visible:ring-ink'
      }`}
    >
      {children}
    </button>
  )
}

/**
 * Single row in a cascade-impact preview table inside a bulk-delete modal.
 * Shared between lakehouse-objects and lakehouse-keywords pages.
 */
export function ImpactRow({ label, value, primary }: { label: string; value: number; primary?: boolean }) {
  return (
    <div className={`flex items-center justify-between rounded-md border px-3 py-1.5 ${
      primary ? 'border-ink bg-canvas-alt' : 'border-border-light bg-white'
    }`}>
      <span className={primary ? 'text-ink font-medium' : 'text-ink-muted'}>{label}</span>
      <span className={`font-mono tabular-nums ${primary ? 'text-ink font-semibold' : value > 0 ? 'text-ink' : 'text-ink-ghost'}`}>{value}</span>
    </div>
  )
}
