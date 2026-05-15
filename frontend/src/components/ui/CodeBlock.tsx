'use client'

interface CodeBlockProps {
  code: string
  language?: string
  maxHeight?: string
  className?: string
}

export function CodeBlock({ code, language = 'DAX', maxHeight = '200px', className = '' }: CodeBlockProps) {
  return (
    <div className={`border border-border-light bg-canvas-alt rounded-md ${className}`}>
      <div className="flex items-center justify-between border-b border-border-light px-3 py-1.5">
        <span className="font-mono text-[9px] font-semibold tracking-wider text-ink-ghost">
          {language}
        </span>
        <button
          onClick={() => navigator.clipboard?.writeText(code)}
          className="font-mono text-[9px] text-ink-ghost hover:text-accent"
        >
          COPY
        </button>
      </div>
      <pre
        className="overflow-auto p-3 font-mono leading-relaxed text-ink-light text-[12px] font-normal"
        style={{ maxHeight }}
      >
        <code>{code}</code>
      </pre>
    </div>
  )
}
