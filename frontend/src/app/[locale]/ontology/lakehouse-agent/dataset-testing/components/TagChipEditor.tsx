'use client'

import { useEffect, useRef, useState, KeyboardEvent } from 'react'
import { X, Plus } from 'lucide-react'
import { Tag } from './SharedCaseBits'

// Inline chip editor. Read-only by default; click the "+ 标签" chip to enter edit mode.
// - type + Enter/comma → commit tag (resolved/created server-side by caller)
// - Backspace on empty input → remove last chip
// - suggestions filtered from `suiteTags` by current input (case-insensitive substring)
//
// The editor is intentionally "last-write-wins": `onCommit` is called with the
// FULL list of tag names after every add/remove. Caller decides how to persist
// (e.g., PUT /cases/{id}/tags with resolved IDs, or POST /cases/bulk-tag with names).
interface Props {
  tags: Tag[]
  suiteTags: Tag[]
  onCommit: (names: string[]) => void
  // When false, chips show read-only — click "+ 标签" to enter edit mode.
  // When true, starts in edit mode (used by filter bar, etc).
  startEditing?: boolean
  placeholder?: string
  // Compact variant hides the "+ 标签" placeholder when empty read-only.
  compact?: boolean
  // Which direction the autocomplete popup opens. 'down' (default) stacks the
  // suggestion list below the input; 'up' stacks it above — required when the
  // editor lives inside a sticky bottom bar, where 'down' would be clipped.
  direction?: 'up' | 'down'
}

export function TagChipEditor({
  tags, suiteTags, onCommit,
  startEditing = false,
  placeholder = '+ 标签',
  compact = false,
  direction = 'down',
}: Props) {
  const [editing, setEditing] = useState(startEditing)
  const [input, setInput] = useState('')
  const [hoverIdx, setHoverIdx] = useState(-1)
  const inputRef = useRef<HTMLInputElement>(null)

  // Local working copy — committed to parent only on blur or Enter/chip-delete.
  const [local, setLocal] = useState<string[]>(() => tags.map(t => t.name))
  useEffect(() => {
    if (!editing) setLocal(tags.map(t => t.name))
  }, [tags, editing])

  useEffect(() => {
    if (editing) inputRef.current?.focus()
  }, [editing])

  const commit = (next: string[]) => {
    setLocal(next)
    onCommit(next)
  }

  const addTag = (name: string) => {
    const clean = name.trim()
    if (!clean) return
    if (local.includes(clean)) return
    commit([...local, clean])
    setInput('')
    setHoverIdx(-1)
  }

  const removeAt = (idx: number) => {
    const next = local.filter((_, i) => i !== idx)
    commit(next)
  }

  const suggestions = input.trim()
    ? suiteTags.filter(t =>
        t.name.toLowerCase().includes(input.trim().toLowerCase()) && !local.includes(t.name))
    : suiteTags.filter(t => !local.includes(t.name))

  const handleKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault()
      if (hoverIdx >= 0 && hoverIdx < suggestions.length) {
        addTag(suggestions[hoverIdx].name)
      } else {
        addTag(input)
      }
    } else if (e.key === 'Backspace' && input === '' && local.length > 0) {
      e.preventDefault()
      removeAt(local.length - 1)
    } else if (e.key === 'Escape') {
      setEditing(false)
      setInput('')
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHoverIdx(i => Math.min(i + 1, suggestions.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHoverIdx(i => Math.max(i - 1, -1))
    }
  }

  // Click outside → exit edit mode (losing suggestion dropdown is fine; chips persisted each commit)
  const containerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!editing) return
    const onDoc = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setEditing(false)
        setInput('')
      }
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [editing])

  if (!editing && local.length === 0) {
    if (compact) {
      return (
        <button
          onClick={e => { e.stopPropagation(); setEditing(true) }}
          className="font-mono text-[10px] text-ink-ghost hover:text-accent px-1"
          title="添加标签"
        >
          <Plus size={11} />
        </button>
      )
    }
    return (
      <button
        onClick={e => { e.stopPropagation(); setEditing(true) }}
        className="inline-flex items-center gap-0.5 border border-dashed border-ink/40 text-ink-ghost hover:text-ink hover:border-ink px-2 py-0.5 font-mono text-[10px]"
      >
        <Plus size={10} /> {placeholder}
      </button>
    )
  }

  // Popup position: sticky bars at the bottom of the viewport must open
  // upward, otherwise the list is hidden below the bar.
  const popupPos = direction === 'up' ? 'bottom-full mb-1' : 'top-full mt-1'

  return (
    <div
      ref={containerRef}
      className="inline-flex items-center flex-wrap gap-1 relative"
      onClick={e => { e.stopPropagation(); if (!editing) setEditing(true) }}
    >
      {local.map((name, i) => (
        <span
          key={`${name}-${i}`}
          className="inline-flex items-center gap-1 border-2 border-ink bg-accent/10 px-1.5 py-0.5 font-mono text-[10px] font-bold text-ink leading-none"
        >
          <span className="text-accent font-bold">#</span>
          {name}
          {editing && (
            <button
              onClick={e => { e.stopPropagation(); removeAt(i) }}
              className="text-ink-ghost hover:text-accent ml-0.5"
              aria-label={`删除标签 ${name}`}
            >
              <X size={10} strokeWidth={3} />
            </button>
          )}
        </span>
      ))}
      {editing && (
        <>
          <input
            ref={inputRef}
            value={input}
            onChange={e => { setInput(e.target.value); setHoverIdx(-1) }}
            onKeyDown={handleKey}
            onClick={e => e.stopPropagation()}
            placeholder="输入后回车"
            className="border-b border-ink outline-none bg-transparent font-mono text-[10px] font-semibold w-20 min-w-[80px] py-0.5"
          />
          {/* Suggestions popup (direction-aware) */}
          {suggestions.length > 0 && (
            <div className={`absolute ${popupPos} left-0 bg-white border-2 border-ink shadow-lg z-30 min-w-[160px] max-h-48 overflow-y-auto`}>
              <div className="px-2 py-1 bg-canvas-alt border-b border-border font-mono text-[8px] text-ink-ghost font-bold tracking-wider">
                ▼// SUGGESTED · {suggestions.length}
              </div>
              {suggestions.slice(0, 12).map((t, i) => (
                <button
                  key={t.id}
                  onClick={e => { e.stopPropagation(); addTag(t.name) }}
                  onMouseEnter={() => setHoverIdx(i)}
                  className={`flex items-center gap-1 w-full text-left px-2 py-1 font-mono text-[10px] font-semibold ${
                    hoverIdx === i ? 'bg-ink text-white' : 'text-ink hover:bg-canvas-alt'
                  }`}
                >
                  <span className={hoverIdx === i ? 'text-accent' : 'text-accent'}>#</span>
                  <span className="flex-1">{t.name}</span>
                  {typeof t.count === 'number' && (
                    <span className={hoverIdx === i ? 'text-white/60' : 'text-ink-ghost'}>
                      {t.count}
                    </span>
                  )}
                </button>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  )
}
