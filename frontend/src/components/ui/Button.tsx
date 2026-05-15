'use client'

import { ButtonHTMLAttributes, forwardRef } from 'react'
import { motion, useReducedMotion } from '@/lib/motion'

type Variant = 'default' | 'primary' | 'danger' | 'ghost'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: 'sm' | 'md' | 'lg'
}

const variantStyles: Record<Variant, string> = {
  default: 'border border-border bg-white text-ink hover:bg-canvas-alt hover:border-ink-ghost',
  primary: 'bg-ink text-white border border-ink hover:bg-accent hover:border-accent',
  danger: 'bg-danger text-white border border-danger hover:opacity-80',
  ghost: 'bg-transparent text-ink-muted hover:bg-canvas-alt hover:text-ink',
}

const sizeStyles = {
  sm: 'px-3 py-1.5 text-xs',
  md: 'px-4 py-2 text-sm',
  lg: 'px-6 py-3 text-sm',
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ variant = 'default', size = 'md', className = '', disabled, children, ...props }, ref) => {
    const reduced = useReducedMotion()

    const baseClass = `inline-flex items-center justify-center gap-2 font-mono text-xs font-semibold tracking-wide transition-all duration-100 ${
      variantStyles[variant]
    } ${sizeStyles[size]} ${
      disabled ? 'cursor-not-allowed opacity-50' : ''
    } rounded-md ${className}`

    if (!reduced) {
      return (
        <motion.button
          ref={ref}
          disabled={disabled}
          className={baseClass}
          whileHover={disabled ? undefined : { scale: 0.99 }}
          whileTap={disabled ? undefined : { scale: 0.97 }}
          {...(props as React.ComponentPropsWithoutRef<typeof motion.button>)}
        >
          {children}
        </motion.button>
      )
    }

    return (
      <button
        ref={ref}
        disabled={disabled}
        className={baseClass}
        {...props}
      >
        {children}
      </button>
    )
  }
)
Button.displayName = 'Button'
