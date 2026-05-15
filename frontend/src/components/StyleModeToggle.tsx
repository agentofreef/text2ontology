'use client'

// Global UI style switcher. Two-state segmented control [ CLASSIC | INDUSTRIAL ].
// Mounted in the top-bar Header so it follows the user across every page.
//
// The toggle itself uses minimal, style-neutral chrome (hairline border, mono
// label, sharp corners) so it looks at home in both themes — it should not
// itself "redesign" when the mode flips.

import { useStyleMode } from '@/lib/style-mode'

export function StyleModeToggle() {
  const { mode, setMode } = useStyleMode()

  const baseBtn =
    'px-2.5 py-1 font-mono text-[10px] tracking-[0.18em] transition-colors duration-150'
  const active = 'bg-ink text-white'
  const inactive = 'text-ink-muted hover:text-ink'

  return (
    <div
      className="flex items-stretch border border-ink/30"
      role="group"
      aria-label="UI style"
      title="Switch UI style — persists across reloads"
    >
      <button
        type="button"
        onClick={() => setMode('classic')}
        className={`${baseBtn} ${mode === 'classic' ? active : inactive}`}
        aria-pressed={mode === 'classic'}
      >
        CLASSIC
      </button>
      <button
        type="button"
        onClick={() => setMode('industrial')}
        className={`${baseBtn} border-l border-ink/30 ${mode === 'industrial' ? active : inactive}`}
        aria-pressed={mode === 'industrial'}
      >
        INDUSTRIAL
      </button>
    </div>
  )
}
