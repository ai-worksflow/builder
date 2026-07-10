'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { collaborationErrorMessage } from '@/lib/collaboration/platform-adapter'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useI18n } from '@/lib/i18n'
import { useWorksflow } from '@/lib/worksflow/store'
import type {
  DependencyRelation,
  DocumentGraphDto,
  DocumentGraphEntityType,
  DocumentGraphNodeDto,
  VersionRefDto,
} from '@/lib/platform/dto'
import { cn } from '@/lib/utils'
import {
  ArrowRight,
  Bot,
  Boxes,
  FileText,
  GitFork,
  Link2,
  PanelsTopLeft,
  PencilLine,
  RefreshCw,
  Rocket,
  ShieldAlert,
  Workflow,
} from 'lucide-react'

const NODE_WIDTH = 224
const NODE_HEIGHT = 118
const COLUMN_WIDTH = 294
const ROW_HEIGHT = 154
const CANVAS_PADDING_X = 40
const CANVAS_PADDING_TOP = 84

const RELATIONS: readonly DependencyRelation[] = [
  'drives', 'satisfied_by', 'contains', 'navigates_to', 'uses', 'calls',
  'reads', 'writes', 'requires', 'realized_by', 'implemented_by',
  'verified_by', 'derives_from',
]

type NodeFilter = 'all' | 'artifacts' | 'ai' | 'workflow' | 'delivery'

interface GraphNode extends DocumentGraphNodeDto {
  readonly x: number
  readonly y: number
  readonly column: number
}

