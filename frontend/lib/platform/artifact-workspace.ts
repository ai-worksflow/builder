import type { CollaborationPlatformClient } from '../collaboration/platform-adapter'
import type {
  ArtifactDependencyDto,
  ArtifactReviewGateDto,
  ArtifactRevisionDto,
  BlueprintContentDto,
  CreateProposalInputDto,
  DocumentContentDto,
  ImpactReportDto,
  PageSpecContentDto,
  ProposalDto,
  PrototypeContentDto,
  TraceLinkDto,
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

export interface ArtifactDetails<TContent> {
  readonly versions: readonly ArtifactRevisionDto<TContent>[]
  readonly dependencies: readonly ArtifactDependencyDto[]
  readonly reviewGate: ArtifactReviewGateDto
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
      revisionNumber: number
      contentHash: string
    }[],
    content: BlueprintContentDto,
  ) {
    return this.client.blueprints.create(projectId, {
      title,
      requirementVersions,
      content,
    })
  }

  createPageSpec(
    projectId: string,
    title: string,
    blueprintRevision: {
      artifactId: string
      revisionId: string
      revisionNumber: number
      contentHash: string
    },
    blueprintPageNodeId: string,
    content: PageSpecContentDto,
  ) {
    return this.client.pageSpecs.create(projectId, {
      title,
      blueprintRevision,
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
      revisionNumber: number
      contentHash: string
    },
    content: PrototypeContentDto,
  ) {
    return this.client.prototypes.create(projectId, {
      title,
      pageSpecRevision,
      exploratory: content.exploratory,
      content,
    })
  }

  async saveDocumentDraft(
    artifactId: string,
    content: DocumentContentDto,
    etag: string,
    sourceVersions: readonly {
      artifactId: string
      revisionId: string
      revisionNumber: number
      contentHash: string
    }[] = [],
  ) {
    try {
      return await this.client.documents.updateDraft(
        artifactId,
        { content, sourceVersions },
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
    sourceVersions: readonly {
      artifactId: string
      revisionId: string
      revisionNumber: number
      contentHash: string
    }[] = [],
  ) {
    try {
      return await this.client.blueprints.updateDraft(
        artifactId,
        { content, sourceVersions },
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
    sourceVersions: readonly {
      artifactId: string
      revisionId: string
      revisionNumber: number
      contentHash: string
    }[] = [],
  ) {
    try {
      const pinnedSources = sourceVersions.length > 0
        ? sourceVersions
        : [content.pageSpecRevision]
      return await this.client.prototypes.updateDraft(
        artifactId,
        { content, sourceVersions: pinnedSources },
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
      this.client.artifacts.reviewGate(artifactId).catch((error) => {
        if (error instanceof PlatformHttpError && error.status === 404) {
          return {
            data: {
              passed: false,
              checks: [{
                code: 'review_gate_unavailable',
                severity: 'warning' as const,
                message: 'The server review-gate endpoint is not available yet.',
              }],
              unresolvedBlockingCommentIds: [],
              traceCoverage: 0,
            },
          }
        }
        throw error
      }),
    ])
    return {
      versions: versions.data.items,
      dependencies: dependencies.data.items,
      reviewGate: reviewGate.data,
    }
  }

  createProposal(projectId: string, input: CreateProposalInputDto) {
    return this.client.proposals.create(projectId, input)
  }

  applyProposal(proposalId: string, operationIndexes: readonly number[], etag: string) {
    return this.client.proposals.apply(
      proposalId,
      { operationIndexes },
      { ifMatch: etag, idempotencyKey: true },
    )
  }

  rejectProposal(proposalId: string, reason: string, etag: string) {
    return this.client.proposals.reject(
      proposalId,
      reason,
      { ifMatch: etag, idempotencyKey: true },
    )
  }

  impact(blueprintArtifactId: string): Promise<{ data: ImpactReportDto }> {
    return this.client.blueprints.impact(blueprintArtifactId)
  }
}
