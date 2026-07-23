'use client'

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
import {
  exactArtifactRefsEqual,
  revisionCandidates,
} from '@/lib/platform/workflow-ui-contract'
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
import { reviewCandidatesForGovernance } from '@/lib/worksflow/project-governance'
import { useI18n } from '@/lib/i18n'
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

interface BlueprintSaveTask {
  readonly session: number
  readonly artifactId: string
  etag: string
  latestContent: BlueprintContentDto
}

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
  const { locale, t } = useI18n()
  const workspace = useArtifactWorkspace()
  const collaboration = useCollaboration()
  const flow = usePlatformFlow()
  const { selectedBlueprintNodeId, setSelectedBlueprintNodeId, setSurface } = useWorksflow()
  const [selectedBlueprintId, setSelectedBlueprintId] = useState('')
  const [selectedPageSpecId, setSelectedPageSpecId] = useState('')
  const [tab, setTab] = useState<EditorTab>('canvas')
  const [content, setContent] = useState<BlueprintContentDto | null>(null)
  const [details, setDetails] = useState<ArtifactDetails<BlueprintContentDto> | null>(null)
  const [detailsArtifactId, setDetailsArtifactId] = useState('')
  const [impact, setImpact] = useState<ImpactReportDto | null>(null)
  const [saving, setSaving] = useState(false)
  const [actionBusy, setActionBusy] = useState(false)
  const [savedAt, setSavedAt] = useState<string | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)
  const [conflict, setConflict] = useState(false)
  const [edgeSourceId, setEdgeSourceId] = useState('')
  const [edgeTargetId, setEdgeTargetId] = useState('')
  const [edgeKind, setEdgeKind] = useState<BlueprintEdgeKind>('contains')
  const [proposalInstruction, setProposalInstruction] = useState(() => t('teamPlatform.blueprint.defaultProposalInstruction'))
  const [selectedOperations, setSelectedOperations] = useState<Record<string, string[]>>({})
  const [comment, setComment] = useState('')
  const [reviewSummary, setReviewSummary] = useState(() => t('teamPlatform.blueprint.defaultReviewSummary'))
  const [reviewerId, setReviewerId] = useState('')
  const [selectedNodeIds, setSelectedNodeIds] = useState<string[]>([])
  const [selectionMessage, setSelectionMessage] = useState<string | null>(null)
  const canvasRef = useRef<HTMLDivElement | null>(null)
  const dragRef = useRef<{ id: string; pointerId: number; offsetX: number; offsetY: number } | null>(null)
  const activeArtifactRef = useRef('')
  const contentRef = useRef<BlueprintContentDto | null>(null)
  const draftEtagRef = useRef('')
  const saveTaskRef = useRef<BlueprintSaveTask | null>(null)
  const saveDraftRef = useRef<(nextContent?: BlueprintContentDto | null) => Promise<unknown>>(async () => null)
  const localDirtyRef = useRef(false)
  const editSessionRef = useRef(0)
  const actionBusyRef = useRef(false)

  const resource = workspace.blueprints.find((item) => item.artifact.id === selectedBlueprintId)
    ?? workspace.blueprints[0]
  const serverContent = resource?.draft?.content ?? resource?.latestRevision?.content
  const serverEtag = resource?.draft?.etag ?? resource?.artifact.etag
  const canEdit = collaboration.can('edit')
  const currentDetails = detailsArtifactId === resource?.artifact.id
    ? details
    : null
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
  const governanceReviewers = reviewCandidatesForGovernance(
    collaboration.members,
    currentUserId,
    collaboration.project?.governanceMode ?? 'team',
  )
  const effectiveReviewerId = collaboration.project?.governanceMode === 'solo'
    ? governanceReviewers[0]?.user.id ?? ''
    : reviewerId
  const clientGate = normalized ? blueprintGate(normalized) : []
  const revisionReady = clientGate.length === 0
  const gatePassed = revisionReady && reviewGateReadyForRequest(currentDetails?.reviewGate)
  const appliedProposals = useMemo(() => proposals
    .filter((proposal) => proposal.status === 'applied' || proposal.status === 'partially_applied')
    .sort((left, right) => (right.appliedAt ?? right.createdAt).localeCompare(left.appliedAt ?? left.createdAt)), [proposals])
  const proposalRevisionById = useMemo(() => {
    const result = new Map<string, ArtifactRevisionDto<BlueprintContentDto>>()
    for (const revision of [resource?.latestRevision, resource?.approvedRevision, ...(currentDetails?.versions ?? [])]) {
      if (!revision?.proposalId) continue
      const current = result.get(revision.proposalId)
      if (!current || revision.revisionNumber > current.revisionNumber) {
        result.set(revision.proposalId, revision)
      }
    }
    return result
  }, [currentDetails?.versions, resource?.approvedRevision, resource?.latestRevision])
  const appliedProposalAwaitingRevision = appliedProposals.find(
    (proposal) => !proposalRevisionById.has(proposal.id),
  )
  const versionedAppliedProposal = appliedProposals.find(
    (proposal) => proposalRevisionById.has(proposal.id),
  )
  const appliedProposalRevision = versionedAppliedProposal
    ? proposalRevisionById.get(versionedAppliedProposal.id)
    : undefined
  const appliedProposalRevisionRef = useMemo(
    () => appliedProposalRevision ? versionRef(appliedProposalRevision) : undefined,
    [appliedProposalRevision],
  )
  const appliedRevisionSubmissionNodes = useMemo(() => {
    if (!appliedProposalRevisionRef || !flow.run || !flow.runDefinition) return []
    return flow.run.nodes.filter((node) => {
      if (
        node.type !== 'human_edit'
        || node.status !== 'waiting_input'
        || !node.allowedActions?.includes('submit_input')
      ) return false
      const definitionNode = flow.runDefinition?.definition.nodes.find(
        (candidate) => candidate.id === node.definitionNodeId,
      )
      return revisionCandidates(definitionNode, node, flow.run, workspace).candidates.some(
        (candidate) => exactArtifactRefsEqual(candidate.ref, appliedProposalRevisionRef),
      )
    })
  }, [appliedProposalRevisionRef, flow.run, flow.runDefinition, workspace])
  const appliedRevisionSubmissionNode = appliedRevisionSubmissionNodes.length === 1
    ? appliedRevisionSubmissionNodes[0]
    : undefined

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
      if (localDirtyRef.current && contentRef.current) {
        void saveDraftRef.current(contentRef.current)
      }
      editSessionRef.current += 1
      activeArtifactRef.current = ''
      contentRef.current = null
      draftEtagRef.current = ''
      localDirtyRef.current = false
      actionBusyRef.current = false
      setContent(null)
      setSaving(false)
      setActionBusy(false)
      return
    }
    if (selectedBlueprintId !== resource.artifact.id) setSelectedBlueprintId(resource.artifact.id)
    const next = normalizeBlueprintContent(serverContent ?? createEmptyBlueprintContent())
    const switched = activeArtifactRef.current !== resource.artifact.id
    if (switched || !contentRef.current) {
      if (switched && localDirtyRef.current && contentRef.current) {
        void saveDraftRef.current(contentRef.current)
      }
      editSessionRef.current += 1
      activeArtifactRef.current = resource.artifact.id
      contentRef.current = next
      draftEtagRef.current = serverEtag ?? ''
      localDirtyRef.current = false
      actionBusyRef.current = false
      setContent(next)
      setSaving(false)
      setActionBusy(false)
      if (switched) {
        setConflict(false)
        setLocalError(null)
        setSavedAt(null)
      }
      return
    }
    const serverPayload = JSON.stringify(next)
    const localPayload = JSON.stringify(contentRef.current)
    if (serverPayload === localPayload) {
      if (serverEtag) draftEtagRef.current = serverEtag
      return
    }
    const currentSave = saveTaskRef.current
    const savingCurrentArtifact = currentSave?.session === editSessionRef.current
      && currentSave.artifactId === resource.artifact.id
    if (!localDirtyRef.current && !savingCurrentArtifact && !conflict) {
      contentRef.current = next
      draftEtagRef.current = serverEtag ?? draftEtagRef.current
      setContent(next)
    }
  }, [conflict, resource, selectedBlueprintId, serverContent, serverEtag])

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
      setDetailsArtifactId('')
      return
    }
    const artifactId = resource.artifact.id
    setDetails(null)
    setDetailsArtifactId('')
    let active = true
    void workspace.loadDetails<BlueprintContentDto>(artifactId)
      .then((next) => {
        if (!active) return
        setDetails(next)
        setDetailsArtifactId(artifactId)
      })
      .catch((error) => { if (active) setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed'))) })
    return () => { active = false }
  }, [resource?.artifact.id, t, workspace.loadDetails])

  const saveDraft = useCallback(async (nextContent = contentRef.current) => {
    const session = editSessionRef.current
    const artifactId = activeArtifactRef.current
    const etag = draftEtagRef.current
    if (!artifactId || !nextContent || !etag || !canEdit || actionBusyRef.current) return null
    const initial = normalizeBlueprintContent(nextContent)
    const activeTask = saveTaskRef.current
    if (activeTask?.session === session && activeTask.artifactId === artifactId) {
      activeTask.latestContent = initial
      return null
    }
    const task: BlueprintSaveTask = {
      session,
      artifactId,
      etag,
      latestContent: initial,
    }
    saveTaskRef.current = task
    setSaving(true)
    setLocalError(null)
    try {
      let lastResult: Awaited<ReturnType<typeof workspace.saveBlueprintDraft>> | null = null
      let pending: BlueprintContentDto | null = initial
      while (pending) {
        const savedPayload: string = JSON.stringify(pending)
        lastResult = await workspace.saveBlueprintDraft(
          task.artifactId,
          pending,
          task.etag,
        )
        const nextEtag = lastResult.data.draft?.etag ?? lastResult.etag
        if (!nextEtag) throw new Error(t('teamPlatform.blueprint.missingDraftEtag'))
        task.etag = nextEtag
        const latest = normalizeBlueprintContent(task.latestContent)
        pending = JSON.stringify(latest) !== savedPayload
          ? latest
          : null
      }
      if (task.session === editSessionRef.current && task.artifactId === activeArtifactRef.current) {
        draftEtagRef.current = task.etag
        localDirtyRef.current = false
        setSavedAt(new Date().toLocaleTimeString(locale))
        setConflict(false)
      }
      return lastResult
    } catch (error) {
      if (task.session === editSessionRef.current && task.artifactId === activeArtifactRef.current) {
        if (error instanceof ArtifactWorkspaceConflictError) setConflict(true)
        setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed')))
      }
      return null
    } finally {
      if (saveTaskRef.current === task) saveTaskRef.current = null
      if (task.session === editSessionRef.current && task.artifactId === activeArtifactRef.current) {
        setSaving(false)
      }
    }
  }, [canEdit, locale, t, workspace.saveBlueprintDraft])

  useEffect(() => {
    saveDraftRef.current = saveDraft
  }, [saveDraft])

  useEffect(() => {
    if (!localDirtyRef.current) return
    const currentSave = saveTaskRef.current
    const savingCurrentArtifact = currentSave?.session === editSessionRef.current
      && currentSave.artifactId === activeArtifactRef.current
    if (!dirty && !savingCurrentArtifact) {
      localDirtyRef.current = false
      return
    }
    if (!content || conflict || !canEdit) return
    const timer = window.setTimeout(() => void saveDraft(content), 700)
    return () => window.clearTimeout(timer)
  }, [canEdit, conflict, content, dirty, saveDraft])

  useEffect(() => () => {
    if (localDirtyRef.current && contentRef.current) {
      void saveDraftRef.current(contentRef.current)
    }
  }, [])

  if (!collaboration.session.signedIn) {
    return <Unavailable title={t('teamPlatform.blueprint.signInTitle')} detail={t('teamPlatform.blueprint.signInDetail')} />
  }
  if (workspace.status === 'loading') {
    return <Unavailable loading title={t('teamPlatform.blueprint.loadingTitle')} detail={t('teamPlatform.blueprint.loadingDetail')} />
  }
  if (workspace.status === 'error') {
    return <Unavailable title={t('teamPlatform.blueprint.unavailableTitle')} detail={workspace.error ?? t('teamPlatform.blueprint.backendNoArtifacts')} onRetry={workspace.refresh} retryLabel={t('common.retry')} />
  }
  if (!resource || !normalized) {
    return <Unavailable title={t('teamPlatform.blueprint.noArtifactsTitle')} detail={t('teamPlatform.blueprint.noArtifactsDetail')} action={t('teamPlatform.dashboard.createBlueprint')} onAction={() => void workspace.createBlueprint(t('teamPlatform.blueprint.productBlueprint'))} />
  }

  const readOnly = !canEdit || actionBusy
  const navigationLocked = dirty || saving || actionBusy || conflict

  function recordEditedContent(next: BlueprintContentDto) {
    contentRef.current = next
    localDirtyRef.current = true
    const currentSave = saveTaskRef.current
    if (
      currentSave?.session === editSessionRef.current
      && currentSave.artifactId === activeArtifactRef.current
    ) {
      currentSave.latestContent = next
    }
  }

  function replaceEditedContent(next: BlueprintContentDto) {
    const normalizedNext = normalizeBlueprintContent(next)
    recordEditedContent(normalizedNext)
    setContent(normalizedNext)
  }

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
      const result = materializeBlueprintContent(
        currentNormalized,
        next.nodes ?? currentNodes,
        next.edges ?? currentEdges,
        next.layout ?? currentLayout,
      )
      recordEditedContent(result)
      return result
    })
  }

  function addNode(kind: BlueprintNodeKind = 'feature') {
    const id = stableId('node')
    const key = `${kind.replace(/([a-z])([A-Z])/g, '$1_$2').toUpperCase()}-${id.slice(-8).toUpperCase()}`
    mutateBlueprint((currentNodes, currentEdges, currentLayout) => ({
      nodes: [...currentNodes, {
        id,
        key,
        kind,
        title: t('teamPlatform.blueprint.newNode', { kind: nodeKindLabel(kind, t) }),
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
    const inserted = insertBlueprintModule(content, localizedModuleTemplate(template, t), origin, stableId)
    replaceEditedContent(inserted.content)
    setSelectedNodeIds(inserted.nodeIds)
    setSelectedBlueprintNodeId(inserted.nodeIds[0] ?? null)
  }

  function groupSelection() {
    if (!content || selectedNodeIds.length === 0) return
    const title = selectedNodeIds.length === 1
      ? nodes.find((node) => node.id === selectedNodeIds[0])?.title ?? t('teamPlatform.blueprint.capability')
      : t('teamPlatform.blueprint.capabilityNumber', { number: (layout.groups.length + 1).toLocaleString(locale) })
    replaceEditedContent(groupBlueprintNodes(content, selectedNodeIds, title, stableId('group')))
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
    const currentSave = saveTaskRef.current
    const session = editSessionRef.current
    const artifactId = activeArtifactRef.current
    if (
      !content
      || !artifactId
      || !revisionReady
      || dirty
      || saving
      || conflict
      || actionBusyRef.current
      || (currentSave?.session === session && currentSave.artifactId === artifactId)
    ) return
    const revisionContent = normalizeBlueprintContent(content)
    actionBusyRef.current = true
    setActionBusy(true)
    setSaving(true)
    setLocalError(null)
    try {
      await workspace.createBlueprintRevision(artifactId, revisionContent)
      if (session !== editSessionRef.current || artifactId !== activeArtifactRef.current) return
      localDirtyRef.current = false
      const nextDetails = await workspace.loadDetails<BlueprintContentDto>(artifactId)
      if (session !== editSessionRef.current || artifactId !== activeArtifactRef.current) return
      setDetails(nextDetails)
      setDetailsArtifactId(artifactId)
      setTab('versions')
    } catch (error) {
      if (session === editSessionRef.current && artifactId === activeArtifactRef.current) {
        setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed')))
      }
    } finally {
      if (session === editSessionRef.current && artifactId === activeArtifactRef.current) {
        actionBusyRef.current = false
        setActionBusy(false)
        setSaving(false)
      }
    }
  }

  async function reloadServerDraft() {
    localDirtyRef.current = false
    setConflict(false)
    setLocalError(null)
    await workspace.refresh()
  }

  async function applyProposal(proposal: ProposalDto) {
    const currentSave = saveTaskRef.current
    const session = editSessionRef.current
    const artifactId = activeArtifactRef.current
    if (
      !canEdit
      || !artifactId
      || dirty
      || saving
      || conflict
      || actionBusyRef.current
      || (currentSave?.session === session && currentSave.artifactId === artifactId)
    ) return
    actionBusyRef.current = true
    setActionBusy(true)
    setSaving(true)
    setLocalError(null)
    try {
      await workspace.applyProposal(proposal.id, selectedOperations[proposal.id] ?? [])
      if (session !== editSessionRef.current || artifactId !== activeArtifactRef.current) return
      const nextDetails = await workspace.loadDetails<BlueprintContentDto>(artifactId)
      if (session !== editSessionRef.current || artifactId !== activeArtifactRef.current) return
      setDetails(nextDetails)
      setDetailsArtifactId(artifactId)
      setSavedAt(null)
    } catch (error) {
      if (session === editSessionRef.current && artifactId === activeArtifactRef.current) {
        setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed')))
      }
    } finally {
      if (session === editSessionRef.current && artifactId === activeArtifactRef.current) {
        actionBusyRef.current = false
        setActionBusy(false)
        setSaving(false)
      }
    }
  }

  async function submitAppliedProposalRevision() {
    const session = editSessionRef.current
    const artifactId = activeArtifactRef.current
    if (
      !appliedProposalRevisionRef
      || !appliedRevisionSubmissionNode
      || !artifactId
      || appliedProposalRevisionRef.artifactId !== artifactId
      || dirty
      || saving
      || conflict
      || flow.busy
      || actionBusyRef.current
    ) return
    actionBusyRef.current = true
    setActionBusy(true)
    setSaving(true)
    setLocalError(null)
    try {
      const submitted = await flow.submitNodeRevision(
        appliedRevisionSubmissionNode,
        appliedProposalRevisionRef,
      )
      if (session !== editSessionRef.current || artifactId !== activeArtifactRef.current) return
      if (!submitted) {
        setLocalError(t('teamPlatform.blueprint.nextStep.submitFailed'))
        return
      }
      setSurface('workbench')
    } catch (error) {
      if (session === editSessionRef.current && artifactId === activeArtifactRef.current) {
        setLocalError(errorMessage(error, t('teamPlatform.blueprint.nextStep.submitFailed')))
      }
    } finally {
      if (session === editSessionRef.current && artifactId === activeArtifactRef.current) {
        actionBusyRef.current = false
        setActionBusy(false)
        setSaving(false)
      }
    }
  }

  async function loadImpact() {
    setSaving(true)
    setLocalError(null)
    try {
      setImpact(await workspace.impact(resource!.artifact.id))
    } catch (error) {
      setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed')))
    } finally {
      setSaving(false)
    }
  }

  async function freezeSelection() {
    const approved = resource?.approvedRevision
    if (!approved) throw new Error(t('teamPlatform.blueprint.freezeRequiresApproval'))
    if (selectedNodeIds.length === 0) throw new Error(t('teamPlatform.blueprint.selectNodeRequired'))
    const approvedNodes = normalizeBlueprintContent(approved.content).semantic?.nodes ?? []
    const approvedIDs = new Set(approvedNodes.map((node) => node.id))
    const missing = selectedNodeIds.filter((nodeId) => !approvedIDs.has(nodeId))
    if (missing.length > 0) {
      throw new Error(t('teamPlatform.blueprint.draftOnlySelection'))
    }
    const frozen = await flow.compileBlueprintSelection({
      blueprintRevision: versionRef(approved),
      nodeIds: [...selectedNodeIds].sort(),
    }, resource.artifact.etag)
    if (!frozen) throw new Error(flow.error ?? t('teamPlatform.blueprint.freezeFailed'))
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
        summary: t('teamPlatform.blueprint.implementationNotes', { nodes: scope.nodes.map((node) => node.title).join(', ') }),
        blocks: scope.nodes.map((node) => ({
          id: stableId('selection-block'),
          type: 'sourceReference' as const,
          text: `${nodeKindLabel(node.kind, t)}: ${node.title}`,
          requirementIds: node.requirementIds ?? [],
          data: { blueprintNodeId: node.id, selectionId: scope.selectionId },
        })),
        requirements: [],
        acceptanceCriteria: [],
      }
      const created = await collaboration.platformClient.documents.create(project.id, {
        title: t('teamPlatform.blueprint.selectionNotes', { name: scope.nodes[0]?.title ?? scope.selectionId.slice(0, 12) }),
        kind: content.kind,
        content,
        sourceVersions: manifest.sources.map((source) => source.ref),
      }, { idempotencyKey: true })
      const draftETag = created.data.draft?.etag ?? created.etag
      if (!draftETag) throw new Error(t('teamPlatform.blueprint.missingDraftEtag'))
      const revision = await collaboration.platformClient.documents.createRevision(
        created.data.artifact.id,
        { changeSummary: t('teamPlatform.blueprint.freezeDocumentationTarget'), changeSource: 'system' },
        { ifMatch: draftETag, idempotencyKey: true },
      )
      await workspace.createProposal({
        jobType: 'selection.documentation',
        targetRevision: versionRef(revision.data),
        instruction: t('teamPlatform.blueprint.selectionDocumentationInstruction', { id: scope.selectionId }),
        inputVersions: manifest.sources.map((source) => source.ref),
        constraints: {
          parentSelectionManifest: { id: manifest.id, hash: manifest.hash },
          frozenSelectionScope: scope as unknown as import('@/lib/platform/dto').JsonObject,
        },
        outputSchemaVersion: 'selection-document-proposal/v1',
      })
      await workspace.refresh()
      setSelectionMessage(t('teamPlatform.blueprint.documentationCreated', { count: scope.nodeIds.length.toLocaleString(locale) }))
    } catch (error) {
      setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed')))
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
          ? t('teamPlatform.blueprint.selectApprovedPage')
          : t('teamPlatform.blueprint.prototypesAlreadyApproved'))
      }
      for (const binding of pending) {
        const pageSpec = binding.pageSpec!
        const revision = await collaboration.platformClient.artifacts.getRevision<PageSpecContentDto>(pageSpec.revisionId)
        if (
          revision.data.artifactId !== pageSpec.artifactId
          || revision.data.contentHash !== pageSpec.contentHash
        ) throw new Error(t('teamPlatform.blueprint.pageSpecChanged', { id: binding.nodeId }))
        const page = scope.nodes.find((node) => node.id === binding.nodeId)
        await collaboration.platformClient.prototypes.create(project.id, {
          title: t('teamPlatform.blueprint.prototypeTitle', { page: page?.title ?? binding.nodeId }),
          pageSpecRevision: pageSpec,
          exploratory: false,
          content: createEmptyPrototypeContent(pageSpec, revision.data.content, false),
        }, { idempotencyKey: true })
      }
      await workspace.refresh()
      setSelectionMessage(t('teamPlatform.blueprint.prototypesCreated', { count: pending.length.toLocaleString(locale) }))
    } catch (error) {
      setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed')))
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
        throw new Error(t('teamPlatform.blueprint.workbenchRequirements'))
      }
      const run = await flow.startFromManifest(manifest, {
        definitionKey: 'blueprint-selection-app',
        scope: { blueprintSelection: { selectionId: scope.selectionId } },
      })
      if (!run) throw new Error(flow.error ?? t('teamPlatform.blueprint.workflowStartFailed'))
      setSurface('workbench')
    } catch (error) {
      setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full min-h-0 bg-canvas max-lg:flex-col">
      <aside className="w-60 shrink-0 overflow-y-auto border-r border-border bg-panel p-3 scrollbar-thin max-lg:max-h-48 max-lg:w-full max-lg:border-b max-lg:border-r-0">
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs font-semibold text-foreground">{t('teamPlatform.blueprint.platformBlueprints')}</span>
          <button type="button" onClick={() => void workspace.createBlueprint(t('teamPlatform.blueprint.untitledBlueprint'))} disabled={readOnly || navigationLocked} className="rounded border border-border p-1.5 text-primary-bright disabled:opacity-40" aria-label={t('teamPlatform.dashboard.createBlueprint')} title={t('teamPlatform.dashboard.createBlueprint')}><FilePlus2 className="size-3.5" /></button>
        </div>
        <div className="mt-3 space-y-1">
          {workspace.blueprints.map((item) => (
            <button
              key={item.artifact.id}
              type="button"
              onClick={() => setSelectedBlueprintId(item.artifact.id)}
              disabled={navigationLocked && item.artifact.id !== resource.artifact.id}
              className={cn('block w-full rounded-md px-2.5 py-2 text-left disabled:cursor-not-allowed disabled:opacity-50', item.artifact.id === resource.artifact.id ? 'bg-primary/15' : 'hover:bg-white/5')}
            >
              <span className="block truncate text-[11px] font-medium text-foreground">{item.artifact.title}</span>
              <span className="mt-0.5 block text-[9px] text-faint-foreground">{artifactStatusLabel(item.artifact.status, t)} · {t('teamPlatform.blueprint.revisionNumber', { number: (item.latestRevision?.revisionNumber ?? 0).toLocaleString(locale) })}</span>
            </button>
          ))}
        </div>
      </aside>

      <main className="flex min-w-0 flex-1 flex-col">
        <header className="flex flex-wrap items-center gap-3 border-b border-border bg-panel px-4 py-3">
          <Workflow className="size-4 text-primary-bright" />
          <span className="min-w-0 flex-1"><span className="block truncate text-sm font-semibold text-foreground">{resource.artifact.title}</span><span className="block text-[9px] text-faint-foreground">{resource.artifact.id} · {t('teamPlatform.blueprint.headerMeta', { etag: serverEtag ?? t('teamPlatform.common.missing'), semantic: nodes.length.toLocaleString(locale), layout: Object.keys(layout.nodePositions).length.toLocaleString(locale) })}</span></span>
          {resource.artifact.status === 'needsSync' && <span className="rounded bg-warning/10 px-2 py-1 text-[9px] text-warning">{t('doc.status.needsSync')}</span>}
          <span className={cn('inline-flex items-center gap-1 rounded px-2 py-1 text-[9px]', conflict ? 'bg-warning/10 text-warning' : saving ? 'bg-primary/10 text-primary-bright' : dirty ? 'bg-warning/10 text-warning' : 'bg-success/10 text-success')}>{saving ? <Loader2 className="size-3 animate-spin" /> : conflict ? <AlertTriangle className="size-3" /> : <Save className="size-3" />}{conflict ? t('teamPlatform.editor.conflict') : saving ? t('teamPlatform.blueprint.saving') : dirty ? t('teamPlatform.editor.pendingAutosave') : savedAt ? t('teamPlatform.editor.savedAt', { time: savedAt }) : t('teamPlatform.editor.serverDraft')}</span>
          <button type="button" onClick={() => void workspace.refresh()} disabled={navigationLocked} className="rounded border border-border p-1.5 text-muted-foreground disabled:opacity-40" aria-label={t('teamPlatform.blueprint.refresh')} title={t('teamPlatform.blueprint.refresh')}><RefreshCw className="size-3.5" /></button>
        </header>

        {(localError || conflict) && <div role="alert" className="border-b border-warning/30 bg-warning/10 px-4 py-2 text-[10px] text-warning">{localError}{conflict && <button type="button" onClick={() => void reloadServerDraft()} className="ml-3 underline">{t('teamPlatform.blueprint.reloadServerDraft')}</button>}</div>}
        {selectionMessage && <div role="status" className="border-b border-success/30 bg-success/10 px-4 py-2 text-[10px] text-success">{selectionMessage}</div>}
        {appliedProposalAwaitingRevision && (
          <div role="status" className="flex flex-wrap items-center gap-3 border-b border-primary/30 bg-primary/10 px-4 py-3">
            <CheckCircle2 className="size-4 shrink-0 text-primary-bright" />
            <div className="min-w-0 flex-1">
              <p className="text-[11px] font-semibold text-foreground">
                {t('teamPlatform.blueprint.nextStep.proposalApplied')}
              </p>
              <p className="mt-0.5 text-[9px] text-muted-foreground">
                {t('teamPlatform.blueprint.nextStep.createRevisionDetail', {
                  id: appliedProposalAwaitingRevision.id.slice(0, 12),
                })}
              </p>
              {!conflict && (dirty || saving) && (
                <p className="mt-1 text-[9px] text-warning">
                  {t('teamPlatform.blueprint.nextStep.waitAutosave')}
                </p>
              )}
              {conflict && (
                <p className="mt-1 text-[9px] text-warning">
                  {t('teamPlatform.blueprint.nextStep.resolveConflict')}
                </p>
              )}
              {!revisionReady && (
                <p className="mt-1 text-[9px] text-warning">
                  {t('teamPlatform.blueprint.nextStep.resolveGate', {
                    count: clientGate.length.toLocaleString(locale),
                  })}
                </p>
              )}
            </div>
            <button
              type="button"
              onClick={() => void createRevision()}
              disabled={readOnly || dirty || saving || conflict || !revisionReady}
              className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-40"
            >
              <GitBranch className="size-3.5" />
              {t('teamPlatform.blueprint.nextStep.createRevision')}
            </button>
          </div>
        )}
        {!appliedProposalAwaitingRevision && appliedProposalRevision && (
          <div role="status" className="flex flex-wrap items-center gap-3 border-b border-success/30 bg-success/10 px-4 py-3">
            <CheckCircle2 className="size-4 shrink-0 text-success" />
            <div className="min-w-0 flex-1">
              <p className="text-[11px] font-semibold text-foreground">{t('teamPlatform.blueprint.nextStep.revisionCreated')}</p>
              <p className="mt-0.5 text-[9px] text-muted-foreground">{t('teamPlatform.blueprint.nextStep.submitDetail', { number: appliedProposalRevision.revisionNumber.toLocaleString(locale) })}</p>
              {!appliedRevisionSubmissionNode && (
                <p className="mt-1 text-[9px] text-warning">
                  {t('teamPlatform.blueprint.nextStep.exactNodeUnavailable')}
                </p>
              )}
            </div>
            <button type="button" onClick={() => setTab('review')} disabled={navigationLocked || !currentDetails} className="rounded-md border border-success/40 px-3 py-2 text-[10px] font-semibold text-success disabled:opacity-40">{t('teamPlatform.blueprint.nextStep.requestReview')}</button>
            <button
              type="button"
              onClick={() => void submitAppliedProposalRevision()}
              disabled={
                navigationLocked
                || flow.busy
                || !appliedRevisionSubmissionNode
                || appliedRevisionSubmissionNodes.length !== 1
              }
              className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-40"
            >
              <Send className="size-3.5" />
              {t('teamPlatform.blueprint.nextStep.returnWorkbench')}
            </button>
          </div>
        )}

        <nav className="flex overflow-x-auto border-b border-border bg-panel p-1 scrollbar-thin">
          {([
            ['canvas', t('teamPlatform.blueprint.tab.graph', { count: nodes.length.toLocaleString(locale) })],
            ['pages', t('teamPlatform.blueprint.tab.pageSpecs', { count: pageSpecs.length.toLocaleString(locale) })],
            ['versions', t('teamPlatform.editor.tab.versions', { count: (currentDetails?.versions.length ?? 0).toLocaleString(locale) })],
            ['proposal', t('teamPlatform.editor.tab.proposals', { count: proposals.length.toLocaleString(locale) })],
            ['impact', t('teamPlatform.blueprint.tab.impact')],
            ['trace', t('teamPlatform.editor.tab.trace', { count: workspace.traces.length.toLocaleString(locale) })],
            ['review', t('teamPlatform.editor.tab.review', { count: (comments.length + reviews.length).toLocaleString(locale) })],
          ] as const).map(([id, label]) => <button key={id} type="button" onClick={() => setTab(id)} className={cn('shrink-0 rounded px-3 py-1.5 text-[10px] font-medium', tab === id ? 'bg-primary/15 text-primary-bright' : 'text-muted-foreground')}>{label}</button>)}
        </nav>

        <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
          {tab === 'canvas' && (
            <div className="grid min-h-full xl:grid-cols-[1fr_290px]">
              <section className="min-w-0 p-4">
                <div className="mb-3 rounded-lg border border-border bg-panel p-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="mr-1 inline-flex items-center gap-1 text-[9px] font-semibold uppercase tracking-wide text-faint-foreground"><Boxes className="size-3" />{t('blueprint.library')}</span>
                    {BLUEPRINT_MODULE_TEMPLATES.map((template) => <button key={template.id} type="button" draggable={!readOnly} title={moduleTemplateDescription(template.id, t)} onDragStart={(event) => event.dataTransfer.setData('application/x-blueprint-module', template.id)} onClick={() => addModule(template)} disabled={readOnly} className="rounded border border-border bg-background px-2 py-1 text-[9px] font-medium text-foreground hover:border-primary/50 disabled:opacity-40">{moduleTemplateLabel(template.id, t)}</button>)}
                    <button type="button" onClick={groupSelection} disabled={readOnly || selectedNodeIds.length === 0} className="ml-auto inline-flex items-center gap-1 rounded border border-primary/40 px-2 py-1 text-[9px] font-semibold text-primary-bright disabled:opacity-40"><Group className="size-3" />{t('teamPlatform.blueprint.groupSelection', { count: selectedNodeIds.length.toLocaleString(locale) })}</button>
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-2 border-t border-border pt-2">
                    <span className="text-[9px] text-faint-foreground">{t('teamPlatform.blueprint.approvedSelectionHint')}</span>
                    <button type="button" onClick={() => void generateDocumentsFromSelection()} disabled={readOnly || selectedNodeIds.length === 0 || saving} className="rounded bg-primary/15 px-2 py-1 text-[9px] font-semibold text-primary-bright disabled:opacity-40">{t('teamPlatform.blueprint.generateDocsSelection')}</button>
                    <button type="button" onClick={() => void createPrototypesFromSelection()} disabled={readOnly || selectedNodeIds.length === 0 || saving} className="rounded bg-primary/15 px-2 py-1 text-[9px] font-semibold text-primary-bright disabled:opacity-40">{t('teamPlatform.blueprint.createPrototypesSelection')}</button>
                    <button type="button" onClick={() => void useSelectionInWorkbench()} disabled={readOnly || selectedNodeIds.length === 0 || saving} className="inline-flex items-center gap-1 rounded bg-primary px-2 py-1 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Play className="size-3" />{t('teamPlatform.blueprint.useSelectionWorkbench')}</button>
                  </div>
                  {layout.groups.length > 0 && <div className="mt-2 space-y-1 border-t border-border pt-2">{layout.groups.map((group) => <div key={group.id} className="flex items-center gap-1.5 rounded border border-border bg-background p-1">
                    <button type="button" aria-label={t('teamPlatform.blueprint.selectCapability', { name: group.title })} onClick={() => selectCapabilityGroup(group)} className="inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-1 text-[9px] font-semibold text-primary-bright hover:bg-primary/10"><Group className="size-3" />{t('teamPlatform.blueprint.nodesCount', { count: group.nodeIds.length.toLocaleString(locale) })}</button>
                    <input aria-label={t('teamPlatform.blueprint.capabilityName', { name: group.title })} value={group.title} readOnly={readOnly} onChange={(event) => renameCapabilityGroup(group.id, event.target.value)} className="h-7 min-w-0 flex-1 rounded border border-border bg-panel px-2 text-[9px] text-foreground" />
                    <button type="button" aria-label={t('teamPlatform.blueprint.ungroupCapability', { name: group.title })} title={t('teamPlatform.blueprint.ungroupCapability', { name: group.title })} onClick={() => ungroupCapability(group.id)} disabled={readOnly} className="rounded p-1 text-destructive disabled:opacity-40"><Trash2 className="size-3" /></button>
                  </div>)}</div>}
                </div>
                <div className="mb-3 flex flex-wrap items-center gap-2">
                  <select onChange={(event) => addNode(event.target.value as BlueprintNodeKind)} value="" disabled={readOnly} aria-label={t('teamPlatform.blueprint.addSemanticNode')} className="h-8 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground disabled:opacity-50"><option value="" disabled>{t('teamPlatform.blueprint.addSemanticNode')}</option>{NODE_KINDS.map((kind) => <option key={kind} value={kind}>{nodeKindLabel(kind, t)}</option>)}</select>
                  <select value={edgeSourceId} onChange={(event) => setEdgeSourceId(event.target.value)} aria-label={t('common.source')} className="h-8 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground"><option value="">{t('common.source')}</option>{nodes.map((node) => <option key={node.id} value={node.id}>{node.title}</option>)}</select>
                  <select value={edgeKind} onChange={(event) => setEdgeKind(event.target.value as BlueprintEdgeKind)} aria-label={t('blueprint.edgeType')} className="h-8 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground">{EDGE_KINDS.map((kind) => <option key={kind} value={kind}>{edgeKindLabel(kind, t)}</option>)}</select>
                  <select value={edgeTargetId} onChange={(event) => setEdgeTargetId(event.target.value)} aria-label={t('common.target')} className="h-8 rounded-md border border-border bg-panel px-2 text-[10px] text-foreground"><option value="">{t('common.target')}</option>{nodes.map((node) => <option key={node.id} value={node.id}>{node.title}</option>)}</select>
                  <button type="button" onClick={addEdge} disabled={readOnly || !edgeSourceId || !edgeTargetId || edgeSourceId === edgeTargetId} className="h-8 rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-40"><Link2 className="mr-1 inline size-3" />{t('common.connect')}</button>
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
                      return <div key={group.id} className="pointer-events-none absolute rounded-xl border border-dashed border-primary/45 bg-primary/5" style={{ left: bounds.left, top: bounds.top, width: bounds.width, height: bounds.height }}><button type="button" aria-label={t('teamPlatform.blueprint.selectCapabilityCanvas', { name: group.title })} onClick={() => selectCapabilityGroup(group)} className="pointer-events-auto absolute -top-5 left-1 rounded bg-panel px-1.5 py-0.5 text-[8px] font-semibold text-primary-bright">{group.title}</button></div>
                    })}
                    <svg aria-hidden className="pointer-events-none absolute inset-0 size-full">
                      {edges.map((edge) => {
                        const source = layout.nodePositions[edge.sourceNodeId]
                        const target = layout.nodePositions[edge.targetNodeId]
                        if (!source || !target) return null
                        return <g key={edge.id}><line x1={source.x + NODE_WIDTH / 2} y1={source.y + NODE_HEIGHT / 2} x2={target.x + NODE_WIDTH / 2} y2={target.y + NODE_HEIGHT / 2} stroke="rgb(129 140 248 / 0.55)" strokeWidth="1.5" /><text x={(source.x + target.x + NODE_WIDTH) / 2} y={(source.y + target.y + NODE_HEIGHT) / 2 - 5} fill="rgb(148 163 184)" fontSize="9">{edgeKindLabel(edge.kind, t)}</text></g>
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
                      ><span className="block truncate text-[9px] uppercase tracking-wide text-faint-foreground">{nodeKindLabel(node.kind, t)}</span><span className="block truncate text-[11px] font-semibold text-foreground">{node.title}</span></button>
                    })}
                  </div>
                </div>
                <div className="mt-3 space-y-1">
                  {edges.map((edge) => <div key={edge.id} className="flex items-center gap-2 rounded border border-border bg-panel px-2 py-1.5 text-[9px] text-muted-foreground"><GitFork className="size-3 text-primary-bright" /><code>{edge.sourceNodeId}</code><b>{edgeKindLabel(edge.kind, t)}</b><code>{edge.targetNodeId}</code><label className="ml-auto flex items-center gap-1"><input type="checkbox" checked={edge.required} disabled={readOnly} onChange={(event) => mutateBlueprint((_nodes, currentEdges) => ({ edges: currentEdges.map((item) => item.id === edge.id ? { ...item, required: event.target.checked } : item) }))} />{t('blueprint.required')}</label><button type="button" disabled={readOnly} onClick={() => mutateBlueprint((_nodes, currentEdges) => ({ edges: currentEdges.filter((item) => item.id !== edge.id) }))} aria-label={t('blueprint.deleteEdge', { id: edge.id })} title={t('blueprint.deleteEdge', { id: edge.id })}><Trash2 className="size-3 text-destructive" /></button></div>)}
                </div>
              </section>

              <aside className="border-l border-border bg-panel p-4 max-xl:border-l-0 max-xl:border-t">
                {!selectedNode ? <p className="rounded border border-dashed border-border p-4 text-[10px] text-faint-foreground">{t('teamPlatform.blueprint.selectSemanticNode')}</p> : <NodeInspector node={selectedNode} position={layout.nodePositions[selectedNode.id] ?? { x: 0, y: 0 }} readOnly={readOnly} onChange={(patch) => updateNode(selectedNode.id, patch)} onMove={(x, y) => moveNode(selectedNode.id, x, y)} onDelete={() => deleteNode(selectedNode.id)} />}
              </aside>
            </div>
          )}

          {tab === 'pages' && <PageSpecsPanel selectedPageSpecId={selectedPageSpecId} onSelectedPageSpecId={setSelectedPageSpecId} nodes={nodes} pageSpecs={workspace.pageSpecs} hasRevision={Boolean(resource.approvedRevision)} readOnly={readOnly} onCreate={async (node, route, userGoal) => {
            try {
              const artifactId = await workspace.createPageSpec(resource.artifact.id, node.id, `${node.title} PageSpec`, route, userGoal)
              if (artifactId) updateNode(node.id, { pageSpecArtifactId: artifactId })
              return artifactId
            } catch (error) {
              setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed')))
              return null
            }
          }} />}

          {tab === 'versions' && <section className="mx-auto max-w-4xl space-y-3 p-5"><GatePanel clientIssues={clientGate} serverGate={currentDetails?.reviewGate} /><button type="button" onClick={() => void createRevision()} disabled={readOnly || !revisionReady || dirty || saving || conflict} className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-2 text-[11px] font-semibold text-primary-foreground disabled:opacity-50"><GitBranch className="size-3.5" />{t('teamPlatform.editor.createRevision')}</button>{currentDetails?.versions.map((version) => <div key={version.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><FileClock className="size-4 text-primary-bright" /><span className="text-[11px] font-medium text-foreground">{t('teamPlatform.editor.revisionNumber', { number: version.revisionNumber.toLocaleString(locale) })}</span><code className="ml-auto text-[9px] text-faint-foreground">{version.contentHash.slice(0, 16)}</code></div><p className="mt-1 text-[10px] text-muted-foreground">{formatDate(version.createdAt, locale)} · {t('teamPlatform.blueprint.pinnedRequirements', { count: (version.sourceVersions?.length ?? 0).toLocaleString(locale) })}</p></div>)}</section>}

          {tab === 'proposal' && <ProposalPanel proposals={proposals} selected={selectedOperations} onSelected={setSelectedOperations} instruction={proposalInstruction} onInstruction={setProposalInstruction} canEdit={!readOnly && !dirty && !saving && !conflict} canCreate={Boolean(latestVersion) && !dirty && !saving && !conflict} onCreate={() => void workspace.createProposal({ jobType: 'blueprint.patch', targetRevision: latestVersion!, instruction: proposalInstruction, inputVersions: resource.draft?.sourceVersions ?? [], outputSchemaVersion: 'blueprint.patch.v1' }).catch((error) => setLocalError(errorMessage(error, t('teamPlatform.blueprint.operationFailed'))))} onApply={(proposal) => void applyProposal(proposal)} />}

          {tab === 'impact' && <section className="mx-auto max-w-4xl space-y-3 p-5"><div className="flex items-center justify-between gap-3"><div><h2 className="text-sm font-semibold text-foreground">{t('teamPlatform.blueprint.impactTitle')}</h2><p className="mt-1 text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.impactDetail')}</p></div><button type="button" onClick={() => void loadImpact()} className="rounded-md bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground"><RefreshCw className="mr-1 inline size-3" />{t('teamPlatform.blueprint.analyzeImpact')}</button></div>{resource.artifact.status === 'needsSync' && <div className="rounded-md border border-warning/30 bg-warning/10 p-3 text-[10px] text-warning"><AlertTriangle className="mr-1 inline size-3" />{t('teamPlatform.blueprint.serverNeedsSync')}</div>}{impact?.items.length === 0 && <p className="rounded border border-dashed border-border p-4 text-[10px] text-faint-foreground">{t('teamPlatform.blueprint.noImpact')}</p>}{impact?.items.map((item, index) => <div key={`${item.targetArtifactId}-${index}`} className={cn('rounded-md border bg-panel p-3 text-[10px]', item.needsSync ? 'border-warning/40' : 'border-border')}><div className="flex items-center gap-2"><code className="text-primary-bright">{item.targetKind}:{item.targetArtifactId}</code><span className="ml-auto rounded bg-white/5 px-1.5 py-0.5">{impactSeverityLabel(item.severity, t)}</span>{item.needsSync && <span className="rounded bg-warning/10 px-1.5 py-0.5 text-warning">{t('doc.status.needsSync')}</span>}</div><p className="mt-1 text-muted-foreground">{item.reason}</p><p className="mt-1 text-faint-foreground">{t('teamPlatform.blueprint.impactSource', { id: item.source.artifactId, revision: item.source.revisionNumber?.toLocaleString(locale) ?? t('teamPlatform.common.unknown'), hash: item.source.contentHash.slice(0, 12) })}</p></div>)}</section>}

          {tab === 'trace' && <section className="mx-auto max-w-4xl space-y-2 p-5">{currentDetails?.dependencies.map((dependency) => <div key={dependency.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><Link2 className="mr-2 inline size-3.5 text-primary-bright" />{dependency.source.artifactId}{dependency.source.revisionNumber ? `@${dependency.source.revisionNumber.toLocaleString(locale)}` : ''} <b>{relationLabel(dependency.relation, t)}</b> {dependency.target.artifactId}{dependency.target.revisionNumber ? `@${dependency.target.revisionNumber.toLocaleString(locale)}` : ''}{dependency.required && <span className="ml-2 text-warning">{t('blueprint.required')}</span>}</div>)}{workspace.traces.filter((trace) => trace.source.artifactId === resource.artifact.id || trace.target.artifactId === resource.artifact.id).map((trace) => <div key={trace.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><code>{trace.source.artifactId}:{trace.source.revisionId}</code> → {relationLabel(trace.relation, t)} → <code>{trace.target.artifactId}:{trace.target.revisionId}</code></div>)}</section>}

          {tab === 'review' && <section className="mx-auto max-w-4xl space-y-3 p-5"><GatePanel clientIssues={clientGate} serverGate={currentDetails?.reviewGate} />{!latestVersion && <p className="rounded-md border border-dashed border-border p-4 text-[10px] text-faint-foreground">{t('teamPlatform.editor.createRevisionFirst')}</p>}{latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_auto]"><input value={comment} onChange={(event) => setComment(event.target.value)} placeholder={t('teamPlatform.blueprint.commentPlaceholder')} aria-label={t('teamPlatform.blueprint.commentPlaceholder')} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><button type="button" onClick={() => void collaboration.addComment(comment, undefined, latestVersion).then((ok) => ok && setComment(''))} disabled={!comment.trim() || !collaboration.can('comment') || saving || actionBusy} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><MessageSquare className="mr-1 inline size-3" />{t('teamPlatform.editor.comment')}</button></div>}{latestVersion && <div className="grid gap-2 sm:grid-cols-[1fr_180px_auto]"><input value={reviewSummary} onChange={(event) => setReviewSummary(event.target.value)} aria-label={t('teamPlatform.reviews.summary')} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground" /><select value={effectiveReviewerId} onChange={(event) => setReviewerId(event.target.value)} disabled={collaboration.project?.governanceMode === 'solo' || saving || actionBusy} aria-label={t('reviews.reviewer')} className="h-9 rounded-md border border-border bg-background px-2 text-[10px] text-foreground disabled:opacity-75"><option value="">{t('reviews.reviewer')}</option>{governanceReviewers.map((member) => <option key={member.user.id} value={member.user.id}>{member.user.name}</option>)}</select><button type="button" onClick={() => void collaboration.requestReview(reviewSummary, latestVersion, [effectiveReviewerId])} disabled={!gatePassed || !effectiveReviewerId || !reviewSummary.trim() || dirty || saving || actionBusy || conflict || !currentDetails} className="rounded-md bg-primary px-3 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><Send className="mr-1 inline size-3" />{t('editor.requestReview')}</button></div>}{comments.map((thread) => <div key={thread.id} className="rounded-md border border-border bg-panel p-3"><span className="text-[10px] font-medium text-foreground">{thread.author.name}</span><p className="mt-1 text-[10px] text-muted-foreground">{thread.body}</p><p className="mt-1 text-[9px] text-faint-foreground">{t('teamPlatform.blueprint.pinnedToRevision', { number: thread.target?.revisionNumber?.toLocaleString(locale) ?? t('teamPlatform.common.unknown') })}</p></div>)}{reviews.map((review) => <div key={review.id} className="rounded-md border border-border bg-panel p-3 text-[10px]"><span className="font-medium text-foreground">{reviewStateLabel(review.state ?? 'pending', t)}</span><span className="ml-2 text-muted-foreground">{review.summary}</span><span className="ml-2 text-faint-foreground">{t('teamPlatform.blueprint.revisionNumber', { number: review.target?.revisionNumber?.toLocaleString(locale) ?? t('teamPlatform.common.unknown') })}</span></div>)}</section>}
        </div>
      </main>
    </div>
  )
}

function NodeInspector({ node, position, readOnly, onChange, onMove, onDelete }: { node: SemanticNode; position: { readonly x: number; readonly y: number }; readOnly: boolean; onChange: (patch: Partial<SemanticNode>) => void; onMove: (x: number, y: number) => void; onDelete: () => void }) {
  const { t } = useI18n()
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <Move className="size-4 text-primary-bright" />
        <h2 className="text-xs font-semibold text-foreground">{t('teamPlatform.blueprint.semanticNode')}</h2>
        <button type="button" onClick={onDelete} disabled={readOnly} className="ml-auto text-destructive disabled:opacity-40" aria-label={t('teamPlatform.blueprint.deleteNode', { title: node.title })} title={t('teamPlatform.blueprint.deleteNode', { title: node.title })}><Trash2 className="size-4" /></button>
      </div>
      <code className="block truncate text-[9px] text-faint-foreground">{node.id}</code>
      <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.stableBusinessKey')}<input value={node.key} readOnly={readOnly} onChange={(event) => onChange({ key: event.target.value })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground" /></label>
      <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.kind')}<select value={node.kind} disabled={readOnly} onChange={(event) => onChange({ kind: event.target.value as BlueprintNodeKind })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground">{!NODE_KINDS.includes(node.kind) && <option value={node.kind}>{t('teamPlatform.blueprint.legacyKind', { kind: node.kind })}</option>}{NODE_KINDS.map((kind) => <option key={kind} value={kind}>{nodeKindLabel(kind, t)}</option>)}</select></label>
      <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.nodeTitle')}<input value={node.title} readOnly={readOnly} onChange={(event) => onChange({ title: event.target.value })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground" /></label>
      <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.description')}<textarea value={node.description ?? ''} readOnly={readOnly} onChange={(event) => onChange({ description: event.target.value })} rows={4} className="mt-1 w-full rounded border border-border bg-background p-2 text-[10px] text-foreground" /></label>
      {node.kind === 'page' && <>
        <label className="block text-[10px] text-muted-foreground">{t('prototype.route')}<input value={node.route ?? ''} readOnly={readOnly} onChange={(event) => onChange({ route: event.target.value })} placeholder="/orders" className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground" /></label>
        <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.userGoal')}<textarea value={node.userGoal ?? ''} readOnly={readOnly} onChange={(event) => onChange({ userGoal: event.target.value })} rows={3} className="mt-1 w-full rounded border border-border bg-background p-2 text-[10px] text-foreground" /></label>
      </>}
      {(node.kind === 'apiOperation' || node.kind === 'api') && <>
        <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.httpMethod')}<select value={node.method?.toUpperCase() ?? ''} disabled={readOnly} onChange={(event) => onChange({ method: event.target.value })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground"><option value="">{t('teamPlatform.blueprint.selectMethod')}</option>{HTTP_METHODS.map((method) => <option key={method} value={method}>{method}</option>)}</select></label>
        <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.apiPath')}<input value={node.path ?? ''} readOnly={readOnly} onChange={(event) => onChange({ path: event.target.value })} placeholder="/orders" className="mt-1 h-8 w-full rounded border border-border bg-background px-2 font-mono text-[10px] text-foreground" /></label>
      </>}
      {node.kind === 'permission' && <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.roles')}<input value={(node.roles ?? []).join(', ')} readOnly={readOnly} onChange={(event) => onChange({ roles: commaList(event.target.value) })} placeholder="admin, editor" className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground" /></label>}
      <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.stableRequirementIds')}<input value={node.requirementIds.join(', ')} readOnly={readOnly} onChange={(event) => onChange({ requirementIds: commaList(event.target.value) })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground" /></label>
      <label className="block text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.assignedMemberIds')}<input value={node.assignedMemberIds.join(', ')} readOnly={readOnly} onChange={(event) => onChange({ assignedMemberIds: commaList(event.target.value) })} className="mt-1 h-8 w-full rounded border border-border bg-background px-2 text-[10px] text-foreground" /></label>
      <div className="rounded border border-border bg-background p-2">
        <span className="text-[9px] font-medium uppercase text-faint-foreground">{t('teamPlatform.blueprint.layoutOnly')}</span>
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
  const { locale, t } = useI18n()
  const pages = nodes.filter((node) => node.kind === 'page')
  if (selectedPageSpecId && pageSpecs.some((item) => item.artifact.id === selectedPageSpecId)) {
    return <section className="p-5"><PageSpecEditor artifactId={selectedPageSpecId} onBack={() => onSelectedPageSpecId('')} /></section>
  }
  return <section className="mx-auto max-w-4xl space-y-3 p-5"><div><h2 className="text-sm font-semibold text-foreground">{t('teamPlatform.blueprint.pageSpecsTitle')}</h2><p className="mt-1 text-[10px] text-muted-foreground">{t('teamPlatform.blueprint.pageSpecsDetail')}</p></div>{!hasRevision && <p className="rounded-md border border-warning/30 bg-warning/10 p-3 text-[10px] text-warning">{t('teamPlatform.blueprint.pageSpecsRequireApproval')}</p>}{pages.length === 0 && <p className="rounded border border-dashed border-border p-4 text-[10px] text-faint-foreground">{t('teamPlatform.blueprint.noPageNodes')}</p>}{pages.map((node) => {
    const existing = pageSpecs.find((item) => (item.draft?.content ?? item.latestRevision?.content)?.blueprintPageNodeId === node.id)
    const route = node.route?.trim() || `/${slug(node.title)}`
    const userGoal = node.userGoal?.trim() || node.description?.trim() || t('teamPlatform.blueprint.completePage', { title: node.title })
    return <div key={node.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex flex-wrap items-center gap-2"><span className="text-[11px] font-semibold text-foreground">{node.title}</span><code className="text-[9px] text-faint-foreground">{node.id}</code>{existing ? <><span className={cn('ml-auto rounded px-2 py-1 text-[9px]', existing.artifact.status === 'approved' ? 'bg-success/10 text-success' : 'bg-primary/10 text-primary-bright')}><CheckCircle2 className="mr-1 inline size-3" />{artifactStatusLabel(existing.artifact.status, t)} · r{(existing.latestRevision?.revisionNumber ?? 0).toLocaleString(locale)}</span><button type="button" onClick={() => onSelectedPageSpecId(existing.artifact.id)} className="rounded border border-primary/40 px-2.5 py-1.5 text-[9px] font-semibold text-primary-bright">{t('teamPlatform.dashboard.openEditor')}</button></> : <button type="button" onClick={() => void onCreate(node, route, userGoal).then((artifactId) => artifactId && onSelectedPageSpecId(artifactId))} disabled={readOnly || !hasRevision} className="ml-auto rounded bg-primary px-2.5 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-40"><Plus className="mr-1 inline size-3" />{t('teamPlatform.blueprint.createRoute', { route })}</button>}</div><p className="mt-2 text-[10px] text-muted-foreground">{userGoal}</p></div>
  })}</section>
}

function GatePanel({ clientIssues, serverGate }: { clientIssues: string[]; serverGate?: ArtifactReviewGateDto }) {
  const { locale, t } = useI18n()
  const serverErrors = serverGate?.checks
    .filter((check) => check.severity === 'error' && check.code !== 'canonical_review_approved')
    ?? []
  const requestReady = clientIssues.length === 0
    && serverErrors.length === 0
    && reviewGateReadyForRequest(serverGate)
  const approved = Boolean(serverGate?.passed)
  const revisionIsBehindDraft = serverErrors.some((check) => check.code === 'draft_matches_latest_revision')
  return (
    <div className={cn('rounded-lg border p-3', approved || requestReady ? 'border-success/30 bg-success/10' : 'border-warning/30 bg-warning/10')}>
      <div className="flex items-center gap-2 text-[11px] font-semibold text-foreground">
        {approved || requestReady ? <CheckCircle2 className="size-4 text-success" /> : <AlertTriangle className="size-4 text-warning" />}
        {t('teamPlatform.blueprint.gateStatus', { status: approved ? t('doc.status.approved') : requestReady ? t('teamPlatform.blueprint.readyToRequest') : t('teamPlatform.graph.status.blocked') })}
      </div>
      {clientIssues.length > 0 && (
        <div className="mt-2">
          <p className="text-[9px] font-semibold uppercase text-faint-foreground">{t('teamPlatform.blueprint.currentDraftIssues')}</p>
          {clientIssues.map((issue) => <p key={issue} className="mt-1 text-[10px] text-muted-foreground">• {blueprintIssueLabel(issue, t)}</p>)}
        </div>
      )}
      {serverErrors.length > 0 && (
        <div className="mt-2">
          <p className="text-[9px] font-semibold uppercase text-faint-foreground">{t('teamPlatform.blueprint.latestRevisionIssues')}</p>
          {revisionIsBehindDraft && <p className="mt-1 text-[9px] text-warning">{t('teamPlatform.blueprint.latestRevisionBehindDraft')}</p>}
          {serverErrors.map((check, index) => <p key={`${check.code}:${check.path ?? ''}:${check.sourceId ?? ''}:${index}`} className="mt-1 text-[10px] text-muted-foreground">• {blueprintIssueLabel(check.message, t)}</p>)}
        </div>
      )}
      {requestReady && !approved && <p className="mt-1 text-[10px] text-success">{t('teamPlatform.editor.gateChecksPassed')}</p>}
      {serverGate && <p className="mt-2 text-[9px] text-faint-foreground">{t('teamPlatform.blueprint.traceCoverage', { percent: new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(serverGate.traceCoverage * 100), count: serverGate.unresolvedBlockingCommentIds.length.toLocaleString(locale) })}</p>}
    </div>
  )
}

function ProposalPanel({ proposals, selected, onSelected, instruction, onInstruction, canEdit, canCreate, onCreate, onApply }: { proposals: readonly ProposalDto[]; selected: Record<string, string[]>; onSelected: (next: Record<string, string[]>) => void; instruction: string; onInstruction: (value: string) => void; canEdit: boolean; canCreate: boolean; onCreate: () => void; onApply: (proposal: ProposalDto) => void }) {
  const { t } = useI18n()
  return <section className="mx-auto max-w-4xl space-y-3 p-5"><div className="rounded-lg border border-border bg-panel p-3"><textarea value={instruction} onChange={(event) => onInstruction(event.target.value)} rows={3} aria-label={t('teamPlatform.editor.proposalInstruction')} className="w-full rounded border border-border bg-background p-2 text-[11px] text-foreground" /><button type="button" onClick={onCreate} disabled={!canEdit || !canCreate || !instruction.trim()} className="mt-2 rounded bg-primary px-3 py-2 text-[10px] font-semibold text-primary-foreground disabled:opacity-50"><Bot className="mr-1 inline size-3" />{t('teamPlatform.editor.askAi')}</button>{!canCreate && <p className="mt-2 text-[9px] text-warning">{t('teamPlatform.blueprint.revisionBeforeAi')}</p>}</div>{proposals.map((proposal) => { const selectedIds = selected[proposal.id] ?? []; const hasAccepted = proposal.operations.some((operation) => operation.decision === 'accepted' || selectedIds.includes(operation.id)); return <div key={proposal.id} className="rounded-lg border border-border bg-panel p-3"><div className="flex items-center gap-2"><span className="text-[11px] font-semibold text-foreground">{t('teamPlatform.editor.manifestId', { id: proposal.manifest.id.slice(0, 12) })}</span><span className="rounded bg-primary/10 px-1.5 py-0.5 text-[9px] text-primary-bright">{proposalStatusLabel(proposal.status, t)}</span><code className="ml-auto text-[9px] text-faint-foreground">{t('teamPlatform.editor.baseHash', { hash: proposal.baseRevision.contentHash.slice(0, 12) })}</code></div><div className="mt-2 space-y-1">{proposal.operations.map((operation) => <label key={operation.id} className="flex gap-2 rounded border border-border bg-background p-2 text-[9px] text-muted-foreground"><input type="checkbox" disabled={operation.decision !== 'pending'} checked={operation.decision === 'accepted' || operation.decision === 'applied' || selectedIds.includes(operation.id)} onChange={(event) => onSelected({ ...selected, [proposal.id]: event.target.checked ? [...selectedIds, operation.id] : selectedIds.filter((item) => item !== operation.id) })} /><span className="min-w-0 flex-1"><code>{proposalOperationLabel(operation.kind, t)} {operation.path || '/'}</code><span className="ml-2 text-faint-foreground">{proposalDecisionLabel(operation.decision, t)}</span>{operation.rationale && <span className="mt-1 block">{operation.rationale}</span>}</span></label>)}</div><button type="button" onClick={() => onApply(proposal)} disabled={!canEdit || !hasAccepted || !['open', 'reviewing', 'ready'].includes(proposal.status)} className="mt-2 rounded bg-primary px-2.5 py-1.5 text-[9px] font-semibold text-primary-foreground disabled:opacity-50">{t('teamPlatform.editor.applyAccepted')}</button></div>})}</section>
}

function Unavailable({ title, detail, loading, action, onAction, onRetry, retryLabel }: { title: string; detail: string; loading?: boolean; action?: string; onAction?: () => void; onRetry?: () => Promise<void>; retryLabel?: string }) {
  return <div className="flex h-full items-center justify-center bg-canvas p-6 text-center"><div className="max-w-md rounded-lg border border-dashed border-border bg-panel p-6">{loading ? <Loader2 className="mx-auto size-7 animate-spin text-primary-bright" /> : <AlertTriangle className="mx-auto size-7 text-warning" />}<h1 className="mt-3 text-base font-semibold text-foreground">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{detail}</p>{action && onAction && <button type="button" onClick={onAction} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground">{action}</button>}{onRetry && <button type="button" onClick={() => void onRetry()} className="mt-4 rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground"><RefreshCw className="mr-1 inline size-4" />{retryLabel}</button>}</div></div>
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

type Translate = ReturnType<typeof useI18n>['t']

function nodeKindLabel(kind: string, t: Translate) {
  const labels: Record<string, string> = {
    feature: t('blueprint.node.feature'),
    page: t('blueprint.node.page'),
    component: t('blueprint.node.component'),
    apiOperation: t('blueprint.node.api'),
    api: t('blueprint.node.api'),
    dataEntity: t('blueprint.node.dataModel'),
    dataModel: t('blueprint.node.dataModel'),
    permission: t('blueprint.node.permission'),
    prototype: t('blueprint.node.prototype'),
    workbenchTarget: t('blueprint.node.workbenchTarget'),
  }
  return labels[kind] ?? kind
}

function edgeKindLabel(kind: string, t: Translate) {
  const labels: Record<string, string> = {
    drives: t('teamPlatform.graph.relation.drives'),
    satisfied_by: t('teamPlatform.graph.relation.satisfiedBy'),
    contains: t('blueprint.edge.contains'),
    navigates_to: t('teamPlatform.graph.relation.navigatesTo'),
    uses: t('blueprint.edge.uses'),
    calls: t('blueprint.edge.calls'),
    reads: t('blueprint.edge.reads'),
    writes: t('blueprint.edge.writes'),
    requires: t('blueprint.edge.requires'),
    realized_by: t('teamPlatform.graph.relation.realizedBy'),
    implemented_by: t('blueprint.edge.implemented_by'),
    verified_by: t('teamPlatform.graph.relation.verifiedBy'),
    derives_from: t('dep.derives_from'),
  }
  return labels[kind] ?? kind
}

function relationLabel(value: string, t: Translate) {
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
    ...Object.fromEntries(EDGE_KINDS.map((kind) => [kind, edgeKindLabel(kind, t)])),
  }
  return labels[value] ?? value
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

function reviewStateLabel(state: string, t: Translate) {
  const labels: Record<string, string> = {
    pending: t('teamPlatform.reviews.state.pending'),
    approved: t('doc.status.approved'),
    changesRequested: t('doc.status.changesRequested'),
  }
  return labels[state] ?? state
}

function impactSeverityLabel(severity: string, t: Translate) {
  const labels: Record<string, string> = {
    info: t('teamPlatform.blueprint.severity.info'),
    warning: t('teamPlatform.blueprint.severity.warning'),
    blocking: t('teamPlatform.blueprint.severity.blocking'),
  }
  return labels[severity] ?? severity
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

function moduleTemplateLabel(id: BlueprintModuleTemplate['id'], t: Translate) {
  const labels: Record<BlueprintModuleTemplate['id'], string> = {
    feature: t('blueprint.node.feature'),
    page: t('blueprint.node.page'),
    api: t('blueprint.node.api'),
    data: t('teamPlatform.graph.kind.data'),
    permission: t('blueprint.node.permission'),
    ui: t('teamPlatform.blueprint.module.ui'),
  }
  return labels[id]
}

function moduleTemplateDescription(id: BlueprintModuleTemplate['id'], t: Translate) {
  return t(`teamPlatform.blueprint.module.${id}.description` as Parameters<Translate>[0])
}

function localizedModuleTemplate(template: BlueprintModuleTemplate, t: Translate): BlueprintModuleTemplate {
  return {
    ...template,
    label: moduleTemplateLabel(template.id, t),
    description: moduleTemplateDescription(template.id, t),
    nodes: template.nodes.map((node) => ({
      ...node,
      title: t(`teamPlatform.blueprint.module.${template.id}.${node.localId}.title` as Parameters<Translate>[0]),
    })),
  }
}

function blueprintIssueLabel(issue: string, t: Translate) {
  const labels: Record<string, string> = {
    'At least one semantic node is required.': t('teamPlatform.blueprint.issue.nodeRequired'),
    'At least one semantic Page node is required.': t('teamPlatform.blueprint.issue.pageRequired'),
    'Every node needs a stable ID, business key, and title.': t('teamPlatform.blueprint.issue.nodeIdentity'),
    'Every node business key must be unique.': t('teamPlatform.blueprint.issue.uniqueKey'),
    'Every semantic node needs a separate layout position.': t('teamPlatform.blueprint.issue.layoutPosition'),
    'Every edge must reference existing semantic nodes.': t('teamPlatform.blueprint.issue.edgeReferences'),
    'Every page node must trace to at least one stable requirement ID.': t('teamPlatform.blueprint.issue.pageRequirement'),
    'Every page node needs a route and user goal before review.': t('teamPlatform.blueprint.issue.pageRouteGoal'),
    'Every page node must belong to a feature through a contains edge.': t('teamPlatform.blueprint.issue.pageFeature'),
    'Every API operation needs a supported HTTP method and absolute path.': t('teamPlatform.blueprint.issue.apiMethodPath'),
    'API method/path pairs must be unique.': t('teamPlatform.blueprint.issue.uniqueApi'),
    'Every API operation must require a Permission node.': t('teamPlatform.blueprint.issue.apiPermission'),
  }
  return labels[issue] ?? issue
}

function formatDate(value: string, locale: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale)
}

function slug(value: string) {
  return value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'page'
}

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback
}

function artifactReference() {
  if (typeof window === 'undefined') return ''
  return new URLSearchParams(window.location.search).get('artifactId') ?? ''
}
