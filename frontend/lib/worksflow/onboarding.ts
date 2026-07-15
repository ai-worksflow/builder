export const ONBOARDING_GUIDE_VERSION = 2
export const ONBOARDING_PERSISTENCE_VERSION = 1

export const ONBOARDING_STEP_IDS = [
  'account',
  'project',
  'brief',
  'reviewer',
  'workflow',
  'implementation',
] as const

export type OnboardingStepId = (typeof ONBOARDING_STEP_IDS)[number]

export interface OnboardingFacts {
  readonly signedIn: boolean
  readonly hasProject: boolean
  readonly hasProjectBrief: boolean
  readonly isSoloProject: boolean
  readonly hasIndependentOwner: boolean
  readonly canEdit: boolean
  readonly hasWorkflowRun: boolean
  readonly hasWorkbenchReady: boolean
  readonly hasCompletedDeliveryRun: boolean
  readonly hasWorkspaceRevision: boolean
}

export type OnboardingWorkflowAction = 'start' | 'view' | 'contact_owner'
export type OnboardingImplementationAction = 'implement' | 'view'

export interface OnboardingStepStatus {
  readonly id: OnboardingStepId
  readonly complete: boolean
  readonly available: boolean
}

export interface OnboardingProgress {
  readonly steps: readonly OnboardingStepStatus[]
  readonly completedCount: number
  readonly totalCount: number
  readonly currentStepId?: OnboardingStepId
  readonly workflowAction: OnboardingWorkflowAction
  readonly implementationAction: OnboardingImplementationAction
  readonly complete: boolean
}

export interface OnboardingRunCandidate {
  readonly id: string
  readonly status: string
  readonly updatedAt: string
}

export interface OnboardingPreference {
  readonly dismissedVersion: number
  readonly completedVersion: number
}

export const DEFAULT_ONBOARDING_PREFERENCE: OnboardingPreference = {
  dismissedVersion: 0,
  completedVersion: 0,
}

export function deriveOnboardingProgress(facts: OnboardingFacts): OnboardingProgress {
  const accountComplete = facts.signedIn
  const projectComplete = accountComplete && facts.hasProject
  const briefComplete = projectComplete && facts.hasProjectBrief
  const reviewerComplete = projectComplete
    && (facts.isSoloProject || facts.hasIndependentOwner)
  const workflowReady = briefComplete && reviewerComplete
  const workflowComplete = workflowReady
    && (facts.hasWorkbenchReady || facts.hasCompletedDeliveryRun)
  const implementationComplete = workflowComplete
    && (facts.hasWorkspaceRevision || facts.hasCompletedDeliveryRun)

  const steps: OnboardingStepStatus[] = [
    { id: 'account', complete: accountComplete, available: true },
    { id: 'project', complete: projectComplete, available: accountComplete },
    { id: 'brief', complete: briefComplete, available: projectComplete },
    { id: 'reviewer', complete: reviewerComplete, available: projectComplete },
    { id: 'workflow', complete: workflowComplete, available: workflowReady },
    { id: 'implementation', complete: implementationComplete, available: workflowComplete },
  ]
  const completedCount = steps.filter((step) => step.complete).length
  const workflowAction = facts.hasWorkflowRun
    ? 'view'
    : facts.canEdit
      ? 'start'
      : 'contact_owner'

  return {
    steps,
    completedCount,
    totalCount: steps.length,
    currentStepId: steps.find((step) => !step.complete)?.id,
    workflowAction,
    implementationAction: facts.canEdit ? 'implement' : 'view',
    complete: completedCount === steps.length,
  }
}

export function latestCompletedOnboardingRun<T extends OnboardingRunCandidate>(
  runs: readonly T[],
): T | undefined {
  return runs.reduce<T | undefined>((latest, run) => {
    if (run.status !== 'completed') return latest
    if (!latest || run.updatedAt.localeCompare(latest.updatedAt) > 0) return run
    return latest
  }, undefined)
}

export function shouldAutoOpenOnboarding(preference: OnboardingPreference) {
  return Math.max(preference.dismissedVersion, preference.completedVersion)
    < ONBOARDING_GUIDE_VERSION
}

export function isOnboardingPreference(value: unknown): value is OnboardingPreference {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const candidate = value as Record<string, unknown>
  return validVersion(candidate.dismissedVersion) && validVersion(candidate.completedVersion)
}

function validVersion(value: unknown) {
  return Number.isInteger(value) && Number(value) >= 0
}
