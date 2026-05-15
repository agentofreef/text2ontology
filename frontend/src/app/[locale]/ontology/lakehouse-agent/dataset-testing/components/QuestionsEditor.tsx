'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Check, CheckCircle2, Pencil, Search, Trash2, X } from 'lucide-react'
import { api } from '@/lib/api'
import { useMessage } from '@/lib/message'
import { Tag } from './SharedCaseBits'
import { TagChipEditor } from './TagChipEditor'
import { BatchTagBar } from './BatchTagBar'

// Master question template for a suite. One row = one editable question
// shared across all runs of the suite. Tags live here — version/run rows
// inherit them when enqueued.
export interface QuestionTemplate {
  id: string
  code: string
  userQuestion: string
  expectedAnswer: string
  sortOrder: number
  tags: Tag[]
  createdAt: string
  updatedAt: string
}

interface Props {
  suiteId: string
  suiteTags: Tag[]
  onMutated: () => void // called after any successful edit/delete so page can refresh tags + runs
}

export function QuestionsEditor({ suiteId, suiteTags, onMutated }: Props) {
  const msg = useMessage()

  const [rows, setRows] = useState<QuestionTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [searchQuery, setSearchQuery] = useState('')
  const [filterTagIds, setFilterTagIds] = useState<Set<string>>(new Set())
  const [checked, setChecked] = useState<Set<string>>(new Set())
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editText, setEditText] = useState('')
  // 正确答案是独立的 inline editor。不和问题共用 editingId，
  // 这样可以单独点开/收起某一行的"正确答案"区。
  const [editingAnswerId, setEditingAnswerId] = useState<string | null>(null)
  const [editAnswerText, setEditAnswerText] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api<{ data: QuestionTemplate[] }>(`/ontology/lh-test-suites/${suiteId}/cases`)
      setRows(res.data || [])
    } catch { msg.error('加载问题列表失败') }
    finally { setLoading(false) }
  }, [suiteId, msg])

  useEffect(() => { load() }, [load])

  const filtered = useMemo(() => {
    const q = searchQuery.trim().toLowerCase()
    return rows.filter(r => {
      if (filterTagIds.size > 0 && !(r.tags || []).some(t => filterTagIds.has(t.id))) return false
      if (q && !r.userQuestion.toLowerCase().includes(q) && !r.code.toLowerCase().includes(q)) return false
      return true
    })
  }, [rows, filterTagIds, searchQuery])

  const selectedIds = Array.from(checked)

  const batchUnionTags = useMemo<Tag[]>(() => {
    if (selectedIds.length === 0) return []
    const pool = new Map<string, Tag>()
    rows.forEach(r => {
      if (!checked.has(r.id)) return
      r.tags.forEach(t => pool.set(t.id, t))
    })
    return Array.from(pool.values()).sort((a, b) => a.name.localeCompare(b.name))
  }, [rows, checked, selectedIds.length])

  const toggleChecked = (id: string) => {
    setChecked(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id); else next.add(id)
      return next
    })
  }

  const allVisibleChecked = filtered.length > 0 && filtered.every(r => checked.has(r.id))
  const toggleCheckAll = () => {
    setChecked(prev => {
      const next = new Set(prev)
      if (allVisibleChecked) {
        filtered.forEach(r => next.delete(r.id))
      } else {
        filtered.forEach(r => next.add(r.id))
      }
      return next
    })
  }
  const clearChecked = () => setChecked(new Set())

  // --- Row operations ---

  const startEdit = (r: QuestionTemplate) => {
    setEditingId(r.id)
    setEditText(r.userQuestion)
  }
  const cancelEdit = () => { setEditingId(null); setEditText('') }

  const saveEdit = async () => {
    if (!editingId) return
    const text = editText.trim()
    if (!text) { msg.error('问题不能为空'); return }
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/cases/${editingId}`, {
        method: 'PUT',
        body: { userQuestion: text },
      })
      msg.success('已保存（已保存的 run 快照不受影响）')
      setEditingId(null); setEditText('')
      load()
      onMutated()
    } catch { msg.error('保存失败') }
  }

  const startEditAnswer = (r: QuestionTemplate) => {
    setEditingAnswerId(r.id)
    setEditAnswerText(r.expectedAnswer || '')
  }
  const cancelEditAnswer = () => { setEditingAnswerId(null); setEditAnswerText('') }

  const saveEditAnswer = async () => {
    if (!editingAnswerId) return
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/cases/${editingAnswerId}`, {
        method: 'PUT',
        body: { expectedAnswer: editAnswerText },
      })
      msg.success('正确答案已保存')
      setEditingAnswerId(null); setEditAnswerText('')
      load()
      onMutated()
    } catch { msg.error('保存失败') }
  }

  const deleteOne = async (id: string) => {
    if (!confirm('删除此问题？历史 run 中此问题的运行结果会因级联而一并删除。')) return
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/cases/${id}`, { method: 'DELETE' })
      load()
      onMutated()
    } catch { msg.error('删除失败') }
  }

  const bulkDelete = async () => {
    if (selectedIds.length === 0) return
    if (!confirm(`删除 ${selectedIds.length} 个问题？所有 run 里对应的快照会被级联删除。`)) return
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/cases/bulk-delete`, {
        method: 'POST',
        body: { caseIds: selectedIds },
      })
      msg.success(`已删除 ${selectedIds.length} 个`)
      clearChecked()
      load()
      onMutated()
    } catch { msg.error('批量删除失败') }
  }

  const onCaseTagsChange = async (caseId: string, tagNames: string[]) => {
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/cases/${caseId}/tags`, {
        method: 'PUT',
        body: { tagNames },
      })
      load()
      onMutated()
    } catch { msg.error('标签保存失败') }
  }

  const onBulkTag = async (caseIds: string[], add: string[], remove: string[]) => {
    if (caseIds.length === 0 || (add.length === 0 && remove.length === 0)) return
    try {
      await api(`/ontology/lh-test-suites/${suiteId}/cases/bulk-tag`, {
        method: 'POST',
        body: { caseIds, add, remove },
      })
      load()
      onMutated()
    } catch { msg.error('批量打标失败') }
  }

  const toggleFilterTag = (id: string) => {
    setFilterTagIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id); else next.add(id)
      return next
    })
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      {/* Notice */}
      <div className="px-4 py-1.5 border-b border-border bg-amber-50 font-mono text-[10px] text-amber-900">
        ⚠ 这里是问题模板（dataset 级）。编辑/删除只影响之后新建或重跑的 run — 已有的 run 快照不变。
      </div>

      {/* Filter bar */}
      <div className="flex items-center gap-2 px-4 py-1.5 border-b border-border bg-canvas-alt/30 flex-wrap">
        <div className="flex items-center gap-1 border border-border bg-white px-2 py-0.5">
          <Search size={11} className="text-ink-ghost" />
          <input
            value={searchQuery}
            onChange={e => setSearchQuery(e.target.value)}
            placeholder="搜索问题"
            className="font-mono text-[10px] outline-none w-32 bg-transparent"
          />
          {searchQuery && (
            <button onClick={() => setSearchQuery('')} className="text-ink-ghost hover:text-accent">
              <X size={10} />
            </button>
          )}
        </div>
        {suiteTags.length > 0 && (
          <>
            <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">标签:</span>
            {suiteTags.map(t => {
              const active = filterTagIds.has(t.id)
              return (
                <button
                  key={t.id}
                  onClick={() => toggleFilterTag(t.id)}
                  className={`inline-flex items-center gap-1 border-2 px-1.5 py-0.5 font-mono text-[10px] font-bold leading-none ${
                    active
                      ? 'bg-ink text-white border-ink'
                      : 'bg-white text-ink border-ink hover:bg-accent/10'
                  }`}
                >
                  <span className="text-accent">#</span>
                  {t.name}
                  {typeof t.count === 'number' && (
                    <span className={active ? 'text-white/60' : 'text-ink-ghost font-semibold'}>{t.count}</span>
                  )}
                </button>
              )
            })}
            {filterTagIds.size > 0 && (
              <button
                onClick={() => setFilterTagIds(new Set())}
                className="font-mono text-[9px] text-accent hover:underline"
              >
                清除
              </button>
            )}
          </>
        )}
        <span className="flex-1" />
        <span className="font-mono text-[9px] text-ink-ghost">
          {filtered.length}/{rows.length} 题
        </span>
      </div>

      {/* Column header */}
      {filtered.length > 0 && (
        <div className="flex items-center gap-3 px-4 py-1 bg-canvas-alt border-b border-border sticky top-0 z-[5]">
          <input
            type="checkbox"
            className="accent-ink cursor-pointer"
            checked={allVisibleChecked}
            onChange={toggleCheckAll}
            title="全选当前视图"
          />
          <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">
            {checked.size > 0 ? `已选 ${checked.size}` : `${filtered.length} 条`}
          </span>
        </div>
      )}

      {/* Rows */}
      <div className="flex-1 overflow-y-auto">
        {loading ? (
          <div className="flex h-24 items-center justify-center font-mono text-xs text-ink-ghost">加载中...</div>
        ) : filtered.length === 0 ? (
          <div className="flex h-24 items-center justify-center font-mono text-xs text-ink-ghost">
            {rows.length === 0 ? '暂无问题 — 上传或粘贴问题后再回来' : '当前筛选下无匹配问题'}
          </div>
        ) : (
          <div className="divide-y divide-border">
            {filtered.map(r => {
              const isEditing = editingId === r.id
              const isChecked = checked.has(r.id)
              const isEditingAnswer = editingAnswerId === r.id
              const hasAnswer = !!(r.expectedAnswer && r.expectedAnswer.trim())
              return (
                <div
                  key={r.id}
                  className="flex items-start gap-3 px-4 py-2 hover:bg-canvas-alt/30"
                >
                  <input
                    type="checkbox"
                    className="accent-ink cursor-pointer mt-1 flex-shrink-0"
                    checked={isChecked}
                    onChange={() => toggleChecked(r.id)}
                  />
                  <span className="font-mono text-[10px] text-ink-ghost w-12 flex-shrink-0 mt-1">{r.code}</span>
                  {/* Question text + 正确答案 — inline editable */}
                  <div className="flex-1 min-w-0">
                    {isEditing ? (
                      <div className="flex items-start gap-2">
                        <textarea
                          autoFocus
                          value={editText}
                          onChange={e => setEditText(e.target.value)}
                          onKeyDown={e => {
                            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); saveEdit() }
                            if (e.key === 'Escape') { e.preventDefault(); cancelEdit() }
                          }}
                          className="flex-1 border border-ink px-2 py-1 font-mono text-xs outline-none resize-y min-h-[32px]"
                          rows={2}
                        />
                        <button
                          onClick={saveEdit}
                          className="border border-accent bg-accent text-white px-2 py-1 font-mono text-[10px] font-bold hover:bg-accent/90"
                          title="保存 (⌘/Ctrl + Enter)"
                        >
                          <Check size={12} />
                        </button>
                        <button
                          onClick={cancelEdit}
                          className="border border-border px-2 py-1 font-mono text-[10px] text-ink-ghost hover:text-ink"
                          title="取消 (Esc)"
                        >
                          <X size={12} />
                        </button>
                      </div>
                    ) : (
                      <div className="flex items-start gap-2">
                        <span
                          className="font-mono text-xs text-ink flex-1 break-words cursor-text"
                          onDoubleClick={() => startEdit(r)}
                        >
                          {r.userQuestion}
                        </span>
                        <button
                          onClick={() => startEdit(r)}
                          className="text-ink-ghost hover:text-accent flex-shrink-0"
                          title="编辑问题文本"
                        >
                          <Pencil size={11} />
                        </button>
                      </div>
                    )}
                    {/* 正确答案（参考答案）— 模板级，仅作为人工对照 */}
                    {isEditingAnswer ? (
                      <div className="mt-1.5 flex items-start gap-2">
                        <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider mt-2 flex-shrink-0">
                          ✓ 正确答案
                        </span>
                        <textarea
                          autoFocus
                          value={editAnswerText}
                          onChange={e => setEditAnswerText(e.target.value)}
                          onKeyDown={e => {
                            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); saveEditAnswer() }
                            if (e.key === 'Escape') { e.preventDefault(); cancelEditAnswer() }
                          }}
                          placeholder="期望 agent 给出的回答内容（自由文本，不参与自动判分）"
                          className="flex-1 border border-ink px-2 py-1 font-mono text-xs outline-none resize-y min-h-[48px] bg-canvas-alt/40"
                          rows={2}
                        />
                        <button
                          onClick={saveEditAnswer}
                          className="border border-accent bg-accent text-white px-2 py-1 font-mono text-[10px] font-bold hover:bg-accent/90"
                          title="保存 (⌘/Ctrl + Enter)"
                        >
                          <Check size={12} />
                        </button>
                        <button
                          onClick={cancelEditAnswer}
                          className="border border-border px-2 py-1 font-mono text-[10px] text-ink-ghost hover:text-ink"
                          title="取消 (Esc)"
                        >
                          <X size={12} />
                        </button>
                      </div>
                    ) : hasAnswer ? (
                      <div
                        className="mt-1 flex items-start gap-2 cursor-text group"
                        onDoubleClick={() => startEditAnswer(r)}
                      >
                        <span className="inline-flex items-center gap-0.5 font-mono text-[9px] text-green-700 font-bold tracking-wider mt-0.5 flex-shrink-0">
                          <CheckCircle2 size={10} /> 正确答案
                        </span>
                        <span className="font-mono text-[11px] text-ink-light flex-1 break-words leading-relaxed">
                          {r.expectedAnswer}
                        </span>
                        <button
                          onClick={() => startEditAnswer(r)}
                          className="text-ink-ghost hover:text-accent flex-shrink-0 opacity-0 group-hover:opacity-100"
                          title="编辑正确答案"
                        >
                          <Pencil size={10} />
                        </button>
                      </div>
                    ) : (
                      <button
                        onClick={() => startEditAnswer(r)}
                        className="mt-0.5 inline-flex items-center gap-1 font-mono text-[9px] text-ink-ghost hover:text-accent tracking-wider"
                        title="为此问题添加参考答案"
                      >
                        <CheckCircle2 size={10} /> 添加正确答案
                      </button>
                    )}
                  </div>
                  {/* Tags */}
                  <div className="flex-shrink-0 max-w-[40%]">
                    <TagChipEditor
                      tags={r.tags || []}
                      suiteTags={suiteTags}
                      onCommit={names => onCaseTagsChange(r.id, names)}
                    />
                  </div>
                  {/* Delete */}
                  <button
                    onClick={() => deleteOne(r.id)}
                    className="text-ink-ghost hover:text-red-500 flex-shrink-0 mt-1"
                    title="删除"
                  >
                    <Trash2 size={12} />
                  </button>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {selectedIds.length > 0 && (
        <BatchTagBar
          count={selectedIds.length}
          suiteTags={suiteTags}
          unionTags={batchUnionTags}
          onAdd={names => onBulkTag(selectedIds, names, [])}
          onRemove={names => onBulkTag(selectedIds, [], names)}
          onClear={clearChecked}
          onDelete={bulkDelete}
        />
      )}
    </div>
  )
}
