'use client'

import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ArrowRight,
  Check,
  CircleHelp,
  Code2,
  FileCheck2,
  FolderKanban,
  LockKeyhole,
  PartyPopper,
  UserRound,
  UserRoundCheck,
  Workflow,
  X,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useI18n, type MessageKey } from '@/lib/i18n'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import { cn } from '@/lib/utils'
import {
  DEFAULT_ONBOARDING_PREFERENCE,
  ONBOARDING_GUIDE_VERSION,
  ONBOARDING_PERSISTENCE_VERSION,
  deriveOnboardingProgress,
  isOnboardingPreference,
  latestCompletedOnboardingRun,
  shouldAutoOpenOnboarding,
  type OnboardingStepId,
} from '@/lib/worksflow/onboarding'
import { usePersistentState } from '@/lib/worksflow/use-persistent-state'
import { useWorksflow } from '@/lib/worksflow/store'

const STEP_COPY: Record<OnboardingStepId, {
  readonly title: MessageKey
  readonly description: MessageKey
  readonly action: MessageKey
  readonly icon: typeof UserRound
}> = {
  account: {
    title: 'onboarding.step.account.title',
    description: 'onboarding.step.account.description',
    action: 'onboarding.step.account.action',
    icon: UserRound,
  },
  project: {
    title: 'onboarding.step.project.title',
    description: 'onboarding.step.project.description',
    action: 'onboarding.step.project.action',
    icon: FolderKanban,
  },
  brief: {
    title: 'onboarding.step.brief.title',
    description: 'onboarding.step.brief.description',
    action: 'onboarding.step.brief.action',
    icon: FileCheck2,
  },
  reviewer: {
    title: 'onboarding.step.reviewer.title',
    description: 'onboarding.step.reviewer.description',
    action: 'onboarding.step.reviewer.action',
    icon: UserRoundCheck,
  },
  workflow: {
    title: 'onboarding.step.workflow.title',
    description: 'onboarding.step.workflow.description',
    action: 'onboarding.step.workflow.action',
    icon: Workflow,
  },
  implementation: {
    title: 'onboarding.step.implementation.title',
    description: 'onboarding.step.implementation.description',
    action: 'onboarding.step.implementation.action',
    icon: Code2,
  },
}

export function OnboardingGuide({ className }: { className?: string }) {
  const { session } = useCollaboration()
  const preferenceKey = session.signedIn
    ? `worksflow.onboarding.${session.user.id}`
    : 'worksflow.onboarding.guest'

  return (
    <OnboardingGuideState
      key={preferenceKey}
      className={className}
      preferenceKey={preferenceKey}
    />
  )
}

