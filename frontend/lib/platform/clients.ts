import type {
  AddProjectMemberInputDto,
  AdvanceProposalInputDto,
  AdvanceProposalResultDto,
  ApplyProposalInputDto,
  ArtifactGenerationResultDto,
  AuditEventDto,
  ArtifactDependencyDto,
  ArtifactDto,
  ArtifactDraftDto,
  ArtifactReviewGateDto,
  ArtifactRevisionDto,
  ArtifactStatus,
  BlueprintContentDto,
  CommentDto,
  CreateArtifactInputDto,
  CreateBlueprintInputDto,
  CreateCommentInputDto,
  CreateDependencyInputDto,
  CreateDocumentInputDto,
  CreatePageSpecInputDto,
  CreateProjectInputDto,
  CreateProjectInvitationInputDto,
  CreateProposalInputDto,
  CreatePrototypeInputDto,
  CreateReviewInputDto,
  CreateRevisionInputDto,
  CreateTraceLinkInputDto,
  CreateWorkbenchBundleInputDto,
  CreateWorkflowInputDto,
  DecideProposalInputDto,
  DecideReviewInputDto,
  DocumentGraphDto,
  DocumentMemberBindingInputDto,
  DocumentMemberBindingSetDto,
  DocumentContentDto,
  CreateDocumentSyncBackInputDto,
  GenerateDownstreamDocumentInputDto,
  ImpactReportDto,
  JsonValue,
  NotificationDto,
  PageDto,
  PageSpecContentDto,
  PresenceDto,
  ProjectAuthorizationDto,
  ProjectDto,
  ProjectInvitationDto,
  ProjectMemberDto,
  ProposalDto,
  ProposalStatus,
  PrototypeContentDto,
  ReviewDto,
  RunDto,
  RunEventDto,
  RunStatus,
  SessionDto,
  SessionSignInInputDto,
  SessionSignUpInputDto,
  StartRunInputDto,
  TraceLinkDto,
  TraceMatrixDto,
  UpdateArtifactDraftInputDto,
  UpdateDraftInputDto,
  UpdateProjectInputDto,
  UpdateProjectMemberInputDto,
  VersionedArtifactDto,
  VersionRefDto,
  WorkbenchBundleDto,
  WorkflowDto,
} from './dto'
import type { CreateInputManifestDto, InputManifestDto } from './flow-contract'
import type { HttpRequestOptions, HttpResult, QueryValue } from './http'
import { HttpClient } from './http'
import { wireVersionRef } from './wire-version-ref'
import { normalizeWorkbenchBundle } from './workbench-normalization'

export interface ClientRequestOptions {
  readonly signal?: AbortSignal
  readonly requestId?: string
}

export interface ClientMutationOptions extends ClientRequestOptions {
  readonly ifMatch?: string
  readonly idempotencyKey?: string | true
}

export interface ListOptions extends ClientRequestOptions {
  readonly cursor?: string
  readonly limit?: number
}

function segment(value: string) {
  return encodeURIComponent(value)
}

function requestOptions(options?: ClientRequestOptions) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
  }
}

function mutationOptions(options?: ClientMutationOptions, idempotentByDefault = false) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
    ifMatch: options?.ifMatch,
    idempotencyKey: options?.idempotencyKey ?? (idempotentByDefault ? true : undefined),
  }
}

function listQuery(
  options?: ListOptions,
  additional?: Readonly<Record<string, QueryValue | readonly QueryValue[]>>,
) {
  return {
    ...additional,
    cursor: options?.cursor,
    limit: options?.limit,
  }
}

type VersionedCommandInput = {
  readonly sourceVersions?: readonly import('./dto').VersionRefDto[]
  readonly requirementVersions?: readonly import('./dto').VersionRefDto[]
  readonly blueprintRevision?: import('./dto').VersionRefDto
  readonly pageSpecRevision?: import('./dto').VersionRefDto
  readonly content?: unknown
}

