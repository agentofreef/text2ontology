'use client'

import { LocaleBootstrapModal } from '@/components/LocaleBootstrapModal'

// Root entry `/lakehouse/`:
// - If localStorage has `omc-locale`, redirect to `/lakehouse/{locale}/`
// - Otherwise, show language picker modal which writes localStorage + navigates
// All logic lives in LocaleBootstrapModal (uses useEffect + window.location.replace).
export default function Home() {
  return <LocaleBootstrapModal />
}
