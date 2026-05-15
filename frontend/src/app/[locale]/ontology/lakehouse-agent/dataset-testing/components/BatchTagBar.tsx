'use client'

import { useState } from 'react'
import { Trash2, X } from 'lucide-react'
import { Tag } from './SharedCaseBits'
import { TagChipEditor } from './TagChipEditor'

// Sticky bottom bar for batch actions on the currently-checked rows.
// Used by both RunCaseList (run view — tag only) and QuestionsEditor
// (template view — tag + bulk delete). Delete is hidden unless `onDelete` is provided.
//
// "+ 添加" input uses TagChipEditor; each new chip fires `onAdd([name])` and
// each × fires `onRemove([name])` scoped to the currently-selected cases.
//
// "− 移除" side lists the union of tags across the selected rows so users can
// strip a tag from all of them with one click.
export function BatchTagBar({
  count, suiteTags, unionTags, onAdd, onRemove, onClear, onDelete,
}: {
  count: number
  suiteTags: Tag[]
  unionTags: Tag[]
  onAdd: (names: string[]) => void
  onRemove: (names: string[]) => void
  onClear: () => void
  onDelete?: () => void
}) {
  const [applied, setApplied] = useState<string[]>([])
  const handleCommit = (names: string[]) => {
    const added = names.filter(n => !applied.includes(n))
    const removed = applied.filter(n => !names.includes(n))
    if (added.length > 0) onAdd(added)
    if (removed.length > 0) onRemove(removed)
    setApplied(names)
  }

  return (
    <div className="sticky bottom-0 bg-white border-t-2 border-ink shadow-lg px-4 py-2 flex items-center gap-3 z-20 flex-wrap">
      <span className="font-mono text-[10px] font-bold text-ink bg-canvas-alt border border-ink px-2 py-0.5">
        已选 {count}
      </span>
      <span className="font-mono text-[9px] text-ink-ghost">+ 添加标签:</span>
      <TagChipEditor
        tags={applied.map(name => ({ id: name, name }))}
        suiteTags={suiteTags}
        onCommit={handleCommit}
        startEditing
        direction="up"
      />
      {unionTags.length > 0 && (
        <>
          <span className="w-px h-5 bg-ink/30 mx-1" />
          <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">− 移除:</span>
          {unionTags.map(t => (
            <button
              key={t.id}
              onClick={() => onRemove([t.name])}
              className="inline-flex items-center gap-1 border-2 border-red-500 bg-red-50 text-red-700 px-1.5 py-0.5 font-mono text-[10px] font-bold hover:bg-red-500 hover:text-white leading-none"
              title={`从已选用例移除标签 ${t.name}`}
            >
              <span className="text-red-500 group-hover:text-white">#</span>
              {t.name}
              <X size={10} strokeWidth={3} />
            </button>
          ))}
        </>
      )}
      <span className="flex-1" />
      {onDelete && (
        <button
          onClick={onDelete}
          className="flex items-center gap-1 border border-red-500 text-red-600 hover:bg-red-50 px-2 py-0.5 font-mono text-[10px] font-bold"
        >
          <Trash2 size={10} /> 删除 {count}
        </button>
      )}
      <button
        onClick={onClear}
        className="font-mono text-[10px] text-ink-ghost hover:text-ink border border-border px-2 py-0.5"
      >
        取消
      </button>
    </div>
  )
}