function wireArtifactContent<T>(content: T): T {
  if (!content || typeof content !== 'object' || Array.isArray(content)) return content
  const value = content as Record<string, unknown>
  const next: Record<string, unknown> = { ...value }

  if (isVersionRef(value.pageSpecRevision)) {
    next.pageSpecRevision = wireVersionRef(value.pageSpecRevision)
  }
  if (Array.isArray(value.pageSpecRefs)) {
    next.pageSpecRefs = value.pageSpecRefs.map((reference) =>
      isVersionRef(reference) ? wireVersionRef(reference) : reference)
  }
  if (value.layers && typeof value.layers === 'object' && !Array.isArray(value.layers)) {
    next.layers = Object.fromEntries(Object.entries(value.layers).map(([id, layer]) => {
      if (!layer || typeof layer !== 'object' || Array.isArray(layer)) return [id, layer]
      const record = layer as Record<string, unknown>
      return [id, isVersionRef(record.componentRef)
        ? { ...record, componentRef: wireVersionRef(record.componentRef) }
        : record]
    }))
  }
  if (Array.isArray(value.traceLinks)) {
    next.traceLinks = value.traceLinks.map((link) => {
      if (!link || typeof link !== 'object' || Array.isArray(link)) return link
      const record = link as Record<string, unknown>
      return {
        ...record,
        source: wireTraceEndpoint(record.source),
        target: wireTraceEndpoint(record.target),
      }
    })
  }
  return next as T
}

function wireTraceEndpoint(endpoint: unknown) {
  if (!endpoint || typeof endpoint !== 'object' || Array.isArray(endpoint)) return endpoint
  const record = endpoint as Record<string, unknown>
  return isVersionRef(record.version)
    ? { ...record, version: wireVersionRef(record.version) }
    : record
}

function isVersionRef(value: unknown): value is import('./wire-version-ref').WireVersionRef {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as Record<string, unknown>
  return typeof record.artifactId === 'string' &&
    typeof record.revisionId === 'string' &&
    typeof record.contentHash === 'string'
}

function wireVersionedCommand<T>(input: T): T {
  const value = input as T & VersionedCommandInput
  return {
    ...value,
    ...(value.sourceVersions
      ? { sourceVersions: value.sourceVersions.map(wireVersionRef) }
      : {}),
    ...(value.requirementVersions
      ? { requirementVersions: value.requirementVersions.map(wireVersionRef) }
      : {}),
    ...(value.blueprintRevision
      ? { blueprintRevision: wireVersionRef(value.blueprintRevision) }
      : {}),
    ...(value.pageSpecRevision
      ? { pageSpecRevision: wireVersionRef(value.pageSpecRevision) }
      : {}),
    ...('content' in value ? { content: wireArtifactContent(value.content) } : {}),
  } as T
}

function wireGenericArtifactCommand<T extends {
  readonly sourceVersions?: readonly {
    readonly version: import('./dto').VersionRefDto
    readonly purpose: string
    readonly required: boolean
  }[]
}>(input: T): T {
  return {
    ...input,
    ...(input.sourceVersions
      ? {
          sourceVersions: input.sourceVersions.map((source) => ({
            ...source,
            version: wireVersionRef(source.version),
          })),
        }
      : {}),
  } as T
}

function wireCommentInput(input: CreateCommentInputDto): CreateCommentInputDto {
  return {
    ...input,
    target: input.target ? wireVersionRef(input.target) : undefined,
    anchor: input.anchor
      ? {
          ...input.anchor,
          revision: input.anchor.revision
            ? wireVersionRef(input.anchor.revision)
            : undefined,
        }
      : undefined,
  }
}

abstract class DomainClient {
  protected readonly http: HttpClient

  constructor(http: HttpClient) {
    this.http = http
  }
}

export class SessionClient extends DomainClient {
  get(options?: ClientRequestOptions) {
    return this.http.get<SessionDto>('/v1/session', requestOptions(options))
  }

  signIn(input: SessionSignInInputDto, options?: ClientMutationOptions) {
    return this.http.post<SessionDto, SessionSignInInputDto>(
      '/v1/session',
      input,
      mutationOptions(options, true),
    )
  }

  signUp(input: SessionSignUpInputDto, options?: ClientMutationOptions) {
    return this.http.post<SessionDto, SessionSignUpInputDto>(
      '/v1/session/register',
      input,
      mutationOptions(options, true),
    )
  }

