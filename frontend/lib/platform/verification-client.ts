import type { ClientMutationOptions, ClientRequestOptions } from './clients'
import { HttpClient, type HttpResult } from './http'
import {
  normalizeCandidateVerificationReceipt,
  normalizeCandidateVerificationRunList,
  normalizeCandidateVerificationRunView,
	  normalizeCanonicalVerificationRunView,
  normalizeVerificationProfileList,
  normalizeVerificationCheckPage,
  type CandidateVerificationReceiptDto,
  type CandidateVerificationRunListDto,
  type CandidateVerificationRunViewDto,
	  type CanonicalVerificationRunViewDto,
	  type CanonicalVerificationSubjectDto,
  type VerificationProfileListDto,
  type VerificationCheckPageDto,
  type VerificationProfileReferenceDto,
} from './verification-contract'

export interface CreateCandidateVerificationRunInput {
  readonly candidateId: string
  readonly checkpointId: string
  readonly expectedSessionVersion: number
  readonly expectedSessionEpoch: number
  readonly expectedCandidateVersion: number
  readonly expectedWriterLeaseEpoch: number
  readonly verificationProfile: VerificationProfileReferenceDto
  readonly reason: string
}

export interface CancelCandidateVerificationRunInput {
  readonly expectedVersion: number
  readonly expectedFenceEpoch: number
  readonly reason: string
}

export interface CreateCanonicalVerificationRunInput {
  readonly workspaceRevision: {
    readonly artifactId: string
    readonly revisionId: string
    readonly contentHash: string
  }
  readonly verificationProfile: VerificationProfileReferenceDto
  readonly reason: string
}

function segment(value: string) {
  return encodeURIComponent(value)
}

function requestOptions(options?: ClientRequestOptions) {
  return { signal: options?.signal, requestId: options?.requestId }
}

function mutationOptions(options?: ClientMutationOptions) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
    idempotencyKey: options?.idempotencyKey ?? true,
  }
}

export class VerificationClient {
  constructor(private readonly http: HttpClient) {}

  async listProfiles(
    sessionId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<VerificationProfileListDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/verification-profiles`,
      requestOptions(options),
    )
    return { ...result, data: normalizeVerificationProfileList(result.data) }
  }

  async listCanonicalProfiles(
    projectId: string,
    workspace: CanonicalVerificationSubjectDto,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<VerificationProfileListDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/canonical-verification-profiles`,
      {
        ...requestOptions(options),
        query: {
          workspaceArtifactId: workspace.workspaceArtifactId,
          workspaceRevisionId: workspace.workspaceRevisionId,
          workspaceContentHash: workspace.workspaceContentHash,
        },
      },
    )
    return { ...result, data: normalizeVerificationProfileList(result.data) }
  }

  async createCanonicalRun(
    projectId: string,
    input: CreateCanonicalVerificationRunInput,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<CanonicalVerificationRunViewDto>> {
    const result = await this.http.post<unknown, CreateCanonicalVerificationRunInput>(
      `/v1/projects/${segment(projectId)}/canonical-verification-runs`,
      input,
      mutationOptions(options),
    )
    return { ...result, data: normalizeCanonicalVerificationRunView(result.data) }
  }

  async listCanonicalRuns(
    projectId: string,
    workspace: CanonicalVerificationSubjectDto,
    limit = 20,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<{ readonly runs: readonly CanonicalVerificationRunViewDto[] }>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/canonical-verification-runs`,
      {
        ...requestOptions(options),
        query: {
          workspaceArtifactId: workspace.workspaceArtifactId,
          workspaceRevisionId: workspace.workspaceRevisionId,
          workspaceContentHash: workspace.workspaceContentHash,
          limit,
        },
      },
    )
    const source = result.data !== null && typeof result.data === 'object'
      ? result.data as { readonly runs?: unknown }
      : {}
    const runs = Array.isArray(source.runs)
      ? source.runs.map(normalizeCanonicalVerificationRunView)
      : []
    return { ...result, data: { runs } }
  }

  async getCanonicalRun(
    projectId: string,
    runId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<CanonicalVerificationRunViewDto>> {
    const result = await this.http.get<unknown>(
      `/v1/projects/${segment(projectId)}/canonical-verification-runs/${segment(runId)}`,
      requestOptions(options),
    )
    return { ...result, data: normalizeCanonicalVerificationRunView(result.data) }
  }


  async listRuns(
    sessionId: string,
    limit = 20,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<CandidateVerificationRunListDto>> {
    const result = await this.http.get<unknown>(
      `/v1/sandbox-sessions/${segment(sessionId)}/verification-runs`,
      { ...requestOptions(options), query: { limit } },
    )
    return { ...result, data: normalizeCandidateVerificationRunList(result.data) }
  }

  async createRun(
    sessionId: string,
    input: CreateCandidateVerificationRunInput,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<CandidateVerificationRunViewDto>> {
    const result = await this.http.post<unknown, CreateCandidateVerificationRunInput>(
      `/v1/sandbox-sessions/${segment(sessionId)}/verification-runs`,
      input,
      mutationOptions(options),
    )
    return { ...result, data: normalizeCandidateVerificationRunView(result.data) }
  }

  async getRun(
    runId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<CandidateVerificationRunViewDto>> {
    const result = await this.http.get<unknown>(
      `/v1/verification-runs/${segment(runId)}`,
      requestOptions(options),
    )
    return { ...result, data: normalizeCandidateVerificationRunView(result.data) }
  }

  async cancelRun(
    runId: string,
    input: CancelCandidateVerificationRunInput,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<CandidateVerificationRunViewDto>> {
    const result = await this.http.post<unknown, CancelCandidateVerificationRunInput>(
      `/v1/verification-runs/${segment(runId)}:cancel`,
      input,
      mutationOptions(options),
    )
    return { ...result, data: normalizeCandidateVerificationRunView(result.data) }
  }

  async retryRun(
    runId: string,
    reason: string,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<CandidateVerificationRunViewDto>> {
    const result = await this.http.post<unknown, { readonly reason: string }>(
      `/v1/verification-runs/${segment(runId)}:retry`,
      { reason },
      mutationOptions(options),
    )
    return { ...result, data: normalizeCandidateVerificationRunView(result.data) }
  }


  async listChecks(
    runId: string,
    offset = 0,
    limit = 50,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<VerificationCheckPageDto>> {
    const result = await this.http.get<unknown>(
      `/v1/verification-runs/${segment(runId)}/checks`,
      { ...requestOptions(options), query: { offset, limit } },
    )
    return { ...result, data: normalizeVerificationCheckPage(result.data) }
  }

  async getReceipt(
    receiptId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<CandidateVerificationReceiptDto>> {
    const result = await this.http.get<unknown>(
      `/v1/verification-receipts/${segment(receiptId)}`,
      requestOptions(options),
    )
    return { ...result, data: normalizeCandidateVerificationReceipt(result.data) }
  }
}
