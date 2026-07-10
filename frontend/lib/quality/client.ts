import {
  PlatformAbortError,
  PlatformHttpError,
  PlatformNetworkError,
  type HttpClient,
} from '../platform/http'
import type {
  QualityCategory,
  QualityCheckDto,
  QualityCheckId,
  QualityReportDto,
  QualityRunInput,
  QualityRunResult,
  QualityVersionRef,
} from './types'

export class QualityClientError extends Error {
  readonly code: string
  readonly status?: number
  readonly retryAfterSeconds?: number

  constructor(
    message: string,
    options: { code?: string; status?: number; retryAfterSeconds?: number } = {},
  ) {
    super(message)
    this.name = 'QualityClientError'
    this.code = options.code ?? 'quality_client_error'
    this.status = options.status
    this.retryAfterSeconds = options.retryAfterSeconds
  }
}

export interface QualityRequestOptions {
  readonly signal?: AbortSignal
  readonly requestId?: string
  readonly idempotencyKey?: string
}

export interface QualityListOptions {
  readonly signal?: AbortSignal
  readonly requestId?: string
  readonly workspaceRevisionId?: string
}

interface LegacyQualityRunInput {
  readonly projectId?: string
  readonly files: readonly unknown[]
  readonly entryPath?: string
}

const checkPresentation: Readonly<Record<
  QualityCheckId,
  { readonly title: string; readonly category: QualityCategory }
>> = {
  build: { title: 'Build', category: 'build' },
  type: { title: 'Type check', category: 'code' },
  lint: { title: 'Lint', category: 'code' },
  test: { title: 'Tests', category: 'test' },
  accessibility: { title: 'Accessibility', category: 'accessibility' },
  dependency: { title: 'Dependencies', category: 'dependency' },
  secret: { title: 'Secret scan', category: 'security' },
}

function segment(value: string) {
  return encodeURIComponent(value)
}

function exactVersionRef(reference: QualityVersionRef) {
  return {
    artifactId: reference.artifactId,
    revisionId: reference.revisionId,
    contentHash: reference.contentHash,
    ...(reference.anchorId ? { anchorId: reference.anchorId } : {}),
  }
}

function percentage(value: number) {
  return Math.max(0, Math.min(100, Math.round(value)))
}

function checkScore(check: QualityCheckDto) {
  if (check.status === 'skipped') return { earned: 0, possible: 0, percentage: 100 }
  const value = check.status === 'passed' ? 100 : check.status === 'warning' ? 75 : 0
  return { earned: value, possible: 100, percentage: value }
}

