'use client'

import { useState } from 'react'
import { cn } from '@/lib/utils'
import { useI18n, type MessageKey } from '@/lib/i18n'
import { useWorksflow, type TeamView } from '@/lib/worksflow/store'
import {
  Bell,
  Boxes,
  ChevronsLeft,
  ChevronsRight,
  FileText,
  GitFork,
  LayoutDashboard,
  MonitorPlay,
  Plus,
  Search,
  UploadCloud,
  Users2,
} from 'lucide-react'
import { TeamDashboard } from './dashboard'
import { DocumentGraph } from './document-graph'
import { DocumentEditor } from './document-editor'
import { BlueprintEditor } from './blueprint-editor'
import { PrototypeStudio } from './prototype-studio'
import { ImportCenter } from './import-center'
import { ReviewCenter } from './review-center'
import { MemberSettings } from './member-settings'
import { LanguageToggle } from '../language-toggle'

const NAV: { id: TeamView; labelKey: MessageKey; icon: typeof LayoutDashboard; badge?: number }[] = [
  { id: 'dashboard', labelKey: 'team.nav.overview', icon: LayoutDashboard },
  { id: 'graph', labelKey: 'team.nav.graph', icon: GitFork },
  { id: 'blueprint', labelKey: 'team.nav.blueprint', icon: Boxes },
  { id: 'editor', labelKey: 'team.nav.documents', icon: FileText },
  { id: 'prototype', labelKey: 'team.nav.prototype', icon: MonitorPlay },
  { id: 'imports', labelKey: 'team.nav.imports', icon: UploadCloud },
  { id: 'reviews', labelKey: 'team.nav.reviews', icon: Users2, badge: 3 },
  { id: 'members', labelKey: 'team.nav.members', icon: Users2 },
]

