import { tStatic } from './i18nLite'
import { mapErrorString } from './errorMap'

export function getBasePath(): string {
  if (typeof window === 'undefined') return ''
  const m = window.location.pathname.match(/^(\/lakehouse)/)
  return m ? m[1] : ''
}

// Parses current locale segment from window.location.pathname.
// Used by api.ts where we have no React context to read useLocale().
// Falls back to localStorage['omc-locale'], then 'zh'.
export function getCurrentLocale(): 'zh' | 'en' {
  if (typeof window === 'undefined') return 'zh'
  const m = window.location.pathname.match(/^\/lakehouse\/(zh|en)(?:\/|$)/)
  if (m) return m[1] as 'zh' | 'en'
  try {
    const saved = window.localStorage.getItem('omc-locale')
    if (saved === 'zh' || saved === 'en') return saved
  } catch {
    /* localStorage unavailable */
  }
  return 'zh'
}

// Phase 4C.3 per-route routing. Build-time env (all optional — leave
// unset to keep the legacy same-origin + monolith gateway behavior):
//
//   NEXT_PUBLIC_BACKEND_API_URL    backend-api :18090 (CRUD, auth,
//                                  projects, config, ingest, export).
//                                  Default when only one override is
//                                  set and a path doesn't match a more
//                                  specific service.
//   NEXT_PUBLIC_AGENT_SERVER_URL   agent-server :18092 (SSE turns,
//                                  threads, lh-testing, ledger,
//                                  annotations, token-recall-tokenize).
//   NEXT_PUBLIC_RECALL_SERVER_URL  recall-server :18093 (token-recall-
//                                  debug operator inspector only).
//
// The path argument passed to api() starts with the suffix AFTER /api
// (e.g. "/ontology/versions", "/ontology/lakehouse-agent-stream",
// "/auth/login"). matchAgentPath / matchRecallPath check prefixes to
// pick the right override. Everything else falls through to backend-api
// or monolith-default.

function matchAgentPath(path: string): boolean {
  return (
    path.startsWith('/ontology/lakehouse-agent-') ||
    path.startsWith('/ontology/lh-test-') ||
    path.startsWith('/ontology/_debug/ledger-rebuild') ||
    path.startsWith('/ontology/lakehouse-ledger') ||
    path.startsWith('/ontology/lakehouse-missions') ||
    path.startsWith('/ontology/agent-annotations') ||
    path.startsWith('/ontology/lakehouse-token-recall-tokenize')
  )
}

function matchRecallPath(path: string): boolean {
  return path.startsWith('/ontology/lakehouse-token-recall-debug')
}

function trimTrailingSlash(s: string): string {
  return s.replace(/\/$/, '')
}

// getApiBaseFor resolves the effective API base for a given path. See
// the doc block above for the env var contract.
export function getApiBaseFor(path: string): string {
  const agent = process.env.NEXT_PUBLIC_AGENT_SERVER_URL
  const recall = process.env.NEXT_PUBLIC_RECALL_SERVER_URL
  const backend = process.env.NEXT_PUBLIC_BACKEND_API_URL

  if (agent && matchAgentPath(path)) return `${trimTrailingSlash(agent)}/api`
  if (recall && matchRecallPath(path)) return `${trimTrailingSlash(recall)}/api`
  if (backend) return `${trimTrailingSlash(backend)}/api`

  // No overrides (or no match + no backend override) → legacy monolith
  // gateway path: same-origin + /lakehouse/api.
  if (typeof window === 'undefined') return '/api'
  return `${window.location.origin}${getBasePath()}/api`
}

// getApiBase is kept as a path-unaware shim so older call sites keep
// working. New code should prefer getApiBaseFor(path) when an override
// would route that path to a non-default service.
export function getApiBase(): string {
  return getApiBaseFor('')
}

// authToken reads the stored session bearer token (SSR-safe). The agent SSE
// stream and the upload SSE endpoint are auth-gated server-side (authmw bearer),
// so streaming requests must carry the same Authorization header that api() does.
function authToken(): string | null {
  if (typeof window === 'undefined') return null
  return localStorage.getItem('lakehouse2ontology_token')
}

interface RequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown
}

export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { body, headers, ...rest } = options
  const token = typeof window !== 'undefined' ? localStorage.getItem('lakehouse2ontology_token') : null
  const res = await fetch(`${getApiBaseFor(path)}${path}`, {
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...headers,
    },
    body: body ? JSON.stringify(body) : undefined,
    ...rest,
  })

  if (res.status === 401) {
    // Token invalid/expired — force logout
    if (typeof window !== 'undefined') {
      localStorage.removeItem('lakehouse2ontology_token')
      localStorage.removeItem('lakehouse2ontology_user')
      window.location.href = `${getBasePath()}/${getCurrentLocale()}/login`
    }
    throw new Error(tStatic('errors.auth.token_expired'))
  }

  if (!res.ok) {
    // Try to parse the body as JSON so error messages set by the backend
    // (e.g. {"error":"该配置被 8 个测试运行引用..."}) reach the UI's
    // MessageBar instead of a generic "API Error: 409 Conflict".
    let detail = ''
    let payload: unknown = null
    try {
      const text = await res.text()
      if (text) {
        try {
          payload = JSON.parse(text)
          if (payload && typeof payload === 'object' && 'error' in payload) {
            detail = String((payload as { error: unknown }).error)
          } else {
            detail = text
          }
        } catch {
          detail = text
        }
      }
    } catch {
      // ignore body-read errors
    }
    // Translate known backend zh error strings to the current locale via errorMap.
    // Falls back to the raw detail if no pattern matches.
    let msg = detail || `${res.status} ${res.statusText}`
    if (detail) {
      const mapped = mapErrorString(detail)
      if (mapped) msg = tStatic(mapped.key, mapped.vars)
    }
    const err = new Error(msg) as Error & { status?: number; payload?: unknown }
    err.status = res.status
    err.payload = payload
    throw err
  }

  return res.json()
}

