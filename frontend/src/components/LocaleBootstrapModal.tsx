'use client'

import { useEffect, useState } from 'react'
import Image from 'next/image'

const LOCALE_KEY = 'omc-locale'
const BASE_PATH = '/lakehouse'

export function LocaleBootstrapModal() {
  const [mounted, setMounted] = useState(false)
  const [show, setShow] = useState(false)

  useEffect(() => {
    setMounted(true)
    if (typeof window === 'undefined') return
    // Defensive: if the URL ALREADY has a /lakehouse/{zh|en}/ segment,
    // we landed here because nginx fell back to the root index.html
    // (the [locale] index file wasn't found). Don't redirect — that
    // creates an infinite loop. Navigate user to the canonical landing
    // page inside their locale tree instead.
    const localeInPath = window.location.pathname.match(/^\/lakehouse\/(zh|en)(?:\/|$)/)
    if (localeInPath) {
      const locale = localeInPath[1]
      window.location.replace(`${BASE_PATH}/${locale}/ontology/lakehouse-agent`)
      return
    }
    try {
      const saved = window.localStorage.getItem(LOCALE_KEY)
      if (saved === 'zh' || saved === 'en') {
        window.location.replace(`${BASE_PATH}/${saved}/ontology/lakehouse-agent`)
        return
      }
    } catch {
      // localStorage blocked; fall through to modal
    }
    setShow(true)
  }, [])

  const choose = (locale: 'zh' | 'en') => {
    try {
      window.localStorage.setItem(LOCALE_KEY, locale)
    } catch {
      // ignore — still navigate
    }
    window.location.replace(`${BASE_PATH}/${locale}/`)
  }

  if (!mounted || !show) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-canvas">
        <span className="font-sans text-sm text-ink-ghost">Loading…</span>
      </div>
    )
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas px-4">
      <div className="w-full max-w-md rounded-xl border border-border bg-white p-8 shadow-sm">
        <div className="mb-6 flex items-center justify-center gap-3">
          <Image src="/lakehouse/logo.svg" alt="TEXT2ONTOLOGY" width={36} height={36} />
          <span className="font-sans text-lg font-semibold text-ink">TEXT2ONTOLOGY</span>
        </div>
        <h2 className="font-sans text-xl font-semibold text-ink text-center">
          选择语言 · Choose Language
        </h2>
        <p className="mt-1 text-sm text-ink-muted text-center">
          Select your preferred interface language
        </p>
        <div className="mt-6 grid grid-cols-2 gap-3">
          <button
            type="button"
            onClick={() => choose('zh')}
            className="rounded-lg border border-border bg-canvas px-4 py-3 text-sm font-medium text-ink transition-colors duration-150 hover:border-ink hover:bg-canvas-alt"
          >
            中文
          </button>
          <button
            type="button"
            onClick={() => choose('en')}
            className="rounded-lg border border-border bg-canvas px-4 py-3 text-sm font-medium text-ink transition-colors duration-150 hover:border-ink hover:bg-canvas-alt"
          >
            English
          </button>
        </div>
      </div>
    </div>
  )
}
