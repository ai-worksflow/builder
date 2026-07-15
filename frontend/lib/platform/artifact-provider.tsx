'use client'

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useCollaboration } from '../collaboration/provider'
import { collaborationErrorMessage } from '../collaboration/platform-adapter'
import { useI18n } from '../i18n'
import { useWorksflow } from '../worksflow/store'
import type {
  Blueprint,
  BlueprintEdgeType,
  BlueprintNodeType,
  BlueprintStatus,
  DocStatus,
  DocType,
  DocumentDependency,
  TeamDocument,
} from '../worksflow/types'
import {
  ArtifactWorkspaceGateway,
  approvedRequirementBaselineSources,
  artifactWorkspaceEventRequiresRefresh,
  createEmptyPageSpecContent,
  createEmptyPrototypeContent,
  replaceArtifactWorkspaceSnapshotResource,
  type ArtifactDetails,
  type CreateArtifactProposalInput,
  type ArtifactWorkspaceSnapshot,
  type ArtifactWorkspaceResourceCollection,
} from './artifact-workspace'
import type {
  ArtifactRevisionDto,
  ArtifactDraftDto,
  BlueprintContentDto,
  DocumentContentDto,
  ImpactReportDto,
  JsonValue,
  PageSpecContentDto,
  ProposalDto,
  PrototypeContentDto,
  VersionedArtifactDto,
} from './dto'

export { createEmptyPageSpecContent, createEmptyPrototypeContent } from './artifact-workspace'

export type ArtifactWorkspaceStatus = 'idle' | 'loading' | 'ready' | 'error'

interface ArtifactWorkspaceContextState extends ArtifactWorkspaceSnapshot {
  readonly status: ArtifactWorkspaceStatus
  readonly error: string | null
  readonly refresh: () => Promise<void>
  readonly createDocument: (title: string, kind?: DocumentContentDto['kind']) => Promise<string | null>
  readonly createBlueprint: (title: string) => Promise<string | null>
  readonly createPageSpec: (
    blueprintArtifactId: string,
    blueprintPageNodeId: string,
    title: string,
    route: string,
    userGoal: string,
  ) => Promise<string | null>
  readonly createPrototype: (
    pageSpecArtifactId: string,
    title: string,
    exploratory?: boolean,
  ) => Promise<string | null>
  readonly saveDocumentDraft: (
    artifactId: string,
    content: DocumentContentDto,
    etag: string,
  ) => ReturnType<ArtifactWorkspaceGateway['saveDocumentDraft']>
  readonly saveBlueprintDraft: (
    artifactId: string,
    content: BlueprintContentDto,
    etag: string,
  ) => ReturnType<ArtifactWorkspaceGateway['saveBlueprintDraft']>
  readonly savePageSpecDraft: (
    artifactId: string,
    content: PageSpecContentDto,
    etag: string,
  ) => ReturnType<ArtifactWorkspaceGateway['savePageSpecDraft']>
  readonly savePrototypeDraft: (
    artifactId: string,
    content: PrototypeContentDto,
    etag: string,
  ) => ReturnType<ArtifactWorkspaceGateway['savePrototypeDraft']>
  readonly createDocumentRevision: (
    artifactId: string,
    content: DocumentContentDto,
  ) => Promise<ArtifactRevisionDto<DocumentContentDto>>
  readonly createBlueprintRevision: (
    artifactId: string,
    content: BlueprintContentDto,
  ) => Promise<ArtifactRevisionDto<BlueprintContentDto>>
  readonly createPageSpecRevision: (
    artifactId: string,
    content: PageSpecContentDto,
  ) => Promise<ArtifactRevisionDto<PageSpecContentDto>>
  readonly createPrototypeRevision: (
    artifactId: string,
    content: PrototypeContentDto,
  ) => Promise<ArtifactRevisionDto<PrototypeContentDto>>
  readonly loadDetails: <TContent>(artifactId: string) => Promise<ArtifactDetails<TContent>>
  readonly createProposal: (input: CreateArtifactProposalInput) => Promise<ProposalDto>
  readonly applyProposal: (
    proposalId: string,
    acceptedOperationIds: readonly string[],
  ) => Promise<ArtifactDraftDto<JsonValue>>
  readonly decideProposalOperation: (
    proposal: Pick<ProposalDto, 'id' | 'version'>,
    operationId: string,
    decision: 'accepted' | 'rejected',
    reason?: string,
  ) => Promise<ProposalDto>
  readonly impact: (blueprintArtifactId: string) => Promise<ImpactReportDto>
}

