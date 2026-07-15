import assert from 'node:assert/strict'
import {
  DEFAULT_ONBOARDING_PREFERENCE,
  ONBOARDING_GUIDE_VERSION,
  deriveOnboardingProgress,
  isOnboardingPreference,
  latestCompletedOnboardingRun,
  shouldAutoOpenOnboarding,
  type OnboardingFacts,
} from '../lib/worksflow/onboarding'

function facts(overrides: Partial<OnboardingFacts> = {}): OnboardingFacts {
  return {
    signedIn: false,
    hasProject: false,
    hasProjectBrief: false,
    isSoloProject: false,
    hasIndependentOwner: false,
    canEdit: true,
    hasWorkflowRun: false,
    hasWorkbenchReady: false,
    hasCompletedDeliveryRun: false,
    hasWorkspaceRevision: false,
    ...overrides,
  }
}

const anonymous = deriveOnboardingProgress(facts())
assert.equal(anonymous.currentStepId, 'account')
assert.deepEqual(
  anonymous.steps.filter((step) => step.available).map((step) => step.id),
  ['account'],
)

const signedIn = deriveOnboardingProgress(facts({ signedIn: true }))
assert.equal(signedIn.currentStepId, 'project')
assert.equal(signedIn.completedCount, 1)

const project = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasProjectBrief: true,
}))
assert.equal(project.currentStepId, 'reviewer')
assert.equal(project.steps.find((step) => step.id === 'brief')?.complete, true)
assert.equal(project.steps.find((step) => step.id === 'reviewer')?.available, true)
assert.equal(project.steps.find((step) => step.id === 'workflow')?.available, false)

const soloProject = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasProjectBrief: true,
  isSoloProject: true,
}))
assert.equal(soloProject.currentStepId, 'workflow')
assert.equal(soloProject.steps.find((step) => step.id === 'reviewer')?.complete, true)

const reviewerWithoutBrief = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasIndependentOwner: true,
}))
assert.equal(reviewerWithoutBrief.currentStepId, 'brief')

const readyForWorkflow = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasProjectBrief: true,
  hasIndependentOwner: true,
}))
assert.equal(readyForWorkflow.currentStepId, 'workflow')
assert.equal(readyForWorkflow.steps.find((step) => step.id === 'workflow')?.available, true)
assert.equal(readyForWorkflow.workflowAction, 'start')

const readOnlyWorkflow = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasProjectBrief: true,
  hasIndependentOwner: true,
  canEdit: false,
}))
assert.equal(readOnlyWorkflow.workflowAction, 'contact_owner')

const runningBeforeWorkbench = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasProjectBrief: true,
  hasIndependentOwner: true,
  hasWorkflowRun: true,
}))
assert.equal(runningBeforeWorkbench.currentStepId, 'workflow')
assert.equal(runningBeforeWorkbench.steps.find((step) => step.id === 'implementation')?.available, false)
assert.equal(runningBeforeWorkbench.workflowAction, 'view')

const workbenchReady = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasProjectBrief: true,
  hasIndependentOwner: true,
  hasWorkflowRun: true,
  hasWorkbenchReady: true,
}))
assert.equal(workbenchReady.currentStepId, 'implementation')
assert.equal(workbenchReady.steps.find((step) => step.id === 'implementation')?.available, true)

const readOnlyImplementation = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasProjectBrief: true,
  hasIndependentOwner: true,
  canEdit: false,
  hasWorkflowRun: true,
  hasWorkbenchReady: true,
}))
assert.equal(readOnlyImplementation.implementationAction, 'view')

const completedDeliveryRun = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasProjectBrief: true,
  hasIndependentOwner: true,
  hasWorkflowRun: true,
  hasCompletedDeliveryRun: true,
}))
assert.equal(completedDeliveryRun.complete, true)
assert.equal(completedDeliveryRun.completedCount, completedDeliveryRun.totalCount)
assert.equal(completedDeliveryRun.currentStepId, undefined)

const appliedWorkspace = deriveOnboardingProgress(facts({
  signedIn: true,
  hasProject: true,
  hasProjectBrief: true,
  hasIndependentOwner: true,
  hasWorkflowRun: true,
  hasWorkbenchReady: true,
  hasWorkspaceRevision: true,
}))
assert.equal(appliedWorkspace.complete, true)

const inconsistentServerFacts = deriveOnboardingProgress(facts({
  hasWorkflowRun: true,
  hasWorkbenchReady: true,
  hasCompletedDeliveryRun: true,
  hasWorkspaceRevision: true,
}))
assert.equal(inconsistentServerFacts.completedCount, 0)

const latestCompleted = latestCompletedOnboardingRun([
  { id: 'running-newer', status: 'running', updatedAt: '2026-07-13T12:00:00Z' },
  { id: 'completed-older', status: 'completed', updatedAt: '2026-07-12T12:00:00Z' },
  { id: 'completed-latest', status: 'completed', updatedAt: '2026-07-13T10:00:00Z' },
  { id: 'cancelled-newest', status: 'cancelled', updatedAt: '2026-07-13T13:00:00Z' },
])
assert.equal(latestCompleted?.id, 'completed-latest')
assert.equal(latestCompletedOnboardingRun([]), undefined)

assert.equal(isOnboardingPreference(DEFAULT_ONBOARDING_PREFERENCE), true)
assert.equal(isOnboardingPreference({ dismissedVersion: 1, completedVersion: 0 }), true)
assert.equal(isOnboardingPreference({ dismissedVersion: -1, completedVersion: 0 }), false)
assert.equal(isOnboardingPreference({ dismissedVersion: 1.5, completedVersion: 0 }), false)
assert.equal(isOnboardingPreference({ dismissedVersion: 0 }), false)
assert.equal(shouldAutoOpenOnboarding(DEFAULT_ONBOARDING_PREFERENCE), true)
assert.equal(shouldAutoOpenOnboarding({
  dismissedVersion: ONBOARDING_GUIDE_VERSION,
  completedVersion: 0,
}), false)

console.log('onboarding state tests passed')
