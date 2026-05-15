'use client'

import { ReactNode } from 'react'

interface CardProps {
  title?: string
  titlePrefix?: string
  extra?: ReactNode
  children: ReactNode
  className?: string
  noPadding?: boolean
}

export function Card({ title, titlePrefix, extra, children, className = '', noPadding }: CardProps) {
  return (
    <div className={`border bg-white border-border-light rounded-lg ${className}`}>
      {title && (
        <div className={`flex items-center justify-between border-b px-5 py-3 border-border-light`}>
          <h3 className="flex items-center gap-2 text-sm font-semibold">
            {title}
          </h3>
          {extra && <div>{extra}</div>}
        </div>
      )}
      <div className={noPadding ? 'flex flex-1 flex-col min-h-0' : 'p-5'}>{children}</div>
    </div>
  )
}