  refresh(options?: ClientMutationOptions) {
    return this.http.post<SessionDto>('/v1/session/refresh', undefined, mutationOptions(options, true))
  }

  signOut(options?: ClientMutationOptions) {
    return this.http.delete<void>('/v1/session', {
      ...mutationOptions(options),
      responseType: 'void',
      clearCsrfOnSuccess: true,
    })
  }
}

export class ProjectsClient extends DomainClient {
  list(options?: ListOptions) {
    return this.http.get<PageDto<ProjectDto>>('/v1/projects', {
      ...requestOptions(options),
      query: listQuery(options),
    })
  }

  get(projectId: string, options?: ClientRequestOptions) {
    return this.http.get<ProjectDto>(`/v1/projects/${segment(projectId)}`, requestOptions(options))
  }

  create(input: CreateProjectInputDto, options?: ClientMutationOptions) {
    return this.http.post<ProjectDto, CreateProjectInputDto>(
      '/v1/projects',
      input,
      mutationOptions(options, true),
    )
  }

  update(projectId: string, input: UpdateProjectInputDto, options: ClientMutationOptions) {
    return this.http.patch<ProjectDto, UpdateProjectInputDto>(
      `/v1/projects/${segment(projectId)}`,
      input,
      mutationOptions(options),
    )
  }

  remove(projectId: string, options: ClientMutationOptions) {
    return this.http.delete<void>(`/v1/projects/${segment(projectId)}`, {
      ...mutationOptions(options),
      responseType: 'void',
    })
  }

  authorize(
    projectId: string,
    action: ProjectAuthorizationDto['action'],
    options?: ClientRequestOptions,
  ) {
    return this.http.get<ProjectAuthorizationDto>(
      `/v1/projects/${segment(projectId)}/authorization`,
      { ...requestOptions(options), query: { action } },
    )
  }
}

export class MembersClient extends DomainClient {
  list(projectId: string, options?: ListOptions) {
    return this.http.get<PageDto<ProjectMemberDto>>(
      `/v1/projects/${segment(projectId)}/members`,
      { ...requestOptions(options), query: listQuery(options) },
    )
  }

  add(projectId: string, input: AddProjectMemberInputDto, options?: ClientMutationOptions) {
    return this.http.post<ProjectMemberDto, AddProjectMemberInputDto>(
      `/v1/projects/${segment(projectId)}/members`,
      input,
      mutationOptions(options, true),
    )
  }

  update(
    projectId: string,
    userId: string,
    input: UpdateProjectMemberInputDto,
    options: ClientMutationOptions,
  ) {
    return this.http.patch<ProjectMemberDto, UpdateProjectMemberInputDto>(
      `/v1/projects/${segment(projectId)}/members/${segment(userId)}`,
      input,
      mutationOptions(options),
    )
  }

  remove(projectId: string, userId: string, options: ClientMutationOptions) {
    return this.http.delete<void>(
      `/v1/projects/${segment(projectId)}/members/${segment(userId)}`,
      { ...mutationOptions(options), responseType: 'void' },
    )
  }

  invite(
    projectId: string,
    input: CreateProjectInvitationInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ProjectInvitationDto, CreateProjectInvitationInputDto>(
      `/v1/projects/${segment(projectId)}/invitations`,
      input,
      mutationOptions(options, true),
    )
  }

  acceptInvitation(token: string, options?: ClientMutationOptions) {
    return this.http.post<ProjectMemberDto, { readonly token: string }>(
      '/v1/invitations/accept',
      { token },
      mutationOptions(options, true),
    )
  }
}

export class ArtifactsClient extends DomainClient {
  compileRequirementBaseline(
    projectId: string,
    sources: readonly VersionRefDto[],
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ArtifactRevisionDto<JsonValue>, { readonly sources: readonly VersionRefDto[] }>(
      `/v1/projects/${segment(projectId)}/requirement-baselines`,
      { sources: sources.map(wireVersionRef) },
      mutationOptions(options, true),
    )
  }

  list(
    projectId: string,
    filters: { readonly kind?: string; readonly status?: ArtifactStatus } = {},
    options?: ListOptions,
  ) {
    return this.http.get<PageDto<ArtifactDto>>(`/v1/projects/${segment(projectId)}/artifacts`, {
      ...requestOptions(options),
      query: listQuery(options, filters),
    })
  }

