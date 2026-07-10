'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { collaborationErrorMessage } from '@/lib/collaboration/platform-adapter'
import { useCollaboration } from '@/lib/collaboration/provider'
import { useI18n } from '@/lib/i18n'
import { useArtifactWorkspace } from '@/lib/platform/artifact-provider'
import { useWorksflow } from '@/lib/worksflow/store'
import type {
  ArtifactDependencyDto,
  ArtifactStatus,
  DependencyRelation,
  VersionRefDto,
  VersionedArtifactDto,
} from '@/lib/platform/dto'
import { cn } from '@/lib/utils'
import {
  ArrowRight,
  Boxes,
  FileText,
  GitFork,
  Layers,
  Link2,
  MonitorPlay,
  PanelsTopLeft,
  PencilLine,
  RefreshCw,
  ShieldAlert,
  Workflow,
} from 'lucide-react'

const NODE_WIDTH = 220
const NODE_HEIGHT = 112
const COLUMN_WIDTH = 286
const ROW_HEIGHT = 148
const CANVAS_PADDING_X = 40
const CANVAS_PADDING_TOP = 84

const RELATIONS: readonly DependencyRelation[] = [
  'drives',
  'satisfied_by',
  'contains',
  'navigates_to',
  'uses',
  'calls',
  'reads',
  'writes',
  'requires',
  'realized_by',
  'implemented_by',
  'verified_by',
  'derives_from',
]

type GraphNodeKind = 'document' | 'blueprint' | 'pageSpec' | 'prototype'
type EdgeFilter = 'all' | 'required' | 'dependencies' | 'traces'

interface GraphNode {
  readonly id: string
  readonly title: string
  readonly kind: GraphNodeKind
  readonly status: ArtifactStatus
  readonly summary: string
  readonly updatedAt: string
  readonly createdBy: string
  readonly hasDraft: boolean
  readonly revisionNumber?: number
  readonly revision?: VersionRefDto
  readonly x: number
  readonly y: number
}

interface GraphEdge {
  readonly id: string
  readonly sourceId: string
  readonly targetId: string
  readonly relation: string
  readonly required: boolean
  readonly kind: 'dependency' | 'trace'
}

