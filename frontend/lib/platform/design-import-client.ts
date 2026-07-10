import type { ClientMutationOptions, ClientRequestOptions, ListOptions } from './clients'
import type {
  CreateDesignImportInputDto,
  DecideDesignImportInputDto,
  DesignImportCapabilitiesDto,
  DesignImportDto,
  DesignImportPageDto,
  DesignImportStatus,
} from './design-import-contract'
import { HttpClient } from './http'
import { wireVersionRef } from './wire-version-ref'

function segment(value: string) {
  return encodeURIComponent(value)
}

function requestOptions(options?: ClientRequestOptions) {
  return { signal: options?.signal, requestId: options?.requestId }
}

function mutationOptions(options?: ClientMutationOptions, idempotentByDefault = false) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
    ifMatch: options?.ifMatch,
    idempotencyKey: options?.idempotencyKey ?? (idempotentByDefault ? true : undefined),
  }
}

export class DesignImportsClient {
  constructor(private readonly http: HttpClient) {}

  capabilities(projectId: string, options?: ClientRequestOptions) {
    return this.http.get<DesignImportCapabilitiesDto>(
      `/v1/projects/${segment(projectId)}/design-import-capabilities`,
      requestOptions(options),
    )
  }

  list(
    projectId: string,
    filters: { readonly status?: DesignImportStatus } = {},
    options?: ListOptions,
  ) {
    return this.http.get<DesignImportPageDto>(
      `/v1/projects/${segment(projectId)}/design-imports`,
      {
        ...requestOptions(options),
        query: { status: filters.status, cursor: options?.cursor, limit: options?.limit },
      },
    )
  }

  get(designImportId: string, options?: ClientRequestOptions) {
    return this.http.get<DesignImportDto>(
      `/v1/design-imports/${segment(designImportId)}`,
      requestOptions(options),
    )
  }

  create(
    projectId: string,
    input: CreateDesignImportInputDto,
    options?: ClientMutationOptions,
  ) {
    return this.http.post<DesignImportDto, CreateDesignImportInputDto>(
      `/v1/projects/${segment(projectId)}/design-imports`,
      { ...input, pageSpecRevision: wireVersionRef(input.pageSpecRevision) },
      mutationOptions(options, true),
    )
  }

  decide(
    designImportId: string,
    input: DecideDesignImportInputDto,
    options: ClientMutationOptions,
  ) {
    return this.http.post<DesignImportDto, DecideDesignImportInputDto>(
      `/v1/design-imports/${segment(designImportId)}/decision`,
      input,
      mutationOptions(options, true),
    )
  }
}