  get<TContent = JsonValue>(artifactId: string, options?: ClientRequestOptions) {
    return this.http.get<VersionedArtifactDto<TContent>>(
      `/v1/artifacts/${segment(artifactId)}`,
      requestOptions(options),
    )
  }

  create<TContent = JsonValue>(
    projectId: string,
    input: CreateArtifactInputDto<TContent>,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<VersionedArtifactDto<TContent>, CreateArtifactInputDto<TContent>>(
      `/v1/projects/${segment(projectId)}/artifacts`,
      wireGenericArtifactCommand(input),
      mutationOptions(options, true),
    )
  }

  listRevisions<TContent = JsonValue>(artifactId: string, options?: ListOptions) {
    return this.http.get<PageDto<ArtifactRevisionDto<TContent>>>(
      `/v1/artifacts/${segment(artifactId)}/revisions`,
      { ...requestOptions(options), query: listQuery(options) },
    )
  }

  getRevision<TContent = JsonValue>(revisionId: string, options?: ClientRequestOptions) {
    return this.http.get<ArtifactRevisionDto<TContent>>(
      `/v1/revisions/${segment(revisionId)}`,
      requestOptions(options),
    )
  }

  listDependencies(artifactId: string, options?: ListOptions) {
    return this.http.get<PageDto<ArtifactDependencyDto>>(
      `/v1/artifacts/${segment(artifactId)}/dependencies`,
      { ...requestOptions(options), query: listQuery(options) },
    )
  }

  createDependency(
    projectId: string,
    input: CreateDependencyInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ArtifactDependencyDto, CreateDependencyInputDto>(
      `/v1/projects/${segment(projectId)}/dependencies`,
      {
        ...input,
        source: wireVersionRef(input.source),
        target: wireVersionRef(input.target),
      },
      mutationOptions(options, true),
    )
  }

  getVersioned<TContent = JsonValue>(artifactId: string, options?: ClientRequestOptions) {
    return this.http.get<VersionedArtifactDto<TContent>>(
      `/v1/artifacts/${segment(artifactId)}`,
      requestOptions(options),
    )
  }

  getDraft<TContent = JsonValue>(artifactId: string, options?: ClientRequestOptions) {
    return this.http.get<ArtifactDraftDto<TContent>>(
      `/v1/artifacts/${segment(artifactId)}/draft`,
      requestOptions(options),
    )
  }

  updateDraft<TContent>(
    draftId: string,
    input: UpdateArtifactDraftInputDto<TContent>,
    options: ClientMutationOptions,
  ) {
    return this.http.patch<ArtifactDraftDto<TContent>, UpdateArtifactDraftInputDto<TContent>>(
      `/v1/drafts/${segment(draftId)}`,
      wireGenericArtifactCommand(input),
      mutationOptions(options),
    )
  }

  createRevision<TContent>(
    artifactId: string,
    input: CreateRevisionInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ArtifactRevisionDto<TContent>, CreateRevisionInputDto>(
      `/v1/artifacts/${segment(artifactId)}/revisions`,
      input,
      mutationOptions(options, true),
    )
  }

  reviewGate(artifactId: string, options?: ClientRequestOptions) {
    return this.http.get<ArtifactReviewGateDto>(
      `/v1/artifacts/${segment(artifactId)}/review-gate`,
      requestOptions(options),
    )
  }
}

abstract class VersionedDomainClient<TContent, TCreate> extends DomainClient {
  protected abstract readonly collection: string

  list(projectId: string, options?: ListOptions) {
    return this.http.get<PageDto<VersionedArtifactDto<TContent>>>(
      `/v1/projects/${segment(projectId)}/${this.collection}`,
      { ...requestOptions(options), query: listQuery(options) },
    )
  }

  get(artifactId: string, options?: ClientRequestOptions) {
    return this.http.get<VersionedArtifactDto<TContent>>(
      `/v1/${this.collection}/${segment(artifactId)}`,
      requestOptions(options),
    )
  }

