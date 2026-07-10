'use client'

import { useMemo, useState } from 'react'
import {
  Check,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  GitBranch,
  History as HistoryIcon,
  PencilLine,
  Play,
  RefreshCw,
  RotateCcw,
  Save,
  Send,
  ShieldCheck,
  Square,
  UploadCloud,
  Workflow,
  X,
} from 'lucide-react'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import {
  revisionRef,
  usePlatformFlow,
} from '@/lib/platform/flow-provider'
import type {
  CreateWorkflowDefinitionInputDto,
  ExactArtifactRefDto,
  WorkflowDefinitionRecordDto,
  WorkflowNodeDefinitionDto,
  WorkflowNodeRunDto,
} from '@/lib/platform/flow-contract'
import type { JsonObject, JsonValue } from '@/lib/platform/dto'
import { cn } from '@/lib/utils'
import { WorkflowGraphEditor } from './workflow-graph-editor'

type EditorMode = 'closed' | 'create' | 'version'

export function FlowPanel() {
  const flow = usePlatformFlow()
  const { can, session, project } = useCollaboration()
  const [expanded, setExpanded] = useState(true)
  const [editorMode, setEditorMode] = useState<EditorMode>('closed')
  const [draftKey, setDraftKey] = useState('custom-application-flow')
  const [draftTitle, setDraftTitle] = useState('Custom application flow')
  const [draftDescription, setDraftDescription] = useState('A composable typed workflow for this project.')
  const [definitionJSON, setDefinitionJSON] = useState('{}')
  const [editorError, setEditorError] = useState<string | null>(null)
  const [selectedVersionId, setSelectedVersionId] = useState('')

  const selectedVersion = flow.definitionVersions.find((item) => item.versionId === selectedVersionId)
    ?? flow.definitionVersions.find((item) => item.published)
    ?? flow.definitionVersions[0]
    ?? flow.selectedDefinition

  function openEditor(mode: Exclude<EditorMode, 'closed'>) {
    const source = selectedVersion?.definition
    setEditorMode(mode)
    setEditorError(null)
    if (mode === 'version' && source) {
      setDefinitionJSON(JSON.stringify({
        name: source.name,
        schemaVersion: source.schemaVersion,
        nodes: source.nodes,
        edges: source.edges,
      }, null, 2))
      return
    }
    setDefinitionJSON(JSON.stringify(starterDefinition(), null, 2))
  }

  async function saveDefinition() {
    setEditorError(null)
    try {
      const parsed = parseDefinitionJSON(definitionJSON)
      if (editorMode === 'create') {
        const input: CreateWorkflowDefinitionInputDto = {
          key: draftKey.trim(),
          title: draftTitle.trim(),
          description: draftDescription.trim(),
          ...parsed,
        }
        const created = await flow.createDefinition(input)
        if (created) {
          setSelectedVersionId(created.versionId)
          setEditorMode('closed')
        }
        return
      }
      if (editorMode === 'version' && flow.selectedDefinition) {
        const created = await flow.createDefinitionVersion(flow.selectedDefinition.id, parsed)
        if (created) {
          setSelectedVersionId(created.versionId)
          setEditorMode('closed')
        }
      }
    } catch (cause) {
      setEditorError(cause instanceof Error ? cause.message : 'Definition JSON is invalid.')
    }
  }

  const canStart = session.signedIn && Boolean(project) && can('edit') && flow.status === 'ready'

  return (
    <aside className={cn(
      'flex shrink-0 flex-col border-r border-border bg-panel transition-[width] max-lg:w-full max-lg:border-b max-lg:border-r-0',
      expanded ? 'w-[340px] max-lg:max-h-[520px]' : 'w-12 max-lg:max-h-12',
    )}>
      <div className="flex h-11 shrink-0 items-center gap-2 border-b border-border px-2.5">
        <Workflow className="size-4 text-primary-bright" />
        {expanded && (
          <>
            <div className="min-w-0 flex-1">
              <div className="truncate text-xs font-semibold text-foreground">Application workflow</div>
              <div className="truncate text-[9px] text-faint-foreground">conversation → artifacts → build manifest → app</div>
            </div>
            <button
              type="button"
              onClick={() => void flow.refresh()}
              disabled={flow.busy}
              className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground disabled:opacity-30"
              aria-label="Refresh workflows"
            >
              <RefreshCw className={cn('size-3.5', flow.busy && 'animate-spin')} />
            </button>
          </>
        )}
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="rounded p-1 text-faint-foreground hover:bg-white/5 hover:text-foreground"
          aria-label={expanded ? 'Collapse workflow panel' : 'Expand workflow panel'}
        >
          {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        </button>
      </div>

      {expanded && (
        <div className="min-h-0 flex-1 overflow-y-auto p-3 scrollbar-thin">
          {flow.error && (
            <div role="alert" className="mb-3 rounded-lg border border-destructive/30 bg-destructive/10 p-2.5 text-[10px] leading-relaxed text-destructive">
              <div className="flex gap-2">
                <CircleAlert className="mt-0.5 size-3.5 shrink-0" />
                <span className="min-w-0 flex-1">{flow.error}</span>
                <button type="button" onClick={flow.clearError} aria-label="Dismiss error"><X className="size-3" /></button>
              </div>
            </div>
          )}

          {!session.signedIn || !project ? (
            <EmptyState text="Sign in and select a server project before using workflows." />
          ) : flow.status === 'loading' ? (
            <EmptyState text="Loading workflow definitions from Go…" loading />
          ) : flow.status === 'error' ? (
            <EmptyState text="Workflow service is unavailable. Generation and shared edits are disabled." />
          ) : (
            <>
              <Section title="Definition" icon={GitBranch}>
                <select
                  value={flow.selectedDefinition?.id ?? ''}
                  onChange={(event) => void flow.selectDefinition(event.target.value)}
                  className="h-8 w-full rounded-md border border-border bg-background px-2 text-[11px] text-foreground outline-none"
                  aria-label="Workflow definition"
                >
                  {flow.definitions.length === 0 && <option value="">No server definition</option>}
                  {flow.definitions.map((definition) => (
                    <option key={definition.id} value={definition.id}>
                      {definition.title} · v{definition.version}{definition.published ? ' · published' : ''}
                    </option>
                  ))}
                </select>

                <div className="mt-2 flex gap-1.5">
                  <select
                    value={selectedVersion?.versionId ?? ''}
                    onChange={(event) => setSelectedVersionId(event.target.value)}
                    className="h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-[10px] text-foreground outline-none"
                    aria-label="Workflow version"
                  >
                    {flow.definitionVersions.map((version) => (
                      <option key={version.versionId} value={version.versionId}>
                        v{version.version} · {version.contentHash.slice(0, 12)}{version.published ? ' · published' : ''}
                      </option>
                    ))}
                  </select>
                  <button
                    type="button"
                    onClick={() => selectedVersion && void flow.publishDefinitionVersion(selectedVersion.id, selectedVersion.versionId)}
                    disabled={!selectedVersion || selectedVersion.published || !can('publish') || flow.busy}
                    className="inline-flex h-8 items-center gap-1 rounded-md border border-border px-2 text-[10px] text-muted-foreground hover:border-primary/40 hover:text-foreground disabled:opacity-35"
                    title="Publish immutable version"
                  >
                    <UploadCloud className="size-3" /> Publish
                  </button>
                </div>

                {can('admin') && (
                  <div className="mt-2 grid grid-cols-2 gap-1.5">
                    <button type="button" onClick={() => openEditor('create')} className="inline-flex h-8 items-center justify-center gap-1 rounded-md border border-border text-[10px] text-muted-foreground hover:border-primary/40 hover:text-foreground">
                      <PencilLine className="size-3" /> New definition
                    </button>
                    <button type="button" onClick={() => openEditor('version')} disabled={!selectedVersion} className="inline-flex h-8 items-center justify-center gap-1 rounded-md border border-border text-[10px] text-muted-foreground hover:border-primary/40 hover:text-foreground disabled:opacity-35">
                      <GitBranch className="size-3" /> New version
                    </button>
                  </div>
                )}

                <button
                  type="button"
                  onClick={() => void flow.startFromProjectBrief({
                    definitionVersionId: selectedVersion?.published ? selectedVersion.versionId : undefined,
                  })}
                  disabled={!canStart || flow.busy || (selectedVersion ? !selectedVersion.published : false)}
                  className="mt-2 inline-flex h-9 w-full items-center justify-center gap-1.5 rounded-md bg-primary px-3 text-[11px] font-semibold text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-40"
                  title="Freeze the exact approved Project Brief revision and start"
                >
                  <Play className="size-3.5" />
                  Start from approved Project Brief
                </button>
                <p className="mt-1.5 text-[9px] leading-relaxed text-faint-foreground">
                  The server freezes artifactId + revisionId + contentHash before the first node runs.
                </p>
              </Section>

              {flow.manifest && (
                <Section title="Frozen input" icon={ShieldCheck}>
                  <Fact label="Manifest" value={flow.manifest.id} mono />
                  <Fact label="Hash" value={flow.manifest.hash} mono />
                  <Fact label="Sources" value={String(flow.manifest.sources.length)} />
                </Section>
              )}

              {flow.runs.length > 0 && (
                <Section title="Server run history" icon={HistoryIcon}>
                  <div className="max-h-36 space-y-1 overflow-y-auto pr-1 scrollbar-thin">
                    {flow.runs.map((item) => (
                      <button
                        key={item.id}
                        type="button"
                        onClick={() => void flow.loadRun(item.id)}
                        className={cn(
                          'flex w-full items-center gap-2 rounded border px-2 py-1.5 text-left',
                          flow.run?.id === item.id
                            ? 'border-primary/35 bg-primary/10'
                            : 'border-border bg-background hover:border-white/20',
                        )}
                      >
                        <RunStatusIcon status={item.status} />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate font-mono text-[9px] text-foreground">{item.id}</span>
                          <span className="block text-[8px] text-faint-foreground">{item.status.replaceAll('_', ' ')} · {new Date(item.updatedAt).toLocaleString()}</span>
                        </span>
                      </button>
                    ))}
                  </div>
                </Section>
              )}

              {flow.run && (
                <Section title="Run" icon={Workflow}>
                  <div className="mb-2 flex items-center gap-2 rounded-md border border-border bg-background p-2">
                    <RunStatusIcon status={flow.run.status} />
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-mono text-[10px] text-foreground">{flow.run.id}</div>
                      <div className="mt-0.5 text-[9px] text-faint-foreground">
                        {flow.run.status.replaceAll('_', ' ')} · event {flow.run.eventCursor}
                      </div>
                    </div>
                    {!['completed', 'failed', 'cancelled', 'stale'].includes(flow.run.status) && (
                      <button type="button" onClick={() => void flow.cancelRun()} disabled={!can('edit') || flow.busy} className="rounded p-1 text-faint-foreground hover:text-destructive" aria-label="Cancel run">
                        <Square className="size-3" />
                      </button>
                    )}
                  </div>

                  <div className="space-y-1.5">
                    {[...flow.run.nodes]
                      .sort((left, right) => left.createdAt.localeCompare(right.createdAt) || left.key.localeCompare(right.key))
                      .map((node) => (
                        <RunNodeCard key={node.id} node={node} />
                      ))}
                  </div>
                </Section>
              )}

              {flow.events.length > 0 && (
                <Section title="Durable events" icon={RefreshCw}>
                  <div className="max-h-36 space-y-1 overflow-y-auto pr-1 scrollbar-thin">
                    {flow.events.slice(-20).toReversed().map((event) => (
                      <div key={event.id} className="rounded border border-border bg-background px-2 py-1.5">
                        <div className="flex gap-2 text-[9px]">
                          <span className="font-mono text-primary-bright">#{event.sequence}</span>
                          <span className="min-w-0 flex-1 truncate text-muted-foreground">{event.type}</span>
                          <span className="text-faint-foreground">{event.nodeKey}</span>
                        </div>
                      </div>
                    ))}
                  </div>
                </Section>
              )}
            </>
          )}
        </div>
      )}

      {editorMode !== 'closed' && (
        <DefinitionEditor
          mode={editorMode}
          definition={selectedVersion}
          draftKey={draftKey}
          draftTitle={draftTitle}
          draftDescription={draftDescription}
          definitionJSON={definitionJSON}
          error={editorError}
          busy={flow.busy}
          onKeyChange={setDraftKey}
          onTitleChange={setDraftTitle}
          onDescriptionChange={setDraftDescription}
          onJSONChange={setDefinitionJSON}
          onClose={() => setEditorMode('closed')}
          onSave={() => void saveDefinition()}
        />
      )}
    </aside>
  )
}

function RunNodeCard({ node }: { node: WorkflowNodeRunDto }) {
  const flow = usePlatformFlow()
  const artifacts = useArtifactWorkspace()
  const { can } = useCollaboration()
  const [revisionKey, setRevisionKey] = useState('')
  const [reason, setReason] = useState('')
  const definitionNode = flow.selectedDefinition?.definition.nodes.find(
    (item) => item.id === node.definitionNodeId,
  )
  const candidates = useMemo(
    () => revisionCandidates(definitionNode, artifacts),
    [artifacts, definitionNode],
  )
  const selected = candidates.find((candidate) => candidate.key === revisionKey) ?? candidates[0]
  const active = ['waiting_input', 'waiting_review', 'failed'].includes(node.status)

  return (
    <div className={cn(
      'rounded-md border p-2',
      active ? 'border-primary/35 bg-primary/5' : 'border-border bg-background',
    )}>
      <div className="flex items-start gap-2">
        <NodeStatusIcon status={node.status} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[10px] font-medium text-foreground">
            {definitionNode?.name ?? node.definitionNodeId}
          </div>
          <div className="mt-0.5 flex flex-wrap gap-x-2 text-[9px] text-faint-foreground">
            <span>{node.type}</span>
            <span>{node.status.replaceAll('_', ' ')}</span>
            {node.sliceId && <span>slice {node.sliceId.slice(0, 8)}</span>}
          </div>
        </div>
        {node.status === 'failed' && (
          <button type="button" onClick={() => void flow.retryNode(node, reason)} disabled={!can('edit') || flow.busy} className="rounded p-1 text-warning hover:bg-warning/10" aria-label="Retry node">
            <RotateCcw className="size-3" />
          </button>
        )}
      </div>

      {node.status === 'waiting_input' && node.type !== 'workbench_build' && (
        <div className="mt-2 border-t border-border pt-2">
          {candidates.length > 0 ? (
            <>
              <select
                value={selected?.key ?? ''}
                onChange={(event) => setRevisionKey(event.target.value)}
                className="h-7 w-full rounded border border-border bg-panel px-1.5 text-[9px] text-foreground outline-none"
                aria-label="Exact artifact revision"
              >
                {candidates.map((candidate) => (
                  <option key={candidate.key} value={candidate.key}>{candidate.label}</option>
                ))}
              </select>
              <button
                type="button"
                onClick={() => selected && void flow.submitNodeRevision(
                  node,
                  selected.ref,
                  definitionNode?.humanEdit?.artifactType === 'blueprint'
                    ? deliverySliceContext(selected.ref, artifacts)
                    : undefined,
                )}
                disabled={!selected || !can('edit') || flow.busy}
                className="mt-1.5 inline-flex h-7 w-full items-center justify-center gap-1 rounded bg-primary text-[9px] font-semibold text-primary-foreground disabled:opacity-40"
              >
                <Send className="size-3" /> Submit pinned revision
              </button>
            </>
          ) : (
            <p className="text-[9px] leading-relaxed text-warning">
              No immutable server revision matches this node. Create a revision in Documents, Blueprint, or Prototype first.
            </p>
          )}
        </div>
      )}

      {node.status === 'waiting_input' && node.type === 'workbench_build' && (
        <p className="mt-2 border-t border-border pt-2 text-[9px] leading-relaxed text-primary-bright">
          Review every generated file operation, then apply the proposal. The applied workspace revision will complete this node.
        </p>
      )}

      {node.status === 'waiting_review' && (
        <div className="mt-2 border-t border-border pt-2">
          <input
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            placeholder="Review reason / requested change"
            className="h-7 w-full rounded border border-border bg-panel px-2 text-[9px] text-foreground outline-none placeholder:text-faint-foreground"
          />
          <div className="mt-1.5 grid grid-cols-2 gap-1">
            <button type="button" onClick={() => void flow.resolveReview(node, 'approve', reason)} disabled={!can('publish') || flow.busy} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-success/15 text-[9px] font-medium text-success disabled:opacity-35">
              <Check className="size-3" /> Approve
            </button>
            <button type="button" onClick={() => void flow.resolveReview(node, 'changes_requested', reason || 'Changes requested in Workbench')} disabled={!can('edit') || flow.busy} className="inline-flex h-7 items-center justify-center gap-1 rounded bg-warning/15 text-[9px] font-medium text-warning disabled:opacity-35">
              <PencilLine className="size-3" /> Changes
            </button>
          </div>
          <p className="mt-1 text-[8px] leading-relaxed text-faint-foreground">
            Approval succeeds only after the canonical artifact review is approved and blocking comments are resolved.
          </p>
        </div>
      )}
    </div>
  )
}

function DefinitionEditor({
  mode,
  definition,
  draftKey,
  draftTitle,
  draftDescription,
  definitionJSON,
  error,
  busy,
  onKeyChange,
  onTitleChange,
  onDescriptionChange,
  onJSONChange,
  onClose,
  onSave,
}: {
  mode: Exclude<EditorMode, 'closed'>
  definition?: WorkflowDefinitionRecordDto | null
  draftKey: string
  draftTitle: string
  draftDescription: string
  definitionJSON: string
  error: string | null
  busy: boolean
  onKeyChange: (value: string) => void
  onTitleChange: (value: string) => void
  onDescriptionChange: (value: string) => void
  onJSONChange: (value: string) => void
  onClose: () => void
  onSave: () => void
}) {
  return (
    <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/70 p-4" role="dialog" aria-modal="true" aria-label="Workflow definition editor">
      <div className="flex max-h-[92vh] w-full max-w-4xl flex-col overflow-hidden rounded-xl border border-border bg-panel shadow-2xl">
        <header className="flex items-center gap-3 border-b border-border px-4 py-3">
          <GitBranch className="size-4 text-primary-bright" />
          <div className="min-w-0 flex-1">
            <h2 className="text-sm font-semibold text-foreground">
              {mode === 'create' ? 'Create workflow definition' : `Create v${(definition?.version ?? 0) + 1}`}
            </h2>
            <p className="text-[10px] text-faint-foreground">Typed DAG validation and publication happen on the Go service.</p>
          </div>
          <button type="button" onClick={onClose} className="rounded p-1.5 text-faint-foreground hover:bg-white/5 hover:text-foreground" aria-label="Close"><X className="size-4" /></button>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto p-4 scrollbar-thin">
          {mode === 'create' && (
            <div className="mb-3 grid grid-cols-2 gap-2 max-md:grid-cols-1">
              <label className="text-[10px] font-semibold uppercase tracking-wider text-faint-foreground [&_input]:mt-1 [&_input]:h-9 [&_input]:w-full [&_input]:rounded-md [&_input]:border [&_input]:border-border [&_input]:bg-background [&_input]:px-2 [&_input]:text-xs [&_input]:font-normal [&_input]:normal-case [&_input]:text-foreground [&_input]:outline-none">Key<input value={draftKey} onChange={(event) => onKeyChange(event.target.value)} pattern="[a-z][a-z0-9-]{2,63}" /></label>
              <label className="text-[10px] font-semibold uppercase tracking-wider text-faint-foreground [&_input]:mt-1 [&_input]:h-9 [&_input]:w-full [&_input]:rounded-md [&_input]:border [&_input]:border-border [&_input]:bg-background [&_input]:px-2 [&_input]:text-xs [&_input]:font-normal [&_input]:normal-case [&_input]:text-foreground [&_input]:outline-none">Title<input value={draftTitle} onChange={(event) => onTitleChange(event.target.value)} /></label>
              <label className="col-span-2 text-[10px] font-semibold uppercase tracking-wider text-faint-foreground max-md:col-span-1 [&_input]:mt-1 [&_input]:h-9 [&_input]:w-full [&_input]:rounded-md [&_input]:border [&_input]:border-border [&_input]:bg-background [&_input]:px-2 [&_input]:text-xs [&_input]:font-normal [&_input]:normal-case [&_input]:text-foreground [&_input]:outline-none">Description<input value={draftDescription} onChange={(event) => onDescriptionChange(event.target.value)} /></label>
            </div>
          )}
          <div className="text-[10px] font-semibold uppercase tracking-wider text-faint-foreground">
            Definition graph and contracts
            <div className="mt-2 normal-case tracking-normal">
              <WorkflowGraphEditor value={definitionJSON} onChange={onJSONChange} />
            </div>
          </div>
          {error && <p role="alert" className="mt-2 text-[10px] text-destructive">{error}</p>}
        </div>
        <footer className="flex items-center justify-end gap-2 border-t border-border px-4 py-3">
          <button type="button" onClick={onClose} className="rounded-md border border-border px-3 py-2 text-[11px] text-muted-foreground hover:text-foreground">Cancel</button>
          <button type="button" onClick={onSave} disabled={busy} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground disabled:opacity-40"><Save className="size-3.5" /> Save immutable draft version</button>
        </footer>
      </div>
    </div>
  )
}

function revisionCandidates(
  definitionNode: WorkflowNodeDefinitionDto | undefined,
  artifacts: ReturnType<typeof useArtifactWorkspace>,
) {
  const type = definitionNode?.humanEdit?.artifactType
  const resources = type === 'document'
    ? artifacts.documents
    : type === 'blueprint'
      ? [...artifacts.blueprints, ...artifacts.pageSpecs]
      : type === 'prototype'
        ? artifacts.prototypes
        : []
  return resources.flatMap((resource) => {
    const revision = resource.latestRevision ?? resource.approvedRevision
    if (!revision) return []
    return [{
      key: `${revision.artifactId}:${revision.id}`,
      label: `${resource.artifact.title} · r${revision.revisionNumber} · ${resource.artifact.status}`,
      ref: revisionRef(revision),
    }]
  })
}

function deliverySliceContext(
  blueprintRevision: ExactArtifactRefDto,
  artifacts: ReturnType<typeof useArtifactWorkspace>,
) {
  const slices = artifacts.pageSpecs.flatMap((pageSpec) => {
    const pageSpecRevision = pageSpec.latestRevision ?? pageSpec.approvedRevision
    if (!pageSpecRevision) return []
    const content = pageSpecRevision.content
    const matchingPrototype = artifacts.prototypes.find((prototype) => {
      const prototypeContent = prototype.latestRevision?.content ?? prototype.draft?.content
      return prototypeContent?.pageSpecRevision?.revisionId === pageSpecRevision.id
    })
    const prototypeRevision = matchingPrototype?.latestRevision ?? matchingPrototype?.approvedRevision
    return [{
      key: content.blueprintPageNodeId || pageSpec.artifact.id,
      title: content.title || pageSpec.artifact.title,
      blueprint: blueprintRevision,
      pageSpec: revisionRef(pageSpecRevision),
      ...(prototypeRevision ? { prototype: revisionRef(prototypeRevision) } : {}),
    }]
  })
  return { deliverySlices: slices }
}

function starterDefinition(): {
  name: string
  schemaVersion: string
  nodes: WorkflowNodeDefinitionDto[]
  edges: CreateWorkflowDefinitionInputDto['edges']
} {
  const envelope: JsonObject = {
    type: 'object',
    additionalProperties: true,
  }
  return {
    name: 'Custom application flow',
    schemaVersion: 'workflow/v2',
    nodes: [
      {
        id: 'input',
        name: 'Pinned project input',
        type: 'artifact_input',
        inputSchema: envelope,
        outputSchema: envelope,
        artifactInput: {
          allowedTypes: ['document'],
          requireApproved: true,
          minimumArtifacts: 1,
        },
      },
      {
        id: 'edit',
        name: 'Human refinement',
        type: 'human_edit',
        inputSchema: envelope,
        outputSchema: envelope,
        humanEdit: {
          artifactType: 'document',
          requiredRole: 'editor',
          instructions: 'Submit an exact immutable artifact revision.',
        },
      },
      {
        id: 'review',
        name: 'Canonical review gate',
        type: 'review_gate',
        inputSchema: envelope,
        outputSchema: envelope,
        reviewGate: {
          requiredRole: 'admin',
          minimumApprovals: 1,
          prohibitSelfReview: true,
          allowWaiver: false,
        },
      },
      {
        id: 'publish',
        name: 'Publish',
        type: 'publish',
        inputSchema: envelope,
        outputSchema: envelope,
        publish: {
          environment: 'preview',
          requiredRole: 'admin',
          allowRollback: true,
        },
      },
    ],
    edges: [
      { id: 'input-edit', from: 'input', to: 'edit' },
      { id: 'edit-review', from: 'edit', to: 'review' },
      { id: 'review-publish', from: 'review', to: 'publish' },
    ],
  }
}

function parseDefinitionJSON(value: string) {
  const parsed: unknown = JSON.parse(value)
  if (!record(parsed)) throw new Error('Definition must be a JSON object.')
  if (typeof parsed.name !== 'string' || !parsed.name.trim()) throw new Error('Definition name is required.')
  if (typeof parsed.schemaVersion !== 'string' || !parsed.schemaVersion.trim()) throw new Error('schemaVersion is required.')
  if (!Array.isArray(parsed.nodes) || !Array.isArray(parsed.edges)) throw new Error('nodes and edges must be arrays.')
  return {
    name: parsed.name,
    schemaVersion: parsed.schemaVersion,
    nodes: parsed.nodes as unknown as WorkflowNodeDefinitionDto[],
    edges: parsed.edges as unknown as CreateWorkflowDefinitionInputDto['edges'],
  }
}

function record(value: unknown): value is Record<string, JsonValue> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function Section({ title, icon: Icon, children }: { title: string; icon: typeof Workflow; children: React.ReactNode }) {
  return (
    <section className="mb-4">
      <h3 className="mb-2 flex items-center gap-1.5 text-[9px] font-semibold uppercase tracking-wider text-faint-foreground"><Icon className="size-3" />{title}</h3>
      {children}
    </section>
  )
}

function Fact({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return <div className="mb-1 flex gap-2 rounded border border-border bg-background px-2 py-1.5 text-[9px]"><span className="text-faint-foreground">{label}</span><span className={cn('min-w-0 flex-1 truncate text-right text-muted-foreground', mono && 'font-mono')} title={value}>{value}</span></div>
}

function EmptyState({ text, loading }: { text: string; loading?: boolean }) {
  return <div className="rounded-lg border border-dashed border-border bg-background p-4 text-center text-[10px] leading-relaxed text-faint-foreground">{loading && <CircleDashed className="mx-auto mb-2 size-4 animate-spin text-primary-bright" />}{text}</div>
}

function RunStatusIcon({ status }: { status: string }) {
  if (status === 'completed') return <CircleCheck className="size-4 text-success" />
  if (status === 'failed' || status === 'cancelled' || status === 'stale') return <CircleAlert className="size-4 text-destructive" />
  return <CircleDashed className="size-4 animate-spin text-primary-bright" />
}

function NodeStatusIcon({ status }: { status: WorkflowNodeRunDto['status'] }) {
  if (status === 'completed') return <CircleCheck className="mt-0.5 size-3.5 shrink-0 text-success" />
  if (status === 'failed' || status === 'cancelled' || status === 'stale') return <CircleAlert className="mt-0.5 size-3.5 shrink-0 text-destructive" />
  if (status === 'waiting_input' || status === 'waiting_review') return <CircleDashed className="mt-0.5 size-3.5 shrink-0 text-warning" />
  return <CircleDashed className="mt-0.5 size-3.5 shrink-0 text-faint-foreground" />
}
