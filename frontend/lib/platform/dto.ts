export type IsoDateTime = string
export type EntityId = string
export type ContentHash = string

export type JsonPrimitive = string | number | boolean | null
export type JsonValue = JsonPrimitive | JsonObject | JsonValue[]

export interface JsonObject {
  readonly [key: string]: JsonValue
}

export interface PageDto<T> {
  readonly items: readonly T[]
  readonly nextCursor?: string
  readonly total?: number
}

export interface VersionRefDto {
  readonly artifactId: EntityId
  readonly revisionId: EntityId
  readonly revisionNumber: number
  readonly contentHash: ContentHash
  readonly anchorId?: EntityId
}

export interface AssetRefDto {
  readonly assetId: EntityId
  readonly contentHash: ContentHash
  readonly mediaType: string
  readonly byteSize: number
  readonly name?: string
}

export interface UserDto {
  readonly id: EntityId
  readonly displayName: string
  readonly email: string
  readonly avatarUrl?: string
  readonly createdAt: IsoDateTime
}

export type SessionState = 'anonymous' | 'authenticated' | 'expired'

export interface SessionDto {
  readonly state: SessionState
  readonly user?: UserDto
  readonly sessionId?: EntityId
  readonly issuedAt?: IsoDateTime
  readonly expiresAt?: IsoDateTime
  readonly csrfToken?: string
}

export interface SessionSignInInputDto {
  readonly email: string
  readonly password: string
}

export interface SessionSignUpInputDto {
  readonly displayName: string
  readonly email: string
  readonly password: string
}

export type ProjectRole = 'owner' | 'admin' | 'editor' | 'commenter' | 'viewer'
export type ProjectLifecycle = 'active' | 'archived'

export interface ProjectDto {
  readonly id: EntityId
  readonly name: string
  readonly description?: string
  readonly teamId?: EntityId
  readonly lifecycle: ProjectLifecycle
  readonly currentUserRole: ProjectRole
  readonly memberCount?: number
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
  readonly etag: string
}

export interface CreateProjectInputDto {
  readonly name: string
  readonly description?: string
  readonly teamId?: EntityId
}

export interface UpdateProjectInputDto {
  readonly name?: string
  readonly description?: string
  readonly lifecycle?: ProjectLifecycle
}

export interface ProjectMemberDto {
  readonly projectId: EntityId
  readonly user: UserDto
  readonly role: ProjectRole
  readonly joinedAt: IsoDateTime
  readonly invitedBy?: EntityId
  readonly etag: string
}

export interface AddProjectMemberInputDto {
  readonly email: string
  readonly displayName?: string
  readonly role: ProjectRole
}

export interface UpdateProjectMemberInputDto {
  readonly role: ProjectRole
}

export interface ProjectInvitationDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly email: string
  readonly role: ProjectRole
  readonly status: 'pending' | 'accepted' | 'expired' | 'revoked'
  readonly expiresAt: IsoDateTime
  readonly createdAt: IsoDateTime
  readonly token?: string
}

export interface CreateProjectInvitationInputDto {
  readonly email: string
  readonly role: ProjectRole
}

export type ArtifactKind =
  | 'document'
  | 'blueprint'
  | 'pageSpec'
  | 'prototype'
  | 'workflow'
  | 'workbenchBundle'
  | 'traceMatrix'

export type ArtifactStatus =
  | 'draft'
  | 'inReview'
  | 'changesRequested'
  | 'approved'
  | 'needsSync'
  | 'archived'

export interface ArtifactDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly kind: ArtifactKind
  readonly title: string
  readonly status: ArtifactStatus
  readonly activeDraftId?: EntityId
  readonly latestRevisionId?: EntityId
  readonly approvedRevisionId?: EntityId
  readonly latestRevision?: VersionRefDto
  readonly approvedRevision?: VersionRefDto
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
  readonly etag: string
}

