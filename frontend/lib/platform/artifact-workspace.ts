import type { CollaborationPlatformClient } from '../collaboration/platform-adapter'
import type {
  ArtifactDependencyDto,
  ArtifactDraftDto,
  ArtifactReviewGateDto,
  ArtifactRevisionDto,
  AcceptanceCriterionDto,
  BlueprintContentDto,
  DocumentBlockDto,
  DocumentContentDto,
  ImpactReportDto,
  JsonObject,
  PageSpecContentDto,
  ProposalDto,
  PrototypeContentDto,
  RequirementItemDto,
  TraceLinkDto,
  VersionRefDto,
  VersionedArtifactDto,
} from './dto'
import { PlatformHttpError } from './http'

const PLATFORM_PAGE_LIMIT = 200
const WORKSPACE_TRACE_LIMIT = 500

export interface ArtifactWorkspaceSnapshot {
  readonly documents: readonly VersionedArtifactDto<DocumentContentDto>[]
  readonly blueprints: readonly VersionedArtifactDto<BlueprintContentDto>[]
  readonly pageSpecs: readonly VersionedArtifactDto<PageSpecContentDto>[]
  readonly prototypes: readonly VersionedArtifactDto<PrototypeContentDto>[]
  readonly proposals: readonly ProposalDto[]
  readonly traces: readonly TraceLinkDto[]
}

const ARTIFACT_WORKSPACE_REFRESH_EVENTS = new Set([
  'artifact.updated',
  'revision.created',
  'document.updated',
  'blueprint.updated',
  'pageSpec.updated',
  'prototype.updated',
  'proposal.updated',
  'trace.updated',
  'artifact.created',
  'artifact.draft_updated',
  'artifact.revision_created',
  'dependency.created',
  'trace.created',
  'proposal.created',
  'proposal.operation_decided',
  'proposal.applied',
  'review.submitted',
  'review.stale',
  'review.decision_recorded',
  'artifact.revision_approved',
  'document.downstream_generated',
  'document.sync_back_proposed',
])

