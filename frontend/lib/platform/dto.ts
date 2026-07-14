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
  readonly revisionNumber?: number
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
export type ProjectGovernanceMode = 'solo' | 'team'

export interface ProjectDto {
  readonly id: EntityId
  readonly name: string
  readonly description?: string
  readonly teamId?: EntityId
  readonly lifecycle: ProjectLifecycle
  readonly governanceMode: ProjectGovernanceMode
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
  readonly governanceMode?: ProjectGovernanceMode
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
  | 'project_brief'
  | 'product_requirements'
  | 'decision_record'
  | 'glossary_policy'
  | 'reference_source'
  | 'change_request'
  | 'requirement_baseline'
  | 'blueprint'
  | 'page_spec'
  | 'prototype'
  | 'prototype_flow'
  | 'fixture_bundle'
  | 'design_system'
  | 'token_set'
  | 'component_registry'
  | 'api_contract'
  | 'data_contract'
  | 'permission_contract'
  | 'workspace'
  | 'test_report'
  | 'quality_report'

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
  readonly artifactKey: string
  readonly title: string
  readonly lifecycle: ProjectLifecycle
  readonly status: ArtifactStatus
  readonly syncStatus: 'current' | 'needs_sync' | 'blocked' | string
  readonly deliveryStatus: string
  readonly activeDraftId?: EntityId
  readonly latestRevisionId?: EntityId
  readonly approvedRevisionId?: EntityId
  readonly version: number
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
  readonly updatedAt: IsoDateTime
  readonly etag: string
}

export interface ArtifactSourceInputDto {
  readonly version: VersionRefDto
  readonly purpose: string
  readonly required: boolean
}

export interface ArtifactSourceDto extends VersionRefDto {
  readonly purpose: string
  readonly required: boolean
}

export interface CreateArtifactInputDto<TContent = JsonValue> {
  readonly kind: ArtifactKind
  readonly artifactKey?: string
  readonly title: string
  readonly schemaVersion?: number
  readonly content?: TContent
  readonly sourceVersions?: readonly ArtifactSourceInputDto[]
}

export interface ArtifactDraftDto<TContent> {
  readonly id: EntityId
  readonly artifactId: EntityId
  readonly baseRevisionId?: EntityId
  readonly sourceVersions: readonly ArtifactSourceDto[]
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
  readonly sourceVersions?: readonly ArtifactSourceDto[]
  readonly schemaVersion?: number
  readonly content: TContent
  readonly contentHash: ContentHash
  readonly status?: ArtifactStatus | string
  readonly changeSource?: 'human' | 'ai_proposal' | 'import' | 'merge' | 'rollback' | 'system'
  readonly changeSummary?: string
  readonly sourceManifestId?: EntityId
  readonly proposalId?: EntityId
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
  readonly approvedAt?: IsoDateTime
}

export interface VersionedArtifactDto<TContent> {
  readonly artifact: ArtifactDto
  readonly draft?: ArtifactDraftDto<TContent>
  readonly latestRevision?: ArtifactRevisionDto<TContent>
  readonly approvedRevision?: ArtifactRevisionDto<TContent>
}

export type DependencyRelation =
  | 'drives'
  | 'satisfied_by'
  | 'contains'
  | 'navigates_to'
  | 'uses'
  | 'calls'
  | 'reads'
  | 'writes'
  | 'requires'
  | 'realized_by'
  | 'implemented_by'
  | 'verified_by'
  | 'derives_from'

export interface ArtifactDependencyDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly source: VersionRefDto
  readonly target: VersionRefDto
  readonly relation: DependencyRelation
  readonly required: boolean
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
}

export interface CreateDependencyInputDto {
  readonly source: VersionRefDto
  readonly target: VersionRefDto
  readonly relation: DependencyRelation
  readonly required: boolean
}

export interface CreateRevisionInputDto {
  readonly changeSummary: string
  readonly changeSource?: 'human' | 'ai_proposal' | 'import' | 'merge' | 'rollback' | 'system'
}

export interface UpdateDraftInputDto<TContent> {
  readonly content: TContent
  readonly sourceVersions?: readonly VersionRefDto[]
}

