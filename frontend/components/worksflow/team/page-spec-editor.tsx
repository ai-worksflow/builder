'use client'

import { useCallback, useEffect, useRef, useState } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  ArtifactWorkspaceConflictError,
  reviewGateReadyForRequest,
  type ArtifactDetails,
} from '@/lib/platform/artifact-workspace'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
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
  Save,
  Send,
  ShieldCheck,
  Trash2,
  Wand2,
} from 'lucide-react'

type EditorTab = 'content' | 'states' | 'data' | 'interactions' | 'versions' | 'proposal' | 'trace' | 'review'
type SaveState = 'idle' | 'dirty' | 'saving' | 'saved' | 'conflict' | 'error'

export function PageSpecEditor({
  artifactId,
  onBack,
}: {
  artifactId: string
  onBack?: () => void
}) {
  const workspace = useArtifactWorkspace()
  const collaboration = useCollaboration()
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
  const [proposalInstruction, setProposalInstruction] = useState('Improve this PageSpec while preserving stable state, interaction, binding, and acceptance IDs.')
  const [selectedOperations, setSelectedOperations] = useState<Record<string, string[]>>({})
  const [comment, setComment] = useState('')
  const [reviewSummary, setReviewSummary] = useState('PageSpec is ready for exact revision review.')
  const [reviewerId, setReviewerId] = useState('')
  const activeArtifactRef = useRef('')
  const contentRef = useRef<PageSpecContentDto | null>(null)
  const draftEtagRef = useRef('')
  const saveInFlightRef = useRef(false)
  const queuedContentRef = useRef<PageSpecContentDto | null>(null)
  const canEdit = collaboration.can('edit')

  const latestVersion = resource?.latestRevision
    ? versionRef(resource.latestRevision)
    : undefined
  const proposals = workspace.proposals.filter((proposal) => proposal.artifactId === artifactId)
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
  const dirty = Boolean(content && serverContent && JSON.stringify(content) !== JSON.stringify(normalizePageSpecContent(serverContent)))
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
      setError(errorMessage(cause))
    }
  }, [resource, workspace])

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
        if (!nextEtag) throw new Error('The server did not return the saved PageSpec draft ETag.')
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
        setError('The server draft changed. Your local PageSpec is preserved until you choose to reload it.')
      } else {
        setSaveState('error')
        setError(errorMessage(cause))
      }
      return null
    } finally {
      saveInFlightRef.current = false
      queuedContentRef.current = null
    }
  }, [canEdit, resource, workspace])

  useEffect(() => {
    if (saveState !== 'dirty' || !content || !canEdit) return
    const timer = window.setTimeout(() => void saveDraft(content), 700)
    return () => window.clearTimeout(timer)
  }, [canEdit, content, saveDraft, saveState])

  function updateContent(
    update: Partial<PageSpecContentDto>
      | ((current: PageSpecContentDto) => PageSpecContentDto),
  ) {
    if (!canEdit) return
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
    if (!resource || !content || clientIssues.length > 0 || dirty || saveState === 'saving' || !hasUnversionedChanges) return
    setSaveState('saving')
    setError(null)
    try {
      await workspace.createPageSpecRevision(resource.artifact.id, content)
      setSaveState('saved')
      await loadDetails()
    } catch (cause) {
      if (cause instanceof ArtifactWorkspaceConflictError) setSaveState('conflict')
      else setSaveState('error')
      setError(errorMessage(cause))
    }
  }

  async function useServerDraft() {
    queuedContentRef.current = null
    setSaveState('idle')
    setError(null)
    await workspace.refresh()
  }

  async function applyProposal(proposal: ProposalDto) {
    try {
      await workspace.applyProposal(proposal.id, selectedOperations[proposal.id] ?? [])
      setSaveState('idle')
      await loadDetails()
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  if (!resource || !content) {
    return (
      <div className="rounded-lg border border-dashed border-border bg-panel p-6 text-center">
        <AlertTriangle className="mx-auto size-6 text-warning" />
        <p className="mt-2 text-sm font-semibold text-foreground">PageSpec unavailable</p>
        <p className="mt-1 text-[10px] text-muted-foreground">Refresh the platform artifacts; browser fixtures are not used.</p>
      </div>
    )
  }

  return (
    <section className="overflow-hidden rounded-xl border border-border bg-background">
      <header className="flex flex-wrap items-center gap-2 border-b border-border bg-panel px-3 py-2.5">
        {onBack && <button type="button" onClick={onBack} className="rounded border border-border p-1.5 text-muted-foreground" aria-label="Back to PageSpec list"><ArrowLeft className="size-3.5" /></button>}
        <ShieldCheck className="size-4 text-primary-bright" />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[12px] font-semibold text-foreground">{resource.artifact.title}</span>
          <span className="block truncate font-mono text-[8px] text-faint-foreground">{resource.artifact.id} · ETag {draftEtag || 'missing'}</span>
        </span>
        <span className={cn(
          'rounded px-2 py-1 text-[8px] font-semibold',
          resource.artifact.status === 'approved'
            ? 'bg-success/15 text-success'
            : 'bg-white/5 text-muted-foreground',
        )}>{resource.artifact.status}</span>
        <span className={cn(
          'inline-flex items-center gap-1 rounded px-2 py-1 text-[8px]',
          saveState === 'conflict' || saveState === 'error'
            ? 'bg-warning/10 text-warning'
            : saveState === 'dirty'
              ? 'bg-warning/10 text-warning'
              : 'bg-success/10 text-success',
        )}>
          {saveState === 'saving' ? <Loader2 className="size-3 animate-spin" /> : <Save className="size-3" />}
          {saveState === 'dirty' ? 'Pending autosave' : saveState}
        </span>
        <button type="button" onClick={() => void workspace.refresh()} className="rounded border border-border p-1.5 text-muted-foreground" aria-label="Refresh PageSpec"><RefreshCw className="size-3.5" /></button>
      </header>

      {error && (
        <div role="alert" className="border-b border-warning/30 bg-warning/10 px-3 py-2 text-[9px] text-warning">
          {error}
          {saveState === 'conflict' && <button type="button" onClick={() => void useServerDraft()} className="ml-3 underline">Use current server draft</button>}
        </div>
      )}

      <nav className="flex overflow-x-auto border-b border-border bg-panel p-1 scrollbar-thin">
        {([
          ['content', 'Basics'],
          ['states', `States ${content.states.length}`],
          ['data', `Data ${content.dataBindings.length}`],
          ['interactions', `Interactions ${content.interactions.length}`],
          ['versions', `Versions ${details?.versions.length ?? 0}`],
          ['proposal', `AI proposals ${proposals.length}`],
          ['trace', `Trace ${details?.dependencies.length ?? 0}`],
          ['review', `Review ${comments.length + reviews.length}`],
        ] as const).map(([id, label]) => (
          <button key={id} type="button" onClick={() => setTab(id)} className={cn('shrink-0 rounded px-2.5 py-1.5 text-[9px] font-medium', tab === id ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground')}>{label}</button>
        ))}
      </nav>

      <div className="max-h-[680px] overflow-y-auto p-4 scrollbar-thin">
        {tab === 'content' && <BasicsEditor content={content} readOnly={!canEdit} onChange={updateContent} />}
        {tab === 'states' && <StatesEditor states={content.states} readOnly={!canEdit} onChange={(states) => updateContent({ states })} />}
        {tab === 'data' && <DataBindingsEditor bindings={content.dataBindings} readOnly={!canEdit} onChange={(dataBindings) => updateContent({ dataBindings })} />}
        {tab === 'interactions' && <InteractionsEditor interactions={content.interactions} readOnly={!canEdit} onChange={(interactions) => updateContent({ interactions })} />}
        {tab === 'versions' && (
          <div className="space-y-3">
            <ReviewGatePanel clientIssues={clientIssues} serverGate={details?.reviewGate} />
            <button type="button" onClick={() => void createRevision()} disabled={!canEdit || clientIssues.length > 0 || dirty || !hasUnversionedChanges || saveState === 'saving' || saveState === 'conflict'} className="inline-flex items-center gap-1.5 rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-40"><GitBranch className="size-3.5" />Create immutable PageSpec revision</button>
            {dirty && <p className="text-[9px] text-warning">Wait for the latest autosave before creating a revision.</p>}
            {!dirty && !hasUnversionedChanges && <p className="text-[9px] text-faint-foreground">The draft already matches the latest immutable revision.</p>}
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
            canEdit={canEdit}
            canCreate={Boolean(latestVersion) && draftMatchesLatest && !dirty && saveState !== 'saving' && saveState !== 'conflict'}
            onCreate={() => void workspace.createProposal({
              jobType: 'page_spec.patch',
              targetRevision: latestVersion!,
              instruction: proposalInstruction,
              inputVersions: resource.draft?.sourceVersions ?? [],
              outputSchemaVersion: 'page-spec.patch.v1',
            }).catch((cause) => setError(errorMessage(cause)))}
            onApply={(proposal) => void applyProposal(proposal)}
          />
        )}
        {tab === 'trace' && (
          <div className="space-y-2">
            {details?.dependencies.map((dependency) => <div key={dependency.id} className="rounded border border-border bg-panel p-3 text-[9px] text-muted-foreground"><Link2 className="mr-1 inline size-3 text-primary-bright" /><code>{dependency.source.artifactId}:{dependency.source.revisionId}</code> {dependency.relation} <code>{dependency.target.artifactId}:{dependency.target.revisionId}</code>{dependency.required && <span className="ml-2 text-warning">required</span>}</div>)}
            {workspace.traces.filter((trace) => trace.source.artifactId === artifactId || trace.target.artifactId === artifactId).map((trace) => <div key={trace.id} className="rounded border border-border bg-panel p-3 text-[9px] text-muted-foreground"><code>{trace.source.artifactId}:{trace.source.revisionId}</code> → {trace.relation} → <code>{trace.target.artifactId}:{trace.target.revisionId}</code></div>)}
          </div>
        )}
        {tab === 'review' && (
          <ReviewEditor
            latestVersion={latestVersion}
            gatePassed={gatePassed}
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
  return (
    <div className="mx-auto max-w-4xl space-y-4">
      <div className="grid gap-3 md:grid-cols-2">
        <Field label="Stable Blueprint page node ID"><input value={content.blueprintPageNodeId} readOnly className={inputClass(true)} /></Field>
        <Field label="Title"><input value={content.title} readOnly={readOnly} onChange={(event) => onChange({ title: event.target.value })} className={inputClass(readOnly)} /></Field>
        <Field label="Route"><input value={content.route} readOnly={readOnly} onChange={(event) => onChange({ route: event.target.value })} className={inputClass(readOnly)} placeholder="/orders" /></Field>
        <Field label="Required roles"><input value={content.requiredRoles.join(', ')} readOnly={readOnly} onChange={(event) => onChange({ requiredRoles: commaList(event.target.value) })} className={inputClass(readOnly)} placeholder="admin, editor" /></Field>
      </div>
      <Field label="User goal"><textarea value={content.userGoal} readOnly={readOnly} onChange={(event) => onChange({ userGoal: event.target.value })} rows={3} className={textareaClass(readOnly)} /></Field>
      <div className="grid gap-3 md:grid-cols-2">
        <Field label="Entry points"><textarea value={content.entryPoints.join('\n')} readOnly={readOnly} onChange={(event) => onChange({ entryPoints: lineList(event.target.value) })} rows={4} className={textareaClass(readOnly)} placeholder="Navigation link\nDirect URL" /></Field>
        <Field label="Exit points"><textarea value={content.exitPoints.join('\n')} readOnly={readOnly} onChange={(event) => onChange({ exitPoints: lineList(event.target.value) })} rows={4} className={textareaClass(readOnly)} placeholder="Order details\nCheckout" /></Field>
      </div>
      <Field label="Stable acceptance criterion IDs"><textarea value={content.acceptanceCriterionIds.join('\n')} readOnly={readOnly} onChange={(event) => onChange({ acceptanceCriterionIds: lineList(event.target.value) })} rows={4} className={textareaClass(readOnly)} placeholder="AC-ORDER-001" /></Field>
      <Field label="Non-functional constraints"><textarea value={content.nonFunctionalConstraints.join('\n')} readOnly={readOnly} onChange={(event) => onChange({ nonFunctionalConstraints: lineList(event.target.value) })} rows={4} className={textareaClass(readOnly)} placeholder="First contentful paint under 2 seconds\nKeyboard accessible" /></Field>
    </div>
  )
}

function StatesEditor({ states, readOnly, onChange }: { states: readonly PageStateDto[]; readOnly: boolean; onChange: (states: readonly PageStateDto[]) => void }) {
  function update(index: number, patch: Partial<PageStateDto>) {
    onChange(states.map((state, stateIndex) => stateIndex === index ? { ...state, ...patch } : state))
  }
  function restoreRequired() {
    const existing = new Set(states.map((state) => state.key))
    onChange([
      ...states,
      ...REQUIRED_PAGE_STATE_KEYS.filter((key) => !existing.has(key)).map(requiredState),
    ])
  }
  return (
    <div className="mx-auto max-w-4xl space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-sm font-semibold text-foreground">Page states</h3>
        <button type="button" onClick={restoreRequired} disabled={readOnly} className="ml-auto rounded border border-border px-2 py-1.5 text-[9px] text-muted-foreground disabled:opacity-40">Restore required states</button>
        <button type="button" onClick={() => onChange([...states, { id: stableId('state'), key: `custom_${states.length + 1}`, title: 'Custom state', required: false, fixtureIds: [], acceptanceCriterionIds: [] }])} disabled={readOnly} className="rounded bg-primary px-2 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Plus className="mr-1 inline size-3" />State</button>
      </div>
      {states.map((state, index) => (
        <article key={state.id} className="rounded-lg border border-border bg-panel p-3">
          <div className="flex flex-wrap items-center gap-2">
            <code className="text-[8px] text-faint-foreground">{state.id}</code>
            <label className="ml-auto flex items-center gap-1 text-[9px] text-muted-foreground"><input type="checkbox" checked={state.required} disabled={readOnly} onChange={(event) => update(index, { required: event.target.checked })} />Required</label>
            <button type="button" disabled={readOnly} onClick={() => onChange(states.filter((_, stateIndex) => stateIndex !== index))} aria-label={`Delete state ${state.title}`}><Trash2 className="size-3.5 text-destructive" /></button>
          </div>
          <div className="mt-2 grid gap-2 md:grid-cols-2">
            <Field label="Stable key"><input value={state.key} readOnly={readOnly} onChange={(event) => update(index, { key: event.target.value })} className={inputClass(readOnly)} /></Field>
            <Field label="Title"><input value={state.title} readOnly={readOnly} onChange={(event) => update(index, { title: event.target.value })} className={inputClass(readOnly)} /></Field>
            <Field label="Description"><textarea value={state.description ?? ''} readOnly={readOnly} onChange={(event) => update(index, { description: event.target.value })} rows={2} className={textareaClass(readOnly)} /></Field>
            <Field label="Entry condition"><textarea value={state.entryCondition ?? ''} readOnly={readOnly} onChange={(event) => update(index, { entryCondition: event.target.value })} rows={2} className={textareaClass(readOnly)} /></Field>
            <Field label="Fixture IDs"><input value={state.fixtureIds.join(', ')} readOnly={readOnly} onChange={(event) => update(index, { fixtureIds: commaList(event.target.value) })} className={inputClass(readOnly)} /></Field>
            <Field label="Acceptance criterion IDs"><input value={state.acceptanceCriterionIds.join(', ')} readOnly={readOnly} onChange={(event) => update(index, { acceptanceCriterionIds: commaList(event.target.value) })} className={inputClass(readOnly)} /></Field>
          </div>
        </article>
      ))}
    </div>
  )
}

function DataBindingsEditor({ bindings, readOnly, onChange }: { bindings: readonly PageDataBindingDto[]; readOnly: boolean; onChange: (bindings: readonly PageDataBindingDto[]) => void }) {
  function update(index: number, patch: Partial<PageDataBindingDto>) {
    onChange(bindings.map((binding, bindingIndex) => bindingIndex === index ? { ...binding, ...patch } : binding))
  }
  return (
    <div className="mx-auto max-w-4xl space-y-3">
      <div className="flex items-center"><Database className="mr-2 size-4 text-primary-bright" /><h3 className="text-sm font-semibold text-foreground">Data bindings</h3><button type="button" onClick={() => onChange([...bindings, { id: stableId('binding'), name: 'Data binding', source: 'api', operationId: '', schema: {}, required: true }])} disabled={readOnly} className="ml-auto rounded bg-primary px-2 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Plus className="mr-1 inline size-3" />Binding</button></div>
      {bindings.map((binding, index) => (
        <article key={binding.id} className="rounded-lg border border-border bg-panel p-3">
          <div className="flex items-center gap-2"><code className="text-[8px] text-faint-foreground">{binding.id}</code><label className="ml-auto flex items-center gap-1 text-[9px] text-muted-foreground"><input type="checkbox" checked={binding.required} disabled={readOnly} onChange={(event) => update(index, { required: event.target.checked })} />Required</label><button type="button" disabled={readOnly} onClick={() => onChange(bindings.filter((_, bindingIndex) => bindingIndex !== index))} aria-label={`Delete binding ${binding.name}`}><Trash2 className="size-3.5 text-destructive" /></button></div>
          <div className="mt-2 grid gap-2 md:grid-cols-2">
            <Field label="Name"><input value={binding.name} readOnly={readOnly} onChange={(event) => update(index, { name: event.target.value })} className={inputClass(readOnly)} /></Field>
            <Field label="Source"><select value={binding.source} disabled={readOnly} onChange={(event) => update(index, { source: event.target.value as PageDataBindingDto['source'] })} className={inputClass(readOnly)}><option value="api">api</option><option value="database">database</option><option value="fixture">fixture</option><option value="local">local</option></select></Field>
            <Field label="Stable operation ID"><input value={binding.operationId ?? ''} readOnly={readOnly} onChange={(event) => update(index, { operationId: event.target.value })} className={inputClass(readOnly)} placeholder="orders.list" /></Field>
            <JsonObjectEditor value={binding.schema} readOnly={readOnly} onChange={(schema) => update(index, { schema })} />
          </div>
        </article>
      ))}
      {bindings.length === 0 && <Empty text="No data bindings declared." />}
    </div>
  )
}

function InteractionsEditor({ interactions, readOnly, onChange }: { interactions: readonly PageInteractionSpecDto[]; readOnly: boolean; onChange: (interactions: readonly PageInteractionSpecDto[]) => void }) {
  function update(index: number, patch: Partial<PageInteractionSpecDto>) {
    onChange(interactions.map((interaction, interactionIndex) => interactionIndex === index ? { ...interaction, ...patch } : interaction))
  }
  return (
    <div className="mx-auto max-w-4xl space-y-3">
      <div className="flex items-center"><MousePointerClick className="mr-2 size-4 text-primary-bright" /><h3 className="text-sm font-semibold text-foreground">Interactions</h3><button type="button" onClick={() => onChange([...interactions, { id: stableId('interaction'), trigger: '', outcome: '', acceptanceCriterionIds: [] }])} disabled={readOnly} className="ml-auto rounded bg-primary px-2 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Plus className="mr-1 inline size-3" />Interaction</button></div>
      {interactions.map((interaction, index) => (
        <article key={interaction.id} className="rounded-lg border border-border bg-panel p-3">
          <div className="flex items-center gap-2"><code className="text-[8px] text-faint-foreground">{interaction.id}</code><button type="button" disabled={readOnly} onClick={() => onChange(interactions.filter((_, interactionIndex) => interactionIndex !== index))} className="ml-auto" aria-label={`Delete interaction ${interaction.id}`}><Trash2 className="size-3.5 text-destructive" /></button></div>
          <div className="mt-2 grid gap-2 md:grid-cols-2">
            <Field label="Trigger"><input value={interaction.trigger} readOnly={readOnly} onChange={(event) => update(index, { trigger: event.target.value })} className={inputClass(readOnly)} placeholder="Click checkout" /></Field>
            <Field label="Outcome"><input value={interaction.outcome} readOnly={readOnly} onChange={(event) => update(index, { outcome: event.target.value })} className={inputClass(readOnly)} placeholder="Navigate to checkout" /></Field>
            <Field label="Target Blueprint page node ID"><input value={interaction.targetPageNodeId ?? interaction.targetPageSpecId ?? ''} readOnly={readOnly} onChange={(event) => update(index, { targetPageNodeId: event.target.value, targetPageSpecId: undefined })} className={inputClass(readOnly)} placeholder="page-checkout" /></Field>
            <Field label="Acceptance criterion IDs"><input value={interaction.acceptanceCriterionIds.join(', ')} readOnly={readOnly} onChange={(event) => update(index, { acceptanceCriterionIds: commaList(event.target.value) })} className={inputClass(readOnly)} /></Field>
          </div>
        </article>
      ))}
      {interactions.length === 0 && <Empty text="No interactions declared." />}
    </div>
  )
}

function ProposalEditor({ proposals, selected, onSelected, instruction, onInstruction, canEdit, canCreate, onCreate, onApply }: { proposals: readonly ProposalDto[]; selected: Record<string, string[]>; onSelected: (next: Record<string, string[]>) => void; instruction: string; onInstruction: (value: string) => void; canEdit: boolean; canCreate: boolean; onCreate: () => void; onApply: (proposal: ProposalDto) => void }) {
  return (
    <div className="mx-auto max-w-4xl space-y-3">
      <div className="rounded-lg border border-border bg-panel p-3">
        <textarea value={instruction} onChange={(event) => onInstruction(event.target.value)} rows={3} className={textareaClass(false)} />
        <button type="button" onClick={onCreate} disabled={!canEdit || !canCreate || !instruction.trim()} className="mt-2 rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-40"><Bot className="mr-1 inline size-3" />Ask AI for PageSpec proposal</button>
        {!canCreate && <p className="mt-2 text-[9px] text-warning">Create an immutable PageSpec revision first. AI never consumes mutable draft bytes.</p>}
      </div>
      {proposals.map((proposal) => {
        const selectedIds = selected[proposal.id] ?? []
        const hasAccepted = proposal.operations.some((operation) => operation.decision === 'accepted' || selectedIds.includes(operation.id))
        return (
          <article key={proposal.id} className="rounded-lg border border-border bg-panel p-3">
            <div className="flex flex-wrap items-center gap-2"><Wand2 className="size-3.5 text-primary-bright" /><span className="text-[10px] font-semibold text-foreground">Manifest {proposal.manifest.id.slice(0, 12)}</span><span className="rounded bg-primary/10 px-1.5 py-0.5 text-[8px] text-primary-bright">{proposal.status}</span><code className="ml-auto text-[8px] text-faint-foreground">base {proposal.baseRevision.contentHash.slice(0, 12)}</code></div>
            {proposal.assumptions.length > 0 && <p className="mt-2 text-[9px] text-muted-foreground">Assumptions: {proposal.assumptions.join(' · ')}</p>}
            {proposal.questions.length > 0 && <p className="mt-1 text-[9px] text-warning">Questions: {proposal.questions.join(' · ')}</p>}
            <div className="mt-2 space-y-1.5">
              {proposal.operations.map((operation) => (
                <label key={operation.id} className="flex gap-2 rounded border border-border bg-background p-2 text-[9px] text-muted-foreground">
                  <input type="checkbox" disabled={operation.decision !== 'pending'} checked={operation.decision === 'accepted' || operation.decision === 'applied' || selectedIds.includes(operation.id)} onChange={(event) => onSelected({ ...selected, [proposal.id]: event.target.checked ? [...selectedIds, operation.id] : selectedIds.filter((id) => id !== operation.id) })} />
                  <span className="min-w-0 flex-1"><code>{operation.kind} {operation.path || '/'}</code><span className="ml-2 text-faint-foreground">{operation.decision}</span>{operation.rationale && <span className="mt-1 block">{operation.rationale}</span>}{operation.value !== undefined && <pre className="mt-1 max-h-36 overflow-auto whitespace-pre-wrap rounded bg-black/20 p-1.5 font-mono text-[8px]">{JSON.stringify(operation.value, null, 2)}</pre>}</span>
                </label>
              ))}
            </div>
            <button type="button" onClick={() => onApply(proposal)} disabled={!canEdit || !hasAccepted || !['open', 'reviewing', 'ready'].includes(proposal.status)} className="mt-2 rounded bg-primary px-2.5 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40">Decide every operation and apply selected changes</button>
          </article>
        )
      })}
      {proposals.length === 0 && <Empty text="No AI proposal targets this PageSpec." />}
    </div>
  )
}

function ReviewEditor({ latestVersion, gatePassed, gate, comment, onComment, reviewSummary, onReviewSummary, reviewerId, onReviewerId, currentUserId, onError }: { latestVersion?: VersionRefDto; gatePassed: boolean; gate: React.ReactNode; comment: string; onComment: (value: string) => void; reviewSummary: string; onReviewSummary: (value: string) => void; reviewerId: string; onReviewerId: (value: string) => void; currentUserId: string | null; onError: (value: string | null) => void }) {
  const collaboration = useCollaboration()
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
      {!latestVersion && <Empty text="Create an immutable revision before commenting or requesting review." />}
      {latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_auto]"><input value={comment} onChange={(event) => onComment(event.target.value)} className={inputClass(false)} placeholder="Comment on this exact PageSpec revision" /><button type="button" onClick={() => void collaboration.addComment(comment, undefined, latestVersion).then((ok) => ok && onComment('')).catch((cause) => onError(errorMessage(cause)))} disabled={!comment.trim() || !collaboration.can('comment')} className="rounded bg-primary px-3 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><MessageSquare className="mr-1 inline size-3" />Comment</button></div>}
      {latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_180px_auto]"><input value={reviewSummary} onChange={(event) => onReviewSummary(event.target.value)} className={inputClass(false)} aria-label="PageSpec review summary" /><select value={reviewerId} onChange={(event) => onReviewerId(event.target.value)} className={inputClass(false)} aria-label="PageSpec reviewer"><option value="">Reviewer</option>{collaboration.members.filter((member) => member.user.id !== currentUserId && ['owner', 'admin', 'editor'].includes(member.role)).map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name}</option>)}</select><button type="button" onClick={() => void collaboration.requestReview(reviewSummary, latestVersion, [reviewerId]).catch((cause) => onError(errorMessage(cause)))} disabled={!gatePassed || !reviewerId || !reviewSummary.trim()} className="rounded bg-primary px-3 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Send className="mr-1 inline size-3" />Request review</button></div>}
      {comments.map((thread) => <div key={thread.id} className="rounded border border-border bg-panel p-3"><span className="text-[10px] font-medium text-foreground">{thread.author.name}</span><p className="mt-1 text-[9px] text-muted-foreground">{thread.body}</p><p className="mt-1 text-[8px] text-faint-foreground">Exact revision {thread.target?.revisionNumber} · {thread.target?.contentHash.slice(0, 12)}</p></div>)}
      {reviews.map((review) => <div key={review.id} className="rounded border border-border bg-panel p-3 text-[9px]"><span className="font-semibold text-foreground">{review.state}</span><span className="ml-2 text-muted-foreground">{review.summary}</span><p className="mt-1 text-faint-foreground">Exact revision {review.target?.revisionNumber} · use Review Center for canonical decisions.</p></div>)}
    </div>
  )
}

function ReviewGatePanel({ clientIssues, serverGate }: { clientIssues: readonly string[]; serverGate?: ArtifactReviewGateDto }) {
  const serverIssues = serverGate?.checks.filter((check) => check.severity === 'error' && check.code !== 'canonical_review_approved').map((check) => check.message) ?? []
  const issues = [...clientIssues, ...serverIssues]
  const ready = issues.length === 0 && reviewGateReadyForRequest(serverGate)
  return (
    <div className={cn('rounded-lg border p-3', ready ? 'border-success/30 bg-success/10' : 'border-warning/30 bg-warning/10')}>
      <div className="flex items-center gap-2 text-[10px] font-semibold text-foreground">{ready ? <CheckCircle2 className="size-4 text-success" /> : <AlertTriangle className="size-4 text-warning" />}Request-review gate {ready ? 'ready' : 'blocked'}</div>
      {issues.map((issue) => <p key={issue} className="mt-1 text-[9px] text-muted-foreground">• {issue}</p>)}
      {!serverGate && <p className="mt-1 text-[9px] text-warning">Waiting for the server review gate.</p>}
      {serverGate && <p className="mt-2 text-[8px] text-faint-foreground">Server gate {serverGate.passed ? 'passed' : 'awaiting canonical approval'} · trace {Math.round(serverGate.traceCoverage * 100)}% · {serverGate.unresolvedBlockingCommentIds.length} blocking comments</p>}
    </div>
  )
}

function JsonObjectEditor({ value, readOnly, onChange }: { value?: JsonObject; readOnly: boolean; onChange: (value?: JsonObject) => void }) {
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
        throw new Error('Schema must be a JSON object.')
      }
      setError(null)
      onChange(parsed as JsonObject)
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }
  return <Field label="JSON schema"><textarea value={draft} readOnly={readOnly} onFocus={() => setFocused(true)} onChange={(event) => setDraft(event.target.value)} onBlur={commit} rows={5} className={textareaClass(readOnly)} />{error && <span className="mt-1 block text-[8px] text-destructive">{error}</span>}</Field>
}

function RevisionCard({ revision }: { revision: ArtifactRevisionDto<PageSpecContentDto> }) {
  return <div className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><FileClock className="size-4 text-primary-bright" /><span className="text-[10px] font-semibold text-foreground">Revision {revision.revisionNumber}</span><code className="ml-auto text-[8px] text-faint-foreground">{revision.contentHash.slice(0, 16)}</code></div><p className="mt-1 text-[9px] text-muted-foreground">{new Date(revision.createdAt).toLocaleString()} · {revision.sourceVersions?.length ?? 0} pinned sources</p></div>
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return <label className="block text-[9px] font-medium text-muted-foreground">{label}<span className="mt-1 block">{children}</span></label>
}

function Empty({ text }: { text: string }) {
  return <p className="rounded border border-dashed border-border p-4 text-center text-[9px] text-faint-foreground">{text}</p>
}

function requiredState(id: typeof REQUIRED_PAGE_STATE_KEYS[number]): PageStateDto {
  return {
    id,
    key: id,
    title: id[0].toUpperCase() + id.slice(1),
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

function commaList(value: string) {
  return uniqueStrings(value.split(','))
}

function lineList(value: string) {
  return uniqueStrings(value.split('\n'))
}

function uniqueStrings(values: readonly string[]) {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}

function inputClass(readOnly: boolean) {
  return cn('h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground outline-none', readOnly && 'opacity-70')
}

function textareaClass(readOnly: boolean) {
  return cn('w-full rounded border border-border bg-background p-2 text-[10px] text-foreground outline-none', readOnly && 'opacity-70')
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : 'PageSpec operation failed.'
}