export interface CreateArtifactInputDto {
  readonly kind: ArtifactKind
  readonly title: string
  readonly sourceVersions?: readonly VersionRefDto[]
}

export interface ArtifactDraftDto<TContent> {
  readonly id: EntityId
  readonly artifactId: EntityId
  readonly baseRevisionId?: EntityId
  readonly sourceVersions: readonly VersionRefDto[]
  readonly revision: number
  readonly content: TContent
  readonly contentHash: ContentHash
  readonly updatedBy: EntityId
  readonly updatedAt: IsoDateTime
  readonly etag: string
}

export interface ArtifactRevisionDto<TContent> {
  readonly id: EntityId
  readonly artifactId: EntityId
  readonly revisionNumber: number
  readonly basedOnRevisionId?: EntityId
  readonly sourceVersions?: readonly VersionRefDto[]
  readonly content: TContent
  readonly contentHash: ContentHash
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
}

export interface VersionedArtifactDto<TContent> {
  readonly artifact: ArtifactDto
  readonly draft?: ArtifactDraftDto<TContent>
  readonly latestRevision?: ArtifactRevisionDto<TContent>
  readonly approvedRevision?: ArtifactRevisionDto<TContent>
}

export type DependencyRelation =
  | 'dependsOn'
  | 'derivesFrom'
  | 'implements'
  | 'renders'
  | 'uses'
  | 'calls'
  | 'requires'
  | 'generatedBy'

export interface ArtifactDependencyDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly source: VersionRefDto
  readonly target: VersionRefDto
  readonly relation: DependencyRelation
  readonly blocking: boolean
  readonly createdAt: IsoDateTime
}

export interface CreateRevisionInputDto {
  readonly changeSummary: string
  readonly changeSource?: 'human' | 'ai_proposal' | 'import' | 'merge' | 'rollback' | 'system'
}

export interface UpdateDraftInputDto<TContent> {
  readonly content: TContent
  readonly sourceVersions?: readonly VersionRefDto[]
}

export type DocumentKind =
  | 'requirement'
  | 'pageSplit'
  | 'featureList'
  | 'apiContract'
  | 'backendDevelopment'
  | 'uiPrototype'
  | 'frontendDevelopment'
  | 'decisionLog'

export interface DocumentBlockDto {
  readonly id: EntityId
  readonly type: 'heading' | 'paragraph' | 'list' | 'table' | 'code' | 'callout'
  readonly text?: string
  readonly data?: JsonObject
  readonly children?: readonly DocumentBlockDto[]
  readonly requirementIds?: readonly EntityId[]
}

export interface AcceptanceCriterionDto {
  readonly id: EntityId
  readonly statement: string
  readonly priority: 'must' | 'should' | 'could'
  readonly status: 'open' | 'accepted' | 'rejected'
}

export interface RequirementItemDto {
  readonly id: EntityId
  readonly title: string
  readonly statement: string
  readonly priority: 'must' | 'should' | 'could'
  readonly acceptanceCriterionIds: readonly EntityId[]
  readonly sourceBlockIds: readonly EntityId[]
}

export interface DocumentContentDto {
  readonly kind: DocumentKind
  readonly summary: string
  readonly blocks: readonly DocumentBlockDto[]
  readonly acceptanceCriteria: readonly AcceptanceCriterionDto[]
  readonly requirements?: readonly RequirementItemDto[]
  readonly openQuestions: readonly string[]
  readonly assumptions: readonly string[]
}

export interface CreateDocumentInputDto {
  readonly title: string
  readonly kind: DocumentKind
  readonly content?: DocumentContentDto
  readonly sourceVersions?: readonly VersionRefDto[]
}

export type BlueprintNodeKind =
  | 'feature'
  | 'page'
  | 'component'
  | 'api'
  | 'dataModel'
  | 'permission'
  | 'workbenchTarget'

export type BlueprintEdgeKind =
  | 'contains'
  | 'uses'
  | 'calls'
  | 'reads'
  | 'writes'
  | 'requires'
  | 'renders'
  | 'implements'