function duration(startedAt: string, completedAt: string) {
  const started = Date.parse(startedAt)
  const completed = Date.parse(completedAt)
  if (!Number.isFinite(started) || !Number.isFinite(completed)) return 0
  return Math.max(0, completed - started)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isBuildArtifactRef(value: unknown) {
  return isRecord(value) &&
    typeof value.id === 'string' &&
    typeof value.contentRef === 'string' &&
    typeof value.contentHash === 'string' &&
    typeof value.buildHash === 'string' &&
    typeof value.entryPath === 'string' &&
    typeof value.fileCount === 'number' &&
    typeof value.totalBytes === 'number'
}

function isQualityReport(value: unknown): value is QualityReportDto {
  if (!isRecord(value) || !isRecord(value.workspaceRevision)) return false
  return (
    typeof value.id === 'string' &&
    typeof value.projectId === 'string' &&
    (value.status === 'passed' || value.status === 'failed') &&
    typeof value.passed === 'boolean' &&
    typeof value.score === 'number' &&
    typeof value.runnerVersion === 'string' &&
    typeof value.sandboxKind === 'string' &&
    Array.isArray(value.checks) &&
    Array.isArray(value.diagnostics) &&
    typeof value.startedAt === 'string' &&
    typeof value.version === 'number' &&
    typeof value.etag === 'string' &&
    typeof value.workspaceRevision.artifactId === 'string' &&
    typeof value.workspaceRevision.revisionId === 'string' &&
    typeof value.workspaceRevision.contentHash === 'string' &&
    (value.buildArtifact === undefined || isBuildArtifactRef(value.buildArtifact))
  )
}

function normalizeQualityReport(report: QualityReportDto): QualityRunResult {
  const completedAt = report.completedAt ?? report.startedAt
  const score = percentage(report.score)
  return {
    status: report.status,
    passed: report.passed,
    score: { earned: score, possible: 100, percentage: score },
    metadata: {
      runId: report.id,
      projectId: report.projectId,
      runnerVersion: report.runnerVersion,
      executionMode: 'sandbox',
      sandboxKind: report.sandboxKind,
      startedAt: report.startedAt,
      completedAt,
      workspaceRevision: report.workspaceRevision,
      reportArtifactId: report.reportArtifactId,
      reportRevisionId: report.reportRevisionId,
      createdBy: report.createdBy,
      version: report.version,
      etag: report.etag,
    },
    checks: report.checks.map((check) => ({
      ...check,
      ...checkPresentation[check.id],
      score: checkScore(check),
    })),
    diagnostics: report.diagnostics,
    durationMs: duration(report.startedAt, completedAt),
    buildArtifact: report.buildArtifact,
  }
}

function qualityError(error: unknown): never {
  if (error instanceof QualityClientError) throw error
  if (error instanceof PlatformAbortError) throw error
  if (error instanceof PlatformHttpError) {
    throw new QualityClientError(error.message, {
      code: error.code,
      status: error.status,
      retryAfterSeconds: error.retryAfterSeconds,
    })
  }
  if (error instanceof PlatformNetworkError) {
    throw new QualityClientError(error.message, { code: 'quality_unreachable' })
  }
  throw new QualityClientError(
    error instanceof Error ? error.message : 'The quality request failed.',
    { code: 'quality_request_failed' },
  )
}

function assertQualityReport(value: unknown) {
  if (!isQualityReport(value)) {
    throw new QualityClientError('The quality service returned an invalid report.', {
      code: 'invalid_quality_result',
    })
  }
  return value
}

export class QualityClient {
  readonly http: HttpClient

  constructor(http: HttpClient) {
    this.http = http
  }

  async run(
    projectId: string,
    workspaceRevision: QualityVersionRef,
    options: QualityRequestOptions = {},
  ) {
    try {
      const result = await this.http.post<
        { readonly qualityRun: QualityReportDto },
        QualityRunInput
      >(
        `/v1/projects/${segment(projectId)}/quality-runs`,
        { workspaceRevision: exactVersionRef(workspaceRevision) },
        {
          signal: options.signal,
          requestId: options.requestId,
          idempotencyKey: options.idempotencyKey ?? true,
        },
      )
      return normalizeQualityReport(assertQualityReport(result.data.qualityRun))
    } catch (error) {
      qualityError(error)
    }
  }

  async get(qualityRunId: string, options: Omit<QualityRequestOptions, 'idempotencyKey'> = {}) {
    try {
      const result = await this.http.get<{ readonly qualityRun: QualityReportDto }>(
        `/v1/quality-runs/${segment(qualityRunId)}`,
        { signal: options.signal, requestId: options.requestId },
      )
      return normalizeQualityReport(assertQualityReport(result.data.qualityRun))
    } catch (error) {
      qualityError(error)
    }
  }

  async list(projectId: string, options: QualityListOptions = {}) {
    try {
      const result = await this.http.get<{ readonly qualityRuns: readonly QualityReportDto[] }>(
        `/v1/projects/${segment(projectId)}/quality-runs`,
        {
          signal: options.signal,
          requestId: options.requestId,
          query: { workspaceRevisionId: options.workspaceRevisionId },
        },
      )
      if (!Array.isArray(result.data.qualityRuns)) {
        throw new QualityClientError('The quality service returned an invalid report list.', {
          code: 'invalid_quality_result',
        })
      }
      return result.data.qualityRuns.map((report) =>
        normalizeQualityReport(assertQualityReport(report)))
    } catch (error) {
      qualityError(error)
    }
  }
}

/** New canonical signature. */
export function requestQualityRun(
  http: HttpClient,
  projectId: string,
  workspaceRevision: QualityVersionRef,
  options?: QualityRequestOptions,
): Promise<QualityRunResult>
/**
 * @deprecated Mutable browser workspaces are not valid quality inputs. Kept
 * temporarily so dormant legacy UI code fails closed instead of calling a
 * removed Next route.
 */
export function requestQualityRun(
  input: LegacyQualityRunInput,
  options?: { readonly signal?: AbortSignal },
): Promise<QualityRunResult>
export async function requestQualityRun(
  first: HttpClient | LegacyQualityRunInput,
  projectIdOrOptions?: string | { readonly signal?: AbortSignal },
  workspaceRevision?: QualityVersionRef,
  options?: QualityRequestOptions,
) {
  if ('request' in first && typeof first.request === 'function') {
    if (typeof projectIdOrOptions !== 'string' || !workspaceRevision) {
      throw new QualityClientError('projectId and an exact WorkspaceRevision are required.', {
        code: 'frozen_workspace_revision_required',
      })
    }
    return new QualityClient(first).run(
      projectIdOrOptions,
      workspaceRevision,
      options,
    )
  }
  throw new QualityClientError(
    'Quality checks require an approved frozen WorkspaceRevision from the Go platform.',
    { code: 'frozen_workspace_revision_required' },
  )
}

export function isQualityRunResult(value: unknown): value is QualityRunResult {
  if (!isRecord(value) || !isRecord(value.metadata) || !isRecord(value.score)) return false
  return (
    (value.status === 'passed' || value.status === 'failed') &&
    typeof value.passed === 'boolean' &&
    typeof value.metadata.runId === 'string' &&
    typeof value.score.percentage === 'number' &&
    Array.isArray(value.checks) &&
    Array.isArray(value.diagnostics) &&
    typeof value.durationMs === 'number'
  )
}

export function qualityResultAsPromptContext(result: QualityRunResult) {
  const diagnostics = result.diagnostics.map((item) => ({
    check: item.checkId,
    code: item.code,
    severity: item.severity,
    message: item.message,
    path: item.path,
    line: item.line,
    column: item.column,
    suggestion: item.suggestion,
  }))
  return JSON.stringify(
    {
      runId: result.metadata.runId,
      workspaceRevision: result.metadata.workspaceRevision,
      score: result.score.percentage,
      passed: result.passed,
      diagnostics,
    },
    null,
    2,
  )
}
