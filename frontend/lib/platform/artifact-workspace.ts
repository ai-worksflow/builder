import type { CollaborationPlatformClient } from '../collaboration/platform-adapter'
import type {
  ArtifactDependencyDto,
  ArtifactReviewGateDto,
  ArtifactRevisionDto,
  BlueprintContentDto,
  DocumentContentDto,
  ImpactReportDto,
  PageSpecContentDto,
  ProposalDto,
  PrototypeContentDto,
  TraceLinkDto,
  VersionRefDto,
  VersionedArtifactDto,
} from './dto'
import { PlatformHttpError } from './http'

export interface ArtifactWorkspaceSnapshot {
  readonly documents: readonly VersionedArtifactDto<DocumentContentDto>[]
  readonly blueprints: readonly VersionedArtifactDto<BlueprintContentDto>[]
  readonly pageSpecs: readonly VersionedArtifactDto<PageSpecContentDto>[]
  readonly prototypes: readonly VersionedArtifactDto<PrototypeContentDto>[]
  readonly proposals: readonly ProposalDto[]
  readonly traces: readonly TraceLinkDto[]
}

export type ArtifactWorkspaceResourceCollection =
  | 'documents'
  | 'blueprints'
  | 'pageSpecs'
  | 'prototypes'

export function replaceArtifactWorkspaceSnapshotResource<TContent>(
  snapshot: ArtifactWorkspaceSnapshot,
  collection: ArtifactWorkspaceResourceCollection,
  artifactId: string,
  resource: VersionedArtifactDto<TContent>,
): ArtifactWorkspaceSnapshot {
  return {
    ...snapshot,
    [collection]: snapshot[collection].map((item) =>
      item.artifact.id === artifactId ? resource : item,
    ),
  } as ArtifactWorkspaceSnapshot
}

export interface ArtifactDetails<TContent> {
  readonly versions: readonly ArtifactRevisionDto<TContent>[]
  readonly dependencies: readonly ArtifactDependencyDto[]
  readonly reviewGate: ArtifactReviewGateDto
}

export function reviewGateReadyForRequest(gate?: ArtifactReviewGateDto) {
  return Boolean(gate) && gate!.checks.every((check) =>
    check.severity !== 'error' || check.code === 'canonical_review_approved')
}

export function documentReviewIssues(content: DocumentContentDto) {
  const issues: string[] = []
  if (!content.summary.trim()) issues.push('Summary is required.')
  if (content.blocks.length === 0) issues.push('At least one structured block is required.')
  if (content.kind === 'projectBrief' && !content.blocks.some((item) =>
    item.type === 'goal' && item.text?.trim(),
  )) {
    issues.push('Project Brief requires at least one non-empty goal block.')
  }
  if (content.blocks.some((item) =>
    item.type === 'openQuestion'
      && item.blocking
      && !['answered', 'resolved', 'waived'].includes(item.status ?? 'open'),
  )) {
    issues.push('Resolve or waive every blocking open question before review.')
  }
  if (content.kind !== 'projectBrief') {
    const requirements = content.requirements ?? []
    const criteria = content.acceptanceCriteria ?? []
    const requirementIds = requirements.map((item) => item.id)
    const criterionIds = criteria.map((item) => item.id)
    const blockIds = new Set(content.blocks.map((item) => item.id))
    const criterionSet = new Set(criterionIds)
    if (requirements.length === 0) issues.push('At least one requirement is required.')
    if (requirements.some((item) => !item.id || !item.statement.trim())) {
      issues.push('Every requirement needs a stable ID and statement.')
    }
    if (new Set(requirementIds).size !== requirementIds.length) {
      issues.push('Requirement IDs must be unique.')
    }
    if (criteria.some((item) => !item.id || !item.statement.trim())) {
      issues.push('Every acceptance criterion needs a stable ID and statement.')
    }
    if (new Set(criterionIds).size !== criterionIds.length) {
      issues.push('Acceptance criterion IDs must be unique.')
    }
    if (requirements.some((item) =>
      item.priority === 'must' && item.acceptanceCriterionIds.length === 0,
    )) {
      issues.push('Every Must requirement needs at least one acceptance criterion.')
    }
    if (requirements.some((item) =>
      item.acceptanceCriterionIds.some((criterionId) => !criterionSet.has(criterionId)),
    )) {
      issues.push('Every requirement acceptance reference must resolve to an existing criterion.')
    }
    if (requirements.some((item) =>
      item.sourceBlockIds.length === 0 || item.sourceBlockIds.some((blockId) => !blockIds.has(blockId)),
    )) {
      issues.push('Every requirement must trace to at least one existing source block.')
    }
  }
  return issues
}

