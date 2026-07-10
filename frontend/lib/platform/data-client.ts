import type {
  AppliedDataMigration,
  AuthUserMetadata,
  AuthUserMetadataInput,
  DataAuditEvent,
  DataMetadataKind,
  DataMetadataMap,
  DataMigrationOperation,
  DataMigrationPreview,
  DataProjectSnapshot,
  DataRecord,
  DataRecordInput,
  DataRecordPage,
  DataTable,
  DataTableInput,
  EnvironmentVariableInput,
  EnvironmentVariableMetadata,
  ServerFunctionMetadata,
  ServerFunctionMetadataInput,
  StorageObjectMetadata,
  StorageObjectMetadataInput,
  SupabaseConnectionInput,
  SupabaseConnectionResult,
} from '../data-runtime/types'
import type { HttpResult } from './http'
import { HttpClient } from './http'
import { PublicDataRuntimeClient } from './public-data-client'

export interface DataClientRequestOptions {
  readonly signal?: AbortSignal
  readonly requestId?: string
}

export interface DataClientMutationOptions extends DataClientRequestOptions {
  readonly idempotencyKey?: string | true
}

export interface DataRecordPageOptions extends DataClientRequestOptions {
  readonly limit?: number
  readonly offset?: number
}

export interface DeletedDataResource {
  readonly deleted: true
  readonly id: string
}

export interface AppliedDataMigrationResult {
  readonly migration: AppliedDataMigration
  readonly tables: readonly DataTable[]
  readonly project: DataProjectSnapshot
}

type MetadataInputMap = {
  readonly 'auth-users': AuthUserMetadataInput
  readonly 'storage-objects': StorageObjectMetadataInput
  readonly 'server-functions': ServerFunctionMetadataInput
}

type MetadataResultMap = {
  readonly 'auth-users': AuthUserMetadata
  readonly 'storage-objects': StorageObjectMetadata
  readonly 'server-functions': ServerFunctionMetadata
}

function segment(value: string) {
  return encodeURIComponent(value)
}

function requestOptions(options?: DataClientRequestOptions) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
  }
}

function mutationOptions(options?: DataClientMutationOptions) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
    idempotencyKey: options?.idempotencyKey ?? true,
  }
}

function projectPath(projectId: string) {
  return `/v1/data/projects/${segment(projectId)}`
}

/**
 * Typed client for the Go data runtime. It deliberately shares the platform
 * HttpClient so cookie sessions, CSRF rotation, request IDs and RFC 9457
 * errors have one source of truth across collaboration and data operations.
 */
export class DataRuntimeClient {
  private readonly http: HttpClient
  readonly publicRuntime: PublicDataRuntimeClient

  constructor(http: HttpClient) {
    this.http = http
    this.publicRuntime = new PublicDataRuntimeClient(http)
  }

  snapshot(projectId: string, options?: DataClientRequestOptions) {
    return this.http.get<{ readonly project: DataProjectSnapshot }>(
      projectPath(projectId),
      requestOptions(options),
    )
  }

  audit(projectId: string, options?: DataClientRequestOptions) {
    return this.http.get<{ readonly audit: readonly DataAuditEvent[] }>(
      `${projectPath(projectId)}/audit`,
      requestOptions(options),
    )
  }

  listTables(projectId: string, options?: DataClientRequestOptions) {
    return this.http.get<{ readonly tables: readonly DataTable[] }>(
      `${projectPath(projectId)}/tables`,
      requestOptions(options),
    )
  }

  getTable(projectId: string, tableId: string, options?: DataClientRequestOptions) {
    return this.http.get<{ readonly table: DataTable }>(
      `${projectPath(projectId)}/tables/${segment(tableId)}`,
      requestOptions(options),
    )
  }

  createTable(
    projectId: string,
    input: DataTableInput,
    options?: DataClientMutationOptions,
  ) {
    return this.http.post<{ readonly table: DataTable }, DataTableInput>(
      `${projectPath(projectId)}/tables`,
      input,
      mutationOptions(options),
    )
  }

  renameTable(
    projectId: string,
    tableId: string,
    name: string,
    options?: DataClientMutationOptions,
  ) {
    return this.http.patch<{ readonly table: DataTable }, { readonly name: string }>(
      `${projectPath(projectId)}/tables/${segment(tableId)}`,
      { name },
      mutationOptions(options),
    )
  }

  deleteTable(
    projectId: string,
    tableId: string,
    options?: DataClientMutationOptions,
  ) {
    return this.http.delete<DeletedDataResource>(
      `${projectPath(projectId)}/tables/${segment(tableId)}`,
      mutationOptions(options),
    )
  }

  listRecords(
    projectId: string,
    tableId: string,
    options: DataRecordPageOptions = {},
  ) {
    return this.http.get<DataRecordPage>(
      `${projectPath(projectId)}/tables/${segment(tableId)}/records`,
      {
        ...requestOptions(options),
        query: {
          limit: options.limit ?? 100,
          offset: options.offset ?? 0,
        },
      },
    )
  }

  getRecord(
    projectId: string,
    tableId: string,
    recordId: string,
    options?: DataClientRequestOptions,
  ) {
    return this.http.get<{ readonly record: DataRecord }>(
      `${projectPath(projectId)}/tables/${segment(tableId)}/records/${segment(recordId)}`,
      requestOptions(options),
    )
  }

