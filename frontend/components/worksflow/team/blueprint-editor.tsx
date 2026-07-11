'use client'

import { useEffect, useMemo, useRef, useState } from 'react'
import { useCollaboration } from '@/lib/collaboration/provider'
import {
  ArtifactWorkspaceConflictError,
  reviewGateReadyForRequest,
  type ArtifactDetails,
} from '@/lib/platform/artifact-workspace'
import {
  createEmptyBlueprintContent,
  createEmptyDocumentContent,
  useArtifactWorkspace,
} from '@/lib/platform/artifact-provider'
import { createEmptyPrototypeContent } from '@/lib/platform/artifact-workspace'
import {
  BLUEPRINT_MODULE_TEMPLATES,
  groupBlueprintNodes,
  insertBlueprintModule,
  readBlueprintSelectionScope,
  type BlueprintModuleTemplate,
} from '@/lib/platform/blueprint-selection'
import {
  blueprintGate,
  emptyBlueprintLayout,
  materializeBlueprintContent,
  normalizeBlueprintContent,
  type SemanticBlueprintNode,
} from '@/lib/platform/blueprint-content'
import type {
  ArtifactReviewGateDto,
  ArtifactRevisionDto,
  BlueprintContentDto,
  BlueprintEdgeDto,
  BlueprintEdgeKind,
  BlueprintLayoutDto,
  BlueprintNodeKind,
  ImpactReportDto,
  PageSpecContentDto,
  ProposalDto,
  VersionRefDto,
} from '@/lib/platform/dto'
import { usePlatformFlow } from '@/lib/platform/flow-provider'
import { cn } from '@/lib/utils'
import { useWorksflow } from '@/lib/worksflow/store'
import { PageSpecEditor } from './page-spec-editor'
import {
  AlertTriangle,
  Bot,
  Boxes,
  CheckCircle2,
  FileClock,
  FilePlus2,
  GitBranch,
  GitFork,
  Group,
  Link2,
  Loader2,
  MessageSquare,
  Move,
  Plus,
  Play,
  RefreshCw,
  Save,
  Send,
  Trash2,
  Workflow,
} from 'lucide-react'

type EditorTab = 'canvas' | 'pages' | 'versions' | 'proposal' | 'impact' | 'trace' | 'review'
type SemanticNode = SemanticBlueprintNode

const NODE_KINDS: readonly BlueprintNodeKind[] = [
  'feature',
  'page',
  'component',
  'apiOperation',
  'dataEntity',
  'permission',
]

const EDGE_KINDS: readonly BlueprintEdgeKind[] = [
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
]

const HTTP_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'] as const

const NODE_COLORS: Record<BlueprintNodeKind, string> = {
  feature: 'border-violet-400/50 bg-violet-400/10',
  page: 'border-sky-400/50 bg-sky-400/10',
  component: 'border-cyan-400/50 bg-cyan-400/10',
  apiOperation: 'border-amber-400/50 bg-amber-400/10',
  dataEntity: 'border-emerald-400/50 bg-emerald-400/10',
  permission: 'border-rose-400/50 bg-rose-400/10',
  api: 'border-amber-400/50 bg-amber-400/10',
  dataModel: 'border-emerald-400/50 bg-emerald-400/10',
  workbenchTarget: 'border-indigo-400/50 bg-indigo-400/10',
}

const CANVAS_WIDTH = 980
const CANVAS_HEIGHT = 560
const NODE_WIDTH = 160
const NODE_HEIGHT = 58
const EMPTY_SEMANTIC_NODES: readonly SemanticNode[] = []
const EMPTY_SEMANTIC_EDGES: readonly BlueprintEdgeDto[] = []
const EMPTY_BLUEPRINT_LAYOUT = emptyBlueprintLayout()