const REQUIREMENT_BASELINE_DOCUMENT_KINDS = new Set([
  'project_brief',
  'product_requirements',
  'decision_record',
  'glossary_policy',
])

export function approvedRequirementBaselineSources(
  documents: readonly VersionedArtifactDto<DocumentContentDto>[],
) {
  const eligible = documents.filter((document) =>
    REQUIREMENT_BASELINE_DOCUMENT_KINDS.has(document.artifact.kind),
  )
  if (!eligible.some((document) =>
    document.artifact.kind === 'product_requirements' && document.approvedRevision,
  )) {
    throw new Error('Approve Product Requirements with stable requirement and acceptance IDs before creating a Blueprint.')
  }
  return eligible.flatMap((document) => {
    const revision = document.approvedRevision
    return revision ? [{
      artifactId: revision.artifactId,
      revisionId: revision.id,
      contentHash: revision.contentHash,
    }] : []
  })
}

export interface CreateArtifactProposalInput {
  readonly jobType: string
  readonly targetRevision: VersionRefDto
  readonly instruction: string
  readonly inputVersions?: readonly VersionRefDto[]
  readonly outputSchemaVersion: string
  readonly model?: string
}

export function createEmptyPageSpecContent(
  blueprintPageNodeId: string,
  title: string,
  route: string,
  userGoal: string,
): PageSpecContentDto {
  return {
    blueprintPageNodeId,
    title,
    route,
    userGoal,
    entryPoints: [],
    exitPoints: [],
    requiredRoles: [],
    states: [
      pageSpecState('ready', 'Ready'),
      pageSpecState('loading', 'Loading'),
      pageSpecState('empty', 'Empty'),
      pageSpecState('error', 'Error'),
    ],
    dataBindings: [],
    interactions: [],
    acceptanceCriterionIds: [],
    nonFunctionalConstraints: [],
  }
}

export function createEmptyPrototypeContent(
  pageSpecRevision: VersionRefDto,
  pageSpecContent: PageSpecContentDto,
  exploratory = false,
): PrototypeContentDto {
  const rootLayerId = artifactStableId('layer')
  const states = pageSpecContent.states
    .map((state) => ({
      id: state.id,
      key: state.key,
      title: state.title,
      required: state.required,
      fixtureIds: [...state.fixtureIds],
      pageStateId: state.id,
    }))
  const breakpoints = [
    {
      id: 'desktop',
      name: 'Desktop',
      minWidth: 1024,
      viewportWidth: 1440,
      viewportHeight: 900,
    },
    {
      id: 'tablet',
      name: 'Tablet',
      minWidth: 768,
      maxWidth: 1023,
      viewportWidth: 768,
      viewportHeight: 1024,
    },
    {
      id: 'mobile',
      name: 'Mobile',
      minWidth: 0,
      maxWidth: 767,
      viewportWidth: 390,
      viewportHeight: 844,
    },
  ] as const
  return {
    pageSpecRevision: wireVersionRef(pageSpecRevision),
    exploratory,
    states,
    breakpoints,
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
        acceptanceCriterionIds: [...pageSpecContent.acceptanceCriterionIds],
        fieldMetadata: {},
      },
    },
    frames: states.flatMap((state) => breakpoints.map((breakpoint) => ({
      id: `frame-${state.id}-${breakpoint.id}`,
      stateId: state.id,
      breakpointId: breakpoint.id,
      rootLayerId,
      title: `${state.title} · ${breakpoint.name}`,
    }))),
    overrides: [],
    interactions: [],
    fixtures: [],
    tokenBindings: [],
    componentBindings: [],
    assets: [],
    traceLinks: [],
  }
}