export interface BlueprintNodeDto {
  readonly id: EntityId
  readonly kind: BlueprintNodeKind
  readonly title: string
  readonly description?: string
  readonly position: { readonly x: number; readonly y: number }
  readonly requirementIds: readonly EntityId[]
  readonly pageSpecArtifactId?: EntityId
  readonly assignedMemberIds: readonly EntityId[]
  readonly metadata?: JsonObject
}

export interface BlueprintEdgeDto {
  readonly id: EntityId
  readonly sourceNodeId: EntityId
  readonly targetNodeId: EntityId
  readonly kind: BlueprintEdgeKind
  readonly required: boolean
}

export interface BlueprintContentDto {
  readonly nodes: readonly BlueprintNodeDto[]
  readonly edges: readonly BlueprintEdgeDto[]
  readonly semantic?: BlueprintSemanticDto
  readonly layout?: BlueprintLayoutDto
  readonly pageSpecRefs?: readonly VersionRefDto[]
  readonly validation: readonly ValidationResultDto[]
}

export interface BlueprintSemanticDto {
  readonly nodes: readonly Omit<BlueprintNodeDto, 'position'>[]
  readonly edges: readonly BlueprintEdgeDto[]
}

export interface BlueprintLayoutDto {
  readonly nodePositions: Readonly<Record<EntityId, { readonly x: number; readonly y: number }>>
  readonly groups: readonly {
    readonly id: EntityId
    readonly title: string
    readonly nodeIds: readonly EntityId[]
  }[]
  readonly viewport?: { readonly x: number; readonly y: number; readonly zoom: number }
}

export interface ArtifactReviewGateDto {
  readonly passed: boolean
  readonly checks: readonly ValidationResultDto[]
  readonly unresolvedBlockingCommentIds: readonly EntityId[]
  readonly traceCoverage: number
}

export interface ImpactItemDto {
  readonly source: VersionRefDto
  readonly targetArtifactId: EntityId
  readonly targetKind: ArtifactKind
  readonly reason: string
  readonly severity: 'info' | 'warning' | 'blocking'
  readonly needsSync: boolean
}

export interface ImpactReportDto {
  readonly projectId: EntityId
  readonly blueprintArtifactId: EntityId
  readonly basedOnRevisionId?: EntityId
  readonly items: readonly ImpactItemDto[]
  readonly generatedAt: IsoDateTime
}

export interface CreateBlueprintInputDto {
  readonly title: string
  readonly requirementVersions: readonly VersionRefDto[]
  readonly content?: BlueprintContentDto
}

export interface PageStateDto {
  readonly id: EntityId
  readonly key: string
  readonly title: string
  readonly description?: string
  readonly entryCondition?: string
  readonly required: boolean
  readonly fixtureIds: readonly EntityId[]
  readonly acceptanceCriterionIds: readonly EntityId[]
}

export interface PageDataBindingDto {
  readonly id: EntityId
  readonly name: string
  readonly source: 'api' | 'database' | 'fixture' | 'local'
  readonly operationId?: string
  readonly schema?: JsonObject
  readonly required: boolean
}

export interface PageInteractionSpecDto {
  readonly id: EntityId
  readonly trigger: string
  readonly outcome: string
  readonly targetPageSpecId?: EntityId
  readonly acceptanceCriterionIds: readonly EntityId[]
}

export interface PageSpecContentDto {
  readonly blueprintPageNodeId: EntityId
  readonly title: string
  readonly route: string
  readonly userGoal: string
  readonly entryPoints: readonly string[]
  readonly exitPoints: readonly string[]
  readonly requiredRoles: readonly string[]
  readonly states: readonly PageStateDto[]
  readonly dataBindings: readonly PageDataBindingDto[]
  readonly interactions: readonly PageInteractionSpecDto[]
  readonly acceptanceCriterionIds: readonly EntityId[]
  readonly nonFunctionalConstraints: readonly string[]
}

