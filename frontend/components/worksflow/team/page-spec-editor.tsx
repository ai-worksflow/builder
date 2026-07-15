'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  ArtifactWorkspaceConflictError,
  reviewGateReadyForRequest,
  type ArtifactDetails,
} from '@/lib/platform/artifact-workspace'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import { workflowEditorTargetForArtifact } from '@/lib/platform/workflow-ui-contract'
import { useWorksflow } from '@/lib/worksflow/store'
import {
  normalizePageSpecContent,
  pageSpecReviewIssues,
  REQUIRED_PAGE_STATE_KEYS,
} from '@/lib/platform/page-spec-content'
import type {
  ArtifactReviewGateDto,
  ArtifactRevisionDto,
  JsonObject,
  PageDataBindingDto,
  PageInteractionSpecDto,
  PageSpecContentDto,
  PageStateDto,
  ProposalDto,
  VersionRefDto,
} from '@/lib/platform/dto'
import { cn } from '@/lib/utils'
import { useI18n } from '@/lib/i18n'
import { reviewCandidatesForGovernance } from '@/lib/worksflow/project-governance'
import {
  AlertTriangle,
  ArrowLeft,
  Bot,
  CheckCircle2,
  Database,
  FileClock,
  GitBranch,
  Link2,
  Loader2,
  MessageSquare,
  MousePointerClick,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  Send,
  ShieldCheck,
  Trash2,
  Wand2,
} from 'lucide-react'

type EditorTab = 'content' | 'states' | 'data' | 'interactions' | 'versions' | 'proposal' | 'trace' | 'review'
type SaveState = 'idle' | 'dirty' | 'saving' | 'saved' | 'conflict' | 'error'

interface WorkflowRouteReference {
  readonly runId: string
  readonly proposalId: string
  readonly nodeKey: string
}

