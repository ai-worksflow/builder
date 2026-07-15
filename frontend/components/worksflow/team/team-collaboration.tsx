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
import { useCollaboration } from '@/lib/collaboration/provider'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'

const NAV: { id: TeamView; labelKey: MessageKey; icon: typeof LayoutDashboard; badge?: number }[] = [
  { id: 'dashboard', labelKey: 'team.nav.overview', icon: LayoutDashboard },
  { id: 'graph', labelKey: 'team.nav.graph', icon: GitFork },
  { id: 'blueprint', labelKey: 'team.nav.blueprint', icon: Boxes },
  { id: 'editor', labelKey: 'team.nav.documents', icon: FileText },
  { id: 'prototype', labelKey: 'team.nav.prototype', icon: MonitorPlay },
  { id: 'imports', labelKey: 'team.nav.imports', icon: UploadCloud },
  { id: 'reviews', labelKey: 'team.nav.reviews', icon: Users2 },
  { id: 'members', labelKey: 'team.nav.members', icon: Users2 },
]

export function TeamCollaboration() {
  const {
    teamView,
    setTeamView,
    activeTeamProject,
    documents,
    openDoc,
    setSurface,
    platformTeamFactsStatus,
    platformTeamFactsError,
  } = useWorksflow()
  const artifactWorkspace = useArtifactWorkspace()
  const {
    session,
    projects,
    project,
    reviews,
    unreadCount,
    presence,
    backendStatus,
    can,
    createProject,
    selectProject,
  } = useCollaboration()
  const canEdit = session.signedIn && can('edit')
  const canUseActiveTeamView = teamView === 'members' || teamView === 'reviews' || canEdit
  const { locale, t } = useI18n()
  const [navCollapsed, setNavCollapsed] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const searchResults = searchQuery.trim()
    ? documents.filter((document) =>
        [document.title, document.summary, document.type]
          .join(' ')
          .toLowerCase()
          .includes(searchQuery.trim().toLowerCase()),
      ).slice(0, 6)
    : []

  return (
    <div className="flex h-full flex-col">
      {/* Global top bar */}
      <header className="flex min-h-[50px] shrink-0 items-center gap-3 border-b border-border bg-panel px-4 py-2 max-md:flex-wrap max-md:px-2">
        <div className="flex min-w-0 flex-1 items-center gap-2 md:flex-none">
          <div className="flex min-w-0 items-center gap-1.5 rounded-md px-2 py-1 text-sm font-medium">
            <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded bg-primary text-[10px] font-bold text-primary-foreground">
              {(project?.name ?? activeTeamProject.name).slice(0, 1)}
            </span>
            <span className="shrink-0">{t('team.projects')}</span>
            <span className="shrink-0 text-faint-foreground">/</span>
            <select
              value={project?.id ?? ''}
              onChange={(event) => void selectProject(event.target.value)}
              disabled={!session.signedIn || projects.length === 0}
              className="max-w-[220px] truncate rounded border border-transparent bg-transparent text-sm font-medium text-foreground outline-none hover:border-border hover:bg-white/5"
              aria-label={t('recent.project')}
            >
              {projects.length === 0 && <option value="">{t('team.noServerProjects')}</option>}
              {projects.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name} · {projectRoleLabel(item.role, t)}
                </option>
              ))}
            </select>
          </div>
          <button
            type="button"
            onClick={() => {
              const name = window.prompt(t('team.project.namePrompt'))?.trim()
              if (name) void createProject(name)
            }}
            disabled={!session.signedIn || backendStatus !== 'online'}
            className="flex h-8 w-8 items-center justify-center rounded-md border border-border text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
            aria-label={t('team.project.new')}
            title={t('team.project.new')}
          >
            <Plus className="h-4 w-4" />
          </button>
        </div>

        <div className="relative mx-auto w-full max-w-md max-md:hidden">
          <label className="flex items-center gap-2 rounded-md border border-border bg-white/5 px-3 py-1.5 text-[13px] text-faint-foreground">
            <Search className="h-3.5 w-3.5" />
            <span className="sr-only">{t('team.searchPlaceholder')}</span>
            <input
              value={searchQuery}
              onChange={(event) => setSearchQuery(event.target.value)}
              placeholder={t('team.searchPlaceholder')}
              className="min-w-0 flex-1 bg-transparent text-[12px] text-foreground outline-none placeholder:text-faint-foreground"
            />
          </label>
          {searchResults.length > 0 && (
            <div className="absolute inset-x-0 top-[calc(100%+6px)] z-50 rounded-lg border border-border bg-popover p-1 shadow-2xl">
              {searchResults.map((document) => (
                <button
                  key={document.id}
                  type="button"
                  onClick={() => {
                    openDoc(document.id)
                    setSearchQuery('')
                  }}
                  className="block w-full rounded-md px-2.5 py-2 text-left hover:bg-white/5"
                >
                  <span className="block truncate text-[11px] font-medium text-foreground">{document.title}</span>
                  <span className="block truncate text-[9px] text-faint-foreground">{documentTypeLabel(document.type, t)} · {documentStatusLabel(document.status, t)}</span>
                </button>
              ))}
            </div>
          )}
        </div>

        <div className="flex shrink-0 items-center gap-2">
          <LanguageToggle />
          <button
            type="button"
            onClick={() => setSurface('settings')}
            className="relative flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-white/5 hover:text-foreground"
            aria-label={t('team.notifications')}
          >
            <Bell className="h-4 w-4" />
            {unreadCount > 0 && <span className="absolute right-0.5 top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-[8px] font-semibold text-primary-foreground">{unreadCount.toLocaleString(locale)}</span>}
          </button>
          <button
            type="button"
            onClick={() => setSurface('settings')}
            className="relative flex h-7 w-7 items-center justify-center rounded-full bg-primary text-[10px] font-semibold text-primary-foreground"
            aria-label={session.signedIn ? t('team.presence.onlineTitle', { name: session.user.name, count: presence.filter((item) => item.status !== 'offline').length.toLocaleString(locale) }) : t('team.signIn')}
            title={session.signedIn ? t('team.presence.onlineTitle', { name: session.user.name, count: presence.filter((item) => item.status !== 'offline').length.toLocaleString(locale) }) : t('team.signIn')}
          >
            {session.signedIn
              ? session.user.name.split(/\s+/).slice(0, 2).map((part) => part[0]).join('').toUpperCase()
              : '?'}
          </button>
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
            const badge = item.id === 'reviews' ? reviews.length : item.badge
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
                {badge ? (
                  <span
                    className={cn(
                      'flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-[10px] font-semibold text-primary-foreground',
                      navCollapsed && 'absolute right-1 top-1',
                    )}
                  >
                    {badge.toLocaleString(locale)}
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
        <fieldset
          disabled={!canUseActiveTeamView}
          className="relative min-h-0 min-w-0 flex-1 overflow-hidden border-0 p-0"
        >
          {!canUseActiveTeamView && (
            <div className="absolute inset-x-3 top-3 z-40 rounded-md border border-warning/30 bg-popover/95 px-3 py-2 text-[10px] text-warning shadow-lg">
              {t('team.readOnlyRole')}
            </div>
          )}
          {teamView === 'dashboard' && platformTeamFactsStatus === 'loading' && <PlatformFactsState loading message={t('team.loadingDocumentsBlueprint')} />}
          {teamView === 'dashboard' && platformTeamFactsStatus === 'error' && <PlatformFactsState message={platformTeamFactsError ?? t('team.serverArtifactsUnavailable')} onRetry={artifactWorkspace.refresh} />}
          {teamView === 'dashboard' && !['loading', 'error'].includes(platformTeamFactsStatus) && <TeamDashboard />}
          {teamView === 'graph' && platformTeamFactsStatus === 'loading' && <PlatformFactsState loading message={t('team.loadingDependencyGraph')} />}
          {teamView === 'graph' && platformTeamFactsStatus === 'error' && <PlatformFactsState message={platformTeamFactsError ?? t('team.serverArtifactsUnavailable')} onRetry={artifactWorkspace.refresh} />}
          {teamView === 'graph' && !['loading', 'error'].includes(platformTeamFactsStatus) && <DocumentGraph />}
          {teamView === 'editor' && <DocumentEditor />}
          {teamView === 'blueprint' && <BlueprintEditor />}
          {teamView === 'prototype' && <PrototypeStudio />}
          {teamView === 'imports' && <ImportCenter />}
          {teamView === 'reviews' && <ReviewCenter />}
          {teamView === 'members' && <MemberSettings />}
        </fieldset>
      </div>
    </div>
  )
}

