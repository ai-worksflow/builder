import type { PlatformClient } from '../platform/client'
import type {
  ArtifactDto,
  CommentDto,
  NotificationDto,
  PresenceDto,
  ProjectDto,
  ProjectMemberDto,
  ReviewDto,
  SessionDto,
  UserDto,
  VersionRefDto,
} from '../platform/dto'
import {
  PlatformHttpError,
  PlatformNetworkError,
  PlatformProtocolError,
} from '../platform/http'
import type { PlatformDomainEvent } from '../platform/websocket'
import type {
  CollaborationAuditEvent,
  CollaborationNotification,
  CollaborationProject,
  CollaborationSessionStatus,
  CollaborationUser,
  CollaborationVersionRef,
  CommentReply,
  CommentThread,
  ProjectAction,
  ProjectMember,
  ProjectPresence,
  ProjectReview,
  ProjectRole,
  ReviewDecision,
} from './types'

export interface CollaborationPlatformClient {
  readonly http: PlatformClient['http']
  readonly data: PlatformClient['data']
  readonly flow: PlatformClient['flow']
  readonly conversation: PlatformClient['conversation']
  readonly session: Pick<PlatformClient['session'], 'get' | 'signUp' | 'signIn' | 'signOut'>
  readonly projects: Pick<
    PlatformClient['projects'],
    'list' | 'get' | 'create' | 'update' | 'remove' | 'authorize'
  >
  readonly members: Pick<PlatformClient['members'], 'list' | 'add' | 'update' | 'remove' | 'invite'>
  readonly artifacts: Pick<
    PlatformClient['artifacts'],
    'list' | 'getRevision' | 'listRevisions' | 'listDependencies' | 'createDependency' | 'compileRequirementBaseline' | 'reviewGate'
  >
  readonly documents: PlatformClient['documents']
  readonly blueprints: PlatformClient['blueprints']
  readonly pageSpecs: PlatformClient['pageSpecs']
  readonly prototypes: PlatformClient['prototypes']
  readonly proposals: PlatformClient['proposals']
  readonly manifests: PlatformClient['manifests']
  readonly traces: PlatformClient['traces']
  readonly comments: Pick<PlatformClient['comments'], 'listProject' | 'createProject' | 'resolve'>
  readonly reviews: Pick<PlatformClient['reviews'], 'list' | 'create' | 'decide'>
  readonly notifications: Pick<PlatformClient['notifications'], 'list' | 'mark'>
  readonly audit: Pick<PlatformClient['audit'], 'list'>
  readonly presence: Pick<PlatformClient['presence'], 'list' | 'heartbeat'>
  readonly websocket: Pick<
    PlatformClient['websocket'],
    'subscribeProject' | 'subscribeRun' | 'connect' | 'disconnect'
  >
}

export interface CollaborationProjectSnapshot {
  readonly project: CollaborationProject
  readonly members: readonly ProjectMember[]
  readonly comments: readonly CommentThread[]
  readonly reviews: readonly ProjectReview[]
  readonly notifications: readonly CollaborationNotification[]
  readonly auditEvents: readonly CollaborationAuditEvent[]
  readonly presence: readonly ProjectPresence[]
  readonly reviewTargets: readonly CollaborationVersionRef[]
}

export type CollaborationInvalidation =
  | 'project'
  | 'members'
  | 'comments'
  | 'reviews'
  | 'notifications'
  | 'artifacts'
  | 'runs'

function legacyUser(user: UserDto): CollaborationUser {
  return {
    id: user.id,
    name: user.displayName,
    email: user.email,
    createdAt: user.createdAt,
  }
}

function unknownUser(userId: string): CollaborationUser {
  return {
    id: userId,
    name: 'Unknown member',
    email: '',
    createdAt: '',
  }
}

function sessionStatus(session: SessionDto): CollaborationSessionStatus {
  if (session.state !== 'authenticated') return { signedIn: false }
  if (!session.user || !session.expiresAt) {
    throw new PlatformProtocolError('The authenticated session is missing its user or expiry.')
  }
  return {
    signedIn: true,
    user: legacyUser(session.user),
    expiresAt: session.expiresAt,
  }
}