function pageSpecState(id: 'ready' | 'loading' | 'empty' | 'error', title: string) {
  return {
    id,
    key: id,
    title,
    required: true,
    fixtureIds: [],
    acceptanceCriterionIds: [],
  }
}

function artifactStableId(prefix: string) {
  const id = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `${prefix}-${id}`
}

export class ArtifactWorkspaceConflictError<TContent> extends Error {
  readonly artifactId: string
  readonly localContent: TContent

  constructor(artifactId: string, localContent: TContent) {
    super('The artifact draft changed on the server. Your local draft was preserved.')
    this.name = 'ArtifactWorkspaceConflictError'
    this.artifactId = artifactId
    this.localContent = localContent
  }
}

export class ArtifactWorkspaceGateway {
  readonly client: CollaborationPlatformClient

  constructor(client: CollaborationPlatformClient) {
    this.client = client
  }

  async load(projectId: string): Promise<ArtifactWorkspaceSnapshot> {
    const [documents, blueprints, pageSpecs, prototypes, proposals, traces] = await Promise.all([
      this.client.documents.list(projectId, { limit: 200 }),
      this.client.blueprints.list(projectId, { limit: 100 }),
      this.client.pageSpecs.list(projectId, { limit: 200 }),
      this.client.prototypes.list(projectId, { limit: 200 }),
      this.client.proposals.list(projectId, {}, { limit: 200 }),
      this.client.traces.list(projectId, { limit: 500 }),
    ])
    return {
      documents: documents.data.items,
      blueprints: blueprints.data.items,
      pageSpecs: pageSpecs.data.items,
      prototypes: prototypes.data.items,
      proposals: proposals.data.items,
      traces: traces.data.items,
    }
  }

  createDocument(
    projectId: string,
    title: string,
    content: DocumentContentDto,
  ) {
    return this.client.documents.create(projectId, {
      title,
      kind: content.kind,
      content,
    })
  }

  createBlueprint(
    projectId: string,
    title: string,
    requirementVersions: readonly {
      artifactId: string
      revisionId: string
      revisionNumber?: number
      contentHash: string
    }[],
    content: BlueprintContentDto,
  ) {
    return this.client.blueprints.create(projectId, {
      title,
      requirementVersions: requirementVersions.map(wireVersionRef),
      content,
    })
  }

  compileRequirementBaseline(projectId: string, sources: readonly VersionRefDto[]) {
    return this.client.artifacts.compileRequirementBaseline(projectId, sources)
  }

  createPageSpec(
    projectId: string,
    title: string,
    blueprintRevision: VersionRefDto,
    blueprintPageNodeId: string,
    content: PageSpecContentDto,
  ) {
    return this.client.pageSpecs.create(projectId, {
      title,
      blueprintRevision: wireVersionRef(blueprintRevision),
      blueprintPageNodeId,
      content,
    })
  }

  createPrototype(
    projectId: string,
    title: string,
    pageSpecRevision: {
      artifactId: string
      revisionId: string
      revisionNumber?: number
      contentHash: string
    },
    content: PrototypeContentDto,
  ) {
    return this.client.prototypes.create(projectId, {
      title,
      pageSpecRevision: wireVersionRef(pageSpecRevision),
      exploratory: content.exploratory,
      content,
    })
  }

