'use client'

import { Link } from '@/i18n/navigation'
import Image from 'next/image'
import { usePathname, useRouter } from '@/i18n/navigation'
import {
  Tag, Search, MessageSquare, History,
  ChevronLeft, ChevronRight, LogOut, Settings, FolderOpen, ChevronDown, Cpu, Plus, Database,
  Box, BarChart3, Tags, Trash2, Filter,
  Network, Lightbulb, RotateCw, FlaskConical, Terminal, KeyRound, UserCog,
} from 'lucide-react'
import { useAuth } from '@/lib/auth'
import { useProject } from '@/lib/project'
import { api } from '@/lib/api'
import { useState, type ElementType } from 'react'
import { AnimatePresence, MotionFade } from '@/lib/motion'
import { useTranslations } from 'next-intl'
import { LocaleSwitcher } from '@/components/LocaleSwitcher'
import { useStyleMode } from '@/lib/style-mode'

interface SidebarProps {
  collapsed: boolean
  onToggle: () => void
}

// ─── Nav model ────────────────────────────────────────────────
// Two levels only:
//   一级 = NavGroup. May be a plain section label, OR a clickable nav row
//          (when href+icon set, the header itself is the entry to a page).
//   二级 = NavLeaf, rendered indented under its group.

type NavLeaf = {
  href: string
  label: string
  icon: ElementType
  // When true, the active highlight requires an exact path match. Used for
  // 湖仓 Agent (`/ontology/lakehouse-agent`) so its sibling sub-pages —
  // /history, /annotations, … — don't also light up the parent chat row.
  exact?: boolean
}

type NavGroup = {
  label: string
  items: NavLeaf[]
  // Optional: turns the group header into a clickable nav row pointing to `href`.
  // Used for 湖仓 Agent — the group label IS the entry to the agent page,
  // and `items` are its 6 sub-pages (二级).
  href?: string
  icon?: ElementType
}

function useLakehouseGroups(t: ReturnType<typeof useTranslations<'nav'>>): NavGroup[] {
  return [
    {
      label: t('data_assets'),
      items: [
        { href: '/ontology/lakehouse',         label: t('lakehouse'),       icon: Database },
        // ER Diagram: page lives at /ontology/er-diagram and still renders the
        // raw FK-relationship view, but it's hidden from the sidebar because
        // ontology design (OD / property / link) is the curated path users
        // are meant to take — the raw ER view duplicates what they'd see in
        // any DB GUI and adds little value here.
        // { href: '/ontology/er-diagram',        label: t('er_diagram'),      icon: Network  },
        { href: '/ontology/lakehouse-objects', label: 'Ontology',           icon: Box      },
        { href: '/ontology/lakehouse-graph',   label: t('property_graph'),  icon: Network  },
      ],
    },
    {
      label: t('knowledge_engineering'),
      items: [
        { href: '/ontology/lakehouse-keywords',         label: t('lakehouse_keywords'), icon: Tags      },
        { href: '/ontology/lakehouse-keyword-triage',   label: t('keyword_triage'),     icon: Filter    },
        { href: '/ontology/lakehouse-metric-intents',   label: t('metric_intents'),     icon: BarChart3 },
      ],
    },
    {
      // Group header is just 'Agent'; the chat page sits as a normal sub-item
      // alongside its siblings so the menu reads as "Agent → 湖仓 Agent / 对话历史 / ...".
      label: 'Agent',
      items: [
        { href: '/ontology/lakehouse-agent',                   label: t('lakehouse_agent'),   icon: MessageSquare, exact: true },
        { href: '/ontology/lakehouse-agent/history',           label: t('chat_history'),      icon: History       },
        { href: '/ontology/lakehouse-agent/annotations',       label: t('annotations'),       icon: Tag           },
        { href: '/ontology/lakehouse-agent/token-recall',      label: t('token_recall'),      icon: Search        },
        { href: '/ontology/lakehouse-agent/knowledge-learned', label: t('learned_knowledge'), icon: Lightbulb     },
        { href: '/ontology/lakehouse-agent/dataset-testing',   label: t('dataset_testing'),   icon: FlaskConical  },
        { href: '/ontology/lakehouse-agent/flywheel',          label: t('data_flywheel'),     icon: RotateCw      },
      ],
    },
    {
      label: 'SQL',
      items: [
        { href: '/ontology/sql-passthrough', label: 'Ontology SQL', icon: Terminal },
        { href: '/ontology/lakehouse-sql',   label: t('lakehouse_sql'), icon: Database },
      ],
    },
  ]
}