// apiCollector is a convenience wrapper for /api/connector/* paths.
// Collector-server is proxied same-origin via nginx at /lakehouse/api/connector/*,
// so no extra base URL is needed — the standard api() helper already resolves correctly.
export function apiCollector<T>(path: string, opts?: RequestOptions): Promise<T> {
  const normalized = path.startsWith('/') ? path : `/${path}`
  return api<T>(`/connector${normalized}`, opts)
}

export async function apiStream(
  path: string,
  body: unknown,
  onChunk: (text: string) => void,
  onDone?: () => void
): Promise<void> {
  const token = authToken()

  // Bounded connect-retry: a transient network failure (proxy hiccup, brief
  // offline) while OPENING the stream is retried with backoff. We only retry
  // before the first chunk reaches the caller — once data has been delivered,
  // a blind re-POST could duplicate side effects, so we surface the error
  // instead. (The endpoint is non-idempotent; true mid-stream resume would
  // need server-side Last-Event-ID support.)
  const maxAttempts = 3
  let lastErr: unknown
  for (let attempt = 0; attempt < maxAttempts; attempt++) {
    let res: Response
    try {
      res = await fetch(`${getApiBaseFor(path)}${path}`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          // The agent stream endpoint is auth-gated (authmw bearer); send the
          // session token so direct browser→agent-server streaming authenticates.
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify(body),
      })
    } catch (e) {
      // Network-level failure opening the connection → retry with backoff.
      lastErr = e
      if (attempt < maxAttempts - 1) {
        await new Promise(r => setTimeout(r, 500 * (attempt + 1)))
        continue
      }
      throw e
    }

    if (!res.ok || !res.body) {
      throw new Error(`Stream Error: ${res.status}`)
    }

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let receivedAny = false
    try {
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        receivedAny = true
        onChunk(decoder.decode(value, { stream: true }))
      }
    } catch (e) {
      // Mid-stream drop after data was delivered: cannot safely re-POST a
      // non-idempotent request, so propagate. A drop with nothing delivered
      // yet is retryable via the outer loop.
      if (receivedAny) throw e
      lastErr = e
      if (attempt < maxAttempts - 1) {
        await new Promise(r => setTimeout(r, 500 * (attempt + 1)))
        continue
      }
      throw e
    }

    onDone?.()
    return
  }
  throw lastErr instanceof Error ? lastErr : new Error('Stream Error')
}

export interface SSEEvent {
  phase: string
  done?: number
  total?: number
  inserted?: number
  vectorized?: number
  skipped?: number
  table?: string
  index?: number
  totalTables?: number
  error?: string
  details?: Record<string, unknown>
}

export async function apiSSE(
  path: string,
  formData: FormData,
  onEvent: (event: SSEEvent) => void,
): Promise<void> {
  const token = authToken()
  const res = await fetch(`${getApiBaseFor(path)}${path}`, {
    method: 'POST',
    // Do NOT set Content-Type — the browser sets the multipart boundary for
    // FormData automatically. Only attach the auth bearer (the upload endpoint
    // is auth-gated). This is a one-shot, non-idempotent file upload, so it is
    // intentionally NOT auto-reconnected: replaying it would re-upload the file.
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    body: formData,
  })

  if (!res.ok || !res.body) {
    // Try to parse error JSON from non-SSE response
    try {
      const errData = await res.json()
      throw new Error(errData.error || `Upload failed: ${res.status}`)
    } catch (e) {
      if (e instanceof Error && e.message.startsWith('Upload failed')) throw e
      throw new Error(`Upload failed: ${res.status}`)
    }
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    // Parse SSE lines
    const lines = buffer.split('\n')
    buffer = lines.pop() || ''

    for (const line of lines) {
      const trimmed = line.trim()
      if (trimmed.startsWith('data: ')) {
        try {
          const event = JSON.parse(trimmed.slice(6)) as SSEEvent
          onEvent(event)
        } catch {
          // skip malformed events
        }
      }
    }
  }

  // Process remaining buffer
  if (buffer.trim().startsWith('data: ')) {
    try {
      const event = JSON.parse(buffer.trim().slice(6)) as SSEEvent
      onEvent(event)
    } catch {
      // skip
    }
  }
}
