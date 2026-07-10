import {
  PlatformAbortError,
  PlatformHttpError,
  PlatformNetworkError,
  type HttpClient,
} from '../platform/http'
import type { VirtualWorkspace } from '../worksflow/workspace-model'

export type DeliveryEnvironment = 'preview' | 'production'
export type DeliveryExportKind = 'source' | 'document' | 'blueprint' | 'prototype'

export interface DeliveryVersionRef {
  readonly artifactId: string
  readonly revisionId: string
  readonly contentHash: string
  readonly anchorId?: string
  readonly revisionNumber?: number
}

export interface DeliveryArchiveInput {
  readonly kind: DeliveryExportKind
  readonly revision?: DeliveryVersionRef
  readonly buildManifestId?: string
  readonly redactSensitive?: boolean
}

export interface DeliveryPublishInput {
  readonly deploymentId?: string
  readonly environment: DeliveryEnvironment
  readonly environmentRef?: string
  readonly workspaceRevision: DeliveryVersionRef
  readonly buildManifestId: string
  readonly qualityRunId: string
  readonly message?: string
}

export interface DeliveryRequestOptions {
  readonly signal?: AbortSignal
  readonly requestId?: string
}

export interface DeliveryMutationOptions extends DeliveryRequestOptions {
  readonly idempotencyKey?: string
  readonly ifMatch?: string
  readonly onLog?: (message: string) => void
}

export interface DeliveryRollbackOptions extends DeliveryRequestOptions {
  readonly idempotencyKey?: string
  readonly ifMatch: string
  readonly message?: string
  readonly onLog?: (message: string) => void
}

export interface DeploymentVersionDto {
  readonly id: string
  readonly number: number
  readonly action: 'publish' | 'rollback'
  readonly sourceVersionId?: string
  readonly workspaceRevision: DeliveryVersionRef
  readonly buildManifestId?: string
  readonly qualityRunId?: string
  readonly status: string
  readonly publicUrl?: string
  readonly entryPath: string
  readonly checksum: string
  readonly fileCount: number
  readonly totalBytes: number
  readonly environmentRef: string
  readonly environmentVariableNames: readonly string[]
  readonly message?: string
  readonly createdBy: string
  readonly createdAt: string
}

export interface DeploymentDto {
  readonly id: string
  readonly projectId: string
  readonly environment: DeliveryEnvironment
  readonly environmentRef: string
  readonly provider: string
  readonly status: string
  readonly activeVersionId?: string
  readonly publicUrl?: string
  readonly versions?: readonly DeploymentVersionDto[]
  readonly version: number
  readonly etag: string
  readonly lastError?: string
  readonly createdBy: string
  readonly createdAt: string
  readonly updatedAt: string
}

export interface DeploymentLog {
  readonly id: string
  readonly deploymentId: string
  readonly deploymentVersionId?: string
  readonly sequence: number
  readonly level: string
  readonly message: string
  readonly createdAt: string
}

/** Compatibility view consumed by the existing presentation components. */
export interface DeploymentVersionMetadata extends DeploymentVersionDto {
  readonly environment: DeliveryEnvironment
}

/** Compatibility view consumed by the existing presentation components. */
export interface DeploymentMetadata extends Omit<DeploymentDto, 'activeVersionId' | 'versions'> {
  readonly deploymentId: string
  readonly project: { readonly id: string; readonly name: string }
  readonly activeVersionId: string
  readonly publicPath: string
  readonly versions: readonly DeploymentVersionMetadata[]
}

export interface ExportArchiveResult {
  readonly blob: Blob
  readonly filename: string
  readonly fileCount: number
  readonly redactionCount: number
  readonly digest?: string
  readonly etag?: string
}

export class DeliveryClientError extends Error {
  readonly code: string
  readonly status?: number
  readonly retryAfterSeconds?: number

  constructor(
    message: string,
    options: { code?: string; status?: number; retryAfterSeconds?: number } = {},
  ) {
    super(message)
    this.name = 'DeliveryClientError'
    this.code = options.code ?? 'delivery_client_error'
    this.status = options.status
    this.retryAfterSeconds = options.retryAfterSeconds
  }
}

function segment(value: string) {
  return encodeURIComponent(value)
}

function exactVersionRef(reference: DeliveryVersionRef) {
  return {
    artifactId: reference.artifactId,
    revisionId: reference.revisionId,
    contentHash: reference.contentHash,
    ...(reference.anchorId ? { anchorId: reference.anchorId } : {}),
  }
}