function useSystemGroup(t: ReturnType<typeof useTranslations<'nav'>>): NavGroup {
  return {
    label: t('system'),
    items: [
      { href: '/settings/data-sources',   label: t('data_sources'),      icon: Database  },
      // Prompt Engineering: page lives at /settings/prompt-config but is
      // hidden from the sidebar — current prompts are driven from llm-config
      // role bindings and DB-stored templates, so this page has no active
      // UX role yet. Keep the file in case it comes back for advanced users.
      // { href: '/settings/prompt-config',  label: t('prompt_engineering'), icon: Settings },
      { href: '/settings/llm-config',     label: t('llm_config'),         icon: Cpu      },
      { href: '/settings/mcp-keys',       label: t('mcp_keys'),           icon: KeyRound },
      { href: '/settings/preferences',    label: t('preferences'),        icon: UserCog  },
    ],
  }
}

// ─── Helpers ──────────────────────────────────────────────────

function isPathActive(pathname: string, href: string): boolean {
  return pathname === href || pathname.startsWith(href + '/')
}

function isExactActive(pathname: string, href: string): boolean {
  return pathname === href
}

// ─── Components ───────────────────────────────────────────────

function NavLeafLink({
  leaf, collapsed, isActive, indent,
}: {
  leaf: NavLeaf
  collapsed: boolean
  isActive: boolean
  indent: boolean
}) {
  const Icon = leaf.icon
  const industrial = useStyleMode().mode === 'industrial'
  // Industrial active style: inverse fill, no rounded, hairline left accent
  // when not active so the rail reads as an engineered surface, not a list.
  const stateCls = isActive
    ? industrial
      ? 'bg-ink text-white font-medium'
      : 'bg-canvas-alt text-ink font-medium'
    : industrial
      ? 'text-ink-muted hover:bg-canvas-alt hover:text-ink border-l-2 border-transparent hover:border-ink'
      : 'text-ink-muted hover:bg-canvas-alt hover:text-ink'
  return (
    <Link
      href={leaf.href}
      className={`flex items-center gap-3 text-sm transition-colors duration-150 ${
        collapsed ? 'justify-center px-0 py-2' : `py-1.5 ${indent ? 'pl-9 pr-3' : 'px-3'}`
      } ${stateCls}`}
      title={collapsed ? leaf.label : undefined}
    >
      <Icon className={`flex-shrink-0 ${indent ? 'h-3.5 w-3.5' : 'h-4 w-4'} ${isActive ? (industrial ? 'text-white' : 'text-ink') : 'text-ink-ghost'}`} />
      {!collapsed && (
        <span className={`${indent ? 'text-[13px]' : ''} ${industrial && !isActive ? 'font-mono text-[12px] tracking-[0.04em]' : ''}`}>
          {industrial ? leaf.label.toUpperCase() : leaf.label}
        </span>
      )}
    </Link>
  )
}

function NavGroupSection({
  group, pathname, collapsed,
}: {
  group: NavGroup
  pathname: string
  collapsed: boolean
}) {
  const [open, setOpen] = useState(true)
  const t = useTranslations('nav')

  if (collapsed) {
    // Collapsed sidebar: skip group chrome; flatten so every reachable route
    // is one icon-click away.
    const flat: NavLeaf[] = [
      ...(group.href && group.icon
        ? [{ href: group.href, label: group.label, icon: group.icon } as NavLeaf]
        : []),
      ...group.items,
    ]
    return (
      <div className="mb-1.5">
        {flat.map((leaf) => (
          <NavLeafLink
            key={leaf.href}
            leaf={leaf}
            collapsed
            isActive={leaf.exact ? isExactActive(pathname, leaf.href) : isPathActive(pathname, leaf.href)}
            indent={false}
          />
        ))}
      </div>
    )
  }

  // Expanded sidebar — single header style for ALL groups.
  // Label area is a <Link> when the group is itself a page entry (e.g. 湖仓 Agent),
  // otherwise plain text. Chevron always toggles. Visible state = `open` only,
  // so the user's fold choice always wins.
  const industrial = useStyleMode().mode === 'industrial'
  const labelClass = industrial
    ? 'flex-1 text-left font-mono text-[10px] uppercase tracking-[0.22em] text-ink-ghost hover:text-ink transition-colors'
    : 'flex-1 text-left text-[11px] font-semibold uppercase tracking-[0.08em] text-ink-light hover:text-ink transition-colors'
  const labelActive = group.href ? isExactActive(pathname, group.href) : false
  const labelText = industrial ? `// ${group.label.toUpperCase()}` : group.label

  return (
    <div className="mb-2">
      <div className="flex items-center px-3 py-1.5">
        {group.href ? (
          <Link
            href={group.href}
            className={`${labelClass} ${labelActive ? 'text-ink' : ''}`}
          >
            {labelText}
          </Link>
        ) : (
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className={labelClass}
          >
            {labelText}
          </button>
        )}
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex h-5 w-5 flex-shrink-0 items-center justify-center text-ink-ghost hover:text-ink transition-colors"
          aria-label={open ? t('collapse') : t('expand')}
        >
          <ChevronDown className={`h-3 w-3 transition-transform duration-150 ${open ? '' : '-rotate-90'}`} />
        </button>
      </div>
      {open && (
        <div className="mt-0.5">
          {group.items.map((leaf) => (
            <NavLeafLink
              key={leaf.href}
              leaf={leaf}
              collapsed={false}
              isActive={leaf.exact ? isExactActive(pathname, leaf.href) : isPathActive(pathname, leaf.href)}
              indent={false}
            />
          ))}
        </div>
      )}
    </div>
  )
}

