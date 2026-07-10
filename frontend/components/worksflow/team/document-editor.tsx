'use client'

import { useEffect, useState } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  ArtifactWorkspaceConflictError,
  documentReviewIssues,
  reviewGateReadyForRequest,
  type ArtifactDetails,
} from '@/lib/platform/artifact-workspace'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import type {
  ArtifactRevisionDto,
  DocumentContentDto,
  ProposalDto,
  VersionRefDto,
} from '@/lib/platform/dto'
import { useWorksflow } from '@/lib/worksflow/store'
import { cn } from '@/lib/utils'
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
} from 'lucide-react'

type EditorTab = 'content' | 'versions' | 'proposal' | 'trace' | 'review'

export function DocumentEditor() {
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
  const [proposalInstruction, setProposalInstruction] = useState('Improve clarity and preserve every stable requirement and acceptance ID.')
  const [selectedOperations, setSelectedOperations] = useState<Record<string, string[]>>({})
  const [comment, setComment] = useState('')
  const [reviewSummary, setReviewSummary] = useState('Ready for version-level review.')
  const [reviewerId, setReviewerId] = useState('')

  const resource = workspace.documents.find((item) => item.artifact.id === selectedDocId)
    ?? workspace.documents[0]
  const serverContent = resource?.draft?.content ?? resource?.latestRevision?.content
  const serverEtag = resource?.draft?.etag ?? resource?.artifact.etag
  const dirty = Boolean(content && serverContent && JSON.stringify(content) !== JSON.stringify(serverContent))
  const proposals = workspace.proposals.filter((proposal) => proposal.artifactId === resource?.artifact.id)
  const latestVersion = resource?.latestRevision
    ? versionRef(resource.latestRevision)
    : undefined
  const comments = collaboration.comments.filter((thread) => thread.target?.artifactId === resource?.artifact.id)
  const currentUserId = collaboration.session.signedIn ? collaboration.session.user.id : null
  const clientGate = content ? documentReviewIssues(content) : []
  const revisionReady = clientGate.length === 0
  const gatePassed = revisionReady && reviewGateReadyForRequest(details?.reviewGate)

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
      .catch((error) => { if (active) setLocalError(message(error)) })
    return () => { active = false }
  }, [resource?.artifact.id, workspace.loadDetails])

  useEffect(() => {
    if (!resource || !content || !serverEtag || !dirty || conflict) return
    const timer = window.setTimeout(() => {
      setSaving(true)
      setLocalError(null)
      void workspace.saveDocumentDraft(resource.artifact.id, content, serverEtag)
        .then(() => {
          setSavedAt(new Date().toLocaleTimeString())
          setConflict(false)
        })
        .catch((error) => {
          if (error instanceof ArtifactWorkspaceConflictError) setConflict(true)
          setLocalError(message(error))
        })
        .finally(() => setSaving(false))
    }, 700)
    return () => window.clearTimeout(timer)
  }, [conflict, content, dirty, resource, serverEtag, workspace.saveDocumentDraft])

  if (!collaboration.session.signedIn) {
    return <Unavailable title="Sign in to open platform documents" detail="Browser document fixtures are not used as a fallback." />
  }
  if (workspace.status === 'loading') return <Unavailable loading title="Loading platform documents" detail="Fetching artifacts, drafts, revisions and trace links." />
  if (workspace.status === 'error') return <Unavailable title="Platform documents unavailable" detail={workspace.error ?? 'The backend did not return document artifacts.'} onRetry={workspace.refresh} />

  if (!resource || !content) {
    return (
      <Unavailable
        title="No document artifacts"
        detail="Create the first Project Brief on the platform, then use immutable revisions to drive the workflow."
        action="Create Project Brief"
        onAction={() => void workspace.createDocument('Project Brief', 'projectBrief')}
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
      setLocalError(message(error))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full min-h-0 bg-canvas max-lg:flex-col">
      <aside className="w-64 shrink-0 overflow-y-auto border-r border-border bg-panel p-3 scrollbar-thin max-lg:max-h-52 max-lg:w-full max-lg:border-b max-lg:border-r-0">
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs font-semibold text-foreground">Platform documents</span>
          <button type="button" onClick={() => void workspace.createDocument('Untitled requirement')} disabled={!collaboration.can('edit')} className="rounded border border-border p-1.5 text-primary-bright disabled:opacity-40" aria-label="Create document"><FilePlus2 className="size-3.5" /></button>
        </div>
        <div className="mt-3 space-y-1">
          {workspace.documents.map((item) => (
            <button key={item.artifact.id} type="button" onClick={() => setSelectedDocId(item.artifact.id)} className={cn('block w-full rounded-md px-2.5 py-2 text-left', item.artifact.id === resource.artifact.id ? 'bg-primary/15' : 'hover:bg-white/5')}>
              <span className="block truncate text-[11px] font-medium text-foreground">{item.artifact.title}</span>
              <span className="mt-0.5 block text-[9px] text-faint-foreground">{item.draft?.content.kind ?? item.latestRevision?.content.kind ?? item.artifact.kind} · {item.artifact.status}</span>
            </button>
          ))}
        </div>
      </aside>

      <main className="flex min-w-0 flex-1 flex-col">
        <header className="flex flex-wrap items-center gap-3 border-b border-border bg-panel px-4 py-3">
          <span className="min-w-0 flex-1"><span className="block truncate text-sm font-semibold text-foreground">{resource.artifact.title}</span><span className="block text-[9px] text-faint-foreground">{resource.artifact.id} · draft ETag {serverEtag ?? 'missing'}</span></span>
          <span className={cn('inline-flex items-center gap-1 rounded px-2 py-1 text-[9px]', conflict ? 'bg-warning/10 text-warning' : saving ? 'bg-primary/10 text-primary-bright' : dirty ? 'bg-warning/10 text-warning' : 'bg-success/10 text-success')}>{saving ? <Loader2 className="size-3 animate-spin" /> : conflict ? <AlertTriangle className="size-3" /> : <Save className="size-3" />}{conflict ? 'Conflict' : saving ? 'Autosaving' : dirty ? 'Pending autosave' : savedAt ? `Saved ${savedAt}` : 'Server draft'}</span>
          <button type="button" onClick={() => void workspace.refresh()} className="rounded border border-border p-1.5 text-muted-foreground" aria-label="Refresh document"><RefreshCw className="size-3.5" /></button>
        </header>

        {(localError || conflict) && <div role="alert" className="border-b border-warning/30 bg-warning/10 px-4 py-2 text-[10px] text-warning">{localError}{conflict && <button type="button" onClick={() => { setConflict(false); setContent(serverContent ?? content) }} className="ml-3 underline">Use current server draft</button>}</div>}

        <nav className="flex overflow-x-auto border-b border-border bg-panel p-1 scrollbar-thin">
          {([
            ['content', 'Content'],
            ['versions', `Versions ${details?.versions.length ?? 0}`],
            ['proposal', `AI proposals ${proposals.length}`],
            ['trace', `Trace ${workspace.traces.length}`],
            ['review', `Review ${comments.length}`],
          ] as const).map(([id, label]) => <button key={id} type="button" onClick={() => setTab(id)} className={cn('shrink-0 rounded px-3 py-1.5 text-[10px] font-medium', tab === id ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground')}>{label}</button>)}
        </nav>

        <div className="min-h-0 flex-1 overflow-y-auto p-5 scrollbar-thin max-sm:p-3">
          {tab === 'content' && <ContentEditor content={content} readOnly={!collaboration.can('edit')} onChange={updateContent} />}
          {tab === 'versions' && (
            <section className="mx-auto max-w-4xl space-y-3">
              <GatePanel clientIssues={clientGate} serverGate={details?.reviewGate} />
              <button type="button" onClick={() => void createRevision()} disabled={!collaboration.can('edit') || !revisionReady || saving} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground disabled:opacity-50"><GitBranch className="size-3.5" />Create immutable revision</button>
              {details?.versions.map((version) => <div key={version.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><FileClock className="size-4 text-primary-bright" /><span className="text-[11px] font-medium text-foreground">Revision {version.revisionNumber}</span><code className="ml-auto text-[9px] text-faint-foreground">{version.contentHash.slice(0, 16)}</code></div><p className="mt-1 text-[10px] text-muted-foreground">{new Date(version.createdAt).toLocaleString()} · {version.sourceVersions?.length ?? 0} pinned sources</p></div>)}
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
              }).catch((error) => setLocalError(message(error)))}
              onApply={(proposal) => void workspace.applyProposal(
                proposal.id,
                selectedOperations[proposal.id] ?? [],
              ).catch((error) => setLocalError(message(error)))}
            />
          )}
          {tab === 'trace' && (
            <section className="mx-auto max-w-4xl space-y-2">
              {details?.dependencies.map((dependency) => <div key={dependency.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><Link2 className="mr-2 inline size-3.5 text-primary-bright" />{dependency.source.artifactId} <b>{dependency.relation}</b> {dependency.target.artifactId}</div>)}
              {workspace.traces.filter((trace) => trace.source.artifactId === resource.artifact.id || trace.target.artifactId === resource.artifact.id).map((trace) => <div key={trace.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><code>{trace.source.artifactId}:{trace.source.revisionId}</code> → {trace.relation} → <code>{trace.target.artifactId}:{trace.target.revisionId}</code></div>)}
            </section>
          )}
          {tab === 'review' && (
            <section className="mx-auto max-w-4xl space-y-3">
              {!latestVersion && <p className="rounded-md border border-dashed border-border p-4 text-[10px] text-faint-foreground">Create a revision before commenting or requesting review.</p>}
              {latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_auto]"><input value={comment} onChange={(event) => setComment(event.target.value)} placeholder="Comment on this exact revision" className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><button type="button" onClick={() => void collaboration.addComment(comment, undefined, latestVersion).then((ok) => ok && setComment(''))} disabled={!comment.trim() || !collaboration.can('comment')} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><MessageSquare className="mr-1 inline size-3" />Comment</button></div>}
              {latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_180px_auto]"><input value={reviewSummary} onChange={(event) => setReviewSummary(event.target.value)} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><select value={reviewerId} onChange={(event) => setReviewerId(event.target.value)} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground"><option value="">Reviewer</option>{collaboration.members.filter((member) => member.user.id !== currentUserId && ['owner', 'admin', 'editor'].includes(member.role)).map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name}</option>)}</select><button type="button" onClick={() => void collaboration.requestReview(reviewSummary, latestVersion, [reviewerId])} disabled={!gatePassed || !reviewerId || !reviewSummary.trim()} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><Send className="mr-1 inline size-3" />Request review</button></div>}
              {comments.map((thread) => <div key={thread.id} className="rounded-md border border-border bg-panel p-3"><span className="text-[10px] font-medium text-foreground">{thread.author.name}</span><p className="mt-1 text-[10px] text-muted-foreground">{thread.body}</p></div>)}
            </section>
          )}
        </div>
      </main>
    </div>
  )
}

function ContentEditor({ content, readOnly, onChange }: { content: DocumentContentDto; readOnly: boolean; onChange: (patch: Partial<DocumentContentDto>) => void }) {
  const updateBlock = (index: number, patch: Partial<DocumentContentDto['blocks'][number]>) => {
    onChange({
      blocks: content.blocks.map((item, itemIndex) =>
        itemIndex === index ? { ...item, ...patch } : item),
    })
  }

  return (
    <section className="mx-auto max-w-4xl space-y-5">
      <label className="block text-[11px] font-medium text-muted-foreground">
        Summary
        <textarea value={content.summary} onChange={(event) => onChange({ summary: event.target.value })} readOnly={readOnly} rows={3} className="mt-1.5 w-full rounded-md border border-border bg-panel p-3 text-sm text-foreground" />
      </label>

      <div>
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold text-foreground">Structured blocks</h2>
          <button type="button" disabled={readOnly} onClick={() => onChange({ blocks: [...content.blocks, { id: stableId('block'), type: content.kind === 'projectBrief' ? 'goal' : 'paragraph', text: '' }] })} className="text-[10px] text-primary-bright"><Plus className="mr-1 inline size-3" />Block</button>
        </div>
        <div className="mt-2 space-y-2">
          {content.blocks.map((block, index) => (
            <div key={block.id} className="rounded-md border border-border bg-panel p-3">
              <div className="flex flex-wrap items-center gap-2">
                <code className="text-[9px] text-faint-foreground">{block.id}</code>
                <select value={block.type} disabled={readOnly} onChange={(event) => updateBlock(index, { type: event.target.value as typeof block.type })} className="ml-auto rounded border border-border bg-background text-[9px] text-foreground">
                  <option value="richText">rich text</option>
                  <option value="goal">goal</option>
                  <option value="actor">actor</option>
                  <option value="userJourney">user journey</option>
                  <option value="requirement">requirement context</option>
                  <option value="acceptanceCriterion">acceptance criterion context</option>
                  <option value="businessRule">business rule</option>
                  <option value="constraint">constraint</option>
                  <option value="nonFunctionalRequirement">non-functional requirement</option>
                  <option value="metric">metric</option>
                  <option value="openQuestion">open question</option>
                  <option value="decision">decision</option>
                  <option value="sourceReference">source reference</option>
                  <option value="heading">heading</option>
                  <option value="paragraph">paragraph</option>
                  <option value="list">list</option>
                  <option value="table">table</option>
                  <option value="code">code</option>
                  <option value="callout">callout</option>
                </select>
                {block.type === 'openQuestion' && (
                  <>
                    <label className="flex items-center gap-1 text-[9px] text-muted-foreground"><input type="checkbox" checked={Boolean(block.blocking)} disabled={readOnly} onChange={(event) => updateBlock(index, { blocking: event.target.checked })} />Blocking</label>
                    <select value={block.status ?? 'open'} disabled={readOnly} onChange={(event) => updateBlock(index, { status: event.target.value as NonNullable<typeof block.status> })} className="rounded border border-border bg-background text-[9px] text-foreground">
                      <option value="open">open</option>
                      <option value="answered">answered</option>
                      <option value="resolved">resolved</option>
                      <option value="waived">waived</option>
                    </select>
                  </>
                )}
                <button type="button" disabled={readOnly} onClick={() => onChange({ blocks: content.blocks.filter((_, itemIndex) => itemIndex !== index) })}><Trash2 className="size-3 text-destructive" /></button>
              </div>
              <textarea value={block.text ?? ''} onChange={(event) => updateBlock(index, { text: event.target.value })} readOnly={readOnly} rows={3} className="mt-2 w-full rounded border border-border bg-background p-2 text-[11px] text-foreground" />
            </div>
          ))}
        </div>
      </div>

      {content.kind !== 'projectBrief' && (
        <>
          <div>
            <div className="flex items-center justify-between"><h2 className="text-sm font-semibold text-foreground">Requirements</h2><button type="button" disabled={readOnly} onClick={() => onChange({ requirements: [...(content.requirements ?? []), { id: stableId('req'), title: 'Requirement', statement: '', priority: 'must', acceptanceCriterionIds: [], sourceBlockIds: [] }] })} className="text-[10px] text-primary-bright"><Plus className="mr-1 inline size-3" />Requirement</button></div>
            <div className="mt-2 space-y-2">{(content.requirements ?? []).map((requirement, index) => {
              const updateRequirement = (patch: Partial<typeof requirement>) => onChange({ requirements: content.requirements?.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) })
              return <div key={requirement.id} className="space-y-2 rounded-md border border-border bg-panel p-3"><div className="flex flex-wrap items-center gap-2"><input value={requirement.id} onChange={(event) => updateRequirement({ id: event.target.value })} readOnly={readOnly} className="h-8 min-w-40 rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" aria-label="Stable requirement ID" /><select value={requirement.priority} disabled={readOnly} onChange={(event) => updateRequirement({ priority: event.target.value as typeof requirement.priority })} className="h-8 rounded border border-border bg-background px-2 text-[9px] text-foreground"><option value="must">must</option><option value="should">should</option><option value="could">could</option></select><button type="button" disabled={readOnly} onClick={() => onChange({ requirements: content.requirements?.filter((_, itemIndex) => itemIndex !== index) })} className="ml-auto"><Trash2 className="size-3 text-destructive" /></button></div><input value={requirement.title} onChange={(event) => updateRequirement({ title: event.target.value })} readOnly={readOnly} placeholder="Requirement title" className="h-8 w-full rounded border border-border bg-background px-2 text-[11px] text-foreground" /><textarea value={requirement.statement} onChange={(event) => updateRequirement({ statement: event.target.value })} readOnly={readOnly} rows={2} placeholder="Testable requirement statement" className="w-full rounded border border-border bg-background p-2 text-[11px] text-foreground" /><div className="grid gap-2 sm:grid-cols-2"><label className="text-[9px] text-muted-foreground">Acceptance criterion IDs<input value={requirement.acceptanceCriterionIds.join(', ')} onChange={(event) => updateRequirement({ acceptanceCriterionIds: commaList(event.target.value) })} readOnly={readOnly} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" /></label><label className="text-[9px] text-muted-foreground">Source block IDs<input value={requirement.sourceBlockIds.join(', ')} onChange={(event) => updateRequirement({ sourceBlockIds: commaList(event.target.value) })} readOnly={readOnly} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" /></label></div></div>
            })}</div>
          </div>
          <div>
            <div className="flex items-center justify-between"><h2 className="text-sm font-semibold text-foreground">Acceptance criteria</h2><button type="button" disabled={readOnly} onClick={() => onChange({ acceptanceCriteria: [...content.acceptanceCriteria, { id: stableId('ac'), statement: '', priority: 'must', status: 'open' }] })} className="text-[10px] text-primary-bright"><Plus className="mr-1 inline size-3" />Criterion</button></div>
            <div className="mt-2 space-y-2">{content.acceptanceCriteria.map((criterion, index) => {
              const updateCriterion = (patch: Partial<typeof criterion>) => onChange({ acceptanceCriteria: content.acceptanceCriteria.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item) })
              return <div key={criterion.id} className="grid gap-2 rounded-md border border-border bg-panel p-3 sm:grid-cols-[150px_100px_110px_1fr_auto]"><input value={criterion.id} onChange={(event) => updateCriterion({ id: event.target.value })} readOnly={readOnly} className="h-8 rounded border border-border bg-background px-2 font-mono text-[9px] text-foreground" aria-label="Stable acceptance criterion ID" /><select value={criterion.priority} disabled={readOnly} onChange={(event) => updateCriterion({ priority: event.target.value as typeof criterion.priority })} className="h-8 rounded border border-border bg-background px-2 text-[9px] text-foreground"><option value="must">must</option><option value="should">should</option><option value="could">could</option></select><select value={criterion.status} disabled={readOnly} onChange={(event) => updateCriterion({ status: event.target.value as typeof criterion.status })} className="h-8 rounded border border-border bg-background px-2 text-[9px] text-foreground"><option value="open">open</option><option value="accepted">accepted</option><option value="rejected">rejected</option></select><input value={criterion.statement} onChange={(event) => updateCriterion({ statement: event.target.value })} readOnly={readOnly} className="min-w-0 rounded border border-border bg-background px-2 text-[11px] text-foreground" /><button type="button" disabled={readOnly} onClick={() => onChange({ acceptanceCriteria: content.acceptanceCriteria.filter((_, itemIndex) => itemIndex !== index) })}><Trash2 className="size-3 text-destructive" /></button></div>
            })}</div>
          </div>
          <StringListEditor title="Open questions" values={content.openQuestions} readOnly={readOnly} onChange={(openQuestions) => onChange({ openQuestions })} />
          <StringListEditor title="Assumptions" values={content.assumptions} readOnly={readOnly} onChange={(assumptions) => onChange({ assumptions })} />
        </>
      )}
    </section>
  )
}

