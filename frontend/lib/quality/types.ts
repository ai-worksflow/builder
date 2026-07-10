export const QUALITY_CHECK_IDS = [
  'build',
  'type',
  'lint',
  'test',
  'accessibility',
  'dependency',
  'secret',
] as const

export type QualityCheckId = (typeof QUALITY_CHECK_IDS)[number]
export type QualitySeverity = 'error' | 'warning' | 'info'
export type QualityCheckStatus = 'passed' | 'warning' | 'failed' | 'skipped'
export type QualityRunStatus = 'passed' | 'failed'
export type QualityCategory =
  | 'build'
  | 'code'
  | 'test'
  | 'accessibility'
  | 'dependency'
  | 'security'

/**
 * Delivery APIs deliberately omit revisionNumber. The immutable identity is
 * artifactId + revisionId + contentHash (+ optional anchorId).
 */
export interface QualityVersionRef {
  readonly artifactId: string
  readonly revisionId: string
  readonly contentHash: string
  readonly anchorId?: string
  readonly revisionNumber?: number
}

export interface QualityRunInput {
  readonly workspaceRevision: QualityVersionRef
}

export interface QualityDiagnostic {
  readonly id?: string
  readonly checkId: QualityCheckId
  readonly code: string
  readonly severity: QualitySeverity
  readonly message: string
  readonly path?: string
  readonly line?: number
  readonly column?: number
  readonly suggestion?: string
}

export interface QualityScore {
  readonly earned: number
  readonly possible: number
  readonly percentage: number
}

export interface QualityCheckResult {
  readonly id: QualityCheckId
  readonly title: string
  readonly category: QualityCategory
  readonly status: QualityCheckStatus
  readonly score: QualityScore
  readonly diagnostics: readonly QualityDiagnostic[]
  readonly durationMs: number
  readonly exitCode?: number
  readonly output?: string
  readonly truncated?: boolean
}

export interface QualityRunMetadata {
  readonly runId: string
  readonly projectId: string
  readonly runnerVersion: string
  readonly executionMode: 'sandbox'
  readonly sandboxKind: string
  readonly startedAt: string
  readonly completedAt: string
  readonly workspaceRevision: QualityVersionRef
  readonly reportArtifactId?: string
  readonly reportRevisionId?: string
  readonly createdBy: string
  readonly version: number
  readonly etag: string
}

/**
 * UI-oriented view of the Go quality report. It preserves the existing
 * score/check/diagnostic shape while all facts originate on the server.
 */
export interface QualityRunResult {
  readonly status: QualityRunStatus
  readonly passed: boolean
  readonly score: QualityScore
  readonly metadata: QualityRunMetadata
  readonly checks: readonly QualityCheckResult[]
  readonly diagnostics: readonly QualityDiagnostic[]
  readonly durationMs: number
}

export interface QualityReportDto {
  readonly id: string
  readonly projectId: string
  readonly workflowRunId?: string
  readonly workspaceRevision: QualityVersionRef
  readonly status: QualityRunStatus
  readonly passed: boolean
  readonly score: number
  readonly runnerVersion: string
  readonly sandboxKind: string
  readonly checks: readonly QualityCheckDto[]
  readonly diagnostics: readonly QualityDiagnostic[]
  readonly reportArtifactId?: string
  readonly reportRevisionId?: string
  readonly createdBy: string
  readonly startedAt: string
  readonly completedAt?: string
  readonly version: number
  readonly etag: string
}

export interface QualityCheckDto {
  readonly id: QualityCheckId
  readonly status: QualityCheckStatus
  readonly exitCode?: number
  readonly durationMs: number
  readonly output?: string
  readonly truncated?: boolean
  readonly diagnostics: readonly QualityDiagnostic[]
}

