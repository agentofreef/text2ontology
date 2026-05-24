'use client'

import { useEffect, useState } from 'react'
import { useTranslations } from 'next-intl'
import { useProject } from '@/lib/project'
import { api } from '@/lib/api'
import type { OntObjectType } from '@/types/api'

// Lakehouse-Agent empty-state home: brand logo + a small at-a-glance dashboard
// of ontology health (objects / keywords / example questions / described-property
// ratio). Replaces the old starter-question chips. Mounts only while there is no
// conversation, so its few read-only fetches run at most once per visit.
interface DashStats {
  objects: number
  active: number
  keywords: number
  examples: number
  propsTotal: number
  propsDescribed: number
}

export function HomeDashboard() {
  const t = useTranslations('agent.main')
  const { currentProject } = useProject()
  const [s, setS] = useState<DashStats | null>(null)

  useEffect(() => {
    if (!currentProject) return
    const pid = currentProject.id
    let cancelled = false
    void (async () => {
      const [objs, kw, suites] = await Promise.all([
        api<{ data: OntObjectType[] }>(`/ontology/objects?projectId=${pid}`).catch(() => ({ data: [] as OntObjectType[] })),
        api<{ total: number }>(`/connector/pbit/lakehouse-keywords/summary?projectId=${pid}`).catch(() => ({ total: 0 })),
        api<{ data: { caseCount: number }[] }>(`/ontology/lh-test-suites?projectId=${pid}`).catch(() => ({ data: [] as { caseCount: number }[] })),
      ])
      if (cancelled) return
      const list = objs.data || []
      let propsTotal = 0
      let propsDescribed = 0
      for (const o of list) {
        for (const p of o.properties || []) {
          propsTotal++
          if ((p.description || '').trim()) propsDescribed++
        }
      }
      setS({
        objects: list.length,
        active: list.filter((o) => o.mark).length,
        keywords: kw.total || 0,
        examples: (suites.data || []).reduce((n, x) => n + (x.caseCount || 0), 0),
        propsTotal,
        propsDescribed,
      })
    })()
    return () => { cancelled = true }
  }, [currentProject])

  const pct = s && s.propsTotal > 0 ? Math.round((s.propsDescribed / s.propsTotal) * 100) : 0
  const cards: { label: string; value: string; sub?: string }[] = [
    { label: t('dashboard.objects'), value: s ? String(s.objects) : '—', sub: s ? t('dashboard.objects_active', { count: s.active }) : undefined },
    { label: t('dashboard.keywords'), value: s ? String(s.keywords) : '—' },
    { label: t('dashboard.examples'), value: s ? String(s.examples) : '—' },
    { label: t('dashboard.described'), value: s ? `${pct}%` : '—', sub: s ? `${s.propsDescribed} / ${s.propsTotal}` : undefined },
  ]

  return (
    <div className="flex w-full max-w-2xl flex-col items-center gap-6 px-6">
      {/* Brand logo (same mark as the login poster). */}
      <svg viewBox="0 0 32 32" fill="none" className="h-14 w-14" aria-hidden="true">
        <rect x="3" y="3" width="18" height="18" fill="#D4D4D4" />
        <line x1="9" y1="9" x2="16" y2="9" stroke="#171717" strokeWidth="0.9" />
        <line x1="9" y1="9" x2="12" y2="16" stroke="#171717" strokeWidth="0.9" />
        <line x1="16" y1="9" x2="12" y2="16" stroke="#171717" strokeWidth="0.9" />
        <circle cx="9" cy="9" r="1.6" fill="#171717" />
        <circle cx="16" cy="9" r="1.6" fill="#171717" />
        <circle cx="12" cy="16" r="1.6" fill="#171717" />
        <rect x="13" y="13" width="16" height="16" fill="#171717" />
        <line x1="16" y1="18" x2="26" y2="18" stroke="#D4D4D4" strokeWidth="0.9" />
        <line x1="16" y1="22" x2="26" y2="22" stroke="#D4D4D4" strokeWidth="0.9" />
        <line x1="16" y1="26" x2="26" y2="26" stroke="#D4D4D4" strokeWidth="0.9" />
      </svg>

      <div className="text-center">
        <div className="text-base font-semibold text-ink">{t('chat.empty_title_lakehouse')}</div>
        <div className="mx-auto mt-1 max-w-sm text-xs text-ink-muted">{t('chat.empty_hint_lakehouse')}</div>
      </div>

      {/* Ontology-health stat cards. */}
      <div className="grid w-full grid-cols-2 gap-3 sm:grid-cols-4">
        {cards.map((c) => (
          <div
            key={c.label}
            className="flex flex-col items-center gap-1 rounded-md border border-border bg-canvas-alt px-3 py-4"
          >
            <span className="text-2xl font-semibold tabular-nums text-ink">{c.value}</span>
            <span className="text-center text-[10px] uppercase tracking-[0.12em] text-ink-ghost">{c.label}</span>
            {c.sub && <span className="text-[10px] tabular-nums text-ink-muted">{c.sub}</span>}
          </div>
        ))}
      </div>
    </div>
  )
}
