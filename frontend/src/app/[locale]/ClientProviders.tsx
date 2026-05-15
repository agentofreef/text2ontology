'use client'

import { Sidebar } from '@/components/layout/Sidebar'
import { TasksDrawer } from '@/components/jobs/TasksDrawer'
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
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)

  const isLoginPage = pathname === '/login'
  const isSetupWizard = pathname === '/setup-wizard'

  useEffect(() => {
    if (!isLoading && !user && !isLoginPage) {
      router.push('/login')
    }
  }, [user, isLoading, isLoginPage, router])

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
    '/ontology/lakehouse-metric-intents',
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
    pathname.startsWith('/ontology/lakehouse-metric-intents/')

  return (
    <div className="flex h-screen overflow-hidden">
      <Sidebar
        collapsed={sidebarCollapsed}
        onToggle={() => setSidebarCollapsed(!sidebarCollapsed)}
      />
      <div className="flex flex-1 flex-col overflow-hidden">
        <main className={`flex-1 ${isAgentPage ? 'overflow-hidden' : 'overflow-y-auto p-6'}`}>
          {children}
        </main>
      </div>
      <TasksDrawer />
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
