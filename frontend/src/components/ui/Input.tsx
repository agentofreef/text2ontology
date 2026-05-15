'use client'

import { InputHTMLAttributes, forwardRef } from 'react'

interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string
}

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ label, className = '', ...props }, ref) => {
    return (
      <div className="flex flex-col gap-1">
        {label && (
          <label className="font-mono text-[10px] font-semibold uppercase tracking-wider text-ink-muted">
            {label}
          </label>
        )}
        <input
          ref={ref}
          className={`bg-canvas-alt px-3 py-2 font-mono text-sm text-ink outline-none transition-colors duration-100 placeholder:text-ink-ghost border border-border-light rounded-md focus:border-ink ${className}`}
          {...props}
        />
      </div>
    )
  }
)
Input.displayName = 'Input'

interface TextareaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement> {
  label?: string
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  ({ label, className = '', ...props }, ref) => {
    return (
      <div className="flex flex-col gap-1">
        {label && (
          <label className="font-mono text-[10px] font-semibold uppercase tracking-wider text-ink-muted">
            {label}
          </label>
        )}
        <textarea
          ref={ref}
          className={`bg-canvas-alt px-3 py-2 font-mono text-sm text-ink outline-none transition-colors duration-100 placeholder:text-ink-ghost border border-border-light rounded-md focus:border-ink ${className}`}
          {...props}
        />
      </div>
    )
  }
)
Textarea.displayName = 'Textarea'
