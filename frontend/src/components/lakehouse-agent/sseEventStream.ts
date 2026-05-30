// Shared SSE parser for the lakehouse-agent stream endpoint.
//
// The agent-server emits Server-Sent Events as `data: {...JSON...}\n\n` lines,
// dispatched by the JSON's `type` field. `apiStream()` in lib/api.ts hands us
// raw chunks; this module buffers across chunk boundaries and parses each
// `data: ` line into a typed event, calling onEvent per event.
//
// Plain async function (not a React hook) — usable from both the legacy
// lakehouse page.tsx and the new ExploreChat.tsx without React-tree coupling.

import { apiStream } from '@/lib/api'

export interface SseEvent {
  type: string
  [k: string]: unknown
}

export async function streamSseEvents(
  path: string,
  body: unknown,
  onEvent: (ev: SseEvent) => void,
  onDone?: () => void,
): Promise<void> {
  let buffer = ''
  await apiStream(
    path,
    body,
    (chunk) => {
      buffer += chunk
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''
      for (const line of lines) {
        if (!line.startsWith('data: ')) continue
        const json = line.slice(6).trim()
        if (!json || json === '[DONE]') continue
        try {
          const parsed = JSON.parse(json) as SseEvent
          if (parsed && typeof parsed.type === 'string') onEvent(parsed)
        } catch {
          // Swallow malformed/heartbeat lines (e.g. SSE ping comments).
        }
      }
    },
    onDone,
  )
}