export function PageSpecEditor({
  artifactId,
  onBack,
}: {
  artifactId: string
  onBack?: () => void
}) {
  const { locale, t } = useI18n()
  const workspace = useArtifactWorkspace()
  const flow = usePlatformFlow()
  const collaboration = useCollaboration()
  const { setSurface } = useWorksflow()
  const resource = workspace.pageSpecs.find((item) => item.artifact.id === artifactId)
  const serverContent = resource?.draft?.content
    ?? resource?.latestRevision?.content
    ?? resource?.approvedRevision?.content
  const serverEtag = resource?.draft?.etag ?? resource?.artifact.etag ?? ''
  const [tab, setTab] = useState<EditorTab>('content')
  const [content, setContent] = useState<PageSpecContentDto | null>(null)
  const [draftEtag, setDraftEtag] = useState('')
  const [saveState, setSaveState] = useState<SaveState>('idle')
  const [error, setError] = useState<string | null>(null)
  const [details, setDetails] = useState<ArtifactDetails<PageSpecContentDto> | null>(null)
  const [proposalInstruction, setProposalInstruction] = useState(() => t('teamPlatform.pageSpec.defaultProposalInstruction'))
  const [selectedOperations, setSelectedOperations] = useState<Record<string, string[]>>({})
  const [comment, setComment] = useState('')
  const [reviewSummary, setReviewSummary] = useState(() => t('teamPlatform.pageSpec.defaultReviewSummary'))
  const [reviewerId, setReviewerId] = useState('')
  const [proposalBusyId, setProposalBusyId] = useState('')
  const [proposalBaseRestoreBusy, setProposalBaseRestoreBusy] = useState(false)
  const [confirmProposalBaseRestore, setConfirmProposalBaseRestore] = useState(false)
  const [confirmedAppliedProposalId, setConfirmedAppliedProposalId] = useState('')
  const [createdWorkflowRevisionId, setCreatedWorkflowRevisionId] = useState('')
  const activeArtifactRef = useRef('')
  const contentRef = useRef<PageSpecContentDto | null>(null)
  const draftEtagRef = useRef('')
  const saveInFlightRef = useRef(false)
  const queuedContentRef = useRef<PageSpecContentDto | null>(null)
  const canEdit = collaboration.can('edit')
  const workflowReference = workflowRouteReference(artifactId)
  const inferredWorkflowProposalId = useMemo(() => workflowEditorTargetForArtifact(
    flow.runDefinition?.definition,
    flow.run?.id === workflowReference.runId ? flow.run : null,
    workspace,
    artifactId,
    'page_spec',
    workflowReference.nodeKey || undefined,
  )?.proposalId ?? '', [
    artifactId,
    flow.run,
    flow.runDefinition?.definition,
    workflowReference.nodeKey,
    workflowReference.runId,
    workspace,
  ])
  const workflowProposalId = resolvedWorkflowProposalReference(
    workflowReference,
    inferredWorkflowProposalId,
  )
  const workflowContextRequested = Boolean(workflowReference.runId || workflowReference.proposalId)

  const latestVersion = resource?.latestRevision
    ? versionRef(resource.latestRevision)
    : undefined
  const artifactProposals = workspace.proposals.filter((proposal) => proposal.artifactId === artifactId)
  const linkedProposal = artifactProposals.find((proposal) => proposal.id === workflowProposalId)
  const proposals = linkedProposal
    ? [linkedProposal, ...artifactProposals.filter((proposal) => proposal.id !== linkedProposal.id)]
    : artifactProposals
  const linkedProposalApplied = Boolean(
    linkedProposal
    && (
      linkedProposal.status === 'applied'
      || linkedProposal.status === 'partially_applied'
      || confirmedAppliedProposalId === linkedProposal.id
    ),
  )
  const linkedProposalRevision = resource?.latestRevision?.proposalId === workflowProposalId
    ? resource.latestRevision
    : undefined
  const linkedProposalBaseIsLatest = Boolean(
    linkedProposal
    && resource?.latestRevision
    && resource.latestRevision.id === linkedProposal.baseRevision.revisionId
    && resource.latestRevision.contentHash === linkedProposal.baseRevision.contentHash,
  )
  const workflowRevisionReady = Boolean(linkedProposalRevision || createdWorkflowRevisionId)
  const workflowReviewLocked = workflowContextRequested && !workflowRevisionReady
  const linkedProposalBaseMatchesDraft = Boolean(
    linkedProposal
    && (resource?.draft?.contentHash ?? resource?.latestRevision?.contentHash) === linkedProposal.baseRevision.contentHash,
  )
  const dirty = Boolean(content && serverContent && JSON.stringify(content) !== JSON.stringify(normalizePageSpecContent(serverContent)))
  const linkedProposalBaseRecoverable = Boolean(
    linkedProposal
    && !linkedProposalApplied
    && !linkedProposalBaseMatchesDraft
    && linkedProposalBaseIsLatest
    && resource?.draft
    && resource.draft.baseRevisionId === linkedProposal.baseRevision.revisionId
    && !dirty
    && !['dirty', 'saving', 'conflict'].includes(saveState),
  )
  const contentCanEdit = canEdit
    && (!workflowContextRequested || Boolean(workflowProposalId && linkedProposalApplied))
  const comments = collaboration.comments.filter((thread) =>
    thread.target?.artifactId === artifactId
    && thread.target.revisionId === latestVersion?.revisionId,
  )
  const reviews = collaboration.reviews.filter((review) =>
    review.target?.artifactId === artifactId
    && review.target.revisionId === latestVersion?.revisionId,
  )
  const currentUserId = collaboration.session.signedIn ? collaboration.session.user.id : null
  const clientIssues = content ? pageSpecReviewIssues(content) : []
  const draftMatchesLatest = Boolean(
    latestVersion
    && (!resource?.draft || resource.draft.contentHash === latestVersion.contentHash),
  )
  const hasUnversionedChanges = Boolean(
    resource?.draft
    && (!latestVersion || resource.draft.contentHash !== latestVersion.contentHash),
  )
  const gatePassed = Boolean(latestVersion) && draftMatchesLatest && !dirty && clientIssues.length === 0
    && reviewGateReadyForRequest(details?.reviewGate)

  useEffect(() => {
    if (workflowReference.proposalId || !inferredWorkflowProposalId || typeof window === 'undefined') return
    const url = new URL(window.location.href)
    url.searchParams.set('proposalId', inferredWorkflowProposalId)
    window.history.replaceState(window.history.state, '', url)
  }, [inferredWorkflowProposalId, workflowReference.proposalId])

  useEffect(() => {
    setConfirmedAppliedProposalId('')
    setCreatedWorkflowRevisionId('')
    setConfirmProposalBaseRestore(false)
  }, [artifactId, workflowProposalId])

  useEffect(() => {
    if (workflowContextRequested && !workflowProposalId) setTab('content')
  }, [workflowContextRequested, workflowProposalId])

  useEffect(() => {
    if (!workflowProposalId || !linkedProposal) return
    setTab(linkedProposalApplied ? 'versions' : 'proposal')
  }, [linkedProposal?.id, linkedProposalApplied, workflowProposalId])

  useEffect(() => {
    if (!resource || !serverContent) {
      activeArtifactRef.current = ''
      contentRef.current = null
      draftEtagRef.current = ''
      setContent(null)
      setDraftEtag('')
      setDetails(null)
      return
    }
    const switched = activeArtifactRef.current !== resource.artifact.id
    if (switched || !['dirty', 'saving', 'conflict'].includes(saveState)) {
      const next = normalizePageSpecContent(serverContent)
      activeArtifactRef.current = resource.artifact.id
      contentRef.current = next
      setContent(next)
      setDraftEtag(serverEtag)
      draftEtagRef.current = serverEtag
      if (switched) {
        setSaveState('idle')
        setError(null)
      }
    }
  }, [resource, saveState, serverContent, serverEtag])

  const loadDetails = useCallback(async () => {
    if (!resource) return
    try {
      setDetails(await workspace.loadDetails<PageSpecContentDto>(resource.artifact.id))
    } catch (cause) {
      setError(errorMessage(cause, t('teamPlatform.pageSpec.operationFailed')))
    }
  }, [resource, t, workspace])

  useEffect(() => {
    void loadDetails()
  }, [loadDetails])

  const saveDraft = useCallback(async (nextContent = contentRef.current) => {
    if (!resource || !nextContent || !draftEtagRef.current || !canEdit) return null
    const initial = normalizePageSpecContent(nextContent)
    if (saveInFlightRef.current) {
      queuedContentRef.current = initial
      return null
    }
    saveInFlightRef.current = true
    try {
      let pending: PageSpecContentDto | null = initial
      let lastResult: Awaited<ReturnType<typeof workspace.savePageSpecDraft>> | null = null
      while (pending) {
        queuedContentRef.current = null
        setSaveState('saving')
        setError(null)
        const savedPayload: string = JSON.stringify(pending)
        lastResult = await workspace.savePageSpecDraft(
          resource.artifact.id,
          pending,
          draftEtagRef.current,
        )
        const nextEtag = lastResult.data.draft?.etag ?? lastResult.etag
        if (!nextEtag) throw new Error(t('teamPlatform.pageSpec.missingDraftEtag'))
        draftEtagRef.current = nextEtag
        setDraftEtag(nextEtag)
        const latestLocal = queuedContentRef.current ?? contentRef.current
        pending = latestLocal && JSON.stringify(latestLocal) !== savedPayload
          ? normalizePageSpecContent(latestLocal)
          : null
      }
      setSaveState('saved')
      return lastResult
    } catch (cause) {
      if (cause instanceof ArtifactWorkspaceConflictError) {
        setSaveState('conflict')
        setError(t('teamPlatform.pageSpec.conflictDetail'))
      } else {
        setSaveState('error')
        setError(errorMessage(cause, t('teamPlatform.pageSpec.operationFailed')))
      }
      return null
    } finally {
      saveInFlightRef.current = false
      queuedContentRef.current = null
    }
  }, [canEdit, resource, t, workspace])

  useEffect(() => {
    if (saveState !== 'dirty' || !content || !canEdit) return
    const timer = window.setTimeout(() => void saveDraft(content), 700)
    return () => window.clearTimeout(timer)
  }, [canEdit, content, saveDraft, saveState])

  function updateContent(
    update: Partial<PageSpecContentDto>
      | ((current: PageSpecContentDto) => PageSpecContentDto),
  ) {
    if (!contentCanEdit) return
    setContent((current) => {
      if (!current) return current
      const next = normalizePageSpecContent(
        typeof update === 'function' ? update(current) : { ...current, ...update },
      )
      contentRef.current = next
      return next
    })
    if (saveState !== 'conflict') setSaveState('dirty')
    setError(null)
  }

  async function createRevision() {
    if (
      !resource
      || !content
      || clientIssues.length > 0
      || dirty
      || saveState === 'saving'
      || saveState === 'conflict'
      || !hasUnversionedChanges
      || (workflowContextRequested && (!workflowProposalId || !linkedProposalApplied))
    ) return
    setSaveState('saving')
    setError(null)
    try {
      const revision = await workspace.createPageSpecRevision(resource.artifact.id)
      if (workflowProposalId) {
        if (
          revision.artifactId !== artifactId
          || revision.proposalId !== workflowProposalId
          || revision.contentHash !== resource.draft?.contentHash
        ) throw new Error(t('teamPlatform.pageSpec.workflowRevisionMismatch'))
        setCreatedWorkflowRevisionId(revision.id)
      }
      setSaveState('saved')
      await loadDetails()
    } catch (cause) {
      if (cause instanceof ArtifactWorkspaceConflictError) setSaveState('conflict')
      else setSaveState('error')
      setError(errorMessage(cause, t('teamPlatform.pageSpec.operationFailed')))
    }
  }

  async function useServerDraft() {
    queuedContentRef.current = null
    setSaveState('idle')
    setError(null)
    await workspace.refresh()
  }

  async function applyProposal(proposal: ProposalDto) {
    if (
      proposalBusyId
      || dirty
      || saveState === 'saving'
      || saveState === 'conflict'
      || (workflowContextRequested && (!workflowProposalId || proposal.id !== workflowProposalId))
      || (workflowContextRequested && !linkedProposalBaseMatchesDraft)
    ) return
    setProposalBusyId(proposal.id)
    setError(null)
    try {
      await workspace.applyProposal(proposal.id, selectedOperations[proposal.id] ?? [])
      if (proposal.id === workflowProposalId) setConfirmedAppliedProposalId(proposal.id)
      setSaveState('idle')
      setTab('versions')
      await loadDetails()
    } catch (cause) {
      setError(errorMessage(cause, t('teamPlatform.pageSpec.operationFailed')))
    } finally {
      setProposalBusyId('')
    }
  }

  async function restoreLinkedProposalBase() {
    if (
      proposalBaseRestoreBusy
      || proposalBusyId
      || saveInFlightRef.current
      || !linkedProposal
      || !linkedProposalBaseRecoverable
      || !resource
    ) return
    setProposalBaseRestoreBusy(true)
    setError(null)
    try {
      const restored = await workspace.restorePageSpecDraftToRevision(
        resource.artifact.id,
        linkedProposal.baseRevision,
      )
      const restoredContent = normalizePageSpecContent(restored.content)
      queuedContentRef.current = null
      contentRef.current = restoredContent
      draftEtagRef.current = restored.etag
      setContent(restoredContent)
      setDraftEtag(restored.etag)
      setSaveState('saved')
      setConfirmProposalBaseRestore(false)
      setTab('proposal')
      await loadDetails()
    } catch (cause) {
      if (cause instanceof ArtifactWorkspaceConflictError) setSaveState('conflict')
      setError(errorMessage(cause, t('teamPlatform.pageSpec.operationFailed')))
    } finally {
      setProposalBaseRestoreBusy(false)
    }
  }

  if (!resource || !content) {
    return (
      <div className="rounded-lg border border-dashed border-border bg-panel p-6 text-center">
        <AlertTriangle className="mx-auto size-6 text-warning" />
        <p className="mt-2 text-sm font-semibold text-foreground">{t('teamPlatform.pageSpec.unavailableTitle')}</p>
        <p className="mt-1 text-[10px] text-muted-foreground">{t('teamPlatform.pageSpec.unavailableDetail')}</p>
      </div>
    )
  }

  return (
    <section className="overflow-hidden rounded-xl border border-border bg-background">
      <header className="flex flex-wrap items-center gap-2 border-b border-border bg-panel px-3 py-2.5">
        {onBack && <button type="button" onClick={onBack} className="rounded border border-border p-1.5 text-muted-foreground" aria-label={t('teamPlatform.pageSpec.backToList')} title={t('teamPlatform.pageSpec.backToList')}><ArrowLeft className="size-3.5" /></button>}
        <ShieldCheck className="size-4 text-primary-bright" />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[12px] font-semibold text-foreground">{resource.artifact.title}</span>
          <span className="block truncate font-mono text-[8px] text-faint-foreground">{resource.artifact.id} · {t('teamPlatform.pageSpec.etag', { etag: draftEtag || t('teamPlatform.common.missing') })}</span>
        </span>
        <span className={cn(
          'rounded px-2 py-1 text-[8px] font-semibold',
          resource.artifact.status === 'approved'
            ? 'bg-success/15 text-success'
            : 'bg-white/5 text-muted-foreground',
        )}>{artifactStatusLabel(resource.artifact.status, t)}</span>
        <span className={cn(
          'inline-flex items-center gap-1 rounded px-2 py-1 text-[8px]',
          saveState === 'conflict' || saveState === 'error'
            ? 'bg-warning/10 text-warning'
            : saveState === 'dirty'
              ? 'bg-warning/10 text-warning'
              : 'bg-success/10 text-success',
        )}>
          {saveState === 'saving' ? <Loader2 className="size-3 animate-spin" /> : <Save className="size-3" />}
          {saveStateLabel(saveState, t)}
        </span>
        <button type="button" onClick={() => void workspace.refresh()} className="rounded border border-border p-1.5 text-muted-foreground" aria-label={t('teamPlatform.pageSpec.refresh')} title={t('teamPlatform.pageSpec.refresh')}><RefreshCw className="size-3.5" /></button>
      </header>

      {error && (
        <div role="alert" className="border-b border-warning/30 bg-warning/10 px-3 py-2 text-[9px] text-warning">
          {error}
          {saveState === 'conflict' && <button type="button" onClick={() => void useServerDraft()} className="ml-3 underline">{t('teamPlatform.editor.useServerDraft')}</button>}
        </div>
      )}

      {workflowProposalId && (
        <div className="flex flex-wrap items-start gap-2 border-b border-primary/25 bg-primary/5 px-3 py-2.5" data-testid="page-spec-workflow-proposal-guide">
          <ShieldCheck className="mt-0.5 size-3.5 shrink-0 text-primary-bright" />
          <div className="min-w-0 flex-1">
            <p className="text-[10px] font-semibold text-foreground">{t('teamPlatform.pageSpec.workflowProposalTitle')}</p>
            <code className="mt-0.5 block break-all text-[8px] text-faint-foreground">{workflowProposalId}</code>
            <p className="mt-1 text-[9px] leading-relaxed text-muted-foreground">
              {!linkedProposal
                ? t('teamPlatform.pageSpec.workflowProposalUnavailable')
                : workflowRevisionReady
                  ? t('teamPlatform.pageSpec.workflowRevisionReady')
                  : linkedProposalApplied
                    ? t('teamPlatform.pageSpec.workflowProposalApplied')
                    : linkedProposalBaseMatchesDraft
                      ? t('teamPlatform.pageSpec.workflowProposalReview')
                      : linkedProposalBaseRecoverable
                        ? t('teamPlatform.pageSpec.workflowProposalStaleDraft')
                        : t('teamPlatform.pageSpec.workflowProposalBaseUnavailable')}
            </p>
          </div>
          {linkedProposalBaseRecoverable && (
            <div className="flex max-w-sm flex-col items-end gap-1.5">
              {confirmProposalBaseRestore ? (
                <>
                  <p className="text-right text-[8px] leading-relaxed text-warning">
                    {t('teamPlatform.pageSpec.restoreProposalBaseWarning')}
                  </p>
                  <span className="flex gap-1.5">
                    <button type="button" onClick={() => setConfirmProposalBaseRestore(false)} disabled={proposalBaseRestoreBusy} className="rounded border border-border px-2.5 py-1.5 text-[9px] text-muted-foreground disabled:opacity-40">
                      {t('common.cancel')}
                    </button>
                    <button type="button" onClick={() => void restoreLinkedProposalBase()} disabled={proposalBaseRestoreBusy || Boolean(proposalBusyId)} className="rounded bg-warning px-2.5 py-1.5 text-[9px] font-semibold text-warning-foreground disabled:opacity-40">
                      {proposalBaseRestoreBusy ? <Loader2 className="mr-1 inline size-3 animate-spin" /> : <RotateCcw className="mr-1 inline size-3" />}
                      {proposalBaseRestoreBusy ? t('teamPlatform.pageSpec.restoringProposalBase') : t('teamPlatform.pageSpec.confirmRestoreProposalBase')}
                    </button>
                  </span>
                </>
              ) : (
                <button type="button" onClick={() => setConfirmProposalBaseRestore(true)} disabled={proposalBaseRestoreBusy || Boolean(proposalBusyId)} className="rounded border border-warning/40 bg-warning/10 px-2.5 py-1.5 text-[9px] font-semibold text-warning disabled:opacity-40">
                  <RotateCcw className="mr-1 inline size-3" />{t('teamPlatform.pageSpec.restoreProposalBase')}
                </button>
              )}
            </div>
          )}
          {workflowRevisionReady && (
            <button type="button" onClick={() => setSurface('workbench')} className="rounded bg-primary px-2.5 py-1.5 text-[9px] font-semibold text-primary-foreground">
              <Send className="mr-1 inline size-3" />{t('teamPlatform.pageSpec.returnToWorkflow')}
            </button>
          )}
        </div>
      )}

      {workflowReference.runId && !workflowProposalId && (
        <div className="flex items-start gap-2 border-b border-warning/30 bg-warning/10 px-3 py-2.5" data-testid="page-spec-workflow-proposal-guide">
          {flow.run?.id === workflowReference.runId && flow.runDefinition
            ? <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warning" />
            : <Loader2 className="mt-0.5 size-3.5 shrink-0 animate-spin text-warning" />}
          <div className="min-w-0 flex-1">
            <p className="text-[10px] font-semibold text-foreground">{t('teamPlatform.pageSpec.workflowProposalTitle')}</p>
            <p className="mt-1 text-[9px] leading-relaxed text-warning">
              {flow.run?.id === workflowReference.runId && flow.runDefinition
                ? t('teamPlatform.pageSpec.workflowProposalUnresolved')
                : t('teamPlatform.pageSpec.workflowProposalResolving')}
            </p>
          </div>
        </div>
      )}

      <nav className="flex overflow-x-auto border-b border-border bg-panel p-1 scrollbar-thin">
        {([
          ['content', t('teamPlatform.pageSpec.tab.basics')],
          ['states', t('teamPlatform.pageSpec.tab.states', { count: content.states.length.toLocaleString(locale) })],
          ['data', t('teamPlatform.pageSpec.tab.data', { count: content.dataBindings.length.toLocaleString(locale) })],
          ['interactions', t('teamPlatform.pageSpec.tab.interactions', { count: content.interactions.length.toLocaleString(locale) })],
          ['versions', t('teamPlatform.editor.tab.versions', { count: (details?.versions.length ?? 0).toLocaleString(locale) })],
          ['proposal', t('teamPlatform.editor.tab.proposals', { count: proposals.length.toLocaleString(locale) })],
          ['trace', t('teamPlatform.editor.tab.trace', { count: (details?.dependencies.length ?? 0).toLocaleString(locale) })],
          ['review', t('teamPlatform.editor.tab.review', { count: (comments.length + reviews.length).toLocaleString(locale) })],
        ] as const).map(([id, label]) => {
          const workflowLocked = id === 'review' && workflowReviewLocked
          return <button
            key={id}
            type="button"
            onClick={() => setTab(id)}
            disabled={workflowLocked}
            title={workflowLocked ? t('teamPlatform.pageSpec.workflowReviewLocked') : undefined}
            className={cn('shrink-0 rounded px-2.5 py-1.5 text-[9px] font-medium disabled:cursor-not-allowed disabled:opacity-40', tab === id ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground')}
          >{label}</button>
        })}
      </nav>

      <div className="max-h-[680px] overflow-y-auto p-4 scrollbar-thin">
        {tab === 'content' && <BasicsEditor content={content} readOnly={!contentCanEdit} onChange={updateContent} />}
        {tab === 'states' && <StatesEditor states={content.states} readOnly={!contentCanEdit} onChange={(states) => updateContent({ states })} />}
        {tab === 'data' && <DataBindingsEditor bindings={content.dataBindings} readOnly={!contentCanEdit} onChange={(dataBindings) => updateContent({ dataBindings })} />}
        {tab === 'interactions' && <InteractionsEditor interactions={content.interactions} readOnly={!contentCanEdit} onChange={(interactions) => updateContent({ interactions })} />}
        {tab === 'versions' && (
          <div className="space-y-3">
            <ReviewGatePanel clientIssues={clientIssues} serverGate={details?.reviewGate} />
            <button type="button" onClick={() => void createRevision()} disabled={!canEdit || clientIssues.length > 0 || dirty || !hasUnversionedChanges || saveState === 'saving' || saveState === 'conflict' || Boolean(workflowContextRequested && (!workflowProposalId || !linkedProposalApplied))} className="inline-flex items-center gap-1.5 rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-40"><GitBranch className="size-3.5" />{t('teamPlatform.pageSpec.createRevision')}</button>
            {dirty && <p className="text-[9px] text-warning">{t('teamPlatform.pageSpec.waitAutosave')}</p>}
            {workflowProposalId && !linkedProposalApplied && <p className="text-[9px] text-warning">{t('teamPlatform.pageSpec.applyWorkflowProposalFirst')}</p>}
            {!dirty && !hasUnversionedChanges && <p className="text-[9px] text-faint-foreground">{t('teamPlatform.pageSpec.draftMatchesRevision')}</p>}
            {details?.versions.map((revision) => <RevisionCard key={revision.id} revision={revision} />)}
          </div>
        )}
        {tab === 'proposal' && (
          <ProposalEditor
            proposals={proposals}
            selected={selectedOperations}
            onSelected={setSelectedOperations}
            instruction={proposalInstruction}
            onInstruction={setProposalInstruction}
            canEdit={canEdit && (!workflowContextRequested || Boolean(workflowProposalId))}
            canCreate={Boolean(latestVersion) && !workflowContextRequested && draftMatchesLatest && !dirty && saveState !== 'saving' && saveState !== 'conflict'}
            linkedProposalId={workflowProposalId}
            proposalBusyId={proposalBusyId}
            applyBlocked={dirty || saveState === 'saving' || saveState === 'conflict' || proposalBaseRestoreBusy || Boolean(workflowContextRequested && (!workflowProposalId || !linkedProposalBaseMatchesDraft))}
            onCreate={() => void workspace.createProposal({
              jobType: 'page_spec.patch',
              targetRevision: latestVersion!,
              instruction: proposalInstruction,
              inputVersions: resource.draft?.sourceVersions ?? [],
              outputSchemaVersion: 'page-spec.patch.v1',
            }).catch((cause) => setError(errorMessage(cause, t('teamPlatform.pageSpec.operationFailed'))))}
            onApply={(proposal) => void applyProposal(proposal)}
          />
        )}
        {tab === 'trace' && (
          <div className="space-y-2">
            {details?.dependencies.map((dependency) => <div key={dependency.id} className="rounded border border-border bg-panel p-3 text-[9px] text-muted-foreground"><Link2 className="mr-1 inline size-3 text-primary-bright" /><code>{dependency.source.artifactId}:{dependency.source.revisionId}</code> {relationLabel(dependency.relation, t)} <code>{dependency.target.artifactId}:{dependency.target.revisionId}</code>{dependency.required && <span className="ml-2 text-warning">{t('blueprint.required')}</span>}</div>)}
            {workspace.traces.filter((trace) => trace.source.artifactId === artifactId || trace.target.artifactId === artifactId).map((trace) => <div key={trace.id} className="rounded border border-border bg-panel p-3 text-[9px] text-muted-foreground"><code>{trace.source.artifactId}:{trace.source.revisionId}</code> → {relationLabel(trace.relation, t)} → <code>{trace.target.artifactId}:{trace.target.revisionId}</code></div>)}
          </div>
        )}
        {tab === 'review' && (
          <ReviewEditor
            latestVersion={latestVersion}
            gatePassed={gatePassed && !workflowReviewLocked}
            gate={<ReviewGatePanel clientIssues={clientIssues} serverGate={details?.reviewGate} />}
            comment={comment}
            onComment={setComment}
            reviewSummary={reviewSummary}
            onReviewSummary={setReviewSummary}
            reviewerId={reviewerId}
            onReviewerId={setReviewerId}
            currentUserId={currentUserId}
            onError={setError}
          />
        )}
      </div>
    </section>
  )
}

function BasicsEditor({ content, readOnly, onChange }: { content: PageSpecContentDto; readOnly: boolean; onChange: (patch: Partial<PageSpecContentDto>) => void }) {
  const { t } = useI18n()
  return (
    <div className="mx-auto max-w-4xl space-y-4">
      <div className="grid gap-3 md:grid-cols-2">
        <Field label={t('teamPlatform.pageSpec.blueprintPageNodeId')}><input value={content.blueprintPageNodeId} readOnly className={inputClass(true)} /></Field>
        <Field label={t('teamPlatform.blueprint.nodeTitle')}><input value={content.title} readOnly={readOnly} onChange={(event) => onChange({ title: event.target.value })} className={inputClass(readOnly)} /></Field>
        <Field label={t('prototype.route')}><input value={content.route} readOnly={readOnly} onChange={(event) => onChange({ route: event.target.value })} className={inputClass(readOnly)} placeholder="/orders" /></Field>
        <Field label={t('teamPlatform.pageSpec.requiredRoles')}><input value={content.requiredRoles.join(', ')} readOnly={readOnly} onChange={(event) => onChange({ requiredRoles: commaList(event.target.value) })} className={inputClass(readOnly)} placeholder="admin, editor" /></Field>
      </div>
      <Field label={t('teamPlatform.blueprint.userGoal')}><textarea value={content.userGoal} readOnly={readOnly} onChange={(event) => onChange({ userGoal: event.target.value })} rows={3} className={textareaClass(readOnly)} /></Field>
      <div className="grid gap-3 md:grid-cols-2">
        <Field label={t('teamPlatform.pageSpec.entryPoints')}><textarea value={content.entryPoints.join('\n')} readOnly={readOnly} onChange={(event) => onChange({ entryPoints: lineList(event.target.value) })} rows={4} className={textareaClass(readOnly)} placeholder={t('teamPlatform.pageSpec.entryPointsPlaceholder')} /></Field>
        <Field label={t('teamPlatform.pageSpec.exitPoints')}><textarea value={content.exitPoints.join('\n')} readOnly={readOnly} onChange={(event) => onChange({ exitPoints: lineList(event.target.value) })} rows={4} className={textareaClass(readOnly)} placeholder={t('teamPlatform.pageSpec.exitPointsPlaceholder')} /></Field>
      </div>
      <Field label={t('teamPlatform.pageSpec.stableAcceptanceIds')}><textarea value={content.acceptanceCriterionIds.join('\n')} readOnly={readOnly} onChange={(event) => onChange({ acceptanceCriterionIds: lineList(event.target.value) })} rows={4} className={textareaClass(readOnly)} placeholder="AC-ORDER-001" /></Field>
      <Field label={t('teamPlatform.pageSpec.nonFunctionalConstraints')}><textarea value={content.nonFunctionalConstraints.join('\n')} readOnly={readOnly} onChange={(event) => onChange({ nonFunctionalConstraints: lineList(event.target.value) })} rows={4} className={textareaClass(readOnly)} placeholder={t('teamPlatform.pageSpec.constraintsPlaceholder')} /></Field>
    </div>
  )
}

function StatesEditor({ states, readOnly, onChange }: { states: readonly PageStateDto[]; readOnly: boolean; onChange: (states: readonly PageStateDto[]) => void }) {
  const { t } = useI18n()
  function update(index: number, patch: Partial<PageStateDto>) {
    onChange(states.map((state, stateIndex) => stateIndex === index ? { ...state, ...patch } : state))
  }
  function restoreRequired() {
    const existing = new Set(states.map((state) => state.key))
    onChange([
      ...states,
      ...REQUIRED_PAGE_STATE_KEYS.filter((key) => !existing.has(key)).map((key) => requiredState(key, t)),
    ])
  }
  return (
    <div className="mx-auto max-w-4xl space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-sm font-semibold text-foreground">{t('teamPlatform.pageSpec.pageStates')}</h3>
        <button type="button" onClick={restoreRequired} disabled={readOnly} className="ml-auto rounded border border-border px-2 py-1.5 text-[9px] text-muted-foreground disabled:opacity-40">{t('teamPlatform.pageSpec.restoreStates')}</button>
        <button type="button" onClick={() => onChange([...states, { id: stableId('state'), key: `custom_${states.length + 1}`, title: t('teamPlatform.pageSpec.customState'), required: false, fixtureIds: [], acceptanceCriterionIds: [] }])} disabled={readOnly} className="rounded bg-primary px-2 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Plus className="mr-1 inline size-3" />{t('teamPlatform.pageSpec.state')}</button>
      </div>
      {states.map((state, index) => (
        <article key={state.id} className="rounded-lg border border-border bg-panel p-3">
          <div className="flex flex-wrap items-center gap-2">
            <code className="text-[8px] text-faint-foreground">{state.id}</code>
            <label className="ml-auto flex items-center gap-1 text-[9px] text-muted-foreground"><input type="checkbox" checked={state.required} disabled={readOnly} onChange={(event) => update(index, { required: event.target.checked })} />{t('blueprint.required')}</label>
            <button type="button" disabled={readOnly} onClick={() => onChange(states.filter((_, stateIndex) => stateIndex !== index))} aria-label={t('teamPlatform.pageSpec.deleteState', { title: state.title })} title={t('teamPlatform.pageSpec.deleteState', { title: state.title })}><Trash2 className="size-3.5 text-destructive" /></button>
          </div>
          <div className="mt-2 grid gap-2 md:grid-cols-2">
            <Field label={t('teamPlatform.pageSpec.stableKey')}><input value={state.key} readOnly={readOnly} onChange={(event) => update(index, { key: event.target.value })} className={inputClass(readOnly)} /></Field>
            <Field label={t('teamPlatform.blueprint.nodeTitle')}><input value={state.title} readOnly={readOnly} onChange={(event) => update(index, { title: event.target.value })} className={inputClass(readOnly)} /></Field>
            <Field label={t('teamPlatform.blueprint.description')}><textarea value={state.description ?? ''} readOnly={readOnly} onChange={(event) => update(index, { description: event.target.value })} rows={2} className={textareaClass(readOnly)} /></Field>
            <Field label={t('teamPlatform.pageSpec.entryCondition')}><textarea value={state.entryCondition ?? ''} readOnly={readOnly} onChange={(event) => update(index, { entryCondition: event.target.value })} rows={2} className={textareaClass(readOnly)} /></Field>
            <Field label={t('teamPlatform.pageSpec.fixtureIds')}><input value={state.fixtureIds.join(', ')} readOnly={readOnly} onChange={(event) => update(index, { fixtureIds: commaList(event.target.value) })} className={inputClass(readOnly)} /></Field>
            <Field label={t('teamPlatform.editor.content.acceptanceIds')}><input value={state.acceptanceCriterionIds.join(', ')} readOnly={readOnly} onChange={(event) => update(index, { acceptanceCriterionIds: commaList(event.target.value) })} className={inputClass(readOnly)} /></Field>
          </div>
        </article>
      ))}
    </div>
  )
}

function DataBindingsEditor({ bindings, readOnly, onChange }: { bindings: readonly PageDataBindingDto[]; readOnly: boolean; onChange: (bindings: readonly PageDataBindingDto[]) => void }) {
  const { t } = useI18n()
  function update(index: number, patch: Partial<PageDataBindingDto>) {
    onChange(bindings.map((binding, bindingIndex) => bindingIndex === index ? { ...binding, ...patch } : binding))
  }
  return (
    <div className="mx-auto max-w-4xl space-y-3">
      <div className="flex items-center"><Database className="mr-2 size-4 text-primary-bright" /><h3 className="text-sm font-semibold text-foreground">{t('teamPlatform.pageSpec.dataBindings')}</h3><button type="button" onClick={() => onChange([...bindings, { id: stableId('binding'), name: t('teamPlatform.pageSpec.defaultBindingName'), source: 'api', operationId: '', schema: {}, required: true }])} disabled={readOnly} className="ml-auto rounded bg-primary px-2 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Plus className="mr-1 inline size-3" />{t('teamPlatform.pageSpec.binding')}</button></div>
      {bindings.map((binding, index) => (
        <article key={binding.id} className="rounded-lg border border-border bg-panel p-3">
          <div className="flex items-center gap-2"><code className="text-[8px] text-faint-foreground">{binding.id}</code><label className="ml-auto flex items-center gap-1 text-[9px] text-muted-foreground"><input type="checkbox" checked={binding.required} disabled={readOnly} onChange={(event) => update(index, { required: event.target.checked })} />{t('blueprint.required')}</label><button type="button" disabled={readOnly} onClick={() => onChange(bindings.filter((_, bindingIndex) => bindingIndex !== index))} aria-label={t('teamPlatform.pageSpec.deleteBinding', { name: binding.name })} title={t('teamPlatform.pageSpec.deleteBinding', { name: binding.name })}><Trash2 className="size-3.5 text-destructive" /></button></div>
          <div className="mt-2 grid gap-2 md:grid-cols-2">
            <Field label={t('teamPlatform.common.name')}><input value={binding.name} readOnly={readOnly} onChange={(event) => update(index, { name: event.target.value })} className={inputClass(readOnly)} /></Field>
            <Field label={t('teamPlatform.pageSpec.source')}><select value={binding.source} disabled={readOnly} onChange={(event) => update(index, { source: event.target.value as PageDataBindingDto['source'] })} className={inputClass(readOnly)}><option value="api">{dataSourceLabel('api', t)}</option><option value="database">{dataSourceLabel('database', t)}</option><option value="fixture">{dataSourceLabel('fixture', t)}</option><option value="local">{dataSourceLabel('local', t)}</option></select></Field>
            <Field label={t('teamPlatform.pageSpec.stableOperationId')}><input value={binding.operationId ?? ''} readOnly={readOnly} onChange={(event) => update(index, { operationId: event.target.value })} className={inputClass(readOnly)} placeholder="orders.list" /></Field>
            <JsonObjectEditor value={binding.schema} readOnly={readOnly} onChange={(schema) => update(index, { schema })} />
          </div>
        </article>
      ))}
      {bindings.length === 0 && <Empty text={t('teamPlatform.pageSpec.noBindings')} />}
    </div>
  )
}

function InteractionsEditor({ interactions, readOnly, onChange }: { interactions: readonly PageInteractionSpecDto[]; readOnly: boolean; onChange: (interactions: readonly PageInteractionSpecDto[]) => void }) {
  const { t } = useI18n()
  function update(index: number, patch: Partial<PageInteractionSpecDto>) {
    onChange(interactions.map((interaction, interactionIndex) => interactionIndex === index ? { ...interaction, ...patch } : interaction))
  }
  return (
    <div className="mx-auto max-w-4xl space-y-3">
      <div className="flex items-center"><MousePointerClick className="mr-2 size-4 text-primary-bright" /><h3 className="text-sm font-semibold text-foreground">{t('teamPlatform.pageSpec.interactions')}</h3><button type="button" onClick={() => onChange([...interactions, { id: stableId('interaction'), trigger: '', outcome: '', acceptanceCriterionIds: [] }])} disabled={readOnly} className="ml-auto rounded bg-primary px-2 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Plus className="mr-1 inline size-3" />{t('teamPlatform.pageSpec.interaction')}</button></div>
      {interactions.map((interaction, index) => (
        <article key={interaction.id} className="rounded-lg border border-border bg-panel p-3">
          <div className="flex items-center gap-2"><code className="text-[8px] text-faint-foreground">{interaction.id}</code><button type="button" disabled={readOnly} onClick={() => onChange(interactions.filter((_, interactionIndex) => interactionIndex !== index))} className="ml-auto" aria-label={t('teamPlatform.pageSpec.deleteInteraction', { id: interaction.id })} title={t('teamPlatform.pageSpec.deleteInteraction', { id: interaction.id })}><Trash2 className="size-3.5 text-destructive" /></button></div>
          <div className="mt-2 grid gap-2 md:grid-cols-2">
            <Field label={t('teamPlatform.pageSpec.trigger')}><input value={interaction.trigger} readOnly={readOnly} onChange={(event) => update(index, { trigger: event.target.value })} className={inputClass(readOnly)} placeholder={t('teamPlatform.pageSpec.triggerPlaceholder')} /></Field>
            <Field label={t('teamPlatform.pageSpec.outcome')}><input value={interaction.outcome} readOnly={readOnly} onChange={(event) => update(index, { outcome: event.target.value })} className={inputClass(readOnly)} placeholder={t('teamPlatform.pageSpec.outcomePlaceholder')} /></Field>
            <Field label={t('teamPlatform.pageSpec.targetPageNodeId')}><input value={interaction.targetPageNodeId ?? interaction.targetPageSpecId ?? ''} readOnly={readOnly} onChange={(event) => update(index, { targetPageNodeId: event.target.value, targetPageSpecId: undefined })} className={inputClass(readOnly)} placeholder="page-checkout" /></Field>
            <Field label={t('teamPlatform.editor.content.acceptanceIds')}><input value={interaction.acceptanceCriterionIds.join(', ')} readOnly={readOnly} onChange={(event) => update(index, { acceptanceCriterionIds: commaList(event.target.value) })} className={inputClass(readOnly)} /></Field>
          </div>
        </article>
      ))}
      {interactions.length === 0 && <Empty text={t('teamPlatform.pageSpec.noInteractions')} />}
    </div>
  )
}

function ProposalEditor({ proposals, selected, onSelected, instruction, onInstruction, canEdit, canCreate, linkedProposalId, proposalBusyId, applyBlocked, onCreate, onApply }: { proposals: readonly ProposalDto[]; selected: Record<string, string[]>; onSelected: (next: Record<string, string[]>) => void; instruction: string; onInstruction: (value: string) => void; canEdit: boolean; canCreate: boolean; linkedProposalId: string; proposalBusyId: string; applyBlocked: boolean; onCreate: () => void; onApply: (proposal: ProposalDto) => void }) {
  const { t } = useI18n()
  return (
    <div className="mx-auto max-w-4xl space-y-3">
      {!linkedProposalId && (
        <div className="rounded-lg border border-border bg-panel p-3">
          <textarea value={instruction} onChange={(event) => onInstruction(event.target.value)} rows={3} className={textareaClass(false)} aria-label={t('teamPlatform.pageSpec.proposalInstruction')} />
          <button type="button" onClick={onCreate} disabled={!canEdit || !canCreate || !instruction.trim()} className="mt-2 rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-40"><Bot className="mr-1 inline size-3" />{t('teamPlatform.pageSpec.askAi')}</button>
          {!canCreate && <p className="mt-2 text-[9px] text-warning">{t('teamPlatform.pageSpec.revisionBeforeAi')}</p>}
        </div>
      )}
      {proposals.map((proposal) => {
        const operations = proposal.operations ?? []
        const assumptions = proposal.assumptions ?? []
        const questions = proposal.questions ?? []
        const selectedIds = selected[proposal.id] ?? []
        const hasAccepted = operations.some((operation) => operation.decision === 'accepted' || selectedIds.includes(operation.id))
        const isLinked = !linkedProposalId || proposal.id === linkedProposalId
        return (
          <article key={proposal.id} className={cn('rounded-lg border bg-panel p-3', isLinked ? 'border-primary/35' : 'border-border opacity-70')}>
            <div className="flex flex-wrap items-center gap-2"><Wand2 className="size-3.5 text-primary-bright" /><span className="text-[10px] font-semibold text-foreground">{t('teamPlatform.pageSpec.manifest', { id: proposal.manifest.id.slice(0, 12) })}</span><span className="rounded bg-primary/10 px-1.5 py-0.5 text-[8px] text-primary-bright">{proposalStatusLabel(proposal.status, t)}</span>{linkedProposalId && isLinked && <span className="rounded bg-success/10 px-1.5 py-0.5 text-[8px] font-semibold text-success">{t('teamPlatform.pageSpec.workflowLinked')}</span>}<code className="ml-auto text-[8px] text-faint-foreground">{t('teamPlatform.pageSpec.baseHash', { hash: proposal.baseRevision.contentHash.slice(0, 12) })}</code></div>
            <code className="mt-1 block break-all text-[8px] text-faint-foreground">{proposal.id}</code>
            {assumptions.length > 0 && <p className="mt-2 text-[9px] text-muted-foreground">{t('teamPlatform.pageSpec.assumptions', { values: assumptions.join(' · ') })}</p>}
            {questions.length > 0 && <p className="mt-1 text-[9px] text-warning">{t('teamPlatform.pageSpec.questions', { values: questions.join(' · ') })}</p>}
            <div className="mt-2 space-y-1.5">
              {operations.map((operation) => (
                <label key={operation.id} className="flex gap-2 rounded border border-border bg-background p-2 text-[9px] text-muted-foreground">
                  <input type="checkbox" disabled={!canEdit || !isLinked || Boolean(proposalBusyId) || operation.decision !== 'pending'} checked={operation.decision === 'accepted' || operation.decision === 'applied' || selectedIds.includes(operation.id)} onChange={(event) => onSelected({ ...selected, [proposal.id]: event.target.checked ? [...selectedIds, operation.id] : selectedIds.filter((id) => id !== operation.id) })} />
                  <span className="min-w-0 flex-1"><code>{proposalOperationLabel(operation.kind, t)} {operation.path || '/'}</code><span className="ml-2 text-faint-foreground">{proposalDecisionLabel(operation.decision, t)}</span>{operation.rationale && <span className="mt-1 block">{operation.rationale}</span>}{operation.value !== undefined && <pre className="mt-1 max-h-36 overflow-auto whitespace-pre-wrap rounded bg-black/20 p-1.5 font-mono text-[8px]">{JSON.stringify(operation.value, null, 2)}</pre>}</span>
                </label>
              ))}
            </div>
            <button type="button" onClick={() => onApply(proposal)} disabled={!canEdit || !isLinked || applyBlocked || Boolean(proposalBusyId) || !hasAccepted || !['open', 'reviewing', 'ready'].includes(proposal.status)} className="mt-2 rounded bg-primary px-2.5 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40">{proposalBusyId === proposal.id ? t('teamPlatform.pageSpec.applyingProposal') : t('teamPlatform.pageSpec.applySelected')}</button>
            {linkedProposalId && !isLinked && <p className="mt-1 text-[8px] text-faint-foreground">{t('teamPlatform.pageSpec.notWorkflowProposal')}</p>}
          </article>
        )
      })}
      {proposals.length === 0 && <Empty text={t('teamPlatform.pageSpec.noProposals')} />}
    </div>
  )
}

function ReviewEditor({ latestVersion, gatePassed, gate, comment, onComment, reviewSummary, onReviewSummary, reviewerId, onReviewerId, currentUserId, onError }: { latestVersion?: VersionRefDto; gatePassed: boolean; gate: React.ReactNode; comment: string; onComment: (value: string) => void; reviewSummary: string; onReviewSummary: (value: string) => void; reviewerId: string; onReviewerId: (value: string) => void; currentUserId: string | null; onError: (value: string | null) => void }) {
  const { locale, t } = useI18n()
  const collaboration = useCollaboration()
  const governanceReviewers = reviewCandidatesForGovernance(
    collaboration.members,
    currentUserId,
    collaboration.project?.governanceMode ?? 'team',
  )
  const effectiveReviewerId = collaboration.project?.governanceMode === 'solo'
    ? governanceReviewers[0]?.user.id ?? ''
    : reviewerId
  const artifactId = latestVersion?.artifactId
  const comments = collaboration.comments.filter((thread) =>
    thread.target?.artifactId === artifactId
    && thread.target?.revisionId === latestVersion?.revisionId,
  )
  const reviews = collaboration.reviews.filter((review) =>
    review.target?.artifactId === artifactId
    && review.target?.revisionId === latestVersion?.revisionId,
  )
  return (
    <div className="mx-auto max-w-4xl space-y-3">
      {gate}
      {!latestVersion && <Empty text={t('teamPlatform.pageSpec.createRevisionFirst')} />}
      {latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_auto]"><input value={comment} onChange={(event) => onComment(event.target.value)} className={inputClass(false)} placeholder={t('teamPlatform.pageSpec.commentPlaceholder')} aria-label={t('teamPlatform.pageSpec.commentPlaceholder')} /><button type="button" onClick={() => void collaboration.addComment(comment, undefined, latestVersion).then((ok) => ok && onComment('')).catch((cause) => onError(errorMessage(cause, t('teamPlatform.pageSpec.operationFailed'))))} disabled={!comment.trim() || !collaboration.can('comment')} className="rounded bg-primary px-3 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><MessageSquare className="mr-1 inline size-3" />{t('teamPlatform.editor.comment')}</button></div>}
      {latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_180px_auto]"><input value={reviewSummary} onChange={(event) => onReviewSummary(event.target.value)} className={inputClass(false)} aria-label={t('teamPlatform.pageSpec.reviewSummary')} /><select value={effectiveReviewerId} onChange={(event) => onReviewerId(event.target.value)} disabled={collaboration.project?.governanceMode === 'solo'} className={inputClass(false)} aria-label={t('teamPlatform.pageSpec.reviewer')}><option value="">{t('reviews.reviewer')}</option>{governanceReviewers.map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name}</option>)}</select><button type="button" onClick={() => void collaboration.requestReview(reviewSummary, latestVersion, [effectiveReviewerId]).catch((cause) => onError(errorMessage(cause, t('teamPlatform.pageSpec.operationFailed'))))} disabled={!gatePassed || !effectiveReviewerId || !reviewSummary.trim()} className="rounded bg-primary px-3 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Send className="mr-1 inline size-3" />{t('editor.requestReview')}</button></div>}
      {comments.map((thread) => <div key={thread.id} className="rounded border border-border bg-panel p-3"><span className="text-[10px] font-medium text-foreground">{thread.author.name}</span><p className="mt-1 text-[9px] text-muted-foreground">{thread.body}</p><p className="mt-1 text-[8px] text-faint-foreground">{t('teamPlatform.pageSpec.exactRevisionHash', { number: thread.target?.revisionNumber?.toLocaleString(locale) ?? t('teamPlatform.common.unknown'), hash: thread.target?.contentHash.slice(0, 12) ?? t('teamPlatform.common.unknown') })}</p></div>)}
      {reviews.map((review) => <div key={review.id} className="rounded border border-border bg-panel p-3 text-[9px]"><span className="font-semibold text-foreground">{reviewStateLabel(review.state ?? 'pending', t)}</span><span className="ml-2 text-muted-foreground">{review.summary}</span><p className="mt-1 text-faint-foreground">{t('teamPlatform.pageSpec.exactRevisionReviewCenter', { number: review.target?.revisionNumber?.toLocaleString(locale) ?? t('teamPlatform.common.unknown') })}</p></div>)}
    </div>
  )
}

