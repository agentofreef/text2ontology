'use client'

type BadgeVariant = 'success' | 'warning' | 'danger' | 'info' | 'default' | 'accent'

interface BadgeProps {
  variant?: BadgeVariant
  children: React.ReactNode
  className?: string
}

const variantStyles: Record<BadgeVariant, string> = {
  success: 'bg-green-50 text-green-700 border-green-200',
  warning: 'bg-gray-100 text-gray-600 border-gray-200',
  danger: 'bg-red-50 text-red-700 border-red-200',
  info: 'bg-gray-100 text-gray-600 border-gray-200',
  default: 'bg-gray-100 text-gray-500 border-gray-200',
  accent: 'bg-ink text-white border-ink',
}

export function Badge({ variant = 'default', children, className = '' }: BadgeProps) {
  return (
    <span
      className={`inline-flex items-center border px-2 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider rounded-full ${variantStyles[variant]} ${className}`}
    >
      {children}
    </span>
  )
}
