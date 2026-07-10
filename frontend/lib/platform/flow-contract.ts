import type {
  ArtifactRevisionDto,
  IsoDateTime,
  JsonObject,
  JsonValue,
  PageDto,
  ProblemDetailsDto,
  ValidationResultDto,
} from './dto'

export type WorkflowNodeType =
  | 'artifact_input'
  | 'ai_transform'
  | 'human_edit'
  | 'review_gate'
  | 'condition'
  | 'fan_out'
  | 'merge'
  | 'quality_gate'
  | 'manifest_compiler'
  | 'workbench_build'
  | 'publish'

export type WorkflowArtifactType =
  | 'document'
  | 'blueprint'
  | 'prototype'
  | 'implementation'
  | 'test'

export interface WorkflowPortDto {
  readonly schema: JsonObject
  readonly description?: string
}

export interface WorkflowArtifactInputConfigDto {
  readonly allowedTypes: readonly WorkflowArtifactType[]
  readonly requireApproved: boolean
  readonly minimumArtifacts: number
}

export interface WorkflowAITransformConfigDto {
  readonly jobType: string
  readonly modelPolicy: string
  readonly outputSchemaVersion: string
  readonly maxAttempts: number
  /** Go time.Duration encoded as nanoseconds. */
  readonly timeout: number
}

export interface WorkflowHumanEditConfigDto {
  readonly artifactType: WorkflowArtifactType
  readonly requiredRole: string
  readonly instructions?: string
}

export interface WorkflowReviewGateConfigDto {
  readonly requiredRole: string
  readonly minimumApprovals: number
  readonly prohibitSelfReview: boolean
  readonly allowWaiver: boolean
}

export interface WorkflowConditionConfigDto {
  readonly branches: readonly {
    readonly name: string
    readonly expression?: string
    readonly default: boolean
  }[]
}

export interface WorkflowFanOutConfigDto {
  readonly itemsPath: string
  readonly sliceKeyPath: string
  readonly mergeNodeId: string
  readonly maxParallel: number
  readonly itemKind?: 'generic' | 'delivery_slice'
}

export interface WorkflowMergeConfigDto {
  readonly fanOutNodeId: string
  readonly policy: 'all' | 'any' | 'quorum'
  readonly quorum?: number
  readonly allowWaiver: boolean
}

export interface WorkflowQualityGateConfigDto {
  readonly gateName: string
  readonly blocking: boolean
  readonly requiredRole?: string
}

export interface WorkflowManifestCompilerConfigDto {
  readonly manifestKind: string
  readonly schemaVersion: number
  readonly hook: string
}

export interface WorkflowWorkbenchBuildConfigDto {
  readonly buildManifestSchemaVersion: number
  readonly maxAttempts: number
  /** Go time.Duration encoded as nanoseconds. */
  readonly timeout: number
}

export interface WorkflowPublishConfigDto {
  readonly environment: string
  readonly requiredRole: string
  readonly allowRollback: boolean
}

export interface WorkflowNodeDefinitionDto {
  readonly id: string
  readonly name: string
  readonly type: WorkflowNodeType
  readonly inputSchema?: JsonObject
  readonly outputSchema?: JsonObject
  readonly inputPorts?: Readonly<Record<string, WorkflowPortDto>>
  readonly outputPorts?: Readonly<Record<string, WorkflowPortDto>>
  readonly artifactInput?: WorkflowArtifactInputConfigDto
  readonly aiTransform?: WorkflowAITransformConfigDto
  readonly humanEdit?: WorkflowHumanEditConfigDto
  readonly reviewGate?: WorkflowReviewGateConfigDto
  readonly condition?: WorkflowConditionConfigDto
  readonly fanOut?: WorkflowFanOutConfigDto
  readonly merge?: WorkflowMergeConfigDto
  readonly qualityGate?: WorkflowQualityGateConfigDto
  readonly manifestCompiler?: WorkflowManifestCompilerConfigDto
  readonly workbenchBuild?: WorkflowWorkbenchBuildConfigDto
  readonly publish?: WorkflowPublishConfigDto
}

export interface WorkflowEdgeDto {
  readonly id: string
  readonly from: string
  readonly fromPort?: string
  readonly to: string
  readonly toPort?: string
  readonly mapping?: Readonly<Record<string, string>>
}

export interface WorkflowDefinitionDto {
  readonly id: string
  readonly version: number
  readonly name: string
  readonly schemaVersion: string
  readonly nodes: readonly WorkflowNodeDefinitionDto[]
  readonly edges: readonly WorkflowEdgeDto[]
  readonly hash: string
  readonly createdBy: string
  readonly createdAt: IsoDateTime
}

export interface WorkflowDefinitionRecordDto {
  readonly id: string
  readonly versionId: string
  readonly projectId: string
  readonly key: string
  readonly title: string
  readonly description?: string
  readonly published: boolean
  readonly version: number
  readonly contentHash: string
  readonly definition: WorkflowDefinitionDto
}