export function DocumentGraph() {
  const { t } = useI18n()
  const { setSelectedDocId, setTeamView } = useWorksflow()
  const { project, members, platformClient, session, can, authorize } = useCollaboration()
  const [graph, setGraph] = useState<DocumentGraphDto | null>(null)
  const [graphError, setGraphError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [nodeFilter, setNodeFilter] = useState<NodeFilter>('all')
  const [targetNodeId, setTargetNodeId] = useState('')
  const [relation, setRelation] = useState<DependencyRelation>('drives')
  const [required, setRequired] = useState(true)
  const [saving, setSaving] = useState(false)
  const [notice, setNotice] = useState<{ kind: 'success' | 'error'; message: string } | null>(null)
  const sequence = useRef(0)
  const realtimeReloadTimer = useRef<number | null>(null)
  const scrollFrameRef = useRef<HTMLDivElement | null>(null)

  const loadGraph = useCallback(async () => {
    const request = ++sequence.current
    if (!session.signedIn || !project) {
      setGraph(null)
      setGraphError(null)
      return
    }
    setLoading(true)
    setGraphError(null)
    try {
      const response = await platformClient.documents.graph(project.id)
      if (sequence.current === request) setGraph(response.data)
    } catch (error) {
      if (sequence.current === request) {
        setGraph(null)
        setGraphError(collaborationErrorMessage(error, 'Unable to load the server document graph.'))
      }
    } finally {
      if (sequence.current === request) setLoading(false)
    }
  }, [platformClient.documents, project, session.signedIn])

  useEffect(() => {
    void loadGraph()
    return () => { sequence.current += 1 }
  }, [loadGraph])

  useEffect(() => {
    if (!session.signedIn || !project) return
    const unsubscribe = platformClient.websocket.subscribeProject(project.id, (event) => {
      if (event.type === 'presence.updated') return
      if (realtimeReloadTimer.current !== null) window.clearTimeout(realtimeReloadTimer.current)
      realtimeReloadTimer.current = window.setTimeout(() => {
        realtimeReloadTimer.current = null
        void loadGraph()
      }, 120)
    })
    platformClient.websocket.connect()
    return () => {
      unsubscribe()
      if (realtimeReloadTimer.current !== null) {
        window.clearTimeout(realtimeReloadTimer.current)
        realtimeReloadTimer.current = null
      }
    }
  }, [loadGraph, platformClient.websocket, project, session.signedIn])

  const allNodes = useMemo(() => layoutNodes(graph?.nodes ?? []), [graph?.nodes])
  const filterNodeIds = useMemo(() => filteredNodeIds(allNodes, graph?.edges ?? [], nodeFilter), [allNodes, graph?.edges, nodeFilter])
  const nodes = allNodes.filter((node) => filterNodeIds.has(node.id))
  const nodeById = useMemo(() => new Map(nodes.map((node) => [node.id, node])), [nodes])
  const edges = (graph?.edges ?? []).filter((edge) => nodeById.has(edge.sourceId) && nodeById.has(edge.targetId))
  const selectedNode = nodes.find((node) => node.id === selectedNodeId) ?? nodes[0] ?? null
  const selectedEdges = selectedNode
    ? edges.filter((edge) => edge.sourceId === selectedNode.id || edge.targetId === selectedNode.id)
    : []
  const artifactNodes = nodes.filter((node) => node.id.startsWith('artifact:') && node.revision)
  const availableTargets = artifactNodes.filter((node) => node.id !== selectedNode?.id)

  useEffect(() => {
    if (!selectedNode) setSelectedNodeId(null)
    else if (selectedNode.id !== selectedNodeId) setSelectedNodeId(selectedNode.id)
  }, [selectedNode, selectedNodeId])

  useEffect(() => {
    if (targetNodeId && !nodeById.has(targetNodeId)) setTargetNodeId('')
  }, [nodeById, targetNodeId])

  const rowsByColumn = [0, 1, 2, 3].map((column) => nodes.filter((node) => node.column === column).length)
  const canvasWidth = CANVAS_PADDING_X * 2 + COLUMN_WIDTH * 3 + NODE_WIDTH
  const canvasHeight = Math.max(560, CANVAS_PADDING_TOP + Math.max(...rowsByColumn, 3) * ROW_HEIGHT + 40)

  const openArtifact = useCallback((node: GraphNode) => {
    if (!node.id.startsWith('artifact:')) return
    setArtifactReference(node.entityId)
    if (node.artifactKind === 'blueprint' || node.artifactKind === 'page_spec') {
      setTeamView('blueprint')
    } else if (node.artifactKind === 'prototype' || node.artifactKind === 'prototype_flow') {
      setTeamView('prototype')
    } else if (node.artifactKind !== 'workspace') {
      setSelectedDocId(node.entityId)
      setTeamView('editor')
    }
  }, [setSelectedDocId, setTeamView])

  async function createDependency() {
    const target = nodeById.get(targetNodeId)
    if (!project || !selectedNode?.revision || !target?.revision ||
      !selectedNode.id.startsWith('artifact:') || !target.id.startsWith('artifact:')) return
    setSaving(true)
    setNotice(null)
    try {
      if (!(await authorize('edit'))) return
      await platformClient.artifacts.createDependency(project.id, {
        source: exactVersionRef(selectedNode.revision),
        target: exactVersionRef(target.revision),
        relation,
        required,
      }, { idempotencyKey: true })
      await loadGraph()
      setTargetNodeId('')
      setNotice({ kind: 'success', message: 'Pinned dependency created on the server.' })
    } catch (error) {
      setNotice({ kind: 'error', message: collaborationErrorMessage(error, 'The dependency was not created.') })
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full max-lg:flex-col max-lg:overflow-y-auto" data-testid="server-document-graph">
      <div ref={scrollFrameRef} className="relative flex-1 overflow-auto bg-canvas scrollbar-thin max-lg:min-h-[560px] max-lg:flex-none">
        <div className="sticky top-0 z-20 flex items-center justify-between gap-3 border-b border-border bg-surface/90 px-5 py-3 backdrop-blur max-md:flex-wrap max-md:px-4">
          <div className="flex min-w-0 items-center gap-2"><Workflow className="size-4 shrink-0 text-primary-bright" /><span className="text-sm font-semibold text-foreground">{t('graph.title')}</span><span className="truncate text-xs text-faint-foreground">{project?.name ?? 'No server project selected'}</span><span className="shrink-0 rounded border border-border bg-surface-2 px-1.5 py-0.5 text-[9px] font-medium uppercase tracking-wide text-faint-foreground">Server projection · exact I/O</span></div>
          <div className="flex max-w-full items-center gap-2 overflow-x-auto scrollbar-thin">
            {([['all', 'All'], ['artifacts', 'Artifacts'], ['ai', 'AI input / output'], ['workflow', 'Workflow / build'], ['delivery', 'Implementation / deploy']] as const).map(([id, label]) => <button key={id} type="button" onClick={() => setNodeFilter(id)} className={cn('shrink-0 rounded-md px-2 py-1 text-[11px] font-medium', nodeFilter === id ? 'bg-primary/15 text-primary-bright' : 'text-faint-foreground hover:bg-white/5')}>{label}</button>)}
            <button type="button" onClick={() => scrollFrameRef.current?.scrollTo({ left: 0, top: 0, behavior: 'smooth' })} className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-[11px] text-muted-foreground"><PanelsTopLeft className="size-3" />{t('graph.fit')}</button>
            <button type="button" onClick={() => void loadGraph()} disabled={loading} className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-[11px] text-muted-foreground disabled:opacity-50"><RefreshCw className={cn('size-3', loading && 'animate-spin')} />Refresh</button>
          </div>
        </div>

        {loading && !graph ? <GraphMessage icon={<RefreshCw className="size-7 animate-spin text-primary-bright" />} title="Loading exact project graph" body="Fetching artifacts, frozen InputManifests, reviewable OutputProposals, workflow/build state, implementations and deployments." />
          : graphError ? <GraphMessage icon={<ShieldAlert className="size-7 text-destructive" />} title="The server graph could not be loaded" body={graphError} />
            : nodes.length === 0 ? <GraphMessage icon={<GitFork className="size-8 text-primary-bright" />} title={t('graph.emptyTitle')} body="No persisted nodes match this filter. This view never substitutes browser fixtures." />
              : <div className="relative" style={{ width: canvasWidth, height: canvasHeight, backgroundSize: '28px 28px' }}>
                <div className="absolute left-0 right-0 top-4 grid grid-cols-4 gap-[70px] px-10 text-[10px] font-semibold uppercase tracking-[0.16em] text-faint-foreground"><ColumnHeading icon={<FileText className="size-3" />} label="Artifacts" count={rowsByColumn[0]} /><ColumnHeading icon={<Bot className="size-3" />} label="Frozen input" count={rowsByColumn[1]} /><ColumnHeading icon={<Boxes className="size-3" />} label="Review output" count={rowsByColumn[2]} /><ColumnHeading icon={<Rocket className="size-3" />} label="Execution / delivery" count={rowsByColumn[3]} /></div>
                <svg className="absolute inset-0" width={canvasWidth} height={canvasHeight} aria-hidden="true"><defs><marker id="graph-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#60a5fa" /></marker></defs>{edges.map((edge) => { const source = nodeById.get(edge.sourceId); const target = nodeById.get(edge.targetId); if (!source || !target) return null; const sourceX = source.x + NODE_WIDTH; const sourceY = source.y + NODE_HEIGHT / 2; const targetX = target.x; const targetY = target.y + NODE_HEIGHT / 2; const direction = Math.sign(targetX - sourceX) || 1; const bend = Math.max(54, Math.abs(targetX - sourceX) * .38); return <path key={edge.id} d={`M ${sourceX} ${sourceY} C ${sourceX + bend * direction} ${sourceY}, ${targetX - bend * direction} ${targetY}, ${targetX} ${targetY}`} fill="none" stroke={edgeColor(edge.relation, edge.required)} strokeWidth={selectedNode && (edge.sourceId === selectedNode.id || edge.targetId === selectedNode.id) ? 2.2 : 1.25} strokeDasharray={edge.id.startsWith('trace:') ? '3 5' : undefined} markerEnd="url(#graph-arrow)" opacity={selectedNode && edge.sourceId !== selectedNode.id && edge.targetId !== selectedNode.id ? .18 : .9} /> })}</svg>
                {nodes.map((node) => <button key={node.id} type="button" data-testid={`document-graph-node-${node.entityType}`} onClick={() => { setSelectedNodeId(node.id); setNotice(null) }} onDoubleClick={() => openArtifact(node)} className={cn('absolute rounded-lg border bg-surface-2 p-3 text-left shadow-sm outline-none', selectedNode?.id === node.id ? 'border-primary shadow-[0_0_0_1px_var(--color-primary)]' : 'border-border hover:border-white/25')} style={{ left: node.x, top: node.y, width: NODE_WIDTH, height: NODE_HEIGHT }}><div className="flex items-center justify-between gap-2"><span className="flex min-w-0 items-center gap-1.5 text-[10px] font-medium uppercase tracking-wide text-faint-foreground"><span className={cn('size-1.5 shrink-0 rounded-full', statusDot(node.status))} /><span className="truncate">{kindLabel(node.entityType)}</span></span><span className="max-w-24 truncate rounded border border-border px-1 py-.5 text-[9px] text-faint-foreground">{node.status}</span></div><div className="mt-1 truncate text-sm font-semibold text-foreground">{node.title}</div><div className="mt-1 line-clamp-2 min-h-7 text-[10px] leading-3.5 text-muted-foreground">{nodeSummary(node)}</div><div className="mt-1.5 flex items-center justify-between gap-2 text-[9px] text-faint-foreground"><code className="truncate">{node.revision?.contentHash?.slice(0, 15) ?? node.entityId.slice(0, 12)}</code>{Boolean(node.memberBindings?.length) && <span className="shrink-0">{node.memberBindings?.length} bindings</span>}</div></button>)}
              </div>}
      </div>

      <aside className="flex w-80 shrink-0 flex-col border-l border-border bg-surface max-lg:max-h-[560px] max-lg:w-full max-lg:border-l-0 max-lg:border-t">
        {selectedNode ? <><div className="border-b border-border p-4"><div className="flex items-center justify-between gap-2"><span className="text-[10px] font-semibold uppercase tracking-wider text-faint-foreground">{kindLabel(selectedNode.entityType)}</span><StatusBadge status={selectedNode.status} /></div><h3 className="mt-1 text-base font-semibold text-foreground">{selectedNode.title}</h3><p className="mt-2 text-xs leading-relaxed text-muted-foreground">{nodeSummary(selectedNode)}</p><dl className="mt-3 grid grid-cols-[84px_1fr] gap-x-2 gap-y-1 text-[10px]"><dt className="text-faint-foreground">Entity</dt><dd className="truncate font-mono text-muted-foreground">{selectedNode.entityId}</dd><dt className="text-faint-foreground">Revision</dt><dd className="truncate font-mono text-muted-foreground">{selectedNode.revision?.revisionId ?? 'Not an artifact revision'}</dd></dl>{selectedNode.id.startsWith('artifact:') && selectedNode.artifactKind !== 'workspace' && <button type="button" onClick={() => openArtifact(selectedNode)} className="mt-3 inline-flex h-8 w-full items-center justify-center gap-1.5 rounded-md border border-primary/35 bg-primary/10 px-2 text-[10px] font-semibold text-primary-bright"><PencilLine className="size-3.5" />Open artifact workspace</button>}{notice && <div className={cn('mt-3 rounded-md border px-2.5 py-2 text-[11px]', notice.kind === 'success' ? 'border-success/30 bg-success/10 text-success' : 'border-destructive/30 bg-destructive/10 text-destructive')}>{notice.message}</div>}</div>
          <div className="flex-1 overflow-y-auto p-4 scrollbar-thin">
            {Boolean(selectedNode.memberBindings?.length) && <><SectionLabel>Member bindings</SectionLabel><div className="mt-2 space-y-1.5">{selectedNode.memberBindings?.map((binding) => <div key={`${binding.userId}:${binding.role}`} className="rounded border border-border bg-surface-2 p-2 text-[10px]"><span className="font-medium text-foreground">{members.find((member) => member.user.id === binding.userId)?.user.name ?? binding.userId}</span><span className="float-right text-primary-bright">{relationLabel(binding.role)}</span>{binding.reason && <p className="mt-1 text-faint-foreground">{binding.reason}</p>}</div>)}</div></>}
            {selectedNode.id.startsWith('artifact:') && selectedNode.revision && <><SectionLabel className="mt-5">Create pinned dependency</SectionLabel><div className="mt-2 space-y-2 rounded-lg border border-border bg-surface-2 p-3"><select value={targetNodeId} onChange={(event) => setTargetNodeId(event.target.value)} className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs text-foreground"><option value="">Choose target artifact</option>{availableTargets.map((node) => <option key={node.id} value={node.id}>{node.title}</option>)}</select><select value={relation} onChange={(event) => setRelation(event.target.value as DependencyRelation)} className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs text-foreground">{RELATIONS.map((item) => <option key={item} value={item}>{relationLabel(item)}</option>)}</select><label className="flex items-center gap-2 text-[11px] text-muted-foreground"><input type="checkbox" checked={required} onChange={(event) => setRequired(event.target.checked)} />Required delivery dependency</label><button type="button" onClick={() => void createDependency()} disabled={!can('edit') || !targetNodeId || saving} className="w-full rounded-md bg-primary px-2.5 py-2 text-xs font-semibold text-primary-foreground disabled:opacity-40">{saving ? 'Creating…' : 'Create dependency'}</button></div></>}
            <SectionLabel className="mt-5">Exact relationships</SectionLabel><div className="mt-2 space-y-1.5">{selectedEdges.map((edge) => { const outgoing = edge.sourceId === selectedNode.id; const other = nodeById.get(outgoing ? edge.targetId : edge.sourceId); if (!other) return null; return <button key={edge.id} type="button" onClick={() => setSelectedNodeId(other.id)} className="flex w-full items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2 text-left"><Link2 className="size-3.5 shrink-0 text-primary-bright" /><span className="min-w-0 flex-1"><span className="block truncate text-[10px] text-faint-foreground">{outgoing ? '' : '← '}{relationLabel(edge.relation)}</span><span className="flex items-center gap-1 truncate text-xs font-medium text-foreground">{outgoing && <ArrowRight className="size-3" />}{other.title}</span></span>{edge.required && <span className="rounded bg-destructive/10 px-1 py-.5 text-[9px] text-destructive">required</span>}</button>})}{selectedEdges.length === 0 && <p className="text-xs text-faint-foreground">No exact relationships.</p>}</div>
            <SectionLabel className="mt-5">Server metadata</SectionLabel><pre className="mt-2 max-h-48 overflow-auto rounded border border-border bg-background p-2 text-[9px] text-faint-foreground">{JSON.stringify(selectedNode.metadata, null, 2)}</pre>
          </div></> : <div className="flex flex-1 items-center justify-center p-6 text-center text-sm text-faint-foreground">{t('graph.emptySelection')}</div>}
      </aside>
    </div>
  )
}

function layoutNodes(nodes: readonly DocumentGraphNodeDto[]) {
  const rows = [0, 0, 0, 0]
  return nodes.map<GraphNode>((node) => {
    const column = nodeColumn(node.entityType)
    const row = rows[column] ?? 0
    rows[column] = row + 1
    return { ...node, column, x: CANVAS_PADDING_X + column * COLUMN_WIDTH, y: CANVAS_PADDING_TOP + row * ROW_HEIGHT }
  })
}

function filteredNodeIds(nodes: readonly GraphNode[], edges: DocumentGraphDto['edges'], filter: NodeFilter) {
  if (filter === 'all') return new Set(nodes.map((node) => node.id))
  const direct = new Set(nodes.filter((node) => nodeGroup(node.entityType) === filter).map((node) => node.id))
  if (filter === 'ai') edges.forEach((edge) => { if (direct.has(edge.sourceId) || direct.has(edge.targetId)) { direct.add(edge.sourceId); direct.add(edge.targetId) } })
  return direct
}

function nodeGroup(type: DocumentGraphEntityType): Exclude<NodeFilter, 'all'> {
  if (type === 'inputManifest' || type === 'outputProposal') return 'ai'
  if (type === 'workflowRun' || type === 'workbenchVersion') return 'workflow'
  if (type === 'implementation' || type === 'deployment' || type === 'workspace') return 'delivery'
  return 'artifacts'
}

function nodeColumn(type: DocumentGraphEntityType) {
  if (type === 'inputManifest') return 1
  if (type === 'outputProposal') return 2
  if (nodeGroup(type) === 'artifacts') return 0
  return 3
}

function nodeSummary(node: DocumentGraphNodeDto) {
  if (node.entityType === 'inputManifest') return `${String(node.metadata.jobType ?? 'AI input')} · ${String(node.metadata.sourceCount ?? 0)} frozen sources`
  if (node.entityType === 'outputProposal') return `${String(node.metadata.operationCount ?? 0)} operations · ${String(node.metadata.pendingCount ?? 0)} pending decisions`
  if (node.entityType === 'deployment') return `${String(node.metadata.environment ?? '')} · ${String(node.metadata.previewUrl ?? 'no preview URL')}`
  if (node.entityType === 'workbenchVersion') return `Manifest ${String(node.metadata.manifestHash ?? '').slice(0, 16)}`
  if (node.entityType === 'implementation') return `${String(node.metadata.operationCount ?? 0)} implementation operations`
  return node.memberBindings?.length ? `${node.memberBindings.length} member bindings` : `${node.artifactKind ?? node.entityType} · exact server state`
}

function kindLabel(kind: DocumentGraphEntityType) {
  const labels: Record<DocumentGraphEntityType, string> = {
    document: 'Document', feature: 'Feature / Blueprint', page: 'Page / Prototype', api: 'API', data: 'Data', workspace: 'Workspace',
    inputManifest: 'InputManifest', outputProposal: 'OutputProposal', workflowRun: 'Workflow run', workbenchVersion: 'Workbench / build',
    implementation: 'Implementation', deployment: 'Deployment',
  }
  return labels[kind]
}

function exactVersionRef(reference: VersionRefDto): VersionRefDto {
  return { artifactId: reference.artifactId, revisionId: reference.revisionId, contentHash: reference.contentHash, ...(reference.anchorId ? { anchorId: reference.anchorId } : {}) }
}

function relationLabel(value: string) { return value.replaceAll('_', ' ').replace(/([a-z])([A-Z])/g, '$1 $2') }
function statusDot(status: string) { if (status === 'approved' || status === 'ready' || status === 'applied') return 'bg-success'; if (status === 'open' || status === 'reviewing' || status === 'running' || status === 'frozen') return 'bg-primary-bright'; if (status.includes('stale') || status.includes('blocked') || status.includes('invalid')) return 'bg-warning'; return 'bg-faint-foreground' }
function edgeColor(relation: string, required: boolean) { if (relation === 'generated_output' || relation === 'proposes_patch' || relation === 'frozen_input' || relation === 'proposal_base') return '#60a5fa'; if (relation === 'deployed_as') return '#a78bfa'; return required ? '#ef4444' : '#4ade80' }

function StatusBadge({ status }: { status: string }) { return <span className="rounded border border-border bg-white/5 px-1.5 py-.5 text-[9px] font-medium text-muted-foreground">{relationLabel(status)}</span> }
function ColumnHeading({ icon, label, count }: { icon: React.ReactNode; label: string; count: number }) { return <div className="flex w-[224px] items-center gap-1.5 border-b border-border pb-2">{icon}<span>{label}</span><span className="ml-auto rounded bg-white/5 px-1.5 py-.5 text-[9px]">{count}</span></div> }
function SectionLabel({ children, className }: { children: React.ReactNode; className?: string }) { return <div className={cn('text-[11px] font-semibold uppercase tracking-wider text-faint-foreground', className)}>{children}</div> }
function GraphMessage({ icon, title, body }: { icon: React.ReactNode; title: string; body: string }) { return <div className="flex min-h-[520px] items-center justify-center p-6"><div className="max-w-lg rounded-lg border border-dashed border-border bg-panel p-5 text-center"><div className="flex justify-center">{icon}</div><h2 className="mt-3 text-base font-semibold text-foreground">{title}</h2><p className="mt-2 text-sm leading-relaxed text-muted-foreground">{body}</p></div></div> }

function setArtifactReference(artifactId: string) {
  if (typeof window === 'undefined') return
  const url = new URL(window.location.href)
  url.searchParams.set('artifactId', artifactId)
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}
