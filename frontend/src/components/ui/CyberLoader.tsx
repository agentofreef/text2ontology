'use client'

import { useEffect, useState } from 'react'

export function CyberLoader({ text = 'LOADING' }: { text?: string }) {
  const [, setTick] = useState(0)

  useEffect(() => {
    const id = setInterval(() => setTick(t => t + 1), 120)
    return () => clearInterval(id)
  }, [])

  return (
    <div className="flex flex-col items-center gap-3">
      <div
        className="h-8 w-8 rounded-full border-2 border-border-light border-t-ink animate-spin"
        style={{ animationDuration: '0.7s' }}
      />
      <div className="font-sans text-xs text-ink-muted tracking-wide">{text}</div>
    </div>
  )
}
