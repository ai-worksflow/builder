import type {
  PublicDeploymentRuntime,
  PublicTablePolicy,
  PublicTablePolicyInput,
} from '../data-runtime/types'
import {
  PlatformProtocolError,
  type HttpClient,
  type HttpResult,
} from './http'

export interface PublicDataRequestOptions {
  readonly signal?: AbortSignal
  readonly requestId?: string
}

export interface PublicDataMutationOptions extends PublicDataRequestOptions {
  readonly idempotencyKey?: string | true
  readonly ifMatch?: string
}

export interface DeletedPublicTablePolicy {
  readonly deleted: true
  readonly tableId: string
}

export interface RevokedPublicDeploymentRuntime {
  readonly revoked: true
  readonly deploymentId: string
}

function segment(value: string) {
  return encodeURIComponent(value)
}

function managementPath(projectId: string) {
  return `/v1/data/projects/${segment(projectId)}/public-runtime`
}

function requestOptions(options?: PublicDataRequestOptions) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
  }
}

function mutationOptions(options?: PublicDataMutationOptions) {
  return {
    signal: options?.signal,
    requestId: options?.requestId,
    idempotencyKey: options?.idempotencyKey ?? true,
    ifMatch: options?.ifMatch,
  }
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringArray(value: unknown): value is readonly string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string')
}

function timestamp(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && Number.isFinite(Date.parse(value))
}

function publicPolicy(value: unknown): value is PublicTablePolicy {
  return (
    record(value) &&
    typeof value.projectId === 'string' &&
    typeof value.tableId === 'string' &&
    typeof value.tableName === 'string' &&
    typeof value.allowRead === 'boolean' &&
    typeof value.allowCreate === 'boolean' &&
    typeof value.allowUpdate === 'boolean' &&
    typeof value.allowDelete === 'boolean' &&
    stringArray(value.readableFields) &&
    stringArray(value.writableFields) &&
    Number.isSafeInteger(value.version) &&
    Number(value.version) >= 0 &&
    typeof value.etag === 'string' &&
    value.etag.startsWith('"public-data-policy:') &&
    value.etag.endsWith('"') &&
    timestamp(value.createdAt) &&
    timestamp(value.updatedAt)
  )
}

function publicRuntime(value: unknown): value is PublicDeploymentRuntime {
  return (
    record(value) &&
    !Object.hasOwn(value, 'capabilityToken') &&
    typeof value.apiBasePath === 'string' &&
    value.apiBasePath.startsWith('/') &&
    typeof value.projectId === 'string' &&
    typeof value.deploymentId === 'string' &&
    typeof value.deploymentVersionId === 'string' &&
    typeof value.capabilityId === 'string' &&
    stringArray(value.allowedOrigins) &&
    timestamp(value.expiresAt) &&
    (value.activatedAt === undefined || timestamp(value.activatedAt))
  )
}

function protocol<T>(
  result: HttpResult<unknown>,
  valid: boolean,
  message: string,
  value: T,
): T {
  if (!valid) {
    throw new PlatformProtocolError(message, result.requestId, result.status)
  }
  return value
}

function assertPolicyResult(
  result: HttpResult<unknown>,
  projectId: string,
  tableId?: string,
) {
  const body = result.data
  const value = record(body) ? body.policy : undefined
  return protocol(
    result,
    publicPolicy(value) &&
      value.projectId === projectId &&
      (tableId === undefined || value.tableId === tableId),
    'The public data service returned an invalid table policy.',
    value as PublicTablePolicy,
  )
}

/**
 * Authenticated management client for anonymous table policies and the active
 * deployment capability. Public application traffic uses a separate Bearer
 * token client and is intentionally not exposed through this builder client.
 */
export class PublicDataRuntimeClient {
  private readonly http: HttpClient

  constructor(http: HttpClient) {
    this.http = http
  }

  async listPolicies(projectId: string, options?: PublicDataRequestOptions) {
    const result = await this.http.get<unknown>(
      `${managementPath(projectId)}/policies`,
      requestOptions(options),
    )
    const body = result.data
    const policies = record(body) ? body.policies : undefined
    const valid = Array.isArray(policies) &&
      policies.every((policy) => publicPolicy(policy) && policy.projectId === projectId) &&
      new Set(policies.map((policy) => (policy as PublicTablePolicy).tableId)).size === policies.length
    return protocol(
      result,
      valid,
      'The public data service returned an invalid policy list.',
      policies as readonly PublicTablePolicy[],
    )
  }

  async putPolicy(
    projectId: string,
    tableId: string,
    input: PublicTablePolicyInput,
    options?: PublicDataMutationOptions,
  ) {
    if (!options?.ifMatch) {
      throw new PlatformProtocolError('Refresh the public table policy before saving it.')
    }
    const result = await this.http.put<unknown, PublicTablePolicyInput>(
      `${managementPath(projectId)}/policies/${segment(tableId)}`,
      input,
      mutationOptions(options),
    )
    return assertPolicyResult(result, projectId, tableId)
  }

  async deletePolicy(
    projectId: string,
    tableId: string,
    options?: PublicDataMutationOptions,
  ) {
    if (!options?.ifMatch) {
      throw new PlatformProtocolError('Refresh the public table policy before removing it.')
    }
    const result = await this.http.delete<unknown>(
      `${managementPath(projectId)}/policies/${segment(tableId)}`,
      mutationOptions(options),
    )
    const body = result.data
    return protocol(
      result,
      record(body) && body.deleted === true && body.tableId === tableId,
      'The public data service returned an invalid policy deletion result.',
      body as unknown as DeletedPublicTablePolicy,
    )
  }

  async activeDeploymentRuntime(
    projectId: string,
    deploymentId: string,
    options?: PublicDataRequestOptions,
  ) {
    const result = await this.http.get<unknown>(
      `${managementPath(projectId)}/deployments/${segment(deploymentId)}`,
      requestOptions(options),
    )
    const body = result.data
    const runtime = record(body) ? body.runtime : undefined
    return protocol(
      result,
      publicRuntime(runtime) &&
        runtime.projectId === projectId &&
        runtime.deploymentId === deploymentId,
      'The public data service returned an invalid deployment runtime.',
      runtime as PublicDeploymentRuntime,
    )
  }

  async revokeDeploymentRuntime(
    projectId: string,
    deploymentId: string,
    options?: PublicDataMutationOptions,
  ) {
    const result = await this.http.delete<unknown>(
      `${managementPath(projectId)}/deployments/${segment(deploymentId)}`,
      mutationOptions(options),
    )
    const body = result.data
    return protocol(
      result,
      record(body) && body.revoked === true && body.deploymentId === deploymentId,
      'The public data service returned an invalid deployment revocation result.',
      body as unknown as RevokedPublicDeploymentRuntime,
    )
  }
}