  createRecord(
    projectId: string,
    tableId: string,
    input: DataRecordInput,
    options?: DataClientMutationOptions,
  ) {
    return this.http.post<{ readonly record: DataRecord }, DataRecordInput>(
      `${projectPath(projectId)}/tables/${segment(tableId)}/records`,
      input,
      mutationOptions(options),
    )
  }

  updateRecord(
    projectId: string,
    tableId: string,
    recordId: string,
    input: DataRecordInput,
    options?: DataClientMutationOptions,
  ) {
    return this.http.patch<{ readonly record: DataRecord }, DataRecordInput>(
      `${projectPath(projectId)}/tables/${segment(tableId)}/records/${segment(recordId)}`,
      input,
      mutationOptions(options),
    )
  }

  deleteRecord(
    projectId: string,
    tableId: string,
    recordId: string,
    options?: DataClientMutationOptions,
  ) {
    return this.http.delete<DeletedDataResource>(
      `${projectPath(projectId)}/tables/${segment(tableId)}/records/${segment(recordId)}`,
      mutationOptions(options),
    )
  }

  listMetadata<K extends DataMetadataKind>(
    projectId: string,
    kind: K,
    options?: DataClientRequestOptions,
  ): Promise<HttpResult<{ readonly kind: K; readonly items: readonly DataMetadataMap[K][] }>> {
    return this.http.get(
      `${projectPath(projectId)}/metadata/${kind}`,
      requestOptions(options),
    )
  }

  getMetadata<K extends DataMetadataKind>(
    projectId: string,
    kind: K,
    itemId: string,
    options?: DataClientRequestOptions,
  ): Promise<HttpResult<{ readonly item: DataMetadataMap[K] }>> {
    return this.http.get(
      `${projectPath(projectId)}/metadata/${kind}/${segment(itemId)}`,
      requestOptions(options),
    )
  }

  createMetadata<K extends DataMetadataKind>(
    projectId: string,
    kind: K,
    input: MetadataInputMap[K],
    options?: DataClientMutationOptions,
  ): Promise<HttpResult<{ readonly item: MetadataResultMap[K] }>> {
    return this.http.post(
      `${projectPath(projectId)}/metadata/${kind}`,
      input,
      mutationOptions(options),
    )
  }

  updateMetadata<K extends DataMetadataKind>(
    projectId: string,
    kind: K,
    itemId: string,
    input: Partial<MetadataInputMap[K]>,
    options?: DataClientMutationOptions,
  ): Promise<HttpResult<{ readonly item: MetadataResultMap[K] }>> {
    return this.http.patch(
      `${projectPath(projectId)}/metadata/${kind}/${segment(itemId)}`,
      input,
      mutationOptions(options),
    )
  }

  deleteMetadata(
    projectId: string,
    kind: DataMetadataKind,
    itemId: string,
    options?: DataClientMutationOptions,
  ) {
    return this.http.delete<DeletedDataResource>(
      `${projectPath(projectId)}/metadata/${kind}/${segment(itemId)}`,
      mutationOptions(options),
    )
  }

  listVariables(projectId: string, options?: DataClientRequestOptions) {
    return this.http.get<{ readonly variables: readonly EnvironmentVariableMetadata[] }>(
      `${projectPath(projectId)}/variables`,
      requestOptions(options),
    )
  }

  setVariable(
    projectId: string,
    input: EnvironmentVariableInput,
    options?: DataClientMutationOptions,
  ) {
    return this.http.post<
      { readonly variable: EnvironmentVariableMetadata },
      EnvironmentVariableInput
    >(
      `${projectPath(projectId)}/variables`,
      input,
      mutationOptions(options),
    )
  }

  deleteVariable(
    projectId: string,
    variableId: string,
    options?: DataClientMutationOptions,
  ) {
    return this.http.delete<DeletedDataResource>(
      `${projectPath(projectId)}/variables/${segment(variableId)}`,
      mutationOptions(options),
    )
  }

  previewMigration(
    projectId: string,
    operations: readonly DataMigrationOperation[],
    options?: DataClientMutationOptions,
  ) {
    return this.http.post<
      { readonly preview: DataMigrationPreview },
      { readonly operations: readonly DataMigrationOperation[] }
    >(
      `${projectPath(projectId)}/migrations/preview`,
      { operations },
      mutationOptions(options),
    )
  }

  applyMigration(
    projectId: string,
    confirmationToken: string,
    options?: DataClientMutationOptions,
  ) {
    return this.http.post<
      AppliedDataMigrationResult,
      { readonly confirmationToken: string }
    >(
      `${projectPath(projectId)}/migrations/apply`,
      { confirmationToken },
      mutationOptions(options),
    )
  }

  connectSupabase(
    projectId: string,
    input: SupabaseConnectionInput,
    options?: DataClientMutationOptions,
  ) {
    return this.http.post<
      { readonly connection: SupabaseConnectionResult },
      SupabaseConnectionInput
    >(
      '/v1/data/connect/supabase',
      input,
      {
        ...mutationOptions(options),
        headers: { 'X-Worksflow-Project-Id': projectId },
      },
    )
  }
}