function ReviewGatePanel({ clientIssues, serverGate }: { clientIssues: readonly string[]; serverGate?: ArtifactReviewGateDto }) {
  const { locale, t } = useI18n()
  const serverErrors = serverGate?.checks.filter(
    (check) => check.severity === 'error' && check.code !== 'canonical_review_approved',
  ) ?? []
  const draftIssues = uniqueStrings([
    ...clientIssues.map((issue) => pageSpecIssueLabel(issue, t)),
    ...serverErrors
      .filter((check) => check.code === 'draft_matches_latest_revision')
      .map((check) => pageSpecIssueLabel(check.message, t)),
  ])
  const revisionIssues = uniqueStrings(serverErrors
    .filter((check) => check.code !== 'draft_matches_latest_revision')
    .map((check) => pageSpecIssueLabel(check.message, t)))
  const ready = draftIssues.length === 0
    && revisionIssues.length === 0
    && reviewGateReadyForRequest(serverGate)
  return (
    <div className={cn('rounded-lg border p-3', ready ? 'border-success/30 bg-success/10' : 'border-warning/30 bg-warning/10')}>
      <div className="flex items-center gap-2 text-[10px] font-semibold text-foreground">{ready ? <CheckCircle2 className="size-4 text-success" /> : <AlertTriangle className="size-4 text-warning" />}{t('teamPlatform.pageSpec.reviewGateStatus', { status: ready ? t('teamPlatform.common.ready') : t('teamPlatform.graph.status.blocked') })}</div>
      {draftIssues.length > 0 && <div className="mt-2"><p className="text-[8px] font-semibold uppercase tracking-wide text-faint-foreground">{t('teamPlatform.pageSpec.draftGateIssues')}</p>{draftIssues.map((issue) => <p key={issue} className="mt-1 text-[9px] text-muted-foreground">• {issue}</p>)}</div>}
      {revisionIssues.length > 0 && <div className="mt-2"><p className="text-[8px] font-semibold uppercase tracking-wide text-faint-foreground">{t('teamPlatform.pageSpec.revisionGateIssues')}</p>{revisionIssues.map((issue) => <p key={issue} className="mt-1 text-[9px] text-muted-foreground">• {issue}</p>)}</div>}
      {!serverGate && <p className="mt-1 text-[9px] text-warning">{t('teamPlatform.pageSpec.waitingServerGate')}</p>}
      {serverGate && <p className="mt-2 text-[8px] text-faint-foreground">{t('teamPlatform.pageSpec.serverGateSummary', { status: ready ? t('teamPlatform.common.ready') : t('teamPlatform.graph.status.blocked'), percent: new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(serverGate.traceCoverage * 100), count: serverGate.unresolvedBlockingCommentIds.length.toLocaleString(locale) })}</p>}
    </div>
  )
}

