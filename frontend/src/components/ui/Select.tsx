'use client'

import { SelectHTMLAttributes, forwardRef } from 'react'

interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  label?: string
  options: { value: string; label: string }[]
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(
  ({ label, options, className = '', ...props }, ref) => {
    return (
      <div className="flex flex-col gap-1">
        {label && (
          <label className="font-mono text-[10px] font-semibold uppercase tracking-wider text-ink-muted">
            {label}
          </label>
        )}
        <select
          ref={ref}
          className={`bg-canvas-alt px-3 py-2 font-mono text-sm text-ink outline-none transition-colors duration-100 border border-border-light rounded-md focus:border-ink ${className}`}
          {...props}
        >
          {options.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </div>
    )
  }
)
Select.displayName = 'Select'
