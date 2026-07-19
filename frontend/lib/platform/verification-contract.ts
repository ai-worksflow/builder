import type { ExactRepositoryRefDto } from './sandbox-contract'

export type VerificationRunState =
  | 'queued'
  | 'claimed'
  | 'materializing'
  | 'preparing'
  | 'running'
  | 'collecting'
  | 'passed'
  | 'failed'
  | 'error'
  | 'cancelled'
  | 'timed_out'

export type VerificationRunAction = 'cancel' | 'retry' | 'view_receipt' | 'freeze' | 'create_release_bundle'
export type VerificationDecision = 'passed' | 'failed' | 'error'
export type VerificationCheckStatus = 'passed' | 'failed' | 'error'
export type VerificationDiagnosticSeverity = 'blocker' | 'warning' | 'info'

export interface VerificationProfileReferenceDto {
  readonly id: string
  readonly version: number
  readonly contentHash: string
}

export interface VerificationProfileSummaryDto {
  readonly verificationProfile: VerificationProfileReferenceDto
  readonly supportedTemplateRoles: readonly string[]
}

export interface VerificationProfileListDto {
  readonly profiles: readonly VerificationProfileSummaryDto[]
}

export interface CanonicalVerificationSubjectDto {
  readonly workspaceArtifactId: string
  readonly workspaceRevisionId: string
  readonly workspaceContentHash: string
}

export interface CanonicalVerificationRunDto {
  readonly schemaVersion: string
  readonly id: string
  readonly projectId: string
  readonly plan: VerificationPlanReferenceDto
  readonly requestKey: string
  readonly requestHash: string
  readonly reason: string
  readonly state: VerificationRunState
  readonly version: number
  readonly fenceEpoch: number
  readonly createdBy: string
  readonly updatedBy: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly replayed: boolean
}

export interface CanonicalVerificationRunViewDto {
  readonly run: CanonicalVerificationRunDto
  readonly subject: CanonicalVerificationSubjectDto
  readonly buildManifest: ExactRepositoryRefDto
  readonly buildContract: ExactRepositoryRefDto
  readonly fullStackTemplate: ExactRepositoryRefDto
  readonly verificationProfile: VerificationProfileReferenceDto
  readonly receipt?: ExactRepositoryRefDto
  readonly allowedActions: readonly VerificationRunAction[]
  readonly blockingReasons: readonly VerificationRunBlockingReasonDto[]
}

export interface VerificationPlanReferenceDto {
  readonly id: string
  readonly contentHash: string
}

export interface CandidateVerificationPlanSubjectDto {
  readonly sessionId: string
  readonly sessionVersion: number
  readonly candidateId: string
  readonly candidateSnapshotId: string
  readonly candidateVersion: number
  readonly journalSequence: number
  readonly sessionEpoch: number
  readonly writerLeaseEpoch: number
  readonly treeStore: string
  readonly treeOwnerId: string
  readonly treeRef: string
  readonly treeContentHash: string
  readonly treeHash: string
}

export interface CandidateVerificationRunDto {
  readonly schemaVersion: string
  readonly id: string
  readonly projectId: string
  readonly plan: VerificationPlanReferenceDto
  readonly requestKey: string
  readonly requestHash: string
  readonly reason: string
  readonly parentRunId?: string
  readonly retryReason?: string
  readonly state: VerificationRunState
  readonly version: number
  readonly fenceEpoch: number
  readonly terminalReason?: string
  readonly executionError?: string
  readonly startedAt?: string
  readonly finishedAt?: string
  readonly createdBy: string
  readonly updatedBy: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly replayed: boolean
}

export interface VerificationAttemptSummaryDto {
  readonly id: string
  readonly ordinal: number
  readonly parentAttemptId?: string
  readonly retryReason?: string
  readonly state: VerificationRunState
  readonly version: number
  readonly fenceEpoch: number
  readonly terminalReason?: string
  readonly executionError?: string
  readonly startedAt?: string
  readonly finishedAt?: string
  readonly createdAt: string
  readonly updatedAt: string
}