const EMPTY_SNAPSHOT: ArtifactWorkspaceSnapshot = {
  documents: [],
  blueprints: [],
  pageSpecs: [],
  prototypes: [],
  proposals: [],
  traces: [],
}

const ArtifactWorkspaceContext = createContext<ArtifactWorkspaceContextState | null>(null)

export function ArtifactWorkspaceProvider({ children }: { children: ReactNode }) {
  const { t } = useI18n()
  const { session, project, platformClient } = useCollaboration()
  const {
    beginPlatformTeamFacts,
    applyPlatformTeamFacts,
    failPlatformTeamFacts,
  } = useWorksflow()
  const gateway = useMemo(() => new ArtifactWorkspaceGateway(platformClient), [platformClient])
  const [snapshot, setSnapshot] = useState<ArtifactWorkspaceSnapshot>(EMPTY_SNAPSHOT)
  const [status, setStatus] = useState<ArtifactWorkspaceStatus>('idle')
  const [error, setError] = useState<string | null>(null)
  const requestId = useRef(0)
  const refreshRef = useRef<() => Promise<void>>(async () => {})

  const refresh = useCallback(async () => {
    if (!session.signedIn || !project) {
      setSnapshot(EMPTY_SNAPSHOT)
      setStatus('idle')
      return
    }
    const currentRequest = ++requestId.current
    setStatus('loading')
    setError(null)
    beginPlatformTeamFacts(project.id)
    try {
      const next = await gateway.load(project.id)
      if (currentRequest !== requestId.current) return
      setSnapshot(next)
      setStatus('ready')
    } catch (cause) {
      if (currentRequest !== requestId.current) return
      const message = collaborationErrorMessage(cause, t('runtime.artifact.loadFailed'))
      setSnapshot(EMPTY_SNAPSHOT)
      setStatus('error')
      setError(message)
      failPlatformTeamFacts(project.id, message)
    }
  }, [
    beginPlatformTeamFacts,
    failPlatformTeamFacts,
    gateway,
    project,
    session.signedIn,
    t,
  ])
  refreshRef.current = refresh

  useEffect(() => {
    void refreshRef.current()
  }, [project?.id, session.signedIn])

  useEffect(() => {
    if (status !== 'ready' || !project) return
    applyPlatformTeamFacts(projectSnapshotAsLegacy(project.id, project.name, snapshot))
  }, [applyPlatformTeamFacts, project, snapshot, status])

  useEffect(() => {
    if (!session.signedIn || !project) return
    const unsubscribe = platformClient.websocket.subscribeProject(
      project.id,
      (event) => {
        if (artifactWorkspaceEventRequiresRefresh(event.type)) void refreshRef.current()
      },
      () => void refreshRef.current(),
    )
    platformClient.websocket.connect()
    return unsubscribe
  }, [platformClient.websocket, project, session.signedIn])

  const updateSnapshotResource = useCallback(<TContent,>(
    collection: ArtifactWorkspaceResourceCollection,
    artifactId: string,
    resource: VersionedArtifactDto<TContent>,
  ) => {
    setSnapshot((current) => replaceArtifactWorkspaceSnapshotResource(
      current,
      collection,
      artifactId,
      resource,
    ))
  }, [])

  const value = useMemo<ArtifactWorkspaceContextState>(() => ({
    ...snapshot,
    status,
    error,
    refresh,
    createDocument: async (title, kind = 'requirement') => {
      if (!project) return null
      const result = await gateway.createDocument(project.id, title, createEmptyDocumentContent(kind))
      await refreshRef.current()
      return result.data.artifact.id
    },
    createBlueprint: async (title) => {
      if (!project) return null
      const approvedSources = approvedRequirementBaselineSources(
        snapshot.documents,
        t('runtime.artifact.approveRequirements'),
      )
      const baselineResult = await gateway.compileRequirementBaseline(project.id, approvedSources)
      const baseline = baselineResult.data
      const requirementVersions = [{
        artifactId: baseline.artifactId,
        revisionId: baseline.id,
        revisionNumber: baseline.revisionNumber,
        contentHash: baseline.contentHash,
      }]
      const result = await gateway.createBlueprint(
        project.id,
        title,
        requirementVersions,
        createEmptyBlueprintContent(),
      )
      await refreshRef.current()
      return result.data.artifact.id
    },
    createPageSpec: async (blueprintArtifactId, blueprintPageNodeId, title, route, userGoal) => {
      if (!project) return null
      const blueprint = snapshot.blueprints.find((item) => item.artifact.id === blueprintArtifactId)
      const revision = blueprint?.approvedRevision
      if (!revision) {
        throw new Error(t('runtime.artifact.approveBlueprint'))
      }
      const result = await gateway.createPageSpec(
        project.id,
        title,
        {
          artifactId: revision.artifactId,
          revisionId: revision.id,
          revisionNumber: revision.revisionNumber,
          contentHash: revision.contentHash,
          anchorId: blueprintPageNodeId,
        },
        blueprintPageNodeId,
        createEmptyPageSpecContent(blueprintPageNodeId, title, route, userGoal),
      )
      await refreshRef.current()
      return result.data.artifact.id
    },
    createPrototype: async (pageSpecArtifactId, title, exploratory = false) => {
      if (!project) return null
      const pageSpec = snapshot.pageSpecs.find((item) => item.artifact.id === pageSpecArtifactId)
      const revision = exploratory
        ? pageSpec?.approvedRevision ?? pageSpec?.latestRevision
        : pageSpec?.approvedRevision
      if (!revision) {
        throw new Error(
          exploratory
            ? t('runtime.artifact.createPageSpecRevision')
            : t('runtime.artifact.approvePageSpec'),
        )
      }
      const reference = {
        artifactId: revision.artifactId,
        revisionId: revision.id,
        revisionNumber: revision.revisionNumber,
        contentHash: revision.contentHash,
      }
      const result = await gateway.createPrototype(
        project.id,
        title,
        reference,
        createEmptyPrototypeContent(reference, revision.content, exploratory),
      )
      await refreshRef.current()
      return result.data.artifact.id
    },
    saveDocumentDraft: async (artifactId, content, etag) => {
      const result = await gateway.saveDocumentDraft(artifactId, content, etag)
      updateSnapshotResource('documents', artifactId, result.data)
      return result
    },
    saveBlueprintDraft: async (artifactId, content, etag) => {
      const result = await gateway.saveBlueprintDraft(artifactId, content, etag)
      updateSnapshotResource('blueprints', artifactId, result.data)
      return result
    },
    savePageSpecDraft: async (artifactId, content, etag) => {
      const result = await gateway.savePageSpecDraft(
        artifactId,
        content,
        etag,
      )
      updateSnapshotResource('pageSpecs', artifactId, result.data)
      return result
    },
    savePrototypeDraft: async (artifactId, content, etag) => {
      const result = await gateway.savePrototypeDraft(artifactId, content, etag)
      updateSnapshotResource('prototypes', artifactId, result.data)
      return result
    },
    createDocumentRevision: async (artifactId, content) => {
      const resource = snapshot.documents.find((item) => item.artifact.id === artifactId)
      const draftEtag = resource?.draft?.etag ?? resource?.artifact.etag
      if (!draftEtag) throw new Error(t('runtime.artifact.refreshDocumentDraft'))
      const saved = await gateway.saveDocumentDraft(
        artifactId,
        content,
        draftEtag,
      )
      const revisionEtag = saved.data.draft?.etag ?? saved.etag
      if (!revisionEtag) throw new Error(t('runtime.artifact.missingSavedDraftEtag'))
      const result = await gateway.createDocumentRevision(artifactId, revisionEtag)
      await refreshRef.current()
      return result.data
    },
    createBlueprintRevision: async (artifactId, content) => {
      const resource = snapshot.blueprints.find((item) => item.artifact.id === artifactId)
      const draftEtag = resource?.draft?.etag ?? resource?.artifact.etag
      if (!draftEtag) throw new Error(t('runtime.artifact.refreshBlueprintDraft'))
      const saved = await gateway.saveBlueprintDraft(
        artifactId,
        content,
        draftEtag,
      )
      const revisionEtag = saved.data.draft?.etag ?? saved.etag
      if (!revisionEtag) throw new Error(t('runtime.artifact.missingSavedDraftEtag'))
      const result = await gateway.createBlueprintRevision(artifactId, revisionEtag)
      await refreshRef.current()
      return result.data
    },
    createPageSpecRevision: async (artifactId, content) => {
      const resource = snapshot.pageSpecs.find((item) => item.artifact.id === artifactId)
      const draftEtag = resource?.draft?.etag ?? resource?.artifact.etag
      if (!draftEtag) throw new Error(t('runtime.artifact.refreshPageSpecDraft'))
      const saved = await gateway.savePageSpecDraft(
        artifactId,
        content,
        draftEtag,
      )
      const revisionEtag = saved.data.draft?.etag ?? saved.etag
      if (!revisionEtag) throw new Error(t('runtime.artifact.missingSavedDraftEtag'))
      const result = await gateway.createPageSpecRevision(artifactId, revisionEtag)
      await refreshRef.current()
      return result.data
    },
    createPrototypeRevision: async (artifactId, content) => {
      const resource = snapshot.prototypes.find((item) => item.artifact.id === artifactId)
      const draftEtag = resource?.draft?.etag ?? resource?.artifact.etag
      if (!draftEtag) throw new Error(t('runtime.artifact.refreshPrototypeDraft'))
      const saved = await gateway.savePrototypeDraft(
        artifactId,
        content,
        draftEtag,
      )
      const revisionEtag = saved.data.draft?.etag ?? saved.etag
      if (!revisionEtag) throw new Error(t('runtime.artifact.missingSavedDraftEtag'))
      const result = await gateway.createPrototypeRevision(artifactId, revisionEtag)
      await refreshRef.current()
      return result.data
    },
    loadDetails: <TContent,>(artifactId: string) => gateway.details<TContent>(artifactId),
    createProposal: async (input) => {
      if (!project) throw new Error(t('runtime.artifact.selectProjectBeforeProposal'))
      const result = await gateway.createProposal(project.id, input)
      await refreshRef.current()
      return result.data
    },
    applyProposal: async (proposalId, acceptedOperationIds) => {
      const result = await gateway.applyProposal(proposalId, acceptedOperationIds, {
        rejectedReason: t('runtime.artifact.proposalNotSelected'),
        invalidSelection: t('runtime.artifact.proposalSelectionInvalid'),
      })
      await refreshRef.current()
      return result.data
    },
    decideProposalOperation: async (proposal, operationId, decision, reason) => {
      const result = await gateway.decideProposalOperation(
        proposal,
        operationId,
        decision,
        reason,
      )
      await refreshRef.current()
      return result.data
    },
    impact: async (artifactId) => (await gateway.impact(artifactId)).data,
  }), [
    error,
    gateway,
    project,
    refresh,
    snapshot,
    status,
    t,
    updateSnapshotResource,
  ])

  return <ArtifactWorkspaceContext.Provider value={value}>{children}</ArtifactWorkspaceContext.Provider>
}