export function TeamCollaboration() {
  const {
    teamView,
    setTeamView,
    teamProjects,
    activeTeamProjectId,
    activeTeamProject,
    openTeamProject,
    createTeamProject,
  } = useWorksflow()
  const { t } = useI18n()
  const [navCollapsed, setNavCollapsed] = useState(false)

  return (
    <div className="flex h-full flex-col">
      {/* Global top bar */}
      <header className="flex min-h-[50px] shrink-0 items-center gap-3 border-b border-border bg-panel px-4 py-2 max-md:flex-wrap max-md:px-2">
        <div className="flex min-w-0 flex-1 items-center gap-2 md:flex-none">
          <div className="flex min-w-0 items-center gap-1.5 rounded-md px-2 py-1 text-sm font-medium">
            <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded bg-primary text-[10px] font-bold text-primary-foreground">
              {activeTeamProject.teamName.slice(0, 1)}
            </span>
            <span className="shrink-0">{activeTeamProject.teamName}</span>
            <span className="shrink-0 text-faint-foreground">/</span>
            <select
              value={activeTeamProjectId}
              onChange={(event) => openTeamProject(event.target.value)}
              className="max-w-[220px] truncate rounded border border-transparent bg-transparent text-sm font-medium text-foreground outline-none hover:border-border hover:bg-white/5"
              aria-label={t('recent.project')}
            >
              {teamProjects.map((project) => (
                <option key={project.id} value={project.id}>
                  {project.name} · {t('team.project.docsCount', { count: project.documents.length })}
                </option>
              ))}
            </select>
          </div>
          <button
            type="button"
            onClick={() => createTeamProject()}
            className="flex h-8 w-8 items-center justify-center rounded-md border border-border text-muted-foreground hover:bg-white/5 hover:text-foreground"
            aria-label={t('team.project.new')}
            title={t('team.project.new')}
          >
            <Plus className="h-4 w-4" />
          </button>
        </div>

        <div className="mx-auto flex w-full max-w-md items-center gap-2 rounded-md border border-border bg-white/5 px-3 py-1.5 text-[13px] text-faint-foreground max-md:hidden">
          <Search className="h-3.5 w-3.5" />
          {t('team.searchPlaceholder')}
        </div>

        <div className="flex shrink-0 items-center gap-2">
          <LanguageToggle />
          <button
            type="button"
            className="relative flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-white/5 hover:text-foreground"
            aria-label={t('team.notifications')}
          >
            <Bell className="h-4 w-4" />
            <span className="absolute right-1.5 top-1.5 h-2 w-2 rounded-full bg-primary" />
          </button>
          <span className="flex h-7 w-7 items-center justify-center rounded-full bg-primary text-[11px] font-semibold text-primary-foreground">
            MC
          </span>
        </div>
      </header>

      <div className="flex min-h-0 flex-1 max-lg:flex-col">
        {/* Team nav */}
        <nav
          className={cn(
            'flex shrink-0 flex-col gap-0.5 border-r border-border bg-panel p-2 transition-[width] duration-200 max-lg:w-full max-lg:flex-row max-lg:overflow-x-auto max-lg:border-b max-lg:border-r-0 max-lg:scrollbar-thin',
            navCollapsed ? 'w-[64px]' : 'w-56',
          )}
          aria-label={t('nav.team')}
        >
          <button
            type="button"
            onClick={() => setNavCollapsed((value) => !value)}
            className={cn(
              'mb-1 flex h-8 items-center rounded-md border border-border text-[12px] font-medium text-muted-foreground hover:bg-white/5 hover:text-foreground max-lg:hidden',
              navCollapsed ? 'justify-center px-0' : 'justify-between gap-2 px-2.5',
            )}
            aria-label={navCollapsed ? t('common.expandSidebar') : t('common.collapseSidebar')}
            aria-expanded={!navCollapsed}
            title={navCollapsed ? t('common.expandSidebar') : t('common.collapseSidebar')}
          >
            {!navCollapsed && <span>{t('nav.teamShort')}</span>}
            {navCollapsed ? (
              <ChevronsRight className="h-4 w-4" />
            ) : (
              <ChevronsLeft className="h-4 w-4" />
            )}
          </button>
          {NAV.map((item) => {
            const Icon = item.icon
            const active = teamView === item.id
            return (
              <button
                key={item.id}
                type="button"
                onClick={() => setTeamView(item.id)}
                className={cn(
                  'relative flex items-center gap-2.5 rounded-md py-2 text-[13px] font-medium transition-colors max-lg:shrink-0 max-lg:px-2.5',
                  navCollapsed ? 'justify-center px-0' : 'px-2.5',
                  active
                    ? 'bg-primary/15 text-primary-bright'
                    : 'text-muted-foreground hover:bg-white/5 hover:text-foreground',
                )}
                aria-current={active ? 'page' : undefined}
                aria-label={t(item.labelKey)}
                title={t(item.labelKey)}
              >
                <Icon className="h-4 w-4" />
                <span className={cn('flex-1 text-left max-lg:not-sr-only', navCollapsed && 'sr-only')}>
                  {t(item.labelKey)}
                </span>
                {item.badge ? (
                  <span
                    className={cn(
                      'flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-[10px] font-semibold text-primary-foreground',
                      navCollapsed && 'absolute right-1 top-1',
                    )}
                  >
                    {item.badge}
                  </span>
                ) : null}
              </button>
            )
          })}

          <div className={cn('mt-auto rounded-lg border border-border bg-card p-3 max-lg:hidden', navCollapsed && 'hidden')}>
            <p className="text-[11px] font-medium text-foreground">{t('team.sidebarNote.title')}</p>
            <p className="mt-1 text-[11px] leading-relaxed text-faint-foreground">
              {t('team.sidebarNote.body')}
            </p>
          </div>
        </nav>

        {/* Active surface */}
        <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
          {teamView === 'dashboard' && <TeamDashboard />}
          {teamView === 'graph' && <DocumentGraph />}
          {teamView === 'editor' && <DocumentEditor />}
          {teamView === 'blueprint' && <BlueprintEditor />}
          {teamView === 'prototype' && <PrototypeStudio />}
          {teamView === 'imports' && <ImportCenter />}
          {teamView === 'reviews' && <ReviewCenter />}
          {teamView === 'members' && <MemberSettings />}
        </div>
      </div>
    </div>
  )
}
