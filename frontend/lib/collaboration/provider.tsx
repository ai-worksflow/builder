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
import { PlatformClient } from '@/lib/platform/client'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import { allowsSoloSelfApprovalRequest } from '@/lib/worksflow/project-governance'
import {
  collaborationBackendUnavailable,
  collaborationErrorMessage,
  PlatformCollaborationGateway,
  type CollaborationPlatformClient,
} from './platform-adapter'
import type {
  CollaborationAuditEvent,
  CollaborationDocument,
  CollaborationNotification,
  CollaborationProject,
  CollaborationSessionStatus,
  CollaborationVersionRef,
  CommentThread,
  ProjectAction,
  ProjectGovernanceMode,
  ProjectMember,
  ProjectPresence,
  ProjectReview,
  ProjectRole,
  ReviewDecision,
} from './types'

const ROLE_ACTIONS: Record<ProjectRole, ReadonlySet<ProjectAction>> = {
  owner: new Set(['view', 'comment', 'edit', 'review', 'publish', 'admin']),
  admin: new Set(['view', 'comment', 'edit', 'review', 'publish', 'admin']),
  editor: new Set(['view', 'comment', 'edit', 'review']),
  commenter: new Set(['view', 'comment']),
  viewer: new Set(['view']),
}

export type CollaborationBackendStatus = 'connecting' | 'online' | 'error'

interface CollaborationContextState {
  platformClient: CollaborationPlatformClient
  loading: boolean
  backendStatus: CollaborationBackendStatus
  session: CollaborationSessionStatus
  projects: CollaborationProject[]
  project: CollaborationProject | null
  members: ProjectMember[]
  comments: CommentThread[]
  reviews: ProjectReview[]
  reviewTargets: CollaborationVersionRef[]
  notifications: CollaborationNotification[]
  unreadCount: number
  auditEvents: CollaborationAuditEvent[]
  presence: ProjectPresence[]
  document: CollaborationDocument | null
  documentLoading: boolean
  documentSaving: boolean
  documentConflict: CollaborationDocumentConflict | null
  error: string | null
  can: (action: ProjectAction) => boolean
  authorize: (action: ProjectAction) => Promise<boolean>
  signUp: (name: string, email: string, password: string) => Promise<boolean>
  signIn: (email: string, password: string) => Promise<boolean>
  signOut: () => Promise<void>
  restoreSession: () => Promise<void>
  refresh: () => Promise<void>
  createProject: (name: string, description?: string) => Promise<string | null>
  renameProject: (projectId: string, name: string) => Promise<boolean>
  updateProjectGovernanceMode: (governanceMode: ProjectGovernanceMode) => Promise<boolean>
  archiveProject: (projectId: string) => Promise<boolean>
  selectProject: (projectId: string) => Promise<boolean>
  addComment: (
    body: string,
    parentId?: string,
    target?: CollaborationVersionRef,
  ) => Promise<boolean>
  resolveComment: (commentId: string, resolved: boolean) => Promise<boolean>
  addReview: (
    decision: ReviewDecision,
    summary: string,
    target?: CollaborationVersionRef,
  ) => Promise<boolean>
  requestReview: (
    summary: string,
    target: CollaborationVersionRef,
    requiredReviewerIds: readonly string[],
  ) => Promise<boolean>
  decideReview: (
    reviewId: string,
    decision: ReviewDecision,
    summary: string,
    soloReviewConfirmed?: boolean,
  ) => Promise<boolean>
  addMember: (input: { name: string; email: string; role: ProjectRole }) => Promise<boolean>
  updateMemberRole: (userId: string, role: ProjectRole) => Promise<boolean>
  removeMember: (userId: string) => Promise<boolean>
  markNotification: (notificationId: string, read: boolean) => Promise<boolean>
  loadDocument: (documentId: string) => Promise<CollaborationDocument | null>
  updateDocument: (
    documentId: string,
    baseRevision: number,
    content: string,
  ) => Promise<CollaborationDocument | null>
  adoptServerDocument: () => CollaborationDocument | null
  retryDocumentUpdate: (content?: string) => Promise<CollaborationDocument | null>
}

export interface CollaborationDocumentConflict {
  readonly message: string
  readonly attemptedContent: string
  readonly serverDocument: CollaborationDocument
}

const CollaborationContext = createContext<CollaborationContextState | null>(null)

let browserPlatformClient: PlatformClient | undefined

