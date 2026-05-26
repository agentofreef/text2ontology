// extractApiError — turn a thrown api() error into a user-facing message for
// the 指标 create/update flows. api() attaches `.status` and `.payload` to the
// Error, so we can pull the structured backend codes:
//   400 {code:"NO_TRIGGERS"}        → "触发词不能为空…"
//   400 {code, errors:[...]}        → joined validation messages
// Falls back to the Error's own message otherwise.

type Translator = (key: string, vars?: Record<string, string | number>) => string

interface ApiErrorPayload {
  code?: string
  error?: string
  errors?: Array<string | { field?: string; message?: string }> | Record<string, string>
}

export function extractApiError(e: unknown, t: Translator): string {
  const err = e as Error & { payload?: unknown; status?: number }
  const payload = (err?.payload ?? null) as ApiErrorPayload | null

  if (payload && typeof payload === 'object') {
    if (payload.code === 'NO_TRIGGERS') {
      return t('error_no_triggers')
    }
    const parts = normalizeErrors(payload.errors)
    if (parts.length > 0) {
      return t('error_validation', { detail: parts.join('; ') })
    }
    if (payload.error) return payload.error
  }

  return err?.message || t('error_unknown')
}

function normalizeErrors(errors: ApiErrorPayload['errors']): string[] {
  if (!errors) return []
  if (Array.isArray(errors)) {
    return errors
      .map(item => {
        if (typeof item === 'string') return item
        if (item && typeof item === 'object') {
          const field = item.field ? `${item.field}: ` : ''
          return `${field}${item.message ?? ''}`.trim()
        }
        return ''
      })
      .filter(Boolean)
  }
  // Record<field, message>
  return Object.entries(errors).map(([k, v]) => `${k}: ${v}`)
}
