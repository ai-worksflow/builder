import type { Phase, Surface, TeamView, WorkbenchView } from './types'

const WORKBENCH_PHASE_BY_SEGMENT: Record<string, Phase> = {
  planning: 'planning',
  'plan-ready': 'planReady',
  building: 'building',
  complete: 'complete',
  error: 'error',
}

const WORKBENCH_SEGMENT_BY_PHASE: Record<Phase, string> = {
  planning: 'planning',
  planReady: 'plan-ready',
  building: 'building',
  complete: 'complete',
  error: 'error',
}

const TEAM_VIEW_BY_SEGMENT: Record<string, TeamView> = {
  dashboard: 'dashboard',
  graph: 'graph',
  editor: 'editor',
  blueprint: 'blueprint',
  prototype: 'prototype',
  imports: 'imports',
  reviews: 'reviews',
  members: 'members',
}

export type ParsedWorksflowRoute =
  | { surface: 'workbench'; phase: Phase; view: WorkbenchView }
  | { surface: 'team'; projectId?: string; teamView: TeamView }
  | { surface: Exclude<Surface, 'workbench' | 'team'> }
  | { surface: null }

export function parseWorksflowRoute(pathname: string, search = ''): ParsedWorksflowRoute {
  const segments = pathname.split('/').filter(Boolean)
  const params = new URLSearchParams(search)

  if (segments[0] === 'workbench') {
    const routedView = params.get('view')
    return {
      surface: 'workbench',
      phase: WORKBENCH_PHASE_BY_SEGMENT[segments[1] ?? 'planning'] ?? 'planning',
      view: routedView === 'code' || routedView === 'database' ? routedView : 'preview',
    }
  }

  if (segments[0] === 'team') {
    return {
      surface: 'team',
      projectId: segments[3],
      teamView: TEAM_VIEW_BY_SEGMENT[segments[4] ?? 'dashboard'] ?? 'dashboard',
    }
  }

  if (segments[0] === 'recent' || segments[0] === 'settings') {
    return { surface: segments[0] }
  }

  return { surface: null }
}

export function workbenchPathFor(phase: Phase, view: WorkbenchView) {
  const query = view === 'preview' ? '' : `?view=${view}`
  return `/workbench/${WORKBENCH_SEGMENT_BY_PHASE[phase]}${query}`
}

export function teamPathFor(teamProjectId: string, teamView: TeamView) {
  return `/team/acme/project/${teamProjectId}/${teamView}`
}
