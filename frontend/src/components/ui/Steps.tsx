'use client'

interface Step {
  label: string
  description?: string
}

interface StepsProps {
  steps: Step[]
  current: number
  className?: string
}

export function Steps({ steps, current, className = '' }: StepsProps) {
  return (
    <div className={`flex items-start gap-0 ${className}`}>
      {steps.map((step, i) => {
        const isActive = i === current
        const isDone = i < current
        return (
          <div key={i} className="flex flex-1 items-start">
            <div className="flex flex-col items-center">
              <div
                className={`flex h-8 w-8 items-center justify-center font-mono text-xs font-bold rounded-full ${
                  isDone
                    ? 'bg-ink text-white'
                    : isActive
                    ? 'border-2 border-accent bg-white text-accent'
                    : 'border border-gray-200 bg-gray-50 text-gray-400'
                }`}
              >
                {isDone ? '✓' : String(i + 1).padStart(2, '0')}
              </div>
              <div className="mt-2 text-center">
                <div className={`font-mono text-[10px] font-semibold uppercase tracking-wider ${
                  isActive ? 'text-accent' : isDone ? 'text-ink' : 'text-ink-ghost'
                }`}>
                  {step.label}
                </div>
                {step.description && (
                  <div className="mt-0.5 text-[10px] text-ink-ghost">{step.description}</div>
                )}
              </div>
            </div>
            {i < steps.length - 1 && (
              <div className={`mt-4 h-px flex-1 ${isDone ? 'bg-ink' : 'bg-border'}`} />
            )}
          </div>
        )
      })}
    </div>
  )
}