export function useArtifactWorkspace() {
  const value = useContext(ArtifactWorkspaceContext)
  if (!value) throw new Error('useArtifactWorkspace must be used within ArtifactWorkspaceProvider')
  return value
}

export function createEmptyDocumentContent(kind: DocumentContentDto['kind']): DocumentContentDto {
  const blockId = stableId('block')
  if (kind === 'projectBrief') {
    return {
      kind,
      summary: '',
      blocks: [{ id: blockId, type: 'goal', text: '' }],
      requirements: [],
      acceptanceCriteria: [],
      openQuestions: [],
      assumptions: [],
    }
  }
  const requirementId = stableId('req')
  const criterionId = stableId('ac')
  return {
    kind,
    summary: '',
    blocks: [{ id: blockId, type: 'paragraph', text: '' }],
    requirements: [{
      id: requirementId,
      title: 'Primary requirement',
      statement: '',
      priority: 'must',
      acceptanceCriterionIds: [criterionId],
      sourceBlockIds: [blockId],
    }],
    acceptanceCriteria: [{
      id: criterionId,
      statement: '',
      priority: 'must',
      status: 'open',
    }],
    openQuestions: [],
    assumptions: [],
  }
}

export function createEmptyBlueprintContent(): BlueprintContentDto {
  return {
    nodes: [],
    edges: [],
    semantic: { nodes: [], edges: [] },
    layout: { nodePositions: {}, groups: [], viewport: { x: 0, y: 0, zoom: 1 } },
    pageSpecRefs: [],
    validation: [],
  }
}