export interface CreatePageSpecInputDto {
  readonly title: string
  readonly blueprintRevision: VersionRefDto
  readonly blueprintPageNodeId: EntityId
  readonly content?: PageSpecContentDto
}

export interface PrototypeStateDto {
  readonly id: EntityId
  readonly key: string
  readonly title: string
  readonly required: boolean
  readonly fixtureIds: readonly EntityId[]
  readonly pageStateId?: EntityId
}

export interface PrototypeBreakpointDto {
  readonly id: EntityId
  readonly name: string
  readonly minWidth: number
  readonly maxWidth?: number
  readonly viewportWidth: number
  readonly viewportHeight: number
}

export type PrototypeLayerKind =
  | 'frame'
  | 'group'
  | 'text'
  | 'image'
  | 'componentInstance'
  | 'input'
  | 'button'
  | 'list'
  | 'overlay'
  | 'slot'

export interface PrototypeFieldMetadataDto {
  readonly source: 'ai' | 'human' | 'import'
  readonly changedBy: EntityId
  readonly changedAt: IsoDateTime
  readonly operationId: EntityId
  readonly aiPolicy: 'replaceable' | 'suggestOnly' | 'preserve'
}

export interface PrototypeLayerDto {
  readonly id: EntityId
  readonly parentId?: EntityId
  readonly childIds: readonly EntityId[]
  readonly kind: PrototypeLayerKind
  readonly name: string
  readonly semanticRole?: string
  readonly layout: JsonObject
  readonly style: JsonObject
  readonly properties: JsonObject
  readonly dataBindingId?: EntityId
  readonly componentRef?: VersionRefDto
  readonly requirementIds: readonly EntityId[]
  readonly acceptanceCriterionIds: readonly EntityId[]
  readonly fieldMetadata: Readonly<Record<string, PrototypeFieldMetadataDto>>
}

export interface PrototypeFrameDto {
  readonly id: EntityId
  readonly stateId: EntityId
  readonly breakpointId: EntityId
  readonly rootLayerId: EntityId
  readonly title: string
}

export interface PrototypeVariantOverrideDto {
  readonly id: EntityId
  readonly stateId?: EntityId
  readonly breakpointId?: EntityId
  readonly layerId: EntityId
  readonly propertyPath: string
  readonly value: JsonValue
}

export type PrototypeActionDto =
  | { readonly type: 'navigate'; readonly targetPageSpecId: EntityId; readonly targetStateId?: EntityId }
  | { readonly type: 'setState'; readonly stateId: EntityId }
  | { readonly type: 'openOverlay'; readonly layerId: EntityId }
  | { readonly type: 'closeOverlay' }
  | { readonly type: 'updateBinding'; readonly bindingId: EntityId; readonly value: JsonValue }
  | { readonly type: 'submitFixture'; readonly fixtureId: EntityId }

export interface PrototypeInteractionDto {
  readonly id: EntityId
  readonly sourceLayerId: EntityId
  readonly trigger: 'click' | 'submit' | 'change' | 'hover' | 'load'
  readonly guards: readonly JsonObject[]
  readonly actions: readonly PrototypeActionDto[]
}

export interface PrototypeFixtureDto {
  readonly id: EntityId
  readonly name: string
  readonly stateId: EntityId
  readonly operationId?: string
  readonly request?: JsonValue
  readonly response: JsonValue
  readonly schema?: JsonObject
  readonly statusCode: number
  readonly latencyMs: number
  readonly sanitized: boolean
  readonly contentHash: ContentHash
}

export interface PrototypeTokenBindingDto {
  readonly id: EntityId
  readonly layerId: EntityId
  readonly propertyPath: string
  readonly tokenId: string
  readonly resolvedValue?: JsonValue
}

export interface PrototypeComponentBindingDto {
  readonly id: EntityId
  readonly layerId: EntityId
  readonly componentId: string
  readonly componentVersion: string
  readonly propertyMapping: JsonObject
}

