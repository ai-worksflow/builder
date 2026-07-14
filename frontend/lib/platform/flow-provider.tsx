'use client'

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useCollaboration } from '../collaboration/provider'
import { collaborationErrorMessage } from '../collaboration/platform-adapter'
import { useArtifactWorkspace } from './artifact-provider'
import { PlatformFlowClient } from './flow-client'
import {
  projectBriefEntryAction,
  projectBriefWorkflowManifestInput,
} from './workflow-entry'
import { PlatformHttpError } from './http'
import { wireVersionRef } from './wire-version-ref'
import {
  appliedWorkbenchProposalIds,
  canApplyWorkbenchQueueItem,
  hydrateWorkbenchQueue,
  nextPendingWorkbenchQueueIndex,
  proposalIsApplied,
  replaceWorkbenchQueueProposal,
  upsertWorkbenchBundle,
  workbenchBundleNeedsRebase,
  workbenchQueueItemHasAppliedPredecessors,
  workbenchQueueItemIndexForProposal,
  workbenchRootBundleId,
  workflowWorkbenchQueueGroups,
  type WorkbenchQueueGroup,
  type WorkbenchQueueItem,
} from './flow-queue'
import type {
  CreateImplementationProposalInputDto,
  BlueprintSelectionCompileInputDto,
  CreateWorkflowDefinitionInputDto,
  CreateWorkflowDefinitionVersionInputDto,
  ExactArtifactRefDto,
  FileOperationDto,
  ImplementationProposalDto,
  InputManifestDto,
  WorkflowDefinitionRecordDto,
  WorkflowCapabilitiesDto,
  WorkflowEventDto,
  WorkflowNodeRunDto,
  WorkflowRunDto,
  WorkflowRunSummaryDto,
  WorkbenchBundleDto,
  WorkspaceRevisionDto,
} from './flow-contract'
import type { ArtifactRevisionDto, JsonObject, PrototypeContentDto, VersionedArtifactDto } from './dto'

export type PlatformFlowStatus = 'idle' | 'loading' | 'ready' | 'error'

interface StartRunOptions {
  readonly definitionVersionId?: string
  readonly scope?: JsonObject
}

interface StartManifestOptions extends StartRunOptions {
  readonly definitionKey?: string
}

interface WorkbenchBundleExpectation {
  readonly runId: string
  readonly rootBundleId: string
  readonly deliverySliceId?: string
  readonly manifestGroupKey?: string
}

interface WorkbenchProposalExpectation extends WorkbenchBundleExpectation {
  readonly proposalId: string
  readonly buildManifestId: string
  readonly conversationCommandId?: string
  readonly instructionHash?: string
}

interface PlatformFlowContextState {
  readonly status: PlatformFlowStatus
  readonly busy: boolean
  readonly error: string | null
  readonly definitions: readonly WorkflowDefinitionRecordDto[]
  readonly capabilities: WorkflowCapabilitiesDto | null
  readonly definitionVersions: readonly WorkflowDefinitionRecordDto[]
  readonly selectedDefinition: WorkflowDefinitionRecordDto | null
  /** Exact immutable definition pinned by the hydrated run, independent from authoring selection. */
  readonly runDefinition: WorkflowDefinitionRecordDto | null
  readonly manifest: InputManifestDto | null
  readonly runs: readonly WorkflowRunSummaryDto[]
  readonly run: WorkflowRunDto | null
  readonly events: readonly WorkflowEventDto[]
  readonly workbenchQueue: readonly WorkbenchQueueItem[]
  readonly workbenchGroups: readonly WorkbenchQueueGroup[]
  readonly selectedWorkbenchNodeKey: string | null
  readonly selectedBundleId: string | null
  readonly workbenchProgress: { readonly applied: number; readonly total: number }
  readonly canApplyProposal: boolean
  readonly canCompleteWorkbench: boolean
  readonly requiresWorkbenchRebase: boolean
  readonly bundle: WorkbenchBundleDto | null
  readonly proposal: ImplementationProposalDto | null
  readonly workspaceRevision: WorkspaceRevisionDto | null
  readonly selectDefinition: (definitionId: string) => Promise<void>
  readonly refresh: () => Promise<void>
  readonly createDefinition: (input: CreateWorkflowDefinitionInputDto) => Promise<WorkflowDefinitionRecordDto | null>
  readonly createDefinitionVersion: (
    definitionId: string,
    input: CreateWorkflowDefinitionVersionInputDto,
  ) => Promise<WorkflowDefinitionRecordDto | null>
  readonly publishDefinitionVersion: (
    definitionId: string,
    versionId: string,
  ) => Promise<WorkflowDefinitionRecordDto | null>
  readonly startFromProjectBrief: (options?: StartRunOptions) => Promise<WorkflowRunDto | null>
  readonly compileBlueprintSelection: (
    input: BlueprintSelectionCompileInputDto,
    blueprintETag: string,
  ) => Promise<InputManifestDto | null>
  readonly startFromManifest: (
    manifest: InputManifestDto,
    options?: StartManifestOptions,
  ) => Promise<WorkflowRunDto | null>
  readonly loadRun: (runId: string) => Promise<WorkflowRunDto | null>
  readonly submitNodeRevision: (
    node: WorkflowNodeRunDto,
    revision: ExactArtifactRefDto,
    workflowContext?: Readonly<Record<string, unknown>>,
  ) => Promise<boolean>
  readonly authorizeExecution: (node: WorkflowNodeRunDto) => Promise<boolean>
  readonly resolveReview: (
    node: WorkflowNodeRunDto,
    resolution: 'approve' | 'changes_requested' | 'waive',
    reason?: string,
    soloReviewConfirmed?: boolean,
  ) => Promise<boolean>
  readonly retryNode: (node: WorkflowNodeRunDto, reason?: string) => Promise<boolean>
  readonly cancelRun: (reason?: string) => Promise<boolean>
  readonly createBundle: (
    prototype: VersionedArtifactDto<PrototypeContentDto>,
  ) => Promise<WorkbenchBundleDto | null>
  readonly selectWorkbenchBundle: (bundleId: string) => void
  readonly selectWorkbenchGroup: (nodeKey: string) => Promise<void>
  readonly loadBundle: (
    bundleId: string,
    expectation?: WorkbenchBundleExpectation,
  ) => Promise<WorkbenchBundleDto | null>
  readonly rebaseWorkbenchBundle: () => Promise<WorkbenchBundleDto | null>
  readonly generateImplementation: (
    instruction: string,
    model?: string,
    expectedBundleId?: string,
  ) => Promise<ImplementationProposalDto | null>
  readonly loadProposal: (
    proposalId: string,
    expectation?: WorkbenchProposalExpectation,
  ) => Promise<ImplementationProposalDto | null>
  readonly decideOperation: (
    operation: FileOperationDto,
    decision: 'accepted' | 'rejected',
    reason?: string,
  ) => Promise<ImplementationProposalDto | null>
  readonly decideAllPending: (decision: 'accepted' | 'rejected', reason?: string) => Promise<boolean>
  readonly applyProposal: () => Promise<WorkspaceRevisionDto | null>
  readonly proposeFileChange: (
    path: string,
    content: string,
    language?: string,
    expectedHash?: string,
  ) => Promise<ImplementationProposalDto | null>
  readonly clearError: () => void
}

const PlatformFlowContext = createContext<PlatformFlowContextState | null>(null)

