import type { ClientMutationOptions, ClientRequestOptions } from './clients'
import { HttpClient, PlatformProtocolError, type HttpResult } from './http'
import {
  normalizeCandidateRebaseConflictContent,
  normalizeCandidateRebaseResult,
  normalizeRepositoryCandidateHeadList,
  normalizeRepositoryCandidateBootstrap,
  parseRepositorySnapshotReceipt,
  parseRepositoryCandidateSearchResult,
  type CandidateRebaseConflictContentDto,
  type CandidateRebaseResolutionStrategy,
  type CandidateRebaseResultDto,
  RepositoryContractError,
  type RepositoryCandidateHeadListDto,
  type RepositoryCandidateBootstrapDto,
  type RepositoryCandidateSearchInputDto,
  type RepositoryCandidateSearchResultDto,
  type RepositorySnapshotReceiptDto,
} from './repository-contract'
import {
  normalizeCandidateWorkspace,
  type CandidateWorkspaceDto,
} from './sandbox-contract'

function segment(value: string) {
  return encodeURIComponent(value)
}

export class RepositoryClient {
  constructor(private readonly http: HttpClient) {}

  async bootstrapCandidate(
    projectId: string,
    buildManifestId: string,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<RepositoryCandidateBootstrapDto>> {
    const result = await this.http.post<unknown, { buildManifestId: string }>(
      `/v1/projects/${segment(projectId)}/repository-candidates`,
      { buildManifestId },
      {
        signal: options?.signal,
        requestId: options?.requestId,
        idempotencyKey: options?.idempotencyKey ?? true,
      },
    )
    let data: RepositoryCandidateBootstrapDto
    try {
      data = await normalizeRepositoryCandidateBootstrap(result.data)
    } catch (cause) {
      if (!(cause instanceof RepositoryContractError)) throw cause
      throw new PlatformProtocolError(cause.message, result.requestId, result.status)
    }
    if (
      data.repositorySnapshotReceipt.snapshot.projectId !== projectId
      || data.repositorySnapshotReceipt.snapshot.buildManifest.id !== buildManifestId
    ) {
      throw new PlatformProtocolError(
        'The repository service returned bootstrap evidence for a different exact project or BuildManifest.',
        result.requestId,
        result.status,
      )
    }
    return { ...result, data }
  }

  async getCandidate(
    projectId: string,
    candidateId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<CandidateWorkspaceDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/repository-candidates/${segment(candidateId)}`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    return { ...result, data: normalizeCandidateWorkspace(result.data) }
  }

  async getRepositorySnapshot(
    projectId: string,
    snapshotId: string,
    contentHash: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<RepositorySnapshotReceiptDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/repository-snapshots/${segment(snapshotId)}?contentHash=${segment(contentHash)}`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    let data: RepositorySnapshotReceiptDto
    try {
      data = await parseRepositorySnapshotReceipt(result.data)
    } catch (cause) {
      if (!(cause instanceof RepositoryContractError)) throw cause
      throw new PlatformProtocolError(cause.message, result.requestId, result.status)
    }
    if (
      data.snapshot.projectId !== projectId
      || data.snapshot.id !== snapshotId
      || data.contentHash !== contentHash
    ) {
      throw new PlatformProtocolError(
        'The repository service returned a different exact RepositorySnapshot.',
        result.requestId,
        result.status,
      )
    }
    return { ...result, data }
  }

  async listCandidateHeads(
    projectId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<RepositoryCandidateHeadListDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/repository-candidates`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    return { ...result, data: normalizeRepositoryCandidateHeadList(result.data) }
  }

  async searchCandidate(
    projectId: string,
    candidateId: string,
    input: RepositoryCandidateSearchInputDto,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<RepositoryCandidateSearchResultDto>> {
    const request = {
      expectedHeadGeneration: input.expectedHeadGeneration,
      expectedRootHash: input.expectedRootHash,
      query: input.query,
      caseSensitive: input.caseSensitive,
      includeGlobs: [...(input.includeGlobs ?? [])],
      maxMatches: input.maxMatches ?? 100,
    }
    const result = await this.http.post<unknown, typeof request>(
      `/v1/projects/${segment(projectId)}/repository-candidates/${segment(candidateId)}/search`,
      request,
      { signal: options?.signal, requestId: options?.requestId },
    )
    let data: RepositoryCandidateSearchResultDto
    try {
      data = parseRepositoryCandidateSearchResult(result.data)
    } catch (cause) {
      if (!(cause instanceof RepositoryContractError)) throw cause
      throw new PlatformProtocolError(cause.message, result.requestId, result.status)
    }
    if (
      data.projectId !== projectId
      || data.head.candidateId !== candidateId
      || data.head.generation !== request.expectedHeadGeneration
      || data.head.rootHash !== request.expectedRootHash
      || data.query !== request.query
      || data.caseSensitive !== request.caseSensitive
      || data.limits.maxMatches !== request.maxMatches
      || data.includeGlobs.length !== request.includeGlobs.length
      || data.includeGlobs.some((glob, index) => glob !== request.includeGlobs[index])
    ) {
      throw new PlatformProtocolError(
        'The repository service returned Candidate search evidence for a different exact request.',
        result.requestId,
        result.status,
      )
    }
    return { ...result, data }
  }

  async startCandidateRebase(
    projectId: string,
    predecessorCandidateId: string,
    input: {
      readonly targetBuildManifestId: string
      readonly expectedCandidateVersion: number
      readonly expectedSessionEpoch: number
      readonly expectedWriterLeaseEpoch: number
    },
    options?: ClientMutationOptions,
  ): Promise<HttpResult<CandidateRebaseResultDto>> {
    const result = await this.http.post<unknown, typeof input>(
      `/v1/projects/${segment(projectId)}/repository-candidates/${segment(predecessorCandidateId)}/rebases`,
      input,
      {
        signal: options?.signal, requestId: options?.requestId,
        idempotencyKey: options?.idempotencyKey ?? true,
      },
    )
    return { ...result, data: normalizeCandidateRebaseResult(result.data) }
  }

  async getCandidateRebase(
    projectId: string,
    rebaseId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<CandidateRebaseResultDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/candidate-rebases/${segment(rebaseId)}`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    return { ...result, data: normalizeCandidateRebaseResult(result.data) }
  }

  async getCandidateRebaseConflictContent(
    projectId: string,
    rebaseId: string,
    conflictId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<CandidateRebaseConflictContentDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/candidate-rebases/${segment(rebaseId)}/conflicts/${segment(conflictId)}/content`,
      { signal: options?.signal, requestId: options?.requestId },
    )
    return { ...result, data: normalizeCandidateRebaseConflictContent(result.data) }
  }

  async resolveCandidateRebaseConflict(
    projectId: string,
    rebaseId: string,
    conflictId: string,
    input: {
      readonly expectedConflictVersion: number
      readonly strategy: CandidateRebaseResolutionStrategy
      readonly content?: string
      readonly mode?: '100644' | '100755'
    },
    options?: ClientMutationOptions,
  ): Promise<HttpResult<CandidateRebaseResultDto>> {
    const result = await this.http.post<unknown, typeof input>(
      `/v1/projects/${segment(projectId)}/candidate-rebases/${segment(rebaseId)}/conflicts/${segment(conflictId)}/resolve`,
      input,
      {
        signal: options?.signal, requestId: options?.requestId,
        idempotencyKey: options?.idempotencyKey ?? true,
      },
    )
    return { ...result, data: normalizeCandidateRebaseResult(result.data) }
  }
}