  create(projectId: string, input: TCreate, options?: ClientMutationOptions) {
    return this.http.post<VersionedArtifactDto<TContent>, TCreate>(
      `/v1/projects/${segment(projectId)}/${this.collection}`,
      wireVersionedCommand(input),
      mutationOptions(options, true),
    )
  }

  updateDraft(
    artifactId: string,
    input: UpdateDraftInputDto<TContent>,
    options: ClientMutationOptions,
  ) {
    return this.http.patch<VersionedArtifactDto<TContent>, UpdateDraftInputDto<TContent>>(
      `/v1/${this.collection}/${segment(artifactId)}/draft`,
      wireVersionedCommand(input),
      mutationOptions(options),
    )
  }

  createRevision(
    artifactId: string,
    input: CreateRevisionInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ArtifactRevisionDto<TContent>, CreateRevisionInputDto>(
      `/v1/${this.collection}/${segment(artifactId)}/revisions`,
      input,
      mutationOptions(options, true),
    )
  }
}

export class DocumentsClient extends VersionedDomainClient<DocumentContentDto, CreateDocumentInputDto> {
  protected readonly collection = 'documents'

  graph(projectId: string, options?: ClientRequestOptions) {
    return this.http.get<DocumentGraphDto>(
      `/v1/projects/${segment(projectId)}/document-graph`,
      requestOptions(options),
    )
  }

  memberBindings(artifactId: string, options?: ClientRequestOptions) {
    return this.http.get<DocumentMemberBindingSetDto>(
      `/v1/artifacts/${segment(artifactId)}/member-bindings`,
      requestOptions(options),
    )
  }

  replaceMemberBindings(
    artifactId: string,
    items: readonly DocumentMemberBindingInputDto[],
    options: ClientMutationOptions,
  ) {
    return this.http.put<DocumentMemberBindingSetDto, { readonly items: readonly DocumentMemberBindingInputDto[] }>(
      `/v1/artifacts/${segment(artifactId)}/member-bindings`,
      { items },
      mutationOptions(options, true),
    )
  }

  generateDownstream(
    projectId: string,
    input: GenerateDownstreamDocumentInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<{
      readonly document: VersionedArtifactDto<DocumentContentDto>
      readonly inputManifest: InputManifestDto
      readonly proposal: ProposalDto
      readonly provider: string
      readonly model: string
      readonly commandId: string
      readonly replayed?: boolean
      readonly resolvedOwnerIds: readonly string[]
    }, GenerateDownstreamDocumentInputDto>(
      `/v1/projects/${segment(projectId)}/documents/generate-downstream`,
      { ...input, sourceRevision: wireVersionRef(input.sourceRevision) },
      mutationOptions(options, true),
    )
  }

  createSyncBackProposal(
    projectId: string,
    input: CreateDocumentSyncBackInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<{
      readonly inputManifest: InputManifestDto
      readonly proposal: ProposalDto
      readonly provenance: CreateDocumentSyncBackInputDto['provenance']
      readonly workspaceSource?: VersionRefDto
      readonly previewUrl?: string
      readonly provider: string
      readonly model: string
    }, CreateDocumentSyncBackInputDto>(
      `/v1/projects/${segment(projectId)}/documents/sync-back`,
      { ...input, targetRevision: wireVersionRef(input.targetRevision) },
      mutationOptions(options, true),
    )
  }
}

export class BlueprintsClient extends VersionedDomainClient<BlueprintContentDto, CreateBlueprintInputDto> {
  protected readonly collection = 'blueprints'

  impact(artifactId: string, options?: ClientRequestOptions) {
    return this.http.get<ImpactReportDto>(
      `/v1/blueprints/${segment(artifactId)}/impact`,
      requestOptions(options),
    )
  }
}

export class PageSpecsClient extends VersionedDomainClient<PageSpecContentDto, CreatePageSpecInputDto> {
  protected readonly collection = 'page-specs'
}

export class PrototypesClient extends VersionedDomainClient<PrototypeContentDto, CreatePrototypeInputDto> {
  protected readonly collection = 'prototypes'
}

export class ReviewsClient extends DomainClient {
  list(
    projectId: string,
    targetArtifactId?: string,
    options?: ListOptions,
  ) {
    return this.http.get<PageDto<ReviewDto>>(`/v1/projects/${segment(projectId)}/reviews`, {
      ...requestOptions(options),
      query: listQuery(options, { artifactId: targetArtifactId }),
    })
  }

