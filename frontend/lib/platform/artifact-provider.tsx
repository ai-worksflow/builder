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
import { useWorksflow } from '../worksflow/store'
import type {
  Blueprint,
  BlueprintStatus,
  DocStatus,
  DocType,
  DocumentDependency,
  TeamDocument,
} from '../worksflow/types'
import {
  ArtifactWorkspaceGateway,
  type ArtifactDetails,
  type ArtifactWorkspaceSnapshot,
} from './artifact-workspace'
import type {
  ArtifactRevisionDto,
  BlueprintContentDto,
  CreateProposalInputDto,
  DocumentContentDto,
  ImpactReportDto,
  PageSpecContentDto,
  ProposalDto,
  PrototypeContentDto,
  VersionedArtifactDto,
} from './dto'

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
  readonly createPrototypeRevision: (
    artifactId: string,
    content: PrototypeContentDto,
  ) => Promise<ArtifactRevisionDto<PrototypeContentDto>>
  readonly loadDetails: <TContent>(artifactId: string) => Promise<ArtifactDetails<TContent>>
  readonly createProposal: (input: CreateProposalInputDto) => Promise<ProposalDto>
  readonly applyProposal: (
    proposalId: string,
    operationIndexes: readonly number[],
    etag: string,
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
      const message = collaborationErrorMessage(cause, 'Unable to load platform artifacts.')
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
    const unsubscribe = platformClient.websocket.subscribeProject(project.id, (event) => {
      if (
        event.type === 'artifact.updated' ||
        event.type === 'revision.created' ||
        event.type === 'document.updated' ||
        event.type === 'blueprint.updated' ||
        event.type === 'pageSpec.updated' ||
        event.type === 'prototype.updated' ||
        event.type === 'proposal.updated' ||
        event.type === 'trace.updated'
      ) void refreshRef.current()
    })
    platformClient.websocket.connect()
    return unsubscribe
  }, [platformClient.websocket, project, session.signedIn])

  const updateSnapshotResource = useCallback(<TContent,>(
    collection: 'documents' | 'blueprints' | 'prototypes',
    artifactId: string,
    resource: VersionedArtifactDto<TContent>,
  ) => {
    setSnapshot((current) => ({
      ...current,
      [collection]: current[collection].map((item) =>
        item.artifact.id === artifactId ? resource : item,
      ),
    } as ArtifactWorkspaceSnapshot))
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
      const requirementVersions = snapshot.documents.flatMap((document) =>
        document.approvedRevision
          ? [{
              artifactId: document.approvedRevision.artifactId,
              revisionId: document.approvedRevision.id,
              revisionNumber: document.approvedRevision.revisionNumber,
              contentHash: document.approvedRevision.contentHash,
            }]
          : [],
      )
      const result = await gateway.createBlueprint(
        project.id,
        title,
        requirementVersions,
        createEmptyBlueprintContent(),
      )
      await refreshRef.current()
      return result.data.artifact.id
    },
    createPageSpec: async (blueprintArtifactId, blueprintPageNodeId, title, route) => {
      if (!project) return null
      const blueprint = snapshot.blueprints.find((item) => item.artifact.id === blueprintArtifactId)
      const revision = blueprint?.approvedRevision ?? blueprint?.latestRevision
      if (!revision) {
        throw new Error('Create an immutable blueprint revision before creating a PageSpec.')
      }
      const result = await gateway.createPageSpec(
        project.id,
        title,
        {
          artifactId: revision.artifactId,
          revisionId: revision.id,
          revisionNumber: revision.revisionNumber,
          contentHash: revision.contentHash,
        },
        blueprintPageNodeId,
        createEmptyPageSpecContent(blueprintPageNodeId, title, route),
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
            ? 'Create an immutable PageSpec revision before creating a prototype.'
            : 'Approve an immutable PageSpec revision before creating a formal prototype.',
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
        createEmptyPrototypeContent(reference, exploratory),
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
    savePrototypeDraft: async (artifactId, content, etag) => {
      const result = await gateway.savePrototypeDraft(artifactId, content, etag)
      updateSnapshotResource('prototypes', artifactId, result.data)
      return result
    },
    createDocumentRevision: async (artifactId, content) => {
      const resource = snapshot.documents.find((item) => item.artifact.id === artifactId)
      const draftEtag = resource?.draft?.etag ?? resource?.artifact.etag
      if (!draftEtag) throw new Error('Refresh the document draft before creating a revision.')
      const saved = await gateway.saveDocumentDraft(
        artifactId,
        content,
        draftEtag,
        resource?.draft?.sourceVersions ?? [],
      )
      const revisionEtag = saved.data.draft?.etag ?? saved.etag
      if (!revisionEtag) throw new Error('The server did not return the saved draft ETag.')
      const result = await gateway.createDocumentRevision(artifactId, revisionEtag)
      await refreshRef.current()
      return result.data
    },
    createBlueprintRevision: async (artifactId, content) => {
      const resource = snapshot.blueprints.find((item) => item.artifact.id === artifactId)
      const draftEtag = resource?.draft?.etag ?? resource?.artifact.etag
      if (!draftEtag) throw new Error('Refresh the blueprint draft before creating a revision.')
      const saved = await gateway.saveBlueprintDraft(
        artifactId,
        content,
        draftEtag,
        resource?.draft?.sourceVersions ?? [],
      )
      const revisionEtag = saved.data.draft?.etag ?? saved.etag
      if (!revisionEtag) throw new Error('The server did not return the saved draft ETag.')
      const result = await gateway.createBlueprintRevision(artifactId, revisionEtag)
      await refreshRef.current()
      return result.data
    },
    createPrototypeRevision: async (artifactId, content) => {
      const resource = snapshot.prototypes.find((item) => item.artifact.id === artifactId)
      const draftEtag = resource?.draft?.etag ?? resource?.artifact.etag
      if (!draftEtag) throw new Error('Refresh the prototype draft before creating a revision.')
      const saved = await gateway.savePrototypeDraft(
        artifactId,
        content,
        draftEtag,
      )
      const revisionEtag = saved.data.draft?.etag ?? saved.etag
      if (!revisionEtag) throw new Error('The server did not return the saved draft ETag.')
      const result = await gateway.createPrototypeRevision(artifactId, revisionEtag)
      await refreshRef.current()
      return result.data
    },
    loadDetails: <TContent,>(artifactId: string) => gateway.details<TContent>(artifactId),
    createProposal: async (input) => {
      if (!project) throw new Error('Select a project before creating a proposal.')
      const result = await gateway.createProposal(project.id, input)
      await refreshRef.current()
      return result.data
    },
    applyProposal: async (proposalId, operationIndexes, etag) => {
      const result = await gateway.applyProposal(proposalId, operationIndexes, etag)
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

export function createEmptyPageSpecContent(
  blueprintPageNodeId: string,
  title: string,
  route: string,
): PageSpecContentDto {
  return {
    blueprintPageNodeId,
    title,
    route,
    userGoal: '',
    entryPoints: [],
    exitPoints: [],
    requiredRoles: [],
    states: [{
      id: stableId('state'),
      key: 'default',
      title: 'Default',
      required: true,
      fixtureIds: [],
      acceptanceCriterionIds: [],
    }],
    dataBindings: [],
    interactions: [],
    acceptanceCriterionIds: [],
    nonFunctionalConstraints: [],
  }
}

export function createEmptyPrototypeContent(
  pageSpecRevision: {
    artifactId: string
    revisionId: string
    revisionNumber: number
    contentHash: string
  },
  exploratory = false,
): PrototypeContentDto {
  const stateId = stableId('state')
  const breakpointId = stableId('breakpoint')
  const rootLayerId = stableId('layer')
  return {
    pageSpecRevision,
    exploratory,
    states: [{ id: stateId, key: 'ready', title: 'Ready', required: true, fixtureIds: [] }],
    breakpoints: [{
      id: breakpointId,
      name: 'Desktop',
      minWidth: 1024,
      viewportWidth: 1440,
      viewportHeight: 900,
    }],
    layers: {
      [rootLayerId]: {
        id: rootLayerId,
        childIds: [],
        kind: 'frame',
        name: 'Page',
        semanticRole: 'main',
        layout: { x: 0, y: 0, width: 1440, height: 900 },
        style: { fill: '#171719' },
        properties: {},
        requirementIds: [],
        acceptanceCriterionIds: [],
        fieldMetadata: {},
      },
    },
    frames: [{ id: stableId('frame'), stateId, breakpointId, rootLayerId, title: 'Ready' }],
    overrides: [],
    interactions: [],
    fixtures: [],
    tokenBindings: [],
    componentBindings: [],
    assets: [],
    traceLinks: [],
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
      type: node.kind,
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
      type: edge.kind === 'implements' ? 'implemented_by' : edge.kind,
      isRequired: edge.required,
    })),
    generatedDocIds: [],
    version: resource.latestRevision?.revisionNumber ?? 0,
    updatedAt: resource.artifact.updatedAt,
  }
}

function documentKind(kind: DocumentContentDto['kind']): DocType {
  if (kind === 'backendDevelopment') return 'backendDev'
  if (kind === 'frontendDevelopment') return 'frontendDev'
  if (kind === 'decisionLog') return 'requirement'
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