export interface VerificationRunBlockingReasonDto {
  readonly code: string
  readonly actions: readonly VerificationRunAction[]
  readonly detail: string
  readonly sourceRef?: ExactRepositoryRefDto
}

export interface CandidateVerificationRunViewDto {
  readonly run: CandidateVerificationRunDto
  readonly subject: CandidateVerificationPlanSubjectDto
  readonly buildManifest: ExactRepositoryRefDto
  readonly buildContract: ExactRepositoryRefDto
  readonly fullStackTemplate: ExactRepositoryRefDto
  readonly verificationProfile: VerificationProfileReferenceDto
  readonly receipt?: ExactRepositoryRefDto
  readonly receiptDecision?: VerificationDecision
  readonly checkCount: number
  readonly requiredCheckCount: number
  readonly completedCheckCount: number
  readonly attemptCount: number
  readonly latestAttempt?: VerificationAttemptSummaryDto
  readonly mustCount: number
  readonly mustPassedCount: number
  readonly blockerCount: number
  readonly warningCount: number
  readonly stale: boolean
  readonly allowedActions: readonly VerificationRunAction[]
  readonly blockingReasons: readonly VerificationRunBlockingReasonDto[]
}

export interface CandidateVerificationRunListDto {
  readonly runs: readonly CandidateVerificationRunViewDto[]
}

export interface VerificationBlobReferenceDto {
  readonly store: string
  readonly ownerId: string
  readonly ref: string
  readonly contentHash: string
  readonly byteSize: number
}

export interface VerificationDiagnosticDto {
  readonly id: string
  readonly code: string
  readonly severity: VerificationDiagnosticSeverity
  readonly message: string
  readonly path?: string
  readonly line: number
  readonly column: number
  readonly suggestion?: string
}

export interface VerificationCheckResultDto {
  readonly id: string
  readonly kind: string
  readonly serviceId?: string
  readonly commandId?: string
  readonly required: boolean
  readonly status: VerificationCheckStatus
  readonly attemptId: string
  readonly verifierImageDigest: string
  readonly argv: readonly string[]
  readonly workingDirectory: string
  readonly exitCode?: number
  readonly startedAt: string
  readonly completedAt: string
  readonly durationMs: number
  readonly attemptCount: number
  readonly stdout?: VerificationBlobReferenceDto
  readonly stderr?: VerificationBlobReferenceDto
  readonly truncated: boolean
  readonly redactionCount: number
  readonly oracleIds: readonly string[]
  readonly acceptanceCriterionIds: readonly string[]
  readonly obligationIds: readonly string[]
  readonly diagnostics: readonly VerificationDiagnosticDto[]
}

export interface VerificationCheckPageDto {
  readonly runId: string
  readonly receipt: ExactRepositoryRefDto
  readonly offset: number
  readonly limit: number
  readonly totalCount: number
  readonly checks: readonly VerificationCheckResultDto[]
}

export interface VerificationObligationCoverageDto {
  readonly obligationId: string
  readonly level: string
  readonly oracleIds: readonly string[]
  readonly checkIds: readonly string[]
  readonly status: string
}

export interface CandidateVerificationSubjectDto {
  readonly sessionId: string
  readonly candidateId: string
  readonly candidateSnapshotId: string
  readonly candidateVersion: number
  readonly journalSequence: number
  readonly sessionEpoch: number
  readonly writerLeaseEpoch: number
  readonly treeHash: string
}

export interface CandidateVerificationReceiptDto {
  readonly schemaVersion: string
  readonly id: string
  readonly runId: string
  readonly scope: 'candidate'
  readonly projectId: string
  readonly subject: CandidateVerificationSubjectDto
  readonly buildManifest: ExactRepositoryRefDto
  readonly buildContract: ExactRepositoryRefDto
  readonly fullStackTemplate: ExactRepositoryRefDto
  readonly verificationProfile: VerificationProfileReferenceDto
  readonly plan: VerificationPlanReferenceDto
  readonly attemptIds: readonly string[]
  readonly checks: readonly VerificationCheckResultDto[]
  readonly obligationCoverage: readonly VerificationObligationCoverageDto[]
  readonly mustCount: number
  readonly mustPassedCount: number
  readonly blockerCount: number
  readonly warningCount: number
  readonly decision: VerificationDecision
  readonly executionError?: string
  readonly payloadHash: string
  readonly createdBy: string
  readonly createdAt: string
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {}
}

