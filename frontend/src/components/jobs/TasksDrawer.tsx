'use client'

import { useState, useEffect } from 'react'
import { AnimatePresence } from 'motion/react'
import { useJobs } from '@/lib/jobs'
import { useProject } from '@/lib/project'
import { TasksDrawerFAB } from './TasksDrawerFAB'
import { TasksDrawerPanel } from './TasksDrawerPanel'

// Global Tasks Drawer.
//
// Mounted once in AppShell (frontend/src/app/layout.tsx). Reads the current
// project from useProject(), polls /api/jobs every 2s, and renders a
// fixed-position FAB + panel.
//
// Visibility rule: hidden entirely when there are no jobs to show. As soon
// as the user enqueues anything (or there's recent history within 24h), the
// FAB appears bottom-right.
export function TasksDrawer() {
  const { currentProject } = useProject()
  const projectId = currentProject?.id ?? null
  const { jobs, runningCount, cancel } = useJobs(projectId)
  const [open, setOpen] = useState(false)

  // Auto-open the panel the first time a running job appears, so users see
  // immediate feedback after kicking off an upload. Only does this once per
  // mount to avoid annoying re-pops.
  const [autoOpenedOnce, setAutoOpenedOnce] = useState(false)
  useEffect(() => {
    if (!autoOpenedOnce && runningCount > 0) {
      setOpen(true)
      setAutoOpenedOnce(true)
    }
  }, [runningCount, autoOpenedOnce])

  // Hide entirely if no project context or zero jobs.
  if (!projectId || jobs.length === 0) return null

  return (
    <>
      <TasksDrawerFAB
        runningCount={runningCount}
        totalCount={jobs.length}
        open={open}
        onClick={() => setOpen((v) => !v)}
      />
      <AnimatePresence>
        {open && (
          <TasksDrawerPanel
            jobs={jobs}
            runningCount={runningCount}
            onClose={() => setOpen(false)}
            onCancel={cancel}
          />
        )}
      </AnimatePresence>
    </>
  )
}
