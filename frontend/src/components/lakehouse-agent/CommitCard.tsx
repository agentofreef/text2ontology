'use client'

// Explore-mode CommitCard (chat-first redesign Step 9).
//
// Renders an LLM-emitted metric draft as an inline chat bubble with editable
// (name / displayName / triggerKeywords) and read-only (canonical / SQL /
// parameters / primary OD / autoGroupBy / response template / description)
// facets. The 采纳 button performs the two-step PUT (dryRun then real)
// against /api/ontology/lakehouse-metrics/<id>; on validator rejection it
// surfaces {code, error, errors[]} inline and bubbles the rejection up to
// the parent so the next chat turn carries it (G9 self-correction).

import { useMemo, useState } from 'react'
import { Database, Brain, Code2, Tag, Sparkles, Layers, FileText, Check } from 'lucide-react'
import { useTranslations } from 'next-intl'
import { api } from '@/lib/api'
import {
  buildSavePayload,
  promoteBlocker,
  type CommitCardPayload,
} from '@/components/lakehouse-agent/commitCardLogic'

export interface ValidatorRejection {
  code: string
  error: string
  errors: string[]
}

export interface CommitCardProps {
  payload: CommitCardPayload
  // Called on validator rejection so ExploreChat can carry the rejection
  // into the next stream POST (G9).
  onRejection?: (r: ValidatorRejection) => void
  // Called on successful 采纳 — collapses the bubble in the parent.
  onAccepted?: (metricId: string) => void
}

type Phase = 'idle' | 'submitting' | 'accepted' | 'failed'