function projectRecord(project: ProjectDto): CollaborationProject {
  return {
    id: project.id,
    name: project.name,
    createdAt: project.createdAt,
    updatedAt: project.updatedAt,
    memberCount: project.memberCount ?? 0,
    role: project.currentUserRole,
    etag: project.etag,
  }
}

function memberRecord(member: ProjectMemberDto): ProjectMember {
  return {
    user: legacyUser(member.user),
    role: member.role,
    joinedAt: member.joinedAt,
    invitedBy: member.invitedBy,
    etag: member.etag,
  }
}

function commentAuthor(
  userId: string,
  members: ReadonlyMap<string, CollaborationUser>,
) {
  return members.get(userId) ?? unknownUser(userId)
}

function commentThreads(
  comments: readonly CommentDto[],
  members: ReadonlyMap<string, CollaborationUser>,
) {
  return comments.map<CommentThread>((comment) => {
    const root = comment.messages.find((message) => !message.parentId) ?? comment.messages[0]
    const anchored = comment.anchor?.revision
    const target = comment.revisionId && anchored &&
      anchored.artifactId === comment.artifactId && anchored.revisionId === comment.revisionId
      ? anchored
      : undefined
    return {
      id: comment.id,
      projectId: comment.projectId,
      threadId: comment.id,
      body: root?.body ?? '',
      author: commentAuthor(root?.createdBy ?? comment.createdBy, members),
      target,
      createdAt: root?.createdAt ?? comment.createdAt,
      resolvedAt: comment.resolvedAt,
      resolvedBy: comment.resolvedBy
        ? commentAuthor(comment.resolvedBy, members)
        : undefined,
      etag: comment.etag,
      replies: comment.messages
        .filter((message) => message.id !== root?.id)
        .map<CommentReply>((reply) => ({
          id: reply.id,
          projectId: comment.projectId,
          threadId: comment.id,
          parentId: reply.parentId ?? root?.id ?? comment.id,
          body: reply.body,
          author: commentAuthor(reply.createdBy, members),
          createdAt: reply.createdAt,
        })),
    }
  })
}

function reviewRecord(
  review: ReviewDto,
  members: ReadonlyMap<string, CollaborationUser>,
): ProjectReview {
  const latestDecision = review.decisions.at(-1)
  const reviewerId = latestDecision?.reviewerId ?? review.policy.reviewerIds[0] ?? review.requestedBy
  const reviewer = members.get(reviewerId) ?? unknownUser(reviewerId)
  return {
    id: review.id,
    projectId: review.projectId,
    decision: latestDecision?.decision,
    state: review.status === 'changes_requested'
      ? 'changesRequested'
      : review.status === 'approved' ? 'approved' : 'pending',
    summary: latestDecision?.summary ?? 'Review requested for this immutable revision.',
    requiredReviewerIds: review.policy.reviewerIds,
    reviewer,
    target: {
      artifactId: review.artifactId,
      revisionId: review.revisionId,
      contentHash: review.contentHash,
    },
    createdAt: review.requestedAt,
    etag: review.etag,
  }
}

function notificationRecord(notification: NotificationDto): CollaborationNotification {
  return {
    id: notification.id,
    userId: notification.userId,
    projectId: notification.projectId,
    kind: notification.kind,
    title: notification.title,
    message: notification.message,
    targetUrl: notification.targetUrl,
    createdAt: notification.createdAt,
    readAt: notification.readAt,
  }
}

function presenceRecord(presence: PresenceDto): ProjectPresence {
  return {
    projectId: presence.projectId,
    user: legacyUser(presence.user),
    status: presence.state,
    updatedAt: presence.updatedAt,
    expiresAt: presence.expiresAt ?? presence.updatedAt,
  }
}

function versionTarget(artifact: ArtifactDto, version: VersionRefDto): CollaborationVersionRef {
  return {
    ...version,
    title: artifact.title,
  }
}

function exactVersionRef(version: VersionRefDto): VersionRefDto {
  return {
    artifactId: version.artifactId,
    revisionId: version.revisionId,
    contentHash: version.contentHash,
    ...(version.anchorId ? { anchorId: version.anchorId } : {}),
  }
}