  create(projectId: string, input: CreateReviewInputDto, options?: ClientMutationOptions) {
    return this.http.post<ReviewDto, CreateReviewInputDto>(
      `/v1/projects/${segment(projectId)}/reviews`,
      { ...input, target: wireVersionRef(input.target) },
      mutationOptions(options, true),
    )
  }

  decide(reviewId: string, input: DecideReviewInputDto, options: ClientMutationOptions) {
    return this.http.post<ReviewDto, DecideReviewInputDto>(
      `/v1/reviews/${segment(reviewId)}/decision`,
      input,
      mutationOptions(options, true),
    )
  }
}

export class CommentsClient extends DomainClient {
  listProject(projectId: string, options?: ListOptions) {
    return this.http.get<PageDto<CommentDto>>(`/v1/projects/${segment(projectId)}/comments`, {
      ...requestOptions(options),
      query: listQuery(options),
    })
  }

  createProject(
    projectId: string,
    input: CreateCommentInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<CommentDto, CreateCommentInputDto>(
      `/v1/projects/${segment(projectId)}/comments`,
      wireCommentInput(input),
      mutationOptions(options, true),
    )
  }

  list(artifactId: string, options?: ListOptions) {
    return this.http.get<PageDto<CommentDto>>(`/v1/artifacts/${segment(artifactId)}/comments`, {
      ...requestOptions(options),
      query: listQuery(options),
    })
  }

  create(artifactId: string, input: CreateCommentInputDto, options?: ClientMutationOptions) {
    return this.http.post<CommentDto, CreateCommentInputDto>(
      `/v1/artifacts/${segment(artifactId)}/comments`,
      wireCommentInput(input),
      mutationOptions(options, true),
    )
  }

  resolve(commentId: string, resolved: boolean, options: ClientMutationOptions) {
    return this.http.patch<CommentDto, { readonly resolved: boolean }>(
      `/v1/comments/${segment(commentId)}`,
      { resolved },
      mutationOptions(options),
    )
  }
}

export class NotificationsClient extends DomainClient {
  list(projectId?: string, options?: ListOptions) {
    return this.http.get<PageDto<NotificationDto>>('/v1/notifications', {
      ...requestOptions(options),
      query: listQuery(options, { projectId }),
    })
  }

  mark(notificationId: string, read: boolean, options: ClientMutationOptions) {
    return this.http.patch<NotificationDto, { readonly read: boolean }>(
      `/v1/notifications/${segment(notificationId)}`,
      { read },
      mutationOptions(options),
    )
  }
}

export class AuditClient extends DomainClient {
  list(projectId: string, options?: ListOptions) {
    return this.http.get<PageDto<AuditEventDto>>(`/v1/projects/${segment(projectId)}/audit`, {
      ...requestOptions(options),
      query: listQuery(options),
    })
  }
}

export class PresenceClient extends DomainClient {
  list(projectId: string, options?: ListOptions) {
    return this.http.get<PageDto<PresenceDto>>(`/v1/projects/${segment(projectId)}/presence`, {
      ...requestOptions(options),
      query: listQuery(options),
    })
  }

  heartbeat(projectId: string, artifactId?: string, options?: ClientMutationOptions) {
    return this.http.post<PresenceDto, { readonly artifactId?: string }>(
      `/v1/projects/${segment(projectId)}/presence/heartbeat`,
      { artifactId },
      mutationOptions(options, true),
    )
  }
}

export class ProposalsClient extends DomainClient {
  create(projectId: string, input: CreateProposalInputDto, options?: ClientMutationOptions) {
    return this.http.post<ProposalDto, CreateProposalInputDto>(
      `/v1/projects/${segment(projectId)}/output-proposals`,
      input,
      mutationOptions(options, true),
    )
  }

  list(
    projectId: string,
    filters: { readonly artifactId?: string; readonly status?: ProposalStatus } = {},
    options?: ListOptions,
  ) {
    return this.http.get<PageDto<ProposalDto>>(`/v1/projects/${segment(projectId)}/output-proposals`, {
      ...requestOptions(options),
      query: listQuery(options, filters),
    })
  }