function OnboardingGuideState({
  className,
  preferenceKey,
}: {
  className?: string
  preferenceKey: string
}) {
  const { t } = useI18n()
  const collaboration = useCollaboration()
  const artifacts = useArtifactWorkspace()
  const flow = usePlatformFlow()
  const {
    routeReady,
    setSurface,
    setTeamView,
    setSelectedDocId,
    setView,
  } = useWorksflow()
  const preference = usePersistentState({
    key: preferenceKey,
    version: ONBOARDING_PERSISTENCE_VERSION,
    initialValue: DEFAULT_ONBOARDING_PREFERENCE,
    validate: isOnboardingPreference,
    debounceMs: 0,
  })
  const projectId = collaboration.project?.id
  const projectBrief = useMemo(() => {
    const candidates = artifacts.documents.filter((item) =>
      item.artifact.projectId === projectId
      && item.artifact.kind === 'project_brief'
      && item.artifact.lifecycle !== 'archived'
      && item.artifact.status !== 'archived',
    )
    return [...candidates].sort((left, right) => {
      const approvedOrder = Number(Boolean(right.approvedRevision))
        - Number(Boolean(left.approvedRevision))
      if (approvedOrder) return approvedOrder
      const revisionOrder = Number(Boolean(right.latestRevision))
        - Number(Boolean(left.latestRevision))
      if (revisionOrder) return revisionOrder
      return right.artifact.updatedAt.localeCompare(left.artifact.updatedAt)
    })[0]
  }, [artifacts.documents, projectId])
  const currentUserId = collaboration.session.signedIn
    ? collaboration.session.user.id
    : undefined
  const canEdit = collaboration.can('edit')
  const projectRuns = flow.runs.filter((run) => run.projectId === projectId)
  const resumableRun = projectRuns.find((run) =>
    !['completed', 'cancelled', 'stale'].includes(run.status),
  )
  const completedRunCandidate = latestCompletedOnboardingRun(projectRuns)
  const guideRun = resumableRun ?? completedRunCandidate
  const hasWorkflowRun = Boolean(guideRun)
  const hydratedRunMatches = Boolean(projectId && flow.run?.projectId === projectId)
  const hasCompletedDeliveryRun = Boolean(
    hydratedRunMatches
    && flow.run?.status === 'completed'
    && flow.run.nodes.some((node) =>
      node.type === 'workbench_build' && node.status === 'completed',
    ),
  )
  const activeBundle = flow.bundle
  const hasWorkbenchReady = hasCompletedDeliveryRun || Boolean(
    hydratedRunMatches
    && (
      flow.workbenchGroups.length > 0
      || (
        activeBundle !== null
        && activeBundle.projectId === projectId
        && activeBundle.workflowRunId === flow.run?.id
      )
    ),
  )
  const hasWorkspaceRevision = Boolean(
    flow.workspaceRevision
    && projectId
    && activeBundle?.projectId === projectId,
  )
  const progress = useMemo(() => deriveOnboardingProgress({
    signedIn: collaboration.session.signedIn,
    hasProject: Boolean(projectId),
    hasProjectBrief: Boolean(projectBrief),
    isSoloProject: collaboration.project?.governanceMode === 'solo',
    hasIndependentOwner: !collaboration.loading && collaboration.members.some((member) =>
      member.role === 'owner' && member.user.id !== currentUserId,
    ),
    canEdit,
    hasWorkflowRun,
    hasWorkbenchReady,
    hasCompletedDeliveryRun,
    hasWorkspaceRevision,
  }), [
    collaboration.loading,
    collaboration.members,
    collaboration.project?.governanceMode,
    collaboration.session.signedIn,
    canEdit,
    currentUserId,
    hasCompletedDeliveryRun,
    hasWorkbenchReady,
    hasWorkflowRun,
    hasWorkspaceRevision,
    projectBrief,
    projectId,
  ])
  const [open, setOpen] = useState(false)
  const autoOpened = useRef(false)
  const priorStep = useRef(progress.currentStepId)
  const followProgress = useRef(false)
  const triggerButton = useRef<HTMLButtonElement>(null)
  const closeButton = useRef<HTMLButtonElement>(null)
  const dialog = useRef<HTMLElement>(null)
  const restoreFocus = useRef<HTMLElement | null>(null)
  const completedRunHydrationAttempt = useRef<string | null>(null)

  useEffect(() => {
    if (
      !routeReady
      || !preference.isHydrated
      || collaboration.loading
      || collaboration.backendStatus === 'connecting'
      || (projectId && (artifacts.status === 'loading' || flow.status === 'loading'))
      || autoOpened.current
    ) return
    autoOpened.current = true
    if (shouldAutoOpenOnboarding(preference.value)) setOpen(true)
  }, [
    artifacts.status,
    collaboration.backendStatus,
    collaboration.loading,
    flow.status,
    preference.isHydrated,
    preference.value,
    projectId,
    routeReady,
  ])

  useEffect(() => {
    if (
      !open
      || resumableRun
      || !completedRunCandidate
      || flow.run?.id === completedRunCandidate.id
      || flow.status === 'loading'
      || completedRunHydrationAttempt.current === completedRunCandidate.id
    ) return
    completedRunHydrationAttempt.current = completedRunCandidate.id
    void flow.loadRun(completedRunCandidate.id)
  }, [completedRunCandidate, flow, open, resumableRun])

  useEffect(() => {
    const previous = priorStep.current
    priorStep.current = progress.currentStepId
    if (followProgress.current && previous !== progress.currentStepId) {
      setOpen(true)
      if (progress.complete) followProgress.current = false
    }
  }, [progress.complete, progress.currentStepId])

  useEffect(() => {
    if (!open) return
    restoreFocus.current = document.activeElement instanceof HTMLElement
      && document.activeElement !== document.body
      ? document.activeElement
      : triggerButton.current
    closeButton.current?.focus()

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        followProgress.current = false
        setOpen(false)
        return
      }
      if (event.key !== 'Tab' || !dialog.current) return
      const focusable = [...dialog.current.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      )].filter((element) => element.getAttribute('aria-hidden') !== 'true')
      if (focusable.length === 0) {
        event.preventDefault()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => {
      window.removeEventListener('keydown', handleKeyDown)
      const target = restoreFocus.current
      const targetStyle = target ? window.getComputedStyle(target) : null
      if (
        target
        && target !== document.body
        && target.isConnected
        && !target.hidden
        && !target.closest('[hidden], [aria-hidden="true"]')
        && targetStyle?.display !== 'none'
        && targetStyle?.visibility !== 'hidden'
      ) {
        target.focus()
      } else {
        triggerButton.current?.focus()
      }
    }
  }, [open])

  function openStep(stepId: OnboardingStepId, shouldFollow = true) {
    followProgress.current = shouldFollow
    if (stepId === 'account') {
      setSurface('settings')
    } else if (stepId === 'project') {
      setSurface('recent')
    } else if (stepId === 'brief') {
      if (projectBrief) setSelectedDocId(projectBrief.artifact.id)
      setSurface('team')
      setTeamView(projectBrief ? 'editor' : 'dashboard')
    } else if (stepId === 'reviewer') {
      if (collaboration.project?.role === 'owner') {
        setSurface('settings')
      } else {
        setSurface('team')
        setTeamView('members')
      }
    } else if (stepId === 'workflow') {
      if (progress.workflowAction === 'contact_owner') {
        setSurface('team')
        setTeamView('members')
      } else {
        setSurface('workbench')
        setView('preview')
        if (guideRun && flow.run?.id !== guideRun.id) {
          void flow.loadRun(guideRun.id)
        }
      }
    } else {
      setSurface('workbench')
      setView('code')
    }
    setOpen(false)
  }

  function closeForNow() {
    followProgress.current = false
    setOpen(false)
  }

  function dismissGuide() {
    followProgress.current = false
    preference.setValue((current) => ({
      ...current,
      dismissedVersion: ONBOARDING_GUIDE_VERSION,
    }))
    setOpen(false)
  }

  function finishGuide() {
    followProgress.current = false
    preference.setValue((current) => ({
      ...current,
      completedVersion: ONBOARDING_GUIDE_VERSION,
    }))
    setOpen(false)
  }

  return (
    <>
      <button
        ref={triggerButton}
        type="button"
        onClick={() => {
          followProgress.current = false
          setOpen(true)
        }}
        className={cn(
          'group relative flex h-12 w-12 flex-col items-center justify-center gap-0.5 rounded-lg text-[9px] font-medium text-muted-foreground transition-colors hover:bg-white/5 hover:text-foreground',
          className,
        )}
        aria-label={t('onboarding.open')}
        aria-haspopup="dialog"
        aria-expanded={open}
        title={t('onboarding.open')}
      >
        <CircleHelp className="h-[18px] w-[18px]" />
        <span className="max-w-[52px] truncate leading-none">{t('onboarding.shortLabel')}</span>
        {!progress.complete && (
          <span aria-hidden="true" className="absolute right-1 top-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-warning px-1 text-[8px] font-semibold text-black">
            {progress.totalCount - progress.completedCount}
          </span>
        )}
      </button>

      {open && (
        <>
          <button
            type="button"
            tabIndex={-1}
            aria-hidden="true"
            className="fixed inset-0 z-[90] cursor-default bg-black/55 backdrop-blur-[1px]"
            onClick={closeForNow}
          />
          <aside
            ref={dialog}
            role="dialog"
            aria-modal="true"
            aria-labelledby="onboarding-title"
            className="fixed bottom-3 right-3 top-3 z-[100] flex w-[420px] max-w-[calc(100vw-24px)] flex-col overflow-hidden rounded-xl border border-border bg-panel shadow-2xl max-sm:left-3 max-sm:w-auto"
          >
            <header className="border-b border-border bg-gradient-to-br from-primary/20 via-panel to-panel p-5">
              <div className="flex items-start gap-3">
                <span className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                  {progress.complete ? <PartyPopper className="size-5" /> : <CircleHelp className="size-5" />}
                </span>
                <span className="min-w-0 flex-1">
                  <h2 id="onboarding-title" className="text-base font-semibold text-foreground">
                    {progress.complete ? t('onboarding.completeTitle') : t('onboarding.title')}
                  </h2>
                  <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">
                    {progress.complete ? t('onboarding.completeDescription') : t('onboarding.description')}
                  </p>
                </span>
                <button
                  ref={closeButton}
                  type="button"
                  onClick={closeForNow}
                  className="rounded-md p-1.5 text-muted-foreground hover:bg-white/5 hover:text-foreground"
                  aria-label={t('onboarding.close')}
                >
                  <X className="size-4" />
                </button>
              </div>
              <div className="mt-4 flex items-center gap-3">
                <div
                  role="progressbar"
                  aria-label={t('onboarding.progress', {
                    completed: progress.completedCount,
                    total: progress.totalCount,
                  })}
                  aria-valuemin={0}
                  aria-valuemax={progress.totalCount}
                  aria-valuenow={progress.completedCount}
                  className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-white/10"
                >
                  <div
                    className="h-full rounded-full bg-primary-bright transition-[width]"
                    style={{ width: `${(progress.completedCount / progress.totalCount) * 100}%` }}
                  />
                </div>
                <span className="shrink-0 text-[10px] font-medium text-muted-foreground">
                  {t('onboarding.progress', {
                    completed: progress.completedCount,
                    total: progress.totalCount,
                  })}
                </span>
              </div>
            </header>

            <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-4 scrollbar-thin">
              {collaboration.backendStatus === 'error' && (
                <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[10px] text-destructive">
                  {t('onboarding.backendUnavailable')}
                </div>
              )}
              {progress.steps.map((step, index) => {
                const copy = STEP_COPY[step.id]
                const Icon = copy.icon
                const isCurrent = step.id === progress.currentStepId
                const status = step.complete
                  ? t('onboarding.status.completed')
                  : isCurrent
                    ? t('onboarding.status.current')
                    : step.available
                      ? t('onboarding.status.available')
                      : t('onboarding.status.locked')
                const showCompletedBriefAction = step.id === 'brief' && step.complete
                const showAction = step.available && (!step.complete || showCompletedBriefAction)
                const action = step.id === 'brief' && step.complete
                  ? t('onboarding.step.brief.viewAction')
                  : step.id === 'reviewer' && collaboration.project?.role !== 'owner'
                    ? t('onboarding.step.reviewer.memberAction')
                  : step.id === 'workflow' && progress.workflowAction === 'contact_owner'
                    ? t('onboarding.step.workflow.contactOwnerAction')
                    : step.id === 'workflow' && progress.workflowAction === 'view'
                      ? t('onboarding.step.workflow.viewAction')
                      : step.id === 'implementation' && progress.implementationAction === 'view'
                        ? t('onboarding.step.implementation.viewAction')
                        : t(copy.action)
                const description = step.id === 'brief' && !projectBrief
                  ? t('onboarding.step.brief.missingDescription')
                  : step.id === 'reviewer'
                    && collaboration.project?.governanceMode === 'solo'
                    ? t('onboarding.step.reviewer.soloDescription')
                  : step.id === 'reviewer'
                    && !step.complete
                    && collaboration.project?.role !== 'owner'
                    ? t('onboarding.step.reviewer.nonOwnerDescription')
                    : step.id === 'workflow' && progress.workflowAction === 'contact_owner'
                      ? t('onboarding.step.workflow.contactOwnerDescription')
                      : step.id === 'workflow'
                        && guideRun?.status === 'completed'
                        && !hasWorkbenchReady
                        ? t('onboarding.step.workflow.completedDescription')
                    : step.id === 'workflow' && hasWorkflowRun && !hasWorkbenchReady
                      ? t('onboarding.step.workflow.activeDescription')
                      : step.id === 'implementation'
                        && progress.implementationAction === 'view'
                        && !step.complete
                        ? t('onboarding.step.implementation.readOnlyDescription')
                      : t(copy.description)
                return (
                  <section
                    key={step.id}
                    className={cn(
                      'rounded-lg border p-3 transition-colors',
                      step.complete
                        ? 'border-success/25 bg-success/5'
                        : isCurrent
                          ? 'border-primary/45 bg-primary/10'
                          : 'border-border bg-background/45',
                    )}
                  >
                    <span className="sr-only">{status}</span>
                    <div className="flex items-start gap-3">
                      <span className={cn(
                        'flex size-8 shrink-0 items-center justify-center rounded-full border text-[10px] font-semibold',
                        step.complete
                          ? 'border-success/35 bg-success/15 text-success'
                          : step.available
                            ? 'border-primary/35 bg-primary/15 text-primary-bright'
                            : 'border-border bg-white/5 text-faint-foreground',
                      )}>
                        {step.complete
                          ? <Check className="size-4" />
                          : step.available
                            ? <Icon className="size-4" />
                            : <LockKeyhole className="size-3.5" />}
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-2">
                          <span className="text-[9px] font-semibold uppercase tracking-wide text-faint-foreground">
                            {t('onboarding.stepNumber', { number: index + 1 })}
                          </span>
                          {isCurrent && (
                            <span className="rounded bg-primary/15 px-1.5 py-0.5 text-[8px] font-semibold text-primary-bright">
                              {t('onboarding.current')}
                            </span>
                          )}
                        </span>
                        <h3 className="mt-0.5 text-[12px] font-semibold text-foreground">{t(copy.title)}</h3>
                        <p className="mt-1 text-[10px] leading-relaxed text-muted-foreground">{description}</p>
                        {showAction && (
                          <button
                            type="button"
                            onClick={() => openStep(step.id, !step.complete)}
                            className="mt-2 inline-flex h-8 items-center gap-1.5 rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground hover:bg-primary/90"
                          >
                            <ArrowRight className="size-3" />
                            {action}
                          </button>
                        )}
                        {!step.complete && !step.available && (
                          <p className="mt-2 text-[9px] text-faint-foreground">{t('onboarding.locked')}</p>
                        )}
                      </span>
                    </div>
                  </section>
                )
              })}
            </div>

            <footer className="flex items-center justify-between gap-3 border-t border-border bg-background/55 px-4 py-3">
              <button
                type="button"
                onClick={dismissGuide}
                className="text-[10px] text-faint-foreground hover:text-foreground"
              >
                {t('onboarding.dismiss')}
              </button>
              <button
                type="button"
                onClick={progress.complete ? finishGuide : closeForNow}
                className="rounded-md border border-border bg-panel px-3 py-2 text-[10px] font-semibold text-foreground hover:border-primary/40"
              >
                {progress.complete ? t('onboarding.finish') : t('onboarding.later')}
              </button>
            </footer>
          </aside>
        </>
      )}
    </>
  )
}
