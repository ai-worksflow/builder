import type { ClientMutationOptions, ClientRequestOptions, ListOptions } from './clients'
import type {
  CreateImplementationProposalInputDto,
  CreateInputManifestDto,
  BlueprintSelectionCompileInputDto,
  CreateWorkbenchBundleInputDto,
  CreateWorkflowDefinitionInputDto,
  CreateWorkflowDefinitionVersionInputDto,
  ExactArtifactRefDto,
  ImplementationGenerationResultDto,
  ImplementationProposalDto,
  InputManifestDto,
  ManifestRefDto,
  StartWorkflowRunInputDto,
  WorkflowDefinitionRecordDto,
  WorkflowCapabilitiesDto,
  WorkflowEventDto,
  WorkflowPageDto,
  WorkflowRunDto,
  WorkflowRunSummaryDto,
  WorkbenchBundleDto,
  WorkbenchBundleLineageStateDto,
  WorkspaceRevisionDto,
} from './flow-contract'
import { HttpClient } from './http'
import { wireVersionRef } from './wire-version-ref'

function segment(value: string) {
  return encodeURIComponent(value)
}

function requestOptions(options?: ClientRequestOptions) {
  return { signal: options?.signal, requestId: options?.requestId }
}

function mutationOptions(options?: ClientMutationOptions, ifMatch?: string) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
    ifMatch: options?.ifMatch ?? ifMatch,
    idempotencyKey: options?.idempotencyKey ?? true,
  }
}

export class PlatformFlowClient {
  readonly http: HttpClient

  constructor(http: HttpClient) {
    this.http = http
  }

  listDefinitions(projectId: string, options?: ListOptions) {
    return this.http.get<WorkflowPageDto<WorkflowDefinitionRecordDto>>(
      `/v1/projects/${segment(projectId)}/workflow-definitions`,
      requestOptions(options),
    )
  }

  capabilities(projectId: string, options?: ClientRequestOptions) {
    return this.http.get<WorkflowCapabilitiesDto>(
      `/v1/projects/${segment(projectId)}/workflow-capabilities`,
      requestOptions(options),
    )
  }

  listDefinitionVersions(projectId: string, definitionId: string, options?: ListOptions) {
    return this.http.get<WorkflowPageDto<WorkflowDefinitionRecordDto>>(
      `/v1/projects/${segment(projectId)}/workflow-definitions/${segment(definitionId)}/versions`,
      requestOptions(options),
    )
  }

  createDefinition(
    projectId: string,
    input: CreateWorkflowDefinitionInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<WorkflowDefinitionRecordDto, CreateWorkflowDefinitionInputDto>(
      `/v1/projects/${segment(projectId)}/workflow-definitions`,
      input,
      mutationOptions(options),
    )
  }

  createDefinitionVersion(
    projectId: string,
    definitionId: string,
    input: CreateWorkflowDefinitionVersionInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<WorkflowDefinitionRecordDto, CreateWorkflowDefinitionVersionInputDto>(
      `/v1/projects/${segment(projectId)}/workflow-definitions/${segment(definitionId)}/versions`,
      input,
      mutationOptions(options),
    )
  }

  publishDefinitionVersion(
    projectId: string,
    definitionId: string,
    versionId: string,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<WorkflowDefinitionRecordDto>(
      `/v1/projects/${segment(projectId)}/workflow-definitions/${segment(definitionId)}/versions/${segment(versionId)}/publish`,
      undefined,
      mutationOptions(options),
    )
  }

  createManifest(projectId: string, input: CreateInputManifestDto, options?: ClientMutationOptions) {
    return this.http.post<InputManifestDto, CreateInputManifestDto>(
      `/v1/projects/${segment(projectId)}/input-manifests`,
      {
        ...input,
        baseRevision: input.baseRevision ? wireVersionRef(input.baseRevision) : undefined,
        sources: input.sources.map((source) => ({
          ...source,
          ref: wireVersionRef(source.ref),
        })),
      },
      mutationOptions(options),
    )
  }

  compileBlueprintSelection(
    projectId: string,
    input: BlueprintSelectionCompileInputDto,
    blueprintETag: string,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<InputManifestDto, BlueprintSelectionCompileInputDto>(
      `/v1/projects/${segment(projectId)}/blueprint-selections/compile`,
      {
        ...input,
        blueprintRevision: wireVersionRef(input.blueprintRevision),
      },
      mutationOptions({ ...options, ifMatch: blueprintETag }),
    )
  }