function projectSnapshotAsLegacy(
  projectId: string,
  projectName: string,
  snapshot: ArtifactWorkspaceSnapshot,
) {
  const documents = snapshot.documents.map(documentAsLegacy)
  const blueprintResource = snapshot.blueprints[0]
  const blueprint = blueprintResource
    ? blueprintAsLegacy(blueprintResource)
    : {
        id: `bp-${projectId}`,
        title: `${projectName} Blueprint`,
        status: 'draft' as const,
        ownerId: '',
        nodes: [],
        edges: [],
        generatedDocIds: [],
        version: 0,
        updatedAt: '',
      }
  const dependencies = snapshot.documents.flatMap((document) =>
    (document.draft?.sourceVersions ?? document.latestRevision?.sourceVersions ?? []).map<DocumentDependency>(
      (source) => ({
        id: `dep-${source.revisionId}-${document.artifact.id}`,
        sourceDocId: source.artifactId,
        targetDocId: document.artifact.id,
        type: 'derives_from',
        isBlocking: document.artifact.status === 'needsSync',
      }),
    ),
  )
  return { projectId, documents, dependencies, blueprint }
}

function documentAsLegacy(resource: VersionedArtifactDto<DocumentContentDto>): TeamDocument {
  const content = resource.draft?.content ?? resource.latestRevision?.content
    ?? createEmptyDocumentContent('requirement')
  return {
    id: resource.artifact.id,
    projectId: resource.artifact.projectId,
    type: documentKind(content.kind),
    title: resource.artifact.title,
    status: documentStatus(resource.artifact.status),
    ownerId: resource.artifact.createdBy,
    members: [],
    updatedAt: resource.artifact.updatedAt,
    blocking: resource.artifact.status === 'needsSync' ? 1 : 0,
    bindings: resource.draft?.sourceVersions.length ?? resource.latestRevision?.sourceVersions?.length ?? 0,
    externalSync: null,
    position: { x: 80, y: 80 },
    summary: content.summary,
    sections: content.blocks.map((block) => ({
      title: block.type,
      body: block.text ?? JSON.stringify(block.data ?? {}),
    })),
    version: resource.latestRevision?.revisionNumber ?? 0,
    lastApprovedVersion: resource.approvedRevision?.revisionNumber,
  }
}