function list(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : []
}

function text(value: unknown) {
  return typeof value === 'string' ? value : ''
}

function optionalText(value: unknown) {
  const normalized = text(value)
  return normalized || undefined
}

function number(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function optionalNumber(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function boolean(value: unknown) {
  return value === true
}

function strings(value: unknown) {
  return list(value).map(text).filter((entry) => entry.length > 0)
}

function exactReference(value: unknown): ExactRepositoryRefDto {
  const source = record(value)
  return { id: text(source.id), contentHash: text(source.contentHash) }
}

function optionalExactReference(value: unknown) {
  if (!value) return undefined
  const normalized = exactReference(value)
  return normalized.id && normalized.contentHash ? normalized : undefined
}

function profileReference(value: unknown): VerificationProfileReferenceDto {
  const source = record(value)
  return {
    id: text(source.id),
    version: number(source.version),
    contentHash: text(source.contentHash),
  }
}

function planReference(value: unknown): VerificationPlanReferenceDto {
  const source = record(value)
  return { id: text(source.id), contentHash: text(source.contentHash) }
}

function planSubject(value: unknown): CandidateVerificationPlanSubjectDto {
  const source = record(value)
  return {
    sessionId: text(source.sessionId),
    sessionVersion: number(source.sessionVersion),
    candidateId: text(source.candidateId),
    candidateSnapshotId: text(source.candidateSnapshotId),
    candidateVersion: number(source.candidateVersion),
    journalSequence: number(source.journalSequence),
    sessionEpoch: number(source.sessionEpoch),
    writerLeaseEpoch: number(source.writerLeaseEpoch),
    treeStore: text(source.treeStore),
    treeOwnerId: text(source.treeOwnerId),
    treeRef: text(source.treeRef),
    treeContentHash: text(source.treeContentHash),
    treeHash: text(source.treeHash),
  }
}

function run(value: unknown): CandidateVerificationRunDto {
  const source = record(value)
  return {
    schemaVersion: text(source.schemaVersion),
    id: text(source.id),
    projectId: text(source.projectId),
    plan: planReference(source.plan),
    requestKey: text(source.requestKey),
    requestHash: text(source.requestHash),
    reason: text(source.reason),
    parentRunId: optionalText(source.parentRunId),
    retryReason: optionalText(source.retryReason),
    state: text(source.state) as VerificationRunState,
    version: number(source.version),
    fenceEpoch: number(source.fenceEpoch),
    terminalReason: optionalText(source.terminalReason),
    executionError: optionalText(source.executionError),
    startedAt: optionalText(source.startedAt),
    finishedAt: optionalText(source.finishedAt),
    createdBy: text(source.createdBy),
    updatedBy: text(source.updatedBy),
    createdAt: text(source.createdAt),
    updatedAt: text(source.updatedAt),
    replayed: boolean(source.replayed),
  }
}

function attempt(value: unknown): VerificationAttemptSummaryDto {
  const source = record(value)
  return {
    id: text(source.id),
    ordinal: number(source.ordinal),
    parentAttemptId: optionalText(source.parentAttemptId),
    retryReason: optionalText(source.retryReason),
    state: text(source.state) as VerificationRunState,
    version: number(source.version),
    fenceEpoch: number(source.fenceEpoch),
    terminalReason: optionalText(source.terminalReason),
    executionError: optionalText(source.executionError),
    startedAt: optionalText(source.startedAt),
    finishedAt: optionalText(source.finishedAt),
    createdAt: text(source.createdAt),
    updatedAt: text(source.updatedAt),
  }
}

function optionalAttempt(value: unknown) {
  if (!value) return undefined
  const normalized = attempt(value)
  return normalized.id ? normalized : undefined
}

function blockingReason(value: unknown): VerificationRunBlockingReasonDto {
  const source = record(value)
  return {
    code: text(source.code),
    actions: strings(source.actions) as VerificationRunAction[],
    detail: text(source.detail),
    sourceRef: optionalExactReference(source.sourceRef),
  }
}

function blobReference(value: unknown): VerificationBlobReferenceDto {
  const source = record(value)
  return {
    store: text(source.store),
    ownerId: text(source.ownerId),
    ref: text(source.ref),
    contentHash: text(source.contentHash),
    byteSize: number(source.byteSize),
  }
}

function optionalBlobReference(value: unknown) {
  if (!value) return undefined
  const normalized = blobReference(value)
  return normalized.ref ? normalized : undefined
}

function diagnostic(value: unknown): VerificationDiagnosticDto {
  const source = record(value)
  return {
    id: text(source.id),
    code: text(source.code),
    severity: text(source.severity) as VerificationDiagnosticSeverity,
    message: text(source.message),
    path: optionalText(source.path),
    line: number(source.line),
    column: number(source.column),
    suggestion: optionalText(source.suggestion),
  }
}

function check(value: unknown): VerificationCheckResultDto {
  const source = record(value)
  return {
    id: text(source.id),
    kind: text(source.kind),
    serviceId: optionalText(source.serviceId),
    commandId: optionalText(source.commandId),
    required: boolean(source.required),
    status: text(source.status) as VerificationCheckStatus,
    attemptId: text(source.attemptId),
    verifierImageDigest: text(source.verifierImageDigest),
    argv: strings(source.argv),
    workingDirectory: text(source.workingDirectory),
    exitCode: optionalNumber(source.exitCode),
    startedAt: text(source.startedAt),
    completedAt: text(source.completedAt),
    durationMs: number(source.durationMs),
    attemptCount: number(source.attemptCount),
    stdout: optionalBlobReference(source.stdout),
    stderr: optionalBlobReference(source.stderr),
    truncated: boolean(source.truncated),
    redactionCount: number(source.redactionCount),
    oracleIds: strings(source.oracleIds),
    acceptanceCriterionIds: strings(source.acceptanceCriterionIds),
    obligationIds: strings(source.obligationIds),
    diagnostics: list(source.diagnostics).map(diagnostic),
  }
}

function coverage(value: unknown): VerificationObligationCoverageDto {
  const source = record(value)
  return {
    obligationId: text(source.obligationId),
    level: text(source.level),
    oracleIds: strings(source.oracleIds),
    checkIds: strings(source.checkIds),
    status: text(source.status),
  }
}

function receiptSubject(value: unknown): CandidateVerificationSubjectDto {
  const source = record(value)
  return {
    sessionId: text(source.sessionId),
    candidateId: text(source.candidateId),
    candidateSnapshotId: text(source.candidateSnapshotId),
    candidateVersion: number(source.candidateVersion),
    journalSequence: number(source.journalSequence),
    sessionEpoch: number(source.sessionEpoch),
    writerLeaseEpoch: number(source.writerLeaseEpoch),
    treeHash: text(source.treeHash),
  }
}

export function normalizeVerificationProfileList(value: unknown): VerificationProfileListDto {
  const source = record(value)
  return {
    profiles: list(source.profiles).map((entry) => {
      const profile = record(entry)
      return {
        verificationProfile: profileReference(profile.verificationProfile),
        supportedTemplateRoles: strings(profile.supportedTemplateRoles),
      }
    }),
  }
}

export function normalizeCanonicalVerificationRunView(value: unknown): CanonicalVerificationRunViewDto {
  const source = record(value)
  const rawRun = record(source.run)
  const rawSubject = record(source.subject)
  return {
    run: {
      schemaVersion: text(rawRun.schemaVersion),
      id: text(rawRun.id),
      projectId: text(rawRun.projectId),
      plan: exactReference(rawRun.plan),
      requestKey: text(rawRun.requestKey),
      requestHash: text(rawRun.requestHash),
      reason: text(rawRun.reason),
      state: text(rawRun.state) as VerificationRunState,
      version: number(rawRun.version),
      fenceEpoch: number(rawRun.fenceEpoch),
      createdBy: text(rawRun.createdBy),
      updatedBy: text(rawRun.updatedBy),
      createdAt: text(rawRun.createdAt),
      updatedAt: text(rawRun.updatedAt),
      replayed: boolean(rawRun.replayed),
    },
    subject: {
      workspaceArtifactId: text(rawSubject.workspaceArtifactId),
      workspaceRevisionId: text(rawSubject.workspaceRevisionId),
      workspaceContentHash: text(rawSubject.workspaceContentHash),
    },
    buildManifest: exactReference(source.buildManifest),
    buildContract: exactReference(source.buildContract),
    fullStackTemplate: exactReference(source.fullStackTemplate),
    verificationProfile: profileReference(source.verificationProfile),
    receipt: optionalExactReference(source.receipt),
    allowedActions: strings(source.allowedActions) as VerificationRunAction[],
    blockingReasons: list(source.blockingReasons).map(blockingReason),
  }
}

export function normalizeCandidateVerificationRunView(
  value: unknown,
): CandidateVerificationRunViewDto {
  const source = record(value)
  const decision = optionalText(source.receiptDecision)
  return {
    run: run(source.run),
    subject: planSubject(source.subject),
    buildManifest: exactReference(source.buildManifest),
    buildContract: exactReference(source.buildContract),
    fullStackTemplate: exactReference(source.fullStackTemplate),
    verificationProfile: profileReference(source.verificationProfile),
    receipt: optionalExactReference(source.receipt),
    receiptDecision: decision as VerificationDecision | undefined,
    checkCount: number(source.checkCount),
    requiredCheckCount: number(source.requiredCheckCount),
    completedCheckCount: number(source.completedCheckCount),
    attemptCount: number(source.attemptCount),
    latestAttempt: optionalAttempt(source.latestAttempt),
    mustCount: number(source.mustCount),
    mustPassedCount: number(source.mustPassedCount),
    blockerCount: number(source.blockerCount),
    warningCount: number(source.warningCount),
    stale: boolean(source.stale),
    allowedActions: strings(source.allowedActions) as VerificationRunAction[],
    blockingReasons: list(source.blockingReasons).map(blockingReason),
  }
}


export function normalizeCandidateVerificationRunList(
  value: unknown,
): CandidateVerificationRunListDto {
  const source = record(value)
  return {
    runs: list(source.runs).map(normalizeCandidateVerificationRunView),
  }
}


export function normalizeVerificationCheckPage(
  value: unknown,
): VerificationCheckPageDto {
  const source = record(value)
  return {
    runId: text(source.runId),
    receipt: exactReference(source.receipt),
    offset: number(source.offset),
    limit: number(source.limit),
    totalCount: number(source.totalCount),
    checks: list(source.checks).map(check),
  }
}

export function normalizeCandidateVerificationReceipt(
  value: unknown,
): CandidateVerificationReceiptDto {
  const source = record(value)
  return {
    schemaVersion: text(source.schemaVersion),
    id: text(source.id),
    runId: text(source.runId),
    scope: 'candidate',
    projectId: text(source.projectId),
    subject: receiptSubject(source.subject),
    buildManifest: exactReference(source.buildManifest),
    buildContract: exactReference(source.buildContract),
    fullStackTemplate: exactReference(source.fullStackTemplate),
    verificationProfile: profileReference(source.verificationProfile),
    plan: planReference(source.plan),
    attemptIds: strings(source.attemptIds),
    checks: list(source.checks).map(check),
    obligationCoverage: list(source.obligationCoverage).map(coverage),
    mustCount: number(source.mustCount),
    mustPassedCount: number(source.mustPassedCount),
    blockerCount: number(source.blockerCount),
    warningCount: number(source.warningCount),
    decision: text(source.decision) as VerificationDecision,
    executionError: optionalText(source.executionError),
    payloadHash: text(source.payloadHash),
    createdBy: text(source.createdBy),
    createdAt: text(source.createdAt),
  }
}