export function DocumentGraph() {
  const { t } = useI18n()
  const { setSelectedDocId, setTeamView } = useWorksflow()
  const workspace = useArtifactWorkspace()
  const {
    project,
    members,
    platformClient,
    session,
    can,
    authorize,
  } = useCollaboration()
  const [dependencies, setDependencies] = useState<readonly ArtifactDependencyDto[]>([])
  const [dependencyError, setDependencyError] = useState<string | null>(null)
  const [loadingDependencies, setLoadingDependencies] = useState(false)
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [targetNodeId, setTargetNodeId] = useState('')
  const [relation, setRelation] = useState<DependencyRelation>('drives')
  const [required, setRequired] = useState(true)
  const [edgeFilter, setEdgeFilter] = useState<EdgeFilter>('all')
  const [hoveredEdgeId, setHoveredEdgeId] = useState<string | null>(null)
  const [notice, setNotice] = useState<{ kind: 'success' | 'error'; message: string } | null>(null)
  const [saving, setSaving] = useState(false)
  const requestSequence = useRef(0)
  const scrollFrameRef = useRef<HTMLDivElement | null>(null)

  const nodes = useMemo(() => {
    const collections: Array<{
      kind: GraphNodeKind
      resources: readonly VersionedArtifactDto<unknown>[]
      summaries: readonly string[]
    }> = [
      {
        kind: 'document',
        resources: workspace.documents,
        summaries: workspace.documents.map((item) =>
          item.draft?.content.summary ?? item.latestRevision?.content.summary ?? '',
        ),
      },
      {
        kind: 'blueprint',
        resources: workspace.blueprints,
        summaries: workspace.blueprints.map((item) => {
          const content = item.draft?.content ?? item.latestRevision?.content
          return content
            ? `${content.semantic?.nodes.length ?? content.nodes.length} semantic nodes`
            : 'No Blueprint content yet'
        }),
      },
      {
        kind: 'pageSpec',
        resources: workspace.pageSpecs,
        summaries: workspace.pageSpecs.map((item) => {
          const content = item.draft?.content ?? item.latestRevision?.content
          return content ? `${content.route} · ${content.userGoal || 'No user goal'}` : 'No PageSpec content yet'
        }),
      },
      {
        kind: 'prototype',
        resources: workspace.prototypes,
        summaries: workspace.prototypes.map((item) => {
          const content = item.draft?.content ?? item.latestRevision?.content
          return content
            ? `${content.frames.length} frames · ${content.states.length} states`
            : 'No prototype content yet'
        }),
      },
    ]

    return collections.flatMap((collection, column) =>
      collection.resources.map((resource, row) =>
        graphNode(resource, collection.kind, collection.summaries[row] ?? '', column, row),
      ),
    )
  }, [workspace.blueprints, workspace.documents, workspace.pageSpecs, workspace.prototypes])

  const artifactIds = useMemo(() => nodes.map((node) => node.id), [nodes])
  const artifactKey = artifactIds.join(':')

  const loadDependencies = useCallback(async () => {
    const sequence = ++requestSequence.current
    if (!session.signedIn || !project || artifactIds.length === 0) {
      setDependencies([])
      setDependencyError(null)
      setLoadingDependencies(false)
      return
    }
    setLoadingDependencies(true)
    setDependencyError(null)
    try {
      const pages = await Promise.all(
        artifactIds.map((artifactId) =>
          platformClient.artifacts.listDependencies(artifactId, { limit: 500 }),
        ),
      )
      if (sequence !== requestSequence.current) return
      const unique = new Map<string, ArtifactDependencyDto>()
      pages.forEach((page) => page.data.items.forEach((item) => unique.set(item.id, item)))
      setDependencies([...unique.values()])
    } catch (cause) {
      if (sequence !== requestSequence.current) return
      setDependencies([])
      setDependencyError(collaborationErrorMessage(cause, 'Unable to load artifact dependencies.'))
    } finally {
      if (sequence === requestSequence.current) setLoadingDependencies(false)
    }
  }, [artifactKey, platformClient.artifacts, project, session.signedIn])

  useEffect(() => {
    void loadDependencies()
    return () => {
      requestSequence.current += 1
    }
  }, [loadDependencies])

  useEffect(() => {
    if (nodes.length === 0) {
      setSelectedNodeId(null)
      return
    }
    if (!selectedNodeId || !nodes.some((node) => node.id === selectedNodeId)) {
      setSelectedNodeId(nodes[0].id)
    }
  }, [nodes, selectedNodeId])

  useEffect(() => {
    if (targetNodeId && !nodes.some((node) => node.id === targetNodeId)) {
      setTargetNodeId('')
    }
  }, [nodes, targetNodeId])

  const nodeById = useMemo(
    () => new Map(nodes.map((node) => [node.id, node])),
    [nodes],
  )
  const dependencyEdges = useMemo<readonly GraphEdge[]>(() =>
    dependencies.flatMap((dependency) =>
      nodeById.has(dependency.source.artifactId) && nodeById.has(dependency.target.artifactId)
        ? [{
            id: dependency.id,
            sourceId: dependency.source.artifactId,
            targetId: dependency.target.artifactId,
            relation: dependency.relation,
            required: dependency.required,
            kind: 'dependency' as const,
          }]
        : [],
    ), [dependencies, nodeById])
  const traceEdges = useMemo<readonly GraphEdge[]>(() =>
    workspace.traces.flatMap((trace) =>
      nodeById.has(trace.source.artifactId) && nodeById.has(trace.target.artifactId)
        ? [{
            id: `trace:${trace.id}`,
            sourceId: trace.source.artifactId,
            targetId: trace.target.artifactId,
            relation: trace.relation,
            required: false,
            kind: 'trace' as const,
          }]
        : [],
    ), [nodeById, workspace.traces])
  const allEdges = useMemo(
    () => [...dependencyEdges, ...traceEdges],
    [dependencyEdges, traceEdges],
  )
  const visibleEdges = allEdges.filter((edge) => {
    if (edgeFilter === 'required') return edge.kind === 'dependency' && edge.required
    if (edgeFilter === 'dependencies') return edge.kind === 'dependency'
    if (edgeFilter === 'traces') return edge.kind === 'trace'
    return true
  })

  const selectedNode = nodes.find((node) => node.id === selectedNodeId) ?? null
  const selectedEdges = allEdges.filter(
    (edge) => edge.sourceId === selectedNodeId || edge.targetId === selectedNodeId,
  )
  const availableTargets = nodes.filter((node) => node.id !== selectedNodeId)
  const canCreateDependency = Boolean(
    project &&
    session.signedIn &&
    can('edit') &&
    selectedNode?.revision &&
    targetNodeId &&
    nodeById.get(targetNodeId)?.revision &&
    !saving,
  )

  const openArtifactWorkspace = useCallback((node: GraphNode) => {
    setArtifactReference(node.id)
    switch (node.kind) {
      case 'document':
        setSelectedDocId(node.id)
        setTeamView('editor')
        break
      case 'blueprint':
      case 'pageSpec':
        setTeamView('blueprint')
        break
      case 'prototype':
        setTeamView('prototype')
        break
    }
  }, [setSelectedDocId, setTeamView])
  const maxRows = Math.max(
    workspace.documents.length,
    workspace.blueprints.length,
    workspace.pageSpecs.length,
    workspace.prototypes.length,
    3,
  )
  const canvasWidth = CANVAS_PADDING_X * 2 + COLUMN_WIDTH * 3 + NODE_WIDTH
  const canvasHeight = Math.max(520, CANVAS_PADDING_TOP + maxRows * ROW_HEIGHT + 40)

  async function createDependency() {
    const targetNode = nodeById.get(targetNodeId)
    if (!project || !selectedNode?.revision || !targetNode?.revision) {
      setNotice({
        kind: 'error',
        message: 'Both artifacts need an immutable revision before a dependency can be created.',
      })
      return
    }
    setSaving(true)
    setNotice(null)
    try {
      if (!(await authorize('edit'))) {
        setNotice({ kind: 'error', message: 'Your current project role cannot create dependencies.' })
        return
      }
      await platformClient.artifacts.createDependency(project.id, {
        source: exactVersionRef(selectedNode.revision),
        target: exactVersionRef(targetNode.revision),
        relation,
        required,
      }, { idempotencyKey: true })
      await loadDependencies()
      setTargetNodeId('')
      setNotice({
        kind: 'success',
        message: `${selectedNode.title} ${relationLabel(relation)} ${targetNode.title}`,
      })
    } catch (cause) {
      setNotice({
        kind: 'error',
        message: collaborationErrorMessage(cause, 'The dependency was not created.'),
      })
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full max-lg:flex-col max-lg:overflow-y-auto">
      <div
        ref={scrollFrameRef}
        className="relative flex-1 overflow-auto bg-canvas scrollbar-thin max-lg:min-h-[560px] max-lg:flex-none"
      >
        <div className="sticky top-0 z-20 flex items-center justify-between gap-3 border-b border-border bg-surface/90 px-5 py-3 backdrop-blur max-md:flex-wrap max-md:px-4">
          <div className="flex min-w-0 items-center gap-2">
            <Workflow className="size-4 shrink-0 text-primary-bright" />
            <span className="text-sm font-semibold text-foreground">{t('graph.title')}</span>
            <span className="truncate text-xs text-faint-foreground">
              {project?.name ?? 'No server project selected'}
            </span>
            <span className="shrink-0 rounded border border-border bg-surface-2 px-1.5 py-0.5 text-[9px] font-medium uppercase tracking-wide text-faint-foreground">
              Auto layout · view only
            </span>
          </div>
          <div className="flex max-w-full items-center gap-2 overflow-x-auto scrollbar-thin">
            {([
              ['all', t('graph.filter.all')],
              ['required', 'Required'],
              ['dependencies', 'Dependencies'],
              ['traces', 'Trace links'],
            ] as const).map(([id, label]) => (
              <button
                key={id}
                type="button"
                onClick={() => setEdgeFilter(id)}
                className={cn(
                  'shrink-0 rounded-md px-2 py-1 text-[11px] font-medium transition-colors',
                  edgeFilter === id
                    ? 'bg-primary/15 text-primary-bright'
                    : 'text-faint-foreground hover:bg-white/5 hover:text-foreground',
                )}
              >
                {label}
              </button>
            ))}
            <button
              type="button"
              onClick={() => scrollFrameRef.current?.scrollTo({ left: 0, top: 0, behavior: 'smooth' })}
              className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-[11px] text-muted-foreground hover:bg-white/5"
            >
              <PanelsTopLeft className="size-3" /> {t('graph.fit')}
            </button>
            <button
              type="button"
              onClick={() => void Promise.all([workspace.refresh(), loadDependencies()])}
              disabled={workspace.status === 'loading' || loadingDependencies}
              className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-[11px] text-muted-foreground hover:bg-white/5 disabled:cursor-wait disabled:opacity-50"
            >
              <RefreshCw className={cn('size-3', (workspace.status === 'loading' || loadingDependencies) && 'animate-spin')} />
              Refresh
            </button>
          </div>
        </div>

        {workspace.status === 'loading' && nodes.length === 0 ? (
          <GraphMessage icon={<RefreshCw className="size-7 animate-spin text-primary-bright" />} title="Loading project graph" body="Fetching server artifacts, immutable revisions, dependencies, and trace links." />
        ) : workspace.error ? (
          <GraphMessage icon={<ShieldAlert className="size-7 text-destructive" />} title="The server graph could not be loaded" body={workspace.error} />
        ) : nodes.length === 0 ? (
          <GraphMessage icon={<GitFork className="size-8 text-primary-bright" />} title={t('graph.emptyTitle')} body="Create a document, Blueprint, PageSpec, or prototype in its editor. This graph only shows artifacts persisted by the active project service." />
        ) : (
          <div
            className="relative"
            style={{ width: canvasWidth, height: canvasHeight, backgroundSize: '28px 28px' }}
          >
            <div className="absolute left-0 right-0 top-4 grid grid-cols-4 gap-[66px] px-10 text-[10px] font-semibold uppercase tracking-[0.16em] text-faint-foreground">
              <ColumnHeading icon={<FileText className="size-3" />} label="Documents" count={workspace.documents.length} />
              <ColumnHeading icon={<Boxes className="size-3" />} label="Blueprints" count={workspace.blueprints.length} />
              <ColumnHeading icon={<Layers className="size-3" />} label="PageSpecs" count={workspace.pageSpecs.length} />
              <ColumnHeading icon={<MonitorPlay className="size-3" />} label="Prototypes" count={workspace.prototypes.length} />
            </div>

            <svg className="absolute inset-0" width={canvasWidth} height={canvasHeight} aria-hidden="true">
              <defs>
                <marker id="dependency-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
                  <path d="M0,0 L8,4 L0,8 z" fill="#4ade80" />
                </marker>
                <marker id="required-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
                  <path d="M0,0 L8,4 L0,8 z" fill="#ef4444" />
                </marker>
                <marker id="trace-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
                  <path d="M0,0 L8,4 L0,8 z" fill="#a78bfa" />
                </marker>
              </defs>
              {visibleEdges.map((edge) => {
                const source = nodeById.get(edge.sourceId)
                const target = nodeById.get(edge.targetId)
                if (!source || !target) return null
                const active = !selectedNodeId || edge.sourceId === selectedNodeId || edge.targetId === selectedNodeId
                const color = edge.kind === 'trace' ? '#a78bfa' : edge.required ? '#ef4444' : '#4ade80'
                const sourceX = source.x + NODE_WIDTH
                const sourceY = source.y + NODE_HEIGHT / 2
                const targetX = target.x
                const targetY = target.y + NODE_HEIGHT / 2
                const direction = Math.sign(targetX - sourceX) || 1
                const bend = Math.max(58, Math.abs(targetX - sourceX) * 0.42)
                const path = `M ${sourceX} ${sourceY} C ${sourceX + bend * direction} ${sourceY}, ${targetX - bend * direction} ${targetY}, ${targetX} ${targetY}`
                return (
                  <g key={edge.id} opacity={active ? 1 : 0.18}>
                    <path
                      d={path}
                      fill="none"
                      stroke={color}
                      strokeWidth={hoveredEdgeId === edge.id ? 2.5 : 1.5}
                      strokeDasharray={edge.kind === 'trace' ? '3 5' : edge.required ? '7 4' : undefined}
                      markerEnd={edge.kind === 'trace' ? 'url(#trace-arrow)' : edge.required ? 'url(#required-arrow)' : 'url(#dependency-arrow)'}
                      pointerEvents="stroke"
                      className="cursor-pointer"
                      onMouseEnter={() => setHoveredEdgeId(edge.id)}
                      onMouseLeave={() => setHoveredEdgeId(null)}
                      onClick={() => setSelectedNodeId(edge.targetId)}
                    />
                  </g>
                )
              })}
            </svg>

            {nodes.map((node) => {
              const selected = node.id === selectedNodeId
              const owner = members.find((member) => member.user.id === node.createdBy)?.user
              return (
                <button
                  key={node.id}
                  type="button"
                  onClick={() => {
                    setSelectedNodeId(node.id)
                    setNotice(null)
                  }}
                  onDoubleClick={() => openArtifactWorkspace(node)}
                  className={cn(
                    'absolute rounded-lg border bg-surface-2 p-3 text-left shadow-sm transition-colors outline-none',
                    selected
                      ? 'border-primary shadow-[0_0_0_1px_var(--color-primary),0_8px_30px_rgba(20,136,252,0.18)]'
                      : 'border-border hover:border-white/25',
                  )}
                  style={{ left: node.x, top: node.y, width: NODE_WIDTH, height: NODE_HEIGHT }}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="flex min-w-0 items-center gap-1.5 text-[10px] font-medium uppercase tracking-wide text-faint-foreground">
                      <span className={cn('size-1.5 shrink-0 rounded-full', statusDot(node.status))} />
                      <span className="truncate">{kindLabel(node.kind)}</span>
                    </span>
                    <span className="shrink-0 rounded border border-border px-1 py-0.5 text-[9px] text-faint-foreground">
                      {node.revisionNumber ? `r${node.revisionNumber}` : 'draft only'}
                    </span>
                  </div>
                  <div className="mt-1 truncate text-sm font-semibold text-foreground">{node.title}</div>
                  <div className="mt-1 line-clamp-2 min-h-7 text-[10px] leading-3.5 text-muted-foreground">
                    {node.summary || 'No summary'}
                  </div>
                  <div className="mt-1.5 flex items-center justify-between gap-2 text-[9px] text-faint-foreground">
                    <span className="truncate">{owner?.name ?? node.createdBy}</span>
                    {node.hasDraft && <span className="shrink-0 text-warning">unversioned changes</span>}
                  </div>
                </button>
              )
            })}
          </div>
        )}
      </div>

      <aside className="flex w-80 shrink-0 flex-col border-l border-border bg-surface max-lg:max-h-[520px] max-lg:w-full max-lg:border-l-0 max-lg:border-t">
        {selectedNode ? (
          <>
            <div className="border-b border-border p-4">
              <div className="flex items-center justify-between gap-2">
                <span className="text-[10px] font-semibold uppercase tracking-wider text-faint-foreground">
                  {kindLabel(selectedNode.kind)}
                </span>
                <StatusBadge status={selectedNode.status} />
              </div>
              <h3 className="mt-1 text-base font-semibold text-foreground">{selectedNode.title}</h3>
              <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
                {selectedNode.summary || 'No summary has been persisted for this artifact.'}
              </p>
              <dl className="mt-3 grid grid-cols-[92px_1fr] gap-x-2 gap-y-1 text-[10px]">
                <dt className="text-faint-foreground">Artifact</dt>
                <dd className="truncate font-mono text-muted-foreground">{selectedNode.id}</dd>
                <dt className="text-faint-foreground">Pinned revision</dt>
                <dd className="truncate font-mono text-muted-foreground">
                  {selectedNode.revision
                    ? `${selectedNode.revision.revisionId}${selectedNode.revisionNumber ? ` · r${selectedNode.revisionNumber}` : ''}`
                    : 'None — create a revision first'}
                </dd>
              </dl>
              <button
                type="button"
                onClick={() => openArtifactWorkspace(selectedNode)}
                className="mt-3 inline-flex h-8 w-full items-center justify-center gap-1.5 rounded-md border border-primary/35 bg-primary/10 px-2 text-[10px] font-semibold text-primary-bright hover:bg-primary/15"
              >
                <PencilLine className="size-3.5" />
                {selectedNode.kind === 'document'
                  ? 'Open document editor'
                  : selectedNode.kind === 'prototype'
                    ? 'Open Prototype Studio'
                    : selectedNode.kind === 'pageSpec'
                      ? 'Open PageSpecs in Blueprint'
                      : 'Open Blueprint editor'}
              </button>
              {notice && (
                <div className={cn(
                  'mt-3 rounded-md border px-2.5 py-2 text-[11px]',
                  notice.kind === 'success'
                    ? 'border-success/30 bg-success/10 text-success'
                    : 'border-destructive/30 bg-destructive/10 text-destructive',
                )}>
                  {notice.message}
                </div>
              )}
              {dependencyError && (
                <div className="mt-3 rounded-md border border-destructive/30 bg-destructive/10 px-2.5 py-2 text-[11px] text-destructive">
                  {dependencyError}
                </div>
              )}
            </div>

            <div className="flex-1 overflow-y-auto p-4 scrollbar-thin">
              <SectionLabel>{t('graph.addBinding')}</SectionLabel>
              <div className="mt-2 space-y-2 rounded-lg border border-border bg-surface-2 p-3">
                <p className="text-[10px] leading-relaxed text-faint-foreground">
                  The server pins both sides to their latest immutable revision. Draft-only artifacts cannot be linked.
                </p>
                <label className="block text-[11px] text-faint-foreground">
                  {t('graph.bindingTarget')}
                  <select
                    value={targetNodeId}
                    onChange={(event) => setTargetNodeId(event.target.value)}
                    className="mt-1 w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs text-foreground outline-none focus:border-primary/60"
                  >
                    <option value="">{t('graph.chooseTarget')}</option>
                    {availableTargets.map((node) => (
                      <option key={node.id} value={node.id} disabled={!node.revision}>
                        {kindLabel(node.kind)} · {node.title}{node.revision ? '' : ' (draft only)'}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="block text-[11px] text-faint-foreground">
                  {t('graph.relationType')}
                  <select
                    value={relation}
                    onChange={(event) => setRelation(event.target.value as DependencyRelation)}
                    className="mt-1 w-full rounded-md border border-border bg-background px-2 py-1.5 text-xs text-foreground outline-none focus:border-primary/60"
                  >
                    {RELATIONS.map((item) => (
                      <option key={item} value={item}>{relationLabel(item)}</option>
                    ))}
                  </select>
                </label>
                <label className="flex items-center gap-2 text-[11px] text-muted-foreground">
                  <input
                    type="checkbox"
                    checked={required}
                    onChange={(event) => setRequired(event.target.checked)}
                  />
                  Required delivery dependency
                </label>
                <button
                  type="button"
                  onClick={() => void createDependency()}
                  disabled={!canCreateDependency}
                  className="w-full rounded-md bg-primary px-2.5 py-2 text-xs font-semibold text-primary-foreground hover:bg-primary-bright disabled:cursor-not-allowed disabled:bg-white/10 disabled:text-faint-foreground"
                >
                  {saving ? 'Creating server dependency…' : 'Create pinned dependency'}
                </button>
                {!selectedNode.revision && (
                  <p className="flex items-start gap-1.5 text-[10px] leading-relaxed text-warning">
                    <ShieldAlert className="mt-0.5 size-3 shrink-0" />
                    Create an immutable revision for this artifact before connecting it.
                  </p>
                )}
                {session.signedIn && !can('edit') && (
                  <p className="text-[10px] text-warning">Your project role is read-only.</p>
                )}
              </div>

              <SectionLabel className="mt-5">{t('graph.dependencies')}</SectionLabel>
              <div className="mt-2 space-y-1.5">
                {selectedEdges.map((edge) => {
                  const outgoing = edge.sourceId === selectedNode.id
                  const other = nodeById.get(outgoing ? edge.targetId : edge.sourceId)
                  if (!other) return null
                  return (
                    <button
                      key={edge.id}
                      type="button"
                      onClick={() => setSelectedNodeId(other.id)}
                      onMouseEnter={() => setHoveredEdgeId(edge.id)}
                      onMouseLeave={() => setHoveredEdgeId(null)}
                      className="flex w-full items-center gap-2 rounded-lg border border-border bg-surface-2 px-2.5 py-2 text-left hover:border-white/20"
                    >
                      {edge.kind === 'trace'
                        ? <Link2 className="size-3.5 shrink-0 text-violet-400" />
                        : <GitFork className={cn('size-3.5 shrink-0', edge.required ? 'text-destructive' : 'text-success')} />}
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-[10px] text-faint-foreground">
                          {outgoing ? '' : '← '}{relationLabel(edge.relation)} · {edge.kind}
                        </span>
                        <span className="flex items-center gap-1 truncate text-xs font-medium text-foreground">
                          {outgoing && <ArrowRight className="size-3 shrink-0 text-faint-foreground" />}
                          {other.title}
                        </span>
                      </span>
                      {edge.required && (
                        <span className="rounded bg-destructive/10 px-1 py-0.5 text-[9px] text-destructive">required</span>
                      )}
                    </button>
                  )
                })}
                {selectedEdges.length === 0 && (
                  <p className="text-xs text-faint-foreground">{t('graph.noDependencies')}</p>
                )}
              </div>

              <div className="mt-5 rounded-lg border border-border bg-background p-3 text-[10px] leading-relaxed text-faint-foreground">
                Node positions are an automatic local view and are not saved. Dependency and trace edges are server records; this screen never reports success until the API accepts a relation.
              </div>
            </div>
          </>
        ) : (
          <div className="flex flex-1 items-center justify-center p-6 text-center text-sm text-faint-foreground">
            {t('graph.emptySelection')}
          </div>
        )}
      </aside>
    </div>
  )
}

function setArtifactReference(artifactId: string) {
  if (typeof window === 'undefined') return
  const url = new URL(window.location.href)
  url.searchParams.set('artifactId', artifactId)
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}

function graphNode<TContent>(
  resource: VersionedArtifactDto<TContent>,
  kind: GraphNodeKind,
  summary: string,
  column: number,
  row: number,
): GraphNode {
  const revision = resource.latestRevision
    ? {
        artifactId: resource.latestRevision.artifactId,
        revisionId: resource.latestRevision.id,
        revisionNumber: resource.latestRevision.revisionNumber,
        contentHash: resource.latestRevision.contentHash,
      }
    : undefined
  return {
    id: resource.artifact.id,
    title: resource.artifact.title,
    kind,
    status: resource.artifact.status,
    summary,
    updatedAt: resource.artifact.updatedAt,
    createdBy: resource.artifact.createdBy,
    hasDraft: Boolean(resource.draft),
    revisionNumber: resource.latestRevision?.revisionNumber,
    revision,
    x: CANVAS_PADDING_X + column * COLUMN_WIDTH,
    y: CANVAS_PADDING_TOP + row * ROW_HEIGHT,
  }
}

function exactVersionRef(reference: VersionRefDto): VersionRefDto {
  return {
    artifactId: reference.artifactId,
    revisionId: reference.revisionId,
    contentHash: reference.contentHash,
    ...(reference.anchorId ? { anchorId: reference.anchorId } : {}),
  }
}

function kindLabel(kind: GraphNodeKind) {
  const labels: Record<GraphNodeKind, string> = {
    document: 'Document',
    blueprint: 'Blueprint',
    pageSpec: 'PageSpec',
    prototype: 'Prototype',
  }
  return labels[kind]
}

function relationLabel(relation: string) {
  return relation.replaceAll('_', ' ')
}

function statusDot(status: ArtifactStatus) {
  if (status === 'approved') return 'bg-success'
  if (status === 'inReview') return 'bg-primary-bright'
  if (status === 'changesRequested' || status === 'needsSync') return 'bg-warning'
  return 'bg-faint-foreground'
}

function StatusBadge({ status }: { status: ArtifactStatus }) {
  return (
    <span className={cn(
      'rounded border px-1.5 py-0.5 text-[9px] font-medium',
      status === 'approved'
        ? 'border-success/30 bg-success/10 text-success'
        : status === 'changesRequested' || status === 'needsSync'
          ? 'border-warning/30 bg-warning/10 text-warning'
          : status === 'inReview'
            ? 'border-primary/30 bg-primary/10 text-primary-bright'
            : 'border-border bg-white/5 text-muted-foreground',
    )}>
      {relationLabel(status)}
    </span>
  )
}

function ColumnHeading({ icon, label, count }: { icon: React.ReactNode; label: string; count: number }) {
  return (
    <div className="flex w-[220px] items-center gap-1.5 border-b border-border pb-2">
      {icon}
      <span>{label}</span>
      <span className="ml-auto rounded bg-white/5 px-1.5 py-0.5 text-[9px]">{count}</span>
    </div>
  )
}

function SectionLabel({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <div className={cn('text-[11px] font-semibold uppercase tracking-wider text-faint-foreground', className)}>
      {children}
    </div>
  )
}

function GraphMessage({
  icon,
  title,
  body,
}: {
  icon: React.ReactNode
  title: string
  body: string
}) {
  return (
    <div className="flex min-h-[520px] items-center justify-center p-6">
      <div className="max-w-lg rounded-lg border border-dashed border-border bg-panel p-5 text-center">
        <div className="flex justify-center">{icon}</div>
        <h2 className="mt-3 text-base font-semibold text-foreground">{title}</h2>
        <p className="mt-2 text-sm leading-relaxed text-muted-foreground">{body}</p>
      </div>
    </div>
  )
}
