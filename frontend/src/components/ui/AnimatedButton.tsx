'use client'
/**
 * AnimatedButton — SV Minimal standard button.
 *
 * 遵循 docs/design/frontend-quality/02-visual-consistency.md §B.5:
 * - 所有按钮必须有 whileHover + whileTap micro-interaction
 * - prefers-reduced-motion 自动降级为纯色切换
 *
 * Variants 覆盖页面常见按钮族：primary CTA / secondary neutral /
 * ghost 辅助操作 / danger 删除等破坏性操作。
 */

import { motion, useReducedMotion, type HTMLMotionProps } from 'motion/react'
import { forwardRef, type ReactNode } from 'react'

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger'
type Size = 'xs' | 'sm' | 'md'

// SV Minimal · hover 不改底色（见 feedback_hover_darken memory + §B.5）
// 所有 hover 反馈走 whileHover scale（motion）+ 边框变色 + icon/文字颜色变化。
const variantClass: Record<Variant, string> = {
  primary:
    'bg-ink text-white border border-ink ' +
    'disabled:opacity-40',
  secondary:
    'bg-white text-ink border border-border ' +
    'hover:border-ink ' +
    'disabled:opacity-40 disabled:hover:border-border',
  ghost:
    'bg-transparent text-ink-muted border border-transparent ' +
    'hover:text-ink ' +
    'disabled:opacity-40',
  danger:
    'bg-white text-danger border border-danger/40 ' +
    'hover:border-danger ' +
    'disabled:opacity-40 disabled:hover:border-danger/40',
}

const sizeClass: Record<Size, string> = {
  xs: 'h-6 px-2 text-[11px] gap-1',
  sm: 'h-7 px-2.5 text-xs gap-1',
  md: 'h-8 px-3 text-sm gap-1.5',
}

type Props = HTMLMotionProps<'button'> & {
  variant?: Variant
  size?: Size
  children?: ReactNode
}

export const AnimatedButton = forwardRef<HTMLButtonElement, Props>(function AnimatedButton(
  { variant = 'secondary', size = 'sm', className = '', disabled, children, ...rest },
  ref,
) {
  const reduce = useReducedMotion()
  const inactive = disabled || reduce
  return (
    <motion.button
      ref={ref}
      disabled={disabled}
      whileHover={inactive ? undefined : { scale: 1.02 }}
      whileTap={inactive ? undefined : { scale: 0.97 }}
      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
      className={
        'inline-flex items-center justify-center rounded-md font-medium outline-none ' +
        'transition-colors duration-150 ' +
        'focus-visible:ring-1 focus-visible:ring-ink ' +
        'disabled:cursor-not-allowed ' +
        `${sizeClass[size]} ${variantClass[variant]} ${className}`
      }
      {...rest}
    >
      {children}
    </motion.button>
  )
})
