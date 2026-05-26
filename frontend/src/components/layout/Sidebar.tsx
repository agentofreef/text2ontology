'use client'

import { Link } from '@/i18n/navigation'
import Image from 'next/image'
import { usePathname, useRouter } from '@/i18n/navigation'
import {
  ChevronLeft, ChevronRight, LogOut, Settings, FolderOpen, ChevronDown, Plus, Database,
  Box, BarChart3, LineChart, Tags, Trash2, Filter,
  Lightbulb, RotateCw, FlaskConical, Terminal, KeyRound, UserCog, Users,
  Upload, Bot, Search, History, Crosshair, PenLine, ScrollText,
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
  onOpenCommand?: () => void
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

// Expanded sidebar shows the COMPLETE catalogue: every mode is a section label
// whose pages are explicit, labelled leaves — INCLUDING the mode's primary page.
// Nothing is hidden behind a header-click ("expanded = all pages"). The thin
// collapsed rail (rendered separately in <Sidebar>) deliberately shows only the
// 4 debug-loop destinations (search / Agent / objects / token-recall).
function useLakehouseGroups(t: ReturnType<typeof useTranslations<'nav'>>): NavGroup[] {
  return [
    {
      // 接入 — phase-1 ingest. One-time per project.
      label: t('mode_ingest'),
      items: [
        { href: '/settings/data-sources', label: t('data_sources'), icon: Upload },
      ],
    },
    {
      // 本体 — the curated model + all curation levers.
      label: t('ontology'),
      items: [
        { href: '/ontology/lakehouse-objects',        label: t('objects'),            icon: Box,       exact: true },
        { href: '/ontology/lakehouse',                label: t('lakehouse'),          icon: Database  },
        { href: '/ontology/lakehouse-keywords',       label: t('lakehouse_keywords'), icon: Tags      },
        { href: '/ontology/lakehouse-keyword-triage', label: t('keyword_triage'),     icon: Filter    },
        { href: '/ontology/lakehouse-metrics',        label: t('metrics'),            icon: LineChart },
      ],
    },
    {
      // 工作台 — the diagnose-first agent loop. The Agent chat is the primary
      // leaf (exact-match highlight so its sub-pages don't also light it up);
      // 对话历史 / 标注 / Token 召回 are restored as explicit leaves.
      label: t('mode_workbench'),
      items: [
        { href: '/ontology/lakehouse-agent',                   label: t('lakehouse_agent'),   icon: Bot,         exact: true },
        { href: '/ontology/lakehouse-agent/history',           label: t('chat_history'),      icon: History      },
        { href: '/ontology/lakehouse-agent/annotations',       label: t('annotations'),       icon: PenLine      },
        { href: '/ontology/lakehouse-agent/token-recall',      label: t('token_recall'),      icon: Crosshair    },
        { href: '/ontology/lakehouse-agent/dataset-testing',   label: t('dataset_testing'),   icon: FlaskConical },
        { href: '/ontology/lakehouse-agent/knowledge-learned', label: t('learned_knowledge'), icon: Lightbulb    },
        { href: '/ontology/lakehouse-agent/flywheel',          label: t('data_flywheel'),     icon: RotateCw     },
      ],
    },
    {
      // SQL — Ontology SQL 直通 (the "ontology circle": Od names as tables,
      // links as JOINs) + raw 湖仓 SQL.
      label: 'SQL',
      items: [
        { href: '/ontology/sql-passthrough', label: t('sql_passthrough'), icon: Terminal },
        { href: '/ontology/lakehouse-sql',   label: t('lakehouse_sql'),   icon: Database },
      ],
    },
  ]
}

// 系统 — config mode. LLM 配置 is now an explicit leaf (was only the header),
// alongside Prompt 工程 / MCP 密钥 / 偏好设置 / 用户管理.
function useSystemGroup(t: ReturnType<typeof useTranslations<'nav'>>, isAdmin: boolean): NavGroup {
  const items: NavLeaf[] = [
    { href: '/settings/llm-config',    label: t('llm_config'),         icon: Settings   },
    { href: '/settings/prompt-config', label: t('prompt_engineering'), icon: ScrollText },
    { href: '/settings/mcp-keys',      label: t('mcp_keys'),           icon: KeyRound   },
    { href: '/settings/preferences',   label: t('preferences'),        icon: UserCog    },
  ]
  // User management is admin-only; the backend also gates every /api/admin/*
  // call, so hiding the nav is convenience, not the security boundary.
  if (isAdmin) {
    items.push({ href: '/settings/users', label: t('user_management'), icon: Users })
  }
  return { label: t('system'), items }
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
      prefetch={false}
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
    // Collapsed rail = ONE icon per mode (the group header). Sub-items are
    // reached via Cmd+K or by expanding the rail — this keeps the rail to a
    // handful of icons instead of mirroring the whole route list.
    if (group.href && group.icon) {
      return (
        <div className="mb-1.5">
          <NavLeafLink
            leaf={{ href: group.href, label: group.label, icon: group.icon }}
            collapsed
            isActive={isPathActive(pathname, group.href)}
            indent={false}
          />
        </div>
      )
    }
    // Fallback for any header-less group: show its items directly.
    return (
      <div className="mb-1.5">
        {group.items.map((leaf) => (
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

  // Expanded sidebar — the mode header is a REAL nav row (icon + label,
  // clickable to the mode's primary page), so it shows the very page the
  // collapsed rail shows as an icon. Sub-pages render indented beneath it, and
  // the chevron toggles them. So expanded ⊇ collapsed (nothing is lost).
  const industrial = useStyleMode().mode === 'industrial'
  const Icon = group.icon
  const headerActive = group.href ? isExactActive(pathname, group.href) : false

  return (
    <div className="mb-2">
      <div className="flex items-center">
        {group.href && Icon ? (
          <Link
            href={group.href}
            prefetch={false}
            className={`flex flex-1 items-center gap-3 px-3 py-1.5 text-sm transition-colors duration-150 ${
              headerActive
                ? industrial ? 'bg-ink text-white font-medium' : 'bg-canvas-alt text-ink font-medium'
                : 'text-ink-muted hover:bg-canvas-alt hover:text-ink'
            }`}
          >
            <Icon className={`h-4 w-4 flex-shrink-0 ${headerActive ? (industrial ? 'text-white' : 'text-ink') : 'text-ink-ghost'}`} />
            <span className={industrial ? 'font-mono text-[12px] tracking-[0.04em]' : 'font-medium'}>
              {industrial ? group.label.toUpperCase() : group.label}
            </span>
          </Link>
        ) : (
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className={`flex-1 px-3 py-1.5 text-left ${industrial ? 'font-mono text-[10px] uppercase tracking-[0.22em] text-ink-ghost' : 'text-[11px] font-semibold uppercase tracking-[0.08em] text-ink-light'}`}
          >
            {industrial ? `// ${group.label.toUpperCase()}` : group.label}
          </button>
        )}
        {group.items.length > 0 && (
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="flex h-5 w-5 flex-shrink-0 items-center justify-center text-ink-ghost hover:text-ink transition-colors"
            aria-label={open ? t('collapse') : t('expand')}
          >
            <ChevronDown className={`h-3 w-3 transition-transform duration-150 ${open ? '' : '-rotate-90'}`} />
          </button>
        )}
      </div>
      {open && group.items.length > 0 && (
        <div className="mt-0.5">
          {group.items.map((leaf) => (
            <NavLeafLink
              key={leaf.href}
              leaf={leaf}
              collapsed={false}
              isActive={leaf.exact ? isExactActive(pathname, leaf.href) : isPathActive(pathname, leaf.href)}
              indent={true}
            />
          ))}
        </div>
      )}
    </div>
  )
}

// ─── Sidebar ──────────────────────────────────────────────────

export function Sidebar({ collapsed, onToggle, onOpenCommand }: SidebarProps) {
  const pathname = usePathname()
  const router = useRouter()
  const { user, logout } = useAuth()
  const { projects, currentProject, switchProject, refetchProjects } = useProject()
  const [projectDropdownOpen, setProjectDropdownOpen] = useState(false)
  const t = useTranslations('nav')
  const tc = useTranslations('command')
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
  const systemGroup = useSystemGroup(t, user?.role === 'admin')
  const groups: NavGroup[] = currentProject
    ? [...lakehouseGroups, systemGroup]
    : [systemGroup]

  // Collapsed rail = exactly the 4 debug-loop destinations. The 搜索 button is
  // the Cmd+K affordance rendered just below; these are the other three. Agent
  // is NOT active while on its own token-recall sub-page (that has its own
  // button), so the two don't light up together.
  const collapsedRail: { href: string; label: string; icon: ElementType; active: boolean }[] = [
    {
      href: '/ontology/lakehouse-agent', label: t('lakehouse_agent'), icon: Bot,
      // Active only on the Agent chat itself / sub-pages WITHOUT their own rail
      // button — token-recall and annotations light up their own buttons.
      active: isPathActive(pathname, '/ontology/lakehouse-agent') &&
        !isPathActive(pathname, '/ontology/lakehouse-agent/token-recall') &&
        !isPathActive(pathname, '/ontology/lakehouse-agent/annotations'),
    },
    {
      href: '/ontology/lakehouse-objects', label: t('objects'), icon: Box,
      active: isPathActive(pathname, '/ontology/lakehouse-objects'),
    },
    {
      href: '/ontology/lakehouse-agent/token-recall', label: t('token_recall'), icon: Crosshair,
      active: isPathActive(pathname, '/ontology/lakehouse-agent/token-recall'),
    },
    {
      href: '/ontology/lakehouse-agent/annotations', label: t('annotations'), icon: PenLine,
      active: isPathActive(pathname, '/ontology/lakehouse-agent/annotations'),
    },
  ]

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
            <Image src="/logo.svg" alt="TEXT2ONTOLOGY" width={24} height={24} />
            <span className={industrial ? 'font-mono text-[12px] font-bold tracking-[0.06em] text-ink' : 'font-sans text-sm font-semibold text-ink'}>
              TEXT2ONTOLOGY
            </span>
          </div>
        )}
        {collapsed && (
          <Image src="/logo.svg" alt="TEXT2ONTOLOGY" width={22} height={22} className="mx-auto" />
        )}
        <button
          onClick={onToggle}
          className="flex h-6 w-6 items-center justify-center text-ink-ghost hover:text-ink transition-colors"
        >
          {collapsed ? <ChevronRight className="h-4 w-4" /> : <ChevronLeft className="h-4 w-4" />}
        </button>
      </div>

      {/* Collapsed-rail project affordance — the full switcher only renders when
          expanded, so surface a compact button that expands the rail (and opens
          the dropdown) to keep project switching reachable from the icon strip. */}
      {collapsed && (
        <div className={`flex justify-center py-2 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
          <button
            data-testid="project-switcher"
            onClick={() => { setProjectDropdownOpen(true); onToggle() }}
            title={currentProject?.name || 'Select Project'}
            className="flex h-9 w-9 items-center justify-center text-ink-ghost transition-colors hover:bg-canvas-alt hover:text-ink"
          >
            <FolderOpen className="h-4 w-4" />
          </button>
        </div>
      )}

      {/* Project Switcher */}
      {!collapsed && (
        <div className={`relative px-3 py-2 ${industrial ? 'border-b-2 border-ink' : 'border-b border-border'}`}>
          <button
            data-testid="project-switcher"
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
                  data-testid="new-project"
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
        {/* Cmd+K affordance — the primary navigator while the rail is the thin
            icon strip. Clickable for mouse users; shows the ⌘K hint when open. */}
        {onOpenCommand && (
          <div className={collapsed ? 'mb-2' : 'mb-2 px-3'}>
            <button
              type="button"
              onClick={onOpenCommand}
              title={collapsed ? `${tc('hint')} ⌘K` : undefined}
              className={`flex items-center gap-2 text-sm transition-colors duration-150 ${
                collapsed
                  ? 'mx-auto h-9 w-9 justify-center text-ink-ghost hover:bg-canvas-alt hover:text-ink'
                  : `w-full ${industrial ? 'border border-ink' : 'rounded border border-border'} px-2.5 py-1.5 text-ink-muted hover:bg-canvas-alt hover:text-ink`
              }`}
            >
              <Search className="h-4 w-4 flex-shrink-0 text-ink-ghost" />
              {!collapsed && (
                <>
                  <span className="flex-1 text-left">{tc('hint')}</span>
                  <kbd className={`text-[10px] text-ink-ghost ${industrial ? 'font-mono' : ''}`}>⌘K</kbd>
                </>
              )}
            </button>
          </div>
        )}
        {collapsed
          ? currentProject && collapsedRail.map((item) => (
              <div key={item.href} className="mb-1.5">
                <NavLeafLink
                  leaf={{ href: item.href, label: item.label, icon: item.icon }}
                  collapsed
                  isActive={item.active}
                  indent={false}
                />
              </div>
            ))
          : groups.map((group) => (
              <NavGroupSection
                key={group.label}
                group={group}
                pathname={pathname}
                collapsed={false}
              />
            ))}
      </nav>

      {/* Locale Switcher (above user row, expanded mode only) */}
      {!collapsed && (
        <div className="border-t border-border px-3 py-2">
          <LocaleSwitcher />
        </div>
      )}

      {/* User & Logout — fixed h-14 so the bottom gridline aligns with the
          workbench's chat input bar (also h-14). */}
      <div className="flex h-14 items-center border-t border-border px-3">
        {!collapsed ? (
          <div className="flex w-full items-center justify-between">
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
          <div className="flex w-full flex-col items-center gap-2">
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