export function PlatformFlowProvider({ children }: { children: ReactNode }) {
  const { session, project, platformClient, backendStatus, can } = useCollaboration()
  const artifacts = useArtifactWorkspace()
  const client = platformClient.flow
  const [status, setStatus] = useState<PlatformFlowStatus>('idle')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [definitions, setDefinitions] = useState<WorkflowDefinitionRecordDto[]>([])
  const [capabilities, setCapabilities] = useState<WorkflowCapabilitiesDto | null>(null)
  const [definitionVersions, setDefinitionVersions] = useState<WorkflowDefinitionRecordDto[]>([])
  const [runDefinition, setRunDefinition] = useState<WorkflowDefinitionRecordDto | null>(null)
  const [selectedDefinitionId, setSelectedDefinitionId] = useState<string | null>(null)
  const [manifest, setManifest] = useState<InputManifestDto | null>(null)
  const [runs, setRuns] = useState<WorkflowRunSummaryDto[]>([])
  const [run, setRun] = useState<WorkflowRunDto | null>(null)
  const [events, setEvents] = useState<WorkflowEventDto[]>([])
  const [workbenchQueue, setWorkbenchQueue] = useState<readonly WorkbenchQueueItem[]>([])
  const [workbenchGroups, setWorkbenchGroups] = useState<readonly WorkbenchQueueGroup[]>([])
  const [selectedWorkbenchNodeKey, setSelectedWorkbenchNodeKey] = useState<string | null>(null)
  const [selectedBundleId, setSelectedBundleId] = useState<string | null>(null)
  const [bundle, setBundle] = useState<WorkbenchBundleDto | null>(null)
  const [proposal, setProposal] = useState<ImplementationProposalDto | null>(null)
  const [workspaceRevision, setWorkspaceRevision] = useState<WorkspaceRevisionDto | null>(null)
  const projectId = project?.id ?? null
  const requestCounter = useRef(0)
  const runRequestCounter = useRef(0)
  const definitionRequestCounter = useRef(0)
  const workbenchHydrationRequestCounter = useRef(0)
  const runLoadRef = useRef<{
    readonly runId: string
    readonly promise: Promise<WorkflowRunDto | null>
  } | null>(null)
  const runRef = useRef(run)
  const eventsRef = useRef(events)
  const selectedDefinitionIdRef = useRef(selectedDefinitionId)
  const workbenchQueueRef = useRef(workbenchQueue)
  const workbenchGroupsRef = useRef(workbenchGroups)
  const selectedWorkbenchNodeKeyRef = useRef<string | null>(
    selectedWorkbenchNodeKey ?? queryReferences().workbenchNodeKey ?? null,
  )
  const selectedBundleIdRef = useRef(selectedBundleId)
  const bundleRef = useRef(bundle)
  const proposalRef = useRef(proposal)
  const workspaceRevisionRef = useRef(workspaceRevision)
  const projectIdRef = useRef(projectId)
  projectIdRef.current = projectId
  runRef.current = run
  eventsRef.current = events
  selectedDefinitionIdRef.current = selectedDefinitionId
  workbenchQueueRef.current = workbenchQueue
  workbenchGroupsRef.current = workbenchGroups
  if (selectedWorkbenchNodeKey) selectedWorkbenchNodeKeyRef.current = selectedWorkbenchNodeKey
  selectedBundleIdRef.current = selectedBundleId
  bundleRef.current = bundle
  proposalRef.current = proposal
  workspaceRevisionRef.current = workspaceRevision

  const fail = useCallback((cause: unknown, fallback: string) => {
    setError(collaborationErrorMessage(cause, fallback))
    if (!(cause instanceof PlatformHttpError && [403, 409, 412, 422].includes(cause.status))) {
      setStatus('error')
    }
  }, [])

  const storeWorkbenchQueue = useCallback((queue: readonly WorkbenchQueueItem[]) => {
    workbenchQueueRef.current = queue
    setWorkbenchQueue(queue)
  }, [])

  const activateWorkbenchItem = useCallback((
    item: WorkbenchQueueItem | null,
    updateQuery = true,
  ) => {
    const bundleValue = item?.bundle ?? null
    const proposalValue = item?.proposal ?? null
    selectedBundleIdRef.current = item?.bundleId ?? null
    bundleRef.current = bundleValue
    proposalRef.current = proposalValue
    setSelectedBundleId(item?.bundleId ?? null)
    setBundle(bundleValue)
    setProposal(proposalValue)
    if (updateQuery) {
      setQueryReference('bundleId', item?.bundleId)
      setQueryReference('proposalId', proposalValue?.id)
    }
  }, [])

  const selectWorkbenchBundle = useCallback((bundleId: string) => {
    const item = workbenchQueueRef.current.find((candidate) => candidate.bundleId === bundleId)
    if (item) activateWorkbenchItem(item)
  }, [activateWorkbenchItem])

  const loadDefinitionVersions = useCallback(async (definitionId: string) => {
    if (!projectId) return
    const requestId = ++definitionRequestCounter.current
    selectedDefinitionIdRef.current = definitionId
    setSelectedDefinitionId(definitionId)
    setDefinitionVersions([])
    try {
      const result = await client.listDefinitionVersions(projectId, definitionId, { limit: 200 })
      if (
        requestId !== definitionRequestCounter.current
        || selectedDefinitionIdRef.current !== definitionId
      ) return
      setDefinitionVersions([...result.data.items].sort((left, right) => right.version - left.version))
    } catch (cause) {
      if (
        requestId === definitionRequestCounter.current
        && selectedDefinitionIdRef.current === definitionId
      ) fail(cause, 'Unable to load workflow versions.')
    }
  }, [client, fail, projectId])

  const refresh = useCallback(async () => {
    if (!session.signedIn || !projectId) {
      setStatus('idle')
      setDefinitions([])
      setCapabilities(null)
      setDefinitionVersions([])
      setSelectedDefinitionId(null)
      setRuns([])
      return
    }
    const requestId = ++requestCounter.current
    const definitionRequestId = ++definitionRequestCounter.current
    setStatus('loading')
    setError(null)
    try {
      const [result, runResult, capabilityResult] = await Promise.all([
        client.listDefinitions(projectId, { limit: 200 }),
        client.listRuns(projectId, {}, { limit: 100 }),
        client.capabilities(projectId),
      ])
      if (requestId !== requestCounter.current) return
      const items = [...result.data.items].sort((left, right) =>
        left.title.localeCompare(right.title) || right.version - left.version,
      )
      setDefinitions(items)
      setCapabilities(capabilityResult.data)
      setRuns([...runResult.data.items].sort((left, right) => right.updatedAt.localeCompare(left.updatedAt)))
      if (definitionRequestId === definitionRequestCounter.current) {
        const selected = items.find((item) => item.id === selectedDefinitionIdRef.current)
          ?? items.find((item) => item.published)
          ?? items[0]
        if (selected) {
          selectedDefinitionIdRef.current = selected.id
          setSelectedDefinitionId(selected.id)
          const versions = await client.listDefinitionVersions(projectId, selected.id, { limit: 200 })
          if (requestId !== requestCounter.current) return
          if (
            definitionRequestId === definitionRequestCounter.current
            && selectedDefinitionIdRef.current === selected.id
          ) {
            setDefinitionVersions([...versions.data.items].sort((left, right) => right.version - left.version))
          }
        } else {
          selectedDefinitionIdRef.current = null
          setSelectedDefinitionId(null)
          setDefinitionVersions([])
        }
      }
      setStatus('ready')
    } catch (cause) {
      if (requestId === requestCounter.current) fail(cause, 'Workflow service is unavailable.')
    }
  }, [client, fail, projectId, session.signedIn])

  const workbenchHydrationIsCurrent = useCallback((
    requestId: number,
    expectation?: WorkbenchBundleExpectation,
  ) => {
    if (requestId !== workbenchHydrationRequestCounter.current) return false
    if (!projectId || projectIdRef.current !== projectId) return false
    if (!expectation) return true
    if (runRef.current?.id !== expectation.runId) return false
    const group = workbenchGroupsRef.current.find(
      (candidate) => candidate.nodeKey === selectedWorkbenchNodeKeyRef.current,
    )
    if (!group) return false
    if (
      expectation.manifestGroupKey
      && group.manifestGroupKey !== expectation.manifestGroupKey
    ) return false
    return group.references.some((reference) => (
      reference.bundleId === expectation.rootBundleId
      && (!expectation.deliverySliceId || reference.sliceId === expectation.deliverySliceId)
    ))
  }, [projectId])

  const loadBundle = useCallback(async (
    bundleId: string,
    expectation?: WorkbenchBundleExpectation,
  ) => {
    if (!projectId) return null
    const requestId = ++workbenchHydrationRequestCounter.current
    try {
      const result = await client.getWorkbenchBundle(bundleId)
      if (!workbenchHydrationIsCurrent(requestId, expectation)) return null
      if (result.data.projectId !== projectId) {
        throw new Error('Workbench bundle belongs to another project.')
      }
      if (!workbenchBundleMatchesExpectation(result.data, expectation)) {
        throw new Error('Workbench bundle hydration does not match the expected run, manifest group, root, and delivery slice.')
      }
      let nextWorkspace: WorkspaceRevisionDto | null = null
      if (result.data.currentWorkspaceRevision) {
        const workspace = await client.getWorkspaceRevision(
          result.data.currentWorkspaceRevision.revisionId,
        )
        if (!workbenchHydrationIsCurrent(requestId, expectation)) return null
        if (!workspaceRevisionMatchesRef(workspace.data, result.data.currentWorkspaceRevision)) {
          throw new Error('Workbench bundle hydration returned a workspace outside its exact lineage reference.')
        }
        nextWorkspace = workspace.data
      }
      if (!workbenchHydrationIsCurrent(requestId, expectation)) return null
      const nextQueue = upsertWorkbenchBundle(workbenchQueueRef.current, result.data)
      storeWorkbenchQueue(nextQueue)
      const rootBundleId = workbenchRootBundleId(result.data)
      activateWorkbenchItem(nextQueue.find((item) => item.bundleId === rootBundleId) ?? null)
      workspaceRevisionRef.current = nextWorkspace
      setWorkspaceRevision(nextWorkspace)
      setQueryReference('workspaceRevisionId', nextWorkspace?.id)
      return result.data
    } catch (cause) {
      if (!workbenchHydrationIsCurrent(requestId, expectation)) return null
      fail(cause, 'Unable to load the frozen build manifest.')
      return null
    }
  }, [activateWorkbenchItem, client, fail, projectId, storeWorkbenchQueue, workbenchHydrationIsCurrent])

  const loadProposal = useCallback(async (
    proposalId: string,
    expectation?: WorkbenchProposalExpectation,
  ) => {
    if (!projectId) return null
    const requestId = ++workbenchHydrationRequestCounter.current
    try {
      const result = await client.getImplementationProposal(proposalId)
      if (!workbenchHydrationIsCurrent(requestId, expectation)) return null
      if (result.data.projectId !== projectId) {
        throw new Error('Implementation proposal belongs to another project.')
      }
      if (
        result.data.id !== proposalId
        || (
          expectation
          && (
            expectation.proposalId !== proposalId
            || result.data.id !== expectation.proposalId
            || result.data.buildManifestId !== expectation.buildManifestId
            || (
              expectation.conversationCommandId
              && (
                result.data.executionSource !== 'conversation_command'
                || result.data.conversationCommandId !== expectation.conversationCommandId
              )
            )
            || (
              expectation.instructionHash
              && result.data.instructionHash !== expectation.instructionHash
            )
          )
        )
      ) {
        throw new Error('Implementation proposal hydration does not match the exact reviewed proposal and build manifest receipt.')
      }
      let knownBundle = workbenchQueueRef.current.find(
        (item) => item.bundle?.id === result.data.buildManifestId,
      )?.bundle ?? null
      if (!knownBundle) {
        const bundleResult = await client.getWorkbenchBundle(result.data.buildManifestId)
        if (!workbenchHydrationIsCurrent(requestId, expectation)) return null
        knownBundle = bundleResult.data
      }
      if (
        knownBundle.id !== result.data.buildManifestId
        || knownBundle.projectId !== result.data.projectId
        || knownBundle.projectId !== projectId
        || !workbenchBundleMatchesExpectation(knownBundle, expectation)
      ) {
        throw new Error('Implementation proposal hydration does not match the exact reviewed run, group, root, and delivery slice receipt.')
      }
      if (!workbenchHydrationIsCurrent(requestId, expectation)) return null
      const withBundle = upsertWorkbenchBundle(workbenchQueueRef.current, knownBundle)
      const nextQueue = replaceWorkbenchQueueProposal(withBundle, result.data, knownBundle)
      storeWorkbenchQueue(nextQueue)
      const itemIndex = workbenchQueueItemIndexForProposal(nextQueue, result.data)
      activateWorkbenchItem(itemIndex >= 0 ? nextQueue[itemIndex] : null)
      return result.data
    } catch (cause) {
      if (!workbenchHydrationIsCurrent(requestId, expectation)) return null
      fail(cause, 'Unable to load the implementation proposal.')
      return null
    }
  }, [activateWorkbenchItem, client, fail, projectId, storeWorkbenchQueue, workbenchHydrationIsCurrent])

  const hydrateWorkbenchGroup = useCallback(async (
    nextRun: WorkflowRunDto,
    group: WorkbenchQueueGroup | null,
    preserveBundleSelection: boolean,
    requestId?: number,
  ) => {
    const hydrationIsCurrent = () => (
      runRef.current?.id === nextRun.id
      && (requestId === undefined || requestId === runRequestCounter.current)
      && (!group || selectedWorkbenchNodeKeyRef.current === group.nodeKey)
    )
    if (!hydrationIsCurrent()) return
    const references = group?.references ?? []
    if (references.length > 0) {
      const lineageResults = await Promise.all(
        references.map((reference) => client.getWorkbenchBundleLineageState(reference.bundleId)),
      )
      if (!hydrationIsCurrent()) return
      for (const [index, result] of lineageResults.entries()) {
        const reference = references[index]
        const state = result.data
        const expectedSliceId = reference.sliceId?.trim() ?? ''
        const actualSliceId = state.activeBundle.deliverySliceId?.trim() ?? ''
        const expectedManifestGroup = group?.manifestGroupKey?.trim() ?? ''
        const actualManifestGroup = state.activeBundle.manifestGroupKey?.trim() ?? ''
        if (
          state.rootBundleId !== reference.bundleId
          || workbenchRootBundleId(state.activeBundle) !== reference.bundleId
          || state.activeBundle.projectId !== nextRun.projectId
          || state.activeBundle.workflowRunId !== nextRun.id
          || (expectedSliceId !== '' && actualSliceId !== expectedSliceId)
          || (expectedSliceId !== ''
            && state.activeBundle.workflowContext?.deliverySliceId !== expectedSliceId)
          || (expectedManifestGroup !== '' && actualManifestGroup !== expectedManifestGroup)
          || (
            state.currentProposal
            && state.currentProposal.buildManifestId !== state.activeBundle.id
          )
        ) {
          throw new Error(`Workbench lineage state for ${reference.bundleId} does not match its exact run, manifest group, ordinal, or delivery slice.`)
        }
      }
      const currentWorkspaceRevision = lineageResults[0]?.data.currentWorkspaceRevision
      if (!lineageResults.every((result) => exactArtifactRefEqual(
        result.data.currentWorkspaceRevision,
        currentWorkspaceRevision,
      ))) {
        throw new Error('Workbench lineage states disagree about the current project workspace revision.')
      }
      const hydratedReferences = references.map((reference, index) => {
        const currentProposal = lineageResults[index].data.currentProposal
        return {
          bundleId: reference.bundleId,
          ...(reference.sliceId ? { sliceId: reference.sliceId } : {}),
          ...(currentProposal ? { proposalId: currentProposal.id } : {}),
        }
      })
      const nextQueue = hydrateWorkbenchQueue(
        hydratedReferences,
        lineageResults.map((result) => result.data.activeBundle),
        lineageResults.flatMap((result) =>
          result.data.currentProposal ? [result.data.currentProposal] : []),
      )
      storeWorkbenchQueue(nextQueue)
      const retained = preserveBundleSelection
        ? nextQueue.find((item) => item.bundleId === selectedBundleIdRef.current)
        : undefined
      const pendingIndex = nextPendingWorkbenchQueueIndex(nextQueue)
      const active = retained ?? nextQueue[pendingIndex >= 0 ? pendingIndex : 0] ?? null
      activateWorkbenchItem(active)
      if (
        currentWorkspaceRevision
        && !workspaceRevisionMatchesRef(workspaceRevisionRef.current, currentWorkspaceRevision)
      ) {
        const workspace = await client.getWorkspaceRevision(currentWorkspaceRevision.revisionId)
        if (!hydrationIsCurrent()) return
        if (!workspaceRevisionMatchesRef(workspace.data, currentWorkspaceRevision)) {
          throw new Error('The workspace revision response does not match the exact project lineage reference.')
        }
        workspaceRevisionRef.current = workspace.data
        setWorkspaceRevision(workspace.data)
        setQueryReference('workspaceRevisionId', workspace.data.id)
      } else if (!currentWorkspaceRevision) {
        workspaceRevisionRef.current = null
        setWorkspaceRevision(null)
        setQueryReference('workspaceRevisionId')
      }
    } else {
      storeWorkbenchQueue([])
      activateWorkbenchItem(null)
    }

    const revisionId = group
      ? nextRun.nodes.find((node) => node.key === group.nodeKey)?.outputRevisionId
      : undefined
    if (references.length === 0 && revisionId && workspaceRevisionRef.current?.id !== revisionId) {
      try {
        const result = await client.getWorkspaceRevision(revisionId)
        if (!hydrationIsCurrent()) return
        setWorkspaceRevision(result.data)
        setQueryReference('workspaceRevisionId', result.data.id)
      } catch (cause) {
        fail(cause, 'Unable to load the applied application workspace.')
      }
    }
  }, [activateWorkbenchItem, client, fail, storeWorkbenchQueue])

  const hydrateRunOutputs = useCallback(async (nextRun: WorkflowRunDto, requestId: number) => {
    const hydrationIsCurrent = () => (
      requestId === runRequestCounter.current
      && runRef.current?.id === nextRun.id
    )
    if (!hydrationIsCurrent()) return
    const groups = workflowWorkbenchQueueGroups(nextRun)
    workbenchGroupsRef.current = groups
    setWorkbenchGroups(groups)
    const query = queryReferences()
    const previousNodeKey = query.workbenchNodeKey
      ?? storedWorkbenchNodeKey(nextRun.id)
      ?? selectedWorkbenchNodeKeyRef.current
    const selectedGroup = groups.find((group) => group.nodeKey === previousNodeKey)
      ?? groups.find((group) => group.references.some(
        (reference) => reference.bundleId === query.bundleId,
      ))
      ?? groups.find((group) => group.status === 'waiting_input')
      ?? groups[0]
      ?? null
    if (!hydrationIsCurrent()) return
    selectedWorkbenchNodeKeyRef.current = selectedGroup?.nodeKey ?? null
    setSelectedWorkbenchNodeKey(selectedGroup?.nodeKey ?? null)
    storeWorkbenchNodeKey(nextRun.id, selectedGroup?.nodeKey)
    setQueryReference('workbenchNodeKey', selectedGroup?.nodeKey)
    await hydrateWorkbenchGroup(
      nextRun,
      selectedGroup,
      selectedGroup?.nodeKey === previousNodeKey,
      requestId,
    )
  }, [hydrateWorkbenchGroup])

  const selectWorkbenchGroup = useCallback(async (nodeKey: string) => {
    const nextRun = runRef.current
    const group = workbenchGroupsRef.current.find((candidate) => candidate.nodeKey === nodeKey)
    if (!nextRun || !group || group.nodeKey === selectedWorkbenchNodeKeyRef.current) return
    workbenchHydrationRequestCounter.current += 1
    selectedWorkbenchNodeKeyRef.current = group.nodeKey
    setSelectedWorkbenchNodeKey(group.nodeKey)
    storeWorkbenchNodeKey(nextRun.id, group.nodeKey)
    setQueryReference('workbenchNodeKey', group.nodeKey)
    storeWorkbenchQueue([])
    activateWorkbenchItem(null)
    try {
      await hydrateWorkbenchGroup(nextRun, group, false)
    } catch (cause) {
      if (
        runRef.current?.id === nextRun.id
        && selectedWorkbenchNodeKeyRef.current === group.nodeKey
      ) fail(cause, `Unable to load Workbench group ${group.nodeKey}.`)
    }
  }, [activateWorkbenchItem, fail, hydrateWorkbenchGroup, storeWorkbenchQueue])

  const performLoadRun = useCallback(async (runId: string) => {
    if (!projectId) return null
    if (runRef.current?.id !== runId) workbenchHydrationRequestCounter.current += 1
    const requestId = ++runRequestCounter.current
    try {
      const runResult = await client.getRun(projectId, runId)
      if (requestId !== runRequestCounter.current) return null
      const currentRun = runRef.current?.id === runId ? runRef.current : null
      if (
        currentRun
        && (
          currentRun.eventCursor > runResult.data.eventCursor
          || (
            currentRun.eventCursor === runResult.data.eventCursor
            && currentRun.updatedAt.localeCompare(runResult.data.updatedAt) > 0
          )
        )
      ) return currentRun

      const existingEvents = currentRun ? eventsRef.current : []
      const after = existingEvents.reduce(
        (cursor, event) => Math.max(cursor, event.sequence),
        0,
      )
      const [newEvents, versionResult] = await Promise.all([
        loadRunEventPages(client, projectId, runId, after, runResult.data.eventCursor),
        client.listDefinitionVersions(projectId, runResult.data.definition.id, { limit: 200 }),
      ])
      if (requestId !== runRequestCounter.current) return null
      const exactDefinition = versionResult.data.items.find(
        (item) => item.versionId === runResult.data.definitionVersionId,
      ) ?? versionResult.data.items.find((item) =>
        item.version === runResult.data.definition.version
        && item.contentHash === runResult.data.definition.hash,
      )
      if (!exactDefinition) {
        throw new Error(`Workflow definition version ${runResult.data.definitionVersionId} is unavailable.`)
      }
      const mergedEvents = mergeWorkflowEvents(existingEvents, newEvents)
      runRef.current = runResult.data
      eventsRef.current = mergedEvents
      setRun(runResult.data)
      setRuns((current) => [
        runResult.data,
        ...current.filter((item) => item.id !== runResult.data.id),
      ].sort((left, right) => right.updatedAt.localeCompare(left.updatedAt)))
      setEvents(mergedEvents)
      setRunDefinition(exactDefinition)
      setQueryReference('runId', runResult.data.id)
      setStatus('ready')
      await hydrateRunOutputs(runResult.data, requestId)
      if (requestId !== runRequestCounter.current || runRef.current?.id !== runResult.data.id) {
        return null
      }
      return runResult.data
    } catch (cause) {
      if (requestId === runRequestCounter.current) fail(cause, 'Unable to load the workflow run.')
      return null
    }
  }, [client, fail, hydrateRunOutputs, projectId])

  const loadRun = useCallback((runId: string): Promise<WorkflowRunDto | null> => {
    const inFlight = runLoadRef.current
    if (inFlight?.runId === runId) return inFlight.promise
    const promise = performLoadRun(runId)
    runLoadRef.current = { runId, promise }
    void promise.then(
      () => {
        if (runLoadRef.current?.promise === promise) runLoadRef.current = null
      },
      () => {
        if (runLoadRef.current?.promise === promise) runLoadRef.current = null
      },
    )
    return promise
  }, [performLoadRun])

  useEffect(() => {
    const initialReferences = queryReferences()
    requestCounter.current += 1
    runRequestCounter.current += 1
    definitionRequestCounter.current += 1
    workbenchHydrationRequestCounter.current += 1
    runLoadRef.current = null
    setManifest(null)
    setRun(null)
    runRef.current = null
    setRunDefinition(null)
    setRuns([])
    setEvents([])
    eventsRef.current = []
    workbenchGroupsRef.current = []
    setWorkbenchGroups([])
    selectedWorkbenchNodeKeyRef.current = initialReferences.workbenchNodeKey ?? null
    setSelectedWorkbenchNodeKey(initialReferences.workbenchNodeKey ?? null)
    storeWorkbenchQueue([])
    activateWorkbenchItem(null, false)
    setBundle(null)
    setProposal(null)
    setWorkspaceRevision(null)
    if (!session.signedIn || !projectId) {
      setStatus('idle')
      return
    }
    const expectedProjectId = projectId
    const refreshPromise = refresh()
    const refreshRequestId = requestCounter.current
    void refreshPromise.then(() => {
      if (
        requestCounter.current !== refreshRequestId
        || projectIdRef.current !== expectedProjectId
      ) return
      const references = initialReferences
      if (references.runId) void loadRun(references.runId)
      else {
        if (references.bundleId) void loadBundle(references.bundleId)
        if (references.proposalId) void loadProposal(references.proposalId)
        if (
          references.workspaceRevisionId
          && !references.bundleId
          && !references.proposalId
        ) {
          const workspaceRequestId = ++workbenchHydrationRequestCounter.current
          const workspaceRevisionId = references.workspaceRevisionId
          void client.getWorkspaceRevision(references.workspaceRevisionId)
            .then(async (result) => {
              if (
                workspaceRequestId !== workbenchHydrationRequestCounter.current
                || projectIdRef.current !== expectedProjectId
              ) return
              if (result.data.id !== workspaceRevisionId) {
                fail(
                  new Error('The workspace response does not match the requested immutable revision.'),
                  'Unable to load the application workspace revision.',
                )
                return
              }
              const artifact = await platformClient.artifacts.get(result.data.artifactId)
              if (
                workspaceRequestId !== workbenchHydrationRequestCounter.current
                || projectIdRef.current !== expectedProjectId
              ) return
              if (
                artifact.data.artifact.id !== result.data.artifactId
                || artifact.data.artifact.projectId !== expectedProjectId
                || artifact.data.artifact.kind !== 'workspace'
              ) {
                fail(
                  new Error('Workspace revision belongs to another project or a non-workspace artifact.'),
                  'Unable to load the application workspace revision.',
                )
                return
              }
              workspaceRevisionRef.current = result.data
              setWorkspaceRevision(result.data)
            })
            .catch((cause) => {
              if (
                workspaceRequestId === workbenchHydrationRequestCounter.current
                && projectIdRef.current === expectedProjectId
              ) fail(cause, 'Unable to load the application workspace revision.')
            })
        }
      }
    })
  }, [
    activateWorkbenchItem,
    client,
    fail,
    loadBundle,
    loadProposal,
    loadRun,
    platformClient.artifacts,
    projectId,
    refresh,
    session.signedIn,
    storeWorkbenchQueue,
  ])

  useEffect(() => {
    if (!session.signedIn || !projectId || !run?.id) return
    const unsubscribe = platformClient.websocket.subscribeRun(projectId, run.id, () => {
      void loadRun(run.id)
    })
    platformClient.websocket.connect()
    const timer = window.setInterval(() => {
      const current = runRef.current
      if (current && !terminalRun(current.status)) void loadRun(current.id)
    }, 3_000)
    return () => {
      unsubscribe()
      window.clearInterval(timer)
    }
  }, [loadRun, platformClient.websocket, projectId, run?.id, session.signedIn])

  const createDefinition = useCallback(async (input: CreateWorkflowDefinitionInputDto) => {
    if (!projectId || !can('admin')) return null
    const authoringDefinitionId = selectedDefinitionIdRef.current
    setBusy(true)
    setError(null)
    try {
      const result = await client.createDefinition(projectId, input)
      await refresh()
      if (selectedDefinitionIdRef.current === authoringDefinitionId) {
        await loadDefinitionVersions(result.data.id)
      }
      setDefinitions((current) => [
        result.data,
        ...current.filter((item) => item.id !== result.data.id),
      ])
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to create the workflow definition.')
      return null
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, loadDefinitionVersions, projectId, refresh])

  const createDefinitionVersion = useCallback(async (
    definitionId: string,
    input: CreateWorkflowDefinitionVersionInputDto,
  ) => {
    if (!projectId || !can('admin')) return null
    const authoringDefinitionId = selectedDefinitionIdRef.current
    setBusy(true)
    setError(null)
    try {
      const result = await client.createDefinitionVersion(projectId, definitionId, input)
      await refresh()
      if (
        authoringDefinitionId === definitionId
        && selectedDefinitionIdRef.current === definitionId
      ) await loadDefinitionVersions(definitionId)
      setDefinitions((current) => [
        result.data,
        ...current.filter((item) => item.id !== definitionId),
      ])
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to save the workflow version.')
      return null
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, loadDefinitionVersions, projectId, refresh])

  const publishDefinitionVersion = useCallback(async (definitionId: string, versionId: string) => {
    if (!projectId || !can('publish')) return null
    const authoringDefinitionId = selectedDefinitionIdRef.current
    setBusy(true)
    setError(null)
    try {
      const result = await client.publishDefinitionVersion(projectId, definitionId, versionId)
      await refresh()
      if (
        authoringDefinitionId === definitionId
        && selectedDefinitionIdRef.current === definitionId
      ) await loadDefinitionVersions(definitionId)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to publish the workflow version.')
      return null
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, loadDefinitionVersions, projectId, refresh])

  const startFromProjectBrief = useCallback(async (options: StartRunOptions = {}) => {
    if (!projectId || !can('edit')) return null
    const projectBrief = artifacts.documents.find(
      (item) => String(item.artifact.kind) === 'project_brief',
    )
    if (!projectBrief) {
      setError('Create a Project Brief before starting a workflow.')
      return null
    }
    setBusy(true)
    setError(null)
    try {
      let effectiveDefinitionVersionId = options.definitionVersionId
      let requestedDefinition = effectiveDefinitionVersionId
        ? [...definitionVersions, ...definitions].find(
            (item) => item.versionId === effectiveDefinitionVersionId,
          )
        : undefined
      if (effectiveDefinitionVersionId && !requestedDefinition) {
        setError(`Workflow definition version ${effectiveDefinitionVersionId} is not loaded.`)
        return null
      }
      if (!effectiveDefinitionVersionId) {
        const minimumDefinition = definitions.find((item) => item.key === 'minimum-product-loop')
        if (minimumDefinition) {
          const versions = await client.listDefinitionVersions(
            projectId,
            minimumDefinition.id,
            { limit: 200 },
          )
          requestedDefinition = [...versions.data.items]
            .filter((item) => item.published)
            .sort((left, right) => right.version - left.version)[0]
          if (!requestedDefinition) {
            setError('The minimum product workflow has no published version.')
            return null
          }
          effectiveDefinitionVersionId = requestedDefinition.versionId
        }
      }
      const sourceNode = requestedDefinition?.definition.nodes.find(
        (node) => node.type === 'artifact_input',
      )
      // When the backend installs the missing minimum loop on first start, its
      // seeded Artifact Input explicitly accepts an unapproved Project Brief.
      const requireApproved = requestedDefinition
        ? sourceNode?.artifactInput?.requireApproved ?? true
        : false
      let sourceRevision = requireApproved
        ? projectBrief.approvedRevision
        : projectBrief.latestRevision ?? projectBrief.approvedRevision
      const entryAction = projectBriefEntryAction({
        requireApproved,
        approvedRevision: projectBrief.approvedRevision,
        latestRevision: projectBrief.latestRevision,
        draft: projectBrief.draft,
      })
      if (entryAction === 'blocked_unapproved_changes') {
        setError('The Project Brief has changes newer than its approved revision. Review and approve an immutable checkpoint before starting this workflow.')
        return null
      }
      if (entryAction === 'checkpoint_draft' && projectBrief.draft) {
        sourceRevision = await artifacts.createDocumentRevision(
          projectBrief.artifact.id,
          projectBrief.draft.content,
        )
      }
      if (!sourceRevision) {
        setError(requireApproved
          ? 'Approve an immutable Project Brief revision before starting this workflow.'
          : 'Create an immutable Project Brief revision before starting this workflow.')
        return null
      }
      const source = revisionRef(sourceRevision)
      const manifestResult = await client.createManifest(
        projectId,
        projectBriefWorkflowManifestInput(source),
      )
      setManifest(manifestResult.data)
      const runResult = await client.startRun(projectId, {
        definitionVersionId: effectiveDefinitionVersionId,
        inputManifest: PlatformFlowClient.manifestRef(manifestResult.data),
        scope: options.scope ?? {},
      })
      setRun(runResult.data)
      setRuns((current) => [
        runResult.data,
        ...current.filter((item) => item.id !== runResult.data.id),
      ])
      setEvents([])
      setQueryReference('runId', runResult.data.id)
      await loadRun(runResult.data.id)
      return runResult.data
    } catch (cause) {
      fail(cause, 'Unable to freeze the Project Brief input and start the workflow.')
      return null
    } finally {
      setBusy(false)
    }
  }, [artifacts, can, client, definitionVersions, definitions, fail, loadRun, projectId])

  const compileBlueprintSelection = useCallback(async (
    input: BlueprintSelectionCompileInputDto,
    blueprintETag: string,
  ) => {
    if (!projectId || !can('edit')) return null
    setBusy(true)
    setError(null)
    try {
      const result = await client.compileBlueprintSelection(
        projectId,
        input,
        blueprintETag,
      )
      setManifest(result.data)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to freeze the approved Blueprint selection.')
      return null
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, projectId])

  const startFromManifest = useCallback(async (
    frozenManifest: InputManifestDto,
    options: StartManifestOptions = {},
  ) => {
    if (!projectId || !can('edit') || frozenManifest.projectId !== projectId) return null
    setBusy(true)
    setError(null)
    try {
      let effectiveDefinitionVersionId = options.definitionVersionId
      if (!effectiveDefinitionVersionId) {
        const definitionKey = options.definitionKey ?? 'blueprint-selection-app'
        const definition = definitions.find((item) => item.key === definitionKey)
        if (!definition) throw new Error(`Workflow ${definitionKey} is not installed for this project.`)
        const versions = await client.listDefinitionVersions(projectId, definition.id, { limit: 200 })
        const published = [...versions.data.items]
          .filter((item) => item.published)
          .sort((left, right) => right.version - left.version)[0]
        if (!published) throw new Error(`Workflow ${definitionKey} has no published version.`)
        effectiveDefinitionVersionId = published.versionId
      }
      const result = await client.startRun(projectId, {
        definitionVersionId: effectiveDefinitionVersionId,
        inputManifest: PlatformFlowClient.manifestRef(frozenManifest),
        scope: options.scope ?? {},
      })
      setManifest(frozenManifest)
      setRun(result.data)
      setRuns((current) => [
        result.data,
        ...current.filter((item) => item.id !== result.data.id),
      ])
      setEvents([])
      setQueryReference('runId', result.data.id)
      await loadRun(result.data.id)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to start the workflow from the frozen selection.')
      return null
    } finally {
      setBusy(false)
    }
  }, [can, client, definitions, fail, loadRun, projectId])

  const submitNodeRevision = useCallback(async (
    node: WorkflowNodeRunDto,
    revision: ExactArtifactRefDto,
    workflowContext?: Readonly<Record<string, unknown>>,
  ) => {
    if (!projectId || !run || !can('edit')) return false
    setBusy(true)
    setError(null)
    try {
      await client.resumeRun(projectId, run.id, node.key, {
        artifactRevision: wireVersionRef(revision),
        ...(workflowContext ? { workflowContext } : {}),
      })
      await loadRun(run.id)
      return true
    } catch (cause) {
      fail(cause, 'Unable to submit the exact artifact revision to this workflow node.')
      return false
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, loadRun, projectId, run])

  const authorizeExecution = useCallback(async (node: WorkflowNodeRunDto) => {
    const permission = node.type === 'publish' ? 'publish' : 'edit'
    if (
      !projectId
      || !run
      || !can(permission)
      || (node.type !== 'quality_gate' && node.type !== 'publish')
    ) return false
    setBusy(true)
    setError(null)
    try {
      await client.authorizeExecution(projectId, run.id, node.key)
      await loadRun(run.id)
      return true
    } catch (cause) {
      fail(cause, 'Unable to authorize this privileged workflow operation as the current member.')
      return false
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, loadRun, projectId, run])

  const resolveReview = useCallback(async (
    node: WorkflowNodeRunDto,
    resolution: 'approve' | 'changes_requested' | 'waive',
    reason = '',
    soloReviewConfirmed = false,
  ) => {
    if (!projectId || !run) return false
    setBusy(true)
    setError(null)
    try {
      await client.resolveReview(
        projectId,
        run.id,
        node.key,
        resolution,
        reason,
        soloReviewConfirmed,
      )
      await loadRun(run.id)
      return true
    } catch (cause) {
      fail(cause, 'The canonical artifact review gate has not been satisfied.')
      return false
    } finally {
      setBusy(false)
    }
  }, [client, fail, loadRun, projectId, run])

  const retryNode = useCallback(async (node: WorkflowNodeRunDto, reason = 'Retry from Workbench') => {
    if (!projectId || !run || !can('edit')) return false
    setBusy(true)
    try {
      await client.retryNode(projectId, run.id, node.key, reason)
      await loadRun(run.id)
      return true
    } catch (cause) {
      fail(cause, 'Unable to retry this workflow node.')
      return false
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, loadRun, projectId, run])

  const cancelRun = useCallback(async (reason = 'Cancelled from Workbench') => {
    if (!projectId || !run || !can('edit')) return false
    setBusy(true)
    try {
      await client.cancelRun(projectId, run.id, reason)
      await loadRun(run.id)
      return true
    } catch (cause) {
      fail(cause, 'Unable to cancel the workflow run.')
      return false
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, loadRun, projectId, run])

  const createBundle = useCallback(async (prototype: VersionedArtifactDto<PrototypeContentDto>) => {
    if (!projectId || !can('edit')) return null
    const revision = prototype.approvedRevision
    if (!revision) {
      setError('Approve an immutable prototype revision before compiling a build manifest.')
      return null
    }
    setBusy(true)
    setError(null)
    try {
      const result = await client.createWorkbenchBundle(projectId, {
        prototypeRevision: revisionRef(revision),
      })
      const nextQueue = upsertWorkbenchBundle([], result.data)
      storeWorkbenchQueue(nextQueue)
      activateWorkbenchItem(nextQueue[0] ?? null)
      setWorkspaceRevision(null)
      workspaceRevisionRef.current = null
      setQueryReference('workspaceRevisionId')
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to compile the frozen build manifest. Check approved upstream traces.')
      return null
    } finally {
      setBusy(false)
    }
  }, [activateWorkbenchItem, can, client, fail, projectId, storeWorkbenchQueue])

  const rebaseWorkbenchBundle = useCallback(async () => {
    const workspace = workspaceRevisionRef.current
    const activeBundle = bundleRef.current
    const queue = workbenchQueueRef.current
    const activeItem = queue.find((item) => item.bundleId === selectedBundleIdRef.current)
      ?? queue.find((item) => item.bundle?.id === activeBundle?.id)
    if (!workspace || !activeBundle || !activeItem || !can('edit')) return null
    if (!workbenchBundleNeedsRebase(activeBundle, workspace)) return activeBundle
    if (proposalIsApplied(activeItem.proposal)) {
      setError('An applied page proposal cannot be moved to a different Workbench input manifest.')
      return null
    }
    setBusy(true)
    setError(null)
    try {
      const result = await client.rebaseWorkbenchBundle(
        activeBundle.id,
        revisionRef(workspace),
      )
      if (
        result.data.id === activeBundle.id
        || workbenchRootBundleId(result.data) !== activeItem.bundleId
        || workbenchBundleNeedsRebase(result.data, workspace)
      ) {
        setError('The server did not return a derived bundle pinned to the exact current workspace revision.')
        return null
      }
      const nextQueue = upsertWorkbenchBundle(queue, result.data)
      storeWorkbenchQueue(nextQueue)
      activateWorkbenchItem(nextQueue.find((item) => item.bundleId === activeItem.bundleId) ?? null)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to rebase the active page bundle onto the exact current workspace revision.')
      return null
    } finally {
      setBusy(false)
    }
  }, [activateWorkbenchItem, can, client, fail, storeWorkbenchQueue])

  const generateImplementation = useCallback(async (
    instruction: string,
    model = 'gpt-5',
    expectedBundleId?: string,
  ) => {
    const activeBundle = bundleRef.current
    if (!activeBundle || !can('edit')) return null
    const queue = workbenchQueueRef.current
    const activeItem = queue.find((item) => item.bundleId === selectedBundleIdRef.current)
      ?? queue.find((item) => item.bundle?.id === activeBundle.id)
    if (!activeItem) {
      setError('The active Workbench manifest is not attached to the frozen root bundle order.')
      return null
    }
    const activeItemIndex = queue.findIndex((item) => item.bundleId === activeItem.bundleId)
    if (!workbenchQueueItemHasAppliedPredecessors(queue, activeItemIndex)) {
      const predecessor = queue.slice(0, Math.max(activeItemIndex, 0)).find(
        (item) => !proposalIsApplied(item.proposal),
      )
      setError(predecessor
        ? `Apply ${predecessor.sliceId ?? predecessor.bundleId} before generating ${activeItem.sliceId ?? activeItem.bundleId}. Workbench follows frozen manifest order.`
        : 'Select a valid Workbench queue item before generating a proposal.')
      return null
    }
    if (
      expectedBundleId
      && activeItem.bundleId !== expectedBundleId
      && activeBundle.id !== expectedBundleId
    ) {
      setError('The exact frozen Workbench bundle is not active.')
      return null
    }
    const workspace = workspaceRevisionRef.current
    if (activeBundle.currentWorkspaceRevision && !workspace) {
      setError('Load the exact workspace revision pinned by this Workbench bundle before generating.')
      return null
    }
    if (workbenchBundleNeedsRebase(activeBundle, workspace)) {
      setError('Rebase the active page bundle onto the exact current workspace revision before generating a new proposal.')
      return null
    }
    const replaceProposal = activeItem.proposal
    if (
      replaceProposal
      && (
        replaceProposal.status !== 'open'
        || replaceProposal.operations.some((operation) => operation.decision !== 'pending')
        || replaceProposal.executionSource === 'conversation_command'
      )
    ) {
      setError('This proposal already has review state or belongs to an accepted conversation command. Finish or reject it instead of regenerating.')
      return null
    }
    setBusy(true)
    setError(null)
    try {
      const result = await client.generateImplementation(
        activeBundle.id,
        model,
        instruction.trim(),
        replaceProposal ? { id: replaceProposal.id, version: replaceProposal.version } : undefined,
      )
      const nextQueue = replaceWorkbenchQueueProposal(
        workbenchQueueRef.current,
        result.data.proposal,
        activeBundle,
      )
      storeWorkbenchQueue(nextQueue)
      const itemIndex = workbenchQueueItemIndexForProposal(nextQueue, result.data.proposal)
      activateWorkbenchItem(itemIndex >= 0 ? nextQueue[itemIndex] : null)
      return result.data.proposal
    } catch (cause) {
      fail(cause, 'AI could not produce a proposal from the frozen build manifest.')
      return null
    } finally {
      setBusy(false)
    }
  }, [activateWorkbenchItem, can, client, fail, storeWorkbenchQueue])

  const decideOperation = useCallback(async (
    operation: FileOperationDto,
    decision: 'accepted' | 'rejected',
    reason = '',
  ) => {
    if (!proposal || !can('edit') || operation.decision !== 'pending') return null
    if (workbenchBundleNeedsRebase(bundle, workspaceRevisionRef.current)) {
      setError('Rebase and generate a new exact-workspace proposal before recording decisions.')
      return null
    }
    setBusy(true)
    setError(null)
    try {
      const result = await client.decideImplementationOperation(
        proposal,
        operation.id,
        decision,
        reason || (decision === 'rejected' ? 'Rejected in Workbench review' : ''),
      )
      const nextQueue = replaceWorkbenchQueueProposal(workbenchQueueRef.current, result.data, bundle)
      storeWorkbenchQueue(nextQueue)
      const itemIndex = workbenchQueueItemIndexForProposal(nextQueue, result.data)
      activateWorkbenchItem(itemIndex >= 0 ? nextQueue[itemIndex] : null)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to record the file operation decision.')
      return null
    } finally {
      setBusy(false)
    }
  }, [activateWorkbenchItem, bundle, can, client, fail, proposal, storeWorkbenchQueue])

  const decideAllPending = useCallback(async (
    decision: 'accepted' | 'rejected',
    reason = '',
  ) => {
    if (!proposal || !can('edit')) return false
    if (workbenchBundleNeedsRebase(bundle, workspaceRevisionRef.current)) {
      setError('Rebase and generate a new exact-workspace proposal before recording decisions.')
      return false
    }
    setBusy(true)
    setError(null)
    try {
      let current = proposal
      for (const operation of proposal.operations) {
        if (operation.decision !== 'pending') continue
        const result = await client.decideImplementationOperation(
          current,
          operation.id,
          decision,
          reason || (decision === 'rejected' ? 'Rejected in Workbench review' : ''),
        )
        current = result.data
      }
      const nextQueue = replaceWorkbenchQueueProposal(workbenchQueueRef.current, current, bundle)
      storeWorkbenchQueue(nextQueue)
      const itemIndex = workbenchQueueItemIndexForProposal(nextQueue, current)
      activateWorkbenchItem(itemIndex >= 0 ? nextQueue[itemIndex] : null)
      return true
    } catch (cause) {
      fail(cause, 'Unable to record all file operation decisions.')
      return false
    } finally {
      setBusy(false)
    }
  }, [activateWorkbenchItem, bundle, can, client, fail, proposal, storeWorkbenchQueue])

  const applyProposal = useCallback(async () => {
    if (!proposal || !can('edit')) return null
    if (!proposalIsApplied(proposal) && workbenchBundleNeedsRebase(bundle, workspaceRevisionRef.current)) {
      setError('This proposal is bound to an older Workbench manifest. Rebase the active page bundle and generate a new proposal; prior decisions will not be migrated.')
      return null
    }
    const queue = workbenchQueueRef.current.length > 0
      ? workbenchQueueRef.current
      : replaceWorkbenchQueueProposal([], proposal, bundle)
    const workbenchNode = run?.nodes.find(
      (node) => node.key === selectedWorkbenchNodeKeyRef.current
        && node.type === 'workbench_build'
        && node.status === 'waiting_input',
    )
    const alreadyAppliedProposalIds = appliedWorkbenchProposalIds(queue)
    if (
      alreadyAppliedProposalIds
      && workspaceRevision
      && projectId
      && run
      && workbenchNode
    ) {
      setBusy(true)
      setError(null)
      try {
        await client.completeWorkbenchNode(
          projectId,
          run.id,
          workbenchNode.key,
          alreadyAppliedProposalIds,
          revisionRef(workspaceRevision),
        )
        await loadRun(run.id)
        return workspaceRevision
      } catch (cause) {
        fail(cause, 'All page proposals are applied, but Workbench completion could not be recorded.')
        return null
      } finally {
        setBusy(false)
      }
    }
    if (proposal.status !== 'ready') return null
    const queueIndex = workbenchQueueItemIndexForProposal(queue, proposal)
    if (!canApplyWorkbenchQueueItem(queue, queueIndex)) {
      const previous = queue.slice(0, Math.max(queueIndex, 0)).find(
        (item) => !proposalIsApplied(item.proposal),
      )
      setError(previous
        ? `Apply ${previous.sliceId ?? previous.bundleId} first. Workbench proposals are rebased in frozen manifest order.`
        : 'This proposal is not ready to apply in the frozen manifest order.')
      return null
    }
    setBusy(true)
    setError(null)
    try {
      const result = await client.applyImplementationProposal(proposal)
      setWorkspaceRevision(result.data)
      workspaceRevisionRef.current = result.data
      setQueryReference('workspaceRevisionId', result.data.id)
      const updated = await client.getImplementationProposal(proposal.id)
      const nextQueue = replaceWorkbenchQueueProposal(queue, updated.data, bundle)
      storeWorkbenchQueue(nextQueue)
      const updatedItemIndex = workbenchQueueItemIndexForProposal(nextQueue, updated.data)
      activateWorkbenchItem(updatedItemIndex >= 0 ? nextQueue[updatedItemIndex] : null)
      const completedProposalIds = appliedWorkbenchProposalIds(nextQueue)
      if (completedProposalIds && projectId && run && workbenchNode) {
        await client.completeWorkbenchNode(
          projectId,
          run.id,
          workbenchNode.key,
          completedProposalIds,
          revisionRef(result.data),
        )
        await loadRun(run.id)
      } else {
        const pendingIndex = nextPendingWorkbenchQueueIndex(nextQueue)
        const active = pendingIndex >= 0
          ? nextQueue[pendingIndex]
          : updatedItemIndex >= 0 ? nextQueue[updatedItemIndex] : null
        activateWorkbenchItem(active)
      }
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to apply the reviewed implementation proposal.')
      return null
    } finally {
      setBusy(false)
    }
  }, [activateWorkbenchItem, bundle, can, client, fail, loadRun, projectId, proposal, run, storeWorkbenchQueue, workspaceRevision])

  const proposeFileChange = useCallback(async (
    path: string,
    content: string,
    language?: string,
    expectedHash?: string,
  ) => {
    if (!projectId || !bundle || !can('edit')) return null
    const queue = workbenchQueueRef.current
    const queueIndex = queue.findIndex((item) =>
      item.bundleId === selectedBundleIdRef.current || item.bundle?.id === bundle.id,
    )
    if (!workbenchQueueItemHasAppliedPredecessors(queue, queueIndex)) {
      const current = queue[queueIndex]
      const predecessor = queue.slice(0, Math.max(queueIndex, 0)).find(
        (item) => !proposalIsApplied(item.proposal),
      )
      setError(predecessor
        ? `Apply ${predecessor.sliceId ?? predecessor.bundleId} before proposing a file change for ${current?.sliceId ?? current?.bundleId ?? bundle.id}. Workbench follows frozen manifest order.`
        : 'Select a valid Workbench queue item before proposing a file change.')
      return null
    }
    if (bundle.currentWorkspaceRevision && !workspaceRevisionRef.current) {
      setError('Load the exact workspace revision pinned by this Workbench bundle before proposing a file change.')
      return null
    }
    if (workbenchBundleNeedsRebase(bundle, workspaceRevisionRef.current)) {
      setError('Rebase the active page bundle onto the exact current workspace revision before proposing a file change.')
      return null
    }
    const operation: CreateImplementationProposalInputDto['operations'][number] = {
      id: randomId('file-operation'),
      kind: 'file.upsert',
      path,
      content,
      language,
      expectedHash,
      rationale: 'Manual Workbench edit',
      dependsOn: [],
      traceSource: [bundle.prototypeRevision.revisionId],
    }
    setBusy(true)
    setError(null)
    try {
      const result = await client.createImplementationProposal(projectId, {
        buildManifestId: bundle.id,
        operations: [operation],
        assumptions: ['Manual edit proposed from the reviewed Workbench file editor.'],
      })
      const nextQueue = replaceWorkbenchQueueProposal(workbenchQueueRef.current, result.data, bundle)
      storeWorkbenchQueue(nextQueue)
      const itemIndex = workbenchQueueItemIndexForProposal(nextQueue, result.data)
      activateWorkbenchItem(itemIndex >= 0 ? nextQueue[itemIndex] : null)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to create a reviewable file change proposal.')
      return null
    } finally {
      setBusy(false)
    }
  }, [activateWorkbenchItem, bundle, can, client, fail, projectId, storeWorkbenchQueue])

  const selectedDefinition = definitions.find((item) => item.id === selectedDefinitionId)
    ?? definitionVersions[0]
    ?? null
  const selectedQueueIndex = workbenchQueue.findIndex(
    (item) => item.bundleId === selectedBundleId,
  )
  const workbenchProgress = useMemo(() => ({
    applied: workbenchQueue.filter((item) => proposalIsApplied(item.proposal)).length,
    total: workbenchQueue.length,
  }), [workbenchQueue])
  const requiresWorkbenchRebase = !proposalIsApplied(proposal)
    && workbenchBundleNeedsRebase(bundle, workspaceRevision)
  const canApplyProposal = !requiresWorkbenchRebase
    && canApplyWorkbenchQueueItem(workbenchQueue, selectedQueueIndex)
  const canCompleteWorkbench = Boolean(
    appliedWorkbenchProposalIds(workbenchQueue)
    && workspaceRevision
    && run?.nodes.some(
      (node) => node.key === selectedWorkbenchNodeKey
        && node.type === 'workbench_build'
        && node.status === 'waiting_input',
    ),
  )

  const value = useMemo<PlatformFlowContextState>(() => ({
    status: backendStatus === 'error' ? 'error' : status,
    busy,
    error: backendStatus === 'error' ? error ?? 'The Go platform service is unavailable.' : error,
    definitions,
    capabilities,
    definitionVersions,
    selectedDefinition,
    runDefinition,
    manifest,
    runs,
    run,
    events,
    workbenchQueue,
    workbenchGroups,
    selectedWorkbenchNodeKey,
    selectedBundleId,
    workbenchProgress,
    canApplyProposal,
    canCompleteWorkbench,
    requiresWorkbenchRebase,
    bundle,
    proposal,
    workspaceRevision,
    selectDefinition: loadDefinitionVersions,
    refresh,
    createDefinition,
    createDefinitionVersion,
    publishDefinitionVersion,
    startFromProjectBrief,
    compileBlueprintSelection,
    startFromManifest,
    loadRun,
    submitNodeRevision,
    authorizeExecution,
    resolveReview,
    retryNode,
    cancelRun,
    createBundle,
    selectWorkbenchGroup,
    selectWorkbenchBundle,
    loadBundle,
    rebaseWorkbenchBundle,
    generateImplementation,
    loadProposal,
    decideOperation,
    decideAllPending,
    applyProposal,
    proposeFileChange,
    clearError: () => setError(null),
  }), [
    applyProposal,
    authorizeExecution,
    backendStatus,
    bundle,
    busy,
    canApplyProposal,
    canCompleteWorkbench,
    cancelRun,
    compileBlueprintSelection,
    createBundle,
    createDefinition,
    createDefinitionVersion,
    decideAllPending,
    decideOperation,
    definitions,
    definitionVersions,
    error,
    events,
    generateImplementation,
    loadBundle,
    loadDefinitionVersions,
    loadProposal,
    loadRun,
    manifest,
    proposal,
    proposeFileChange,
    publishDefinitionVersion,
    refresh,
    rebaseWorkbenchBundle,
    requiresWorkbenchRebase,
    resolveReview,
    retryNode,
    run,
    runDefinition,
    runs,
    selectWorkbenchGroup,
    selectWorkbenchBundle,
    selectedBundleId,
    selectedWorkbenchNodeKey,
    selectedDefinition,
    startFromProjectBrief,
    startFromManifest,
    status,
    submitNodeRevision,
    workbenchProgress,
    workbenchGroups,
    workbenchQueue,
    workspaceRevision,
  ])

  return <PlatformFlowContext.Provider value={value}>{children}</PlatformFlowContext.Provider>
}

function workbenchBundleMatchesExpectation(
  bundle: WorkbenchBundleDto,
  expectation?: WorkbenchBundleExpectation,
) {
  if (
    bundle.deliverySliceId
    && bundle.workflowContext?.deliverySliceId
    && bundle.deliverySliceId !== bundle.workflowContext.deliverySliceId
  ) return false
  if (!expectation) return true
  return bundle.workflowRunId === expectation.runId
    && workbenchRootBundleId(bundle) === expectation.rootBundleId
    && (!expectation.deliverySliceId || bundle.deliverySliceId === expectation.deliverySliceId)
    && (!expectation.deliverySliceId
      || bundle.workflowContext?.deliverySliceId === expectation.deliverySliceId)
    && (!expectation.manifestGroupKey || bundle.manifestGroupKey === expectation.manifestGroupKey)
}

function exactArtifactRefEqual(
  left: ExactArtifactRefDto | undefined,
  right: ExactArtifactRefDto | undefined,
) {
  if (!left || !right) return left === right
  return left.artifactId === right.artifactId
    && left.revisionId === right.revisionId
    && left.contentHash === right.contentHash
}

function workspaceRevisionMatchesRef(
  workspace: WorkspaceRevisionDto | null | undefined,
  reference: ExactArtifactRefDto,
) {
  return Boolean(
    workspace
    && workspace.artifactId === reference.artifactId
    && workspace.id === reference.revisionId
    && workspace.contentHash === reference.contentHash,
  )
}

export function usePlatformFlow() {
  const value = useContext(PlatformFlowContext)
  if (!value) throw new Error('usePlatformFlow must be used within PlatformFlowProvider')
  return value
}

export function revisionRef(revision: Pick<
  ArtifactRevisionDto<unknown>,
  'artifactId' | 'id' | 'revisionNumber' | 'contentHash'
>): ExactArtifactRefDto {
  return {
    artifactId: revision.artifactId,
    revisionId: revision.id,
    revisionNumber: revision.revisionNumber,
    contentHash: revision.contentHash,
  }
}

async function loadRunEventPages(
  client: PlatformFlowClient,
  projectId: string,
  runId: string,
  initialAfter: number,
  targetCursor: number,
) {
  const events: WorkflowEventDto[] = []
  let after = initialAfter
  while (after < targetCursor) {
    const result = await client.listRunEvents(projectId, runId, after, { limit: 500 })
    const page = [...result.data.items].sort((left, right) => left.sequence - right.sequence)
    if (page.length === 0) break
    events.push(...page)
    const nextAfter = page[page.length - 1].sequence
    if (nextAfter <= after) break
    after = nextAfter
    if (page.length < 500 && after >= targetCursor) break
  }
  return events
}

function mergeWorkflowEvents(
  current: readonly WorkflowEventDto[],
  incoming: readonly WorkflowEventDto[],
) {
  const bySequence = new Map<number, WorkflowEventDto>()
  for (const event of [...current, ...incoming]) bySequence.set(event.sequence, event)
  return [...bySequence.values()].sort((left, right) => left.sequence - right.sequence)
}

function terminalRun(status: WorkflowRunDto['status']) {
  return ['completed', 'failed', 'cancelled', 'stale'].includes(status)
}

function queryReferences() {
  if (typeof window === 'undefined') return {}
  const query = new URLSearchParams(window.location.search)
  return {
    runId: query.get('runId') ?? undefined,
    bundleId: query.get('bundleId') ?? undefined,
    proposalId: query.get('proposalId') ?? undefined,
    workspaceRevisionId: query.get('workspaceRevisionId') ?? undefined,
    workbenchNodeKey: query.get('workbenchNodeKey') ?? undefined,
  }
}

function setQueryReference(key: string, value?: string) {
  if (typeof window === 'undefined') return
  const url = new URL(window.location.href)
  if (value) url.searchParams.set(key, value)
  else url.searchParams.delete(key)
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}

function storedWorkbenchNodeKey(runId: string) {
  if (typeof window === 'undefined') return undefined
  return window.sessionStorage.getItem(`worksflow:workbench-group:${runId}`) ?? undefined
}

function storeWorkbenchNodeKey(runId: string, nodeKey?: string) {
  if (typeof window === 'undefined') return
  const key = `worksflow:workbench-group:${runId}`
  if (nodeKey) window.sessionStorage.setItem(key, nodeKey)
  else window.sessionStorage.removeItem(key)
}

function randomId(prefix: string) {
  const id = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${id}`
}
