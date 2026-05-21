'use client'

import { createContext, useContext, useState, useEffect, useCallback, ReactNode } from 'react'
import type { Project } from '@/types/api'
import { getApiBase } from './api'
import { useAuth } from './auth'

interface ProjectContextType {
  projects: Project[]
  currentProject: Project | null
  switchProject: (project: Project) => void
  refetchProjects: () => Promise<void>
  isLoading: boolean
}

const ProjectContext = createContext<ProjectContextType | null>(null)

function authHeaders(): Record<string, string> {
  const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export function ProjectProvider({ children }: { children: ReactNode }) {
  const [projects, setProjects] = useState<Project[]>([])
  const [currentProject, setCurrentProject] = useState<Project | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const { token } = useAuth()

  const fetchProjects = useCallback(async () => {
    if (!token) {
      setProjects([])
      setCurrentProject(null)
      setIsLoading(false)
      return
    }
    const savedProjectId = localStorage.getItem('lakehouse2ontology_project_id')
    try {
      const r = await fetch(`${getApiBase()}/projects`, { headers: authHeaders() })
      if (r.status === 401) {
        setIsLoading(false)
        return
      }
      const res = await r.json()
      const list: Project[] = res.data || []
      setProjects(list)
      if (list.length > 0) {
        const saved = list.find((p) => p.id === savedProjectId)
        setCurrentProject(saved || list[0])
      } else {
        // The membership-filtered list is empty for this user — clear any
        // selection (and the stale id another user may have left in storage)
        // so the switcher doesn't keep showing a project they can't access.
        setCurrentProject(null)
        localStorage.removeItem('lakehouse2ontology_project_id')
      }
    } catch {
      // Fallback: no projects
    } finally {
      setIsLoading(false)
    }
  }, [token])

  useEffect(() => {
    fetchProjects()
  }, [fetchProjects])

  const switchProject = (project: Project) => {
    setCurrentProject(project)
    localStorage.setItem('lakehouse2ontology_project_id', project.id)
  }

  const refetchProjects = useCallback(async () => {
    try {
      const r = await fetch(`${getApiBase()}/projects`, { headers: authHeaders() })
      if (r.status === 401) return
      const res = await r.json()
      const list: Project[] = res.data || []
      setProjects(list)
    } catch {
      // ignore
    }
  }, [])

  return (
    <ProjectContext.Provider value={{ projects, currentProject, switchProject, refetchProjects, isLoading }}>
      {children}
    </ProjectContext.Provider>
  )
}

export function useProject() {
  const ctx = useContext(ProjectContext)
  if (!ctx) throw new Error('useProject must be used within ProjectProvider')
  return ctx
}
