'use client'

import { useEffect } from 'react'
import { useRouter } from '@/i18n/navigation'
import { useAuth } from '@/lib/auth'

export default function LocaleHome() {
  const { user, isLoading } = useAuth()
  const router = useRouter()

  useEffect(() => {
    if (!isLoading) {
      router.replace(user ? '/ontology/lakehouse-agent' : '/login')
    }
  }, [user, isLoading, router])

  return null
}