function JsonObjectEditor({ value, readOnly, onChange }: { value?: JsonObject; readOnly: boolean; onChange: (value?: JsonObject) => void }) {
  const { t } = useI18n()
  const serialized = JSON.stringify(value ?? {}, null, 2)
  const [draft, setDraft] = useState(serialized)
  const [focused, setFocused] = useState(false)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    if (!focused) setDraft(serialized)
  }, [focused, serialized])
  function commit() {
    setFocused(false)
    try {
      const parsed = JSON.parse(draft) as unknown
      if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
        throw new Error(t('teamPlatform.pageSpec.schemaObjectRequired'))
      }
      setError(null)
      onChange(parsed as JsonObject)
    } catch (cause) {
      setError(errorMessage(cause, t('teamPlatform.pageSpec.invalidJsonSchema')))
    }
  }
  return <Field label={t('teamPlatform.pageSpec.jsonSchema')}><textarea value={draft} readOnly={readOnly} onFocus={() => setFocused(true)} onChange={(event) => setDraft(event.target.value)} onBlur={commit} rows={5} className={textareaClass(readOnly)} />{error && <span className="mt-1 block text-[8px] text-destructive">{error}</span>}</Field>
}

function RevisionCard({ revision }: { revision: ArtifactRevisionDto<PageSpecContentDto> }) {
  const { locale, t } = useI18n()
  return <div className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><FileClock className="size-4 text-primary-bright" /><span className="text-[10px] font-semibold text-foreground">{t('teamPlatform.pageSpec.revisionNumber', { number: revision.revisionNumber.toLocaleString(locale) })}</span><code className="ml-auto text-[8px] text-faint-foreground">{revision.contentHash.slice(0, 16)}</code></div><p className="mt-1 text-[9px] text-muted-foreground">{formatDate(revision.createdAt, locale)} · {t('teamPlatform.pageSpec.pinnedSources', { count: (revision.sourceVersions?.length ?? 0).toLocaleString(locale) })}</p></div>
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return <label className="block text-[9px] font-medium text-muted-foreground">{label}<span className="mt-1 block">{children}</span></label>
}

