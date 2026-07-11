'use client'

import { useEffect, useMemo, useState } from 'react'
import {
  Check,
  ChevronDown,
  CircleAlert,
  Code2,
  Database,
  Eye,
  FileCode2,
  FilePlus2,
  FileText,
  GitCompareArrows,
  LoaderCircle,
  PackageCheck,
  Monitor,
  PencilLine,
  Play,
  RefreshCw,
  Rocket,
  Save,
  ShieldCheck,
  Sparkles,
  X,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import type {
  FileOperationDto,
  ImplementationProposalDto,
  WorkspaceRevisionDto,
} from '@/lib/platform/flow-contract'
import {
  workbenchBundleNeedsRebase,
  workbenchQueueItemHasAppliedPredecessors,
  type WorkbenchQueueItem,
} from '@/lib/platform/flow-queue'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import type { WorkbenchView } from '@/lib/worksflow/types'
import { FlowPanel } from './flow-panel'
import { DatabasePanel } from './database-panel'
import { ReleasePanel } from './release-panel'
import { ConversationPanel } from './conversation-panel'

const VIEWS: readonly { id: WorkbenchView; label: string; icon: typeof Monitor }[] = [
  { id: 'preview', label: 'Preview', icon: Monitor },
  { id: 'code', label: 'Implementation', icon: Code2 },
  { id: 'database', label: 'Data', icon: Database },
]

export function PlatformWorkbench() {
  const { view, setView, setSurface } = useWorksflow()
  const { session, project, backendStatus, can } = useCollaboration()
  const flow = usePlatformFlow()
  const artifacts = useArtifactWorkspace()
  const [prototypeId, setPrototypeId] = useState('')
  const [showRelease, setShowRelease] = useState(false)
  const [showConversation, setShowConversation] = useState(true)
  const selectedPrototype = artifacts.prototypes.find((item) => item.artifact.id === prototypeId)
    ?? artifacts.prototypes.find((item) => item.approvedRevision)
    ?? artifacts.prototypes[0]

  useEffect(() => {
    if (!prototypeId && selectedPrototype) setPrototypeId(selectedPrototype.artifact.id)
  }, [prototypeId, selectedPrototype])

  const unavailable = !session.signedIn || !project || backendStatus === 'error'

  return (
    <div className="relative flex h-full flex-col">
      <header className="flex min-h-12 shrink-0 items-center gap-3 border-b border-border bg-panel px-3 max-md:flex-wrap">
        <button
          type="button"
          onClick={() => setSurface('recent')}
          className="flex min-w-0 items-center gap-2 rounded-md px-2 py-1 hover:bg-white/5"
          title="Open project list"
        >
          <span className="flex size-6 items-center justify-center rounded bg-primary text-[10px] font-bold text-primary-foreground">
            {(project?.name ?? 'W').slice(0, 1).toUpperCase()}
          </span>
          <span className="max-w-52 truncate text-xs font-semibold text-foreground">
            {project?.name ?? 'Select a server project'}
          </span>
        </button>

        <nav className="flex items-center gap-1 rounded-md border border-border bg-background p-0.5" aria-label="Workbench view">
          {VIEWS.map((item) => {
            const Icon = item.icon
            return (
              <button
                key={item.id}
                type="button"
                onClick={() => setView(item.id)}
                className={cn(
                  'inline-flex h-7 items-center gap-1.5 rounded px-2 text-[10px] font-medium',
                  view === item.id
                    ? 'bg-primary/15 text-primary-bright'
                    : 'text-faint-foreground hover:text-foreground',
                )}
              >
                <Icon className="size-3" /> {item.label}
              </button>
            )
          })}
        </nav>

        <div className="ml-auto flex min-w-0 items-center gap-2 max-md:ml-0 max-md:w-full">
          <button
            type="button"
            onClick={() => setShowConversation(true)}
            disabled={unavailable}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-primary/30 bg-primary/10 px-3 text-[10px] font-semibold text-primary-bright disabled:opacity-40"
            title="Open the governed conversation control plane"
          >
            <Sparkles className="size-3.5" /> Conversation
          </button>
          <select
            value={selectedPrototype?.artifact.id ?? ''}
            onChange={(event) => setPrototypeId(event.target.value)}
            disabled={unavailable || artifacts.status !== 'ready' || artifacts.prototypes.length === 0}
            className="h-8 min-w-0 max-w-64 flex-1 rounded-md border border-border bg-background px-2 text-[10px] text-foreground outline-none disabled:opacity-40"
            aria-label="Approved prototype build input"
          >
            {artifacts.prototypes.length === 0 && <option value="">No server prototype</option>}
            {artifacts.prototypes.map((prototype) => (
              <option key={prototype.artifact.id} value={prototype.artifact.id}>
                {prototype.artifact.title} · {prototype.approvedRevision ? `approved r${prototype.approvedRevision.revisionNumber}` : 'not approved'}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={() => selectedPrototype && void flow.createBundle(selectedPrototype)}
            disabled={unavailable || !can('edit') || !selectedPrototype?.approvedRevision || flow.busy}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-40"
            title="Compile exact approved sources into an immutable application build manifest"
          >
            <PackageCheck className="size-3.5" /> Freeze build input
          </button>
          <button
            type="button"
            onClick={() => setShowRelease(true)}
            disabled={unavailable || !flow.workspaceRevision}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-border bg-background px-3 text-[10px] font-semibold text-muted-foreground hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
            title="Run quality gates, export, publish, roll back, or deliver through GitHub"
          >
            <Rocket className="size-3.5" /> Release
          </button>
        </div>
      </header>

      <div className="flex min-h-0 flex-1 max-lg:flex-col">
        <FlowPanel />
        <main className="min-h-0 min-w-0 flex-1 bg-canvas p-3 max-lg:min-h-[520px]">
          <div className="h-full overflow-hidden rounded-lg border border-border bg-panel">
            {unavailable ? (
              <ServiceGate
                title={!session.signedIn ? 'Sign in to use the application Workbench' : !project ? 'Select a server project' : 'Go platform unavailable'}
                description="Workbench does not generate from browser mock data. A signed-in project and the Go Artifact/Workflow/Manifest services are required."
              />
            ) : flow.status === 'loading' ? (
              <ServiceGate loading title="Loading server workflow state" description="Resolving exact input and output references…" />
            ) : flow.status === 'error' ? (
              <ServiceGate title="Workflow service unavailable" description={flow.error ?? 'Generation and shared edits remain disabled until the service recovers.'} onRetry={flow.refresh} />
            ) : view === 'preview' ? (
              <PlatformPreview />
            ) : view === 'code' ? (
              <ImplementationWorkspace />
            ) : (
              <DatabasePanel />
            )}
          </div>
        </main>
      </div>
      {showConversation && <ConversationPanel onClose={() => setShowConversation(false)} />}
      {showRelease && <ReleasePanel onClose={() => setShowRelease(false)} />}
    </div>
  )
}

function ImplementationWorkspace() {
  const flow = usePlatformFlow()
  const { can } = useCollaboration()
  const files = flow.workspaceRevision?.content.files ?? []
  const [selectedPath, setSelectedPath] = useState('')
  const selectedFile = selectedPath
    ? files.find((item) => item.path === selectedPath)
    : files[0]
  const [draft, setDraft] = useState(selectedFile?.content ?? '')
  const [newPath, setNewPath] = useState('')
  const [instruction, setInstruction] = useState('Build a complete runnable implementation from the frozen manifest. Preserve exact traceability and include tests.')
  const [model, setModel] = useState('gpt-5')
  const [showManifest, setShowManifest] = useState(true)
  const selectedQueueIndex = flow.workbenchQueue.findIndex(
    (item) => item.bundleId === flow.selectedBundleId,
  )
  const currentQueueItem = selectedQueueIndex >= 0
    ? flow.workbenchQueue[selectedQueueIndex]
    : undefined
  const orderedGenerationAllowed = workbenchQueueItemHasAppliedPredecessors(
    flow.workbenchQueue,
    selectedQueueIndex,
  )
  const blockingPredecessor = flow.workbenchQueue
    .slice(0, Math.max(selectedQueueIndex, 0))
    .find((item) => !proposalApplied(item.proposal))
  const activeProposal = currentQueueItem?.proposal
  const regenerationAllowed = !activeProposal || (
    activeProposal.status === 'open'
    && activeProposal.executionSource !== 'conversation_command'
    && activeProposal.operations.every((operation) => operation.decision === 'pending')
  )

  useEffect(() => {
    if (!selectedPath && selectedFile) setSelectedPath(selectedFile.path)
    setDraft(selectedFile?.content ?? '')
  }, [selectedFile?.content, selectedFile?.path, selectedPath])

  if (!flow.bundle) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        <WorkbenchGroupTabs />
        <ServiceGate
          title={flow.workbenchGroups.length > 0 ? 'Loading selected Workbench group' : 'Freeze an approved prototype first'}
          description={flow.workbenchGroups.length > 0 ? 'Resolving only this workflow node’s frozen manifest lineage.' : 'Select an approved prototype above. The server will compile its exact PageSpec, requirement, blueprint, fixture, token, component, and trace revisions into a build manifest.'}
        />
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <WorkbenchGroupTabs />
      <section className="shrink-0 border-b border-border bg-background/40 p-3">
        <div className="flex flex-wrap items-start gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <ShieldCheck className="size-4 text-success" />
              <h2 className="text-xs font-semibold text-foreground">Frozen application build manifest</h2>
              {flow.workbenchProgress.total > 1 && (
                <span className="rounded bg-primary/10 px-1.5 py-0.5 text-[8px] font-semibold text-primary-bright">
                  {flow.workbenchProgress.applied}/{flow.workbenchProgress.total} applied
                </span>
              )}
              <button type="button" onClick={() => setShowManifest((value) => !value)} className="rounded p-1 text-faint-foreground hover:text-foreground" aria-label="Toggle manifest details"><ChevronDown className={cn('size-3 transition-transform', !showManifest && '-rotate-90')} /></button>
            </div>
            <p className="mt-1 truncate font-mono text-[9px] text-faint-foreground" title={flow.bundle.contentHash}>
              {currentQueueItem && flow.bundle.id !== currentQueueItem.bundleId
                ? `${currentQueueItem.bundleId} → ${flow.bundle.id}`
                : flow.bundle.id} · {flow.bundle.contentHash}
            </p>
          </div>
          <div className="flex min-w-[320px] flex-1 gap-1.5 max-md:min-w-0 max-md:basis-full">
            <input value={model} onChange={(event) => setModel(event.target.value)} className="h-8 w-24 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground outline-none" aria-label="Generation model" />
            <input value={instruction} onChange={(event) => setInstruction(event.target.value)} className="h-8 min-w-0 flex-1 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground outline-none" aria-label="Implementation instruction" />
            <button type="button" onClick={() => void flow.generateImplementation(instruction, model)} disabled={!can('edit') || flow.busy || !instruction.trim() || !model.trim() || !orderedGenerationAllowed || flow.requiresWorkbenchRebase || proposalApplied(currentQueueItem?.proposal) || !regenerationAllowed} className="inline-flex h-8 shrink-0 items-center gap-1 rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-40" title={!regenerationAllowed ? 'A reviewed or conversation-owned proposal cannot be superseded.' : undefined}>
              {flow.busy ? <LoaderCircle className="size-3 animate-spin" /> : <Sparkles className="size-3" />} {!orderedGenerationAllowed ? 'Blocked by order' : !regenerationAllowed ? 'Finish current review' : currentQueueItem?.proposal ? 'Regenerate proposal' : 'Generate proposal'}
            </button>
          </div>
        </div>
        {!orderedGenerationAllowed && currentQueueItem && blockingPredecessor && (
          <div role="status" className="mt-3 flex items-center gap-2 rounded-md border border-warning/35 bg-warning/10 px-3 py-2 text-[9px] leading-relaxed text-warning">
            <CircleAlert className="size-4 shrink-0" />
            <span>Blocked by frozen manifest order. Apply {blockingPredecessor.sliceId ?? blockingPredecessor.bundleId} before generating or proposing file changes for {currentQueueItem.sliceId ?? currentQueueItem.bundleId}.</span>
          </div>
        )}
        {flow.requiresWorkbenchRebase && flow.workspaceRevision && currentQueueItem && (
          <div role="status" className="mt-3 flex items-center gap-3 rounded-md border border-warning/35 bg-warning/10 px-3 py-2">
            <GitCompareArrows className="size-4 shrink-0 text-warning" />
            <p className="min-w-0 flex-1 text-[9px] leading-relaxed text-warning">
              Rebase active page bundle {flow.bundle.id} (order root {currentQueueItem.bundleId}) onto exact workspace r{flow.workspaceRevision.revisionNumber} ({flow.workspaceRevision.id}) before generation. The server creates a new derived manifest; prior proposal decisions are not migrated.
            </p>
            <button type="button" onClick={() => void flow.rebaseWorkbenchBundle()} disabled={!can('edit') || flow.busy} className="inline-flex h-7 shrink-0 items-center gap-1 rounded bg-warning px-2 text-[9px] font-semibold text-black disabled:opacity-40">
              <GitCompareArrows className="size-3" /> Rebase next bundle
            </button>
          </div>
        )}
        {flow.workbenchQueue.length > 1 && (
          <div className="mt-3 flex items-center gap-1.5 overflow-x-auto pb-0.5 scrollbar-thin" aria-label="Page build queue">
            <span className="mr-1 shrink-0 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">Manifest order</span>
            {flow.workbenchQueue.map((item, index) => {
              const state = workbenchQueueItemState(
                flow.workbenchQueue,
                index,
                flow.workspaceRevision,
              )
              return (
                <button
                  key={item.bundleId}
                  type="button"
                  onClick={() => flow.selectWorkbenchBundle(item.bundleId)}
                  className={cn(
                    'inline-flex h-7 shrink-0 items-center gap-1.5 rounded border px-2 text-[9px]',
                    item.bundleId === flow.selectedBundleId
                      ? 'border-primary/50 bg-primary/10 text-primary-bright'
                      : 'border-border bg-panel text-muted-foreground hover:text-foreground',
                  )}
                  title={`${item.sliceId ?? item.bundleId} · ${state}`}
                >
                  <span className="font-semibold">{index + 1}</span>
                  <span className="max-w-32 truncate">{item.sliceId ?? `Page ${index + 1}`}</span>
                  <span className={cn(
                    'rounded px-1 py-0.5 text-[7px] font-semibold uppercase',
                    state === 'applied'
                      ? 'bg-success/15 text-success'
                      : state === 'ready'
                        ? 'bg-primary/15 text-primary-bright'
                        : 'bg-warning/10 text-warning',
                  )}>{state}</span>
                </button>
              )
            })}
          </div>
        )}
        {showManifest && <ManifestFacts />}
      </section>

      <div className="flex min-h-0 flex-1 max-md:flex-col">
        <aside className="flex w-56 shrink-0 flex-col border-r border-border bg-panel max-md:h-40 max-md:w-full max-md:border-b max-md:border-r-0">
          <div className="flex h-9 items-center gap-2 border-b border-border px-2.5 text-[10px] font-semibold text-faint-foreground">
            <FileCode2 className="size-3.5" /> Applied workspace
            <span className="ml-auto rounded bg-white/5 px-1.5 py-0.5 font-mono text-[8px]">{files.length}</span>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-1.5 scrollbar-thin">
            {files.length === 0 && <p className="p-3 text-center text-[9px] leading-relaxed text-faint-foreground">No applied workspace revision yet. Generate, review, and apply a proposal.</p>}
            {files.map((file) => (
              <button key={file.path} type="button" onClick={() => setSelectedPath(file.path)} className={cn('mb-0.5 flex w-full items-center gap-1.5 rounded px-2 py-1.5 text-left text-[10px]', selectedFile?.path === file.path ? 'bg-primary/10 text-primary-bright' : 'text-muted-foreground hover:bg-white/5 hover:text-foreground')}>
                <FileText className="size-3 shrink-0" /><span className="truncate">{file.path}</span>
              </button>
            ))}
          </div>
          <div className="border-t border-border p-2">
            <div className="flex gap-1">
              <input value={newPath} onChange={(event) => setNewPath(event.target.value)} placeholder="src/new-file.ts" className="h-7 min-w-0 flex-1 rounded border border-border bg-background px-1.5 font-mono text-[9px] text-foreground outline-none" />
              <button type="button" onClick={() => { if (!newPath.trim()) return; setSelectedPath(newPath.trim()); setDraft(''); }} disabled={!can('edit')} className="flex size-7 items-center justify-center rounded border border-border text-faint-foreground hover:text-foreground disabled:opacity-40" aria-label="Prepare new file proposal"><FilePlus2 className="size-3" /></button>
            </div>
          </div>
        </aside>

        <section className="flex min-w-0 flex-1 flex-col bg-background">
          <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3 text-[10px] text-muted-foreground">
            <FileCode2 className="size-3 text-primary-bright" />
            <span className="min-w-0 flex-1 truncate font-mono">{selectedPath || selectedFile?.path || 'Select a workspace file'}</span>
            {(selectedPath || selectedFile) && (
              <button
                type="button"
                onClick={() => void flow.proposeFileChange(
                  selectedPath || selectedFile!.path,
                  draft,
                  selectedFile?.language,
                  selectedFile?.contentHash,
                )}
                disabled={!can('edit') || flow.busy || !orderedGenerationAllowed || (!selectedPath && !selectedFile) || draft === selectedFile?.content}
                className="inline-flex h-7 items-center gap-1 rounded bg-primary px-2 text-[9px] font-semibold text-primary-foreground disabled:opacity-35"
                title={orderedGenerationAllowed ? 'Create a reviewable implementation proposal; never mutate the workspace directly' : 'Apply earlier page bundles before proposing a file change'}
              >
                <Save className="size-3" /> Propose change
              </button>
            )}
          </div>
          {selectedPath || selectedFile ? (
            <textarea
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              readOnly={!can('edit')}
              spellCheck={false}
              className="min-h-0 flex-1 resize-none bg-background p-4 font-mono text-[11px] leading-5 text-muted-foreground outline-none read-only:opacity-70"
              aria-label="Server workspace proposal editor"
            />
          ) : (
            <div className="flex flex-1 items-center justify-center text-[10px] text-faint-foreground">Apply a proposal to create the first canonical workspace revision.</div>
          )}
        </section>

        <ProposalReview />
      </div>
    </div>
  )
}

function ManifestFacts() {
  const { bundle } = usePlatformFlow()
  if (!bundle) return null
  const facts = [
    ['Project Brief / requirements', bundle.requirementRevisions.length],
    ['Blueprint revision', compactRef(bundle.blueprintRevision)],
    ['PageSpec revision', compactRef(bundle.pageSpecRevision)],
    ['Prototype revision', compactRef(bundle.prototypeRevision)],
    ['Contracts', bundle.contractRevisions.length],
    ['Design system refs', bundle.designSystemRevisions.length],
    ['Context evidence', bundle.contextRevisions?.length ?? 0],
    ['Workflow input', bundle.workflowContext ? `${bundle.workflowContext.inputManifest.jobType} · ${bundle.workflowContext.inputManifest.sources.length} sources` : 'legacy / manual'],
    ['Rendered states', bundle.renderedFrames.length],
  ] as const
  return (
    <>
      <div className="mt-3 grid grid-cols-4 gap-1.5 max-xl:grid-cols-2 max-md:grid-cols-1">
        {facts.map(([label, value]) => (
          <div key={label} className="rounded border border-border bg-panel px-2 py-1.5">
            <div className="text-[8px] uppercase tracking-wider text-faint-foreground">{label}</div>
            <div className="mt-0.5 truncate font-mono text-[9px] text-muted-foreground" title={String(value)}>{value}</div>
          </div>
        ))}
      </div>
      {((bundle.contextRevisions?.length ?? 0) > 0 || bundle.workflowContext) && (
        <details className="mt-2 rounded border border-border bg-panel">
          <summary className="cursor-pointer px-2 py-1.5 text-[8px] font-semibold text-faint-foreground">Inspect frozen context evidence</summary>
          <div className="space-y-1 border-t border-border p-2 font-mono text-[8px] text-muted-foreground">
            {bundle.contextRevisions?.map((context) => (
              <div key={`${context.kind}:${context.revision.revisionId}:${context.revision.anchorId ?? ''}`} className="truncate" title={JSON.stringify(context.revision)}>
                context:{context.kind} · {compactRef(context.revision)}{context.revision.anchorId ? `#${context.revision.anchorId}` : ''}
              </div>
            ))}
            {bundle.workflowContext && (
              <>
                <div className="truncate" title={bundle.workflowContext.definition.hash}>definition · {bundle.workflowContext.definition.id}@v{bundle.workflowContext.definition.version}</div>
                <div className="truncate" title={bundle.workflowContext.inputManifest.hash}>input manifest · {bundle.workflowContext.inputManifest.id} · {bundle.workflowContext.inputManifest.jobType}/{bundle.workflowContext.inputManifest.outputSchemaVersion}</div>
                {bundle.workflowContext.inputManifest.baseRevision && <div className="truncate" title={JSON.stringify(bundle.workflowContext.inputManifest.baseRevision)}>base · {compactRef(bundle.workflowContext.inputManifest.baseRevision)}</div>}
                {bundle.workflowContext.inputManifest.sources.map((source, index) => (
                  <div key={`${source.purpose}:${source.ref.revisionId}:${source.ref.anchorId ?? ''}:${index}`} className="truncate" title={JSON.stringify(source.ref)}>
                    {source.purpose} · {compactRef(source.ref)}{source.ref.anchorId ? `#${source.ref.anchorId}` : ''}
                  </div>
                ))}
                <pre className="max-h-28 overflow-auto whitespace-pre-wrap rounded bg-black/20 p-1.5">run scope {JSON.stringify(bundle.workflowContext.runScope ?? {}, null, 2)}</pre>
              </>
            )}
          </div>
        </details>
      )}
    </>
  )
}

function ProposalReview() {
  const flow = usePlatformFlow()
  const { can } = useCollaboration()
  const [rejectionReason, setRejectionReason] = useState('Not required for this implementation scope')
  const proposal = flow.proposal
  const queueIndex = flow.workbenchQueue.findIndex(
    (item) => item.bundleId === flow.selectedBundleId,
  )
  const queueTotal = flow.workbenchQueue.length
  const isLastQueueItem = queueIndex >= 0 && queueIndex === queueTotal - 1
  const applyLabel = flow.canCompleteWorkbench
    ? 'Complete Workbench'
    : queueTotal > 1
      ? isLastQueueItem ? 'Apply and complete Workbench' : 'Apply and continue'
      : 'Apply accepted operations'
  const applyDescription = flow.canCompleteWorkbench
    ? 'Every frozen page has an applied proposal. Submit the ordered proposal set and final workspace revision to continue the workflow.'
    : flow.requiresWorkbenchRebase
      ? 'This proposal is bound to an older manifest. Rebase the active page bundle; prior operation decisions will not be copied.'
      : proposal?.status === 'ready' && !flow.canApplyProposal
        ? 'Apply earlier pages first. Each proposal is rebased onto the latest workspace in frozen manifest order.'
        : queueTotal > 1
          ? 'Apply creates an immutable workspace revision, then advances to the next frozen page bundle.'
          : 'Apply creates an approved immutable workspace revision and consumes this exact build manifest.'

  return (
    <aside className="flex w-[330px] shrink-0 flex-col border-l border-border bg-panel max-xl:w-72 max-md:h-80 max-md:w-full max-md:border-l-0 max-md:border-t">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-2.5">
        <GitCompareArrows className="size-3.5 text-primary-bright" />
        <span className="text-[10px] font-semibold text-foreground">Reviewable output proposal</span>
        {queueTotal > 1 && queueIndex >= 0 && (
          <span className="ml-auto text-[8px] font-semibold text-faint-foreground">{queueIndex + 1}/{queueTotal}</span>
        )}
        {proposal && <span className={cn('rounded bg-white/5 px-1.5 py-0.5 font-mono text-[8px] text-faint-foreground', queueTotal <= 1 && 'ml-auto')}>v{proposal.version}</span>}
      </div>
      {!proposal ? (
        <div className="flex flex-1 items-center justify-center p-4 text-center text-[9px] leading-relaxed text-faint-foreground">
          AI output appears here as file operations. Nothing is written to the canonical workspace until every operation is decided and the proposal is applied.
        </div>
      ) : (
        <>
          <div className="border-b border-border p-2">
            <div className="flex items-center gap-2 text-[9px]">
              <span className={cn('rounded px-1.5 py-0.5 font-semibold', proposal.status === 'ready' ? 'bg-success/15 text-success' : proposal.status.includes('applied') ? 'bg-primary/15 text-primary-bright' : 'bg-warning/15 text-warning')}>{proposal.status}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-faint-foreground" title={proposal.payloadHash}>{proposal.payloadHash}</span>
            </div>
            <div className="mt-2 grid grid-cols-2 gap-1">
              <button type="button" onClick={() => void flow.decideAllPending('accepted')} disabled={!can('edit') || flow.busy || flow.requiresWorkbenchRebase || proposal.operations.every((operation) => operation.decision !== 'pending')} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-success/15 text-[9px] font-medium text-success disabled:opacity-35"><Check className="size-3" /> Accept pending</button>
              <button type="button" onClick={() => void flow.decideAllPending('rejected', rejectionReason)} disabled={!can('edit') || flow.busy || flow.requiresWorkbenchRebase || proposal.operations.every((operation) => operation.decision !== 'pending')} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-destructive/10 text-[9px] font-medium text-destructive disabled:opacity-35"><X className="size-3" /> Reject pending</button>
            </div>
            <input value={rejectionReason} onChange={(event) => setRejectionReason(event.target.value)} className="mt-1.5 h-7 w-full rounded border border-border bg-background px-2 text-[9px] text-foreground outline-none" aria-label="Operation rejection reason" />
          </div>
          <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-2 scrollbar-thin">
            <ProposalOutputSummary proposal={proposal} />
            <div className="flex items-center gap-2 px-0.5 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">
              <span>File operations</span>
              <span className="ml-auto rounded bg-white/5 px-1 py-0.5 font-mono">{proposal.operations.length}</span>
            </div>
            {proposal.operations.map((operation) => <OperationCard key={operation.id} operation={operation} />)}
          </div>
          <div className="border-t border-border p-2">
            {proposal.unimplementedItems.length > 0 && <p className="mb-2 text-[8px] leading-relaxed text-warning">Unimplemented: {proposal.unimplementedItems.join(' · ')}</p>}
            <button type="button" onClick={() => void flow.applyProposal()} disabled={!can('edit') || flow.busy || (!flow.canCompleteWorkbench && (proposal.status !== 'ready' || !flow.canApplyProposal))} className="inline-flex h-8 w-full items-center justify-center gap-1.5 rounded bg-primary text-[10px] font-semibold text-primary-foreground disabled:cursor-not-allowed disabled:opacity-35">
              {flow.busy ? <LoaderCircle className="size-3 animate-spin" /> : <Play className="size-3" />} {applyLabel}
            </button>
            <p className="mt-1.5 text-[8px] leading-relaxed text-faint-foreground">{applyDescription}</p>
          </div>
        </>
      )}
    </aside>
  )
}

function ProposalOutputSummary({ proposal }: { proposal: ImplementationProposalDto }) {
  const sections: readonly { readonly label: string; readonly values: readonly unknown[] }[] = [
    { label: 'Routes', values: proposal.routes },
    { label: 'APIs', values: proposal.apis },
    { label: 'Migrations', values: proposal.migrations },
    { label: 'Tests', values: proposal.tests },
    { label: 'Previews', values: proposal.previews },
    { label: 'Trace links', values: proposal.traceLinks },
    { label: 'Diagnostics', values: proposal.diagnostics },
    { label: 'Assumptions', values: proposal.assumptions },
    { label: 'Unimplemented', values: proposal.unimplementedItems },
  ]

  return (
    <section className="rounded-md border border-border bg-background p-2" aria-label="Implementation proposal output summary">
      <div className="mb-1.5 flex items-center gap-2 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">
        <PackageCheck className="size-3 text-primary-bright" /> Output contract
      </div>
      <div className="grid grid-cols-2 gap-1">
        {sections.map((section) => (
          <details key={section.label} className="group rounded border border-border/70 bg-panel open:col-span-2">
            <summary className="flex cursor-pointer list-none items-center gap-1 px-1.5 py-1 text-[8px] text-muted-foreground hover:text-foreground">
              <ChevronDown className="size-2.5 -rotate-90 transition-transform group-open:rotate-0" />
              <span className="min-w-0 flex-1 truncate">{section.label}</span>
              <span className="rounded bg-white/5 px-1 font-mono text-[7px] text-faint-foreground">{section.values.length}</span>
            </summary>
            <div className="space-y-1 border-t border-border/70 p-1.5">
              {section.values.length === 0 ? (
                <p className="text-[8px] text-faint-foreground">None declared.</p>
              ) : section.values.map((value, index) => (
                <pre key={index} className="max-h-32 overflow-auto whitespace-pre-wrap break-words rounded bg-black/20 p-1.5 font-mono text-[8px] leading-relaxed text-faint-foreground scrollbar-thin">
                  {proposalSummaryValue(value)}
                </pre>
              ))}
            </div>
          </details>
        ))}
      </div>
    </section>
  )
}

function OperationCard({ operation }: { operation: FileOperationDto }) {
  const flow = usePlatformFlow()
  const { can } = useCollaboration()
  const [expanded, setExpanded] = useState(false)
  return (
    <article className="rounded-md border border-border bg-background p-2">
      <button type="button" onClick={() => setExpanded((value) => !value)} className="flex w-full items-start gap-2 text-left">
        <OperationIcon kind={operation.kind} />
        <span className="min-w-0 flex-1">
          <span className="block truncate font-mono text-[9px] text-foreground">{operation.path}</span>
          <span className="mt-0.5 block text-[8px] text-faint-foreground">{operation.kind} · {operation.decision}</span>
        </span>
        <ChevronDown className={cn('size-3 text-faint-foreground transition-transform', !expanded && '-rotate-90')} />
      </button>
      {expanded && (
        <div className="mt-2 border-t border-border pt-2">
          {operation.rationale && <p className="mb-2 text-[8px] leading-relaxed text-muted-foreground">{operation.rationale}</p>}
          {operation.content !== undefined && <pre className="max-h-36 overflow-auto whitespace-pre-wrap rounded bg-black/20 p-2 font-mono text-[8px] leading-relaxed text-faint-foreground scrollbar-thin">{operation.content}</pre>}
          {operation.decision === 'pending' && (
            <div className="mt-2 grid grid-cols-2 gap-1">
              <button type="button" onClick={() => void flow.decideOperation(operation, 'accepted')} disabled={!can('edit') || flow.busy || flow.requiresWorkbenchRebase} className="inline-flex h-6 items-center justify-center gap-1 rounded bg-success/15 text-[8px] font-medium text-success disabled:opacity-35"><Check className="size-2.5" />Accept</button>
              <button type="button" onClick={() => void flow.decideOperation(operation, 'rejected', 'Rejected during file review')} disabled={!can('edit') || flow.busy || flow.requiresWorkbenchRebase} className="inline-flex h-6 items-center justify-center gap-1 rounded bg-destructive/10 text-[8px] font-medium text-destructive disabled:opacity-35"><X className="size-2.5" />Reject</button>
            </div>
          )}
        </div>
      )}
    </article>
  )
}

function PlatformPreview() {
  const flow = usePlatformFlow()
  const files = flow.workspaceRevision?.content.files ?? []
  const [viewport, setViewport] = useState<'desktop' | 'tablet' | 'mobile'>('desktop')
  const preview = useMemo(() => previewDocument(files), [files])
  const width = viewport === 'desktop' ? '100%' : viewport === 'tablet' ? 768 : 390

  if (!flow.workspaceRevision) {
    return <ServiceGate title="No applied application revision" description="Generate an implementation proposal, review every file operation, and apply it before previewing." />
  }
  if (!preview) {
    return <ServiceGate title="Preview entry not found" description="The canonical workspace revision has no index.html or HTML entry. Add one through a reviewable file proposal." />
  }

  return (
    <div className="flex h-full flex-col bg-canvas">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-border bg-panel px-3">
        <Eye className="size-3.5 text-primary-bright" />
        <span className="text-[10px] font-semibold text-foreground">Applied revision preview</span>
        <span className="min-w-0 flex-1 truncate font-mono text-[8px] text-faint-foreground" title={flow.workspaceRevision.contentHash}>{flow.workspaceRevision.id} · {flow.workspaceRevision.contentHash}</span>
        {(['desktop', 'tablet', 'mobile'] as const).map((item) => (
          <button key={item} type="button" onClick={() => setViewport(item)} className={cn('rounded px-2 py-1 text-[9px]', viewport === item ? 'bg-primary/15 text-primary-bright' : 'text-faint-foreground hover:text-foreground')}>{item}</button>
        ))}
      </div>
      <div className="flex min-h-0 flex-1 justify-center overflow-auto bg-[#08080a] p-3 scrollbar-thin">
        <iframe
          title="Canonical application preview"
          srcDoc={preview}
          sandbox="allow-forms allow-modals allow-popups allow-scripts"
          className="h-full rounded-md border border-border bg-white shadow-2xl transition-[width]"
          style={{ width }}
        />
      </div>
    </div>
  )
}

function ServiceGate({ title, description, loading, onRetry }: { title: string; description: string; loading?: boolean; onRetry?: () => Promise<void> }) {
  return (
    <div className="flex h-full items-center justify-center bg-canvas p-6">
      <div className="max-w-lg rounded-xl border border-dashed border-border bg-panel p-7 text-center">
        {loading ? <LoaderCircle className="mx-auto mb-3 size-6 animate-spin text-primary-bright" /> : <CircleAlert className="mx-auto mb-3 size-6 text-faint-foreground" />}
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        <p className="mt-2 text-[10px] leading-relaxed text-faint-foreground">{description}</p>
        {onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground"><RefreshCw className="size-3" /> Retry server state</button>}
      </div>
    </div>
  )
}

function WorkbenchGroupTabs() {
  const flow = usePlatformFlow()
  if (flow.workbenchGroups.length <= 1) return null
  return (
    <div className="flex shrink-0 items-center gap-1.5 overflow-x-auto border-b border-border bg-background/60 px-3 py-2 scrollbar-thin" aria-label="Workbench groups">
      <span className="mr-1 shrink-0 text-[8px] font-semibold uppercase tracking-wider text-faint-foreground">DAG groups</span>
      {flow.workbenchGroups.map((group, index) => (
        <button
          key={group.nodeKey}
          type="button"
          onClick={() => void flow.selectWorkbenchGroup(group.nodeKey)}
          className={cn(
            'inline-flex h-7 shrink-0 items-center gap-1.5 rounded border px-2 text-[9px]',
            group.nodeKey === flow.selectedWorkbenchNodeKey
              ? 'border-primary/50 bg-primary/10 text-primary-bright'
              : 'border-border bg-panel text-muted-foreground hover:text-foreground',
          )}
          aria-label={`Workbench group ${group.nodeKey} ${group.status.replaceAll('_', ' ')}`}
          title={`${group.manifestGroupKey ?? group.nodeKey} · ${group.references.length} root bundle(s)`}
        >
          <span className="font-semibold">{index + 1}</span>
          <span>{group.sliceId ?? group.nodeKey}</span>
          <span className="rounded bg-white/5 px-1 py-0.5 text-[7px] uppercase">{group.status.replaceAll('_', ' ')}</span>
        </button>
      ))}
    </div>
  )
}

function OperationIcon({ kind }: { kind: FileOperationDto['kind'] }) {
  if (kind === 'file.delete') return <X className="mt-0.5 size-3 shrink-0 text-destructive" />
  if (kind === 'file.rename') return <PencilLine className="mt-0.5 size-3 shrink-0 text-warning" />
  return <FilePlus2 className="mt-0.5 size-3 shrink-0 text-success" />
}

function compactRef(ref: { artifactId: string; revisionId: string }) {
  return `${ref.artifactId.slice(0, 8)}:${ref.revisionId.slice(0, 8)}`
}

function proposalApplied(proposal: ImplementationProposalDto | null | undefined) {
  return proposal?.status === 'applied' || proposal?.status === 'partially_applied'
}

function workbenchQueueItemState(
  queue: readonly WorkbenchQueueItem[],
  index: number,
  workspace: WorkspaceRevisionDto | null,
) {
  const item = queue[index]
  if (proposalApplied(item.proposal)) return 'applied'
  if (!workbenchQueueItemHasAppliedPredecessors(queue, index)) return 'blocked'
  if (workbenchBundleNeedsRebase(item.bundle, workspace)) return 'rebase'
  if (!item.proposal) return 'generate'
  if (item.proposal.status === 'ready') return 'ready'
  if (item.proposal.status === 'stale') return 'stale'
  return 'review'
}

function proposalSummaryValue(value: unknown) {
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

function previewDocument(files: readonly { path: string; content: string }[]) {
  const html = files.find((file) => /(^|\/)index\.html$/i.test(file.path))
    ?? files.find((file) => file.path.toLowerCase().endsWith('.html'))
  if (!html) return null
  const css = files.filter((file) => file.path.toLowerCase().endsWith('.css')).map((file) => file.content).join('\n')
  const script = files.filter((file) => /\.(m?js)$/i.test(file.path)).map((file) => file.content).join('\n')
  return html.content
    .replace('</head>', `<style>${css.replaceAll('</style>', '<\\/style>')}</style></head>`)
    .replace('</body>', `<script>${script.replaceAll('</script>', '<\\/script>')}</script></body>`)
}