export function collaborationErrorMessage(error: unknown, fallback: string) {
  if (error instanceof PlatformNetworkError) {
    return 'The collaboration backend is unavailable. Check the Go service and try again.'
  }
  if (error instanceof PlatformHttpError) {
    if (error.status === 401) return 'Your session has expired. Sign in again.'
    if (error.status === 403) return 'Your project role does not allow this action.'
    if (error.status === 409) return error.problem.detail ?? 'This item changed on the server. Refresh and retry.'
    return error.problem.detail ?? error.problem.title
  }
  return error instanceof Error ? error.message : fallback
}

export function collaborationBackendUnavailable(error: unknown) {
  return error instanceof PlatformNetworkError
}

export class PlatformCollaborationGateway {
  readonly client: CollaborationPlatformClient

  constructor(client: CollaborationPlatformClient) {
    this.client = client
  }

  async restoreSession() {
    return sessionStatus((await this.client.session.get()).data)
  }

  async signUp(displayName: string, email: string, password: string) {
    return sessionStatus((await this.client.session.signUp({ displayName, email, password })).data)
  }

  async signIn(email: string, password: string) {
    return sessionStatus((await this.client.session.signIn({ email, password })).data)
  }

  async signOut() {
    await this.client.session.signOut()
    return { signedIn: false } as const
  }

  async listProjects() {
    const response = await this.client.projects.list({ limit: 100 })
    return response.data.items.map(projectRecord)
  }

  async createProject(name: string, description?: string) {
    const response = await this.client.projects.create({ name, description })
    if (response.data.currentUserRole !== 'owner') {
      throw new PlatformProtocolError('A newly created project must return its creator as owner.')
    }
    return projectRecord(response.data)
  }

  async renameProject(project: CollaborationProject, name: string) {
    if (!project.etag) throw new PlatformProtocolError('Refresh the project before renaming it.')
    const response = await this.client.projects.update(
      project.id,
      { name },
      { ifMatch: project.etag, idempotencyKey: true },
    )
    return projectRecord(response.data)
  }

  async archiveProject(project: CollaborationProject) {
    if (!project.etag) throw new PlatformProtocolError('Refresh the project before archiving it.')
    await this.client.projects.remove(project.id, {
      ifMatch: project.etag,
      idempotencyKey: true,
    })
  }

  async authorize(projectId: string, action: ProjectAction) {
    const authorization = (await this.client.projects.authorize(projectId, action)).data
    return authorization.allowed
  }

  async loadProject(projectId: string): Promise<CollaborationProjectSnapshot> {
    const [projectResult, memberResult, commentResult, reviewResult, notificationResult, artifactResult, presenceResult] =
      await Promise.all([
        this.client.projects.get(projectId),
        this.client.members.list(projectId, { limit: 100 }),
        this.client.comments.listProject(projectId, { limit: 200 }),
        this.client.reviews.list(projectId, undefined, { limit: 100 }),
        this.client.notifications.list(projectId, { limit: 100 }),
        this.client.artifacts.list(projectId, {}, { limit: 200 }),
        this.client.presence.list(projectId, { limit: 100 }),
      ])
    const project = projectRecord(projectResult.data)
    const members = memberResult.data.items.map(memberRecord)
    const memberUsers = new Map(members.map((member) => [member.user.id, member.user]))
    const auditEvents = project.role === 'owner' || project.role === 'admin'
      ? (await this.client.audit.list(projectId, { limit: 100 })).data.items.map((event) => ({
          id: event.id,
          projectId: event.projectId,
          actorId: event.actorId,
          action: event.action,
          targetType: event.targetType,
          targetId: event.targetId,
          metadata: event.metadata,
          createdAt: event.createdAt,
        }))
      : []
    const targetRevisions = await Promise.all(artifactResult.data.items.map(async (artifact) => {
      if (!artifact.latestRevisionId) return undefined
      const revision = (await this.client.artifacts.getRevision(artifact.latestRevisionId)).data
      if (revision.artifactId !== artifact.id) {
        throw new PlatformProtocolError('The latest revision does not belong to its listed artifact.')
      }
      return versionTarget(artifact, {
        artifactId: artifact.id,
        revisionId: revision.id,
        revisionNumber: revision.revisionNumber,
        contentHash: revision.contentHash,
      })
    }))
    const reviewTargets = targetRevisions.filter(
      (target): target is CollaborationVersionRef => Boolean(target),
    )

    return {
      project: { ...project, memberCount: members.length },
      members,
      comments: commentThreads(commentResult.data.items, memberUsers),
      reviews: reviewResult.data.items.map((review) => reviewRecord(review, memberUsers)),
      notifications: notificationResult.data.items.map(notificationRecord),
      auditEvents,
      presence: presenceResult.data.items.map(presenceRecord),
      reviewTargets,
    }
  }