function exportBody(input: DeliveryArchiveInput) {
  return {
    kind: input.kind,
    ...(input.revision ? { revision: exactVersionRef(input.revision) } : {}),
    ...(input.buildManifestId ? { buildManifestId: input.buildManifestId } : {}),
    ...(input.redactSensitive === undefined
      ? {}
      : { redactSensitive: input.redactSensitive }),
  }
}

function publishBody(input: DeliveryPublishInput) {
  return {
    ...(input.deploymentId ? { deploymentId: input.deploymentId } : {}),
    environment: input.environment,
    environmentRef: input.environmentRef ?? '',
    workspaceRevision: exactVersionRef(input.workspaceRevision),
    buildManifestId: input.buildManifestId,
    qualityRunId: input.qualityRunId,
    ...(input.message ? { message: input.message } : {}),
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isDeploymentVersion(value: unknown): value is DeploymentVersionDto {
  return (
    isRecord(value) &&
    typeof value.id === 'string' &&
    typeof value.number === 'number' &&
    (value.action === 'publish' || value.action === 'rollback') &&
    typeof value.status === 'string' &&
    typeof value.entryPath === 'string' &&
    typeof value.checksum === 'string' &&
    typeof value.fileCount === 'number' &&
    typeof value.totalBytes === 'number' &&
    Array.isArray(value.environmentVariableNames) &&
    typeof value.createdAt === 'string'
  )
}

function isDeployment(value: unknown): value is DeploymentDto {
  return (
    isRecord(value) &&
    typeof value.id === 'string' &&
    typeof value.projectId === 'string' &&
    (value.environment === 'preview' || value.environment === 'production') &&
    typeof value.status === 'string' &&
    typeof value.version === 'number' &&
    typeof value.etag === 'string' &&
    typeof value.createdAt === 'string' &&
    typeof value.updatedAt === 'string' &&
    (value.versions === undefined || (
      Array.isArray(value.versions) && value.versions.every(isDeploymentVersion)
    ))
  )
}

function assertDeployment(value: unknown) {
  if (!isDeployment(value)) {
    throw new DeliveryClientError('The delivery service returned an invalid deployment.', {
      code: 'invalid_deployment_response',
    })
  }
  return value
}

function normalizeDeployment(
  deployment: DeploymentDto,
  projectName = deployment.projectId,
): DeploymentMetadata {
  return {
    ...deployment,
    deploymentId: deployment.id,
    project: { id: deployment.projectId, name: projectName },
    activeVersionId: deployment.activeVersionId ?? '',
    publicPath: deployment.publicUrl ?? '',
    versions: (deployment.versions ?? []).map((version) => ({
      ...version,
      environment: deployment.environment,
    })),
  }
}

function deliveryError(error: unknown): never {
  if (error instanceof DeliveryClientError) throw error
  if (error instanceof PlatformAbortError) throw error
  if (error instanceof PlatformHttpError) {
    throw new DeliveryClientError(error.message, {
      code: error.code,
      status: error.status,
      retryAfterSeconds: error.retryAfterSeconds,
    })
  }
  if (error instanceof PlatformNetworkError) {
    throw new DeliveryClientError(error.message, { code: 'delivery_unreachable' })
  }
  throw new DeliveryClientError(
    error instanceof Error ? error.message : 'The delivery request failed.',
    { code: 'delivery_request_failed' },
  )
}

export class DeliveryClient {
  readonly http: HttpClient

  constructor(http: HttpClient) {
    this.http = http
  }

  async exportArchive(
    projectId: string,
    input: DeliveryArchiveInput,
    options: DeliveryRequestOptions = {},
  ): Promise<ExportArchiveResult> {
    try {
      const result = await this.http.request<Blob>(
        `/v1/projects/${segment(projectId)}/exports`,
        {
          method: 'POST',
          body: exportBody(input),
          responseType: 'blob',
          signal: options.signal,
          requestId: options.requestId,
        },
      )
      return {
        blob: result.data,
        filename: filenameFromDisposition(result.headers.get('content-disposition'))
          ?? `${safeSlug(projectId)}-${input.kind}.zip`,
        fileCount: positiveInteger(result.headers.get('x-archive-file-count')),
        redactionCount: positiveInteger(result.headers.get('x-archive-redaction-count')),
        digest: result.headers.get('digest') ?? undefined,
        etag: result.etag,
      }
    } catch (error) {
      deliveryError(error)
    }
  }

  async publish(
    projectId: string,
    input: DeliveryPublishInput,
    options: DeliveryMutationOptions = {},
  ) {
    try {
      const result = await this.http.post<
        { readonly deployment: DeploymentDto; readonly absoluteUrl: string },
        ReturnType<typeof publishBody>
      >(
        `/v1/projects/${segment(projectId)}/deployments`,
        publishBody(input),
        {
          signal: options.signal,
          requestId: options.requestId,
          idempotencyKey: options.idempotencyKey ?? true,
          ifMatch: options.ifMatch,
        },
      )
      const deployment = normalizeDeployment(assertDeployment(result.data.deployment))
      const absoluteUrl = typeof result.data.absoluteUrl === 'string'
        ? result.data.absoluteUrl
        : deployment.publicUrl ?? ''
      options.onLog?.(
        `Deployment ${deployment.activeVersionId || deployment.deploymentId} is ${deployment.status}.`,
      )
      return { deployment, absoluteUrl }
    } catch (error) {
      deliveryError(error)
    }
  }

  async list(projectId: string, options: DeliveryRequestOptions = {}) {
    try {
      const result = await this.http.get<{ readonly deployments: readonly DeploymentDto[] }>(
        `/v1/projects/${segment(projectId)}/deployments`,
        { signal: options.signal, requestId: options.requestId },
      )
      if (!Array.isArray(result.data.deployments)) {
        throw new DeliveryClientError('The deployment history is invalid.', {
          code: 'invalid_deployment_history',
        })
      }
      const summaries = result.data.deployments.map(assertDeployment)
      // The list endpoint is intentionally compact. Hydrate immutable version
      // history because rollback and audit UIs require exact target IDs.
      return Promise.all(summaries.map(async (summary) => {
        const details = await this.get(summary.id, options)
        return details
      }))
    } catch (error) {
      deliveryError(error)
    }
  }

  async get(deploymentId: string, options: DeliveryRequestOptions = {}) {
    try {
      const result = await this.http.get<{ readonly deployment: DeploymentDto }>(
        `/v1/deployments/${segment(deploymentId)}`,
        { signal: options.signal, requestId: options.requestId },
      )
      return normalizeDeployment(assertDeployment(result.data.deployment))
    } catch (error) {
      deliveryError(error)
    }
  }

  async logs(deploymentId: string, options: DeliveryRequestOptions = {}) {
    try {
      const result = await this.http.get<{ readonly logs: readonly DeploymentLog[] }>(
        `/v1/deployments/${segment(deploymentId)}/logs`,
        { signal: options.signal, requestId: options.requestId },
      )
      if (!Array.isArray(result.data.logs)) {
        throw new DeliveryClientError('The deployment log is invalid.', {
          code: 'invalid_deployment_log',
        })
      }
      return result.data.logs
    } catch (error) {
      deliveryError(error)
    }
  }

  async rollback(
    deploymentId: string,
    targetVersionId: string,
    options: DeliveryRollbackOptions,
  ) {
    try {
      const result = await this.http.post<
        { readonly deployment: DeploymentDto; readonly absoluteUrl: string },
        { readonly targetVersionId: string; readonly message?: string }
      >(
        `/v1/deployments/${segment(deploymentId)}/rollback`,
        {
          targetVersionId,
          ...(options.message ? { message: options.message } : {}),
        },
        {
          signal: options.signal,
          requestId: options.requestId,
          idempotencyKey: options.idempotencyKey ?? true,
          ifMatch: options.ifMatch,
        },
      )
      const deployment = normalizeDeployment(assertDeployment(result.data.deployment))
      options.onLog?.(`Rollback created immutable version ${deployment.activeVersionId}.`)
      return deployment
    } catch (error) {
      deliveryError(error)
    }
  }
}

/** New canonical signature. */
export function exportWorkspaceArchive(
  http: HttpClient,
  projectId: string,
  input: DeliveryArchiveInput,
  options?: DeliveryRequestOptions,
): Promise<ExportArchiveResult>
/** @deprecated Mutable browser workspaces are not valid export inputs. */
export function exportWorkspaceArchive(
  workspace: VirtualWorkspace,
  options?: { readonly signal?: AbortSignal },
): Promise<ExportArchiveResult>
export async function exportWorkspaceArchive(
  first: HttpClient | VirtualWorkspace,
  projectIdOrOptions?: string | { readonly signal?: AbortSignal },
  input?: DeliveryArchiveInput,
  options?: DeliveryRequestOptions,
) {
  if ('request' in first && typeof first.request === 'function') {
    if (typeof projectIdOrOptions !== 'string' || !input) {
      throw new DeliveryClientError('projectId and an exact export source are required.', {
        code: 'frozen_delivery_source_required',
      })
    }
    return new DeliveryClient(first).exportArchive(projectIdOrOptions, input, options)
  }
  throw frozenSourceError('Export')
}

/** New canonical signature. */
export function publishWorkspace(
  http: HttpClient,
  projectId: string,
  input: DeliveryPublishInput,
  options?: DeliveryMutationOptions,
): Promise<{ readonly deployment: DeploymentMetadata; readonly absoluteUrl: string }>
/** @deprecated Mutable browser workspaces are not valid publish inputs. */
export function publishWorkspace(
  workspace: VirtualWorkspace,
  html: string,
  entryPath: string | undefined,
  options?: {
    readonly deploymentId?: string
    readonly message?: string
    readonly environment?: DeliveryEnvironment
    readonly signal?: AbortSignal
    readonly onLog?: (message: string) => void
  },
): Promise<{ readonly deployment: DeploymentMetadata; readonly absoluteUrl: string }>
export async function publishWorkspace(
  first: HttpClient | VirtualWorkspace,
  projectIdOrHtml: string,
  inputOrEntryPath: DeliveryPublishInput | string | undefined,
  options?: DeliveryMutationOptions,
) {
  if ('request' in first && typeof first.request === 'function') {
    if (!isRecord(inputOrEntryPath)) {
      throw new DeliveryClientError('An exact publish source is required.', {
        code: 'frozen_delivery_source_required',
      })
    }
    return new DeliveryClient(first).publish(
      projectIdOrHtml,
      inputOrEntryPath as unknown as DeliveryPublishInput,
      options,
    )
  }
  throw frozenSourceError('Publishing')
}

/** New canonical signature. */
export function listDeployments(
  http: HttpClient,
  projectId: string,
  options?: DeliveryRequestOptions,
): Promise<DeploymentMetadata[]>
/** @deprecated Pass the Collaboration Platform HttpClient as the first argument. */
export function listDeployments(projectId: string): Promise<DeploymentMetadata[]>
export async function listDeployments(
  first: HttpClient | string,
  projectId?: string,
  options?: DeliveryRequestOptions,
) {
  if (typeof first !== 'string' && 'request' in first && typeof first.request === 'function') {
    if (!projectId) {
      throw new DeliveryClientError('projectId is required.', { code: 'invalid_project_id' })
    }
    return new DeliveryClient(first).list(projectId, options)
  }
  throw sharedClientError()
}

/** New canonical signature. */
export function rollbackDeployment(
  http: HttpClient,
  deploymentId: string,
  targetVersionId: string,
  options: DeliveryRollbackOptions,
): Promise<DeploymentMetadata>
/** @deprecated Pass the shared client and current strong ETag. */
export function rollbackDeployment(
  deploymentId: string,
  targetVersionId: string,
): Promise<DeploymentMetadata>
export async function rollbackDeployment(
  first: HttpClient | string,
  deploymentIdOrVersion: string,
  targetVersionId?: string,
  options?: DeliveryRollbackOptions,
) {
  if (typeof first !== 'string' && 'request' in first && typeof first.request === 'function') {
    if (!targetVersionId || !options?.ifMatch) {
      throw new DeliveryClientError('Rollback requires a target version and current ETag.', {
        code: 'deployment_precondition_required',
      })
    }
    return new DeliveryClient(first).rollback(
      deploymentIdOrVersion,
      targetVersionId,
      options,
    )
  }
  throw sharedClientError()
}

export function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.rel = 'noopener'
  anchor.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

function frozenSourceError(operation: string) {
  return new DeliveryClientError(
    `${operation} requires an approved frozen WorkspaceRevision or build manifest from the Go platform.`,
    { code: 'frozen_delivery_source_required' },
  )
}

function sharedClientError() {
  return new DeliveryClientError(
    'Delivery requests require the Collaboration Platform HttpClient.',
    { code: 'platform_http_client_required' },
  )
}

function filenameFromDisposition(value: string | null) {
  if (!value) return undefined
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(value)?.[1]
  if (encoded) {
    try {
      return decodeURIComponent(encoded)
    } catch {
      return undefined
    }
  }
  return /filename="([^"]+)"/i.exec(value)?.[1]
}

function safeSlug(value: string) {
  return value
    .normalize('NFKD')
    .replace(/[^a-zA-Z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80) || 'worksflow-project'
}

function positiveInteger(value: string | null) {
  const number = Number(value)
  return Number.isInteger(number) && number >= 0 ? number : 0
}