export function BlueprintEditor() {
  const workspace = useArtifactWorkspace()
  const collaboration = useCollaboration()
  const flow = usePlatformFlow()
  const { selectedBlueprintNodeId, setSelectedBlueprintNodeId, setSurface } = useWorksflow()
  const [selectedBlueprintId, setSelectedBlueprintId] = useState('')
  const [selectedPageSpecId, setSelectedPageSpecId] = useState('')
  const [tab, setTab] = useState<EditorTab>('canvas')
  const [content, setContent] = useState<BlueprintContentDto | null>(null)
  const [details, setDetails] = useState<ArtifactDetails<BlueprintContentDto> | null>(null)
  const [impact, setImpact] = useState<ImpactReportDto | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedAt, setSavedAt] = useState<string | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)
  const [conflict, setConflict] = useState(false)
  const [edgeSourceId, setEdgeSourceId] = useState('')
  const [edgeTargetId, setEdgeTargetId] = useState('')
  const [edgeKind, setEdgeKind] = useState<BlueprintEdgeKind>('contains')
  const [proposalInstruction, setProposalInstruction] = useState('Decompose approved requirements into pages, components and implementation boundaries.')
  const [selectedOperations, setSelectedOperations] = useState<Record<string, string[]>>({})
  const [comment, setComment] = useState('')
  const [reviewSummary, setReviewSummary] = useState('Blueprint is ready for version-level review.')
  const [reviewerId, setReviewerId] = useState('')
  const [selectedNodeIds, setSelectedNodeIds] = useState<string[]>([])
  const [selectionMessage, setSelectionMessage] = useState<string | null>(null)
  const canvasRef = useRef<HTMLDivElement | null>(null)
  const dragRef = useRef<{ id: string; pointerId: number; offsetX: number; offsetY: number } | null>(null)

  const resource = workspace.blueprints.find((item) => item.artifact.id === selectedBlueprintId)
    ?? workspace.blueprints[0]
  const serverContent = resource?.draft?.content ?? resource?.latestRevision?.content
  const serverEtag = resource?.draft?.etag ?? resource?.artifact.etag
  const normalized = useMemo(() => content ? normalizeBlueprintContent(content) : null, [content])
  const nodes = normalized?.semantic?.nodes ?? EMPTY_SEMANTIC_NODES
  const edges = normalized?.semantic?.edges ?? EMPTY_SEMANTIC_EDGES
  const layout = normalized?.layout ?? EMPTY_BLUEPRINT_LAYOUT
  const selectedNode = nodes.find((node) => node.id === selectedBlueprintNodeId) ?? null
  const dirty = Boolean(content && serverContent && JSON.stringify(content) !== JSON.stringify(normalizeBlueprintContent(serverContent)))
  const proposals = workspace.proposals.filter((proposal) => proposal.artifactId === resource?.artifact.id)
  const pageSpecs = workspace.pageSpecs.filter((pageSpec) =>
    nodes.some((node) => node.id === (pageSpec.draft?.content ?? pageSpec.latestRevision?.content)?.blueprintPageNodeId),
  )
  const latestVersion = resource?.latestRevision ? versionRef(resource.latestRevision) : undefined
  const comments = collaboration.comments.filter((thread) => thread.target?.artifactId === resource?.artifact.id)
  const reviews = collaboration.reviews.filter((review) => review.target?.artifactId === resource?.artifact.id)
  const currentUserId = collaboration.session.signedIn ? collaboration.session.user.id : null
  const clientGate = normalized ? blueprintGate(normalized) : []
  const revisionReady = clientGate.length === 0
  const gatePassed = revisionReady && reviewGateReadyForRequest(details?.reviewGate)

  useEffect(() => {
    const artifactId = artifactReference()
    if (!artifactId) return
    const blueprint = workspace.blueprints.find((item) => item.artifact.id === artifactId)
    if (blueprint) {
      setSelectedBlueprintId(blueprint.artifact.id)
      return
    }
    const pageSpec = workspace.pageSpecs.find((item) => item.artifact.id === artifactId)
    if (!pageSpec) return
    const pageNodeId = (pageSpec.draft?.content ?? pageSpec.latestRevision?.content)?.blueprintPageNodeId
    const owner = workspace.blueprints.find((item) => {
      const value = normalizeBlueprintContent(item.draft?.content ?? item.latestRevision?.content ?? createEmptyBlueprintContent())
      return value.semantic?.nodes.some((node) => node.id === pageNodeId)
    })
    if (owner) setSelectedBlueprintId(owner.artifact.id)
    setSelectedPageSpecId(pageSpec.artifact.id)
    setTab('pages')
  }, [workspace.blueprints, workspace.pageSpecs])

  useEffect(() => {
    if (!resource) {
      setContent(null)
      return
    }
    if (selectedBlueprintId !== resource.artifact.id) setSelectedBlueprintId(resource.artifact.id)
    if (!conflict) setContent(normalizeBlueprintContent(serverContent ?? createEmptyBlueprintContent()))
  }, [conflict, resource, selectedBlueprintId, serverContent])

  useEffect(() => {
    const selectedExists = nodes.some((node) => node.id === selectedBlueprintNodeId)
    const preferred = selectedExists ? selectedBlueprintNodeId : nodes[0]?.id ?? null
    if (!selectedExists && preferred !== selectedBlueprintNodeId) setSelectedBlueprintNodeId(preferred)
    setSelectedNodeIds((current) => {
      const retained = current.filter((nodeId) => nodes.some((node) => node.id === nodeId))
      if (retained.length > 0) return retained.length === current.length ? current : retained
      const next = preferred ? [preferred] : []
      return next.length === current.length && next.every((nodeId, index) => current[index] === nodeId)
        ? current
        : next
    })
  }, [nodes, selectedBlueprintNodeId, setSelectedBlueprintNodeId])

  useEffect(() => {
    if (!resource) {
      setDetails(null)
      return
    }
    let active = true
    void workspace.loadDetails<BlueprintContentDto>(resource.artifact.id)
      .then((next) => { if (active) setDetails(next) })
      .catch((error) => { if (active) setLocalError(errorMessage(error)) })
    return () => { active = false }
  }, [resource?.artifact.id, workspace.loadDetails])

  useEffect(() => {
    if (!resource || !content || !serverEtag || !dirty || conflict || !collaboration.can('edit')) return
    const timer = window.setTimeout(() => {
      setSaving(true)
      setLocalError(null)
      void workspace.saveBlueprintDraft(resource.artifact.id, normalizeBlueprintContent(content), serverEtag)
        .then(() => {
          setSavedAt(new Date().toLocaleTimeString())
          setConflict(false)
        })
        .catch((error) => {
          if (error instanceof ArtifactWorkspaceConflictError) setConflict(true)
          setLocalError(errorMessage(error))
        })
        .finally(() => setSaving(false))
    }, 700)
    return () => window.clearTimeout(timer)
  }, [collaboration, conflict, content, dirty, resource, serverEtag, workspace.saveBlueprintDraft])

  if (!collaboration.session.signedIn) {
    return <Unavailable title="Sign in to open platform blueprints" detail="Browser blueprint fixtures are not used as a fallback." />
  }
  if (workspace.status === 'loading') {
    return <Unavailable loading title="Loading platform blueprints" detail="Fetching semantic graph, layout, PageSpecs and pinned trace data." />
  }
  if (workspace.status === 'error') {
    return <Unavailable title="Platform blueprints unavailable" detail={workspace.error ?? 'The backend did not return blueprint artifacts.'} onRetry={workspace.refresh} />
  }
  if (!resource || !normalized) {
    return <Unavailable title="No blueprint artifacts" detail="Create a blueprint from approved requirement revisions." action="Create blueprint" onAction={() => void workspace.createBlueprint('Product blueprint')} />
  }

  const readOnly = !collaboration.can('edit')

  function mutateBlueprint(
    update: (
      semanticNodes: readonly SemanticNode[],
      semanticEdges: readonly BlueprintEdgeDto[],
      currentLayout: BlueprintLayoutDto,
    ) => {
      nodes?: readonly SemanticNode[]
      edges?: readonly BlueprintEdgeDto[]
      layout?: BlueprintLayoutDto
    },
  ) {
    setContent((current) => {
      if (!current) return current
      const currentNormalized = normalizeBlueprintContent(current)
      const currentNodes = currentNormalized.semantic?.nodes ?? []
      const currentEdges = currentNormalized.semantic?.edges ?? []
      const currentLayout = currentNormalized.layout ?? emptyBlueprintLayout()
      const next = update(currentNodes, currentEdges, currentLayout)
      return materializeBlueprintContent(
        currentNormalized,
        next.nodes ?? currentNodes,
        next.edges ?? currentEdges,
        next.layout ?? currentLayout,
      )
    })
    setConflict(false)
  }

  function addNode(kind: BlueprintNodeKind = 'feature') {
    const id = stableId('node')
    const key = `${kind.replace(/([a-z])([A-Z])/g, '$1_$2').toUpperCase()}-${id.slice(-8).toUpperCase()}`
    mutateBlueprint((currentNodes, currentEdges, currentLayout) => ({
      nodes: [...currentNodes, {
        id,
        key,
        kind,
        title: `New ${kind}`,
        description: '',
        ...(kind === 'page' ? { route: `/${slug(key)}`, userGoal: '' } : {}),
        ...(kind === 'apiOperation' ? { method: 'GET', path: '' } : {}),
        ...(kind === 'permission' ? { roles: [] } : {}),
        requirementIds: [],
        assignedMemberIds: [],
      }],
      edges: currentEdges,
      layout: {
        ...currentLayout,
        nodePositions: {
          ...currentLayout.nodePositions,
          [id]: { x: 48 + (currentNodes.length % 4) * 210, y: 48 + Math.floor(currentNodes.length / 4) * 110 },
        },
      },
    }))
    setSelectedBlueprintNodeId(id)
    setSelectedNodeIds([id])
  }

  function addModule(
    template: BlueprintModuleTemplate,
    origin = { x: 48, y: 48 + Math.floor(nodes.length / 4) * 110 },
  ) {
    if (!content) return
    const inserted = insertBlueprintModule(content, template, origin, stableId)
    setContent(inserted.content)
    setSelectedNodeIds(inserted.nodeIds)
    setSelectedBlueprintNodeId(inserted.nodeIds[0] ?? null)
    setConflict(false)
  }

  function groupSelection() {
    if (!content || selectedNodeIds.length === 0) return
    const title = selectedNodeIds.length === 1
      ? nodes.find((node) => node.id === selectedNodeIds[0])?.title ?? 'Capability'
      : `Capability ${layout.groups.length + 1}`
    setContent(groupBlueprintNodes(content, selectedNodeIds, title, stableId('group')))
    setConflict(false)
  }

  function selectCapabilityGroup(group: BlueprintLayoutDto['groups'][number]) {
    const nodeIDs = group.nodeIds.filter((nodeId) => nodes.some((node) => node.id === nodeId))
    setSelectedNodeIds(nodeIDs)
    setSelectedBlueprintNodeId(nodeIDs[0] ?? null)
  }

  function renameCapabilityGroup(groupId: string, title: string) {
    mutateBlueprint((_nodes, _edges, currentLayout) => ({
      layout: {
        ...currentLayout,
        groups: currentLayout.groups.map((group) => group.id === groupId ? { ...group, title } : group),
      },
    }))
  }

  function ungroupCapability(groupId: string) {
    mutateBlueprint((_nodes, _edges, currentLayout) => ({
      layout: { ...currentLayout, groups: currentLayout.groups.filter((group) => group.id !== groupId) },
    }))
  }

  function selectNode(nodeId: string, additive: boolean) {
    setSelectedNodeIds((current) => {
      if (!additive) return [nodeId]
      return current.includes(nodeId)
        ? current.filter((item) => item !== nodeId)
        : [...current, nodeId]
    })
    setSelectedBlueprintNodeId(nodeId)
  }

  function updateNode(nodeId: string, patch: Partial<SemanticNode>) {
    mutateBlueprint((currentNodes) => ({
      nodes: currentNodes.map((node) => node.id === nodeId ? { ...node, ...patch } : node),
    }))
  }

  function deleteNode(nodeId: string) {
    mutateBlueprint((currentNodes, currentEdges, currentLayout) => {
      const positions = { ...currentLayout.nodePositions }
      delete positions[nodeId]
      return {
        nodes: currentNodes.filter((node) => node.id !== nodeId),
        edges: currentEdges.filter((edge) => edge.sourceNodeId !== nodeId && edge.targetNodeId !== nodeId),
        layout: {
          ...currentLayout,
          nodePositions: positions,
          groups: currentLayout.groups
            .map((group) => ({ ...group, nodeIds: group.nodeIds.filter((item) => item !== nodeId) }))
            .filter((group) => group.nodeIds.length > 0),
        },
      }
    })
    setSelectedNodeIds((current) => current.filter((item) => item !== nodeId))
  }

  function moveNode(nodeId: string, x: number, y: number) {
    mutateBlueprint((_currentNodes, _currentEdges, currentLayout) => ({
      layout: {
        ...currentLayout,
        nodePositions: {
          ...currentLayout.nodePositions,
          [nodeId]: {
            x: Math.max(0, Math.min(CANVAS_WIDTH - NODE_WIDTH, Math.round(x))),
            y: Math.max(0, Math.min(CANVAS_HEIGHT - NODE_HEIGHT, Math.round(y))),
          },
        },
      },
    }))
  }

  function addEdge() {
    if (!edgeSourceId || !edgeTargetId || edgeSourceId === edgeTargetId) return
    mutateBlueprint((_currentNodes, currentEdges) => ({
      edges: [...currentEdges, {
        id: stableId('edge'),
        sourceNodeId: edgeSourceId,
        targetNodeId: edgeTargetId,
        kind: edgeKind,
        required: true,
      }],
    }))
  }

  async function createRevision() {
    if (!content || !revisionReady) return
    setSaving(true)
    setLocalError(null)
    try {
      await workspace.createBlueprintRevision(resource!.artifact.id, normalizeBlueprintContent(content))
      setDetails(await workspace.loadDetails<BlueprintContentDto>(resource!.artifact.id))
    } catch (error) {
      setLocalError(errorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  async function reloadServerDraft() {
    setConflict(false)
    setLocalError(null)
    await workspace.refresh()
  }

  async function loadImpact() {
    setSaving(true)
    setLocalError(null)
    try {
      setImpact(await workspace.impact(resource!.artifact.id))
    } catch (error) {
      setLocalError(errorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  async function freezeSelection() {
    const approved = resource?.approvedRevision
    if (!approved) throw new Error('Approve an immutable Blueprint revision before freezing a selection.')
    if (selectedNodeIds.length === 0) throw new Error('Select at least one Blueprint node.')
    const approvedNodes = normalizeBlueprintContent(approved.content).semantic?.nodes ?? []
    const approvedIDs = new Set(approvedNodes.map((node) => node.id))
    const missing = selectedNodeIds.filter((nodeId) => !approvedIDs.has(nodeId))
    if (missing.length > 0) {
      throw new Error('The selection contains draft-only nodes. Create and approve a Blueprint revision first.')
    }
    const frozen = await flow.compileBlueprintSelection({
      blueprintRevision: versionRef(approved),
      nodeIds: [...selectedNodeIds].sort(),
    }, resource.artifact.etag)
    if (!frozen) throw new Error(flow.error ?? 'The Blueprint selection could not be frozen.')
    return frozen
  }

  async function generateDocumentsFromSelection() {
    const project = collaboration.project
    if (!project) return
    setSaving(true)
    setLocalError(null)
    setSelectionMessage(null)
    try {
      const manifest = await freezeSelection()
      const scope = readBlueprintSelectionScope(manifest)
      const content = {
        ...createEmptyDocumentContent('frontendDevelopment'),
        summary: `Implementation notes for ${scope.nodes.map((node) => node.title).join(', ')}`,
        blocks: scope.nodes.map((node) => ({
          id: stableId('selection-block'),
          type: 'sourceReference' as const,
          text: `${node.kind}: ${node.title}`,
          requirementIds: node.requirementIds ?? [],
          data: { blueprintNodeId: node.id, selectionId: scope.selectionId },
        })),
        requirements: [],
        acceptanceCriteria: [],
      }
      const created = await collaboration.platformClient.documents.create(project.id, {
        title: `Selection notes · ${scope.nodes[0]?.title ?? scope.selectionId.slice(0, 12)}`,
        kind: content.kind,
        content,
        sourceVersions: manifest.sources.map((source) => source.ref),
      }, { idempotencyKey: true })
      const draftETag = created.data.draft?.etag ?? created.etag
      if (!draftETag) throw new Error('The document service did not return a draft ETag.')
      const revision = await collaboration.platformClient.documents.createRevision(
        created.data.artifact.id,
        { changeSummary: 'Freeze Blueprint selection documentation target', changeSource: 'system' },
        { ifMatch: draftETag, idempotencyKey: true },
      )
      await workspace.createProposal({
        jobType: 'selection.documentation',
        targetRevision: versionRef(revision.data),
        instruction: `Generate implementation documentation only for Blueprint selection ${scope.selectionId}. Preserve every stable node anchor and identify assumptions explicitly.`,
        inputVersions: manifest.sources.map((source) => source.ref),
        constraints: {
          parentSelectionManifest: { id: manifest.id, hash: manifest.hash },
          frozenSelectionScope: scope as unknown as import('@/lib/platform/dto').JsonObject,
        },
        outputSchemaVersion: 'selection-document-proposal/v1',
      })
      await workspace.refresh()
      setSelectionMessage(`Documentation proposal created from ${scope.nodeIds.length} frozen nodes.`)
    } catch (error) {
      setLocalError(errorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  async function createPrototypesFromSelection() {
    const project = collaboration.project
    if (!project) return
    setSaving(true)
    setLocalError(null)
    setSelectionMessage(null)
    try {
      const manifest = await freezeSelection()
      const scope = readBlueprintSelectionScope(manifest)
      const pending = scope.pageBindings.filter((binding) => binding.pageSpec && !binding.prototype)
      if (pending.length === 0) {
        throw new Error(scope.pageBindings.length === 0
          ? 'Select at least one approved Blueprint Page with a PageSpec.'
          : 'Every selected Page already has an approved Prototype in this frozen selection.')
      }
      for (const binding of pending) {
        const pageSpec = binding.pageSpec!
        const revision = await collaboration.platformClient.artifacts.getRevision<PageSpecContentDto>(pageSpec.revisionId)
        if (
          revision.data.artifactId !== pageSpec.artifactId
          || revision.data.contentHash !== pageSpec.contentHash
        ) throw new Error(`PageSpec ${binding.nodeId} changed while creating its Prototype.`)
        const page = scope.nodes.find((node) => node.id === binding.nodeId)
        await collaboration.platformClient.prototypes.create(project.id, {
          title: `${page?.title ?? binding.nodeId} Prototype`,
          pageSpecRevision: pageSpec,
          exploratory: false,
          content: createEmptyPrototypeContent(pageSpec, revision.data.content, false),
        }, { idempotencyKey: true })
      }
      await workspace.refresh()
      setSelectionMessage(`${pending.length} formal Prototype draft${pending.length === 1 ? '' : 's'} created from exact PageSpec revisions; review and approve them before running Workbench.`)
    } catch (error) {
      setLocalError(errorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  async function useSelectionInWorkbench() {
    setSaving(true)
    setLocalError(null)
    setSelectionMessage(null)
    try {
      const manifest = await freezeSelection()
      const scope = readBlueprintSelectionScope(manifest)
      if (scope.pageBindings.length === 0 || scope.pageBindings.some((binding) => !binding.pageSpec || !binding.prototype)) {
        throw new Error('Workbench requires every selected Page to have approved PageSpec and Prototype revisions.')
      }
      const run = await flow.startFromManifest(manifest, {
        definitionKey: 'blueprint-selection-app',
        scope: { blueprintSelection: { selectionId: scope.selectionId } },
      })
      if (!run) throw new Error(flow.error ?? 'The selection workflow did not start.')
      setSurface('workbench')
    } catch (error) {
      setLocalError(errorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full min-h-0 bg-canvas max-lg:flex-col">
      <aside className="w-60 shrink-0 overflow-y-auto border-r border-border bg-panel p-3 scrollbar-thin max-lg:max-h-48 max-lg:w-full max-lg:border-b max-lg:border-r-0">
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs font-semibold text-foreground">Platform blueprints</span>
          <button type="button" onClick={() => void workspace.createBlueprint('Untitled blueprint')} disabled={readOnly} className="rounded border border-border p-1.5 text-primary-bright disabled:opacity-40" aria-label="Create blueprint"><FilePlus2 className="size-3.5" /></button>
        </div>
        <div className="mt-3 space-y-1">
          {workspace.blueprints.map((item) => (
            <button key={item.artifact.id} type="button" onClick={() => setSelectedBlueprintId(item.artifact.id)} className={cn('block w-full rounded-md px-2.5 py-2 text-left', item.artifact.id === resource.artifact.id ? 'bg-primary/15' : 'hover:bg-white/5')}>
              <span className="block truncate text-[11px] font-medium text-foreground">{item.artifact.title}</span>
              <span className="mt-0.5 block text-[9px] text-faint-foreground">{item.artifact.status} · revision {item.latestRevision?.revisionNumber ?? 0}</span>
            </button>
          ))}
        </div>
      </aside>

      <main className="flex min-w-0 flex-1 flex-col">
        <header className="flex flex-wrap items-center gap-3 border-b border-border bg-panel px-4 py-3">
          <Workflow className="size-4 text-primary-bright" />
          <span className="min-w-0 flex-1"><span className="block truncate text-sm font-semibold text-foreground">{resource.artifact.title}</span><span className="block text-[9px] text-faint-foreground">{resource.artifact.id} · draft ETag {serverEtag ?? 'missing'} · semantic {nodes.length} / layout {Object.keys(layout.nodePositions).length}</span></span>
          {resource.artifact.status === 'needsSync' && <span className="rounded bg-warning/10 px-2 py-1 text-[9px] text-warning">needs_sync</span>}
          <span className={cn('inline-flex items-center gap-1 rounded px-2 py-1 text-[9px]', conflict ? 'bg-warning/10 text-warning' : saving ? 'bg-primary/10 text-primary-bright' : dirty ? 'bg-warning/10 text-warning' : 'bg-success/10 text-success')}>{saving ? <Loader2 className="size-3 animate-spin" /> : conflict ? <AlertTriangle className="size-3" /> : <Save className="size-3" />}{conflict ? 'Conflict' : saving ? 'Saving' : dirty ? 'Pending autosave' : savedAt ? `Saved ${savedAt}` : 'Server draft'}</span>
          <button type="button" onClick={() => void workspace.refresh()} className="rounded border border-border p-1.5 text-muted-foreground" aria-label="Refresh blueprint"><RefreshCw className="size-3.5" /></button>
        </header>

        {(localError || conflict) && <div role="alert" className="border-b border-warning/30 bg-warning/10 px-4 py-2 text-[10px] text-warning">{localError}{conflict && <button type="button" onClick={() => void reloadServerDraft()} className="ml-3 underline">Reload current server draft</button>}</div>}
        {selectionMessage && <div role="status" className="border-b border-success/30 bg-success/10 px-4 py-2 text-[10px] text-success">{selectionMessage}</div>}

        <nav className="flex overflow-x-auto border-b border-border bg-panel p-1 scrollbar-thin">
          {([
            ['canvas', `Graph ${nodes.length}`],
            ['pages', `PageSpecs ${pageSpecs.length}`],
            ['versions', `Versions ${details?.versions.length ?? 0}`],
            ['proposal', `AI proposals ${proposals.length}`],
            ['impact', 'Impact lens'],
            ['trace', `Trace ${workspace.traces.length}`],
            ['review', `Review ${comments.length + reviews.length}`],
          ] as const).map(([id, label]) => <button key={id} type="button" onClick={() => setTab(id)} className={cn('shrink-0 rounded px-3 py-1.5 text-[10px] font-medium', tab === id ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground')}>{label}</button>)}
        </nav>

        <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
          {tab === 'canvas' && (
            <div className="grid min-h-full xl:grid-cols-[1fr_290px]">
              <section className="min-w-0 p-4">
                <div className="mb-3 rounded-lg border border-border bg-panel p-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="mr-1 inline-flex items-center gap-1 text-[9px] font-semibold uppercase tracking-wide text-faint-foreground"><Boxes className="size-3" />Module library</span>
                    {BLUEPRINT_MODULE_TEMPLATES.map((template) => <button key={template.id} type="button" draggable={!readOnly} title={template.description} onDragStart={(event) => event.dataTransfer.setData('application/x-blueprint-module', template.id)} onClick={() => addModule(template)} disabled={readOnly} className="rounded border border-border bg-background px-2 py-1 text-[9px] font-medium text-foreground hover:border-primary/50 disabled:opacity-40">{template.label}</button>)}
                    <button type="button" onClick={groupSelection} disabled={readOnly || selectedNodeIds.length === 0} className="ml-auto inline-flex items-center gap-1 rounded border border-primary/40 px-2 py-1 text-[9px] font-semibold text-primary-bright disabled:opacity-40"><Group className="size-3" />Group as capability ({selectedNodeIds.length})</button>
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-2 border-t border-border pt-2">
                    <span className="text-[9px] text-faint-foreground">Approved selection actions use exact revision + stable anchors</span>
                    <button type="button" onClick={() => void generateDocumentsFromSelection()} disabled={readOnly || selectedNodeIds.length === 0 || saving} className="rounded bg-primary/15 px-2 py-1 text-[9px] font-semibold text-primary-bright disabled:opacity-40">Generate docs from selection</button>
                    <button type="button" onClick={() => void createPrototypesFromSelection()} disabled={readOnly || selectedNodeIds.length === 0 || saving} className="rounded bg-primary/15 px-2 py-1 text-[9px] font-semibold text-primary-bright disabled:opacity-40">Create prototypes from selection</button>
                    <button type="button" onClick={() => void useSelectionInWorkbench()} disabled={readOnly || selectedNodeIds.length === 0 || saving} className="inline-flex items-center gap-1 rounded bg-primary px-2 py-1 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Play className="size-3" />Use selection in workflow / Workbench</button>
                  </div>
                  {layout.groups.length > 0 && <div className="mt-2 space-y-1 border-t border-border pt-2">{layout.groups.map((group) => <div key={group.id} className="flex items-center gap-1.5 rounded border border-border bg-background p-1">
                    <button type="button" aria-label={`Select capability ${group.title}`} onClick={() => selectCapabilityGroup(group)} className="inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-1 text-[9px] font-semibold text-primary-bright hover:bg-primary/10"><Group className="size-3" />{group.nodeIds.length} nodes</button>
                    <input aria-label={`Capability name ${group.title}`} value={group.title} readOnly={readOnly} onChange={(event) => renameCapabilityGroup(group.id, event.target.value)} className="h-7 min-w-0 flex-1 rounded border border-border bg-panel px-2 text-[9px] text-foreground" />
                    <button type="button" aria-label={`Ungroup capability ${group.title}`} onClick={() => ungroupCapability(group.id)} disabled={readOnly} className="rounded p-1 text-destructive disabled:opacity-40"><Trash2 className="size-3" /></button>
                  </div>)}</div>}
                </div>
                <div className="mb-3 flex flex-wrap items-center gap-2">
                  <select onChange={(event) => addNode(event.target.value as BlueprintNodeKind)} value="" disabled={readOnly} className="h-8 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground disabled:opacity-50"><option value="" disabled>Add semantic node…</option>{NODE_KINDS.map((kind) => <option key={kind} value={kind}>{kind}</option>)}</select>
                  <select value={edgeSourceId} onChange={(event) => setEdgeSourceId(event.target.value)} className="h-8 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground"><option value="">Source</option>{nodes.map((node) => <option key={node.id} value={node.id}>{node.title}</option>)}</select>
                  <select value={edgeKind} onChange={(event) => setEdgeKind(event.target.value as BlueprintEdgeKind)} className="h-8 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground">{EDGE_KINDS.map((kind) => <option key={kind}>{kind}</option>)}</select>
                  <select value={edgeTargetId} onChange={(event) => setEdgeTargetId(event.target.value)} className="h-8 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground"><option value="">Target</option>{nodes.map((node) => <option key={node.id} value={node.id}>{node.title}</option>)}</select>
                  <button type="button" onClick={addEdge} disabled={readOnly || !edgeSourceId || !edgeTargetId || edgeSourceId === edgeTargetId} className="h-8 rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-40"><Link2 className="mr-1 inline size-3" />Connect</button>
                </div>
                <div
                  ref={canvasRef}
                  className="relative overflow-auto rounded-lg border border-border bg-background"
                  style={{ minHeight: CANVAS_HEIGHT }}
                  onDragOver={(event) => event.preventDefault()}
                  onDrop={(event) => {
                    event.preventDefault()
                    const template = BLUEPRINT_MODULE_TEMPLATES.find((item) => item.id === event.dataTransfer.getData('application/x-blueprint-module'))
                    const canvas = canvasRef.current
                    if (!template || !canvas || readOnly) return
                    const rect = canvas.getBoundingClientRect()
                    addModule(template, {
                      x: event.clientX - rect.left + canvas.scrollLeft,
                      y: event.clientY - rect.top + canvas.scrollTop,
                    })
                  }}
                >
                  <div className="relative" style={{ width: CANVAS_WIDTH, height: CANVAS_HEIGHT, backgroundImage: 'radial-gradient(circle, rgb(148 163 184 / 0.15) 1px, transparent 1px)', backgroundSize: '20px 20px' }}>
                    {layout.groups.map((group) => {
                      const bounds = blueprintGroupBounds(group.nodeIds, layout)
                      if (!bounds) return null
                      return <div key={group.id} className="pointer-events-none absolute rounded-xl border border-dashed border-primary/45 bg-primary/5" style={{ left: bounds.left, top: bounds.top, width: bounds.width, height: bounds.height }}><button type="button" aria-label={`Select capability ${group.title} on canvas`} onClick={() => selectCapabilityGroup(group)} className="pointer-events-auto absolute -top-5 left-1 rounded bg-panel px-1.5 py-0.5 text-[8px] font-semibold text-primary-bright">{group.title}</button></div>
                    })}
                    <svg aria-hidden className="pointer-events-none absolute inset-0 size-full">
                      {edges.map((edge) => {
                        const source = layout.nodePositions[edge.sourceNodeId]
                        const target = layout.nodePositions[edge.targetNodeId]
                        if (!source || !target) return null
                        return <g key={edge.id}><line x1={source.x + NODE_WIDTH / 2} y1={source.y + NODE_HEIGHT / 2} x2={target.x + NODE_WIDTH / 2} y2={target.y + NODE_HEIGHT / 2} stroke="rgb(129 140 248 / 0.55)" strokeWidth="1.5" /><text x={(source.x + target.x + NODE_WIDTH) / 2} y={(source.y + target.y + NODE_HEIGHT) / 2 - 5} fill="rgb(148 163 184)" fontSize="9">{edge.kind}</text></g>
                      })}
                    </svg>
                    {nodes.map((node) => {
                      const position = layout.nodePositions[node.id] ?? { x: 0, y: 0 }
                      return <button
                        key={node.id}
                        type="button"
                        aria-pressed={selectedNodeIds.includes(node.id)}
                        onPointerDown={(event) => {
                          if (readOnly || event.button !== 0) return
                          const rect = event.currentTarget.getBoundingClientRect()
                          event.currentTarget.setPointerCapture(event.pointerId)
                          dragRef.current = { id: node.id, pointerId: event.pointerId, offsetX: event.clientX - rect.left, offsetY: event.clientY - rect.top }
                        }}
                        onPointerMove={(event) => {
                          const drag = dragRef.current
                          const canvas = canvasRef.current
                          if (!drag || drag.id !== node.id || !canvas) return
                          const rect = canvas.getBoundingClientRect()
                          moveNode(node.id, event.clientX - rect.left + canvas.scrollLeft - drag.offsetX, event.clientY - rect.top + canvas.scrollTop - drag.offsetY)
                        }}
                        onPointerUp={(event) => {
                          if (dragRef.current?.id === node.id && event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
                          dragRef.current = null
                        }}
                        onClick={(event) => selectNode(node.id, event.metaKey || event.ctrlKey || event.shiftKey)}
                        className={cn('absolute cursor-move rounded-lg border p-2 text-left shadow-sm touch-none', NODE_COLORS[node.kind], selectedNodeIds.includes(node.id) && 'ring-2 ring-primary')}
                        style={{ width: NODE_WIDTH, height: NODE_HEIGHT, transform: `translate(${position.x}px, ${position.y}px)` }}
                      ><span className="block truncate text-[9px] uppercase tracking-wide text-faint-foreground">{node.kind}</span><span className="block truncate text-[11px] font-semibold text-foreground">{node.title}</span></button>
                    })}
                  </div>
                </div>
                <div className="mt-3 space-y-1">
                  {edges.map((edge) => <div key={edge.id} className="flex items-center gap-2 rounded border border-border bg-panel px-2 py-1.5 text-[9px] text-muted-foreground"><GitFork className="size-3 text-primary-bright" /><code>{edge.sourceNodeId}</code><b>{edge.kind}</b><code>{edge.targetNodeId}</code><label className="ml-auto flex items-center gap-1"><input type="checkbox" checked={edge.required} disabled={readOnly} onChange={(event) => mutateBlueprint((_nodes, currentEdges) => ({ edges: currentEdges.map((item) => item.id === edge.id ? { ...item, required: event.target.checked } : item) }))} />required</label><button type="button" disabled={readOnly} onClick={() => mutateBlueprint((_nodes, currentEdges) => ({ edges: currentEdges.filter((item) => item.id !== edge.id) }))}><Trash2 className="size-3 text-destructive" /></button></div>)}
                </div>
              </section>

              <aside className="border-l border-border bg-panel p-4 max-xl:border-l-0 max-xl:border-t">
                {!selectedNode ? <p className="rounded border border-dashed border-border p-4 text-[10px] text-faint-foreground">Select a semantic node to edit it. Positions live only in the layout section.</p> : <NodeInspector node={selectedNode} position={layout.nodePositions[selectedNode.id] ?? { x: 0, y: 0 }} readOnly={readOnly} onChange={(patch) => updateNode(selectedNode.id, patch)} onMove={(x, y) => moveNode(selectedNode.id, x, y)} onDelete={() => deleteNode(selectedNode.id)} />}
              </aside>
            </div>
          )}

          {tab === 'pages' && <PageSpecsPanel selectedPageSpecId={selectedPageSpecId} onSelectedPageSpecId={setSelectedPageSpecId} nodes={nodes} pageSpecs={workspace.pageSpecs} hasRevision={Boolean(resource.approvedRevision)} readOnly={readOnly} onCreate={async (node, route, userGoal) => {
            try {
              const artifactId = await workspace.createPageSpec(resource.artifact.id, node.id, `${node.title} PageSpec`, route, userGoal)
              if (artifactId) updateNode(node.id, { pageSpecArtifactId: artifactId })
              return artifactId
            } catch (error) {
              setLocalError(errorMessage(error))
              return null
            }
          }} />}

          {tab === 'versions' && <section className="mx-auto max-w-4xl space-y-3 p-5"><GatePanel clientIssues={clientGate} serverGate={details?.reviewGate} /><button type="button" onClick={() => void createRevision()} disabled={readOnly || !revisionReady || saving} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground disabled:opacity-50"><GitBranch className="size-3.5" />Create immutable revision</button>{details?.versions.map((version) => <div key={version.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><FileClock className="size-4 text-primary-bright" /><span className="text-[11px] font-medium text-foreground">Revision {version.revisionNumber}</span><code className="ml-auto text-[9px] text-faint-foreground">{version.contentHash.slice(0, 16)}</code></div><p className="mt-1 text-[10px] text-muted-foreground">{new Date(version.createdAt).toLocaleString()} · {version.sourceVersions?.length ?? 0} pinned requirement revisions</p></div>)}</section>}

          {tab === 'proposal' && <ProposalPanel proposals={proposals} selected={selectedOperations} onSelected={setSelectedOperations} instruction={proposalInstruction} onInstruction={setProposalInstruction} canEdit={!readOnly} canCreate={Boolean(latestVersion) && !dirty} onCreate={() => void workspace.createProposal({ jobType: 'blueprint.patch', targetRevision: latestVersion!, instruction: proposalInstruction, inputVersions: resource.draft?.sourceVersions ?? [], outputSchemaVersion: 'blueprint.patch.v1' }).catch((error) => setLocalError(errorMessage(error)))} onApply={(proposal) => void workspace.applyProposal(proposal.id, selectedOperations[proposal.id] ?? []).catch((error) => setLocalError(errorMessage(error)))} />}

          {tab === 'impact' && <section className="mx-auto max-w-4xl space-y-3 p-5"><div className="flex items-center justify-between gap-3"><div><h2 className="text-sm font-semibold text-foreground">Downstream impact lens</h2><p className="mt-1 text-[10px] text-muted-foreground">Compares pinned blueprint versions with PageSpecs, prototypes and workbench outputs.</p></div><button type="button" onClick={() => void loadImpact()} className="rounded-md bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground"><RefreshCw className="mr-1 inline size-3" />Analyze impact</button></div>{resource.artifact.status === 'needsSync' && <div className="rounded-md border border-warning/30 bg-warning/10 p-3 text-[10px] text-warning"><AlertTriangle className="mr-1 inline size-3" />The server marked this blueprint or a dependent artifact as needs_sync.</div>}{impact?.items.length === 0 && <p className="rounded border border-dashed border-border p-4 text-[10px] text-faint-foreground">No downstream impact was reported.</p>}{impact?.items.map((item, index) => <div key={`${item.targetArtifactId}-${index}`} className={cn('rounded-md border bg-panel p-3 text-[10px]', item.needsSync ? 'border-warning/40' : 'border-border')}><div className="flex items-center gap-2"><code className="text-primary-bright">{item.targetKind}:{item.targetArtifactId}</code><span className="ml-auto rounded bg-white/5 px-1.5 py-0.5">{item.severity}</span>{item.needsSync && <span className="rounded bg-warning/10 px-1.5 py-0.5 text-warning">needs_sync</span>}</div><p className="mt-1 text-muted-foreground">{item.reason}</p><p className="mt-1 text-faint-foreground">source {item.source.artifactId}@{item.source.revisionNumber} · {item.source.contentHash.slice(0, 12)}</p></div>)}</section>}

          {tab === 'trace' && <section className="mx-auto max-w-4xl space-y-2 p-5">{details?.dependencies.map((dependency) => <div key={dependency.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><Link2 className="mr-2 inline size-3.5 text-primary-bright" />{dependency.source.artifactId}{dependency.source.revisionNumber ? `@${dependency.source.revisionNumber}` : ''} <b>{dependency.relation}</b> {dependency.target.artifactId}{dependency.target.revisionNumber ? `@${dependency.target.revisionNumber}` : ''}{dependency.required && <span className="ml-2 text-warning">required</span>}</div>)}{workspace.traces.filter((trace) => trace.source.artifactId === resource.artifact.id || trace.target.artifactId === resource.artifact.id).map((trace) => <div key={trace.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><code>{trace.source.artifactId}:{trace.source.revisionId}</code> → {trace.relation} → <code>{trace.target.artifactId}:{trace.target.revisionId}</code></div>)}</section>}

          {tab === 'review' && <section className="mx-auto max-w-4xl space-y-3 p-5"><GatePanel clientIssues={clientGate} serverGate={details?.reviewGate} />{!latestVersion && <p className="rounded-md border border-dashed border-border p-4 text-[10px] text-faint-foreground">Create a revision before commenting or requesting review.</p>}{latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_auto]"><input value={comment} onChange={(event) => setComment(event.target.value)} placeholder="Comment on this exact blueprint revision" className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><button type="button" onClick={() => void collaboration.addComment(comment, undefined, latestVersion).then((ok) => ok && setComment(''))} disabled={!comment.trim() || !collaboration.can('comment')} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><MessageSquare className="mr-1 inline size-3" />Comment</button></div>}{latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_180px_auto]"><input value={reviewSummary} onChange={(event) => setReviewSummary(event.target.value)} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><select value={reviewerId} onChange={(event) => setReviewerId(event.target.value)} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground"><option value="">Reviewer</option>{collaboration.members.filter((member) => member.user.id !== currentUserId && ['owner', 'admin', 'editor'].includes(member.role)).map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name}</option>)}</select><button type="button" onClick={() => void collaboration.requestReview(reviewSummary, latestVersion, [reviewerId])} disabled={!gatePassed || !reviewerId || !reviewSummary.trim()} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><Send className="mr-1 inline size-3" />Request review</button></div>}{comments.map((thread) => <div key={thread.id} className="rounded-md border border-border bg-panel p-3"><span className="text-[10px] font-medium text-foreground">{thread.author.name}</span><p className="mt-1 text-[10px] text-muted-foreground">{thread.body}</p><p className="mt-1 text-[9px] text-faint-foreground">Pinned to revision {thread.target?.revisionNumber}</p></div>)}{reviews.map((review) => <div key={review.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><span className="font-medium text-foreground">{review.state}</span><span className="ml-2 text-muted-foreground">{review.summary}</span><span className="ml-2 text-faint-foreground">revision {review.target?.revisionNumber ?? 'unknown'}</span></div>)}</section>}
        </div>
      </main>
    </div>
  )
}

function NodeInspector({ node, position, readOnly, onChange, onMove, onDelete }: { node: SemanticNode; position: { readonly x: number; readonly y: number }; readOnly: boolean; onChange: (patch: Partial<SemanticNode>) => void; onMove: (x: number, y: number) => void; onDelete: () => void }) {
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <Move className="size-4 text-primary-bright" />
        <h2 className="text-xs font-semibold text-foreground">Semantic node</h2>
        <button type="button" onClick={onDelete} disabled={readOnly} className="ml-auto text-destructive disabled:opacity-40"><Trash2 className="size-4" /></button>
      </div>
      <code className="block truncate text-[9px] text-faint-foreground">{node.id}</code>
      <label className="block text-[10px] text-muted-foreground">Stable business key<input value={node.key} readOnly={readOnly} onChange={(event) => onChange({ key: event.target.value })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground" /></label>
      <label className="block text-[10px] text-muted-foreground">Kind<select value={node.kind} disabled={readOnly} onChange={(event) => onChange({ kind: event.target.value as BlueprintNodeKind })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground">{!NODE_KINDS.includes(node.kind) && <option value={node.kind}>{node.kind} (legacy; migrate before review)</option>}{NODE_KINDS.map((kind) => <option key={kind}>{kind}</option>)}</select></label>
      <label className="block text-[10px] text-muted-foreground">Title<input value={node.title} readOnly={readOnly} onChange={(event) => onChange({ title: event.target.value })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground" /></label>
      <label className="block text-[10px] text-muted-foreground">Description<textarea value={node.description ?? ''} readOnly={readOnly} onChange={(event) => onChange({ description: event.target.value })} rows={4} className="mt-1 w-full rounded border border-border bg-background p-2 text-[10px] text-foreground" /></label>
      {node.kind === 'page' && <>
        <label className="block text-[10px] text-muted-foreground">Route<input value={node.route ?? ''} readOnly={readOnly} onChange={(event) => onChange({ route: event.target.value })} placeholder="/orders" className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground" /></label>
        <label className="block text-[10px] text-muted-foreground">User goal<textarea value={node.userGoal ?? ''} readOnly={readOnly} onChange={(event) => onChange({ userGoal: event.target.value })} rows={3} className="mt-1 w-full rounded border border-border bg-background p-2 text-[10px] text-foreground" /></label>
      </>}
      {(node.kind === 'apiOperation' || node.kind === 'api') && <>
        <label className="block text-[10px] text-muted-foreground">HTTP method<select value={node.method?.toUpperCase() ?? ''} disabled={readOnly} onChange={(event) => onChange({ method: event.target.value })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground"><option value="">Select method</option>{HTTP_METHODS.map((method) => <option key={method} value={method}>{method}</option>)}</select></label>
        <label className="block text-[10px] text-muted-foreground">API path<input value={node.path ?? ''} readOnly={readOnly} onChange={(event) => onChange({ path: event.target.value })} placeholder="/orders" className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground" /></label>
      </>}
      {node.kind === 'permission' && <label className="block text-[10px] text-muted-foreground">Roles<input value={(node.roles ?? []).join(', ')} readOnly={readOnly} onChange={(event) => onChange({ roles: commaList(event.target.value) })} placeholder="admin, editor" className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground" /></label>}
      <label className="block text-[10px] text-muted-foreground">Stable requirement IDs<input value={node.requirementIds.join(', ')} readOnly={readOnly} onChange={(event) => onChange({ requirementIds: commaList(event.target.value) })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground" /></label>
      <label className="block text-[10px] text-muted-foreground">Assigned member IDs<input value={node.assignedMemberIds.join(', ')} readOnly={readOnly} onChange={(event) => onChange({ assignedMemberIds: commaList(event.target.value) })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground" /></label>
      <div className="rounded border border-border bg-background p-2">
        <span className="text-[9px] font-medium uppercase text-faint-foreground">Layout only</span>
        <div className="mt-2 grid grid-cols-2 gap-2">
          <label className="text-[9px] text-muted-foreground">X<input type="number" value={position.x} disabled={readOnly} onChange={(event) => onMove(Number(event.target.value), position.y)} className="mt-1 h-8 w-full rounded border border-border bg-panel px-2 text-[10px] text-foreground" /></label>
          <label className="text-[9px] text-muted-foreground">Y<input type="number" value={position.y} disabled={readOnly} onChange={(event) => onMove(position.x, Number(event.target.value))} className="mt-1 h-8 w-full rounded border border-border bg-panel px-2 text-[10px] text-foreground" /></label>
        </div>
      </div>
      {node.pageSpecArtifactId && <p className="rounded border border-success/30 bg-success/10 p-2 text-[9px] text-success">PageSpec {node.pageSpecArtifactId}</p>}
    </div>
  )
}

function PageSpecsPanel({ selectedPageSpecId, onSelectedPageSpecId, nodes, pageSpecs, hasRevision, readOnly, onCreate }: { selectedPageSpecId: string; onSelectedPageSpecId: (artifactId: string) => void; nodes: readonly SemanticNode[]; pageSpecs: ReturnType<typeof useArtifactWorkspace>['pageSpecs']; hasRevision: boolean; readOnly: boolean; onCreate: (node: SemanticNode, route: string, userGoal: string) => Promise<string | null> }) {
  const pages = nodes.filter((node) => node.kind === 'page')
  if (selectedPageSpecId && pageSpecs.some((item) => item.artifact.id === selectedPageSpecId)) {
    return <section className="p-5"><PageSpecEditor artifactId={selectedPageSpecId} onBack={() => onSelectedPageSpecId('')} /></section>
  }
  return <section className="mx-auto max-w-4xl space-y-3 p-5"><div><h2 className="text-sm font-semibold text-foreground">PageSpec outputs</h2><p className="mt-1 text-[10px] text-muted-foreground">Every PageSpec pins an approved Blueprint revision and its stable page-node anchor. Open one here to edit, version, review, and approve it before formal prototyping.</p></div>{!hasRevision && <p className="rounded-md border border-warning/30 bg-warning/10 p-3 text-[10px] text-warning">Approve a Blueprint revision before creating formal PageSpecs.</p>}{pages.length === 0 && <p className="rounded border border-dashed border-border p-4 text-[10px] text-faint-foreground">Add at least one page node to split the product into functional pages.</p>}{pages.map((node) => {
    const existing = pageSpecs.find((item) => (item.draft?.content ?? item.latestRevision?.content)?.blueprintPageNodeId === node.id)
    const route = node.route?.trim() || `/${slug(node.title)}`
    const userGoal = node.userGoal?.trim() || node.description?.trim() || `Complete ${node.title}`
    return <div key={node.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex flex-wrap items-center gap-2"><span className="text-[11px] font-semibold text-foreground">{node.title}</span><code className="text-[9px] text-faint-foreground">{node.id}</code>{existing ? <><span className={cn('ml-auto rounded px-2 py-1 text-[9px]', existing.artifact.status === 'approved' ? 'bg-success/10 text-success' : 'bg-primary/10 text-primary-bright')}><CheckCircle2 className="mr-1 inline size-3" />{existing.artifact.status} · r{existing.latestRevision?.revisionNumber ?? 0}</span><button type="button" onClick={() => onSelectedPageSpecId(existing.artifact.id)} className="rounded border border-primary/40 px-2.5 py-1.5 text-[9px] font-semibold text-primary-bright">Open editor</button></> : <button type="button" onClick={() => void onCreate(node, route, userGoal).then((artifactId) => artifactId && onSelectedPageSpecId(artifactId))} disabled={readOnly || !hasRevision} className="ml-auto rounded bg-primary px-2.5 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Plus className="mr-1 inline size-3" />Create {route}</button>}</div><p className="mt-2 text-[10px] text-muted-foreground">{userGoal}</p></div>
  })}</section>
}

function GatePanel({ clientIssues, serverGate }: { clientIssues: string[]; serverGate?: ArtifactReviewGateDto }) {
  const serverErrors = serverGate?.checks
    .filter((check) => check.severity === 'error' && check.code !== 'canonical_review_approved')
    .map((check) => check.message) ?? []
  const issues = [...clientIssues, ...serverErrors]
  const requestReady = issues.length === 0 && reviewGateReadyForRequest(serverGate)
  const approved = Boolean(serverGate?.passed)
  return <div className={cn('rounded-lg border p-3', approved || requestReady ? 'border-success/30 bg-success/10' : 'border-warning/30 bg-warning/10')}><div className="flex items-center gap-2 text-[11px] font-semibold text-foreground">{approved || requestReady ? <CheckCircle2 className="size-4 text-success" /> : <AlertTriangle className="size-4 text-warning" />}Review gate {approved ? 'approved' : requestReady ? 'ready to request' : 'blocked'}</div>{issues.map((issue) => <p key={issue} className="mt-1 text-[10px] text-muted-foreground">• {issue}</p>)}{requestReady && !approved && <p className="mt-1 text-[10px] text-success">Pre-review checks passed; canonical reviewer approval is pending.</p>}{serverGate && <p className="mt-2 text-[9px] text-faint-foreground">Trace coverage {Math.round(serverGate.traceCoverage * 100)}% · {serverGate.unresolvedBlockingCommentIds.length} unresolved blocking comments</p>}</div>
}

function ProposalPanel({ proposals, selected, onSelected, instruction, onInstruction, canEdit, canCreate, onCreate, onApply }: { proposals: readonly ProposalDto[]; selected: Record<string, string[]>; onSelected: (next: Record<string, string[]>) => void; instruction: string; onInstruction: (value: string) => void; canEdit: boolean; canCreate: boolean; onCreate: () => void; onApply: (proposal: ProposalDto) => void }) {
  return <section className="mx-auto max-w-4xl space-y-3 p-5"><div className="rounded-lg border border-border bg-panel p-3"><textarea value={instruction} onChange={(event) => onInstruction(event.target.value)} rows={3} className="w-full rounded border border-border bg-background p-2 text-[11px] text-foreground" /><button type="button" onClick={onCreate} disabled={!canEdit || !canCreate || !instruction.trim()} className="mt-2 rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><Bot className="mr-1 inline size-3" />Ask AI for proposal</button>{!canCreate && <p className="mt-2 text-[9px] text-warning">Save and create an immutable blueprint revision first. AI never consumes mutable draft bytes.</p>}</div>{proposals.map((proposal) => { const selectedIds = selected[proposal.id] ?? []; const hasAccepted = proposal.operations.some((operation) => operation.decision === 'accepted' || selectedIds.includes(operation.id)); return <div key={proposal.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><span className="text-[11px] font-semibold text-foreground">Manifest {proposal.manifest.id.slice(0, 12)}</span><span className="rounded bg-primary/10 px-1.5 py-0.5 text-[9px] text-primary-bright">{proposal.status}</span><code className="ml-auto text-[9px] text-faint-foreground">base {proposal.baseRevision.contentHash.slice(0, 12)}</code></div><div className="mt-2 space-y-1">{proposal.operations.map((operation) => <label key={operation.id} className="flex gap-2 rounded border border-border bg-background p-2 text-[9px] text-muted-foreground"><input type="checkbox" disabled={operation.decision !== 'pending'} checked={operation.decision === 'accepted' || operation.decision === 'applied' || selectedIds.includes(operation.id)} onChange={(event) => onSelected({ ...selected, [proposal.id]: event.target.checked ? [...selectedIds, operation.id] : selectedIds.filter((item) => item !== operation.id) })} /><span className="min-w-0 flex-1"><code>{operation.kind} {operation.path || '/'}</code><span className="ml-2 text-faint-foreground">{operation.decision}</span>{operation.rationale && <span className="mt-1 block">{operation.rationale}</span>}</span></label>)}</div><button type="button" onClick={() => onApply(proposal)} disabled={!canEdit || !hasAccepted || !['open', 'reviewing', 'ready'].includes(proposal.status)} className="mt-2 rounded bg-primary px-2.5 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-50">Decide all and apply accepted operations</button></div>})}</section>
}

function Unavailable({ title, detail, loading, action, onAction, onRetry }: { title: string; detail: string; loading?: boolean; action?: string; onAction?: () => void; onRetry?: () => Promise<void> }) {
  return <div className="flex h-full items-center justify-center bg-canvas p-6 text-center"><div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-6">{loading ? <Loader2 className="mx-auto size-7 animate-spin text-primary-bright" /> : <AlertTriangle className="mx-auto size-7 text-warning" />}<h1 className="mt-3 text-base font-semibold text-foreground">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{detail}</p>{action && onAction && <button type="button" onClick={onAction} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground">{action}</button>}{onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground"><RefreshCw className="mr-1 inline size-4" />Retry</button>}</div></div>
}

function versionRef<TContent>(revision: ArtifactRevisionDto<TContent>): VersionRefDto {
  return { artifactId: revision.artifactId, revisionId: revision.id, revisionNumber: revision.revisionNumber, contentHash: revision.contentHash }
}

function stableId(prefix: string) {
  const id = typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${id}`
}

function blueprintGroupBounds(nodeIds: readonly string[], layout: BlueprintLayoutDto) {
  const positions = nodeIds.flatMap((nodeId) => layout.nodePositions[nodeId] ? [layout.nodePositions[nodeId]] : [])
  if (positions.length === 0) return null
  const padding = 18
  const left = Math.max(0, Math.min(...positions.map((position) => position.x)) - padding)
  const top = Math.max(0, Math.min(...positions.map((position) => position.y)) - padding)
  const right = Math.max(...positions.map((position) => position.x + NODE_WIDTH)) + padding
  const bottom = Math.max(...positions.map((position) => position.y + NODE_HEIGHT)) + padding
  return { left, top, width: right - left, height: bottom - top }
}

function commaList(value: string) {
  return Array.from(new Set(value.split(',').map((item) => item.trim()).filter(Boolean)))
}

function slug(value: string) {
  return value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'page'
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : 'Blueprint operation failed.'
}

function artifactReference() {
  if (typeof window === 'undefined') return ''
  return new URLSearchParams(window.location.search).get('artifactId') ?? ''
}