  getManifest(manifestId: string, options?: ClientRequestOptions) {
    return this.http.get<InputManifestDto>(
      `/v1/input-manifests/${segment(manifestId)}`,
      requestOptions(options),
    )
  }

  startRun(projectId: string, input: StartWorkflowRunInputDto, options?: ClientMutationOptions) {
    return this.http.post<WorkflowRunDto, StartWorkflowRunInputDto>(
      `/v1/projects/${segment(projectId)}/workflow-runs`,
      input,
      mutationOptions(options),
    )
  }

  listRuns(
    projectId: string,
    filters: { readonly status?: string } = {},
    options?: ListOptions,
  ) {
    return this.http.get<WorkflowPageDto<WorkflowRunSummaryDto>>(
      `/v1/projects/${segment(projectId)}/workflow-runs`,
      {
        ...requestOptions(options),
        query: {
          status: filters.status,
          cursor: options?.cursor,
          limit: options?.limit,
        },
      },
    )
  }

  getRun(projectId: string, runId: string, options?: ClientRequestOptions) {
    return this.http.get<WorkflowRunDto>(
      `/v1/projects/${segment(projectId)}/workflow-runs/${segment(runId)}`,
      requestOptions(options),
    )
  }

  listRunEvents(
    projectId: string,
    runId: string,
    after = 0,
    options?: ListOptions,
  ) {
    return this.http.get<WorkflowPageDto<WorkflowEventDto>>(
      `/v1/projects/${segment(projectId)}/workflow-runs/${segment(runId)}/events`,
      {
        ...requestOptions(options),
        query: { after, limit: options?.limit },
      },
    )
  }

  resumeRun(
    projectId: string,
    runId: string,
    nodeKey: string,
    output: unknown,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<void, { readonly nodeKey: string; readonly output: unknown }>(
      `/v1/projects/${segment(projectId)}/workflow-runs/${segment(runId)}/resume`,
      { nodeKey, output },
      mutationOptions(options),
    )
  }

  authorizeExecution(
    projectId: string,
    runId: string,
    nodeKey: string,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<void, { readonly nodeKey: string }>(
      `/v1/projects/${segment(projectId)}/workflow-runs/${segment(runId)}/execute`,
      { nodeKey },
      mutationOptions(options),
    )
  }

  recordProposal(
    projectId: string,
    runId: string,
    nodeKey: string,
    proposal: { readonly id: string; readonly payloadHash: string },
    options?: ClientMutationOptions,
  ) {
    return this.http.post<void, { readonly nodeKey: string; readonly proposal: typeof proposal }>(
      `/v1/projects/${segment(projectId)}/workflow-runs/${segment(runId)}/proposals`,
      { nodeKey, proposal },
      mutationOptions(options),
    )
  }

  resolveReview(
    projectId: string,
    runId: string,
    nodeKey: string,
    resolution: 'approve' | 'changes_requested' | 'waive',
    reason = '',
    options?: ClientMutationOptions,
  ) {
    return this.http.post<void, {
      readonly nodeKey: string
      readonly resolution: typeof resolution
      readonly reason: string
    }>(
      `/v1/projects/${segment(projectId)}/workflow-runs/${segment(runId)}/approve`,
      { nodeKey, resolution, reason },
      mutationOptions(options),
    )
  }

  cancelRun(projectId: string, runId: string, reason: string, options?: ClientMutationOptions) {
    return this.runReasonAction(projectId, runId, 'cancel', '', reason, options)
  }

  retryNode(
    projectId: string,
    runId: string,
    nodeKey: string,
    reason: string,
    options?: ClientMutationOptions,
  ) {
    return this.runReasonAction(projectId, runId, 'retry', nodeKey, reason, options)
  }

  waiveNode(
    projectId: string,
    runId: string,
    nodeKey: string,
    reason: string,
    options?: ClientMutationOptions,
  ) {
    return this.runReasonAction(projectId, runId, 'waive', nodeKey, reason, options)
  }

  private runReasonAction(
    projectId: string,
    runId: string,
    action: 'cancel' | 'retry' | 'waive',
    nodeKey: string,
    reason: string,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<void, { readonly nodeKey?: string; readonly reason: string }>(
      `/v1/projects/${segment(projectId)}/workflow-runs/${segment(runId)}/${action}`,
      { nodeKey: nodeKey || undefined, reason },
      mutationOptions(options),
    )
  }

