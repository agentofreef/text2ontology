'use client'

// Composer — minimal industrial input form.
//
// Single ink frame, two stacked regions:
//   ┌──────────────────────────────────────────────┐
//   │ textarea (auto-grow)                         │
//   ├──────────────────────────────────────────────┤
//   │ ⌘ + ↵ 发送                          [→]     │
//   └──────────────────────────────────────────────┘
//
// The send button is a single small square — proportional to the form,
// not a heavy column. Keyboard hint reads as plain text, not a key cap.

import { useEffect, useRef } from 'react'
import { ArrowUp, Loader2 } from 'lucide-react'

interface Props {
  value: string
  onChange: (v: string) => void
  onSubmit: () => void
  disabled?: boolean
  streaming?: boolean
  placeholder?: string
}

export function Composer({ value, onChange, onSubmit, disabled, streaming, placeholder }: Props) {
  const taRef = useRef<HTMLTextAreaElement | null>(null)

  useEffect(() => {
    const el = taRef.current
    if (!el) return
    el.style.height = '0px'
    const next = Math.min(el.scrollHeight, 180)
    el.style.height = `${Math.max(next, 56)}px`
  }, [value])

  const canSend = !disabled && !streaming && value.trim().length > 0

  return (
    <div className="px-4 pb-4 pt-3">
      <div className="mx-auto max-w-3xl">
        <div className="border border-ink bg-white">
          <textarea
            ref={taRef}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={(e) => {
              if ((e.key === 'Enter' && (e.metaKey || e.ctrlKey)) || (e.key === 'Enter' && !e.shiftKey)) {
                e.preventDefault()
                if (canSend) onSubmit()
              }
            }}
            placeholder={placeholder || '描述你想分析什么 — 例:燕麦奶断供会影响多少营收?'}
            rows={1}
            disabled={disabled}
            className="block w-full resize-none bg-transparent px-3.5 pt-3 pb-2 font-mono text-[13.5px] leading-relaxed text-ink placeholder:text-ink-muted outline-none disabled:opacity-50"
          />

          {/* Action row */}
          <div className="flex items-center justify-between border-t border-ink bg-canvas-alt px-3 py-1.5">
            <span className="font-mono text-[10px] tracking-[0.14em] uppercase text-ink-muted">
              ⌘ + ↵ {streaming ? '运行中…' : '发送'}
            </span>
            <button
              type="button"
              onClick={onSubmit}
              disabled={!canSend}
              aria-label="发送"
              className={`inline-flex h-7 w-7 items-center justify-center border border-ink transition-colors ${
                canSend
                  ? 'bg-ink text-white hover:bg-white hover:text-ink'
                  : 'bg-white text-ink-muted opacity-50'
              }`}
            >
              {streaming ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <ArrowUp className="h-3.5 w-3.5" />}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
