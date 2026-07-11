import type { JsonObject } from './dto'

export interface ConversationPageDto<T> {
  readonly items: readonly T[]
  readonly nextCursor?: string
}

export interface ConversationDto {
  readonly id: string
  readonly projectId: string
  readonly title: string
  readonly status: 'active' | 'archived'
  readonly version: number
  readonly etag: string
  readonly createdBy: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly archivedAt?: string
  readonly summaryCheckpointHeadId?: string
}

export interface ConversationMessageDto {
  readonly id: string
  readonly conversationId: string
  readonly sequence: number
  readonly role: 'user' | 'assistant'
  readonly content: string
  readonly proposalId?: string
  readonly createdBy: string
  readonly createdAt: string
}

export type ConversationSummaryCheckpointStatus =
  | 'pending_review'
  | 'approved'
  | 'rejected'
  | 'superseded'

export interface ConversationSummaryCheckpointDto {
  readonly id: string
  readonly projectId: string
  readonly conversationId: string
  readonly previousCheckpointId?: string
  readonly throughMessageId: string
  readonly throughSequence: number
  readonly messageCount: number
  readonly contentBytes: number
  readonly prefixHash: string
  readonly hashAlgorithm: 'conversation-prefix-chain/v1'
  readonly summary: string
  readonly summaryHash: string
  readonly status: ConversationSummaryCheckpointStatus
  readonly version: number
  readonly etag: string
  readonly createdBy: string
  readonly createdAt: string
  readonly reviewedBy?: string
  readonly reviewedAt?: string
  readonly reviewReason?: string
}

export interface CreateConversationSummaryCheckpointDto {
  readonly throughMessageId: string
  readonly summary: string
}

export interface ConversationContextProvenanceDto {
  readonly version: number
  readonly mode: 'legacy_unrecorded' | 'submitted' | 'full_prefix' | 'checkpoint_tail'
  readonly triggerMessageId?: string
  readonly checkpoint?: {
    readonly id: string
    readonly throughMessageId: string
    readonly throughSequence: number
    readonly prefixHash: string
    readonly summaryHash: string
    readonly summary: string
  }
  readonly tail?: {
    readonly fromSequence: number
    readonly toSequence: number
    readonly messageCount: number
    readonly contentBytes: number
    readonly hash: string
  }
  readonly contextHash?: string
  readonly providerInputHash?: string
}

export interface ConversationSummaryCheckpointRequiredExtensionsDto {
  readonly triggerMessageId: string
  readonly triggerSequence: number
  readonly messageCount: number
  readonly messageContentBytes: number
  readonly contextBytes: number
  readonly currentApprovedCheckpointId?: string
  readonly currentThroughSequence?: number
  readonly recommendedThroughMessageId: string
  readonly recommendedThroughSequence: number
  readonly createHref: string
}

export interface ConversationArtifactRefDto {
  readonly artifactId: string
  readonly revisionId: string
  readonly contentHash: string
  readonly anchorId?: string
}

export interface ConversationManifestIntentDto {
  readonly mode: 'use_existing'
  readonly inputManifest: {
    readonly id: string
    readonly hash: string
  }
  readonly purpose: string
}

export interface WorkbenchInstructionDto {
  readonly objective: string
  readonly constraints?: readonly string[]
  readonly expectedRunId?: string
  readonly expectedBundleId?: string
  readonly sliceId?: string
  readonly sliceKey?: string
  readonly sliceTitle?: string
}

export interface WorkbenchTargetHintDto {
  readonly runId: string
  readonly rootBundleId: string
}

export type ConversationIntentKind = 'start_workflow' | 'workbench_instruction'

export interface WorkflowIntentProposalDto {
  readonly id: string
  readonly projectId: string
  readonly conversationId: string
  readonly triggerMessageId: string
  readonly assistantMessageId: string
  readonly kind: ConversationIntentKind
  readonly status: 'pending' | 'accepted' | 'rejected'
  readonly version: number
  readonly etag: string
  readonly suggestedDefinitionVersionId: string
  readonly desiredOutputCapability: 'application'
  readonly scope: JsonObject
  readonly sourceRefs: readonly ConversationArtifactRefDto[]
  readonly manifestIntent: ConversationManifestIntentDto
  readonly workbenchInstruction: WorkbenchInstructionDto
  readonly origin: 'submitted' | 'ai'
  readonly ai?: {
    readonly provider: string
    readonly model: string
    readonly responseId?: string
  }
  readonly conversationContext?: ConversationContextProvenanceDto
  readonly decisionReason?: string
  readonly proposedBy: string
  readonly decidedBy?: string
  readonly createdAt: string
  readonly decidedAt?: string
}

export interface GeneratedWorkflowIntentProposalDto {
  readonly proposal: WorkflowIntentProposalDto
  readonly message: ConversationMessageDto
  readonly provider: string
  readonly model: string
}

export interface CreatedWorkflowIntentProposalDto {
  readonly proposal: WorkflowIntentProposalDto
  readonly message: ConversationMessageDto
}

export interface CreateWorkflowIntentProposalDto {
  readonly triggerMessageId: string
  readonly kind: ConversationIntentKind
  readonly suggestedDefinitionVersionId: string
  readonly scope: JsonObject
  readonly sourceRefs: readonly ConversationArtifactRefDto[]
  readonly manifestIntent: ConversationManifestIntentDto
  readonly workbenchInstruction: WorkbenchInstructionDto
  readonly assistantContent: string
}

export interface GenerateWorkflowIntentProposalDto {
  readonly triggerMessageId: string
  readonly desiredOutputCapability: 'application' | string
  readonly sourceRefs: readonly ConversationArtifactRefDto[]
  readonly manifestIntent: ConversationManifestIntentDto
  readonly workbenchTargetHint?: WorkbenchTargetHintDto
  readonly model?: string
}

export interface ConversationCommandPayloadDto {
  readonly definitionVersionId: string
  readonly desiredOutputCapability: 'application'
  readonly scope: JsonObject
  readonly sourceRefs: readonly ConversationArtifactRefDto[]
  readonly manifestIntent: ConversationManifestIntentDto
  readonly workbench: WorkbenchInstructionDto
  readonly conversationContext?: ConversationContextProvenanceDto
}

export interface ConversationCommandDto {
  readonly id: string
  readonly projectId: string
  readonly conversationId: string
  readonly proposalId: string
  readonly kind: ConversationIntentKind
  readonly status: 'pending' | 'executed' | 'rejected' | 'failed'
  readonly version: number
  readonly etag: string
  readonly payload: ConversationCommandPayloadDto
  readonly result?: JsonObject
  readonly failure?: { readonly code: string; readonly message: string }
  readonly acceptedBy: string
  readonly executionActorId?: string
  readonly executedBy?: string
  readonly rejectedBy?: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly executedAt?: string
  readonly rejectedAt?: string
  readonly failedAt?: string
}

export interface DecideWorkflowIntentProposalResultDto {
  readonly proposal: WorkflowIntentProposalDto
  readonly command?: ConversationCommandDto
}