export interface PrototypeContentDto {
  readonly pageSpecRevision: VersionRefDto
  readonly exploratory: boolean
  readonly states: readonly PrototypeStateDto[]
  readonly breakpoints: readonly PrototypeBreakpointDto[]
  readonly layers: Readonly<Record<EntityId, PrototypeLayerDto>>
  readonly frames: readonly PrototypeFrameDto[]
  readonly overrides: readonly PrototypeVariantOverrideDto[]
  readonly interactions: readonly PrototypeInteractionDto[]
  readonly fixtures: readonly PrototypeFixtureDto[]
  readonly tokenBindings: readonly PrototypeTokenBindingDto[]
  readonly componentBindings: readonly PrototypeComponentBindingDto[]
  readonly assets: readonly AssetRefDto[]
  readonly traceLinks: readonly TraceLinkDto[]
}

export interface CreatePrototypeInputDto {
  readonly title: string
  readonly pageSpecRevision: VersionRefDto
  readonly exploratory?: boolean
  readonly content?: PrototypeContentDto
}

export type ReviewDecision = 'pending' | 'approved' | 'changesRequested'

export interface ReviewDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly target: VersionRefDto
  readonly decision: ReviewDecision
  readonly summary: string
  readonly requiredReviewerIds: readonly EntityId[]
  readonly decidedBy?: EntityId
  readonly createdByUser?: UserDto
  readonly decidedByUser?: UserDto
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
  readonly decidedAt?: IsoDateTime
}

export interface CreateReviewInputDto {
  readonly target: VersionRefDto
  readonly summary: string
  readonly requiredReviewerIds: readonly EntityId[]
}

export interface DecideReviewInputDto {
  readonly decision: Exclude<ReviewDecision, 'pending'>
  readonly summary: string
}

export interface CommentAnchorDto {
  readonly revision?: VersionRefDto
  readonly revisionId?: EntityId
  readonly blockId?: EntityId
  readonly blueprintNodeId?: EntityId
  readonly stateId?: EntityId
  readonly breakpointId?: EntityId
  readonly layerId?: EntityId
  readonly propertyPath?: string
  readonly normalizedPoint?: { readonly x: number; readonly y: number }
  readonly snapshotAssetId?: EntityId
}

export interface CommentDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly artifactId?: EntityId
  readonly parentId?: EntityId
  readonly target?: VersionRefDto
  readonly body: string
  readonly anchor?: CommentAnchorDto
  readonly resolved: boolean
  readonly createdBy: EntityId
  readonly author?: UserDto
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
}

export interface CreateCommentInputDto {
  readonly body: string
  readonly artifactId?: EntityId
  readonly target?: VersionRefDto
  readonly parentId?: EntityId
  readonly anchor?: CommentAnchorDto
}

export type NotificationKind =
  | 'comment'
  | 'reply'
  | 'review'
  | 'membership'
  | 'artifact'
  | 'run'

export interface NotificationDto {
  readonly id: EntityId
  readonly userId: EntityId
  readonly projectId: EntityId
  readonly kind: NotificationKind
  readonly title: string
  readonly message: string
  readonly targetUrl?: string
  readonly createdAt: IsoDateTime
  readonly readAt?: IsoDateTime
}

export interface AuditEventDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly actorId: EntityId
  readonly action: string
  readonly targetType: string
  readonly targetId: EntityId
  readonly metadata: JsonObject
  readonly createdAt: IsoDateTime
}

export interface PresenceDto {
  readonly projectId: EntityId
  readonly user: UserDto
  readonly artifactId?: EntityId
  readonly state: 'active' | 'idle' | 'offline'
  readonly updatedAt: IsoDateTime
  readonly expiresAt?: IsoDateTime
}

export interface ProjectAuthorizationDto {
  readonly projectId: EntityId
  readonly action: 'view' | 'comment' | 'edit' | 'publish' | 'admin'
  readonly allowed: boolean
  readonly role: ProjectRole
}