export interface UpdateArtifactDraftInputDto<TContent> {
  readonly schemaVersion?: number
  readonly content: TContent
  readonly sourceVersions?: readonly ArtifactSourceInputDto[]
}

export type DocumentKind =
  | 'projectBrief'
  | 'requirement'
  | 'pageSplit'
  | 'featureList'
  | 'apiContract'
  | 'backendDevelopment'
  | 'uiPrototype'
  | 'frontendDevelopment'
  | 'decisionLog'
  | 'dataContract'
  | 'permissionContract'
  | 'changeRequest'
  | 'glossaryPolicy'

export interface DocumentBlockDto {
  readonly id: EntityId
  readonly type:
    | 'richText'
    | 'goal'
    | 'actor'
    | 'userJourney'
    | 'requirement'
    | 'acceptanceCriterion'
    | 'businessRule'
    | 'constraint'
    | 'nonFunctionalRequirement'
    | 'metric'
    | 'openQuestion'
    | 'decision'
    | 'sourceReference'
    | 'heading'
    | 'paragraph'
    | 'list'
    | 'table'
    | 'code'
    | 'callout'
  readonly text?: string
  readonly blocking?: boolean
  readonly status?: 'open' | 'answered' | 'resolved' | 'waived'
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
  | 'apiOperation'
  | 'dataEntity'
  | 'permission'
  // Read compatibility for drafts created before the canonical Blueprint IR.
  | 'api'
  | 'dataModel'
  | 'workbenchTarget'

export type BlueprintEdgeKind =
  | 'drives'
  | 'satisfied_by'
  | 'contains'
  | 'navigates_to'
  | 'uses'
  | 'calls'
  | 'reads'
  | 'writes'
  | 'requires'
  | 'realized_by'
  | 'implemented_by'
  | 'verified_by'
  // Read compatibility for drafts created before the canonical Blueprint IR.
  | 'renders'
  | 'implements'

export interface BlueprintNodeDto {
  readonly id: EntityId
  readonly key: string
  readonly kind: BlueprintNodeKind
  readonly title: string
  readonly description?: string
  readonly route?: string
  readonly userGoal?: string
  readonly method?: string
  readonly path?: string
  readonly roles?: readonly string[]
  // Read compatibility for drafts created before Permission roles were canonicalized.
  readonly requiredRoles?: readonly string[]
  readonly role?: string
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
  readonly targetPageNodeId?: EntityId
  // Read compatibility for PageSpecs that stored a PageSpec artifact ID instead
  // of the stable Blueprint page node ID.
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
  readonly traceLinks: readonly PrototypeTraceLinkDto[]
}

export interface CreatePrototypeInputDto {
  readonly title: string
  readonly pageSpecRevision: VersionRefDto
  readonly exploratory?: boolean
  readonly content?: PrototypeContentDto
}

export type ReviewDecision = 'open' | 'approved' | 'changes_requested'

export interface ReviewDecisionDto {
  readonly id: EntityId
  readonly reviewerId: EntityId
  readonly decision: 'approve' | 'request_changes'
  readonly summary: string
  readonly createdAt: IsoDateTime
}

export interface ReviewDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly artifactId: EntityId
  readonly revisionId: EntityId
  readonly contentHash: ContentHash
  readonly status: ReviewDecision
  readonly policy: {
    readonly reviewerIds: readonly EntityId[]
    readonly minimumApprovals: number
    readonly prohibitSelfReview: boolean
    readonly governanceMode?: ProjectGovernanceMode
    readonly soloSelfReviewOwnerId?: EntityId
  }
  readonly requestedBy: EntityId
  readonly requestedAt: IsoDateTime
  readonly closedAt?: IsoDateTime
  readonly decisions: readonly ReviewDecisionDto[]
  readonly etag: string
}

export interface CreateReviewInputDto {
  readonly target: VersionRefDto
  readonly summary: string
  readonly requiredReviewerIds: readonly EntityId[]
  readonly allowSelfApproval?: boolean
}

