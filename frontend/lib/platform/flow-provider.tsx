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
  readonly loadBundle: (bundleId: string) => Promise<WorkbenchBundleDto | null>
  readonly generateImplementation: (
    instruction: string,
    model?: string,
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
  const [selectedDefinitionId, setSelectedDefinitionId] = useState<string | null>(null)
  const [manifest, setManifest] = useState<InputManifestDto | null>(null)
  const [runs, setRuns] = useState<WorkflowRunSummaryDto[]>([])
  const [run, setRun] = useState<WorkflowRunDto | null>(null)
  const [events, setEvents] = useState<WorkflowEventDto[]>([])
  const [bundle, setBundle] = useState<WorkbenchBundleDto | null>(null)
  const [proposal, setProposal] = useState<ImplementationProposalDto | null>(null)
  const [workspaceRevision, setWorkspaceRevision] = useState<WorkspaceRevisionDto | null>(null)
  const projectId = project?.id ?? null
  const requestCounter = useRef(0)
  const runRef = useRef(run)
  runRef.current = run

  const fail = useCallback((cause: unknown, fallback: string) => {
    setError(collaborationErrorMessage(cause, fallback))
    setStatus('error')
  }, [])

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
      const selected = items.find((item) => item.id === selectedDefinitionId)
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
  }, [client, fail, projectId, selectedDefinitionId, session.signedIn])

  const loadBundle = useCallback(async (bundleId: string) => {
    try {
      const result = await client.getWorkbenchBundle(bundleId)
      setBundle(result.data)
      setQueryReference('bundleId', result.data.id)
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
  }, [client, fail])

  const loadProposal = useCallback(async (proposalId: string) => {
    try {
      const result = await client.getImplementationProposal(proposalId)
      setProposal(result.data)
      setQueryReference('proposalId', result.data.id)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to load the implementation proposal.')
      return null
    }
  }, [client, fail])

  const hydrateRunOutputs = useCallback(async (nextRun: WorkflowRunDto) => {
    const buildManifest = objectValue(nextRun.context.values?.buildManifest)
    const bundleIds = stringArray(buildManifest?.bundleIds)
    if (bundleIds[0] && bundle?.id !== bundleIds[0]) await loadBundle(bundleIds[0])

    const proposalIds = workflowImplementationProposalIds(nextRun)
    if (proposalIds[0] && proposal?.id !== proposalIds[0]) await loadProposal(proposalIds[0])

    const revisionId = nextRun.nodes.find((node) => node.type === 'workbench_build')?.outputRevisionId
    if (revisionId && workspaceRevision?.id !== revisionId) {
      try {
        const result = await client.getWorkspaceRevision(revisionId)
        setWorkspaceRevision(result.data)
        setQueryReference('workspaceRevisionId', result.data.id)
      } catch (cause) {
        fail(cause, 'Unable to load the applied application workspace.')
      }
    }
  }, [bundle?.id, client, fail, loadBundle, loadProposal, proposal?.id, workspaceRevision?.id])

  const loadRun = useCallback(async (runId: string) => {
    if (!projectId) return null
    try {
      const [runResult, eventResult] = await Promise.all([
        client.getRun(projectId, runId),
        client.listRunEvents(projectId, runId, 0, { limit: 500 }),
      ])
      setRun(runResult.data)
      setRuns((current) => [
        runResult.data,
        ...current.filter((item) => item.id !== runResult.data.id),
      ].sort((left, right) => right.updatedAt.localeCompare(left.updatedAt)))
      setEvents([...eventResult.data.items])
      setQueryReference('runId', runResult.data.id)
      setStatus('ready')
      await hydrateRunOutputs(runResult.data)
      return runResult.data
    } catch (cause) {
      fail(cause, 'Unable to load the workflow run.')
      return null
    }
  }, [client, fail, hydrateRunOutputs, projectId])

  useEffect(() => {
    requestCounter.current += 1
    setManifest(null)
    setRun(null)
    setRuns([])
    setEvents([])
    setBundle(null)
    setProposal(null)
    setWorkspaceRevision(null)
    if (!session.signedIn || !projectId) {
      setStatus('idle')
      return
    }
    void refresh().then(() => {
      const references = queryReferences()
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
  }, [client, fail, loadBundle, loadProposal, loadRun, projectId, refresh, session.signedIn])

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
    const approved = projectBrief?.approvedRevision
    if (!approved) {
      setError('Approve an immutable Project Brief revision before starting a workflow.')
      return null
    }
    setBusy(true)
    setError(null)
    try {
      const source = revisionRef(approved)
      const manifestResult = await client.createManifest(projectId, {
        jobType: 'workflow_start',
        sources: [{ ref: source, purpose: 'project_brief' }],
        constraints: {
          entryArtifactId: source.artifactId,
          entryRevisionId: source.revisionId,
          entryContentHash: source.contentHash,
        },
        outputSchemaVersion: 'workflow-input/v1',
      })
      setManifest(manifestResult.data)
      const runResult = await client.startRun(projectId, {
        definitionVersionId: options.definitionVersionId,
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
  }, [artifacts.documents, can, client, fail, loadRun, projectId])

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
        artifactRevision: revision,
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
      setBundle(result.data)
      setProposal(null)
      setWorkspaceRevision(null)
      setQueryReference('bundleId', result.data.id)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to compile the frozen build manifest. Check approved upstream traces.')
      return null
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, projectId, run?.id])

  const generateImplementation = useCallback(async (
    instruction: string,
    model = 'gpt-5',
  ) => {
    if (!bundle || !can('edit')) return null
    setBusy(true)
    setError(null)
    try {
      const result = await client.generateImplementation(bundle.id, model, instruction.trim())
      setProposal(result.data.proposal)
      setQueryReference('proposalId', result.data.proposal.id)
      return result.data.proposal
    } catch (cause) {
      fail(cause, 'AI could not produce a proposal from the frozen build manifest.')
      return null
    } finally {
      setBusy(false)
    }
  }, [bundle, can, client, fail])

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
      setProposal(result.data)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to record the file operation decision.')
      return null
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, proposal])

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
      setProposal(current)
      return true
    } catch (cause) {
      fail(cause, 'Unable to record all file operation decisions.')
      return false
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, proposal])

  const applyProposal = useCallback(async () => {
    if (!proposal || proposal.status !== 'ready' || !can('edit')) return null
    setBusy(true)
    setError(null)
    try {
      const result = await client.applyImplementationProposal(proposal)
      setWorkspaceRevision(result.data)
      setQueryReference('workspaceRevisionId', result.data.id)
      const updated = await client.getImplementationProposal(proposal.id)
      setProposal(updated.data)
      const workbenchNode = run?.nodes.find(
        (node) => node.type === 'workbench_build' && node.status === 'waiting_input',
      )
      if (projectId && run && workbenchNode) {
        await client.completeWorkbenchNode(
          projectId,
          run.id,
          workbenchNode.key,
          [proposal.id],
          revisionRef(result.data),
        )
        await loadRun(run.id)
      }
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to apply the reviewed implementation proposal.')
      return null
    } finally {
      setBusy(false)
    }
  }, [can, client, fail, loadRun, projectId, proposal, run])

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
      setProposal(result.data)
      setQueryReference('proposalId', result.data.id)
      return result.data
    } catch (cause) {
      fail(cause, 'Unable to create a reviewable file change proposal.')
      return null
    } finally {
      setBusy(false)
    }
  }, [bundle, can, client, fail, projectId])

  const selectedDefinition = definitions.find((item) => item.id === selectedDefinitionId)
    ?? definitionVersions[0]
    ?? null

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
    resolveReview,
    retryNode,
    cancelRun,
    createBundle,
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
    backendStatus,
    bundle,
    busy,
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
    selectedDefinition,
    startFromProjectBrief,
    status,
    submitNodeRevision,
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

function workflowImplementationProposalIds(run: WorkflowRunDto) {
  const ids = new Set<string>()
  for (const metadata of Object.values(run.context.nodes)) {
    const output = objectValue(metadata.output)
    for (const proposal of arrayValue(output?.implementationProposals)) {
      const record = objectValue(proposal)
      if (typeof record?.proposalId === 'string') ids.add(record.proposalId)
    }
  }
  return [...ids]
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function arrayValue(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : []
}

function stringArray(value: unknown) {
  return arrayValue(value).filter((item): item is string => typeof item === 'string')
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

function setQueryReference(key: string, value: string) {
  if (typeof window === 'undefined') return
  const url = new URL(window.location.href)
  url.searchParams.set(key, value)
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}

function randomId(prefix: string) {
  const id = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${id}`
}