// ─── Sidebar ──────────────────────────────────────────────────

export function Sidebar({ collapsed, onToggle }: SidebarProps) {
  const pathname = usePathname()
  const router = useRouter()
  const { user, logout } = useAuth()
  const { projects, currentProject, switchProject, refetchProjects } = useProject()
  const [projectDropdownOpen, setProjectDropdownOpen] = useState(false)
  const t = useTranslations('nav')
  const industrial = useStyleMode().mode === 'industrial'

  const handleLogout = () => {
    logout()
    router.push('/login')
  }

  // Every project on this branch funnels into the ontology layer regardless
  // of source type (postgres / pbi / file all produce lakehouse data, plus
  // legacy `pbit-lakehouse` / `pbix-lakehouse` projects). Show the full nav
  // whenever a project is selected; only hide it when the user hasn't picked
  // a project yet (in which case only the system group makes sense).
  const lakehouseGroups = useLakehouseGroups(t)
  const systemGroup = useSystemGroup(t)
  const groups: NavGroup[] = currentProject
    ? [...lakehouseGroups, systemGroup]
    : [systemGroup]

  return (
    <aside
      className={`flex flex-col bg-canvas transition-all duration-150 ${
        collapsed ? 'w-16' : 'w-60'
      } ${industrial ? 'border-r-2 border-ink' : 'border-r border-border'}`}
      style={{ transition: 'width 150ms linear' }}
    >
      {/* Brand header */}
      <div className={`flex h-14 items-center justify-between px-4 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
        {!collapsed && (
          <div className="flex items-center gap-2.5">
            <Image src="/logo.svg" alt="text2ontology" width={24} height={24} />
            <span className={industrial ? 'font-mono text-[12px] font-bold tracking-[0.06em] text-ink' : 'font-sans text-sm font-semibold text-ink'}>
              text2ontology
            </span>
          </div>
        )}
        {collapsed && (
          <Image src="/logo.svg" alt="text2ontology" width={22} height={22} className="mx-auto" />
        )}
        <button
          onClick={onToggle}
          className="flex h-6 w-6 items-center justify-center text-ink-ghost hover:text-ink transition-colors"
        >
          {collapsed ? <ChevronRight className="h-4 w-4" /> : <ChevronLeft className="h-4 w-4" />}
        </button>
      </div>

      {/* Project Switcher */}
      {!collapsed && (
        <div className={`relative px-3 py-2 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
          <button
            onClick={() => setProjectDropdownOpen(!projectDropdownOpen)}
            className={`flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-left transition-colors duration-150 ${
              industrial
                ? 'border border-ink hover:bg-canvas-alt'
                : 'rounded hover:bg-canvas-alt'
            }`}
          >
            <div className="flex items-center gap-2 overflow-hidden">
              <FolderOpen className="h-3.5 w-3.5 flex-shrink-0 text-ink-ghost" />
              <div className="truncate">
                <div className={industrial ? 'truncate font-mono text-[11px] font-bold tracking-[0.06em] text-ink' : 'truncate text-xs font-medium text-ink'}>
                  {industrial
                    ? `[${(currentProject?.name || 'NO PROJECT').toUpperCase()}]`
                    : (currentProject?.name || 'Select Project')}
                </div>
                <div className={industrial ? 'font-mono text-[9px] tracking-[0.22em] text-ink-ghost' : 'text-[9px] text-ink-ghost'}>
                  {industrial ? '// PROJECT' : 'PROJECT'}
                </div>
              </div>
            </div>
            <ChevronDown
              className={`h-3 w-3 flex-shrink-0 text-ink-ghost transition-transform duration-150 ${
                projectDropdownOpen ? 'rotate-180' : ''
              }`}
            />
          </button>
          <AnimatePresence>
            {projectDropdownOpen && (
              <MotionFade className={`absolute left-3 right-3 z-50 mt-1 bg-canvas ${industrial ? 'border-2 border-ink' : 'rounded border border-border shadow-sm'}`}>
                {projects.map((p) => (
                  <div
                    key={p.id}
                    className={`flex w-full items-center justify-between px-2.5 py-2 text-xs ${
                      currentProject?.id === p.id ? 'text-ink font-medium' : 'text-ink-muted'
                    } hover:bg-canvas-alt transition-colors`}
                  >
                    <button
                      onClick={() => {
                        switchProject(p)
                        setProjectDropdownOpen(false)
                      }}
                      className="flex flex-1 items-center gap-2 text-left"
                    >
                      <span
                        className={`h-1.5 w-1.5 rounded-full flex-shrink-0 ${
                          currentProject?.id === p.id ? 'bg-ink' : 'bg-border'
                        }`}
                      />
                      <span className="truncate">{p.name}</span>
                    </button>
                    <button
                      onClick={async (e) => {
                        e.stopPropagation()
                        if (!confirm(t('delete_project_confirm', { name: p.name }))) return
                        try {
                          await api(`/projects/${p.id}`, { method: 'DELETE' })
                          refetchProjects()
                          if (currentProject?.id === p.id && projects.length > 1) {
                            const next = projects.find(x => x.id !== p.id)
                            if (next) switchProject(next)
                          }
                        } catch { /* ignore */ }
                      }}
                      className="ml-1 flex-shrink-0 p-0.5 text-ink-ghost hover:text-danger transition-colors"
                      title={t('delete_project')}
                    >
                      <Trash2 className="h-3 w-3" />
                    </button>
                  </div>
                ))}
                <button
                  onClick={() => {
                    setProjectDropdownOpen(false)
                    router.push('/setup-wizard')
                  }}
                  className="flex w-full items-center gap-2 border-t border-border px-2.5 py-2 text-left text-xs text-ink-muted hover:bg-canvas-alt hover:text-ink transition-colors"
                >
                  <Plus className="h-3 w-3 flex-shrink-0" />
                  {t('new_project')}
                </button>
              </MotionFade>
            )}
          </AnimatePresence>
        </div>
      )}

      {/* Navigation */}
      <nav className="flex-1 overflow-y-auto py-2">
        {groups.map((group) => (
          <NavGroupSection
            key={group.label}
            group={group}
            pathname={pathname}
            collapsed={collapsed}
          />
        ))}
      </nav>

      {/* Locale Switcher (above user row, expanded mode only) */}
      {!collapsed && (
        <div className="border-t border-border px-3 py-2">
          <LocaleSwitcher />
        </div>
      )}

      {/* User & Logout */}
      <div className="border-t border-border p-3">
        {!collapsed ? (
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <div className="flex h-7 w-7 items-center justify-center rounded-full bg-canvas-alt text-xs font-semibold text-ink">
                {user?.displayName?.charAt(0)?.toUpperCase() || 'U'}
              </div>
              <div>
                <div className="text-xs font-medium text-ink">{user?.displayName || 'User'}</div>
                <div className="text-[9px] text-ink-ghost">{user?.role?.toUpperCase() || 'USER'}</div>
              </div>
            </div>
            <div className="flex items-center gap-1">
              <button
                onClick={handleLogout}
                className="flex h-7 w-7 items-center justify-center text-ink-ghost hover:text-ink transition-colors"
                title={t('logout')}
              >
                <LogOut className="h-3.5 w-3.5" />
              </button>
            </div>
          </div>
        ) : (
          <div className="flex flex-col items-center gap-2">
            <button
              onClick={handleLogout}
              className="mx-auto flex h-8 w-8 items-center justify-center text-ink-ghost hover:text-ink transition-colors"
              title={t('logout')}
            >
              <LogOut className="h-4 w-4" />
            </button>
          </div>
        )}
      </div>
    </aside>
  )
}
