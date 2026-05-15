'use client'

// NOTE: This component is currently unmounted from the AppShell — the project's
// design avoids a default global top breadcrumb. SaaS-level controls (UI style,
// language, password) live in `/settings/preferences` instead.
// Kept here as a reusable building block in case a page wants a top metadata
// strip of its own.

import { useAuth } from '@/lib/auth'
import { useProject } from '@/lib/project'

export function Header() {
  const { user } = useAuth()
  const { currentProject } = useProject()

  return (
    <header className="flex h-14 items-center justify-end border-b border-border bg-canvas px-6">
      <div className="flex items-center gap-3">
        {currentProject && (
          <>
            <span className="text-sm text-ink-muted">{currentProject.name}</span>
            <div className="h-4 w-px bg-border" />
          </>
        )}
        {user && <span className="text-sm text-ink-muted">{user.displayName}</span>}
      </div>
    </header>
  )
}