function StringListEditor({ title, values, readOnly, onChange }: { title: string; values: readonly string[]; readOnly: boolean; onChange: (values: string[]) => void }) {
  return <div><div className="flex items-center justify-between"><h2 className="text-sm font-semibold text-foreground">{title}</h2><button type="button" disabled={readOnly} onClick={() => onChange([...values, ''])} className="text-[10px] text-primary-bright"><Plus className="mr-1 inline size-3" />Add</button></div><div className="mt-2 space-y-2">{values.map((value, index) => <div key={index} className="flex gap-2 rounded-md border border-border bg-panel p-2"><input value={value} onChange={(event) => onChange(values.map((item, itemIndex) => itemIndex === index ? event.target.value : item))} readOnly={readOnly} className="h-8 min-w-0 flex-1 rounded border border-border bg-background px-2 text-[10px] text-foreground" /><button type="button" disabled={readOnly} onClick={() => onChange(values.filter((_, itemIndex) => itemIndex !== index))}><Trash2 className="size-3 text-destructive" /></button></div>)}</div></div>
}

function GatePanel({ clientIssues, serverGate }: { clientIssues: string[]; serverGate?: ArtifactDetails<DocumentContentDto>['reviewGate'] }) {
  const serverIssues = serverGate?.checks
    .filter((check) => check.severity === 'error' && check.code !== 'canonical_review_approved')
    .map((check) => check.message) ?? []
  const issues = [...clientIssues, ...serverIssues]
  const requestReady = issues.length === 0 && reviewGateReadyForRequest(serverGate)
  const approved = Boolean(serverGate?.passed)
  return <div className={cn('rounded-lg border p-3', approved || requestReady ? 'border-success/30 bg-success/10' : 'border-warning/30 bg-warning/10')}><p className="text-[11px] font-semibold text-foreground">Review gate</p>{approved ? <p className="mt-1 text-[10px] text-success"><Check className="mr-1 inline size-3" />The exact revision has canonical approval.</p> : requestReady ? <p className="mt-1 text-[10px] text-success"><Check className="mr-1 inline size-3" />Pre-review checks passed; canonical reviewer approval is still pending.</p> : <ul className="mt-1 list-disc pl-4 text-[10px] text-warning">{issues.map((issue) => <li key={issue}>{issue}</li>)}{!serverGate && <li>Waiting for the server review gate.</li>}</ul>}</div>
}

