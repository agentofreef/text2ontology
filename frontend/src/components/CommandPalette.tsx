'use client'

// Cmd+K command palette — the primary navigation surface once the sidebar
// collapses to a thin icon rail. It lists EVERY destination, including the
// three tools folded out of the sidebar (Token 召回 / 对话历史 / 标注), so they
// stay one keystroke away. No external dependency: built from the same nav
// model + dual style-mode tokens used by the sidebar.

import { useState, useEffect, useMemo, useRef } from 'react'
import { useRouter } from '@/i18n/navigation'
import { useTranslations } from 'next-intl'
import { useStyleMode } from '@/lib/style-mode'
import {
  Upload, Box, Database, Tags, Filter, BarChart3, Bot,
  FlaskConical, Search, History, PenLine, Lightbulb, RotateCw, Terminal,
  Cpu, KeyRound, UserCog, Users, CornerDownLeft, Crosshair, ScrollText, type LucideIcon,
} from 'lucide-react'

type Dest = { href: string; label: string; icon: LucideIcon }
type Section = { label: string; items: Dest[] }

export function CommandPalette({
  open, onClose, isAdmin,
}: {
  open: boolean
  onClose: () => void
  isAdmin: boolean
}) {
  const t = useTranslations('nav')
  const tc = useTranslations('command')
  const router = useRouter()
  const industrial = useStyleMode().mode === 'industrial'
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)

  const sections: Section[] = useMemo(() => {
    const sys: Dest[] = [
      { href: '/settings/llm-config',    label: t('llm_config'),         icon: Cpu        },
      { href: '/settings/prompt-config', label: t('prompt_engineering'), icon: ScrollText },
      { href: '/settings/mcp-keys',      label: t('mcp_keys'),           icon: KeyRound   },
      { href: '/settings/preferences',   label: t('preferences'),        icon: UserCog    },
    ]
    if (isAdmin) sys.push({ href: '/settings/users', label: t('user_management'), icon: Users })
    return [
      { label: t('mode_ingest'), items: [
        { href: '/settings/data-sources', label: t('data_sources'), icon: Upload },
      ] },
      { label: t('ontology'), items: [
        { href: '/ontology/lakehouse-objects',        label: t('objects'),            icon: Box       },
        { href: '/ontology/lakehouse',                label: t('lakehouse'),          icon: Database  },
        { href: '/ontology/lakehouse-keywords',       label: t('lakehouse_keywords'), icon: Tags      },
        { href: '/ontology/lakehouse-keyword-triage', label: t('keyword_triage'),     icon: Filter    },
        { href: '/ontology/lakehouse-metric-intents', label: t('metric_intents'),     icon: BarChart3 },
      ] },
      { label: t('mode_workbench'), items: [
        { href: '/ontology/lakehouse-agent',                   label: t('lakehouse_agent'),   icon: Bot          },
        { href: '/ontology/lakehouse-agent/history',           label: t('chat_history'),      icon: History      },
        { href: '/ontology/lakehouse-agent/annotations',       label: t('annotations'),       icon: PenLine      },
        { href: '/ontology/lakehouse-agent/token-recall',      label: t('token_recall'),      icon: Crosshair    },
        { href: '/ontology/lakehouse-agent/dataset-testing',   label: t('dataset_testing'),   icon: FlaskConical },
        { href: '/ontology/lakehouse-agent/knowledge-learned', label: t('learned_knowledge'), icon: Lightbulb    },
        { href: '/ontology/lakehouse-agent/flywheel',          label: t('data_flywheel'),     icon: RotateCw     },
      ] },
      { label: 'SQL', items: [
        { href: '/ontology/sql-passthrough', label: t('sql_passthrough'), icon: Terminal },
        { href: '/ontology/lakehouse-sql',   label: t('lakehouse_sql'),   icon: Database },
      ] },
      { label: t('system'), items: sys },
    ]
  }, [t, isAdmin])

  const filtered: Section[] = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return sections
    return sections
      .map((s) => ({
        label: s.label,
        items: s.items.filter(
          (i) => i.label.toLowerCase().includes(q) || s.label.toLowerCase().includes(q),
        ),
      }))
      .filter((s) => s.items.length > 0)
  }, [sections, query])

  const flat: Dest[] = useMemo(() => filtered.flatMap((s) => s.items), [filtered])

  // Reset + focus each time the palette opens.
  useEffect(() => {
    if (!open) return
    setQuery('')
    setActive(0)
    const id = setTimeout(() => inputRef.current?.focus(), 0)
    return () => clearTimeout(id)
  }, [open])

  // Keep the highlight in range as the filter narrows.
  useEffect(() => { setActive(0) }, [query])

  if (!open) return null

  const go = (href: string) => { onClose(); router.push(href) }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') { e.preventDefault(); onClose() }
    else if (e.key === 'ArrowDown') { e.preventDefault(); setActive((a) => Math.min(a + 1, flat.length - 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((a) => Math.max(a - 1, 0)) }
    else if (e.key === 'Enter') { e.preventDefault(); const d = flat[active]; if (d) go(d.href) }
  }

  const panelCls = industrial ? 'border-2 border-ink' : 'border border-border rounded-lg shadow-lg'
  const inputCls = industrial ? 'border-b-2 border-ink' : 'border-b border-border'

  // Running index that ties each rendered row to its position in `flat` so the
  // keyboard highlight and the rendered list stay in sync.
  let row = -1

  return (
    <div
      className="fixed inset-0 z-[100] flex items-start justify-center bg-black/30 pt-[12vh]"
      onClick={onClose}
    >
      <div
        className={`w-full max-w-lg overflow-hidden bg-canvas ${panelCls}`}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <div className={`flex items-center gap-2 px-3 ${inputCls}`}>
          <Search className="h-4 w-4 flex-shrink-0 text-ink-ghost" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={tc('placeholder')}
            className="w-full bg-transparent py-3 text-sm text-ink placeholder:text-ink-ghost focus:outline-none"
          />
          <kbd className="flex-shrink-0 text-[10px] text-ink-ghost">ESC</kbd>
        </div>

        <div className="max-h-[50vh] overflow-y-auto py-1">
          {flat.length === 0 && (
            <div className="px-3 py-6 text-center text-xs text-ink-ghost">{tc('empty')}</div>
          )}
          {filtered.map((s) => (
            <div key={s.label} className="mb-1">
              <div className={`px-3 py-1 text-[10px] uppercase tracking-[0.08em] text-ink-ghost ${industrial ? 'font-mono' : 'font-semibold'}`}>
                {industrial ? `// ${s.label.toUpperCase()}` : s.label}
              </div>
              {s.items.map((d) => {
                row += 1
                const myRow = row
                const isActive = myRow === active
                const Icon = d.icon
                return (
                  <button
                    key={d.href}
                    onMouseEnter={() => setActive(myRow)}
                    onClick={() => go(d.href)}
                    className={`flex w-full items-center gap-3 px-3 py-2 text-left text-sm transition-colors ${
                      isActive
                        ? industrial ? 'bg-ink text-white' : 'bg-canvas-alt text-ink'
                        : 'text-ink-muted hover:bg-canvas-alt'
                    }`}
                  >
                    <Icon className={`h-4 w-4 flex-shrink-0 ${isActive && industrial ? 'text-white' : 'text-ink-ghost'}`} />
                    <span className="flex-1 truncate">{d.label}</span>
                    {isActive && <CornerDownLeft className={`h-3.5 w-3.5 ${industrial ? 'text-white' : 'text-ink-ghost'}`} />}
                  </button>
                )
              })}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
