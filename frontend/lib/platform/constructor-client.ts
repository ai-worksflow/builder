import type { ClientMutationOptions, ClientRequestOptions } from './clients'
import {
  normalizeApplicationBuildContract,
  normalizeFullStackTemplatePage,
  normalizeFullStackTemplateRegistration,
  normalizeTemplateReleasePage,
  normalizeTemplateReleaseRegistration,
  type ApplicationBuildContractDto,
  type CreateApplicationBuildContractInputDto,
  type ExactFullStackTemplateRefDto,
  type ExactTemplateReleaseRefDto,
  type FullStackTemplatePageDto,
  type FullStackTemplateRegistrationDto,
  type TemplateReleasePageDto,
  type TemplateReleasePolicyState,
  type TemplateReleaseRegistrationDto,
} from './constructor-contract'
import { HttpClient, type HttpResult } from './http'

export interface TemplateRegistryListOptions extends ClientRequestOptions {
  readonly limit?: number
}

export interface TemplateReleaseFilters {
  readonly templateId?: string
  readonly states?: readonly TemplateReleasePolicyState[]
}

export interface FullStackTemplateFilters {
  readonly templateId?: string
}

export type ExactTemplateReleaseQuery = Pick<
  ExactTemplateReleaseRefDto,
  'contentHash' | 'subjectHash'
>

export type ExactFullStackTemplateQuery = Pick<ExactFullStackTemplateRefDto, 'contentHash'>

function segment(value: string) {
  return encodeURIComponent(value)
}

function requestOptions(options?: ClientRequestOptions) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
  }
}

function mutationOptions(options?: ClientMutationOptions) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
    ifMatch: options?.ifMatch,
    idempotencyKey: options?.idempotencyKey ?? true,
  }
}

function withNormalizedData<T>(result: HttpResult<unknown>, data: T): HttpResult<T> {
  return { ...result, data }
}

/**
 * Read-only template discovery plus immutable Application Build Contract
 * compilation. Template admission and release-policy mutation are
 * intentionally absent from this browser client.
 */
export class ConstructorClient {
  constructor(private readonly http: HttpClient) {}

  async listTemplateReleases(
    filters: TemplateReleaseFilters = {},
    options?: TemplateRegistryListOptions,
  ): Promise<HttpResult<TemplateReleasePageDto>> {
    const result = await this.http.get<unknown>('/v1/template-releases', {
      ...requestOptions(options),
      query: {
        templateId: filters.templateId,
        limit: options?.limit,
        state: filters.states,
      },
    })
    return withNormalizedData(result, normalizeTemplateReleasePage(result.data))
  }

  async getTemplateRelease(
    releaseId: string,
    exact?: ExactTemplateReleaseQuery,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<TemplateReleaseRegistrationDto>> {
    const result = await this.http.get<unknown>(
      `/v1/template-releases/${segment(releaseId)}`,
      {
        ...requestOptions(options),
        query: {
          contentHash: exact?.contentHash,
          subjectHash: exact?.subjectHash,
        },
      },
    )
    return withNormalizedData(result, normalizeTemplateReleaseRegistration(result.data))
  }

  async listFullStackTemplates(
    filters: FullStackTemplateFilters = {},
    options?: TemplateRegistryListOptions,
  ): Promise<HttpResult<FullStackTemplatePageDto>> {
    const result = await this.http.get<unknown>('/v1/full-stack-templates', {
      ...requestOptions(options),
      query: { templateId: filters.templateId, limit: options?.limit },
    })
    return withNormalizedData(result, normalizeFullStackTemplatePage(result.data))
  }

  async getFullStackTemplate(
    templateId: string,
    exact?: ExactFullStackTemplateQuery,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<FullStackTemplateRegistrationDto>> {
    const result = await this.http.get<unknown>(
      `/v1/full-stack-templates/${segment(templateId)}`,
      {
        ...requestOptions(options),
        query: { contentHash: exact?.contentHash },
      },
    )
    return withNormalizedData(result, normalizeFullStackTemplateRegistration(result.data))
  }

  async createBuildContract(
    buildManifestId: string,
    input: CreateApplicationBuildContractInputDto,
    options?: ClientMutationOptions,
  ): Promise<HttpResult<ApplicationBuildContractDto>> {
    // Rebuild the body instead of spreading caller input: source revisions,
    // certification claims, and policy facts are server-resolved only.
    const body: CreateApplicationBuildContractInputDto = {
      fullStackTemplate: {
        id: input.fullStackTemplate.id,
        contentHash: input.fullStackTemplate.contentHash,
      },
    }
    const result = await this.http.post<unknown, CreateApplicationBuildContractInputDto>(
      `/v1/build-manifests/${segment(buildManifestId)}/build-contracts`,
      body,
      mutationOptions(options),
    )
    return withNormalizedData(result, normalizeApplicationBuildContract(result.data))
  }

  async getBuildContractForManifest(
    buildManifestId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ApplicationBuildContractDto>> {
    const result = await this.http.get<unknown>(
      `/v1/build-manifests/${segment(buildManifestId)}/build-contract`,
      requestOptions(options),
    )
    return withNormalizedData(result, normalizeApplicationBuildContract(result.data))
  }

  async getBuildContract(
    contractId: string,
    options?: ClientRequestOptions,
  ): Promise<HttpResult<ApplicationBuildContractDto>> {
    const result = await this.http.get<unknown>(
      `/v1/application-build-contracts/${segment(contractId)}`,
      requestOptions(options),
    )
    return withNormalizedData(result, normalizeApplicationBuildContract(result.data))
  }
}
