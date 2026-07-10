'use client'

import { useMemo, useState, type FormEvent } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import type {
  CollaborationVersionRef,
  ProjectRole,
  ReviewDecision,
} from '@/lib/collaboration/types'
import { cn } from '@/lib/utils'
import {
  Bell,
  CheckCircle2,
  Clock3,
  Loader2,
  LogIn,
  LogOut,
  MessageSquare,
  Plus,
  RefreshCw,
  ShieldCheck,
  UserPlus,
  Users2,
} from 'lucide-react'

type AuthMode = 'signIn' | 'signUp'
type CollaborationTab = 'comments' | 'members' | 'reviews' | 'notifications' | 'audit'

const PROJECT_ROLES: ProjectRole[] = ['owner', 'admin', 'editor', 'commenter', 'viewer']

export function CollaborationCenter() {
  const collaboration = useCollaboration()
  if (!collaboration.session.signedIn) return <AuthenticationPanel />
  return <SignedInCollaboration />
}

function AuthenticationPanel() {
  const {
    loading,
    backendStatus,
    error,
    signIn,
    signUp,
    restoreSession,
  } = useCollaboration()
  const [mode, setMode] = useState<AuthMode>('signIn')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [submitted, setSubmitted] = useState(false)

  const validation = useMemo(() => validateAuthentication({
    mode,
    name,
    email,
    password,
    confirmation,
  }), [confirmation, email, mode, name, password])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSubmitted(true)
    if (Object.keys(validation).length > 0) return
    const ok = mode === 'signIn'
      ? await signIn(email.trim(), password)
      : await signUp(name.trim(), email.trim(), password)
    if (ok) {
      setPassword('')
      setConfirmation('')
    }
  }

  return (
    <section className="rounded-lg border border-border bg-panel p-4">
      <div className="flex flex-wrap items-start gap-3">
        <span className="flex size-9 items-center justify-center rounded-lg bg-primary/15 text-primary-bright">
          <ShieldCheck className="size-4" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-sm font-semibold text-foreground">Identity and collaboration</span>
          <span className="mt-1 block text-[11px] leading-relaxed text-faint-foreground">
            Sign in to the shared platform backend. Projects, membership and reviews are never restored from browser storage.
          </span>
        </span>
        <BackendBadge status={backendStatus} />
      </div>

      <div className="mt-4 grid grid-cols-2 rounded-md border border-border bg-background p-1">
        {([
          ['signIn', 'Sign in'],
          ['signUp', 'Create account'],
        ] as const).map(([id, label]) => (
          <button
            key={id}
            type="button"
            onClick={() => {
              setMode(id)
              setSubmitted(false)
            }}
            className={cn(
              'rounded px-3 py-2 text-[11px] font-medium',
              mode === id ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {label}
          </button>
        ))}
      </div>

      <form className="mt-4 space-y-3" onSubmit={submit} noValidate>
        {mode === 'signUp' && (
          <AuthField
            label="Display name"
            value={name}
            onChange={setName}
            autoComplete="name"
            error={submitted ? validation.name : undefined}
          />
        )}
        <AuthField
          label="Email"
          value={email}
          onChange={setEmail}
          type="email"
          autoComplete="email"
          error={submitted ? validation.email : undefined}
        />
        <AuthField
          label="Password"
          value={password}
          onChange={setPassword}
          type="password"
          autoComplete={mode === 'signIn' ? 'current-password' : 'new-password'}
          hint={mode === 'signUp' ? 'Use at least 10 characters.' : undefined}
          error={submitted ? validation.password : undefined}
        />
        {mode === 'signUp' && (
          <AuthField
            label="Confirm password"
            value={confirmation}
            onChange={setConfirmation}
            type="password"
            autoComplete="new-password"
            error={submitted ? validation.confirmation : undefined}
          />
        )}

        <button
          type="submit"
          disabled={loading}
          className="inline-flex w-full items-center justify-center gap-1.5 rounded-md bg-primary px-3 py-2.5 text-[11px] font-semibold text-primary-foreground hover:bg-primary-bright disabled:cursor-wait disabled:opacity-60"
        >
          {loading ? <Loader2 className="size-3.5 animate-spin" /> : mode === 'signIn' ? <LogIn className="size-3.5" /> : <UserPlus className="size-3.5" />}
          {loading ? 'Contacting platform…' : mode === 'signIn' ? 'Sign in' : 'Create account'}
        </button>
      </form>

      {error && (
        <div role="alert" className="mt-3 rounded-md border border-destructive/30 bg-destructive/10 p-3">
          <p className="text-[11px] text-destructive">{error}</p>
          {backendStatus === 'error' && (
            <button
              type="button"
              onClick={() => void restoreSession()}
              disabled={loading}
              className="mt-2 inline-flex items-center gap-1 rounded border border-border bg-background px-2 py-1 text-[10px] text-foreground disabled:opacity-50"
            >
              <RefreshCw className={cn('size-3', loading && 'animate-spin')} />
              Retry backend connection
            </button>
          )}
        </div>
      )}
    </section>
  )
}

function SignedInCollaboration() {
  const {
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
    unreadCount,
    auditEvents,
    presence,
    error,
    can,
    signOut,
    refresh,
    createProject,
    selectProject,
    addComment,
    resolveComment,
    requestReview,
    decideReview,
    addMember,
    updateMemberRole,
    removeMember,
    markNotification,
  } = useCollaboration()
  const [tab, setTab] = useState<CollaborationTab>('comments')
  const [newProjectName, setNewProjectName] = useState('')
  const [comment, setComment] = useState('')
  const [commentTargetId, setCommentTargetId] = useState('')
  const [memberName, setMemberName] = useState('')
  const [memberEmail, setMemberEmail] = useState('')
  const [memberRole, setMemberRole] = useState<ProjectRole>('viewer')
  const [reviewerId, setReviewerId] = useState('')
  const [reviewSummary, setReviewSummary] = useState('')
  const [reviewTargetId, setReviewTargetId] = useState('')

  if (!session.signedIn) return null
  const selectedReviewTarget = reviewTargets.find((target) => target.revisionId === reviewTargetId)
    ?? reviewTargets[0]
  const selectedCommentTarget = reviewTargets.find((target) => target.revisionId === commentTargetId)
    ?? reviewTargets[0]

  async function submitProject(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = newProjectName.trim()
    if (!name) return
    const id = await createProject(name)
    if (id) setNewProjectName('')
  }

  return (
    <section className="overflow-hidden rounded-lg border border-border bg-panel">
      <header className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3">
        <span className="flex size-8 items-center justify-center rounded-full bg-primary text-[11px] font-semibold text-primary-foreground">
          {initials(session.user.name)}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[12px] font-medium text-foreground">{session.user.name}</span>
          <span className="block truncate text-[10px] text-faint-foreground">{session.user.email}</span>
        </span>
        <BackendBadge status={backendStatus} />
        <span className="flex items-center gap-1 text-[10px] text-success">
          <span className="size-2 rounded-full bg-success" />
          {presence.filter((item) => item.status !== 'offline').length} online
        </span>
        <button type="button" onClick={() => void refresh()} disabled={loading} className="rounded-md border border-border p-1.5 text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-50" aria-label="Refresh collaboration">
          <RefreshCw className={cn('size-3.5', loading && 'animate-spin')} />
        </button>
        <button type="button" onClick={() => void signOut()} disabled={loading} className="rounded-md border border-border p-1.5 text-muted-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-50" aria-label="Sign out">
          <LogOut className="size-3.5" />
        </button>
      </header>

      <div className="grid gap-3 border-b border-border bg-background/40 p-4 md:grid-cols-[minmax(0,1fr)_minmax(260px,0.7fr)]">
        <label className="text-[10px] font-medium text-muted-foreground">
          Shared project
          <select
            value={project?.id ?? ''}
            onChange={(event) => void selectProject(event.target.value)}
            disabled={loading || projects.length === 0}
            className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none focus:border-primary/60 disabled:opacity-50"
          >
            {projects.length === 0 && <option value="">No server projects</option>}
            {projects.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.role}</option>)}
          </select>
        </label>
        <form onSubmit={submitProject} className="flex items-end gap-2">
          <label className="min-w-0 flex-1 text-[10px] font-medium text-muted-foreground">
            New project
            <input
              value={newProjectName}
              onChange={(event) => setNewProjectName(event.target.value)}
              placeholder="Project name"
              maxLength={120}
              className="mt-1.5 h-9 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none focus:border-primary/60"
            />
          </label>
          <button type="submit" disabled={loading || !newProjectName.trim()} className="inline-flex h-9 items-center gap-1 rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50">
            <Plus className="size-3" /> Create
          </button>
        </form>
      </div>

      {error && <p role="alert" className="border-b border-destructive/20 bg-destructive/10 px-4 py-2 text-[10px] text-destructive">{error}</p>}

      {!project ? (
        <div className="p-8 text-center">
          <Users2 className="mx-auto size-7 text-primary-bright" />
          <p className="mt-2 text-sm font-medium text-foreground">Create your first shared project</p>
          <p className="mt-1 text-[11px] text-faint-foreground">The creator becomes the project owner on the backend.</p>
        </div>
      ) : (
        <>
          <nav className="flex overflow-x-auto border-b border-border p-1 scrollbar-thin">
            {([
              ['comments', `Comments ${comments.length}`, MessageSquare],
              ['members', `Members ${members.length}`, Users2],
              ['reviews', `Reviews ${reviews.length}`, CheckCircle2],
              ['notifications', `Notifications ${unreadCount}`, Bell],
              ['audit', `Audit ${auditEvents.length}`, Clock3],
            ] as const).map(([id, label, Icon]) => (
              <button key={id} type="button" onClick={() => setTab(id)} className={cn('flex shrink-0 items-center gap-1.5 rounded-md px-2.5 py-1.5 text-[10px] font-medium', tab === id ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground hover:bg-white/5 hover:text-foreground')}>
                <Icon className="size-3.5" />{label}
              </button>
            ))}
          </nav>

          <div className="max-h-[500px] overflow-y-auto p-4 scrollbar-thin">
            {tab === 'comments' && (
              <CommentsTab
                comments={comments}
                targets={reviewTargets}
                selectedTarget={selectedCommentTarget}
                onTargetChange={setCommentTargetId}
                canComment={can('comment')}
                canResolve={can('edit')}
                comment={comment}
                onCommentChange={setComment}
                onAdd={async () => {
                  if (await addComment(comment, undefined, selectedCommentTarget)) setComment('')
                }}
                onResolve={resolveComment}
              />
            )}
            {tab === 'members' && (
              <MembersTab
                members={members}
                canAdmin={can('admin')}
                name={memberName}
                email={memberEmail}
                role={memberRole}
                onNameChange={setMemberName}
                onEmailChange={setMemberEmail}
                onRoleChange={setMemberRole}
                onAdd={async () => {
                  if (await addMember({ name: memberName, email: memberEmail, role: memberRole })) {
                    setMemberName('')
                    setMemberEmail('')
                  }
                }}
                onRole={updateMemberRole}
                onRemove={removeMember}
              />
            )}
            {tab === 'reviews' && (
              <ReviewsTab
                reviews={reviews}
                targets={reviewTargets}
                selectedTarget={selectedReviewTarget}
                onTargetChange={setReviewTargetId}
                canReview={can('edit')}
                reviewers={members.filter((member) =>
                  member.user.id !== session.user.id &&
                  ['owner', 'admin', 'editor'].includes(member.role),
                )}
                reviewerId={reviewerId}
                summary={reviewSummary}
                onReviewerChange={setReviewerId}
                onSummaryChange={setReviewSummary}
                onSubmit={async () => {
                  if (
                    selectedReviewTarget &&
                    await requestReview(reviewSummary, selectedReviewTarget, [reviewerId])
                  ) {
                    setReviewSummary('')
                  }
                }}
                onDecide={decideReview}
                currentUserId={session.user.id}
              />
            )}
            {tab === 'notifications' && (
              <div className="space-y-2">
                {notifications.length === 0 && <EmptyState text="No notifications." />}
                {notifications.map((notification) => (
                  <button key={notification.id} type="button" onClick={() => void markNotification(notification.id, !notification.readAt)} className={cn('block w-full rounded-md border border-border px-3 py-2 text-left', !notification.readAt && 'bg-primary/5')}>
                    <span className="block text-[10px] font-medium text-foreground">{notification.title}</span>
                    <span className="mt-0.5 block text-[9px] text-muted-foreground">{notification.message}</span>
                  </button>
                ))}
              </div>
            )}
            {tab === 'audit' && (
              <div className="space-y-2">
                {!can('admin') && <EmptyState text="Audit history is available to project owners and admins." />}
                {can('admin') && auditEvents.length === 0 && <EmptyState text="No audit events." />}
                {auditEvents.map((event) => (
                  <div key={event.id} className="rounded-md border border-border px-3 py-2 text-[10px]">
                    <span className="font-medium text-foreground">{event.action}</span>
                    <span className="ml-2 text-faint-foreground">{event.targetType}:{event.targetId}</span>
                  </div>
                ))}
              </div>
            )}
          </div>
        </>
      )}
    </section>
  )
}

function CommentsTab({
  comments,
  targets,
  selectedTarget,
  onTargetChange,
  canComment,
  canResolve,
  comment,
  onCommentChange,
  onAdd,
  onResolve,
}: {
  comments: ReturnType<typeof useCollaboration>['comments']
  targets: CollaborationVersionRef[]
  selectedTarget?: CollaborationVersionRef
  onTargetChange: (revisionId: string) => void
  canComment: boolean
  canResolve: boolean
  comment: string
  onCommentChange: (value: string) => void
  onAdd: () => Promise<void>
  onResolve: (commentId: string, resolved: boolean) => Promise<boolean>
}) {
  return (
    <div className="space-y-2">
      {canComment && (
        <form onSubmit={(event) => { event.preventDefault(); void onAdd() }} className="grid gap-2 sm:grid-cols-[220px_1fr_auto]">
          <select value={selectedTarget?.revisionId ?? ''} onChange={(event) => onTargetChange(event.target.value)} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground"><option value="">Select artifact version</option>{targets.map((target) => <option key={target.revisionId} value={target.revisionId}>{target.title ?? target.artifactId} · r{target.revisionNumber}</option>)}</select>
          <input value={comment} onChange={(event) => onCommentChange(event.target.value)} placeholder="Comment on this exact revision" className="h-9 min-w-0 rounded-md border border-border bg-background px-2 text-[10px] text-foreground outline-none" />
          <button type="submit" disabled={!selectedTarget || !comment.trim()} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50">Post</button>
        </form>
      )}
      {targets.length === 0 && <EmptyState text="Create a versioned artifact before adding formal comments." />}
      {comments.length === 0 && <EmptyState text="No comments yet." />}
      {comments.map((thread) => (
        <div key={thread.id} className="rounded-md border border-border bg-card px-3 py-2">
          <div className="flex items-center gap-2">
            <span className="text-[10px] font-medium text-foreground">{thread.author.name}</span>
            <span className="ml-auto text-[9px] text-faint-foreground">{new Date(thread.createdAt).toLocaleString()}</span>
          </div>
          <p className="mt-1 text-[10px] text-muted-foreground">{thread.body}</p>
          {thread.target && <p className="mt-1 font-mono text-[8px] text-faint-foreground">revision {thread.target.revisionNumber} · {thread.target.contentHash.slice(0, 12)}</p>}
          {thread.replies.map((reply) => <p key={reply.id} className="mt-2 border-l border-border pl-2 text-[9px] text-muted-foreground"><b>{reply.author.name}:</b> {reply.body}</p>)}
          {canResolve && <button type="button" onClick={() => void onResolve(thread.id, !thread.resolvedAt)} className="mt-2 text-[9px] text-primary-bright">{thread.resolvedAt ? 'Reopen' : 'Resolve'}</button>}
        </div>
      ))}
    </div>
  )
}

function MembersTab({
  members,
  canAdmin,
  name,
  email,
  role,
  onNameChange,
  onEmailChange,
  onRoleChange,
  onAdd,
  onRole,
  onRemove,
}: {
  members: ReturnType<typeof useCollaboration>['members']
  canAdmin: boolean
  name: string
  email: string
  role: ProjectRole
  onNameChange: (value: string) => void
  onEmailChange: (value: string) => void
  onRoleChange: (value: ProjectRole) => void
  onAdd: () => Promise<void>
  onRole: (userId: string, role: ProjectRole) => Promise<boolean>
  onRemove: (userId: string) => Promise<boolean>
}) {
  return (
    <div className="space-y-2">
      {canAdmin && (
        <form onSubmit={(event) => { event.preventDefault(); void onAdd() }} className="grid gap-2 rounded-md border border-border bg-card p-3 sm:grid-cols-[1fr_1fr_120px_auto]">
          <input value={name} onChange={(event) => onNameChange(event.target.value)} placeholder="Display name" className="h-8 rounded border border-border bg-background px-2 text-[10px] text-foreground" />
          <input value={email} onChange={(event) => onEmailChange(event.target.value)} type="email" placeholder="Email" className="h-8 rounded border border-border bg-background px-2 text-[10px] text-foreground" />
          <select value={role} onChange={(event) => onRoleChange(event.target.value as ProjectRole)} className="h-8 rounded border border-border bg-background px-2 text-[10px] text-foreground">{PROJECT_ROLES.filter((item) => item !== 'owner').map((item) => <option key={item}>{item}</option>)}</select>
          <button type="submit" disabled={!name.trim() || !validEmail(email)} className="rounded bg-primary px-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-50">Invite</button>
        </form>
      )}
      {members.map((member) => (
        <div key={member.user.id} className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-card px-3 py-2">
          <span className="min-w-0 flex-1"><span className="block truncate text-[10px] font-medium text-foreground">{member.user.name}</span><span className="block truncate text-[9px] text-faint-foreground">{member.user.email}</span></span>
          {canAdmin && member.role !== 'owner' ? (
            <select value={member.role} onChange={(event) => void onRole(member.user.id, event.target.value as ProjectRole)} className="h-7 rounded border border-border bg-background px-1 text-[9px] text-foreground">{PROJECT_ROLES.filter((item) => item !== 'owner').map((item) => <option key={item}>{item}</option>)}</select>
          ) : <span className="rounded bg-primary/10 px-2 py-1 text-[9px] text-primary-bright">{member.role}</span>}
          {canAdmin && member.role !== 'owner' && <button type="button" onClick={() => window.confirm(`Remove ${member.user.name} from this project?`) && void onRemove(member.user.id)} className="text-[9px] text-destructive">Remove</button>}
        </div>
      ))}
    </div>
  )
}

function ReviewsTab({
  reviews,
  targets,
  selectedTarget,
  onTargetChange,
  canReview,
  reviewers,
  reviewerId,
  summary,
  onReviewerChange,
  onSummaryChange,
  onSubmit,
  onDecide,
  currentUserId,
}: {
  reviews: ReturnType<typeof useCollaboration>['reviews']
  targets: CollaborationVersionRef[]
  selectedTarget?: CollaborationVersionRef
  onTargetChange: (revisionId: string) => void
  canReview: boolean
  reviewers: ReturnType<typeof useCollaboration>['members']
  reviewerId: string
  summary: string
  onReviewerChange: (userId: string) => void
  onSummaryChange: (summary: string) => void
  onSubmit: () => Promise<void>
  onDecide: (reviewId: string, decision: ReviewDecision, summary: string) => Promise<boolean>
  currentUserId: string
}) {
  return (
    <div className="space-y-2">
      {canReview && (
        <form onSubmit={(event) => { event.preventDefault(); void onSubmit() }} className="grid gap-2 rounded-md border border-border bg-card p-3 sm:grid-cols-2">
          <label className="text-[9px] text-faint-foreground">Artifact revision<select value={selectedTarget?.revisionId ?? ''} onChange={(event) => onTargetChange(event.target.value)} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground"><option value="">Select a version</option>{targets.map((target) => <option key={target.revisionId} value={target.revisionId}>{target.title ?? target.artifactId} · r{target.revisionNumber}</option>)}</select></label>
          <label className="text-[9px] text-faint-foreground">Required reviewer<select value={reviewerId} onChange={(event) => onReviewerChange(event.target.value)} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground"><option value="">Select reviewer</option>{reviewers.map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name}</option>)}</select></label>
          <textarea value={summary} onChange={(event) => onSummaryChange(event.target.value)} placeholder="Version-level review summary" rows={3} className="rounded border border-border bg-background p-2 text-[10px] text-foreground sm:col-span-2" />
          <button type="submit" disabled={!selectedTarget || !reviewerId || !summary.trim()} className="rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-50 sm:col-span-2">Request version review</button>
        </form>
      )}
      {targets.length === 0 && <EmptyState text="Create a versioned artifact before requesting review." />}
      {reviews.map((review) => (
        <div key={review.id} className="rounded-md border border-border bg-card px-3 py-2">
          <div className="flex items-center gap-2"><span className="rounded bg-primary/10 px-1.5 py-0.5 text-[9px] text-primary-bright">{review.state ?? review.decision}</span><span className="text-[10px] font-medium text-foreground">{review.reviewer.name}</span><span className="ml-auto text-[9px] text-faint-foreground">r{review.target?.revisionNumber ?? '?'}</span></div>
          <p className="mt-1 text-[10px] text-muted-foreground">{review.summary}</p>
          {review.state === 'pending' && canReview && review.requiredReviewerIds?.includes(currentUserId) && <div className="mt-2 flex gap-2"><button type="button" onClick={() => void onDecide(review.id, 'approve', 'Approved after reviewing this exact revision.')} className="text-[9px] text-success">Approve</button><button type="button" onClick={() => { const reason = window.prompt('Describe the required changes')?.trim(); if (reason) void onDecide(review.id, 'request_changes', reason) }} className="text-[9px] text-warning">Request changes</button></div>}
        </div>
      ))}
    </div>
  )
}

function AuthField({
  label,
  value,
  onChange,
  type = 'text',
  autoComplete,
  hint,
  error,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  type?: string
  autoComplete?: string
  hint?: string
  error?: string
}) {
  const id = `collaboration-${label.toLowerCase().replace(/\s+/g, '-')}`
  return (
    <label htmlFor={id} className="block text-[10px] font-medium text-muted-foreground">
      {label}
      <input id={id} value={value} onChange={(event) => onChange(event.target.value)} type={type} autoComplete={autoComplete} aria-invalid={Boolean(error)} aria-describedby={error ? `${id}-error` : undefined} className={cn('mt-1.5 h-9 w-full rounded-md border bg-background px-2.5 text-[11px] text-foreground outline-none focus:border-primary/60', error ? 'border-destructive' : 'border-border')} />
      {error ? <span id={`${id}-error`} className="mt-1 block text-[9px] text-destructive">{error}</span> : hint ? <span className="mt-1 block text-[9px] text-faint-foreground">{hint}</span> : null}
    </label>
  )
}

function BackendBadge({ status }: { status: ReturnType<typeof useCollaboration>['backendStatus'] }) {
  return <span className={cn('rounded-full border px-2 py-1 text-[9px] font-medium', status === 'online' ? 'border-success/30 bg-success/10 text-success' : status === 'error' ? 'border-destructive/30 bg-destructive/10 text-destructive' : 'border-border bg-background text-faint-foreground')}>{status === 'online' ? 'Platform online' : status === 'error' ? 'Platform unavailable' : 'Connecting…'}</span>
}

function EmptyState({ text }: { text: string }) {
  return <p className="rounded-md border border-dashed border-border p-4 text-center text-[10px] text-faint-foreground">{text}</p>
}

function validateAuthentication(input: {
  mode: AuthMode
  name: string
  email: string
  password: string
  confirmation: string
}) {
  const errors: Partial<Record<'name' | 'email' | 'password' | 'confirmation', string>> = {}
  if (input.mode === 'signUp' && input.name.trim().length < 2) errors.name = 'Enter at least 2 characters.'
  if (!validEmail(input.email)) errors.email = 'Enter a valid email address.'
  if (!input.password) errors.password = 'Enter your password.'
  if (input.mode === 'signUp' && Array.from(input.password).length < 10) errors.password = 'Use at least 10 characters.'
  if (input.mode === 'signUp' && input.confirmation !== input.password) errors.confirmation = 'Passwords do not match.'
  return errors
}

function validEmail(value: string) {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim())
}

function initials(name: string) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]?.toUpperCase()).join('') || 'U'
}
