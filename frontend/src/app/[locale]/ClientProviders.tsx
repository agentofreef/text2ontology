'use client'

import { Sidebar } from '@/components/layout/Sidebar'
import { TasksDrawer } from '@/components/jobs/TasksDrawer'
import { CommandPalette } from '@/components/CommandPalette'
import { AuthProvider, useAuth } from '@/lib/auth'
import { ProjectProvider } from '@/lib/project'
import { MessageProvider } from '@/lib/message'
import { StyleModeProvider } from '@/lib/style-mode'
import { usePathname, useRouter } from '@/i18n/navigation'
import { useState, useEffect } from 'react'

function AppShell({ children }: { children: React.ReactNode }) {
  const { user, isLoading } = useAuth()
  const pathname = usePathname()
  const router = useRouter()
  // Sidebar defaults to the thin icon rail — this is a workbench, not a
  // browse-heavy app — and the choice is remembered across reloads. Cmd+K is
  // the primary navigator while the rail is collapsed.
  const [sidebarCollapsed, setSidebarCollapsed] = useState(true)
  const [cmdkOpen, setCmdkOpen] = useState(false)

  const isLoginPage = pathname === '/login'
  const isSetupWizard = pathname === '/setup-wizard'

  useEffect(() => {
    if (!isLoading && !user && !isLoginPage) {
      router.push('/login')
    }
  }, [user, isLoading, isLoginPage, router])

  // Hydrate the remembered collapse choice once on mount.
  useEffect(() => {
    try {
      const raw = localStorage.getItem('lakehouse2ontology_sidebar_collapsed')
      if (raw === '0') setSidebarCollapsed(false)
      else if (raw === '1') setSidebarCollapsed(true)
    } catch { /* localStorage unavailable */ }
  }, [])

  // Global ⌘K / Ctrl+K toggles the command palette (authenticated shell only).
  useEffect(() => {
    if (!user) return
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault()
        setCmdkOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [user])

  const toggleSidebar = () => {
    setSidebarCollapsed((v) => {
      const next = !v
      try { localStorage.setItem('lakehouse2ontology_sidebar_collapsed', next ? '1' : '0') } catch { /* ignore */ }
      return next
    })
  }

  if (isLoading) {
    return (
      <div className="flex h-screen items-center justify-center bg-canvas">
        <span className="font-sans text-sm text-ink-ghost">Loading...</span>
      </div>
    )
  }

  if (isLoginPage || isSetupWizard) {
    return <>{children}</>
  }

  if (!user) {
    return null
  }

  const fullHeightExactPaths = [
    '/ontology/agent',
    '/ontology/agent-v2',
    '/ontology/agent-sql',
    '/ontology/workbench',
    '/ontology/lakehouse-agent',
    '/ontology/lakehouse-keywords',
    '/ontology/lakehouse',
    '/ontology/lakehouse-objects',
    '/ontology/lakehouse-graph',
    '/ontology/lakehouse-keyword-triage',
    '/ontology/lakehouse-metrics',
    '/ontology/er-diagram',
    '/ontology/lakehouse-agent/token-recall',
    '/ontology/lakehouse-agent/annotations',
    '/ontology/lakehouse-agent/knowledge-learned',
    '/ontology/lakehouse-agent/flywheel',
    '/ontology/lakehouse-agent/history',
    '/ontology/sql-passthrough',
    '/ontology/lakehouse-sql',
    '/settings/mcp-keys',
    '/settings/data-sources',
    '/settings/data-sources/add',
    '/settings/data-sources/add/sqlite',
  ]
  const isAgentPage =
    fullHeightExactPaths.some((p) => pathname === p) ||
    pathname.startsWith('/ontology/lakehouse-agent/dataset-testing') ||
    pathname.startsWith('/ontology/lakehouse-metrics/')

  return (
    <div className="flex h-screen overflow-hidden">
      <Sidebar
        collapsed={sidebarCollapsed}
        onToggle={toggleSidebar}
        onOpenCommand={() => setCmdkOpen(true)}
      />
      <div className="flex flex-1 flex-col overflow-hidden">
        <main className={`flex-1 ${isAgentPage ? 'overflow-hidden' : 'overflow-y-auto p-6'}`}>
          {children}
        </main>
      </div>
      <TasksDrawer />
      <CommandPalette
        open={cmdkOpen}
        onClose={() => setCmdkOpen(false)}
        isAdmin={user?.role === 'admin'}
      />
    </div>
  )
}

export function ClientProviders({ children }: { children: React.ReactNode }) {
  // StyleModeProvider is outermost so even the login / setup-wizard pages
  // (which short-circuit AppShell) can read and toggle the style.
  return (
    <StyleModeProvider>
      <AuthProvider>
        <ProjectProvider>
          <MessageProvider>
            <AppShell>{children}</AppShell>
          </MessageProvider>
        </ProjectProvider>
      </AuthProvider>
    </StyleModeProvider>
  )
}
