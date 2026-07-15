'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { collaborationErrorMessage } from '@/lib/collaboration/platform-adapter'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  ArtifactWorkspaceConflictError,
  documentReviewIssues,
  normalizeDocumentContent,
  reviewGateReadyForRequest,
  type ArtifactDetails,
} from '@/lib/platform/artifact-workspace'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import type {
  ArtifactRevisionDto,
  DocumentMemberBindingInputDto,
  DocumentMemberRole,
  DownstreamDocumentKind,
  DocumentSyncBackProvenanceKind,
  DocumentContentDto,
  ProposalDto,
  VersionRefDto,
} from '@/lib/platform/dto'
import { useWorksflow } from '@/lib/worksflow/store'
import { reviewCandidatesForGovernance } from '@/lib/worksflow/project-governance'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import {
  AlertTriangle,
  Bot,
  Check,
  FileClock,
  FilePlus2,
  GitBranch,
  Link2,
  Loader2,
  MessageSquare,
  Plus,
  RefreshCw,
  Save,
  Send,
  Trash2,
  UserRoundCog,
} from 'lucide-react'

type EditorTab = 'content' | 'versions' | 'proposal' | 'trace' | 'review' | 'collaboration'

export function DocumentEditor() {
  const { locale, t } = useI18n()
  const {
    selectedDocId,
    setSelectedDocId,
  } = useWorksflow()
  const workspace = useArtifactWorkspace()
  const collaboration = useCollaboration()
  const [tab, setTab] = useState<EditorTab>('content')
  const [content, setContent] = useState<DocumentContentDto | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedAt, setSavedAt] = useState<string | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)
  const [conflict, setConflict] = useState(false)
  const [details, setDetails] = useState<ArtifactDetails<DocumentContentDto> | null>(null)
  const [proposalInstruction, setProposalInstruction] = useState(() => t('teamPlatform.editor.defaultProposalInstruction'))
  const [selectedOperations, setSelectedOperations] = useState<Record<string, string[]>>({})
  const [comment, setComment] = useState('')
  const [reviewSummary, setReviewSummary] = useState(() => t('teamPlatform.editor.defaultReviewSummary'))
  const [reviewerId, setReviewerId] = useState('')
  const [memberBindings, setMemberBindings] = useState<DocumentMemberBindingInputDto[]>([])
  const [bindingsEtag, setBindingsEtag] = useState('')
  const [bindingUserId, setBindingUserId] = useState('')
  const [bindingRole, setBindingRole] = useState<DocumentMemberRole>('reviewer')
  const [bindingsLoading, setBindingsLoading] = useState(false)
  const [bindingsDirty, setBindingsDirty] = useState(false)
  const [bindingsRemoteChange, setBindingsRemoteChange] = useState(false)
  const [downstreamKind, setDownstreamKind] = useState<DownstreamDocumentKind>('api_contract')
  const [downstreamTitle, setDownstreamTitle] = useState(() => t('teamPlatform.editor.defaultDownstreamTitle'))
  const [downstreamInstruction, setDownstreamInstruction] = useState(() => t('teamPlatform.editor.defaultDownstreamInstruction'))
  const [syncBackKind, setSyncBackKind] = useState<DocumentSyncBackProvenanceKind>('implementationProposal')
  const [syncBackId, setSyncBackId] = useState('')
  const [syncBackInstruction, setSyncBackInstruction] = useState(() => t('teamPlatform.editor.defaultSyncBackInstruction'))
  const [collaborationNotice, setCollaborationNotice] = useState<string | null>(null)
  const bindingsRequest = useRef(0)
  const bindingsReloadTimer = useRef<number | null>(null)
  const bindingsDirtyRef = useRef(false)

  const resource = workspace.documents.find((item) => item.artifact.id === selectedDocId)
    ?? workspace.documents[0]
  const serverContent = resource?.draft?.content ?? resource?.latestRevision?.content
  const serverEtag = resource?.draft?.etag ?? resource?.artifact.etag
  const dirty = Boolean(content && serverContent && JSON.stringify(content) !== JSON.stringify(serverContent))
  const displayContent = useMemo(
    () => content ? normalizeDocumentContent(content) : null,
    [content],
  )
  const proposals = workspace.proposals.filter((proposal) => proposal.artifactId === resource?.artifact.id)
  const latestVersion = resource?.latestRevision
    ? versionRef(resource.latestRevision)
    : undefined
  const approvedVersion = resource?.approvedRevision
    ? versionRef(resource.approvedRevision)
    : undefined
  const comments = collaboration.comments.filter((thread) => thread.target?.artifactId === resource?.artifact.id)
  const currentUserId = collaboration.session.signedIn ? collaboration.session.user.id : null
  const governanceReviewers = reviewCandidatesForGovernance(
    collaboration.members,
    currentUserId,
    collaboration.project?.governanceMode ?? 'team',
  )
  const effectiveReviewerId = collaboration.project?.governanceMode === 'solo'
    ? governanceReviewers[0]?.user.id ?? ''
    : reviewerId
  const clientGate = displayContent ? documentReviewIssues(displayContent) : []
  const revisionReady = clientGate.length === 0
  const gatePassed = revisionReady && reviewGateReadyForRequest(details?.reviewGate)

  const loadMemberBindings = useCallback(async (discardLocal = false) => {
    const artifactId = resource?.artifact.id
    if (bindingsDirtyRef.current && !discardLocal) {
      setBindingsRemoteChange(true)
      return
    }
    const request = ++bindingsRequest.current
    if (!artifactId) {
      setMemberBindings([])
      setBindingsEtag('')
      setBindingsLoading(false)
      return
    }
    setBindingsLoading(true)
    try {
      const response = await collaboration.platformClient.documents.memberBindings(artifactId)
      if (bindingsRequest.current !== request) return
      setMemberBindings(response.data.items.map(({ userId, role, reason }) => ({ userId, role, reason })))
      setBindingsEtag(response.etag ?? response.data.etag)
      bindingsDirtyRef.current = false
      setBindingsDirty(false)
      setBindingsRemoteChange(false)
    } catch (error) {
      if (bindingsRequest.current === request) {
        setLocalError(collaborationErrorMessage(error, t('teamPlatform.editor.loadBindingsFailed')))
      }
    } finally {
      if (bindingsRequest.current === request) setBindingsLoading(false)
    }
  }, [collaboration.platformClient.documents, resource?.artifact.id, t])

  useEffect(() => {
    if (!resource) {
      setContent(null)
      return
    }
    if (!conflict) setContent(serverContent ?? null)
    if (selectedDocId !== resource.artifact.id) setSelectedDocId(resource.artifact.id)
  }, [conflict, resource, selectedDocId, serverContent, setSelectedDocId])

  useEffect(() => {
    if (!resource) {
      setDetails(null)
      return
    }
    let active = true
    void workspace.loadDetails<DocumentContentDto>(resource.artifact.id)
      .then((next) => { if (active) setDetails(next) })
      .catch((error) => { if (active) setLocalError(message(error, t('teamPlatform.editor.operationFailed'))) })
    return () => { active = false }
  }, [resource?.artifact.id, t, workspace.loadDetails])

  useEffect(() => {
    bindingsDirtyRef.current = false
    setBindingsDirty(false)
    setBindingsRemoteChange(false)
    void loadMemberBindings(true)
    return () => { bindingsRequest.current += 1 }
  }, [loadMemberBindings])

  useEffect(() => {
    if (!collaboration.session.signedIn || !collaboration.project || !resource) return
    const artifactId = resource.artifact.id
    const unsubscribe = collaboration.platformClient.websocket.subscribeProject(collaboration.project.id, (event) => {
      if (event.type !== 'artifact.member_bindings_replaced' || event.payload.artifactId !== artifactId) return
      if (bindingsReloadTimer.current !== null) window.clearTimeout(bindingsReloadTimer.current)
      bindingsReloadTimer.current = window.setTimeout(() => {
        bindingsReloadTimer.current = null
        void loadMemberBindings()
      }, 120)
    })
    collaboration.platformClient.websocket.connect()
    return () => {
      unsubscribe()
      if (bindingsReloadTimer.current !== null) {
        window.clearTimeout(bindingsReloadTimer.current)
        bindingsReloadTimer.current = null
      }
    }
  }, [collaboration.platformClient.websocket, collaboration.project, collaboration.session.signedIn, loadMemberBindings, resource?.artifact.id])

  useEffect(() => {
    if (!bindingUserId && collaboration.members[0]) setBindingUserId(collaboration.members[0].user.id)
  }, [bindingUserId, collaboration.members])

  useEffect(() => {
    if (!resource || !content || !serverEtag || !dirty || conflict) return
    const timer = window.setTimeout(() => {
      setSaving(true)
      setLocalError(null)
      void workspace.saveDocumentDraft(resource.artifact.id, content, serverEtag)
        .then(() => {
          setSavedAt(new Date().toLocaleTimeString(locale))
          setConflict(false)
        })
        .catch((error) => {
          if (error instanceof ArtifactWorkspaceConflictError) setConflict(true)
          setLocalError(message(error, t('teamPlatform.editor.operationFailed')))
        })
        .finally(() => setSaving(false))
    }, 700)
    return () => window.clearTimeout(timer)
  }, [conflict, content, dirty, locale, resource, serverEtag, t, workspace.saveDocumentDraft])

  if (!collaboration.session.signedIn) {
    return <Unavailable title={t('teamPlatform.editor.signInTitle')} detail={t('teamPlatform.editor.signInDetail')} />
  }
  if (workspace.status === 'loading') return <Unavailable loading title={t('teamPlatform.editor.loadingTitle')} detail={t('teamPlatform.editor.loadingDetail')} />
  if (workspace.status === 'error') return <Unavailable title={t('teamPlatform.editor.unavailableTitle')} detail={workspace.error ?? t('teamPlatform.editor.backendNoArtifacts')} onRetry={workspace.refresh} retryLabel={t('common.retry')} />

  if (!resource || !content || !displayContent) {
    return (
      <Unavailable
        title={t('teamPlatform.editor.noArtifactsTitle')}
        detail={t('teamPlatform.editor.noArtifactsDetail')}
        action={t('teamPlatform.dashboard.createProjectBrief')}
        onAction={() => void workspace.createDocument(t('teamPlatform.dashboard.projectBrief'), 'projectBrief')}
      />
    )
  }

  function updateContent(patch: Partial<DocumentContentDto>) {
    setContent((current) => current ? { ...current, ...patch } : current)
    setConflict(false)
  }

  async function createRevision() {
    if (!content || !revisionReady) return
    setSaving(true)
    setLocalError(null)
    try {
      await workspace.createDocumentRevision(resource!.artifact.id, content)
      setDetails(await workspace.loadDetails<DocumentContentDto>(resource!.artifact.id))
    } catch (error) {
      setLocalError(message(error, t('teamPlatform.editor.operationFailed')))
    } finally {
      setSaving(false)
    }
  }

  async function saveMemberBindings() {
    if (!resource || !bindingsEtag || memberBindings.length === 0) return
    setSaving(true)
    setLocalError(null)
    try {
      if (!(await collaboration.authorize('edit'))) return
      const response = await collaboration.platformClient.documents.replaceMemberBindings(
        resource.artifact.id,
        memberBindings,
        { ifMatch: bindingsEtag, idempotencyKey: true },
      )
      setMemberBindings(response.data.items.map(({ userId, role, reason }) => ({ userId, role, reason })))
      setBindingsEtag(response.etag ?? response.data.etag)
      bindingsDirtyRef.current = false
      setBindingsDirty(false)
      setBindingsRemoteChange(false)
      setCollaborationNotice(t('teamPlatform.editor.bindingsSaved'))
    } catch (error) {
      setBindingsRemoteChange(true)
      setLocalError(collaborationErrorMessage(error, t('teamPlatform.editor.saveBindingsFailed')))
    } finally {
      setSaving(false)
    }
  }

  function editMemberBindings(update: (items: DocumentMemberBindingInputDto[]) => DocumentMemberBindingInputDto[]) {
    bindingsDirtyRef.current = true
    setBindingsDirty(true)
    setBindingsRemoteChange(false)
    setMemberBindings(update)
  }

  async function generateDownstreamDocument() {
    if (!resource || !approvedVersion || !collaboration.project) return
    setSaving(true)
    setLocalError(null)
    try {
      if (!(await collaboration.authorize('edit'))) return
      const response = await collaboration.platformClient.documents.generateDownstream(
        collaboration.project.id,
        {
          sourceRevision: approvedVersion,
          targetKind: downstreamKind,
          targetTitle: downstreamTitle,
          instruction: downstreamInstruction,
        },
        { idempotencyKey: true },
      )
      await workspace.refresh()
      setSelectedDocId(response.data.document.artifact.id)
      setTab('proposal')
      setCollaborationNotice(t('teamPlatform.editor.downstreamProposalCreated', { id: response.data.proposal.id }))
    } catch (error) {
      setLocalError(collaborationErrorMessage(error, t('teamPlatform.editor.generateDownstreamFailed')))
    } finally {
      setSaving(false)
    }
  }

  async function createSyncBackProposal() {
    if (!approvedVersion || !collaboration.project || !syncBackId.trim()) return
    setSaving(true)
    setLocalError(null)
    try {
      if (!(await collaboration.authorize('edit'))) return
      const response = await collaboration.platformClient.documents.createSyncBackProposal(
        collaboration.project.id,
        {
          targetRevision: approvedVersion,
          provenance: { kind: syncBackKind, id: syncBackId.trim() },
          instruction: syncBackInstruction,
        },
        { idempotencyKey: true },
      )
      await workspace.refresh()
      setTab('proposal')
      setCollaborationNotice(t('teamPlatform.editor.syncBackProposalCreated', { id: response.data.proposal.id }))
    } catch (error) {
      setLocalError(collaborationErrorMessage(error, t('teamPlatform.editor.syncBackFailed')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full min-h-0 bg-canvas max-lg:flex-col">
      <aside className="w-64 shrink-0 overflow-y-auto border-r border-border bg-panel p-3 scrollbar-thin max-lg:max-h-52 max-lg:w-full max-lg:border-b max-lg:border-r-0">
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs font-semibold text-foreground">{t('teamPlatform.editor.platformDocuments')}</span>
          <button type="button" onClick={() => void workspace.createDocument(t('teamPlatform.editor.untitledRequirement'))} disabled={!collaboration.can('edit')} className="rounded border border-border p-1.5 text-primary-bright disabled:opacity-40" aria-label={t('team.dashboard.createDocument')} title={t('team.dashboard.createDocument')}><FilePlus2 className="size-3.5" /></button>
        </div>
        <div className="mt-3 space-y-1">
          {workspace.documents.map((item) => (
            <button key={item.artifact.id} type="button" onClick={() => setSelectedDocId(item.artifact.id)} className={cn('block w-full rounded-md px-2.5 py-2 text-left', item.artifact.id === resource.artifact.id ? 'bg-primary/15' : 'hover:bg-white/5')}>
              <span className="block truncate text-[11px] font-medium text-foreground">{item.artifact.title}</span>
              <span className="mt-0.5 block text-[9px] text-faint-foreground">{documentKindLabel(item.draft?.content.kind ?? item.latestRevision?.content.kind ?? item.artifact.kind, t)} · {artifactStatusLabel(item.artifact.status, t)}</span>
            </button>
          ))}
        </div>
      </aside>

      <main className="flex min-w-0 flex-1 flex-col">
        <header className="flex flex-wrap items-center gap-3 border-b border-border bg-panel px-4 py-3">
          <span className="min-w-0 flex-1"><span className="block truncate text-sm font-semibold text-foreground">{resource.artifact.title}</span><span className="block text-[9px] text-faint-foreground">{resource.artifact.id} · {t('teamPlatform.editor.draftEtag', { etag: serverEtag ?? t('teamPlatform.common.missing') })}</span></span>
          <span className={cn('inline-flex items-center gap-1 rounded px-2 py-1 text-[9px]', conflict ? 'bg-warning/10 text-warning' : saving ? 'bg-primary/10 text-primary-bright' : dirty ? 'bg-warning/10 text-warning' : 'bg-success/10 text-success')}>{saving ? <Loader2 className="size-3 animate-spin" /> : conflict ? <AlertTriangle className="size-3" /> : <Save className="size-3" />}{conflict ? t('teamPlatform.editor.conflict') : saving ? t('teamPlatform.editor.autosaving') : dirty ? t('teamPlatform.editor.pendingAutosave') : savedAt ? t('teamPlatform.editor.savedAt', { time: savedAt }) : t('teamPlatform.editor.serverDraft')}</span>
          <button type="button" onClick={() => void workspace.refresh()} className="rounded border border-border p-1.5 text-muted-foreground" aria-label={t('teamPlatform.editor.refreshDocument')} title={t('teamPlatform.editor.refreshDocument')}><RefreshCw className="size-3.5" /></button>
        </header>

        {(localError || conflict) && <div role="alert" className="border-b border-warning/30 bg-warning/10 px-4 py-2 text-[10px] text-warning">{localError}{conflict && <button type="button" onClick={() => { setConflict(false); setContent(serverContent ?? content) }} className="ml-3 underline">{t('teamPlatform.editor.useServerDraft')}</button>}</div>}

        <nav className="flex overflow-x-auto border-b border-border bg-panel p-1 scrollbar-thin">
          {([
            ['content', t('editor.tab.content')],
            ['versions', t('teamPlatform.editor.tab.versions', { count: (details?.versions.length ?? 0).toLocaleString(locale) })],
            ['proposal', t('teamPlatform.editor.tab.proposals', { count: proposals.length.toLocaleString(locale) })],
            ['trace', t('teamPlatform.editor.tab.trace', { count: workspace.traces.length.toLocaleString(locale) })],
            ['review', t('teamPlatform.editor.tab.review', { count: comments.length.toLocaleString(locale) })],
            ['collaboration', t('teamPlatform.editor.tab.collaboration', { count: memberBindings.length.toLocaleString(locale) })],
          ] as const).map(([id, label]) => <button key={id} type="button" onClick={() => setTab(id)} className={cn('shrink-0 rounded px-3 py-1.5 text-[10px] font-medium', tab === id ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground')}>{label}</button>)}
        </nav>

        <div className="min-h-0 flex-1 overflow-y-auto p-5 scrollbar-thin max-sm:p-3">
          {tab === 'content' && <ContentEditor content={displayContent} readOnly={!collaboration.can('edit')} onChange={updateContent} />}
          {tab === 'versions' && (
            <section className="mx-auto max-w-4xl space-y-3">
              <GatePanel clientIssues={clientGate} serverGate={details?.reviewGate} />
              <button type="button" onClick={() => void createRevision()} disabled={!collaboration.can('edit') || !revisionReady || saving} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground disabled:opacity-50"><GitBranch className="size-3.5" />{t('teamPlatform.editor.createRevision')}</button>
              {details?.versions.map((version) => <div key={version.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><FileClock className="size-4 text-primary-bright" /><span className="text-[11px] font-medium text-foreground">{t('teamPlatform.editor.revisionNumber', { number: version.revisionNumber.toLocaleString(locale) })}</span><code className="ml-auto text-[9px] text-faint-foreground">{version.contentHash.slice(0, 16)}</code></div><p className="mt-1 text-[10px] text-muted-foreground">{formatDate(version.createdAt, locale)} · {t('teamPlatform.editor.pinnedSources', { count: (version.sourceVersions?.length ?? 0).toLocaleString(locale) })}</p></div>)}
            </section>
          )}
          {tab === 'proposal' && (
            <ProposalPanel
              proposals={proposals}
              selected={selectedOperations}
              onSelected={setSelectedOperations}
              instruction={proposalInstruction}
              onInstruction={setProposalInstruction}
              canEdit={collaboration.can('edit')}
              canCreate={Boolean(latestVersion) && !dirty}
              onCreate={() => void workspace.createProposal({
                jobType: 'document.patch',
                targetRevision: latestVersion!,
                instruction: proposalInstruction,
                inputVersions: resource.draft?.sourceVersions ?? [],
                outputSchemaVersion: 'document.patch.v1',
              }).catch((error) => setLocalError(message(error, t('teamPlatform.editor.operationFailed'))))}
              onApply={(proposal) => void workspace.applyProposal(
                proposal.id,
                selectedOperations[proposal.id] ?? [],
              ).catch((error) => setLocalError(message(error, t('teamPlatform.editor.operationFailed'))))}
            />
          )}
          {tab === 'trace' && (
            <section className="mx-auto max-w-4xl space-y-2">
              {details?.dependencies.map((dependency) => <div key={dependency.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><Link2 className="mr-2 inline size-3.5 text-primary-bright" />{dependency.source.artifactId} <b>{dependencyRelationLabel(dependency.relation, t)}</b> {dependency.target.artifactId}</div>)}
              {workspace.traces.filter((trace) => trace.source.artifactId === resource.artifact.id || trace.target.artifactId === resource.artifact.id).map((trace) => <div key={trace.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><code>{trace.source.artifactId}:{trace.source.revisionId}</code> → {dependencyRelationLabel(trace.relation, t)} → <code>{trace.target.artifactId}:{trace.target.revisionId}</code></div>)}
            </section>
          )}
          {tab === 'review' && (
            <section className="mx-auto max-w-4xl space-y-3">
              {!latestVersion && <p className="rounded-md border border-dashed border-border p-4 text-[10px] text-faint-foreground">{t('teamPlatform.editor.createRevisionFirst')}</p>}
              {latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_auto]"><input value={comment} onChange={(event) => setComment(event.target.value)} placeholder={t('teamPlatform.editor.commentPlaceholder')} aria-label={t('teamPlatform.editor.commentPlaceholder')} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><button type="button" onClick={() => void collaboration.addComment(comment, undefined, latestVersion).then((ok) => ok && setComment(''))} disabled={!comment.trim() || !collaboration.can('comment')} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><MessageSquare className="mr-1 inline size-3" />{t('teamPlatform.editor.comment')}</button></div>}
              {latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_180px_auto]"><input value={reviewSummary} onChange={(event) => setReviewSummary(event.target.value)} aria-label={t('teamPlatform.reviews.summary')} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><select value={effectiveReviewerId} onChange={(event) => setReviewerId(event.target.value)} disabled={collaboration.project?.governanceMode === 'solo'} aria-label={t('teamPlatform.reviews.requiredReviewer')} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground disabled:opacity-75"><option value="">{t('reviews.reviewer')}</option>{governanceReviewers.map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name}</option>)}</select><button type="button" onClick={() => void collaboration.requestReview(reviewSummary, latestVersion, [effectiveReviewerId])} disabled={!gatePassed || !effectiveReviewerId || !reviewSummary.trim()} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><Send className="mr-1 inline size-3" />{t('editor.requestReview')}</button></div>}
              {comments.map((thread) => <div key={thread.id} className="rounded-md border border-border bg-panel p-3"><span className="text-[10px] font-medium text-foreground">{thread.author.name}</span><p className="mt-1 text-[10px] text-muted-foreground">{thread.body}</p></div>)}
            </section>
          )}
          {tab === 'collaboration' && (
            <section className="mx-auto max-w-4xl space-y-4" data-testid="document-collaboration-panel">
              {collaborationNotice && <p className="rounded-md border border-success/30 bg-success/10 p-2 text-[10px] text-success">{collaborationNotice}</p>}
              <div className="rounded-lg border border-border bg-panel p-4">
                <div className="flex items-center justify-between gap-2">
                  <div><h2 className="text-sm font-semibold text-foreground"><UserRoundCog className="mr-1.5 inline size-4 text-primary-bright" />{t('teamPlatform.editor.memberBindings')}</h2><p className="mt-1 text-[10px] text-muted-foreground">{t('teamPlatform.editor.memberBindingsDetail')}</p></div>
                  <span className="flex items-center gap-2"><code className="text-[9px] text-faint-foreground">{bindingsEtag || t('teamPlatform.editor.loadingEtag')}{bindingsDirty ? t('teamPlatform.editor.localEditsSuffix') : ''}</code><button type="button" data-testid="refresh-document-bindings" onClick={() => void loadMemberBindings()} disabled={bindingsLoading} className="rounded border border-border p-1 text-muted-foreground disabled:opacity-40" aria-label={t('teamPlatform.editor.refreshBindings')} title={t('teamPlatform.editor.refreshBindings')}><RefreshCw className={cn('size-3', bindingsLoading && 'animate-spin')} /></button></span>
                </div>
                {bindingsRemoteChange && <div data-testid="document-bindings-conflict" role="alert" className="mt-3 flex items-center justify-between gap-3 rounded border border-warning/30 bg-warning/10 p-2 text-[10px] text-warning"><span>{t('teamPlatform.editor.bindingsConflict')}</span><button type="button" onClick={() => void loadMemberBindings(true)} className="shrink-0 underline">{t('teamPlatform.editor.reloadBindings')}</button></div>}
                {bindingsLoading ? <Loader2 className="mt-3 size-4 animate-spin text-primary-bright" /> : <div className="mt-3 space-y-2">{memberBindings.map((binding, index) => {
                  const member = collaboration.members.find((item) => item.user.id === binding.userId)
                  return <div key={`${binding.userId}:${binding.role}`} className="grid gap-2 rounded border border-border bg-background p-2 sm:grid-cols-[1fr_170px_auto]"><span className="self-center text-[10px] text-foreground">{member?.user.name ?? binding.userId}</span><select value={binding.role} disabled={!collaboration.can('edit')} onChange={(event) => editMemberBindings((items) => items.map((item, itemIndex) => itemIndex === index ? { ...item, role: event.target.value as DocumentMemberRole } : item))} aria-label={t('teamPlatform.editor.bindingRoleFor', { name: member?.user.name ?? binding.userId })} className="h-8 rounded border border-border bg-panel px-2 text-[10px] text-foreground"><BindingRoleOptions /></select><button type="button" disabled={!collaboration.can('edit')} onClick={() => editMemberBindings((items) => items.filter((_, itemIndex) => itemIndex !== index))} className="rounded border border-border px-2 text-destructive disabled:opacity-40" aria-label={t('teamPlatform.editor.removeBinding')} title={t('teamPlatform.editor.removeBinding')}><Trash2 className="size-3" /></button></div>
                })}</div>}
                <div className="mt-3 grid gap-2 sm:grid-cols-[1fr_170px_auto_auto]"><select value={bindingUserId} onChange={(event) => setBindingUserId(event.target.value)} aria-label={t('teamPlatform.editor.projectMember')} className="h-9 rounded border border-border bg-background px-2 text-[10px] text-foreground"><option value="">{t('teamPlatform.editor.projectMember')}</option>{collaboration.members.map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name}</option>)}</select><select value={bindingRole} onChange={(event) => setBindingRole(event.target.value as DocumentMemberRole)} aria-label={t('common.role')} className="h-9 rounded border border-border bg-background px-2 text-[10px] text-foreground"><BindingRoleOptions /></select><button type="button" disabled={!bindingUserId || memberBindings.some((item) => item.userId === bindingUserId && item.role === bindingRole)} onClick={() => editMemberBindings((items) => [...items, { userId: bindingUserId, role: bindingRole }])} className="rounded border border-border px-3 text-[10px] text-primary-bright disabled:opacity-40"><Plus className="mr-1 inline size-3" />{t('common.add')}</button><button type="button" onClick={() => void saveMemberBindings()} disabled={!collaboration.can('edit') || !bindingsDirty || !bindingsEtag || saving || !memberBindings.some((item) => item.role === 'owner')} className="rounded bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-40">{t('teamPlatform.editor.saveBindings')}</button></div>
              </div>

              <div className="rounded-lg border border-border bg-panel p-4">
                <h2 className="text-sm font-semibold text-foreground"><Bot className="mr-1.5 inline size-4 text-primary-bright" />{t('teamPlatform.editor.generateDownstream')}</h2>
                <p className="mt-1 text-[10px] text-muted-foreground">{t('teamPlatform.editor.generateDownstreamDetail')}</p>
                <div className="mt-3 grid gap-2 sm:grid-cols-[180px_1fr]"><select value={downstreamKind} onChange={(event) => setDownstreamKind(event.target.value as DownstreamDocumentKind)} aria-label={t('teamPlatform.editor.downstreamKind')} className="h-9 rounded border border-border bg-background px-2 text-[10px] text-foreground"><DownstreamKindOptions /></select><input value={downstreamTitle} onChange={(event) => setDownstreamTitle(event.target.value)} placeholder={t('teamPlatform.editor.targetDocumentTitle')} className="h-9 rounded border border-border bg-background px-2 text-[10px] text-foreground" /></div>
                <textarea value={downstreamInstruction} onChange={(event) => setDownstreamInstruction(event.target.value)} rows={3} aria-label={t('teamPlatform.editor.downstreamInstruction')} className="mt-2 w-full rounded border border-border bg-background p-2 text-[10px] text-foreground" />
                <button type="button" onClick={() => void generateDownstreamDocument()} disabled={!collaboration.can('edit') || !approvedVersion || saving || !downstreamTitle.trim() || !downstreamInstruction.trim()} className="mt-2 rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-40">{t('teamPlatform.editor.generateReviewableOutput')}</button>
                {!approvedVersion && <p className="mt-2 text-[9px] text-warning">{t('teamPlatform.editor.approvalRequired')}</p>}
              </div>

              <div className="rounded-lg border border-border bg-panel p-4">
                <h2 className="text-sm font-semibold text-foreground"><GitBranch className="mr-1.5 inline size-4 text-primary-bright" />{t('teamPlatform.editor.syncImplementation')}</h2>
                <p className="mt-1 text-[10px] text-muted-foreground">{t('teamPlatform.editor.syncImplementationDetail')}</p>
                <div className="mt-3 grid gap-2 sm:grid-cols-[200px_1fr]"><select value={syncBackKind} onChange={(event) => setSyncBackKind(event.target.value as DocumentSyncBackProvenanceKind)} aria-label={t('teamPlatform.editor.provenanceKind')} className="h-9 rounded border border-border bg-background px-2 text-[10px] text-foreground"><option value="implementationProposal">{t('teamPlatform.editor.provenance.implementationProposal')}</option><option value="workspaceRevision">{t('teamPlatform.editor.provenance.workspaceRevision')}</option><option value="buildManifest">{t('teamPlatform.editor.provenance.buildManifest')}</option><option value="deployment">{t('teamPlatform.editor.provenance.deployment')}</option></select><input value={syncBackId} onChange={(event) => setSyncBackId(event.target.value)} placeholder={t('teamPlatform.editor.provenanceId')} className="h-9 rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground" /></div>
                <textarea value={syncBackInstruction} onChange={(event) => setSyncBackInstruction(event.target.value)} rows={3} aria-label={t('teamPlatform.editor.syncBackInstruction')} className="mt-2 w-full rounded border border-border bg-background p-2 text-[10px] text-foreground" />
                <button type="button" onClick={() => void createSyncBackProposal()} disabled={!collaboration.can('edit') || !approvedVersion || saving || !syncBackId.trim() || !syncBackInstruction.trim()} className="mt-2 rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-40">{t('teamPlatform.editor.createSyncBackProposal')}</button>
              </div>
            </section>
          )}
        </div>
      </main>
    </div>
  )
}

function ContentEditor({ content, readOnly, onChange }: { content: DocumentContentDto; readOnly: boolean; onChange: (patch: Partial<DocumentContentDto>) => void }) {
  const { t } = useI18n()
  const updateBlock = (index: number, patch: Partial<DocumentContentDto['blocks'][number]>) => {
    onChange({
      blocks: content.blocks.map((item, itemIndex) =>
        itemIndex === index ? { ...item, ...patch } : item),
    })
  }

  return (
    <section className="mx-auto max-w-4xl space-y-5">
      <label className="block text-[11px] font-medium text-muted-foreground">
        {t('teamPlatform.editor.content.summary')}
        <textarea value={content.summary} onChange={(event) => onChange({ summary: event.target.value })} readOnly={readOnly} rows={3} placeholder={t('teamPlatform.editor.content.summaryPlaceholder')} className="mt-1.5 w-full rounded-md border border-border bg-panel p-3 text-sm text-foreground" />
      </label>

      <div>
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold text-foreground">{t('teamPlatform.editor.content.structuredBlocks')}</h2>
          <button type="button" disabled={readOnly} onClick={() => onChange({ blocks: [...content.blocks, { id: stableId('block'), type: content.kind === 'projectBrief' ? 'goal' : 'paragraph', text: '' }] })} className="text-[10px] text-primary-bright"><Plus className="mr-1 inline size-3" />{t('teamPlatform.editor.content.block')}</button>
        </div>
        <div className="mt-2 space-y-2">
          {content.blocks.map((block, index) => (
            <div key={block.id} className="rounded-md border border-border bg-panel p-3">
              <div className="flex flex-wrap items-center gap-2">
                <code className="text-[9px] text-faint-foreground">{block.id}</code>
                <select value={block.type} disabled={readOnly} onChange={(event) => updateBlock(index, { type: event.target.value as typeof block.type })} className="ml-auto rounded border border-border bg-background text-[9px] text-foreground">
                  {Array.from(new Set([block.type, 'richText', 'goal', 'actor', 'userJourney', 'requirement', 'acceptanceCriterion', 'businessRule', 'constraint', 'nonFunctionalRequirement', 'metric', 'openQuestion', 'decision', 'sourceReference', 'heading', 'paragraph', 'list', 'table', 'code', 'callout'])).map((type) => <option key={type} value={type}>{blockTypeLabel(type, t)}</option>)}
                </select>
                {block.type === 'openQuestion' && (
                  <>
                    <label className="flex items-center gap-1 text-[9px] text-muted-foreground"><input type="checkbox" checked={Boolean(block.blocking)} disabled={readOnly} onChange={(event) => updateBlock(index, { blocking: event.target.checked })} />{t('graph.meta.blocking')}</label>
                    <select value={block.status ?? 'open'} disabled={readOnly} onChange={(event) => updateBlock(index, { status: event.target.value as NonNullable<typeof block.status> })} className="rounded border border-border bg-background text-[9px] text-foreground">
                      {['open', 'answered', 'resolved', 'waived'].map((status) => <option key={status} value={status}>{questionStatusLabel(status, t)}</option>)}
                    </select>
                  </>
                )}
                <button type="button" disabled={readOnly} onClick={() => onChange({ blocks: content.blocks.filter((_, itemIndex) => itemIndex !== index) })} aria-label={t('teamPlatform.editor.content.removeBlock')} title={t('teamPlatform.editor.content.removeBlock')}><Trash2 className="size-3 text-destructive" /></button>
              </div>
              <textarea value={block.text ?? ''} onChange={(event) => updateBlock(index, { text: event.target.value })} readOnly={readOnly} rows={3} className="mt-2 w-full rounded border border-border bg-background p-2 text-[11px] text-foreground" />
            </div>
          ))}
        </div>
      </div>

      {content.kind !== 'projectBrief' && (
        <>
          <div>
            <div className="flex items-center justify-between"><h2 className="text-sm font-semibold text-foreground">{t('teamPlatform.editor.content.requirements')}</h2><button type="button" disabled={readOnly} onClick={() => onChange({ requirements: [...(content.requirements ?? []), { id: stableId('req'), title: t('teamPlatform.editor.content.requirement'), statement: '', priority: 'must', acceptanceCriterionIds: [], sourceBlockIds: [] }] })} className="text-[10px] text-primary-bright"><Plus className="mr-1 inline size-3" />{t('teamPlatform.editor.content.requirement')}</button></div>
            <div className="mt-2 space-y-2">{(content.requirements ?? []).map((requirement, index) => {
              const updateRequirement = (patch: Partial<typeof requirement>) => onChange({ requirements: content.requirements?.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) })
              return <div key={requirement.id} className="space-y-2 rounded-md border border-border bg-panel p-3"><div className="flex flex-wrap items-center gap-2"><input value={requirement.id} onChange={(event) => updateRequirement({ id: event.target.value })} readOnly={readOnly} className="h-8 min-w-40 rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" aria-label={t('teamPlatform.editor.content.stableRequirementId')} /><select value={requirement.priority} disabled={readOnly} onChange={(event) => updateRequirement({ priority: event.target.value as typeof requirement.priority })} aria-label={t('teamPlatform.editor.content.priority')} className="h-8 rounded border border-border bg-background px-2 text-[9px] text-foreground">{['must', 'should', 'could'].map((priority) => <option key={priority} value={priority}>{priorityLabel(priority, t)}</option>)}</select><button type="button" disabled={readOnly} onClick={() => onChange({ requirements: content.requirements?.filter((_, itemIndex) => itemIndex !== index) })} className="ml-auto" aria-label={t('teamPlatform.editor.content.removeRequirement')} title={t('teamPlatform.editor.content.removeRequirement')}><Trash2 className="size-3 text-destructive" /></button></div><input value={requirement.title} onChange={(event) => updateRequirement({ title: event.target.value })} readOnly={readOnly} placeholder={t('teamPlatform.editor.content.requirementTitle')} className="h-8 w-full rounded border border-border bg-background px-2 text-[11px] text-foreground" /><textarea value={requirement.statement} onChange={(event) => updateRequirement({ statement: event.target.value })} readOnly={readOnly} rows={2} placeholder={t('teamPlatform.editor.content.requirementStatement')} className="w-full rounded border border-border bg-background p-2 text-[11px] text-foreground" /><div className="grid gap-2 sm:grid-cols-2"><label className="text-[9px] text-muted-foreground">{t('teamPlatform.editor.content.acceptanceIds')}<input value={requirement.acceptanceCriterionIds.join(', ')} onChange={(event) => updateRequirement({ acceptanceCriterionIds: commaList(event.target.value) })} readOnly={readOnly} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" /></label><label className="text-[9px] text-muted-foreground">{t('teamPlatform.editor.content.sourceBlockIds')}<input value={requirement.sourceBlockIds.join(', ')} onChange={(event) => updateRequirement({ sourceBlockIds: commaList(event.target.value) })} readOnly={readOnly} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" /></label></div></div>
            })}</div>
          </div>
          <div>
            <div className="flex items-center justify-between"><h2 className="text-sm font-semibold text-foreground">{t('teamPlatform.editor.content.acceptanceCriteria')}</h2><button type="button" disabled={readOnly} onClick={() => onChange({ acceptanceCriteria: [...content.acceptanceCriteria, { id: stableId('ac'), statement: '', priority: 'must', status: 'open' }] })} className="text-[10px] text-primary-bright"><Plus className="mr-1 inline size-3" />{t('teamPlatform.editor.content.criterion')}</button></div>
            <div className="mt-2 space-y-2">{content.acceptanceCriteria.map((criterion, index) => {
              const updateCriterion = (patch: Partial<typeof criterion>) => onChange({ acceptanceCriteria: content.acceptanceCriteria.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) })
              return <div key={criterion.id} className="grid gap-2 rounded-md border border-border bg-panel p-3 sm:grid-cols-[150px_100px_110px_1fr_auto]"><input value={criterion.id} onChange={(event) => updateCriterion({ id: event.target.value })} readOnly={readOnly} className="h-8 rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" aria-label={t('teamPlatform.editor.content.stableCriterionId')} /><select value={criterion.priority} disabled={readOnly} onChange={(event) => updateCriterion({ priority: event.target.value as typeof criterion.priority })} aria-label={t('teamPlatform.editor.content.priority')} className="h-8 rounded border border-border bg-background px-2 text-[9px] text-foreground">{['must', 'should', 'could'].map((priority) => <option key={priority} value={priority}>{priorityLabel(priority, t)}</option>)}</select><select value={criterion.status} disabled={readOnly} onChange={(event) => updateCriterion({ status: event.target.value as typeof criterion.status })} aria-label={t('common.status')} className="h-8 rounded border border-border bg-background px-2 text-[9px] text-foreground">{['open', 'accepted', 'rejected'].map((status) => <option key={status} value={status}>{criterionStatusLabel(status, t)}</option>)}</select><input value={criterion.statement} onChange={(event) => updateCriterion({ statement: event.target.value })} readOnly={readOnly} aria-label={t('teamPlatform.editor.content.criterionStatement')} className="min-w-0 rounded border border-border bg-background px-2 text-[11px] text-foreground" /><button type="button" disabled={readOnly} onClick={() => onChange({ acceptanceCriteria: content.acceptanceCriteria.filter((_, itemIndex) => itemIndex !== index) })} aria-label={t('teamPlatform.editor.content.removeCriterion')} title={t('teamPlatform.editor.content.removeCriterion')}><Trash2 className="size-3 text-destructive" /></button></div>
            })}</div>
          </div>
          <StringListEditor title={t('teamPlatform.editor.content.openQuestions')} values={content.openQuestions} readOnly={readOnly} onChange={(openQuestions) => onChange({ openQuestions })} />
          <StringListEditor title={t('teamPlatform.editor.content.assumptions')} values={content.assumptions} readOnly={readOnly} onChange={(assumptions) => onChange({ assumptions })} />
        </>
      )}
    </section>
  )
}

function StringListEditor({ title, values, readOnly, onChange }: { title: string; values: readonly string[]; readOnly: boolean; onChange: (values: string[]) => void }) {
  const { t } = useI18n()
  return <div><div className="flex items-center justify-between"><h2 className="text-sm font-semibold text-foreground">{title}</h2><button type="button" disabled={readOnly} onClick={() => onChange([...values, ''])} className="text-[10px] text-primary-bright"><Plus className="mr-1 inline size-3" />{t('common.add')}</button></div><div className="mt-2 space-y-2">{values.map((value, index) => <div key={index} className="flex gap-2 rounded-md border border-border bg-panel p-2"><input value={value} onChange={(event) => onChange(values.map((item, itemIndex) => itemIndex === index ? event.target.value : item))} readOnly={readOnly} aria-label={t('teamPlatform.editor.content.listItem', { number: index + 1 })} className="h-8 min-w-0 flex-1 rounded border border-border bg-background px-2 text-[10px] text-foreground" /><button type="button" disabled={readOnly} onClick={() => onChange(values.filter((_, itemIndex) => itemIndex !== index))} aria-label={t('teamPlatform.editor.content.removeListItem', { number: index + 1 })} title={t('teamPlatform.editor.content.removeListItem', { number: index + 1 })}><Trash2 className="size-3 text-destructive" /></button></div>)}</div></div>
}

function GatePanel({ clientIssues, serverGate }: { clientIssues: string[]; serverGate?: ArtifactDetails<DocumentContentDto>['reviewGate'] }) {
  const { t } = useI18n()
  const serverIssues = serverGate?.checks
    .filter((check) => check.severity === 'error' && check.code !== 'canonical_review_approved')
    .map((check) => check.message) ?? []
  const issues = [...clientIssues, ...serverIssues]
  const requestReady = issues.length === 0 && reviewGateReadyForRequest(serverGate)
  const approved = Boolean(serverGate?.passed)
  return <div className={cn('rounded-lg border p-3', approved || requestReady ? 'border-success/30 bg-success/10' : 'border-warning/30 bg-warning/10')}><p className="text-[11px] font-semibold text-foreground">{t('teamPlatform.editor.reviewGate')}</p>{approved ? <p className="mt-1 text-[10px] text-success"><Check className="mr-1 inline size-3" />{t('teamPlatform.editor.gateApproved')}</p> : requestReady ? <p className="mt-1 text-[10px] text-success"><Check className="mr-1 inline size-3" />{t('teamPlatform.editor.gateChecksPassed')}</p> : <ul className="mt-1 list-disc pl-4 text-[10px] text-warning">{issues.map((issue) => <li key={issue}>{reviewIssueLabel(issue, t)}</li>)}{!serverGate && <li>{t('teamPlatform.editor.waitingForGate')}</li>}</ul>}</div>
}

function ProposalPanel({ proposals, selected, onSelected, instruction, onInstruction, canEdit, canCreate, onCreate, onApply }: { proposals: ProposalDto[]; selected: Record<string, string[]>; onSelected: (value: Record<string, string[]>) => void; instruction: string; onInstruction: (value: string) => void; canEdit: boolean; canCreate: boolean; onCreate: () => void; onApply: (proposal: ProposalDto) => void }) {
  const { t } = useI18n()
  return <section className="mx-auto max-w-4xl space-y-3"><div className="flex gap-2"><input value={instruction} onChange={(event) => onInstruction(event.target.value)} aria-label={t('teamPlatform.editor.proposalInstruction')} className="h-9 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><button type="button" onClick={onCreate} disabled={!canEdit || !canCreate || !instruction.trim()} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><Bot className="mr-1 inline size-3" />{t('teamPlatform.editor.askAi')}</button></div>{!canCreate && <p className="rounded border border-warning/30 bg-warning/10 p-2 text-[9px] text-warning">{t('teamPlatform.editor.revisionBeforeAi')}</p>}{proposals.map((proposal) => { const selectedIds = selected[proposal.id] ?? []; const hasAccepted = proposal.operations.some((operation) => operation.decision === 'accepted' || selectedIds.includes(operation.id)); return <div key={proposal.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><span className="text-[11px] font-semibold text-foreground">{t('teamPlatform.editor.manifestId', { id: proposal.manifest.id.slice(0, 12) })}</span><span className="rounded bg-primary/10 px-1.5 py-0.5 text-[9px] text-primary-bright">{proposalStatusLabel(proposal.status, t)}</span><code className="ml-auto text-[9px] text-faint-foreground">{t('teamPlatform.editor.baseHash', { hash: proposal.baseRevision.contentHash.slice(0, 12) })}</code></div><div className="mt-2 space-y-1">{proposal.operations.map((operation) => <label key={operation.id} className="flex gap-2 rounded border border-border bg-background p-2 text-[9px] text-muted-foreground"><input type="checkbox" disabled={operation.decision !== 'pending'} checked={operation.decision === 'accepted' || operation.decision === 'applied' || selectedIds.includes(operation.id)} onChange={(event) => onSelected({ ...selected, [proposal.id]: event.target.checked ? [...selectedIds, operation.id] : selectedIds.filter((item) => item !== operation.id) })} /><span className="min-w-0 flex-1"><code>{proposalOperationLabel(operation.kind, t)} {operation.path || '/'}</code><span className="ml-2 text-faint-foreground">{proposalDecisionLabel(operation.decision, t)}</span>{operation.rationale && <span className="mt-1 block">{operation.rationale}</span>}</span></label>)}</div><button type="button" onClick={() => onApply(proposal)} disabled={!canEdit || !hasAccepted || !['open', 'reviewing', 'ready'].includes(proposal.status)} className="mt-2 rounded bg-primary px-2.5 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-50">{t('teamPlatform.editor.applyAccepted')}</button></div>})}</section>
}

function BindingRoleOptions() {
  const { t } = useI18n()
  return <><option value="owner">{t('role.owner')}</option><option value="assignee">{t('role.assignee')}</option><option value="downstreamOwner">{t('role.downstreamOwner')}</option><option value="reviewer">{t('role.reviewer')}</option><option value="watcher">{t('role.watcher')}</option></>
}

function DownstreamKindOptions() {
  const { t } = useI18n()
  return <>{['product_requirements', 'project_brief', 'api_contract', 'data_contract', 'permission_contract', 'decision_record', 'reference_source', 'change_request', 'glossary_policy'].map((kind) => <option key={kind} value={kind}>{documentKindLabel(kind, t)}</option>)}</>
}

function Unavailable({ title, detail, loading, action, onAction, onRetry, retryLabel }: { title: string; detail: string; loading?: boolean; action?: string; onAction?: () => void; onRetry?: () => Promise<void>; retryLabel?: string }) {
  return <div className="flex h-full items-center justify-center bg-canvas p-6 text-center"><div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-6">{loading ? <Loader2 className="mx-auto size-7 animate-spin text-primary-bright" /> : <AlertTriangle className="mx-auto size-7 text-warning" />}<h1 className="mt-3 text-base font-semibold text-foreground">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{detail}</p>{action && onAction && <button type="button" onClick={onAction} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground">{action}</button>}{onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground"><RefreshCw className="mr-1 inline size-4" />{retryLabel}</button>}</div></div>
}

function versionRef(revision: ArtifactRevisionDto<DocumentContentDto>): VersionRefDto {
  return { artifactId: revision.artifactId, revisionId: revision.id, revisionNumber: revision.revisionNumber, contentHash: revision.contentHash }
}

function stableId(prefix: string) {
  return `${prefix}-${typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`}`
}

function commaList(value: string) {
  return Array.from(new Set(value.split(',').map((item) => item.trim()).filter(Boolean)))
}

type Translate = ReturnType<typeof useI18n>['t']

function documentKindLabel(kind: string, t: Translate) {
  const labels: Record<string, string> = {
    projectBrief: t('teamPlatform.editor.kind.projectBrief'),
    project_brief: t('teamPlatform.editor.kind.projectBrief'),
    productRequirements: t('teamPlatform.editor.kind.productRequirements'),
    product_requirements: t('teamPlatform.editor.kind.productRequirements'),
    apiContract: t('doc.type.apiContract'),
    api_contract: t('doc.type.apiContract'),
    data_contract: t('teamPlatform.editor.kind.dataContract'),
    permission_contract: t('teamPlatform.editor.kind.permissionContract'),
    decision_record: t('teamPlatform.editor.kind.decisionRecord'),
    reference_source: t('teamPlatform.editor.kind.referenceSource'),
    change_request: t('teamPlatform.editor.kind.changeRequest'),
    glossary_policy: t('teamPlatform.editor.kind.glossaryPolicy'),
  }
  return labels[kind] ?? kind
}

function artifactStatusLabel(status: string, t: Translate) {
  const labels: Record<string, string> = {
    draft: t('doc.status.draft'),
    readyForReview: t('doc.status.readyForReview'),
    changesRequested: t('doc.status.changesRequested'),
    approved: t('doc.status.approved'),
    needsSync: t('doc.status.needsSync'),
    archived: t('doc.status.archived'),
  }
  return labels[status] ?? status
}

function dependencyRelationLabel(relation: string, t: Translate) {
  const labels: Record<string, string> = {
    depends_on: t('dep.depends_on'),
    generates: t('dep.generates'),
    blocks: t('dep.blocks'),
    implements: t('dep.implements'),
    reviews: t('dep.reviews'),
    references: t('dep.references'),
    composes: t('dep.composes'),
    derives_from: t('dep.derives_from'),
    syncs_with: t('dep.syncs_with'),
  }
  return labels[relation] ?? relation
}

function blockTypeLabel(type: string, t: Translate) {
  const labels: Record<string, string> = {
    richText: t('teamPlatform.editor.block.richText'),
    goal: t('teamPlatform.editor.block.goal'),
    actor: t('teamPlatform.editor.block.actor'),
    userJourney: t('teamPlatform.editor.block.userJourney'),
    requirement: t('teamPlatform.editor.block.requirement'),
    acceptanceCriterion: t('teamPlatform.editor.block.acceptanceCriterion'),
    businessRule: t('teamPlatform.editor.block.businessRule'),
    constraint: t('teamPlatform.editor.block.constraint'),
    nonFunctionalRequirement: t('teamPlatform.editor.block.nonFunctionalRequirement'),
    metric: t('teamPlatform.editor.block.metric'),
    openQuestion: t('teamPlatform.editor.block.openQuestion'),
    decision: t('teamPlatform.editor.block.decision'),
    sourceReference: t('teamPlatform.editor.block.sourceReference'),
    heading: t('teamPlatform.editor.block.heading'),
    paragraph: t('teamPlatform.editor.block.paragraph'),
    list: t('teamPlatform.editor.block.list'),
    table: t('teamPlatform.editor.block.table'),
    code: t('teamPlatform.editor.block.code'),
    callout: t('teamPlatform.editor.block.callout'),
  }
  return labels[type] ?? type
}

function questionStatusLabel(status: string, t: Translate) {
  const labels: Record<string, string> = {
    open: t('teamPlatform.editor.status.open'),
    answered: t('teamPlatform.editor.status.answered'),
    resolved: t('teamPlatform.editor.status.resolved'),
    waived: t('teamPlatform.editor.status.waived'),
  }
  return labels[status] ?? status
}

function criterionStatusLabel(status: string, t: Translate) {
  const labels: Record<string, string> = {
    open: t('teamPlatform.editor.status.open'),
    accepted: t('teamPlatform.editor.status.accepted'),
    rejected: t('teamPlatform.editor.status.rejected'),
  }
  return labels[status] ?? status
}

function priorityLabel(priority: string, t: Translate) {
  const labels: Record<string, string> = {
    must: t('teamPlatform.editor.priority.must'),
    should: t('teamPlatform.editor.priority.should'),
    could: t('teamPlatform.editor.priority.could'),
  }
  return labels[priority] ?? priority
}

function proposalStatusLabel(status: string, t: Translate) {
  const labels: Record<string, string> = {
    open: t('teamPlatform.editor.proposalStatus.open'),
    reviewing: t('teamPlatform.editor.proposalStatus.reviewing'),
    ready: t('teamPlatform.editor.proposalStatus.ready'),
    applied: t('teamPlatform.editor.proposalStatus.applied'),
    rejected: t('teamPlatform.editor.proposalStatus.rejected'),
    superseded: t('teamPlatform.editor.proposalStatus.superseded'),
  }
  return labels[status] ?? status
}

function proposalDecisionLabel(decision: string, t: Translate) {
  const labels: Record<string, string> = {
    pending: t('teamPlatform.editor.proposalDecision.pending'),
    accepted: t('teamPlatform.editor.proposalDecision.accepted'),
    rejected: t('teamPlatform.editor.proposalDecision.rejected'),
    applied: t('teamPlatform.editor.proposalDecision.applied'),
  }
  return labels[decision] ?? decision
}

function proposalOperationLabel(kind: string, t: Translate) {
  const labels: Record<string, string> = {
    add: t('teamPlatform.editor.operation.add'),
    remove: t('teamPlatform.editor.operation.remove'),
    replace: t('teamPlatform.editor.operation.replace'),
    move: t('teamPlatform.editor.operation.move'),
    copy: t('teamPlatform.editor.operation.copy'),
    test: t('teamPlatform.editor.operation.test'),
  }
  return labels[kind] ?? kind
}

function reviewIssueLabel(issue: string, t: Translate) {
  const labels: Record<string, string> = {
    'Summary is required.': t('teamPlatform.editor.issue.summaryRequired'),
    'At least one structured block is required.': t('teamPlatform.editor.issue.blockRequired'),
    'Project Brief requires at least one non-empty goal block.': t('teamPlatform.editor.issue.goalRequired'),
    'Resolve or waive every blocking open question before review.': t('teamPlatform.editor.issue.blockingQuestion'),
    'At least one requirement is required.': t('teamPlatform.editor.issue.requirementRequired'),
    'Every requirement needs a stable ID and statement.': t('teamPlatform.editor.issue.requirementIdStatement'),
    'Requirement IDs must be unique.': t('teamPlatform.editor.issue.requirementIdsUnique'),
    'Every acceptance criterion needs a stable ID and statement.': t('teamPlatform.editor.issue.criterionIdStatement'),
    'Acceptance criterion IDs must be unique.': t('teamPlatform.editor.issue.criterionIdsUnique'),
    'Every Must requirement needs at least one acceptance criterion.': t('teamPlatform.editor.issue.mustCriterion'),
    'Every requirement acceptance reference must resolve to an existing criterion.': t('teamPlatform.editor.issue.acceptanceReference'),
    'Every requirement must trace to at least one existing source block.': t('teamPlatform.editor.issue.sourceTrace'),
  }
  return labels[issue] ?? issue
}

function formatDate(value: string, locale: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale)
}

function message(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback
}