function PlatformFactsState({ message, loading, onRetry }: { message: string; loading?: boolean; onRetry?: () => Promise<void> }) {
  const { t } = useI18n()
  return <div className="flex h-full items-center justify-center bg-canvas p-6"><div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-6 text-center"><p className="text-sm text-muted-foreground">{message}</p>{loading && <p className="mt-2 text-[10px] text-primary-bright">{t('team.platformRequestInProgress')}</p>}{onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 rounded-md bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground">{t('team.retryPlatformArtifacts')}</button>}</div></div>
}

type Translate = ReturnType<typeof useI18n>['t']

function projectRoleLabel(role: string, t: Translate) {
  if (role === 'owner') return t('common.owner')
  if (role === 'admin') return t('team.role.admin')
  if (role === 'editor') return t('common.editor')
  if (role === 'commenter') return t('common.commenter')
  if (role === 'viewer') return t('common.viewer')
  return role
}

function documentTypeLabel(type: string, t: Translate) {
  const labels: Record<string, string> = {
    requirement: t('doc.type.requirement'),
    pageSplit: t('doc.type.pageSplit'),
    featureList: t('doc.type.featureList'),
    apiContract: t('doc.type.apiContract'),
    backendDev: t('doc.type.backendDev'),
    uiPrototype: t('doc.type.uiPrototype'),
    frontendDev: t('doc.type.frontendDev'),
  }
  return labels[type] ?? type
}

function documentStatusLabel(status: string, t: Translate) {
  const labels: Record<string, string> = {
    draft: t('doc.status.draft'),
    readyForReview: t('doc.status.readyForReview'),
    changesRequested: t('doc.status.changesRequested'),
    approved: t('doc.status.approved'),
    needsSync: t('doc.status.needsSync'),
    archived: t('doc.status.archived'),
  }
  return labels[status] ?? status
}