export interface CreateWorkflowDefinitionInputDto {
  readonly key: string
  readonly title: string
  readonly description?: string
  readonly name?: string
  readonly schemaVersion?: string
  readonly nodes: readonly WorkflowNodeDefinitionDto[]
  readonly edges: readonly WorkflowEdgeDto[]
}

export interface CreateWorkflowDefinitionVersionInputDto {
  readonly name?: string
  readonly schemaVersion?: string
  readonly nodes: readonly WorkflowNodeDefinitionDto[]
  readonly edges: readonly WorkflowEdgeDto[]
}

export interface ExactArtifactRefDto {
  readonly artifactId: string
  readonly revisionId: string
  readonly revisionNumber?: number
  readonly contentHash: string
  readonly anchorId?: string
}

export interface ManifestRefDto {
  readonly id: string
  readonly hash: string
}

export interface ManifestSourceDto {
  readonly ref: ExactArtifactRefDto
  readonly purpose: string
}

export interface InputManifestDto {
  readonly id: string
  readonly projectId: string
  readonly jobType: string
  readonly deliverySliceId?: string
  readonly baseRevision?: ExactArtifactRefDto
  readonly sources: readonly ManifestSourceDto[]
  readonly constraints: JsonObject
  readonly outputSchemaVersion: string
  readonly createdBy: string
  readonly createdAt: IsoDateTime
  readonly hash: string
}

export interface CreateInputManifestDto {
  readonly jobType: string
  readonly deliverySliceId?: string
  readonly baseRevision?: ExactArtifactRefDto
  readonly sources: readonly ManifestSourceDto[]
  readonly constraints: JsonObject
  readonly outputSchemaVersion: string
}

export type WorkflowRunStatus =
  | 'pending'
  | 'running'
  | 'waiting_input'
  | 'waiting_review'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'stale'

export type WorkflowNodeStatus =
  | 'pending'
  | 'ready'
  | 'running'
  | 'waiting_input'
  | 'waiting_review'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'stale'

export interface WorkflowNodeRunDto {
  readonly id: string
  readonly runId: string
  readonly key: string
  readonly definitionNodeId: string
  readonly sliceId?: string
  readonly type: WorkflowNodeType
  readonly status: WorkflowNodeStatus
  readonly attempt: number
  readonly inputManifest?: ManifestRefDto
  readonly outputProposal?: { readonly id: string; readonly payloadHash: string }
  readonly outputRevisionId?: string
  readonly leaseExpiresAt?: IsoDateTime
  readonly availableAt: IsoDateTime
  readonly startedAt?: IsoDateTime
  readonly completedAt?: IsoDateTime
  readonly failure?: JsonValue
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
}

export interface WorkflowSliceContextDto {
  readonly id: string
  readonly key: string
  readonly title: string
  readonly fanOutNodeId: string
  readonly payload?: JsonValue
  readonly blueprint?: ExactArtifactRefDto
  readonly pageSpec?: ExactArtifactRefDto
  readonly prototype?: ExactArtifactRefDto
  readonly ownerId?: string
}

export interface WorkflowRunContextDto {
  readonly values?: Readonly<Record<string, JsonValue>>
  readonly nodes: Readonly<Record<string, {
    readonly definitionNodeId: string
    readonly sliceId?: string
    readonly maxAttempts: number
    readonly timeoutNanos: number
    readonly waived?: boolean
    readonly waiverReason?: string
    readonly selectedBranch?: string
    readonly output?: JsonValue
    readonly executionActor?: WorkflowActorProvenanceDto
    readonly reviewDecisionActor?: WorkflowActorProvenanceDto
  }>>
  readonly disabledEdges?: Readonly<Record<string, boolean>>
  readonly selectedBranches?: Readonly<Record<string, string>>
  readonly slices?: Readonly<Record<string, WorkflowSliceContextDto>>
}

export interface WorkflowActorProvenanceDto {
  readonly actorId: string
  readonly role: string
  readonly action: string
  readonly source: 'authenticated_command' | 'review_approval' | 'review_waiver'
  readonly authorizedAt: IsoDateTime
}

export interface WorkflowRunDto {
  readonly id: string
  readonly projectId: string
  readonly definitionVersionId: string
  readonly definition: { readonly id: string; readonly version: number; readonly hash: string }
  readonly inputManifest?: ManifestRefDto
  readonly status: WorkflowRunStatus
  readonly scope?: JsonValue
  readonly context: WorkflowRunContextDto
  readonly eventCursor: number
  readonly startedBy: string
  readonly startedAt?: IsoDateTime
  readonly completedAt?: IsoDateTime
  readonly cancelledAt?: IsoDateTime
  readonly failure?: JsonValue
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
  readonly nodes: readonly WorkflowNodeRunDto[]
}

/** Bounded workflow history row; hydrate with getRun before reading context or nodes. */
export interface WorkflowRunSummaryDto {
  readonly id: string
  readonly projectId: string
  readonly definitionVersionId: string
  readonly status: WorkflowRunStatus
  readonly eventCursor: number
  readonly startedBy: string
  readonly startedAt?: IsoDateTime
  readonly completedAt?: IsoDateTime
  readonly cancelledAt?: IsoDateTime
  readonly failure?: JsonValue
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
}

