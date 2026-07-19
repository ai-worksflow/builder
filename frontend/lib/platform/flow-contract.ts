import type {
  ArtifactKind,
  ArtifactRevisionDto,
  IsoDateTime,
  JsonObject,
  JsonValue,
  PageDto,
  ProblemDetailsDto,
  ProjectGovernanceMode,
  ValidationResultDto,
} from './dto'
import type { ExactApplicationBuildContractRefDto } from './constructor-contract'

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
  | 'transform'

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
  readonly allowedKinds?: readonly ArtifactKind[]
  readonly requireApproved: boolean
  readonly minimumArtifacts: number
  readonly maximumArtifacts: number
}

export interface WorkflowInputContractDto {
  readonly capability: 'project_brief' | 'blueprint_selection' | string
  readonly manifestJobTypes: readonly string[]
  readonly artifactKinds: readonly ArtifactKind[]
  readonly minimumArtifacts: number
  readonly maximumArtifacts: number
  readonly requireApproved: boolean
  readonly requiredSourcePurposes: readonly string[]
  readonly manifestSchemaContracts: Readonly<Record<string, string>>
}

export interface WorkflowOutputContractDto {
  readonly capability: 'application' | string
  readonly producedArtifactKinds: readonly ArtifactKind[]
  readonly terminalOutcome: 'application' | 'deployment'
  readonly terminalNodeType: WorkflowNodeType
}

export interface WorkflowExecutionProfileRefDto {
  readonly version: string
  readonly hash: string
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
  readonly artifactKind?: ArtifactKind
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
  /** Omitted only by historical workflow versions; governed authoring requires it. */
  readonly maxItems?: number
  readonly itemKind?: 'generic' | 'delivery_slice' | 'blueprint_page' | 'blueprint_selection_page'
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
  readonly transform?: { readonly transform: string }
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
  /** Absent only in immutable pre-pin definition JSON; the record still returns its legacy ref. */
  readonly executionProfile?: WorkflowExecutionProfileRefDto
  readonly nodes: readonly WorkflowNodeDefinitionDto[]
  readonly edges: readonly WorkflowEdgeDto[]
  /** Absent only on immutable historical versions created before contracts. */
  readonly inputContract?: WorkflowInputContractDto
  /** Absent only on immutable historical versions created before contracts. */
  readonly outputContract?: WorkflowOutputContractDto
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
  readonly executionProfile: WorkflowExecutionProfileRefDto
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
  readonly inputContract: WorkflowInputContractDto
  readonly outputContract: WorkflowOutputContractDto
}

export interface CreateWorkflowDefinitionVersionInputDto {
  readonly name?: string
  readonly schemaVersion?: string
  readonly nodes: readonly WorkflowNodeDefinitionDto[]
  readonly edges: readonly WorkflowEdgeDto[]
  readonly inputContract: WorkflowInputContractDto
  readonly outputContract: WorkflowOutputContractDto
}

export interface WorkflowAITransformCapabilityDto {
  readonly jobType: string
  readonly outputSchemaVersion: string
  readonly modelPolicies: readonly string[]
  readonly requiredArtifactKinds: readonly ArtifactKind[]
  readonly requiredApprovedKinds: readonly ArtifactKind[]
  readonly producedArtifactKinds: readonly ArtifactKind[]
}

export interface WorkflowManifestCompilerCapabilityDto {
  readonly manifestKind: string
  readonly schemaVersion: number
  readonly hook: string
  readonly requiredArtifactKinds: readonly ArtifactKind[]
  readonly requiredApprovedKinds: readonly ArtifactKind[]
  readonly requiresMergedSlices: boolean
  readonly producedSemanticKinds: readonly string[]
  readonly allowedContextArtifactKinds: readonly ArtifactKind[]
}