export function artifactWorkspaceEventRequiresRefresh(type: string) {
  return ARTIFACT_WORKSPACE_REFRESH_EVENTS.has(type)
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

export function mergeArtifactWorkspaceProposalApply<TDraft>(
  snapshot: ArtifactWorkspaceSnapshot,
  proposal: ProposalDto,
  draft: ArtifactDraftDto<TDraft>,
): ArtifactWorkspaceSnapshot {
  if (proposal.artifactId !== draft.artifactId) {
    throw new Error('Proposal apply returned a draft for a different artifact.')
  }

  const existingProposal = snapshot.proposals.find((item) => item.id === proposal.id)
  const proposals = existingProposal
    ? snapshot.proposals.map((item) =>
        item.id === proposal.id && item.version <= proposal.version ? proposal : item,
      )
    : [...snapshot.proposals, proposal]
  const resourcesWithDraft = <TContent,>(
    resources: readonly VersionedArtifactDto<TContent>[],
  ): readonly VersionedArtifactDto<TContent>[] => resources.map((resource) => {
    if (resource.artifact.id !== draft.artifactId) return resource
    if (
      resource.draft
      && (
        resource.draft.revision > draft.revision
        || (
          resource.draft.revision === draft.revision
          && resource.draft.updatedAt > draft.updatedAt
        )
      )
    ) return resource
    return {
      ...resource,
      draft: draft as unknown as ArtifactDraftDto<TContent>,
    }
  })

  return {
    ...snapshot,
    documents: resourcesWithDraft(snapshot.documents),
    blueprints: resourcesWithDraft(snapshot.blueprints),
    pageSpecs: resourcesWithDraft(snapshot.pageSpecs),
    prototypes: resourcesWithDraft(snapshot.prototypes),
    proposals,
  }
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

/**
 * Builds a complete editor/validation view over document content returned by
 * older or partially materialized payloads. The input remains the canonical
 * raw value; callers must not persist this derived compatibility view merely
 * because it was rendered.
 */
export function normalizeDocumentContent(content: DocumentContentDto): DocumentContentDto {
  const raw = objectValue(content)
  return {
    ...raw,
    kind: nonEmptyString(raw.kind) ? raw.kind as DocumentContentDto['kind'] : 'requirement',
    summary: stringValue(raw.summary),
    blocks: arrayValue(raw.blocks).map(normalizeDocumentBlock),
    requirements: arrayValue(raw.requirements).map(normalizeRequirement),
    acceptanceCriteria: arrayValue(raw.acceptanceCriteria).map(normalizeAcceptanceCriterion),
    openQuestions: stringArray(raw.openQuestions),
    assumptions: stringArray(raw.assumptions),
  } as DocumentContentDto
}

function normalizeDocumentBlock(value: unknown): DocumentBlockDto {
  const raw = objectValue(value)
  return {
    ...raw,
    id: stringValue(raw.id),
    // Preserve forward-compatible server block types such as sourceContext in
    // the display model; the editor includes the current value as an option.
    type: (nonEmptyString(raw.type) ? raw.type : 'paragraph') as DocumentBlockDto['type'],
    ...(raw.text === undefined ? {} : { text: stringValue(raw.text) }),
    ...(raw.children === undefined
      ? {}
      : { children: arrayValue(raw.children).map(normalizeDocumentBlock) }),
    ...(raw.requirementIds === undefined
      ? {}
      : { requirementIds: stringArray(raw.requirementIds) }),
  } as DocumentBlockDto
}

function normalizeRequirement(value: unknown): RequirementItemDto {
  const raw = objectValue(value)
  return {
    ...raw,
    id: stringValue(raw.id),
    title: stringValue(raw.title),
    statement: stringValue(raw.statement),
    priority: priorityValue(raw.priority),
    acceptanceCriterionIds: stringArray(raw.acceptanceCriterionIds),
    sourceBlockIds: stringArray(raw.sourceBlockIds),
  } as RequirementItemDto
}

function normalizeAcceptanceCriterion(value: unknown): AcceptanceCriterionDto {
  const raw = objectValue(value)
  return {
    ...raw,
    id: stringValue(raw.id),
    statement: stringValue(raw.statement),
    priority: priorityValue(raw.priority),
    status: criterionStatusValue(raw.status),
  } as AcceptanceCriterionDto
}

function priorityValue(value: unknown): RequirementItemDto['priority'] {
  const normalized = stringValue(value).trim().toLowerCase()
  return normalized === 'should' || normalized === 'could' ? normalized : 'must'
}

function criterionStatusValue(value: unknown): AcceptanceCriterionDto['status'] {
  const normalized = stringValue(value).trim().toLowerCase()
  return normalized === 'accepted' || normalized === 'rejected' ? normalized : 'open'
}

function objectValue(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {}
}

function arrayValue(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : []
}

function stringArray(value: unknown): string[] {
  return arrayValue(value).filter((item): item is string => typeof item === 'string')
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

export function documentReviewIssues(content: DocumentContentDto) {
  const normalized = normalizeDocumentContent(content)
  const issues: string[] = []
  if (!normalized.summary.trim()) issues.push('Summary is required.')
  if (normalized.blocks.length === 0) issues.push('At least one structured block is required.')
  if (normalized.kind === 'projectBrief' && !normalized.blocks.some((item) =>
    item.type === 'goal' && item.text?.trim(),
  )) {
    issues.push('Project Brief requires at least one non-empty goal block.')
  }
  if (normalized.blocks.some((item) =>
    item.type === 'openQuestion'
      && item.blocking
      && !['answered', 'resolved', 'waived'].includes(item.status ?? 'open'),
  )) {
    issues.push('Resolve or waive every blocking open question before review.')
  }
  if (normalized.kind !== 'projectBrief') {
    const requirements = normalized.requirements ?? []
    const criteria = normalized.acceptanceCriteria
    const requirementIds = requirements.map((item) => item.id)
    const criterionIds = criteria.map((item) => item.id)
    const blockIds = new Set(normalized.blocks.map((item) => item.id))
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
  missingRequirementsMessage = 'Approve Product Requirements with stable requirement and acceptance IDs before creating a Blueprint.',
) {
  const eligible = documents.filter((document) =>
    REQUIREMENT_BASELINE_DOCUMENT_KINDS.has(document.artifact.kind),
  )
  if (!eligible.some((document) =>
    document.artifact.kind === 'product_requirements' && document.approvedRevision,
  )) {
    throw new Error(missingRequirementsMessage)
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
  readonly constraints?: JsonObject
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

async function loadWorkspaceTraces(
  client: CollaborationPlatformClient,
  projectId: string,
): Promise<TraceLinkDto[]> {
  const traces: TraceLinkDto[] = []
  const cursors = new Set<string>()
  let cursor: string | undefined

  while (traces.length < WORKSPACE_TRACE_LIMIT) {
    const remaining = WORKSPACE_TRACE_LIMIT - traces.length
    const result = await client.traces.list(projectId, {
      cursor,
      limit: Math.min(PLATFORM_PAGE_LIMIT, remaining),
    })
    traces.push(...result.data.items.slice(0, remaining))

    const nextCursor = result.data.nextCursor?.trim()
    if (!nextCursor || result.data.items.length === 0) break
    if (cursors.has(nextCursor)) throw new Error('Trace pagination returned a repeated cursor.')
    cursors.add(nextCursor)
    cursor = nextCursor
  }

  return traces
}

async function loadWorkspaceProposals(
  client: CollaborationPlatformClient,
  projectId: string,
): Promise<ProposalDto[]> {
  const proposals: ProposalDto[] = []
  const cursors = new Set<string>()
  let cursor: string | undefined

  while (true) {
    const result = await client.proposals.list(projectId, {}, {
      cursor,
      limit: PLATFORM_PAGE_LIMIT,
    })
    proposals.push(...result.data.items)

    const nextCursor = result.data.nextCursor?.trim()
    if (!nextCursor || result.data.items.length === 0) break
    if (cursors.has(nextCursor)) throw new Error('Proposal pagination returned a repeated cursor.')
    cursors.add(nextCursor)
    cursor = nextCursor
  }

  return proposals
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
      loadWorkspaceProposals(this.client, projectId),
      loadWorkspaceTraces(this.client, projectId),
    ])
    return {
      documents: documents.data.items,
      blueprints: blueprints.data.items,
      pageSpecs: pageSpecs.data.items,
      prototypes: prototypes.data.items,
      proposals,
      traces,
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
      constraints: { instruction: input.instruction.trim(), ...(input.constraints ?? {}) },
      outputSchemaVersion: input.outputSchemaVersion,
    }, { idempotencyKey: true })
    const generated = await this.client.manifests.generateArtifactProposal(
      manifest.data.id,
      input.model?.trim() || 'gpt-5',
      { idempotencyKey: true },
    )
    return { ...generated, data: generated.data.proposal }
  }

  async applyProposal(
    proposalId: string,
    acceptedOperationIds: readonly string[],
    messages: {
      rejectedReason?: string
      invalidSelection?: string
    } = {},
  ) {
    const accepted = new Set(acceptedOperationIds)
    let current = (await this.client.proposals.get(proposalId)).data
    for (const operation of current.operations) {
      if (operation.decision !== 'pending') continue
      const decision = accepted.has(operation.id) ? 'accepted' : 'rejected'
      const result = await this.client.proposals.decide(proposalId, {
        operationId: operation.id,
        decision,
        reason: decision === 'rejected'
          ? messages.rejectedReason ?? 'Not selected during proposal review.'
          : undefined,
        version: current.version,
      }, {
        ifMatch: proposalEtag(current),
        idempotencyKey: true,
      })
      current = result.data
    }
    if (current.status !== 'ready') {
      throw new Error(
        messages.invalidSelection
          ?? 'At least one operation must be accepted and every operation must be decided before apply.',
      )
    }
    const applied = await this.client.proposals.apply(proposalId, { version: current.version }, {
      ifMatch: proposalEtag(current),
      idempotencyKey: true,
    })
    if (applied.data.artifactId !== current.artifactId) {
      throw new Error('Proposal apply returned a draft for a different artifact.')
    }
    // The apply endpoint returns only the authoritative draft. Mirror the
    // server's deterministic ready -> applied transition until revalidation.
    const appliedProposal: ProposalDto = {
      ...current,
      status: current.operations.some((operation) => operation.decision === 'rejected')
        ? 'partially_applied'
        : 'applied',
      operations: current.operations.map((operation) => operation.decision === 'accepted'
        ? { ...operation, decision: 'applied' as const }
        : operation),
      version: current.version + 1,
    }
    return { ...applied, appliedProposal }
  }

  decideProposalOperation(
    proposal: Pick<ProposalDto, 'id' | 'version'>,
    operationId: string,
    decision: 'accepted' | 'rejected',
    reason?: string,
  ) {
    return this.client.proposals.decide(proposal.id, {
      operationId,
      decision,
      ...(reason ? { reason } : {}),
      version: proposal.version,
    }, {
      ifMatch: proposalEtag(proposal),
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