export type JsonPatchOperationDto =
  | { readonly op: 'add' | 'replace' | 'test'; readonly path: string; readonly value: JsonValue }
  | { readonly op: 'remove'; readonly path: string }
  | { readonly op: 'move' | 'copy'; readonly from: string; readonly path: string }

export type ProposalOperationDto =
  | { readonly type: 'artifact.patch'; readonly artifactId: EntityId; readonly patch: readonly JsonPatchOperationDto[] }
  | { readonly type: 'artifact.create'; readonly artifact: CreateArtifactInputDto; readonly content: JsonValue }
  | { readonly type: 'artifact.archive'; readonly artifactId: EntityId }
  | { readonly type: 'dependency.upsert'; readonly dependency: ArtifactDependencyDto }
  | { readonly type: 'prototype.layer.add'; readonly layer: PrototypeLayerDto }
  | { readonly type: 'prototype.layer.patch'; readonly layerId: EntityId; readonly patch: readonly JsonPatchOperationDto[] }
  | { readonly type: 'prototype.layer.delete'; readonly layerId: EntityId }
  | { readonly type: 'prototype.state.add'; readonly state: PrototypeStateDto }
  | { readonly type: 'prototype.interaction.add'; readonly interaction: PrototypeInteractionDto }

export type ProposalStatus = 'pending' | 'partiallyApplied' | 'applied' | 'rejected' | 'superseded'

export interface ProposalDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly kind: string
  readonly status: ProposalStatus
  readonly inputManifestId: EntityId
  readonly targetArtifactId?: EntityId
  readonly baseDraftHash?: ContentHash
  readonly operations: readonly ProposalOperationDto[]
  readonly assumptions: readonly string[]
  readonly openQuestions: readonly string[]
  readonly diagnostics: readonly ValidationResultDto[]
  readonly createdByRunId?: EntityId
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
}

export interface ApplyProposalInputDto {
  readonly operationIndexes?: readonly number[]
  readonly conflictResolutions?: Readonly<Record<string, 'current' | 'proposal'>>
}

export interface CreateProposalInputDto {
  readonly kind: string
  readonly targetArtifactId: EntityId
  readonly baseDraftHash: ContentHash
  readonly instruction: string
  readonly inputVersions: readonly VersionRefDto[]
  readonly outputSchemaVersion: string
}

export type WorkflowStepKind =
  | 'requirementsInterview'
  | 'decomposeBlueprint'
  | 'createPageSpecs'
  | 'generatePrototype'
  | 'reviewGate'
  | 'generateImplementation'
  | 'qualityGate'

export interface WorkflowStepDto {
  readonly id: EntityId
  readonly kind: WorkflowStepKind
  readonly name: string
  readonly dependsOnStepIds: readonly EntityId[]
  readonly inputSelectors: readonly JsonObject[]
  readonly requiredApproval: boolean
  readonly configuration: JsonObject
}

export interface WorkflowDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly name: string
  readonly description?: string
  readonly version: number
  readonly enabled: boolean
  readonly steps: readonly WorkflowStepDto[]
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
}

export interface CreateWorkflowInputDto {
  readonly name: string
  readonly description?: string
  readonly steps: readonly WorkflowStepDto[]
}

export type RunKind =
  | 'requirementsInterview'
  | 'decomposeBlueprint'
  | 'createPageSpecs'
  | 'generatePrototype'
  | 'updatePrototype'
  | 'generateImplementation'
  | 'qualityCheck'

export type RunStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled'

export interface RunUsageDto {
  readonly inputTokens: number
  readonly outputTokens: number
  readonly costUsd?: number
  readonly durationMs?: number
}

export interface RunDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly workflowId?: EntityId
  readonly workflowStepId?: EntityId
  readonly kind: RunKind
  readonly status: RunStatus
  readonly inputManifestId: EntityId
  readonly outputProposalIds: readonly EntityId[]
  readonly model?: string
  readonly usage?: RunUsageDto
  readonly error?: ProblemDetailsDto
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
  readonly startedAt?: IsoDateTime
  readonly completedAt?: IsoDateTime
}

