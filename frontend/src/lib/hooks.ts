'use client'

import { useState, useEffect, useCallback, useRef } from 'react'
import { api } from './api'
import { useProject } from './project'
import type { ListResponse } from '@/types/api'

// useFetch / useFetchSingle — list & single-resource fetch with request-id
// guard. Every invocation gets a monotonically increasing id; only the
// latest request is allowed to call setState. This eliminates the class of
// bugs where the URL rapidly changes (e.g. currentProject settling after mount) and the earlier
// in-flight response arrives AFTER the later one, overwriting fresh data
// with stale. Also skips state writes after unmount.

export function useFetch<T>(path: string) {
  const [data, setData] = useState<T[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const { currentProject } = useProject()
  const reqIdRef = useRef(0)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  const fetchData = useCallback(async () => {
    const myId = ++reqIdRef.current
    setLoading(true)
    setError(null)
    try {
      const separator = path.includes('?') ? '&' : '?'
      const url = currentProject
        ? `${path}${separator}projectId=${currentProject.id}`
        : path
      const res = await api<ListResponse<T>>(url)
      if (!mountedRef.current || myId !== reqIdRef.current) return
      setData(res.data)
      setTotal(res.total)
    } catch (e) {
      if (!mountedRef.current || myId !== reqIdRef.current) return
      setError(e instanceof Error ? e.message : 'Unknown error')
    } finally {
      if (mountedRef.current && myId === reqIdRef.current) setLoading(false)
    }
  }, [path, currentProject])

  useEffect(() => {
    fetchData()
  }, [fetchData])

  return { data, total, loading, error, refetch: fetchData }
}

export function useFetchSingle<T>(path: string) {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const { currentProject } = useProject()
  const reqIdRef = useRef(0)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  const fetchData = useCallback(async () => {
    const myId = ++reqIdRef.current
    setLoading(true)
    setError(null)
    try {
      const separator = path.includes('?') ? '&' : '?'
      const url = currentProject
        ? `${path}${separator}projectId=${currentProject.id}`
        : path
      const res = await api<T>(url)
      if (!mountedRef.current || myId !== reqIdRef.current) return
      setData(res)
    } catch (e) {
      if (!mountedRef.current || myId !== reqIdRef.current) return
      setError(e instanceof Error ? e.message : 'Unknown error')
    } finally {
      if (mountedRef.current && myId === reqIdRef.current) setLoading(false)
    }
  }, [path, currentProject])

  useEffect(() => {
    fetchData()
  }, [fetchData])

  return { data, loading, error, refetch: fetchData }
}

export function useCreate<T>(path: string) {
  const [loading, setLoading] = useState(false)

  const create = async (body: Partial<T>): Promise<T> => {
    setLoading(true)
    try {
      return await api<T>(path, { method: 'POST', body })
    } finally {
      setLoading(false)
    }
  }

  return { create, loading }
}

export function useUpdate<T>(path: string) {
  const [loading, setLoading] = useState(false)

  const update = async (id: string, body: Partial<T>): Promise<T> => {
    setLoading(true)
    try {
      return await api<T>(`${path}/${id}`, { method: 'PUT', body })
    } finally {
      setLoading(false)
    }
  }

  return { update, loading }
}

export function useMarkToggle(basePath: string) {
  const toggle = async (id: string, newValue: boolean) => {
    await api(`${basePath}/${id}/mark`, {
      method: 'PUT',
      body: { mark: newValue },
    })
  }

  const batchMark = async (ids: string[], newValue: boolean) => {
    await Promise.all(ids.map((id) => toggle(id, newValue)))
  }

  return { toggle, batchMark }
}
