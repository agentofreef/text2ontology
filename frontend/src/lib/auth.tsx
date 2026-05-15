'use client'

import { createContext, useContext, useState, useEffect, ReactNode } from 'react'
import { tStatic } from './i18nLite'

interface User {
  username: string
  displayName: string
  role: string
}

interface AuthContextType {
  user: User | null
  token: string | null
  isLoading: boolean
  login: (username: string, password: string) => Promise<{ success: boolean; error?: string }>
  logout: () => void
}

const AuthContext = createContext<AuthContextType | null>(null)

// One-time migration of legacy text2dax_* localStorage keys to lakehouse2ontology_*.
// Copies old value to new key (if new key absent), then removes old key.
// Safe to re-run — no-op if already migrated or keys never existed.
function migrateLegacyStorage() {
  if (typeof window === 'undefined') return
  const prefix = 'text2dax_'
  const pairs: [string, string][] = [
    [prefix + 'token', 'lakehouse2ontology_token'],
    [prefix + 'user', 'lakehouse2ontology_user'],
    [prefix + 'project_id', 'lakehouse2ontology_project_id'],
  ]
  for (const [oldKey, newKey] of pairs) {
    const oldVal = localStorage.getItem(oldKey)
    if (oldVal !== null) {
      if (localStorage.getItem(newKey) === null) {
        localStorage.setItem(newKey, oldVal)
      }
      localStorage.removeItem(oldKey)
    }
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [token, setToken] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState(true)

  useEffect(() => {
    try {
      migrateLegacyStorage()
      const savedToken = localStorage.getItem('lakehouse2ontology_token')
      const savedUser = localStorage.getItem('lakehouse2ontology_user')
      if (savedToken && savedUser) {
        try {
          const parsedUser = JSON.parse(savedUser)
          setToken(savedToken)
          setUser(parsedUser)
        } catch {
          // Corrupt or stale user payload (e.g. legacy non-JSON value migrated
          // from text2dax_user). Clear both so the user is forced to re-login
          // cleanly instead of getting stuck on the INITIALIZING splash.
          localStorage.removeItem('lakehouse2ontology_token')
          localStorage.removeItem('lakehouse2ontology_user')
        }
      }
    } finally {
      // ALWAYS unblock the AppShell splash, even if storage threw.
      setIsLoading(false)
    }
  }, [])

  const login = async (username: string, password: string) => {
    try {
      const { getApiBase } = await import('./api')
      const res = await fetch(`${getApiBase()}/auth/login`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })
      const data = await res.json()
      if (data.success) {
        setToken(data.token)
        setUser(data.user)
        localStorage.setItem('lakehouse2ontology_token', data.token)
        localStorage.setItem('lakehouse2ontology_user', JSON.stringify(data.user))
        return { success: true }
      }
      return { success: false, error: data.error || tStatic('auth.login_failed') }
    } catch {
      return { success: false, error: tStatic('auth.network_error') }
    }
  }

  const logout = () => {
    setUser(null)
    setToken(null)
    localStorage.removeItem('lakehouse2ontology_token')
    localStorage.removeItem('lakehouse2ontology_user')
  }

  return (
    <AuthContext.Provider value={{ user, token, isLoading, login, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
