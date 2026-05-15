'use client'

import { useState, ReactNode } from 'react'

interface Tab {
  key: string
  label: string
  content: ReactNode
}

interface TabsProps {
  tabs: Tab[]
  defaultKey?: string
  className?: string
}

export function Tabs({ tabs, defaultKey, className = '' }: TabsProps) {
  const [activeKey, setActiveKey] = useState(defaultKey || tabs[0]?.key)

  return (
    <div className={className}>
      <div className="flex border-b border-border-light">
        {tabs.map((tab) => (
          <button
            key={tab.key}
            onClick={() => setActiveKey(tab.key)}
            className={`px-4 py-2 font-sans text-xs font-semibold transition-colors duration-100 ${
              activeKey === tab.key
                ? 'border-b-2 border-ink text-ink -mb-[1px]'
                : 'text-ink-muted hover:text-ink'
            }`}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <div className="pt-4">
        {tabs.find((t) => t.key === activeKey)?.content}
      </div>
    </div>
  )
}