  get(proposalId: string, options?: ClientRequestOptions) {
    return this.http.get<ProposalDto>(`/v1/output-proposals/${segment(proposalId)}`, requestOptions(options))
  }

  decide(
    proposalId: string,
    input: DecideProposalInputDto,
    options: ClientMutationOptions,
  ) {
    return this.http.post<ProposalDto, DecideProposalInputDto>(
      `/v1/output-proposals/${segment(proposalId)}/decisions`,
      input,
      mutationOptions(options, true),
    )
  }

  apply(
    proposalId: string,
    input: ApplyProposalInputDto,
    options: ClientMutationOptions,
  ) {
    return this.http.post<ArtifactDraftDto<JsonValue>, ApplyProposalInputDto>(
      `/v1/output-proposals/${segment(proposalId)}/apply`,
      input,
      mutationOptions(options, true),
    )
  }

  advance(
    proposalId: string,
    input: AdvanceProposalInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<AdvanceProposalResultDto, AdvanceProposalInputDto>(
      `/v1/output-proposals/${segment(proposalId)}/advance`,
      input,
      mutationOptions(options, true),
    )
  }

}

export class WorkflowsClient extends DomainClient {
  list(projectId: string, options?: ListOptions) {
    return this.http.get<PageDto<WorkflowDto>>(`/v1/projects/${segment(projectId)}/workflows`, {
      ...requestOptions(options),
      query: listQuery(options),
    })
  }

  get(workflowId: string, options?: ClientRequestOptions) {
    return this.http.get<WorkflowDto>(`/v1/workflows/${segment(workflowId)}`, requestOptions(options))
  }

  create(projectId: string, input: CreateWorkflowInputDto, options?: ClientMutationOptions) {
    return this.http.post<WorkflowDto, CreateWorkflowInputDto>(
      `/v1/projects/${segment(projectId)}/workflows`,
      input,
      mutationOptions(options, true),
    )
  }

  update(
    workflowId: string,
    input: Partial<CreateWorkflowInputDto> & { readonly enabled?: boolean },
    options: ClientMutationOptions,
  ) {
    return this.http.patch<WorkflowDto, typeof input>(
      `/v1/workflows/${segment(workflowId)}`,
      input,
      mutationOptions(options),
    )
  }
}

export class ManifestsClient extends DomainClient {
  create(projectId: string, input: CreateInputManifestDto, options?: ClientMutationOptions) {
    return this.http.post<InputManifestDto, CreateInputManifestDto>(
      `/v1/projects/${segment(projectId)}/input-manifests`,
      {
        ...input,
        baseRevision: input.baseRevision ? wireVersionRef(input.baseRevision) : undefined,
        sources: input.sources.map((source) => ({
          ...source,
          ref: wireVersionRef(source.ref),
        })),
      },
      mutationOptions(options, true),
    )
  }

  get(manifestId: string, options?: ClientRequestOptions) {
    return this.http.get<InputManifestDto>(
      `/v1/input-manifests/${segment(manifestId)}`,
      requestOptions(options),
    )
  }

  generateArtifactProposal(
    manifestId: string,
    model: string,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<ArtifactGenerationResultDto, { readonly model: string }>(
      `/v1/input-manifests/${segment(manifestId)}/generate`,
      { model },
      mutationOptions(options, true),
    )
  }
}

export class RunsClient extends DomainClient {
  list(
    projectId: string,
    filters: { readonly status?: RunStatus; readonly workflowId?: string } = {},
    options?: ListOptions,
  ) {
    return this.http.get<PageDto<RunDto>>(`/v1/projects/${segment(projectId)}/runs`, {
      ...requestOptions(options),
      query: listQuery(options, filters),
    })
  }

  get(runId: string, options?: ClientRequestOptions) {
    return this.http.get<RunDto>(`/v1/runs/${segment(runId)}`, requestOptions(options))
  }

  start(projectId: string, input: StartRunInputDto, options?: ClientMutationOptions) {
    return this.http.post<RunDto, StartRunInputDto>(
      `/v1/projects/${segment(projectId)}/runs`,
      input,
      mutationOptions(options, true),
    )
  }