function Empty({ text }: { text: string }) {
  return <p className="rounded border border-dashed border-border p-4 text-center text-[9px] text-faint-foreground">{text}</p>
}

type Translate = ReturnType<typeof useI18n>['t']

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

function saveStateLabel(state: SaveState, t: Translate) {
  const labels: Record<SaveState, string> = {
    idle: t('teamPlatform.editor.serverDraft'),
    dirty: t('teamPlatform.editor.pendingAutosave'),
    saving: t('teamPlatform.editor.autosaving'),
    saved: t('teamPlatform.pageSpec.saved'),
    conflict: t('teamPlatform.editor.conflict'),
    error: t('teamPlatform.pageSpec.saveFailed'),
  }
  return labels[state]
}

function relationLabel(relation: string, t: Translate) {
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

function dataSourceLabel(source: string, t: Translate) {
  const labels: Record<string, string> = {
    api: t('teamPlatform.pageSpec.source.api'),
    database: t('teamPlatform.pageSpec.source.database'),
    fixture: t('teamPlatform.pageSpec.source.fixture'),
    local: t('teamPlatform.pageSpec.source.local'),
  }
  return labels[source] ?? source
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

function reviewStateLabel(state: string, t: Translate) {
  const labels: Record<string, string> = {
    pending: t('teamPlatform.reviews.state.pending'),
    approved: t('doc.status.approved'),
    changesRequested: t('doc.status.changesRequested'),
  }
  return labels[state] ?? state
}

function pageSpecIssueLabel(issue: string, t: Translate) {
  const labels: Record<string, string> = {
    'A stable Blueprint page node ID is required.': t('teamPlatform.pageSpec.issue.blueprintNodeId'),
    'Page title is required.': t('teamPlatform.pageSpec.issue.title'),
    'Route is required and must start with /.': t('teamPlatform.pageSpec.issue.route'),
    'User goal is required.': t('teamPlatform.pageSpec.issue.userGoal'),
    'Every state needs a unique stable ID.': t('teamPlatform.pageSpec.issue.stateId'),
    'Every state needs a unique stable key.': t('teamPlatform.pageSpec.issue.stateKey'),
    'Every state needs a title.': t('teamPlatform.pageSpec.issue.stateTitle'),
    'Every data binding needs a unique stable ID.': t('teamPlatform.pageSpec.issue.bindingId'),
    'Every data binding needs a name.': t('teamPlatform.pageSpec.issue.bindingName'),
    'API data bindings must name a stable operation ID.': t('teamPlatform.pageSpec.issue.apiOperationId'),
    'Every interaction needs a unique stable ID.': t('teamPlatform.pageSpec.issue.interactionId'),
    'Every interaction needs both a trigger and an outcome.': t('teamPlatform.pageSpec.issue.interactionFields'),
    'Trace the PageSpec to at least one stable acceptance criterion ID.': t('teamPlatform.pageSpec.issue.acceptanceTrace'),
    'PageSpec must trace to at least one acceptance criterion.': t('teamPlatform.pageSpec.issue.acceptanceTrace'),
    'The working draft has unrevisioned changes.': t('teamPlatform.pageSpec.issue.unrevisionedChanges'),
  }
  const missingState = /^PageSpec must declare the canonical (.+) state key\.$/.exec(issue)
    ?? /^PageSpec must declare the (.+) state\.$/.exec(issue)
  if (missingState) return t('teamPlatform.pageSpec.issue.canonicalState', { state: requiredStateTitle(missingState[1], t) })
  const requiredStateMatch = /^Required state (.+) must be marked required\.$/.exec(issue)
    ?? /^The canonical (.+) state must be marked required\.$/.exec(issue)
  if (requiredStateMatch) return t('teamPlatform.pageSpec.issue.requiredState', { state: requiredStateTitle(requiredStateMatch[1], t) })
  return labels[issue] ?? issue
}

function requiredStateTitle(id: string, t: Translate) {
  const labels: Record<string, string> = {
    ready: t('teamPlatform.pageSpec.state.ready'),
    loading: t('teamPlatform.pageSpec.state.loading'),
    empty: t('teamPlatform.pageSpec.state.empty'),
    error: t('teamPlatform.pageSpec.state.error'),
  }
  return labels[id] ?? id
}

function requiredState(id: typeof REQUIRED_PAGE_STATE_KEYS[number], t: Translate): PageStateDto {
  return {
    id,
    key: id,
    title: requiredStateTitle(id, t),
    required: true,
    fixtureIds: [],
    acceptanceCriterionIds: [],
  }
}

function versionRef(revision: ArtifactRevisionDto<PageSpecContentDto>): VersionRefDto {
  return {
    artifactId: revision.artifactId,
    revisionId: revision.id,
    revisionNumber: revision.revisionNumber,
    contentHash: revision.contentHash,
  }
}

function stableId(prefix: string) {
  const id = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${id}`
}

function workflowRouteReference(artifactId: string): WorkflowRouteReference {
  if (typeof window === 'undefined') return { runId: '', proposalId: '', nodeKey: '' }
  const query = new URLSearchParams(window.location.search)
  if (query.get('artifactId') !== artifactId) return { runId: '', proposalId: '', nodeKey: '' }
  return {
    runId: query.get('runId') ?? '',
    proposalId: query.get('proposalId') ?? '',
    nodeKey: query.get('workbenchNodeKey') ?? '',
  }
}

function resolvedWorkflowProposalReference(
  reference: WorkflowRouteReference,
  inferredProposalId: string,
) {
  if (!reference.runId) return reference.proposalId
  if (!inferredProposalId) return ''
  return !reference.proposalId || reference.proposalId === inferredProposalId
    ? inferredProposalId
    : ''
}

function commaList(value: string) {
  return uniqueStrings(value.split(','))
}

function lineList(value: string) {
  return uniqueStrings(value.split('\n'))
}

function uniqueStrings(values: readonly string[]) {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}

function formatDate(value: string, locale: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale)
}

function inputClass(readOnly: boolean) {
  return cn('h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground outline-none', readOnly && 'opacity-70')
}

function textareaClass(readOnly: boolean) {
  return cn('w-full rounded border border-border bg-background p-2 text-[10px] text-foreground outline-none', readOnly && 'opacity-70')
}

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback
}