export interface DecideReviewInputDto {
  readonly decision: 'approved' | 'changesRequested'
  readonly summary: string
  readonly soloReviewConfirmed?: boolean
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
  readonly artifactId: EntityId
  readonly revisionId?: EntityId
  readonly anchor: CommentAnchorDto
  readonly severity: string
  readonly assignedTo?: EntityId
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
  readonly resolvedBy?: EntityId
  readonly resolvedAt?: IsoDateTime
  readonly outdatedAt?: IsoDateTime
  readonly messages: readonly {
    readonly id: EntityId
    readonly parentId?: EntityId
    readonly body: string
    readonly mentions: readonly EntityId[]
    readonly createdBy: EntityId
    readonly createdAt: IsoDateTime
    readonly editedAt?: IsoDateTime
  }[]
  readonly etag: string
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
  readonly action: 'view' | 'comment' | 'edit' | 'review' | 'publish' | 'admin'
  readonly allowed: boolean
  readonly role: ProjectRole
}

export type JsonPatchOperationDto =
  | { readonly op: 'add' | 'replace' | 'test'; readonly path: string; readonly value: JsonValue }
  | { readonly op: 'remove'; readonly path: string }
  | { readonly op: 'move' | 'copy'; readonly from: string; readonly path: string }

export type ProposalDecision = 'pending' | 'accepted' | 'rejected' | 'applied'

export interface ProposalOperationDto {
  readonly id: EntityId
  readonly kind: 'add' | 'replace' | 'remove'
  readonly path: string
  readonly value?: JsonValue
  readonly dependsOn?: readonly EntityId[]
  readonly rationale?: string
  readonly decision: ProposalDecision
  readonly decidedBy?: EntityId
  readonly reason?: string
}

export type ProposalStatus =
  | 'open'
  | 'reviewing'
  | 'ready'
  | 'rejected'
  | 'applied'
  | 'partially_applied'
  | 'stale'

export interface ProposalDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly artifactId: EntityId
  readonly manifest: { readonly id: EntityId; readonly hash: ContentHash }
  readonly baseRevision: VersionRefDto
  readonly payloadHash: ContentHash
  readonly status: ProposalStatus
  readonly operations: readonly ProposalOperationDto[]
  readonly assumptions: readonly string[]
  readonly questions: readonly string[]
  readonly version: number
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
  readonly appliedAt?: IsoDateTime
}

export interface ApplyProposalInputDto {
  readonly version: number
}

export interface DecideProposalInputDto {
  readonly operationId: EntityId
  readonly decision: 'accepted' | 'rejected'
  readonly reason?: string
  readonly version: number
}

export interface CreateProposalInputDto {
  readonly inputManifestId: EntityId
  readonly artifactId: EntityId
  readonly operations: readonly Omit<ProposalOperationDto, 'decision' | 'decidedBy' | 'reason'>[]
  readonly assumptions?: readonly string[]
  readonly questions?: readonly string[]
}

