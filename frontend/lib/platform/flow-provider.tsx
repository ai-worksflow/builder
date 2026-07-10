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
import { projectBriefWorkflowManifestInput } from './workflow-entry'
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
  workflowWorkbenchQueueReferences,
  type WorkbenchQueueItem,
} from './flow-queue'
import type {
  CreateImplementationProposalInputDto,
  CreateWorkflowDefinitionInputDto,
  CreateWorkflowDefinitionVersionInputDto,
  ExactArtifactRefDto,
  FileOperationDto,
  ImplementationProposalDto,
  InputManifestDto,
  WorkflowDefinitionRecordDto,
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

interface PlatformFlowContextState {
  readonly status: PlatformFlowStatus
  readonly busy: boolean
  readonly error: string | null
  readonly definitions: readonly WorkflowDefinitionRecordDto[]
  readonly definitionVersions: readonly WorkflowDefinitionRecordDto[]
  readonly selectedDefinition: WorkflowDefinitionRecordDto | null
  readonly manifest: InputManifestDto | null
  readonly runs: readonly WorkflowRunSummaryDto[]
  readonly run: WorkflowRunDto | null
  readonly events: readonly WorkflowEventDto[]
  readonly workbenchQueue: readonly WorkbenchQueueItem[]
  readonly selectedBundleId: string | null
  readonly workbenchProgress: { readonly applied: number; readonly total: number }
  readonly canApplyProposal: boolean
  readonly canCompleteWorkbench: boolean
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
  ) => Promise<boolean>
  readonly retryNode: (node: WorkflowNodeRunDto, reason?: string) => Promise<boolean>
  readonly cancelRun: (reason?: string) => Promise<boolean>
  readonly createBundle: (
    prototype: VersionedArtifactDto<PrototypeContentDto>,
  ) => Promise<WorkbenchBundleDto | null>
  readonly selectWorkbenchBundle: (bundleId: string) => void
  readonly loadBundle: (bundleId: string) => Promise<WorkbenchBundleDto | null>
  readonly generateImplementation: (
    instruction: string,
    model?: string,
    expectedBundleId?: string,
  ) => Promise<ImplementationProposalDto | null>
  readonly loadProposal: (proposalId: string) => Promise<ImplementationProposalDto | null>
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
  const [definitionVersions, setDefinitionVersions] = useState<WorkflowDefinitionRecordDto[]>([])
  const [runDefinition, setRunDefinition] = useState<WorkflowDefinitionRecordDto | null>(null)
  const [selectedDefinitionId, setSelectedDefinitionId] = useState<string | null>(null)
  const [manifest, setManifest] = useState<InputManifestDto | null>(null)
  const [runs, setRuns] = useState<WorkflowRunSummaryDto[]>([])
  const [run, setRun] = useState<WorkflowRunDto | null>(null)
  const [events, setEvents] = useState<WorkflowEventDto[]>([])
  const [workbenchQueue, setWorkbenchQueue] = useState<readonly WorkbenchQueueItem[]>([])
  const [selectedBundleId, setSelectedBundleId] = useState<string | null>(null)
  const [bundle, setBundle] = useState<WorkbenchBundleDto | null>(null)
  const [proposal, setProposal] = useState<ImplementationProposalDto | null>(null)
  const [workspaceRevision, setWorkspaceRevision] = useState<WorkspaceRevisionDto | null>(null)
  const projectId = project?.id ?? null
  const requestCounter = useRef(0)
  const runRequestCounter = useRef(0)
  const runRef = useRef(run)
  const eventsRef = useRef(events)
  const selectedDefinitionIdRef = useRef(selectedDefinitionId)
  const workbenchQueueRef = useRef(workbenchQueue)
  const selectedBundleIdRef = useRef(selectedBundleId)
  const bundleRef = useRef(bundle)
  const proposalRef = useRef(proposal)
  const workspaceRevisionRef = useRef(workspaceRevision)
  runRef.current = run
  eventsRef.current = events
  selectedDefinitionIdRef.current = selectedDefinitionId
  workbenchQueueRef.current = workbenchQueue
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

  const activateWorkbenchItem = useCallback((item: WorkbenchQueueItem | null) => {
    const bundleValue = item?.bundle ?? null
    const proposalValue = item?.proposal ?? null
    selectedBundleIdRef.current = item?.bundleId ?? null
    bundleRef.current = bundleValue
    proposalRef.current = proposalValue
    setSelectedBundleId(item?.bundleId ?? null)
    setBundle(bundleValue)
    setProposal(proposalValue)
    setQueryReference('bundleId', item?.bundleId)
    setQueryReference('proposalId', proposalValue?.id)
  }, [])

  const selectWorkbenchBundle = useCallback((bundleId: string) => {
    const item = workbenchQueueRef.current.find((candidate) => candidate.bundleId === bundleId)
    if (item) activateWorkbenchItem(item)
  }, [activateWorkbenchItem])

  const loadDefinitionVersions = useCallback(async (definitionId: string) => {
    if (!projectId) return
    try {
      const result = await client.listDefinitionVersions(projectId, definitionId, { limit: 200 })
      setDefinitionVersions([...result.data.items].sort((left, right) => right.version - left.version))
      setSelectedDefinitionId(definitionId)
    } catch (cause) {
      fail(cause, 'Unable to load workflow versions.')
    }
  }, [client, fail, projectId])

  const refresh = useCallback(async () => {
    if (!session.signedIn || !projectId) {
      setStatus('idle')
      setDefinitions([])
      setDefinitionVersions([])
      setSelectedDefinitionId(null)
      setRuns([])
      return
    }
    const requestId = ++requestCounter.current
    setStatus('loading')
    setError(null)
    try {
      const [result, runResult] = await Promise.all([
        client.listDefinitions(projectId, { limit: 200 }),
        client.listRuns(projectId, {}, { limit: 100 }),
      ])
      if (requestId !== requestCounter.current) return
      const items = [...result.data.items].sort((left, right) =>
        left.title.localeCompare(right.title) || right.version - left.version,
      )
      setDefinitions(items)
      setRuns([...runResult.data.items].sort((left, right) => right.updatedAt.localeCompare(left.updatedAt)))
      const selected = items.find((item) => item.id === selectedDefinitionIdRef.current)
        ?? items.find((item) => item.published)
        ?? items[0]
      if (selected) {
        setSelectedDefinitionId(selected.id)
        const versions = await client.listDefinitionVersions(projectId, selected.id, { limit: 200 })
        if (requestId !== requestCounter.current) return
        setDefinitionVersions([...versions.data.items].sort((left, right) => right.version - left.version))
      } else {
        setDefinitionVersions([])
      }
      setStatus('ready')
    } catch (cause) {
      if (requestId === requestCounter.current) fail(cause, 'Workflow service is unavailable.')
    }
  }, [client, fail, projectId, session.signedIn])

  const loadBundle = useCallback(async (bundleId: string) => {
    try {
      const result = await client.getWorkbenchBundle(bundleId)
      const nextQueue = upsertWorkbenchBundle(workbenchQueueRef.current, result.data)
      storeWorkbenchQueue(nextQueue)
      activateWorkbenchItem(nextQueue.find((item) => item.bundleId === result.data.id) ?? null)
      if (result.data.currentWorkspaceRevision) {
        const workspace = await client.getWorkspaceRevision(
          result.data.currentWorkspaceRevision.revisionId,
        )
        setWorkspaceRevision(workspace.data)
        setQueryReference('workspaceRevisionId', workspace.data.id)
      }
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to load the frozen build manifest.')
      return null
    }
  }, [activateWorkbenchItem, client, fail, storeWorkbenchQueue])

  const loadProposal = useCallback(async (proposalId: string) => {
    try {
      const result = await client.getImplementationProposal(proposalId)
      let knownBundle = workbenchQueueRef.current.find(
        (item) => item.bundleId === result.data.buildManifestId,
      )?.bundle ?? null
      if (!knownBundle) {
        const bundleResult = await client.getWorkbenchBundle(result.data.buildManifestId)
        knownBundle = bundleResult.data
      }
      const withBundle = knownBundle
        ? upsertWorkbenchBundle(workbenchQueueRef.current, knownBundle)
        : workbenchQueueRef.current
      const nextQueue = replaceWorkbenchQueueProposal(withBundle, result.data, knownBundle)
      storeWorkbenchQueue(nextQueue)
      activateWorkbenchItem(nextQueue.find(
        (item) => item.bundleId === result.data.buildManifestId,
      ) ?? null)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to load the implementation proposal.')
      return null
    }
  }, [activateWorkbenchItem, client, fail, storeWorkbenchQueue])

  const hydrateRunOutputs = useCallback(async (nextRun: WorkflowRunDto) => {
    const references = workflowWorkbenchQueueReferences(nextRun)
    if (references.length > 0) {
      const proposalIds = references.flatMap(
        (reference) => reference.proposalId ? [reference.proposalId] : [],
      )
      const [bundleResults, proposalResults] = await Promise.all([
        Promise.all(references.map((reference) => client.getWorkbenchBundle(reference.bundleId))),
        Promise.all(proposalIds.map((proposalId) => client.getImplementationProposal(proposalId))),
      ])
      const nextQueue = hydrateWorkbenchQueue(
        references,
        bundleResults.map((result) => result.data),
        proposalResults.map((result) => result.data),
        workbenchQueueRef.current,
      )
      storeWorkbenchQueue(nextQueue)
      const retained = nextQueue.find((item) => item.bundleId === selectedBundleIdRef.current)
      const pendingIndex = nextPendingWorkbenchQueueIndex(nextQueue)
      const active = retained ?? nextQueue[pendingIndex >= 0 ? pendingIndex : 0] ?? null
      activateWorkbenchItem(active)
    } else {
      storeWorkbenchQueue([])
      activateWorkbenchItem(null)
    }

    const revisionId = nextRun.nodes.find((node) => node.type === 'workbench_build')?.outputRevisionId
    if (revisionId && workspaceRevisionRef.current?.id !== revisionId) {
      try {
        const result = await client.getWorkspaceRevision(revisionId)
        setWorkspaceRevision(result.data)
        setQueryReference('workspaceRevisionId', result.data.id)
      } catch (cause) {
        fail(cause, 'Unable to load the applied application workspace.')
      }
    }
  }, [activateWorkbenchItem, client, fail, storeWorkbenchQueue])

  const loadRun = useCallback(async (runId: string) => {
    if (!projectId) return null
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
      setSelectedDefinitionId(exactDefinition.id)
      setDefinitionVersions([...versionResult.data.items].sort(
        (left, right) => right.version - left.version,
      ))
      setQueryReference('runId', runResult.data.id)
      setStatus('ready')
      await hydrateRunOutputs(runResult.data)
      return runResult.data
    } catch (cause) {
      if (requestId === runRequestCounter.current) fail(cause, 'Unable to load the workflow run.')
      return null
    }
  }, [client, fail, hydrateRunOutputs, projectId])

  useEffect(() => {
    const initialReferences = queryReferences()
    requestCounter.current += 1
    runRequestCounter.current += 1
    setManifest(null)
    setRun(null)
    runRef.current = null
    setRunDefinition(null)
    setRuns([])
    setEvents([])
    eventsRef.current = []
    storeWorkbenchQueue([])
    activateWorkbenchItem(null)
    setBundle(null)
    setProposal(null)
    setWorkspaceRevision(null)
    if (!session.signedIn || !projectId) {
      setStatus('idle')
      return
    }
    void refresh().then(() => {
      const references = initialReferences
      if (references.runId) void loadRun(references.runId)
      else {
        if (references.bundleId) void loadBundle(references.bundleId)
        if (references.proposalId) void loadProposal(references.proposalId)
        if (references.workspaceRevisionId) {
          void client.getWorkspaceRevision(references.workspaceRevisionId)
            .then((result) => setWorkspaceRevision(result.data))
            .catch((cause) => fail(cause, 'Unable to load the application workspace revision.'))
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
    setBusy(true)
    setError(null)
    try {
      const result = await client.createDefinition(projectId, input)
      await refresh()
      await loadDefinitionVersions(result.data.id)
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
    setBusy(true)
    setError(null)
    try {
      const result = await client.createDefinitionVersion(projectId, definitionId, input)
      await refresh()
      await loadDefinitionVersions(definitionId)
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
    setBusy(true)
    setError(null)
    try {
      const result = await client.publishDefinitionVersion(projectId, definitionId, versionId)
      await refresh()
      await loadDefinitionVersions(definitionId)
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
      if (!sourceRevision && !requireApproved && projectBrief.draft) {
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
  ) => {
    if (!projectId || !run) return false
    setBusy(true)
    setError(null)
    try {
      await client.resolveReview(projectId, run.id, node.key, resolution, reason)
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
        workflowRunId: run?.id,
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
  }, [activateWorkbenchItem, can, client, fail, projectId, run?.id, storeWorkbenchQueue])

  const generateImplementation = useCallback(async (
    instruction: string,
    model = 'gpt-5',
    expectedBundleId?: string,
  ) => {
    const activeBundle = bundleRef.current
    if (!activeBundle || !can('edit')) return null
    if (expectedBundleId && activeBundle.id !== expectedBundleId) {
      setError('The exact frozen Workbench bundle is not active.')
      return null
    }
    setBusy(true)
    setError(null)
    try {
      const result = await client.generateImplementation(activeBundle.id, model, instruction.trim())
      const nextQueue = replaceWorkbenchQueueProposal(
        workbenchQueueRef.current,
        result.data.proposal,
        activeBundle,
      )
      storeWorkbenchQueue(nextQueue)
      activateWorkbenchItem(nextQueue.find((item) => item.bundleId === activeBundle.id) ?? null)
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
      activateWorkbenchItem(nextQueue.find(
        (item) => item.bundleId === result.data.buildManifestId,
      ) ?? null)
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
      activateWorkbenchItem(nextQueue.find(
        (item) => item.bundleId === current.buildManifestId,
      ) ?? null)
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
    const queue = workbenchQueueRef.current.length > 0
      ? workbenchQueueRef.current
      : replaceWorkbenchQueueProposal([], proposal, bundle)
    const workbenchNode = run?.nodes.find(
      (node) => node.type === 'workbench_build' && node.status === 'waiting_input',
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
    const queueIndex = queue.findIndex((item) => item.bundleId === proposal.buildManifestId)
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
      activateWorkbenchItem(nextQueue.find(
        (item) => item.bundleId === updated.data.buildManifestId,
      ) ?? null)
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
          : nextQueue.find((item) => item.bundleId === updated.data.buildManifestId) ?? null
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
      activateWorkbenchItem(nextQueue.find((item) => item.bundleId === bundle.id) ?? null)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to create a reviewable file change proposal.')
      return null
    } finally {
      setBusy(false)
    }
  }, [activateWorkbenchItem, bundle, can, client, fail, projectId, storeWorkbenchQueue])

  const selectedDefinition = runDefinition
    ?? definitions.find((item) => item.id === selectedDefinitionId)
    ?? definitionVersions[0]
    ?? null
  const selectedQueueIndex = workbenchQueue.findIndex(
    (item) => item.bundleId === selectedBundleId,
  )
  const workbenchProgress = useMemo(() => ({
    applied: workbenchQueue.filter((item) => proposalIsApplied(item.proposal)).length,
    total: workbenchQueue.length,
  }), [workbenchQueue])
  const canApplyProposal = canApplyWorkbenchQueueItem(workbenchQueue, selectedQueueIndex)
  const canCompleteWorkbench = Boolean(
    appliedWorkbenchProposalIds(workbenchQueue)
    && workspaceRevision
    && run?.nodes.some(
      (node) => node.type === 'workbench_build' && node.status === 'waiting_input',
    ),
  )

  const value = useMemo<PlatformFlowContextState>(() => ({
    status: backendStatus === 'error' ? 'error' : status,
    busy,
    error: backendStatus === 'error' ? error ?? 'The Go platform service is unavailable.' : error,
    definitions,
    definitionVersions,
    selectedDefinition,
    manifest,
    runs,
    run,
    events,
    workbenchQueue,
    selectedBundleId,
    workbenchProgress,
    canApplyProposal,
    canCompleteWorkbench,
    bundle,
    proposal,
    workspaceRevision,
    selectDefinition: loadDefinitionVersions,
    refresh,
    createDefinition,
    createDefinitionVersion,
    publishDefinitionVersion,
    startFromProjectBrief,
    loadRun,
    submitNodeRevision,
    authorizeExecution,
    resolveReview,
    retryNode,
    cancelRun,
    createBundle,
    selectWorkbenchBundle,
    loadBundle,
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
    resolveReview,
    retryNode,
    run,
    runs,
    selectWorkbenchBundle,
    selectedBundleId,
    selectedDefinition,
    startFromProjectBrief,
    status,
    submitNodeRevision,
    workbenchProgress,
    workbenchQueue,
    workspaceRevision,
  ])

  return <PlatformFlowContext.Provider value={value}>{children}</PlatformFlowContext.Provider>
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
  }
}

function setQueryReference(key: string, value?: string) {
  if (typeof window === 'undefined') return
  const url = new URL(window.location.href)
  if (value) url.searchParams.set(key, value)
  else url.searchParams.delete(key)
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}

function randomId(prefix: string) {
  const id = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${id}`
}