  async addMember(
    projectId: string,
    input: { readonly name: string; readonly email: string; readonly role: ProjectRole },
  ) {
    try {
      return memberRecord((await this.client.members.add(projectId, {
        displayName: input.name,
        email: input.email,
        role: input.role,
      })).data)
    } catch (error) {
      if (!(error instanceof PlatformHttpError) || error.status !== 404) throw error
      return (await this.client.members.invite(projectId, {
        email: input.email,
        role: input.role,
      })).data
    }
  }

  async updateMemberRole(projectId: string, userId: string, role: ProjectRole, etag: string) {
    return memberRecord((await this.client.members.update(
      projectId,
      userId,
      { role },
      { ifMatch: etag },
    )).data)
  }

  async removeMember(projectId: string, userId: string, etag: string) {
    await this.client.members.remove(projectId, userId, { ifMatch: etag })
  }

  async addComment(
    projectId: string,
    body: string,
    target: VersionRefDto,
    parentId?: string,
  ) {
    const pinnedTarget = exactVersionRef(target)
    await this.client.comments.createProject(projectId, {
      body,
      parentId,
      artifactId: pinnedTarget.artifactId,
      target: pinnedTarget,
      anchor: { revision: pinnedTarget, revisionId: pinnedTarget.revisionId },
    })
  }

  async resolveComment(commentId: string, resolved: boolean, etag: string) {
    await this.client.comments.resolve(commentId, resolved, { ifMatch: etag })
  }

  async requestReview(
    projectId: string,
    target: VersionRefDto,
    summary: string,
    requiredReviewerIds: readonly string[],
  ) {
    const pinnedTarget = exactVersionRef(target)
    return this.client.reviews.create(projectId, {
      target: pinnedTarget,
      summary,
      requiredReviewerIds,
    })
  }

  async decideReview(reviewId: string, decision: ReviewDecision, summary: string, etag: string) {
    return this.client.reviews.decide(reviewId, {
      decision: decision === 'approve' ? 'approved' : 'changesRequested',
      summary,
    }, { ifMatch: etag })
  }

  async markNotification(notificationId: string, read: boolean) {
    await this.client.notifications.mark(notificationId, read, {})
  }

  async heartbeat(projectId: string) {
    return presenceRecord((await this.client.presence.heartbeat(projectId)).data)
  }

  watchProject(
    projectId: string,
    onInvalidate: (scope: CollaborationInvalidation) => void,
    onPresence: (presence: ProjectPresence) => void,
  ) {
    const unsubscribe = this.client.websocket.subscribeProject(projectId, (event) => {
      if (event.type === 'presence.updated') {
        onPresence(presenceRecord(event.payload))
        return
      }
      const scope = invalidationForEvent(event)
      if (scope) onInvalidate(scope)
    })
    this.client.websocket.connect()
    return unsubscribe
  }

  disconnectRealtime() {
    this.client.websocket.disconnect()
  }
}

function invalidationForEvent(event: PlatformDomainEvent): CollaborationInvalidation | null {
  if (event.type === 'project.updated') return 'project'
  if (event.type === 'member.updated') return 'members'
  if (event.type === 'comment.created' || event.type === 'comment.updated') return 'comments'
  if (event.type === 'review.updated') return 'reviews'
  if (event.type === 'notification.updated') return 'notifications'
  if (
    event.type === 'artifact.updated' ||
    event.type === 'revision.created' ||
    event.type === 'document.updated' ||
    event.type === 'blueprint.updated' ||
    event.type === 'pageSpec.updated' ||
    event.type === 'prototype.updated'
  ) return 'artifacts'
  if (event.type === 'run.updated' || event.type === 'run.event') return 'runs'
  return null
}