export function CommitCard({ payload, onRejection, onAccepted }: CommitCardProps) {
  const t = useTranslations('agent.main.explore.commit_card')

  // Editable fields (start from payload, persist locally until 采纳).
  const [name, setName] = useState(payload.name)
  const [displayName, setDisplayName] = useState(payload.displayName)
  const [keywordsText, setKeywordsText] = useState(
    (payload.triggerKeywords ?? []).join(', '),
  )
  const [phase, setPhase] = useState<Phase>('idle')
  const [rejection, setRejection] = useState<ValidatorRejection | null>(null)

  const editedPayload: CommitCardPayload = useMemo(
    () => ({
      ...payload,
      name,
      displayName,
      triggerKeywords: keywordsText
        .split(/[,，]/)
        .map((s) => s.trim())
        .filter(Boolean),
    }),
    [payload, name, displayName, keywordsText],
  )

  const blocker = useMemo(() => promoteBlocker(editedPayload), [editedPayload])
  const disabled = phase === 'submitting' || phase === 'accepted' || blocker !== null

  async function handleAccept() {
    if (disabled) return
    setPhase('submitting')
    setRejection(null)
    const savePayload = buildSavePayload(editedPayload, { promote: true })
    try {
      // Step 1: dryRun validates against pkg/sqlrewrite + validateIntentRemote
      await api(`/ontology/lakehouse-metrics/${payload.id}?dryRun=true`, {
        method: 'PUT',
        body: savePayload,
      })
      // Step 2: real PUT — flips mark=true and writes triggers in one tx
      await api(`/ontology/lakehouse-metrics/${payload.id}`, {
        method: 'PUT',
        body: savePayload,
      })
      setPhase('accepted')
      onAccepted?.(payload.id)
    } catch (e) {
      const err = e as Error & { payload?: unknown }
      const p = err.payload as Partial<ValidatorRejection> | null | undefined
      const r: ValidatorRejection = {
        code: typeof p?.code === 'string' ? p.code : 'unknown',
        error: typeof p?.error === 'string' ? p.error : err.message,
        errors: Array.isArray(p?.errors) ? p!.errors as string[] : [],
      }
      setRejection(r)
      setPhase('failed')
      onRejection?.(r)
    }
  }

  if (phase === 'accepted') {
    return (
      <div className="flex items-center gap-2 border border-border bg-canvas px-3 py-2 text-sm text-ink">
        <Check className="h-4 w-4 text-success" />
        <span>{t('accepted_bubble', { name: displayName || name })}</span>
      </div>
    )
  }

  return (
    <div className="border border-border bg-canvas-alt text-ink">
      <div className="flex items-center gap-2 border-b border-border bg-canvas px-3 py-2">
        <Sparkles className="h-4 w-4 text-accent" />
        <span className="text-sm font-semibold tracking-wide">CommitCard</span>
        <span className="ml-auto font-mono text-[11px] opacity-50">
          draft:{payload.draftId.slice(0, 8)}
        </span>
      </div>

      <div className="space-y-3 p-3">
        <EditableRow
          icon={<Tag className="h-3.5 w-3.5" />}
          label={t('edit_name_label')}
          value={name}
          onChange={setName}
          mono
          placeholder="snake_case_name"
        />
        <EditableRow
          icon={<Tag className="h-3.5 w-3.5" />}
          label={t('edit_display_name_label')}
          value={displayName}
          onChange={setDisplayName}
        />
        <EditableRow
          icon={<Tag className="h-3.5 w-3.5" />}
          label={t('edit_trigger_keywords_label')}
          value={keywordsText}
          onChange={setKeywordsText}
          placeholder="月营收, monthly revenue"
        />

        <ReadOnlyRow
          icon={<Database className="h-3.5 w-3.5" />}
          label={t('readonly_primary_od_label')}
          value={payload.primaryOd}
          mono
        />
        {/* Structured facets — the spec the LLM emitted (engine compiles SQL).
            Shown when present; canonical/SQL rows below are the derived form. */}
        {payload.intent && (
          <ReadOnlyRow
            icon={<Sparkles className="h-3.5 w-3.5" />}
            label="意图"
            value={payload.intent === 'enumerate' ? '列举 (enumerate)' : '度量 (aggregate)'}
          />
        )}
        {payload.measure && (
          <ReadOnlyRow
            icon={<Brain className="h-3.5 w-3.5" />}
            label="度量"
            value={`${payload.measure.agg}${payload.measure.column ? `(${payload.measure.column})` : '(*)'}`}
            mono
          />
        )}
        {payload.dimensions && payload.dimensions.length > 0 && (
          <ReadOnlyRow
            icon={<Layers className="h-3.5 w-3.5" />}
            label="维度"
            value={payload.dimensions.join(', ')}
            mono
          />
        )}
        {payload.filters && payload.filters.length > 0 && (
          <ReadOnlyRow
            icon={<Layers className="h-3.5 w-3.5" />}
            label="过滤"
            value={payload.filters.map((f) => `${f.prop} ${f.op} ${f.value}`).join(' AND ')}
            mono
          />
        )}
        <ReadOnlyRow
          icon={<Brain className="h-3.5 w-3.5" />}
          label={t('readonly_canonical_label')}
          value={payload.canonicalMetric}
          mono
        />
        <ReadOnlyRow
          icon={<Layers className="h-3.5 w-3.5" />}
          label={t('readonly_auto_group_by_label')}
          value={(payload.autoGroupBy ?? []).join(', ') || '—'}
        />
        <ReadOnlyBlock
          icon={<Code2 className="h-3.5 w-3.5" />}
          label={t('readonly_sql_label')}
          value={payload.querySql}
        />
        <ReadOnlyBlock
          icon={<Code2 className="h-3.5 w-3.5" />}
          label={t('readonly_parameters_label')}
          value={
            (payload.parameters ?? []).length === 0
              ? '—'
              : JSON.stringify(payload.parameters, null, 2)
          }
        />
        <ReadOnlyBlock
          icon={<FileText className="h-3.5 w-3.5" />}
          label={t('readonly_response_template_label')}
          value={payload.responseTemplate || '—'}
        />
        <ReadOnlyBlock
          icon={<FileText className="h-3.5 w-3.5" />}
          label={t('readonly_description_label')}
          value={payload.description || '—'}
        />
      </div>

      {rejection !== null && (
        <div className="border-t border-border bg-canvas px-3 py-2 text-xs text-danger">
          <div className="font-semibold">
            {t('error_label')} · {rejection.code}
          </div>
          <div className="mt-1">{rejection.error}</div>
          {rejection.errors.length > 0 && (
            <ul className="ml-4 mt-1 list-disc">
              {rejection.errors.map((line, i) => (
                <li key={i} className="font-mono">{line}</li>
              ))}
            </ul>
          )}
        </div>
      )}

      <div className="flex items-center justify-between gap-2 border-t border-border bg-canvas px-3 py-2">
        <div className="text-[11px] opacity-60">
          {blocker !== null
            ? t('blocked_reason', { reason: blocker })
            : ' '}
        </div>
        <button
          type="button"
          onClick={handleAccept}
          disabled={disabled}
          className="border border-accent bg-accent px-3 py-1 text-xs font-semibold text-canvas disabled:cursor-not-allowed disabled:opacity-40"
        >
          {phase === 'submitting' ? t('accepting') : t('accept_button')}
        </button>
      </div>
    </div>
  )
}

function EditableRow({
  icon,
  label,
  value,
  onChange,
  placeholder,
  mono,
}: {
  icon: React.ReactNode
  label: string
  value: string
  onChange: (v: string) => void
  placeholder?: string
  mono?: boolean
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-1.5 text-[11px] uppercase tracking-wide opacity-60">
        {icon}
        <span>{label}</span>
      </div>
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className={
          'w-full border border-border bg-canvas px-2 py-1 text-sm text-ink outline-none focus:border-accent ' +
          (mono ? 'font-mono' : '')
        }
      />
    </div>
  )
}

function ReadOnlyRow({
  icon,
  label,
  value,
  mono,
}: {
  icon: React.ReactNode
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-1.5 text-[11px] uppercase tracking-wide opacity-60">
        {icon}
        <span>{label}</span>
      </div>
      <div
        className={
          'w-full border border-border bg-canvas px-2 py-1 text-sm text-ink opacity-90 ' +
          (mono ? 'font-mono' : '')
        }
      >
        {value || '—'}
      </div>
    </div>
  )
}

function ReadOnlyBlock({
  icon,
  label,
  value,
}: {
  icon: React.ReactNode
  label: string
  value: string
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-1.5 text-[11px] uppercase tracking-wide opacity-60">
        {icon}
        <span>{label}</span>
      </div>
      <pre className="w-full overflow-x-auto whitespace-pre-wrap border border-border bg-canvas px-2 py-1 font-mono text-xs text-ink opacity-90">
        {value}
      </pre>
    </div>
  )
}
