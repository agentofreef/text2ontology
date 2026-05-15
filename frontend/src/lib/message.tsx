'use client'

import { createContext, useContext, useState, useCallback, ReactNode } from 'react'
import { X, CheckSquare, AlertTriangle, XOctagon, Info } from 'lucide-react'

// ==================== Types ====================

type MessageType = 'success' | 'warning' | 'error' | 'info'

interface MessageItem {
  id: number
  type: MessageType
  content: string
}

interface MessageContextType {
  success: (content: string) => void
  warning: (content: string) => void
  error: (content: string) => void
  info: (content: string) => void
}

// ==================== Context ====================

const MessageContext = createContext<MessageContextType | null>(null)

let nextId = 0

// ==================== Styles ====================

const typeStyles: Record<MessageType, string> = {
  success: 'border-l-success',
  warning: 'border-l-warning',
  error:   'border-l-danger',
  info:    'border-l-info',
}

const typeIcons: Record<MessageType, typeof Info> = {
  success: CheckSquare,
  warning: AlertTriangle,
  error:   XOctagon,
  info:    Info,
}

const typeIconColors: Record<MessageType, string> = {
  success: 'text-success',
  warning: 'text-warning',
  error:   'text-danger',
  info:    'text-info',
}

const AUTO_CLOSE_MS = 3000

// ==================== Single Message Bar ====================

function MessageBar({ item, onClose }: { item: MessageItem; onClose: () => void }) {
  const Icon = typeIcons[item.type]

  return (
    <div
      className={`flex items-start gap-2 border border-border bg-white px-4 py-3 border-l-[3px] ${typeStyles[item.type]} animate-[slideIn_150ms_linear]`}
      style={{ minWidth: 280, maxWidth: 420 }}
    >
      <Icon className={`h-4 w-4 mt-0.5 shrink-0 ${typeIconColors[item.type]}`} />
      <span className="flex-1 leading-relaxed text-ink-light font-sans text-sm">{item.content}</span>
      <button onClick={onClose} className="shrink-0 text-ink-ghost hover:text-ink mt-0.5">
        <X className="h-3 w-3" />
      </button>
    </div>
  )
}

// ==================== Provider ====================

export function MessageProvider({ children }: { children: ReactNode }) {
  const [messages, setMessages] = useState<MessageItem[]>([])

  const push = useCallback((type: MessageType, content: string) => {
    const id = ++nextId
    setMessages((prev) => [...prev, { id, type, content }])
    setTimeout(() => {
      setMessages((prev) => prev.filter((m) => m.id !== id))
    }, AUTO_CLOSE_MS)
  }, [])

  const remove = useCallback((id: number) => {
    setMessages((prev) => prev.filter((m) => m.id !== id))
  }, [])

  const ctx: MessageContextType = {
    success: useCallback((c: string) => push('success', c), [push]),
    warning: useCallback((c: string) => push('warning', c), [push]),
    error:   useCallback((c: string) => push('error', c), [push]),
    info:    useCallback((c: string) => push('info', c), [push]),
  }

  return (
    <MessageContext.Provider value={ctx}>
      {children}
      {/* Message container — fixed top-right */}
      {messages.length > 0 && (
        <div className="fixed top-4 right-4 z-[9999] flex flex-col gap-2">
          {messages.map((m) => (
            <MessageBar key={m.id} item={m} onClose={() => remove(m.id)} />
          ))}
        </div>
      )}
    </MessageContext.Provider>
  )
}

// ==================== Hook ====================

export function useMessage() {
  const ctx = useContext(MessageContext)
  if (!ctx) throw new Error('useMessage must be used within MessageProvider')
  return ctx
}