  async saveDocumentDraft(
    artifactId: string,
    content: DocumentContentDto,
    etag: string,
    sourceVersions?: readonly {
      artifactId: string
      revisionId: string
      revisionNumber?: number
      contentHash: string
    }[],
  ) {
    try {
      return await this.client.documents.updateDraft(
        artifactId,
        {
          content,
          ...(sourceVersions ? { sourceVersions: sourceVersions.map(wireVersionRef) } : {}),
        },
        { ifMatch: etag },
      )
    } catch (error) {
      if (error instanceof PlatformHttpError && [409, 412, 428].includes(error.status)) {
        throw new ArtifactWorkspaceConflictError(artifactId, content)
      }
      throw error
    }
  }

  async saveBlueprintDraft(
    artifactId: string,
    content: BlueprintContentDto,
    etag: string,
    sourceVersions?: readonly {
      artifactId: string
      revisionId: string
      revisionNumber?: number
      contentHash: string
    }[],
  ) {
    try {
      return await this.client.blueprints.updateDraft(
        artifactId,
        {
          content,
          ...(sourceVersions ? { sourceVersions: sourceVersions.map(wireVersionRef) } : {}),
        },
        { ifMatch: etag },
      )
    } catch (error) {
      if (error instanceof PlatformHttpError && [409, 412, 428].includes(error.status)) {
        throw new ArtifactWorkspaceConflictError(artifactId, content)
      }
      throw error
    }
  }

  async savePageSpecDraft(
    artifactId: string,
    content: PageSpecContentDto,
    etag: string,
    sourceVersions?: readonly {
      artifactId: string
      revisionId: string
      revisionNumber?: number
      contentHash: string
    }[],
  ) {
    try {
      return await this.client.pageSpecs.updateDraft(
        artifactId,
        {
          content,
          ...(sourceVersions ? { sourceVersions: sourceVersions.map(wireVersionRef) } : {}),
        },
        { ifMatch: etag },
      )
    } catch (error) {
      if (error instanceof PlatformHttpError && [409, 412, 428].includes(error.status)) {
        throw new ArtifactWorkspaceConflictError(artifactId, content)
      }
      throw error
    }
  }

  async savePrototypeDraft(
    artifactId: string,
    content: PrototypeContentDto,
    etag: string,
    sourceVersions?: readonly {
      artifactId: string
      revisionId: string
      revisionNumber?: number
      contentHash: string
    }[],
  ) {
    try {
      return await this.client.prototypes.updateDraft(
        artifactId,
        {
          content,
          ...(sourceVersions ? { sourceVersions: sourceVersions.map(wireVersionRef) } : {}),
        },
        { ifMatch: etag },
      )
    } catch (error) {
      if (error instanceof PlatformHttpError && [409, 412, 428].includes(error.status)) {
        throw new ArtifactWorkspaceConflictError(artifactId, content)
      }
      throw error
    }
  }

  createDocumentRevision(
    artifactId: string,
    draftEtag: string,
    changeSummary = 'Create document revision',
  ) {
    return this.client.documents.createRevision(
      artifactId,
      { changeSummary, changeSource: 'human' },
      { ifMatch: draftEtag, idempotencyKey: true },
    )
  }

  createBlueprintRevision(
    artifactId: string,
    draftEtag: string,
    changeSummary = 'Create blueprint revision',
  ) {
    return this.client.blueprints.createRevision(
      artifactId,
      { changeSummary, changeSource: 'human' },
      { ifMatch: draftEtag, idempotencyKey: true },
    )
  }

  createPageSpecRevision(
    artifactId: string,
    draftEtag: string,
    changeSummary = 'Create PageSpec revision',
  ) {
    return this.client.pageSpecs.createRevision(
      artifactId,
      { changeSummary, changeSource: 'human' },
      { ifMatch: draftEtag, idempotencyKey: true },
    )
  }

