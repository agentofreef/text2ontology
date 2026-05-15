'use client'

import { useState } from 'react'
import { Plus, Upload, X } from 'lucide-react'

interface SuiteQuestionsBarProps {
  onUpload: (file: File) => void
  onAddPasted: (questions: string[]) => void
}

export function SuiteQuestionsBar({ onUpload, onAddPasted }: SuiteQuestionsBarProps) {
  const [showPaste, setShowPaste] = useState(false)
  const [pasteText, setPasteText] = useState('')

  const handleAdd = () => {
    const questions = pasteText.split('\n').map(l => l.trim()).filter(Boolean)
    if (questions.length === 0) return
    onAddPasted(questions)
    setPasteText('')
    setShowPaste(false)
  }

  return (
    <div className="border-b border-border">
      <div className="flex items-center gap-2 px-4 py-2 bg-canvas-alt/30">
        <span className="font-mono text-[9px] text-ink-ghost font-bold tracking-wider">QUESTIONS</span>
        <span className="flex-1" />
        <label className="flex items-center gap-1 border border-border px-2 py-0.5 font-mono text-[9px] text-ink-ghost hover:text-ink cursor-pointer">
          <Upload size={9} /> 上传 CSV/Excel
          <input type="file" accept=".csv,.xlsx,.xls" className="hidden"
            onChange={e => { const f = e.target.files?.[0]; if (f) onUpload(f); e.target.value = '' }} />
        </label>
        <button onClick={() => setShowPaste(!showPaste)}
          className="flex items-center gap-1 border border-border px-2 py-0.5 font-mono text-[9px] text-ink-ghost hover:text-ink">
          <Plus size={9} /> 粘贴问题
        </button>
      </div>

      {showPaste && (
        <div className="px-4 py-3 bg-canvas-alt space-y-2">
          <textarea
            className="w-full h-24 border border-border px-3 py-2 font-mono text-xs focus:outline-none focus:border-accent resize-none"
            placeholder="每行一个问题，例如：&#10;查询本月销售额&#10;按区域分组统计订单数"
            value={pasteText}
            onChange={e => setPasteText(e.target.value)}
          />
          <div className="flex items-center gap-2">
            <span className="font-mono text-[9px] text-ink-ghost">
              {pasteText.split('\n').filter(l => l.trim()).length} 个问题
            </span>
            <span className="flex-1" />
            <button onClick={handleAdd}
              disabled={!pasteText.trim()}
              className="border border-accent bg-accent text-white px-3 py-1 font-mono text-[10px] font-bold disabled:opacity-30">
              添加
            </button>
            <button onClick={() => { setShowPaste(false); setPasteText('') }}
              className="border border-border px-3 py-1 font-mono text-[10px] text-ink-ghost flex items-center gap-1">
              <X size={10} /> 取消
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
