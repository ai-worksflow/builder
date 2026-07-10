import type { GenerationMode } from '@/lib/generation/types'

export type DefaultProjectRole = 'viewer' | 'commenter' | 'editor'

export interface WorksflowPreferences {
  generationModel: string
  generationMode: Exclude<GenerationMode, 'plan'>
  notifyBlockingChanges: boolean
  notifyReviewSync: boolean
  requireApprovedContext: boolean
  defaultProjectRole: DefaultProjectRole
}

export const DEFAULT_WORKSFLOW_PREFERENCES: WorksflowPreferences = {
  generationModel: 'gpt-5.5',
  generationMode: 'iterate',
  notifyBlockingChanges: true,
  notifyReviewSync: true,
  requireApprovedContext: false,
  defaultProjectRole: 'editor',
}

export function isWorksflowPreferences(value: unknown): value is WorksflowPreferences {
  if (!value || typeof value !== 'object') return false
  const preferences = value as Record<string, unknown>
  return (
    typeof preferences.generationModel === 'string' &&
    preferences.generationModel.length > 0 &&
    preferences.generationModel.length <= 100 &&
    ['build', 'iterate', 'fix'].includes(String(preferences.generationMode)) &&
    typeof preferences.notifyBlockingChanges === 'boolean' &&
    typeof preferences.notifyReviewSync === 'boolean' &&
    typeof preferences.requireApprovedContext === 'boolean' &&
    ['viewer', 'commenter', 'editor'].includes(String(preferences.defaultProjectRole))
  )
}

export function updatePreferences(
  current: WorksflowPreferences,
  patch: Partial<WorksflowPreferences>,
): WorksflowPreferences {
  const next = { ...current, ...patch }
  return isWorksflowPreferences(next) ? next : current
}
