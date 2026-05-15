'use client'

import { useLocale } from 'next-intl'
import { usePathname, useRouter } from '@/i18n/navigation'
import { routing, type Locale } from '@/i18n/routing'

const LOCALE_KEY = 'omc-locale'

export function LocaleSwitcher() {
  const locale = useLocale() as Locale
  const router = useRouter()
  const pathname = usePathname()

  const switchTo = (next: Locale) => {
    try {
      window.localStorage.setItem(LOCALE_KEY, next)
    } catch {
      /* ignore */
    }
    router.replace(pathname, { locale: next })
  }

  return (
    <div className="inline-flex items-center rounded-lg border border-border bg-canvas text-xs font-medium">
      {routing.locales.map((loc) => (
        <button
          key={loc}
          type="button"
          onClick={() => switchTo(loc)}
          className={`px-2.5 py-1 transition-colors duration-150 ${
            locale === loc
              ? 'bg-ink text-white'
              : 'text-ink-muted hover:bg-canvas-alt hover:text-ink'
          }`}
          aria-current={locale === loc ? 'true' : undefined}
        >
          {loc === 'zh' ? '中文' : 'EN'}
        </button>
      ))}
    </div>
  )
}