  cancel(runId: string, options?: ClientMutationOptions) {
    return this.http.post<RunDto>(
      `/v1/runs/${segment(runId)}/cancel`,
      undefined,
      mutationOptions(options, true),
    )
  }

  events(runId: string, after?: number, options?: ListOptions) {
    return this.http.get<PageDto<RunEventDto>>(`/v1/runs/${segment(runId)}/events`, {
      ...requestOptions(options),
      query: listQuery(options, { after }),
    })
  }
}

export class WorkbenchClient extends DomainClient {
  async createBundle(
    projectId: string,
    input: CreateWorkbenchBundleInputDto,
    options?: ClientMutationOptions,
  ) {
    const result = await this.http.post<WorkbenchBundleDto, CreateWorkbenchBundleInputDto>(
      `/v1/projects/${segment(projectId)}/build-manifests`,
      { ...input, prototypeRevision: wireVersionRef(input.prototypeRevision) },
      mutationOptions(options, true),
    )
    return { ...result, data: normalizeWorkbenchBundle(result.data) }
  }

  async getBundle(bundleId: string, options?: ClientRequestOptions) {
    const result = await this.http.get<WorkbenchBundleDto>(
      `/v1/build-manifests/${segment(bundleId)}`,
      requestOptions(options),
    )
    return { ...result, data: normalizeWorkbenchBundle(result.data) }
  }
}

export class TracesClient extends DomainClient {
  list(projectId: string, options?: ListOptions) {
    return this.http.get<PageDto<TraceLinkDto>>(`/v1/projects/${segment(projectId)}/traces`, {
      ...requestOptions(options),
      query: listQuery(options),
    })
  }

  create(projectId: string, trace: CreateTraceLinkInputDto, options?: ClientMutationOptions) {
    return this.http.post<TraceLinkDto, CreateTraceLinkInputDto>(
      `/v1/projects/${segment(projectId)}/traces`,
      {
        ...trace,
        source: wireVersionRef(trace.source),
        target: wireVersionRef(trace.target),
      },
      mutationOptions(options, true),
    )
  }

  matrix(projectId: string, options?: ClientRequestOptions) {
    return this.http.get<TraceMatrixDto>(
      `/v1/projects/${segment(projectId)}/trace-matrix`,
      requestOptions(options),
    )
  }
}

export interface PlatformDomainClients {
  readonly session: SessionClient
  readonly projects: ProjectsClient
  readonly members: MembersClient
  readonly artifacts: ArtifactsClient
  readonly documents: DocumentsClient
  readonly blueprints: BlueprintsClient
  readonly pageSpecs: PageSpecsClient
  readonly prototypes: PrototypesClient
  readonly reviews: ReviewsClient
  readonly comments: CommentsClient
  readonly notifications: NotificationsClient
  readonly audit: AuditClient
  readonly presence: PresenceClient
  readonly proposals: ProposalsClient
  readonly workflows: WorkflowsClient
  readonly manifests: ManifestsClient
  readonly runs: RunsClient
  readonly workbench: WorkbenchClient
  readonly traces: TracesClient
}

export function createPlatformDomainClients(http: HttpClient): PlatformDomainClients {
  return {
    session: new SessionClient(http),
    projects: new ProjectsClient(http),
    members: new MembersClient(http),
    artifacts: new ArtifactsClient(http),
    documents: new DocumentsClient(http),
    blueprints: new BlueprintsClient(http),
    pageSpecs: new PageSpecsClient(http),
    prototypes: new PrototypesClient(http),
    reviews: new ReviewsClient(http),
    comments: new CommentsClient(http),
    notifications: new NotificationsClient(http),
    audit: new AuditClient(http),
    presence: new PresenceClient(http),
    proposals: new ProposalsClient(http),
    workflows: new WorkflowsClient(http),
    manifests: new ManifestsClient(http),
    runs: new RunsClient(http),
    workbench: new WorkbenchClient(http),
    traces: new TracesClient(http),
  }
}

export type PlatformHttpResult<T> = HttpResult<T>
export type PlatformHttpOptions<TBody = unknown> = HttpRequestOptions<TBody>
