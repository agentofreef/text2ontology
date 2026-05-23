'use client'

import { useEffect } from 'react'
import { useRouter } from '@/i18n/navigation'

// The graph + object list were merged into a single split-view page at
// /ontology/lakehouse-objects (see plan mellow-toasting-wirth). This route is
// kept so old bookmarks resolve — it redirects to the combined page.
export default function LakehouseGraphRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace('/ontology/lakehouse-objects')
  }, [router])
  return null
}