function defaultPlatformClient() {
  if (typeof window === 'undefined') return new PlatformClient()
  browserPlatformClient ??= new PlatformClient()
  return browserPlatformClient
}

export function CollaborationProvider({
  children,
  client,
}: {
  children: ReactNode
  client?: CollaborationPlatformClient
}) {
  const { t } = useI18n()
  const {
    selectedProductProjectId,
    selectPlatformProject,
  } = useWorksflow()
  const defaultClientRef = useRef<PlatformClient | null>(null)
  if (!client && !defaultClientRef.current) defaultClientRef.current = defaultPlatformClient()
  const activeClient = client ?? defaultClientRef.current!
  const gateway = useMemo(() => new PlatformCollaborationGateway(activeClient), [activeClient])
  const selectPlatformProjectRef = useRef(selectPlatformProject)
  selectPlatformProjectRef.current = selectPlatformProject

  const [loading, setLoading] = useState(true)
  const [backendStatus, setBackendStatus] = useState<CollaborationBackendStatus>('connecting')
  const [session, setSession] = useState<CollaborationSessionStatus>({ signedIn: false })
  const [projects, setProjects] = useState<CollaborationProject[]>([])
  const [project, setProject] = useState<CollaborationProject | null>(null)
  const [members, setMembers] = useState<ProjectMember[]>([])
  const [comments, setComments] = useState<CommentThread[]>([])
  const [reviews, setReviews] = useState<ProjectReview[]>([])
  const [reviewTargets, setReviewTargets] = useState<CollaborationVersionRef[]>([])
  const [notifications, setNotifications] = useState<CollaborationNotification[]>([])
  const [auditEvents, setAuditEvents] = useState<CollaborationAuditEvent[]>([])
  const [presence, setPresence] = useState<ProjectPresence[]>([])
  const [error, setError] = useState<string | null>(null)
  const refreshRequest = useRef(0)
  const refreshRef = useRef<() => Promise<void>>(async () => {})

  const clearProjectState = useCallback(() => {
    refreshRequest.current += 1
    setProject(null)
    setMembers([])
    setComments([])
    setReviews([])
    setReviewTargets([])
    setNotifications([])
    setAuditEvents([])
    setPresence([])
  }, [])

  const applyProjectSnapshot = useCallback(
    async (projectId: string) => {
      const requestId = ++refreshRequest.current
      const snapshot = await gateway.loadProject(projectId)
      if (requestId !== refreshRequest.current) return
      setProject(snapshot.project)
      setMembers([...snapshot.members])
      setComments([...snapshot.comments])
      setReviews([...snapshot.reviews])
      setReviewTargets([...snapshot.reviewTargets])
      setNotifications([...snapshot.notifications])
      setAuditEvents([...snapshot.auditEvents])
      setPresence([...snapshot.presence])
      setBackendStatus('online')
    },
    [gateway],
  )

  const refresh = useCallback(async () => {
    if (!session.signedIn) {
      setProjects([])
      clearProjectState()
      return
    }
    setLoading(true)
    setError(null)
    try {
      const nextProjects = await gateway.listProjects()
      setProjects(nextProjects)
      const selected = nextProjects.find((item) => item.id === selectedProductProjectId)
        ?? nextProjects.find((item) => item.id === project?.id)
        ?? nextProjects[0]
      if (!selected) {
        clearProjectState()
        setBackendStatus('online')
        return
      }
      selectPlatformProjectRef.current({ id: selected.id, name: selected.name })
      await applyProjectSnapshot(selected.id)
    } catch (cause) {
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.refreshFailed')))
    } finally {
      setLoading(false)
    }
  }, [
    applyProjectSnapshot,
    clearProjectState,
    gateway,
    project?.id,
    selectedProductProjectId,
    session.signedIn,
    t,
  ])
  refreshRef.current = refresh

  const restoreSession = useCallback(async () => {
    setLoading(true)
    setBackendStatus('connecting')
    setError(null)
    try {
      const nextSession = await gateway.restoreSession()
      setSession(nextSession)
      setBackendStatus('online')
      if (!nextSession.signedIn) {
        setProjects([])
        clearProjectState()
      }
    } catch (cause) {
      setSession({ signedIn: false })
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.restoreSessionFailed')))
    } finally {
      setLoading(false)
    }
  }, [clearProjectState, gateway, t])

  useEffect(() => {
    void restoreSession()
  }, [restoreSession])

  const sessionUserId = session.signedIn ? session.user.id : null
  useEffect(() => {
    if (!sessionUserId) return
    void refreshRef.current()
  }, [selectedProductProjectId, sessionUserId])

  const realtimeProjectId = project?.id
  useEffect(() => {
    if (!session.signedIn || !realtimeProjectId) return
    let active = true
    let invalidationTimer: number | undefined
    const unsubscribe = gateway.watchProject(
      realtimeProjectId,
      () => {
        window.clearTimeout(invalidationTimer)
        invalidationTimer = window.setTimeout(() => {
          if (active) void refreshRef.current()
        }, 120)
      },
      (nextPresence) => {
        if (!active) return
        setPresence((current) => [
          nextPresence,
          ...current.filter((item) => item.user.id !== nextPresence.user.id),
        ])
      },
    )
    const heartbeat = () => {
      void gateway.heartbeat(realtimeProjectId)
        .then((nextPresence) => {
          if (!active) return
          setPresence((current) => [
            nextPresence,
            ...current.filter((item) => item.user.id !== nextPresence.user.id),
          ])
        })
        .catch((cause) => {
          if (!active) return
          if (collaborationBackendUnavailable(cause)) setBackendStatus('error')
          setError(collaborationErrorMessage(cause, t('runtime.collab.presenceFailed')))
        })
    }
    heartbeat()
    const heartbeatTimer = window.setInterval(heartbeat, 20_000)
    return () => {
      active = false
      unsubscribe()
      window.clearInterval(heartbeatTimer)
      window.clearTimeout(invalidationTimer)
    }
  }, [gateway, realtimeProjectId, session.signedIn, t])

  const can = useCallback(
    (action: ProjectAction) => Boolean(project && ROLE_ACTIONS[project.role].has(action)),
    [project],
  )

  const authorize = useCallback(async (action: ProjectAction) => {
    if (!session.signedIn || !project) {
      setError(t('runtime.collab.signInSelectProject'))
      return false
    }
    try {
      const allowed = await gateway.authorize(project.id, action)
      if (!allowed) setError(t('runtime.collab.roleActionDenied'))
      return allowed
    } catch (cause) {
      if (collaborationBackendUnavailable(cause)) setBackendStatus('error')
      setError(collaborationErrorMessage(cause, t('runtime.collab.actionNotPermitted')))
      return false
    }
  }, [gateway, project, session.signedIn, t])

  const signUp = useCallback(async (name: string, email: string, password: string) => {
    setLoading(true)
    setError(null)
    try {
      const next = await gateway.signUp(name, email, password)
      setSession(next)
      setBackendStatus('online')
      return next.signedIn
    } catch (cause) {
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.registrationFailed')))
      return false
    } finally {
      setLoading(false)
    }
  }, [gateway, t])

  const signIn = useCallback(async (email: string, password: string) => {
    setLoading(true)
    setError(null)
    try {
      const next = await gateway.signIn(email, password)
      setSession(next)
      setBackendStatus('online')
      return next.signedIn
    } catch (cause) {
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.signInFailed')))
      return false
    } finally {
      setLoading(false)
    }
  }, [gateway, t])

  const signOut = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setSession(await gateway.signOut())
      setProjects([])
      clearProjectState()
      gateway.disconnectRealtime()
      setBackendStatus('online')
    } catch (cause) {
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.signOutFailed')))
    } finally {
      setLoading(false)
    }
  }, [clearProjectState, gateway, t])

  const createProject = useCallback(async (name: string, description?: string) => {
    if (!session.signedIn) {
      setError(t('runtime.collab.signInBeforeProject'))
      return null
    }
    setLoading(true)
    setError(null)
    try {
      const created = await gateway.createProject(name, description)
      setProjects((current) => [created, ...current.filter((item) => item.id !== created.id)])
      selectPlatformProjectRef.current({ id: created.id, name: created.name })
      await applyProjectSnapshot(created.id)
      return created.id
    } catch (cause) {
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.createProjectFailed')))
      return null
    } finally {
      setLoading(false)
    }
  }, [applyProjectSnapshot, gateway, session.signedIn, t])

  const selectProject = useCallback(async (projectId: string) => {
    const selected = projects.find((item) => item.id === projectId)
    if (!selected) {
      setError(t('runtime.collab.projectUnavailable'))
      return false
    }
    setLoading(true)
    setError(null)
    try {
      selectPlatformProjectRef.current({ id: selected.id, name: selected.name })
      await applyProjectSnapshot(selected.id)
      return true
    } catch (cause) {
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.openProjectFailed')))
      return false
    } finally {
      setLoading(false)
    }
  }, [applyProjectSnapshot, projects, t])

  const renameProject = useCallback(async (projectId: string, name: string) => {
    const target = projects.find((item) => item.id === projectId)
    if (!target || !name.trim()) return false
    setLoading(true)
    setError(null)
    try {
      if (!(await gateway.authorize(projectId, 'admin'))) {
        setError(t('runtime.collab.renameDenied'))
        return false
      }
      const updated = await gateway.renameProject(target, name.trim())
      setProjects((current) => current.map((item) => item.id === updated.id ? updated : item))
      if (project?.id === updated.id) {
        selectPlatformProjectRef.current({ id: updated.id, name: updated.name })
        await applyProjectSnapshot(updated.id)
      }
      return true
    } catch (cause) {
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.renameProjectFailed')))
      return false
    } finally {
      setLoading(false)
    }
  }, [applyProjectSnapshot, gateway, project?.id, projects, t])

  const updateProjectGovernanceMode = useCallback(async (
    governanceMode: ProjectGovernanceMode,
  ) => {
    if (!project || project.governanceMode === governanceMode) return Boolean(project)
    if (project.role !== 'owner') {
      setError(t('runtime.collab.governanceOwnerRequired'))
      return false
    }
    setLoading(true)
    setError(null)
    try {
      const updated = await gateway.updateGovernanceMode(project, governanceMode)
      setProjects((current) => current.map((item) => item.id === updated.id ? updated : item))
      await applyProjectSnapshot(updated.id)
      return true
    } catch (cause) {
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.governanceUpdateFailed')))
      return false
    } finally {
      setLoading(false)
    }
  }, [applyProjectSnapshot, gateway, project, t])

  const archiveProject = useCallback(async (projectId: string) => {
    const target = projects.find((item) => item.id === projectId)
    if (!target) return false
    setLoading(true)
    setError(null)
    try {
      if (!(await gateway.authorize(projectId, 'admin'))) {
        setError(t('runtime.collab.archiveDenied'))
        return false
      }
      await gateway.archiveProject(target)
      const remaining = projects.filter((item) => item.id !== projectId)
      setProjects(remaining)
      if (project?.id === projectId) {
        const next = remaining[0]
        if (next) {
          selectPlatformProjectRef.current({ id: next.id, name: next.name })
          await applyProjectSnapshot(next.id)
        } else {
          clearProjectState()
        }
      }
      return true
    } catch (cause) {
      setBackendStatus(collaborationBackendUnavailable(cause) ? 'error' : 'online')
      setError(collaborationErrorMessage(cause, t('runtime.collab.archiveProjectFailed')))
      return false
    } finally {
      setLoading(false)
    }
  }, [applyProjectSnapshot, clearProjectState, gateway, project?.id, projects, t])

  const mutate = useCallback(async (
    action: ProjectAction,
    operation: (projectId: string) => Promise<unknown>,
  ) => {
    if (!project || !(await authorize(action))) return false
    setError(null)
    try {
      await operation(project.id)
      await applyProjectSnapshot(project.id)
      return true
    } catch (cause) {
      if (collaborationBackendUnavailable(cause)) setBackendStatus('error')
      setError(collaborationErrorMessage(cause, t('runtime.collab.actionFailed')))
      return false
    }
  }, [applyProjectSnapshot, authorize, project, t])

  const unavailableDocumentOperation = useCallback(async () => {
    setError(t('runtime.collab.documentMigrationUnavailable'))
    return null
  }, [t])

  const value = useMemo<CollaborationContextState>(() => ({
    platformClient: activeClient,
    loading,
    backendStatus,
    session,
    projects,
    project,
    members,
    comments,
    reviews,
    reviewTargets,
    notifications,
    unreadCount: notifications.filter((notification) => !notification.readAt).length,
    auditEvents,
    presence,
    document: null,
    documentLoading: false,
    documentSaving: false,
    documentConflict: null,
    error,
    can,
    authorize,
    signUp,
    signIn,
    signOut,
    restoreSession,
    refresh,
    createProject,
    renameProject,
    updateProjectGovernanceMode,
    archiveProject,
    selectProject,
    addComment: (body, parentId, target = reviewTargets[0]) => {
      if (!target) {
        setError(t('runtime.collab.versionedArtifactBeforeComment'))
        return Promise.resolve(false)
      }
      return mutate('comment', (projectId) => gateway.addComment(projectId, body, target, parentId))
    },
    resolveComment: (commentId, resolved) => {
      const thread = comments.find((item) => item.id === commentId)
      if (!thread?.etag) {
        setError(t('runtime.collab.refreshComments'))
        return Promise.resolve(false)
      }
      return mutate('edit', () => gateway.resolveComment(commentId, resolved, thread.etag))
    },
    addReview: (decision, summary, target = reviewTargets[0]) => {
      void decision
      if (!target) {
        setError(t('runtime.collab.versionedArtifactBeforeReview'))
        return Promise.resolve(false)
      }
      const reviewer = project?.governanceMode === 'solo'
        ? members.find((member) => member.role === 'owner')
        : members.find((member) =>
            (!session.signedIn || member.user.id !== session.user.id) &&
            ['owner', 'admin', 'editor'].includes(member.role),
          )
      if (!reviewer) {
        setError(t('runtime.collab.assignReviewer'))
        return Promise.resolve(false)
      }
      const reviewerIds = [reviewer.user.id]
      return mutate('edit', (projectId) =>
        gateway.requestReview(
          projectId,
          target,
          summary,
          reviewerIds,
          allowsSoloSelfApprovalRequest(project?.governanceMode ?? 'team', members, reviewerIds),
        ),
      )
    },
    requestReview: (summary, target, requiredReviewerIds) => {
      const allowSelfApproval = allowsSoloSelfApprovalRequest(
        project?.governanceMode ?? 'team',
        members,
        requiredReviewerIds,
      )
      return mutate('edit', (projectId) => gateway.requestReview(
        projectId,
        target,
        summary,
        requiredReviewerIds,
        allowSelfApproval,
      ))
    },
    decideReview: (reviewId, decision, summary, soloReviewConfirmed = false) => {
      const review = reviews.find((item) => item.id === reviewId)
      if (!review?.etag) {
        setError(t('runtime.collab.refreshReviews'))
        return Promise.resolve(false)
      }
      return mutate('edit', () => gateway.decideReview(
        reviewId,
        decision,
        summary,
        review.etag,
        soloReviewConfirmed,
      ))
    },
    addMember: (input) => mutate('admin', (projectId) => gateway.addMember(projectId, input)),
    updateMemberRole: (userId, role) => {
      const member = members.find((item) => item.user.id === userId)
      const etag = member?.etag
      if (!etag) {
        setError(t('runtime.collab.refreshMembersRole'))
        return Promise.resolve(false)
      }
      return mutate('admin', (projectId) =>
        gateway.updateMemberRole(projectId, userId, role, etag),
      )
    },
    removeMember: (userId) => {
      const member = members.find((item) => item.user.id === userId)
      const etag = member?.etag
      if (!etag) {
        setError(t('runtime.collab.refreshMembersRemove'))
        return Promise.resolve(false)
      }
      return mutate('admin', (projectId) => gateway.removeMember(projectId, userId, etag))
    },
    markNotification: async (notificationId, read) => {
      try {
        await gateway.markNotification(notificationId, read)
        if (project) await applyProjectSnapshot(project.id)
        return true
      } catch (cause) {
        if (collaborationBackendUnavailable(cause)) setBackendStatus('error')
        setError(collaborationErrorMessage(cause, t('runtime.collab.notificationFailed')))
        return false
      }
    },
    loadDocument: unavailableDocumentOperation,
    updateDocument: unavailableDocumentOperation,
    adoptServerDocument: () => null,
    retryDocumentUpdate: unavailableDocumentOperation,
  }), [
    activeClient,
    applyProjectSnapshot,
    auditEvents,
    authorize,
    archiveProject,
    backendStatus,
    can,
    comments,
    createProject,
    error,
    gateway,
    loading,
    members,
    mutate,
    notifications,
    presence,
    project,
    projects,
    refresh,
    restoreSession,
    renameProject,
    updateProjectGovernanceMode,
    reviewTargets,
    reviews,
    selectProject,
    session,
    signIn,
    signOut,
    signUp,
    t,
    unavailableDocumentOperation,
  ])

  return <CollaborationContext.Provider value={value}>{children}</CollaborationContext.Provider>
}

export function useCollaboration() {
  const value = useContext(CollaborationContext)
  if (!value) throw new Error('useCollaboration must be used within CollaborationProvider')
  return value
}
