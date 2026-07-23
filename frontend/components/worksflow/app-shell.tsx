'use client'

import { useEffect } from 'react'
import { cn } from '@/lib/utils'
import { useI18n, type MessageKey } from '@/lib/i18n'
import { teamPathFor, useWorksflow, workbenchPathFor } from '@/lib/worksflow/store'
import { Workbench } from './workbench/workbench'
import { TeamCollaboration } from './team/team-collaboration'
import { RecentProjects } from './recent-projects'
import { SettingsCenter } from './settings-center'
import { LanguageToggle } from './language-toggle'
import { OnboardingGuide } from './onboarding-guide'
import { Boxes, Clock, PanelsTopLeft, Settings, Zap } from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'

const NAV: {
  id: 'workbench' | 'team' | 'recent' | 'settings'
  labelKey: MessageKey
  shortLabelKey: MessageKey
  icon: typeof Zap
  badge?: number
}[] = [
  { id: 'workbench', labelKey: 'nav.workbench', shortLabelKey: 'nav.workbenchShort', icon: Zap },
  { id: 'team', labelKey: 'nav.team', shortLabelKey: 'nav.teamShort', icon: Boxes },
  { id: 'recent', labelKey: 'nav.recent', shortLabelKey: 'nav.recentShort', icon: Clock },
  { id: 'settings', labelKey: 'nav.settings', shortLabelKey: 'nav.settingsShort', icon: Settings },
] as const

const WORKBENCH_DEEP_LINK_KEYS = [
  'runId',
  'workbenchNodeKey',
  'bundleId',
  'proposalId',
  'workspaceRevisionId',
  'conversationId',
] as const

const TEAM_DEEP_LINK_KEYS = ['runId', 'workbenchNodeKey', 'artifactId', 'proposalId'] as const

export function AppShell() {
  const {
    routeReady,
    surface,
    setSurface,
    phase,
    view,
    teamView,
    activeTeamProjectId,
    activeTeamProject,
  } = useWorksflow()
  const { locale, t } = useI18n()
  const { unreadCount } = useCollaboration()

  useEffect(() => {
    if (!routeReady || typeof window === 'undefined') return

    const nextPath =
      surface === 'workbench'
        ? workbenchPathFor(phase, view)
        : surface === 'team'
          ? teamPathFor(activeTeamProject.teamId, activeTeamProjectId, teamView)
          : `/${surface}`
    const currentUrl = new URL(window.location.href)
    const nextUrl = new URL(nextPath, window.location.origin)
    if (surface === 'workbench') {
      WORKBENCH_DEEP_LINK_KEYS.forEach((key) => {
        const value = currentUrl.searchParams.get(key)
        if (value) nextUrl.searchParams.set(key, value)
      })
    } else if (surface === 'team') {
      TEAM_DEEP_LINK_KEYS.forEach((key) => {
        const value = currentUrl.searchParams.get(key)
        if (value) nextUrl.searchParams.set(key, value)
      })
    }
    const currentPath = `${currentUrl.pathname}${currentUrl.search}`
    const preservedPath = `${nextUrl.pathname}${nextUrl.search}`

    if (currentPath !== preservedPath) {
      window.history.replaceState(null, '', preservedPath)
    }
  }, [activeTeamProject.teamId, activeTeamProjectId, phase, routeReady, surface, teamView, view])

  return (
    <div className="flex h-dvh w-full overflow-hidden bg-background text-foreground max-md:flex-col">
      {/* Global left rail */}
      <nav
        className="flex w-[68px] shrink-0 flex-col items-center gap-1 border-r border-border bg-[#0d0d0f] py-3 max-md:order-2 max-md:h-[64px] max-md:w-full max-md:flex-row max-md:justify-around max-md:border-r-0 max-md:border-t max-md:px-2 max-md:py-1"
        aria-label={t('app.globalNav')}
      >
        <div className="mb-3 flex h-9 w-9 items-center justify-center rounded-md bg-primary text-primary-foreground max-md:mb-0">
          <PanelsTopLeft className="h-5 w-5" />
        </div>
        {NAV.map((item) => {
          const Icon = item.icon
          const active = item.id === surface
          const badge = (item.id === 'team' ? unreadCount : item.badge) ?? 0
          return (
            <button
              key={item.id}
              type="button"
              onClick={() => setSurface(item.id)}
              className={cn(
                'group relative flex h-12 w-12 flex-col items-center justify-center gap-0.5 rounded-lg text-[9px] font-medium transition-colors',
                active
                  ? 'bg-primary/15 text-primary-bright'
                  : 'text-muted-foreground hover:bg-white/5 hover:text-foreground',
              )}
              aria-label={t(item.labelKey)}
              aria-current={active ? 'page' : undefined}
              title={t(item.labelKey)}
            >
              {active && (
                <span className="absolute left-0 top-1/2 h-6 w-0.5 -translate-y-1/2 rounded-r bg-primary-bright" />
              )}
              <Icon className="h-[18px] w-[18px]" />
              <span className="max-w-[52px] truncate leading-none">
                {t(item.shortLabelKey)}
              </span>
              {badge > 0 ? (
                <span className="absolute right-1.5 top-1.5 flex h-3.5 min-w-3.5 items-center justify-center rounded-full bg-primary px-1 text-[9px] font-semibold text-primary-foreground">
                  {badge.toLocaleString(locale)}
                </span>
              ) : null}
            </button>
          )
        })}
        <OnboardingGuide className="mt-auto max-md:mt-0" />
        <LanguageToggle className="w-12 px-0" />
      </nav>

      {/* Main surface */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col max-md:order-1">
        {surface === 'workbench' && <Workbench />}
        {surface === 'team' && <TeamCollaboration />}
        {surface === 'recent' && <RecentProjects />}
        {surface === 'settings' && <SettingsCenter />}
      </div>
    </div>
  )
}