  createWorkbenchBundle(
    projectId: string,
    input: CreateWorkbenchBundleInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<WorkbenchBundleDto, CreateWorkbenchBundleInputDto>(
      `/v1/projects/${segment(projectId)}/build-manifests`,
      { ...input, prototypeRevision: wireVersionRef(input.prototypeRevision) },
      mutationOptions(options),
    )
  }

  getWorkbenchBundle(bundleId: string, options?: ClientRequestOptions) {
    return this.http.get<WorkbenchBundleDto>(
      `/v1/build-manifests/${segment(bundleId)}`,
      requestOptions(options),
    )
  }

  getWorkbenchBundleLineageState(rootBundleId: string, options?: ClientRequestOptions) {
    return this.http.get<WorkbenchBundleLineageStateDto>(
      `/v1/build-manifests/${segment(rootBundleId)}/lineage-state`,
      requestOptions(options),
    )
  }

  rebaseWorkbenchBundle(
    bundleId: string,
    workspaceRevision: ExactArtifactRefDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<WorkbenchBundleDto, {
      readonly workspaceRevision: ExactArtifactRefDto
    }>(
      `/v1/build-manifests/${segment(bundleId)}/rebase`,
      { workspaceRevision: wireVersionRef(workspaceRevision) },
      mutationOptions(options),
    )
  }

  generateImplementation(
    bundleId: string,
    model: string,
    instruction: string,
    replaceProposal?: Pick<ImplementationProposalDto, 'id' | 'version'>,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ImplementationGenerationResultDto, {
      readonly model: string
      readonly instruction: string
      readonly replaceProposalId?: string
      readonly replaceProposalVersion?: number
    }>(
      `/v1/build-manifests/${segment(bundleId)}/generate`,
      {
        model,
        instruction,
        ...(replaceProposal ? {
          replaceProposalId: replaceProposal.id,
          replaceProposalVersion: replaceProposal.version,
        } : {}),
      },
      mutationOptions(options),
    )
  }

  createImplementationProposal(
    projectId: string,
    input: CreateImplementationProposalInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ImplementationProposalDto, CreateImplementationProposalInputDto>(
      `/v1/projects/${segment(projectId)}/implementation-proposals`,
      input,
      mutationOptions(options),
    )
  }

  getImplementationProposal(proposalId: string, options?: ClientRequestOptions) {
    return this.http.get<ImplementationProposalDto>(
      `/v1/implementation-proposals/${segment(proposalId)}`,
      requestOptions(options),
    )
  }

  decideImplementationOperation(
    proposal: Pick<ImplementationProposalDto, 'id' | 'version'>,
    operationId: string,
    decision: 'accepted' | 'rejected',
    reason = '',
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ImplementationProposalDto, {
      readonly operationId: string
      readonly decision: typeof decision
      readonly reason: string
      readonly version: number
    }>(
      `/v1/implementation-proposals/${segment(proposal.id)}/decisions`,
      { operationId, decision, reason, version: proposal.version },
      mutationOptions(options, `"implementation-proposal:${proposal.id}:${proposal.version}"`),
    )
  }

  applyImplementationProposal(
    proposal: Pick<ImplementationProposalDto, 'id' | 'version'>,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<WorkspaceRevisionDto, { readonly version: number }>(
      `/v1/implementation-proposals/${segment(proposal.id)}/apply`,
      { version: proposal.version },
      mutationOptions(options, `"implementation-proposal:${proposal.id}:${proposal.version}"`),
    )
  }

  getWorkspaceRevision(revisionId: string, options?: ClientRequestOptions) {
    return this.http.get<WorkspaceRevisionDto>(
      `/v1/revisions/${segment(revisionId)}`,
      requestOptions(options),
    )
  }

  completeWorkbenchNode(
    projectId: string,
    runId: string,
    nodeKey: string,
    implementationProposalIds: readonly string[],
    workspaceRevision: ExactArtifactRefDto,
    options?: ClientMutationOptions,
  ) {
    return this.resumeRun(projectId, runId, nodeKey, {
      implementationProposalIds,
      workspaceRevision: wireVersionRef(workspaceRevision),
    }, options)
  }

  static manifestRef(manifest: InputManifestDto): ManifestRefDto {
    return { id: manifest.id, hash: manifest.hash }
  }
}
