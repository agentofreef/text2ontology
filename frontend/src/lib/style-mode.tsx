'use client'

// Global UI style mode — lets the user toggle between the original "classic"
// look (rounded cards, soft shadows) and the "industrial" look introduced with
// docs/cover.svg + the new login page (sharp corners, hairline borders, mono
// trace labels). Persisted to localStorage so the choice survives reload.
//
// Wiring:
//   1. <StyleModeProvider> wraps the tree (in ClientProviders).
//   2. Any client component reads via `useStyleMode()`.
//   3. The provider also writes `data-style="industrial"` on <html>, so plain
//      CSS / Tailwind arbitrary variants can target it without prop-drilling.

import { createContext, useContext, useEffect, useState, useCallback } from 'react'

export type StyleMode = 'classic' | 'industrial'

const STORAGE_KEY = 'lakehouse2ontology_style_mode'
const DEFAULT_MODE: StyleMode = 'classic'

type Ctx = {
  mode: StyleMode
  setMode: (m: StyleMode) => void
  toggle: () => void
}

const StyleModeContext = createContext<Ctx | null>(null)

export function StyleModeProvider({ children }: { children: React.ReactNode }) {
  const [mode, setModeState] = useState<StyleMode>(DEFAULT_MODE)

  // Hydrate from localStorage once on mount.
  useEffect(() => {
    try {
      const raw = localStorage.getItem(STORAGE_KEY)
      if (raw === 'classic' || raw === 'industrial') {
        setModeState(raw)
      }
    } catch {
      // localStorage unavailable (private mode, SSR fallback) — keep default.
    }
  }, [])

  // Mirror to <html data-style="..."> + persist.
  useEffect(() => {
    if (typeof document !== 'undefined') {
      document.documentElement.setAttribute('data-style', mode)
    }
    try {
      localStorage.setItem(STORAGE_KEY, mode)
    } catch {
      // ignore
    }
  }, [mode])

  const setMode = useCallback((m: StyleMode) => setModeState(m), [])
  const toggle = useCallback(
    () => setModeState((m) => (m === 'industrial' ? 'classic' : 'industrial')),
    [],
  )

  return (
    <StyleModeContext.Provider value={{ mode, setMode, toggle }}>
      {children}
    </StyleModeContext.Provider>
  )
}

export function useStyleMode(): Ctx {
  const ctx = useContext(StyleModeContext)
  if (!ctx) {
    // Defensive fallback for components rendered outside the provider (e.g.
    // login page before AuthProvider wraps in some routing edge cases).
    return {
      mode: DEFAULT_MODE,
      setMode: () => {},
      toggle: () => {},
    }
  }
  return ctx
}

// Tiny helper for ergonomic conditional className composition.
//   className={cx('base', industrial && 'border-ink', classic && 'rounded-lg')}
export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ')
}