function ProposalPanel({ proposals, selected, onSelected, instruction, onInstruction, canEdit, canCreate, onCreate, onApply }: { proposals: ProposalDto[]; selected: Record<string, string[]>; onSelected: (value: Record<string, string[]>) => void; instruction: string; onInstruction: (value: string) => void; canEdit: boolean; canCreate: boolean; onCreate: () => void; onApply: (proposal: ProposalDto) => void }) {
  return <section className="mx-auto max-w-4xl space-y-3"><div className="flex gap-2"><input value={instruction} onChange={(event) => onInstruction(event.target.value)} className="h-9 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><button type="button" onClick={onCreate} disabled={!canEdit || !canCreate || !instruction.trim()} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><Bot className="mr-1 inline size-3" />Ask AI</button></div>{!canCreate && <p className="rounded border border-warning/30 bg-warning/10 p-2 text-[9px] text-warning">Save and create an immutable revision before asking AI. Draft bytes are never sent as workflow truth.</p>}{proposals.map((proposal) => { const selectedIds = selected[proposal.id] ?? []; const hasAccepted = proposal.operations.some((operation) => operation.decision === 'accepted' || selectedIds.includes(operation.id)); return <div key={proposal.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><span className="text-[11px] font-semibold text-foreground">Manifest {proposal.manifest.id.slice(0, 12)}</span><span className="rounded bg-primary/10 px-1.5 py-0.5 text-[9px] text-primary-bright">{proposal.status}</span><code className="ml-auto text-[9px] text-faint-foreground">base {proposal.baseRevision.contentHash.slice(0, 12)}</code></div><div className="mt-2 space-y-1">{proposal.operations.map((operation) => <label key={operation.id} className="flex gap-2 rounded border border-border bg-background p-2 text-[9px] text-muted-foreground"><input type="checkbox" disabled={operation.decision !== 'pending'} checked={operation.decision === 'accepted' || operation.decision === 'applied' || selectedIds.includes(operation.id)} onChange={(event) => onSelected({ ...selected, [proposal.id]: event.target.checked ? [...selectedIds, operation.id] : selectedIds.filter((item) => item !== operation.id) })} /><span className="min-w-0 flex-1"><code>{operation.kind} {operation.path || '/'}</code><span className="ml-2 text-faint-foreground">{operation.decision}</span>{operation.rationale && <span className="mt-1 block">{operation.rationale}</span>}</span></label>)}</div><button type="button" onClick={() => onApply(proposal)} disabled={!canEdit || !hasAccepted || !['open', 'reviewing', 'ready'].includes(proposal.status)} className="mt-2 rounded bg-primary px-2.5 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-50">Decide all and apply accepted operations</button></div>})}</section>
}

function Unavailable({ title, detail, loading, action, onAction, onRetry }: { title: string; detail: string; loading?: boolean; action?: string; onAction?: () => void; onRetry?: () => Promise<void> }) {
  return <div className="flex h-full items-center justify-center bg-canvas p-6 text-center"><div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-6">{loading ? <Loader2 className="mx-auto size-7 animate-spin text-primary-bright" /> : <AlertTriangle className="mx-auto size-7 text-warning" />}<h1 className="mt-3 text-base font-semibold text-foreground">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{detail}</p>{action && onAction && <button type="button" onClick={onAction} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground">{action}</button>}{onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground"><RefreshCw className="mr-1 inline size-4" />Retry</button>}</div></div>
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

function message(error: unknown) {
  return error instanceof Error ? error.message : 'Document operation failed.'
}
