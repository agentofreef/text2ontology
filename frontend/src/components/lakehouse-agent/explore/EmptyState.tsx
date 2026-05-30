'use client'

// EmptyState — industrial-style first impression for the explore tab.
// Shows the loop diagram + sample prompts. Ink frame, mono labels, no rounding.

import { Sparkles, MessageSquare, Search, FileCheck, ArrowRight } from 'lucide-react'

interface Props {
  onPrompt: (text: string) => void
}

const SAMPLES = [
  { title: '燕麦奶断供影响营收', detail: 'BOM 反推被波及的菜品 + 累计营收估算' },
  { title: '上海各店本月营收 TOP10', detail: '门店 × 期间 × 营收 → 按城市切' },
  { title: '客单价按渠道对比', detail: '堂食 vs 外卖 vs 美团 vs 饿了么' },
  { title: '哪些菜品依赖咖啡豆', detail: 'BOM 正查 + 用量统计' },
] as const

const STEPS = [
  { icon: MessageSquare, label: '你描述问题' },
  { icon: Search, label: 'AI 探索数据' },
  { icon: Sparkles, label: '草稿浮现于右栏' },
  { icon: FileCheck, label: '采纳 = 一条新口径' },
]

export function EmptyState({ onPrompt }: Props) {
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-10 px-6 py-12">
      {/* Hero */}
      <div className="flex flex-col items-center gap-3 text-center">
        <div className="flex h-12 w-12 items-center justify-center border border-ink bg-canvas-alt">
          <Sparkles className="h-6 w-6 text-ink" strokeWidth={1.5} />
        </div>
        <h2 className="font-mono text-[15px] tracking-[0.15em] uppercase text-ink">
          和 AI 一起,把分析意图沉淀成 metric
        </h2>
        <p className="max-w-md font-mono text-[11.5px] leading-relaxed text-ink-muted">
          每一次会话的产物不是数字,而是 <span className="text-ink">一条可入库、可复用的口径</span>。
          查询模式下,你或其他人能直接召回。
        </p>
      </div>

      {/* Step ribbon */}
      <div className="flex items-center justify-center gap-2">
        {STEPS.map((s, i) => {
          const Icon = s.icon
          return (
            <div key={s.label} className="flex items-center gap-2">
              <div className="flex flex-col items-center gap-1.5">
                <div className="flex h-9 w-9 items-center justify-center border border-ink bg-white">
                  <Icon className="h-4 w-4 text-ink" strokeWidth={1.75} />
                </div>
                <span className="font-mono text-[9.5px] tracking-[0.08em] uppercase text-ink-muted">{s.label}</span>
              </div>
              {i < STEPS.length - 1 && <ArrowRight className="h-3 w-3 text-ink-muted" />}
            </div>
          )
        })}
      </div>

      {/* Sample prompts */}
      <div>
        <div className="mb-3 flex items-center gap-1.5 font-mono text-[10px] tracking-[0.22em] uppercase text-ink-muted">
          <span>试一个</span>
          <span className="h-px flex-1 bg-ink" />
        </div>
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          {SAMPLES.map((s) => (
            <button
              key={s.title}
              type="button"
              onClick={() => onPrompt(s.title)}
              className="group flex flex-col gap-1 border border-ink bg-white px-4 py-3 text-left transition-all hover:bg-canvas-alt"
            >
              <span className="font-mono text-[12px] tracking-[0.04em] text-ink">{s.title}</span>
              <span className="font-mono text-[10.5px] text-ink-muted">{s.detail}</span>
              <span className="mt-1 inline-flex items-center gap-1 font-mono text-[10px] tracking-[0.1em] uppercase text-ink opacity-0 transition-opacity group-hover:opacity-100">
                点击发送
                <ArrowRight className="h-2.5 w-2.5" />
              </span>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