function blueprintAsLegacy(resource: VersionedArtifactDto<BlueprintContentDto>): Blueprint {
  const content = resource.draft?.content ?? resource.latestRevision?.content
    ?? createEmptyBlueprintContent()
  const semanticNodes = content.semantic?.nodes ?? content.nodes.map(({ position: _, ...node }) => node)
  const semanticEdges = content.semantic?.edges ?? content.edges
  return {
    id: resource.artifact.id,
    title: resource.artifact.title,
    status: blueprintStatus(resource.artifact.status),
    ownerId: resource.artifact.createdBy,
    nodes: semanticNodes.map((node) => ({
      id: node.id,
      type: legacyBlueprintNodeKind(node.kind),
      title: node.title,
      description: node.description,
      position: content.layout?.nodePositions[node.id] ?? { x: 0, y: 0 },
      boundDocumentIds: [],
      boundMemberIds: [...node.assignedMemberIds],
      boundPrototypeArtifactIds: [],
      generatedDocIds: [],
      missing: [],
    })),
    edges: semanticEdges.map((edge) => ({
      id: edge.id,
      sourceNodeId: edge.sourceNodeId,
      targetNodeId: edge.targetNodeId,
      type: legacyBlueprintEdgeKind(edge.kind),
      isRequired: edge.required,
    })),
    generatedDocIds: [],
    version: resource.latestRevision?.revisionNumber ?? 0,
    updatedAt: resource.artifact.updatedAt,
  }
}

function legacyBlueprintNodeKind(kind: BlueprintContentDto['nodes'][number]['kind']): BlueprintNodeType {
  if (kind === 'apiOperation') return 'api'
  if (kind === 'dataEntity') return 'dataModel'
  return kind
}

function legacyBlueprintEdgeKind(kind: BlueprintContentDto['edges'][number]['kind']): BlueprintEdgeType {
  if (kind === 'implements' || kind === 'implemented_by') return 'implemented_by'
  if (kind === 'realized_by') return 'renders'
  if (kind === 'drives' || kind === 'satisfied_by' || kind === 'navigates_to' || kind === 'verified_by') return 'generates'
  return kind
}

function documentKind(kind: DocumentContentDto['kind']): DocType {
  if (kind === 'projectBrief') return 'requirement'
  if (kind === 'backendDevelopment') return 'backendDev'
  if (kind === 'frontendDevelopment') return 'frontendDev'
  if (kind === 'decisionLog') return 'requirement'
	if (kind === 'dataContract' || kind === 'permissionContract' || kind === 'changeRequest' || kind === 'glossaryPolicy') return 'requirement'
  return kind
}

function documentStatus(status: VersionedArtifactDto<unknown>['artifact']['status']): DocStatus {
  if (status === 'inReview') return 'readyForReview'
  if (status === 'changesRequested') return 'changesRequested'
  if (status === 'approved') return 'approved'
  if (status === 'needsSync') return 'needsSync'
  if (status === 'archived') return 'archived'
  return 'draft'
}

function blueprintStatus(status: VersionedArtifactDto<unknown>['artifact']['status']): BlueprintStatus {
  if (status === 'approved') return 'validated'
  if (status === 'inReview') return 'readyForDocs'
  if (status === 'needsSync') return 'outdated'
  if (status === 'archived') return 'implemented'
  return 'draft'
}

function stableId(prefix: string) {
  const id = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${id}`
}
