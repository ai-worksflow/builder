export const PROJECT_ROLES = [
  'owner',
  'admin',
  'editor',
  'commenter',
  'viewer',
] as const

export type ProjectRole = (typeof PROJECT_ROLES)[number]

export const PROJECT_ACTIONS = [
  'view',
  'comment',
  'edit',
  'publish',
  'admin',
] as const

export type ProjectAction = (typeof PROJECT_ACTIONS)[number]

export interface CollaborationUser {
  readonly id: string
  readonly name: string
  readonly email: string
  readonly createdAt: string
}

export type CollaborationSessionStatus =
  | { readonly signedIn: false }
  | {
      readonly signedIn: true
      readonly user: CollaborationUser
      readonly expiresAt: string
    }

export interface ProjectMember {
  readonly user: CollaborationUser
  readonly role: ProjectRole
  readonly joinedAt: string
  readonly invitedBy?: string
  readonly etag?: string
}

export interface CollaborationProject {
  readonly id: string
  readonly name: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly memberCount: number
  readonly role: ProjectRole
  readonly etag?: string
}

export interface CommentReply {
  readonly id: string
  readonly projectId: string
  readonly threadId: string
  readonly parentId: string
  readonly body: string
  readonly author: CollaborationUser
  readonly createdAt: string
}

export interface CommentThread {
  readonly id: string
  readonly projectId: string
  readonly threadId: string
  readonly body: string
  readonly author: CollaborationUser
  readonly target?: CollaborationVersionRef
  readonly createdAt: string
  readonly resolvedAt?: string
  readonly resolvedBy?: CollaborationUser
  readonly replies: readonly CommentReply[]
}

export type ReviewDecision = 'approve' | 'request_changes'

export interface ProjectReview {
  readonly id: string
  readonly projectId: string
  readonly decision: ReviewDecision
  readonly state?: 'pending' | 'approved' | 'changesRequested'
  readonly summary: string
  readonly requiredReviewerIds?: readonly string[]
  readonly reviewer: CollaborationUser
  readonly target?: CollaborationVersionRef
  readonly createdAt: string
}

export interface CollaborationVersionRef {
  readonly artifactId: string
  readonly revisionId: string
  readonly revisionNumber: number
  readonly contentHash: string
  readonly title?: string
}

export type NotificationKind =
  | 'comment'
  | 'reply'
  | 'review'
  | 'membership'
  | 'artifact'
  | 'run'

export interface CollaborationNotification {
  readonly id: string
  readonly userId: string
  readonly projectId: string
  readonly kind: NotificationKind
  readonly title: string
  readonly message: string
  readonly targetUrl?: string
  readonly createdAt: string
  readonly readAt?: string
}

export interface CollaborationAuditEvent {
  readonly id: string
  readonly projectId: string
  readonly actorId: string
  readonly action: string
  readonly targetType: string
  readonly targetId: string
  readonly metadata: Readonly<Record<string, unknown>>
  readonly createdAt: string
}

export interface ProjectPresence {
  readonly projectId: string
  readonly user: CollaborationUser
  readonly status: 'active' | 'idle' | 'offline'
  readonly updatedAt: string
  readonly expiresAt: string
}

export interface CollaborationDocument {
  readonly projectId: string
  readonly documentId: string
  readonly revision: number
  readonly content: string
  readonly updatedAt?: string
  readonly updatedBy?: CollaborationUser
}