export interface StartRunInputDto {
  readonly kind: RunKind
  readonly inputManifestId: EntityId
  readonly workflowId?: EntityId
  readonly workflowStepId?: EntityId
  readonly model?: string
}

export interface RunEventDto {
  readonly id: EntityId
  readonly runId: EntityId
  readonly sequence: number
  readonly type: 'lifecycle' | 'progress' | 'diagnostic' | 'output'
  readonly message?: string
  readonly progress?: number
  readonly data?: JsonValue
  readonly occurredAt: IsoDateTime
}

export interface ManifestSourceDto {
  readonly version: VersionRefDto
  readonly purpose: string
  readonly required: boolean
}

export interface InputManifestDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly sliceId?: EntityId
  readonly jobType: RunKind
  readonly sources: readonly ManifestSourceDto[]
  readonly instruction: string
  readonly constraints: JsonObject
  readonly outputSchemaVersion: string
  readonly contentHash: ContentHash
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
}

export interface CreateInputManifestDto {
  readonly sliceId?: EntityId
  readonly jobType: RunKind
  readonly sources: readonly ManifestSourceDto[]
  readonly instruction: string
  readonly constraints: JsonObject
  readonly outputSchemaVersion: string
}

export interface RenderedFrameRefDto extends AssetRefDto {
  readonly stateId: EntityId
  readonly breakpointId: EntityId
}

export interface WorkbenchBundleDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly pageSpecRevision: VersionRefDto
  readonly prototypeRevision: VersionRefDto
  readonly requirementRevisions: readonly VersionRefDto[]
  readonly blueprintRevision: VersionRefDto
  readonly sceneGraph: AssetRefDto
  readonly renderedFrames: readonly RenderedFrameRefDto[]
  readonly interactionManifest: AssetRefDto
  readonly fixtureBundle: AssetRefDto
  readonly tokenManifest: AssetRefDto
  readonly componentMapping: AssetRefDto
  readonly traceMatrix: AssetRefDto
  readonly acceptanceManifest: AssetRefDto
  readonly contentHash: ContentHash
  readonly createdAt: IsoDateTime
}

export interface CreateWorkbenchBundleInputDto {
  readonly prototypeRevision: VersionRefDto
  readonly allowStale?: boolean
  readonly overrideReason?: string
}

export type TraceTargetKind =
  | 'requirement'
  | 'acceptanceCriterion'
  | 'blueprintNode'
  | 'pageSpec'
  | 'prototypeLayer'
  | 'prototypeInteraction'
  | 'workbenchFile'
  | 'test'
  | 'preview'

export interface TraceEndpointDto {
  readonly kind: TraceTargetKind
  readonly id: EntityId
  readonly version?: VersionRefDto
  readonly path?: string
}

export interface TraceLinkDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly source: TraceEndpointDto
  readonly target: TraceEndpointDto
  readonly relation: 'derivesFrom' | 'satisfies' | 'implements' | 'verifies' | 'renders'
  readonly runId?: EntityId
  readonly createdAt?: IsoDateTime
}

export interface TraceMatrixDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly links: readonly TraceLinkDto[]
  readonly uncoveredRequirementIds: readonly EntityId[]
  readonly generatedAt: IsoDateTime
}

export interface ValidationResultDto {
  readonly code: string
  readonly severity: 'info' | 'warning' | 'error'
  readonly message: string
  readonly path?: string
  readonly sourceId?: EntityId
}

export interface ProblemDetailsDto {
  readonly type: string
  readonly title: string
  readonly status: number
  readonly detail?: string
  readonly instance?: string
  readonly code?: string
  readonly requestId?: string
  readonly errors?: Readonly<Record<string, readonly string[]>>
  readonly extensions?: Readonly<Record<string, JsonValue>>
}