export interface ArtifactGenerationResultDto {
  readonly proposal: ProposalDto
  readonly provider: string
  readonly model: string
  readonly usage?: {
    readonly inputTokens: number
    readonly outputTokens: number
    readonly totalTokens: number
  }
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
  readonly rootBuildManifestId?: EntityId
  readonly derivedFromBuildManifestId?: EntityId
  readonly projectId: EntityId
  readonly pageSpecRevision: VersionRefDto
  readonly prototypeRevision: VersionRefDto
  readonly requirementRevisions: readonly VersionRefDto[]
  readonly contextRevisions?: readonly {
    readonly kind: string
    readonly revision: VersionRefDto
  }[]
  readonly workflowContext?: {
    readonly definition: {
      readonly id: EntityId
      readonly version: number
      readonly hash: ContentHash
    }
    readonly inputManifest: {
      readonly id: EntityId
      readonly projectId: EntityId
      readonly jobType: string
      readonly deliverySliceId?: EntityId
      readonly baseRevision?: VersionRefDto
      readonly sources: readonly {
        readonly ref: VersionRefDto
        readonly purpose: string
      }[]
      readonly constraints: JsonValue
      readonly outputSchemaVersion: string
      readonly createdBy: EntityId
      readonly createdAt: IsoDateTime
      readonly hash: ContentHash
    }
    readonly deliverySliceId?: EntityId
    readonly runScope?: JsonValue
    readonly outputContract?: JsonValue
  }
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

export type DocumentMemberRole = 'owner' | 'assignee' | 'downstreamOwner' | 'reviewer' | 'watcher'

export interface DocumentMemberBindingDto {
  readonly userId: EntityId
  readonly role: DocumentMemberRole
  readonly reason?: string
  readonly assignedBy: EntityId
  readonly assignedAt: IsoDateTime
}

export interface DocumentMemberBindingInputDto {
  readonly userId: EntityId
  readonly role: DocumentMemberRole
  readonly reason?: string
}

export interface DocumentMemberBindingSetDto {
  readonly artifactId: EntityId
  readonly projectId: EntityId
  readonly version: number
  readonly etag: string
  readonly items: readonly DocumentMemberBindingDto[]
  readonly updatedAt?: IsoDateTime
}

export type DocumentGraphEntityType =
  | 'document'
  | 'feature'
  | 'page'
  | 'api'
  | 'data'
  | 'workspace'
  | 'inputManifest'
  | 'outputProposal'
  | 'workflowRun'
  | 'workbenchVersion'
  | 'implementation'
  | 'deployment'

export interface DocumentGraphNodeDto {
  readonly id: EntityId
  readonly entityId: EntityId
  readonly entityType: DocumentGraphEntityType
  readonly artifactKind?: ArtifactKind
  readonly title: string
  readonly status: string
  readonly revision?: VersionRefDto
  readonly memberBindings?: readonly DocumentMemberBindingDto[]
  readonly metadata: JsonObject
  readonly updatedAt: IsoDateTime
}

export interface DocumentGraphEdgeDto {
  readonly id: EntityId
  readonly sourceId: EntityId
  readonly targetId: EntityId
  readonly relation: string
  readonly required: boolean
  readonly metadata: JsonObject
}

export interface DocumentGraphDto {
  readonly projectId: EntityId
  readonly nodes: readonly DocumentGraphNodeDto[]
  readonly edges: readonly DocumentGraphEdgeDto[]
}

export type DownstreamDocumentKind =
  | 'project_brief'
  | 'product_requirements'
  | 'decision_record'
  | 'glossary_policy'
  | 'reference_source'
  | 'change_request'
  | 'api_contract'
  | 'data_contract'
  | 'permission_contract'

export interface GenerateDownstreamDocumentInputDto {
  readonly sourceRevision: VersionRefDto
  readonly targetKind: DownstreamDocumentKind
  readonly targetTitle: string
  readonly targetKey?: string
  readonly instruction: string
  readonly model?: string
}

export type DocumentSyncBackProvenanceKind =
  | 'workspaceRevision'
  | 'implementationProposal'
  | 'buildManifest'
  | 'deployment'

export interface CreateDocumentSyncBackInputDto {
  readonly targetRevision: VersionRefDto
  readonly provenance: {
    readonly kind: DocumentSyncBackProvenanceKind
    readonly id: EntityId
  }
  readonly instruction: string
  readonly model?: string
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

export interface PrototypeTraceLinkDto {
  readonly id: EntityId
  readonly source: TraceEndpointDto
  readonly target: TraceEndpointDto
  readonly relation: 'derivesFrom' | 'satisfies' | 'implements' | 'verifies' | 'renders'
  readonly runId?: EntityId
  readonly createdAt?: IsoDateTime
}

export interface CreateTraceLinkInputDto {
  readonly source: VersionRefDto
  readonly target: VersionRefDto
  readonly relation: DependencyRelation
  readonly metadata?: JsonObject
}

export interface TraceLinkDto extends CreateTraceLinkInputDto {
  readonly id: EntityId
  readonly projectId: EntityId
  readonly metadata: JsonObject
  readonly createdBy: EntityId
  readonly createdAt: IsoDateTime
}

export interface TraceMatrixDto {
  readonly projectId: EntityId
  readonly links: readonly TraceLinkDto[]
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