export interface WorkflowCapabilitiesDto {
  readonly version: number
  readonly nodeTypes: readonly WorkflowNodeType[]
  readonly inputContracts: readonly WorkflowInputContractDto[]
  readonly outputContracts: readonly WorkflowOutputContractDto[]
  readonly aiTransforms: readonly WorkflowAITransformCapabilityDto[]
  readonly manifestCompilers: readonly WorkflowManifestCompilerCapabilityDto[]
  readonly transforms: readonly string[]
  readonly fanOutItemKinds: readonly ('generic' | 'delivery_slice' | 'blueprint_page' | 'blueprint_selection_page')[]
  readonly fanOutMaximumItems: Readonly<Record<string, number>>
  readonly qualityGates: readonly string[]
  readonly publishEnvironments: readonly string[]
  readonly workbenchSchemaVersions: readonly number[]
  readonly analysisLimits: {
    readonly maximumDefinitionNodes: number
    readonly maximumDefinitionEdges: number
    readonly maxSemanticPathStates: number
    readonly maximumConditionExpressionBytes: number
  }
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

export interface BlueprintSelectionCompileInputDto {
  readonly blueprintRevision: ExactArtifactRefDto
  readonly nodeIds: readonly string[]
}

export interface BlueprintSelectionPageBindingDto {
  readonly nodeId: string
  readonly pageSpec?: ExactArtifactRefDto
  readonly prototype?: ExactArtifactRefDto
}

export interface BlueprintSelectionScopeDto {
  readonly schemaVersion: 1
  readonly selectionId: string
  readonly blueprint: ExactArtifactRefDto
  readonly nodeIds: readonly string[]
  readonly nodes: readonly {
    readonly id: string
    readonly key: string
    readonly kind: string
    readonly title: string
    readonly requirementIds?: readonly string[]
  }[]
  readonly edges: readonly {
    readonly id: string
    readonly sourceNodeId: string
    readonly targetNodeId: string
    readonly kind: string
    readonly required: boolean
  }[]
  readonly pageBindings: readonly BlueprintSelectionPageBindingDto[]
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

export interface WorkflowProposalLineagePinDto {
  readonly proposal: { readonly id: string; readonly payloadHash: string }
  readonly manifest: ManifestRefDto
  readonly producerNodeKey: string
  readonly producerDefinitionNodeId: string
}

export interface WorkflowNodeOutputReferenceDto {
  readonly runId: string
  readonly nodeKey: string
  readonly definitionNodeId: string
  readonly inputManifest?: ManifestRefDto
  readonly outputProposal?: { readonly id: string; readonly payloadHash: string }
  readonly proposalPins?: readonly WorkflowProposalLineagePinDto[]
  readonly artifactRevisions?: readonly ExactArtifactRefDto[]
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
    readonly input?: JsonValue
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
  readonly definition: { readonly id: string; readonly version: number; readonly hash: string; readonly executionProfile: WorkflowExecutionProfileRefDto }
  readonly executionProfile: WorkflowExecutionProfileRefDto
  readonly inputManifest?: ManifestRefDto
  /** Review policy frozen when this run started. */
  readonly governanceMode?: ProjectGovernanceMode
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
  readonly executionProfile: WorkflowExecutionProfileRefDto
  readonly governanceMode?: ProjectGovernanceMode
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

export interface ApplicationBuildContextDto {
  readonly definition: {
    readonly id: string
    readonly version: number
    readonly hash: string
    /** Absent only on immutable Workbench bundles frozen before execution-profile pinning. */
    readonly executionProfile?: WorkflowExecutionProfileRefDto
  }
  /** Required on every newly created bundle; absent only for historical frozen payloads. */
  readonly executionProfile?: WorkflowExecutionProfileRefDto
  readonly inputManifest: InputManifestDto
  readonly deliverySliceId?: string
  readonly runScope?: JsonValue
  readonly outputContract?: WorkflowOutputContractDto
}

export interface WorkbenchBundleDto {
  readonly id: string
  /** Stable manifest-order identity shared by every exact-workspace derivative. */
  readonly rootBuildManifestId?: string
  /** Immediate immutable manifest from which this exact-workspace bundle was derived. */
  readonly derivedFromBuildManifestId?: string
  readonly projectId: string
  readonly workflowRunId?: string
  readonly manifestGroupKey?: string
  readonly deliverySliceId?: string
  readonly pageSpecRevision: ExactArtifactRefDto
  readonly prototypeRevision: ExactArtifactRefDto
  readonly requirementRevisions: readonly ExactArtifactRefDto[]
  readonly blueprintRevision: ExactArtifactRefDto
  readonly contractRevisions: readonly ExactArtifactRefDto[]
  readonly designSystemRevisions: readonly ExactArtifactRefDto[]
  readonly contextRevisions?: readonly {
    readonly kind: string
    readonly revision: ExactArtifactRefDto
  }[]
  readonly workflowContext?: ApplicationBuildContextDto
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

export interface WorkbenchBundleLineageStateDto {
  readonly rootBundleId: string
  readonly activeBundle: WorkbenchBundleDto
  readonly currentProposal?: ImplementationProposalDto
  /** Project-wide latest applied workspace, shared by every root lineage state. */
  readonly currentWorkspaceRevision?: ExactArtifactRefDto
  readonly lineage: readonly {
    readonly bundleId: string
    readonly derivedFromBuildManifestId?: string
    readonly workspaceRevision?: ExactArtifactRefDto
    readonly status: string
    readonly createdAt: IsoDateTime
    readonly latestProposal?: {
      readonly id: string
      readonly status: ImplementationProposalDto['status']
      readonly version: number
      readonly createdAt: IsoDateTime
    }
  }[]
}

export interface CreateWorkbenchBundleInputDto {
  readonly prototypeRevision: ExactArtifactRefDto
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
  readonly mode?: '100644' | '100755'
  readonly expectedHash?: string
  readonly dependsOn?: readonly string[]
  readonly rationale?: string
  readonly traceSource?: readonly string[]
  readonly decision: ImplementationDecision
  readonly decidedBy?: string
  readonly reason?: string
}

export interface CandidateImplementationSourceDto {
  readonly freezeReceiptId: string
  readonly repositorySnapshotId: string
  readonly sessionId: string
  readonly candidateId: string
  readonly candidateSnapshotId: string
  readonly candidateVersion: number
  readonly journalSequence: number
  readonly sessionEpoch: number
  readonly writerLeaseEpoch: number
  readonly baseTreeHash: string
  readonly treeHash: string
  readonly fullStackTemplate: {
    readonly id: string
    readonly contentHash: string
  }
  readonly verificationReceipt: {
    readonly id: string
    readonly contentHash: string
  }
}

export interface ImplementationProposalDto {
  readonly id: string
  readonly projectId: string
  readonly buildManifestId: string
  readonly applicationBuildContract: ExactApplicationBuildContractRefDto
  readonly baseWorkspaceRevision?: ExactArtifactRefDto
  readonly executionSource: 'manual_submission' | 'manual_generation' | 'workflow_runner' | 'conversation_command' | 'candidate_freeze'
  readonly conversationCommandId?: string
  readonly supersedesProposalId?: string
  readonly instructionHash?: string
  readonly aiProvider?: string
  readonly aiModel?: string
  readonly candidateSource?: CandidateImplementationSourceDto
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
  readonly status: 'open' | 'reviewing' | 'ready' | 'rejected' | 'applied' | 'partially_applied' | 'stale'
  readonly version: number
  readonly payloadHash: string
  readonly createdBy: string
  readonly createdAt: IsoDateTime
  readonly appliedAt?: IsoDateTime
}

export interface CreateImplementationProposalInputDto {
  readonly buildManifestId: string
  readonly applicationBuildContract: ExactApplicationBuildContractRefDto
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

export interface FlowServiceErrorDto extends ProblemDetailsDto {
  readonly retryAfterSeconds?: number
}

export type WorkflowPageDto<T> = PageDto<T>
