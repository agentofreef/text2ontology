'use client'

import { useEffect } from 'react'

export function LangSync({ locale }: { locale: string }) {
  useEffect(() => {
    if (typeof document !== 'undefined') {
      document.documentElement.lang = locale === 'zh' ? 'zh-CN' : locale
    }
  }, [locale])
  return null
}