export interface StartWorkflowRunInputDto {
  readonly runId?: string
  readonly definitionVersionId?: string
  readonly inputManifest: ManifestRefDto
  readonly scope?: JsonValue
}

export interface WorkflowEventDto {
  readonly id: string
  readonly runId: string
  readonly sequence: number
  readonly type: string
  readonly nodeKey?: string
  readonly payload?: JsonValue
  readonly actorId?: string
  readonly createdAt: IsoDateTime
}

export interface AssetRefDto {
  readonly assetId: string
  readonly contentHash: string
  readonly mediaType: string
  readonly byteSize: number
  readonly name?: string
}

export interface WorkbenchBundleDto {
  readonly id: string
  readonly projectId: string
  readonly workflowRunId?: string
  readonly deliverySliceId?: string
  readonly pageSpecRevision: ExactArtifactRefDto
  readonly prototypeRevision: ExactArtifactRefDto
  readonly requirementRevisions: readonly ExactArtifactRefDto[]
  readonly blueprintRevision: ExactArtifactRefDto
  readonly contractRevisions: readonly ExactArtifactRefDto[]
  readonly designSystemRevisions: readonly ExactArtifactRefDto[]
  readonly currentWorkspaceRevision?: ExactArtifactRefDto
  readonly sceneGraph: AssetRefDto
  readonly renderedFrames: readonly (AssetRefDto & {
    readonly stateId: string
    readonly breakpointId: string
  })[]
  readonly interactionManifest: AssetRefDto
  readonly fixtureBundle: AssetRefDto
  readonly tokenManifest: AssetRefDto
  readonly componentMapping: AssetRefDto
  readonly traceMatrix: AssetRefDto
  readonly acceptanceManifest: AssetRefDto
  readonly assumptions: readonly string[]
  readonly waivers: readonly string[]
  readonly createdBy: string
  readonly createdAt: IsoDateTime
  readonly contentHash: string
}

export interface CreateWorkbenchBundleInputDto {
  readonly prototypeRevision: ExactArtifactRefDto
  readonly workflowRunId?: string
  readonly deliverySliceId?: string
  readonly allowStale?: boolean
  readonly overrideReason?: string
}

export type ImplementationDecision = 'pending' | 'accepted' | 'rejected' | 'applied'

export interface FileOperationDto {
  readonly id: string
  readonly kind: 'file.upsert' | 'file.delete' | 'file.rename'
  readonly path: string
  readonly fromPath?: string
  readonly content?: string
  readonly language?: string
  readonly expectedHash?: string
  readonly dependsOn?: readonly string[]
  readonly rationale?: string
  readonly traceSource?: readonly string[]
  readonly decision: ImplementationDecision
  readonly decidedBy?: string
  readonly reason?: string
}

export interface ImplementationProposalDto {
  readonly id: string
  readonly projectId: string
  readonly buildManifestId: string
  readonly baseWorkspaceRevision?: ExactArtifactRefDto
  readonly operations: readonly FileOperationDto[]
  readonly routes: readonly JsonValue[]
  readonly apis: readonly JsonValue[]
  readonly migrations: readonly JsonValue[]
  readonly tests: readonly JsonValue[]
  readonly previews: readonly JsonValue[]
  readonly traceLinks: readonly JsonValue[]
  readonly diagnostics: readonly ValidationResultDto[]
  readonly assumptions: readonly string[]
  readonly unimplementedItems: readonly string[]
  readonly status: 'open' | 'reviewing' | 'ready' | 'applied' | 'partially_applied' | 'stale'
  readonly version: number
  readonly payloadHash: string
  readonly createdBy: string
  readonly createdAt: IsoDateTime
  readonly appliedAt?: IsoDateTime
}

export interface CreateImplementationProposalInputDto {
  readonly buildManifestId: string
  readonly operations: readonly Omit<FileOperationDto, 'decision' | 'decidedBy' | 'reason'>[]
  readonly routes?: readonly JsonValue[]
  readonly apis?: readonly JsonValue[]
  readonly migrations?: readonly JsonValue[]
  readonly tests?: readonly JsonValue[]
  readonly previews?: readonly JsonValue[]
  readonly traceLinks?: readonly JsonValue[]
  readonly diagnostics?: readonly ValidationResultDto[]
  readonly assumptions?: readonly string[]
  readonly unimplementedItems?: readonly string[]
}

export interface WorkspaceContentDto {
  readonly schemaVersion: number
  readonly id: string
  readonly name: string
  readonly revision: number
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
  readonly files: readonly {
    readonly path: string
    readonly content: string
    readonly language?: string
    readonly contentHash?: string
  }[]
}

export type WorkspaceRevisionDto = ArtifactRevisionDto<WorkspaceContentDto>

export interface ImplementationGenerationResultDto {
  readonly proposal: ImplementationProposalDto
  readonly provider: string
  readonly model: string
  readonly usage?: JsonValue
}

export interface FlowServiceErrorDto extends ProblemDetailsDto {
  readonly retryAfterSeconds?: number
}

export type WorkflowPageDto<T> = PageDto<T>