  createPrototypeRevision(
    artifactId: string,
    draftEtag: string,
    changeSummary = 'Create prototype revision',
  ) {
    return this.client.prototypes.createRevision(
      artifactId,
      { changeSummary, changeSource: 'human' },
      { ifMatch: draftEtag, idempotencyKey: true },
    )
  }

  async details<TContent>(artifactId: string): Promise<ArtifactDetails<TContent>> {
    const [versions, dependencies, reviewGate] = await Promise.all([
      this.client.artifacts.listRevisions<TContent>(artifactId, { limit: 100 }),
      this.client.artifacts.listDependencies(artifactId, { limit: 200 }),
      this.client.artifacts.reviewGate(artifactId),
    ])
    return {
      versions: versions.data.items,
      dependencies: dependencies.data.items,
      reviewGate: reviewGate.data,
    }
  }

  async createProposal(projectId: string, input: CreateArtifactProposalInput) {
    const upstreamSources = uniqueVersionRefs(input.inputVersions ?? [])
    // The base revision is already pinned separately. Prefer real upstream
    // sources so applying the proposal cannot create a self-dependency. Root
    // artifacts without upstream lineage retain the base as generation input.
    const sources = upstreamSources.length > 0
      ? upstreamSources
      : [input.targetRevision]
    const manifest = await this.client.manifests.create(projectId, {
      jobType: input.jobType,
      baseRevision: wireVersionRef(input.targetRevision),
      sources: sources.map((ref, index) => ({
        ref: wireVersionRef(ref),
        purpose: upstreamSources.length > 0 ? 'approved_upstream' : index === 0 ? 'proposal_base' : 'approved_upstream',
      })),
      constraints: { instruction: input.instruction.trim() },
      outputSchemaVersion: input.outputSchemaVersion,
    }, { idempotencyKey: true })
    const generated = await this.client.manifests.generateArtifactProposal(
      manifest.data.id,
      input.model?.trim() || 'gpt-5',
      { idempotencyKey: true },
    )
    return { ...generated, data: generated.data.proposal }
  }

  async applyProposal(proposalId: string, acceptedOperationIds: readonly string[]) {
    const accepted = new Set(acceptedOperationIds)
    let current = (await this.client.proposals.get(proposalId)).data
    for (const operation of current.operations) {
      if (operation.decision !== 'pending') continue
      const decision = accepted.has(operation.id) ? 'accepted' : 'rejected'
      const result = await this.client.proposals.decide(proposalId, {
        operationId: operation.id,
        decision,
        reason: decision === 'rejected' ? 'Not selected during proposal review.' : undefined,
        version: current.version,
      }, {
        ifMatch: proposalEtag(current),
        idempotencyKey: true,
      })
      current = result.data
    }
    if (current.status !== 'ready') {
      throw new Error('At least one operation must be accepted and every operation must be decided before apply.')
    }
    return this.client.proposals.apply(proposalId, { version: current.version }, {
      ifMatch: proposalEtag(current),
      idempotencyKey: true,
    })
  }

  impact(blueprintArtifactId: string): Promise<{ data: ImpactReportDto }> {
    return this.client.blueprints.impact(blueprintArtifactId)
  }
}

function proposalEtag(proposal: Pick<ProposalDto, 'id' | 'version'>) {
  return `"output-proposal:${proposal.id}:${proposal.version}"`
}

function uniqueVersionRefs(references: readonly VersionRefDto[]) {
  const seen = new Set<string>()
  return references.filter((reference) => {
    const key = `${reference.artifactId}:${reference.revisionId}:${reference.contentHash}:${reference.anchorId ?? ''}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function wireVersionRef(reference: VersionRefDto): VersionRefDto {
  return {
    artifactId: reference.artifactId,
    revisionId: reference.revisionId,
    contentHash: reference.contentHash,
    ...(reference.anchorId ? { anchorId: reference.anchorId } : {}),
  }
}
