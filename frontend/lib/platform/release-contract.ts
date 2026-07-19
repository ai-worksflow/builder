import type { ExactRepositoryRefDto } from './sandbox-contract'
import type {
  CanonicalVerificationSubjectDto,
  VerificationProfileReferenceDto,
} from './verification-contract'

export interface CanonicalReleaseArtifactDto {
  readonly id: string
  readonly kind: string
  readonly store: string
  readonly ref: string
  readonly contentHash: string
  readonly mediaType: string
  readonly byteSize: number
}

export interface ReleaseBundleDto {
  readonly schemaVersion: 'release-bundle/v1'
  readonly id: string
  readonly projectId: string
  readonly workspace: CanonicalVerificationSubjectDto
  readonly canonicalReceipt: ExactRepositoryRefDto
  readonly buildManifest: ExactRepositoryRefDto
  readonly buildContract: ExactRepositoryRefDto
  readonly fullStackTemplate: ExactRepositoryRefDto
  readonly verificationProfile: VerificationProfileReferenceDto
  readonly releaseArtifacts: readonly CanonicalReleaseArtifactDto[]
  readonly bundleHash: string
  readonly createdBy: string
  readonly createdAt: string
}

export interface ReleaseBundleViewDto {
  readonly bundle: ReleaseBundleDto
  readonly replayed: boolean
}

export interface ReleaseCapabilitiesDto {
  readonly schemaVersion: string
  readonly deliveryEnabled: boolean
}

export type ReleaseDeliveryRunState =
  | 'queued'
  | 'claimed'
  | 'submitting'
  | 'reconcile_wait'
  | 'reconciling'
  | 'deploying'
  | 'verifying'
  | 'reconcile_blocked'
  | 'passed'
  | 'healthy'
  | 'failed'
  | 'error'
  | 'cancelled'

const deliveryMutationLockedStates: ReadonlySet<ReleaseDeliveryRunState> = new Set([
  'queued',
  'claimed',
  'submitting',
  'reconcile_wait',
  'reconciling',
  'deploying',
  'verifying',
  'reconcile_blocked',
])

export function isReleaseDeliveryMutationLocked(state: ReleaseDeliveryRunState): boolean {
  return deliveryMutationLockedStates.has(state)
}

export function isReleaseDeliveryRunTerminal(state: ReleaseDeliveryRunState): boolean {
  return state === 'reconcile_blocked'
    || state === 'passed'
    || state === 'healthy'
    || state === 'failed'
    || state === 'error'
    || state === 'cancelled'
}

const deliveryRunDisplayPriority: Readonly<Record<ReleaseDeliveryRunState, number>> = {
  reconcile_blocked: 80,
  reconciling: 70,
  reconcile_wait: 60,
  submitting: 50,
  verifying: 40,
  deploying: 30,
  claimed: 20,
  queued: 10,
  passed: 0,
  healthy: 0,
  failed: 0,
  error: 0,
  cancelled: 0,
}

/**
 * Select the run the UI must surface from a newest-first server projection.
 * A locked run always wins over a terminal run, and the most dangerous
 * reconciliation state wins when more than one mutation is still unresolved.
 */
export function selectReleaseDeliveryRunForDisplay<T extends { readonly state: ReleaseDeliveryRunState }>(
  runs: readonly T[],
): T | undefined {
  return runs.reduce<T | undefined>((selected, run) => {
    if (!selected) return run
    const runPriority = deliveryRunDisplayPriority[run.state]
    const selectedPriority = deliveryRunDisplayPriority[selected.state]
    return runPriority > selectedPriority ? run : selected
  }, undefined)
}

export function hasReleaseDeliveryMutationLock(
  runs: readonly { readonly state: ReleaseDeliveryRunState }[],
): boolean {
  return runs.some((run) => isReleaseDeliveryMutationLocked(run.state))
}

export interface ReleasePreviewRunDto {
  readonly id: string
  readonly projectId: string
  readonly releaseBundle: ExactRepositoryRefDto
  readonly reason: string
  readonly state: ReleaseDeliveryRunState
  readonly version: number
  readonly createdBy: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly receipt?: ExactRepositoryRefDto
}

export interface ReleasePromotionApprovalDto {
  readonly schemaVersion: 'release-promotion-approval/v1'
  readonly id: string
  readonly projectId: string
  readonly releaseBundle: ExactRepositoryRefDto
  readonly previewReceipt: ExactRepositoryRefDto
  readonly reason: string
  readonly payloadHash: string
  readonly createdBy: string
  readonly createdAt: string
}

export interface ReleaseProductionRunDto {
  readonly id: string
  readonly projectId: string
  readonly operation: 'promote' | 'rollback'
  readonly releaseBundle: ExactRepositoryRefDto
  readonly previewReceipt: ExactRepositoryRefDto
  readonly promotionApproval: ExactRepositoryRefDto
  readonly sourceRevision?: ExactRepositoryRefDto
  readonly reason: string
  readonly state: ReleaseDeliveryRunState
  readonly version: number
  readonly createdBy: string
  readonly createdAt: string
  readonly updatedAt: string
  readonly receipt?: ExactRepositoryRefDto
  readonly revision?: ExactRepositoryRefDto
}

export interface ReleaseProductionCheckDto {
  readonly id: string
  readonly kind: string
  readonly status: 'passed' | 'failed'
  readonly detail?: string
}

export interface ReleaseControllerOperationReferenceDto {
  readonly operationId: string
  readonly resultHash: string
}

interface ReleasePreviewReceiptBaseDto {
  readonly id: string
  readonly runId: string
  readonly projectId: string
  readonly releaseBundle: ExactRepositoryRefDto
  readonly canonicalReceipt: ExactRepositoryRefDto
  readonly workspace: CanonicalVerificationSubjectDto
  readonly releaseArtifacts: readonly CanonicalReleaseArtifactDto[]
  readonly namespace: string
  readonly provider: string
  readonly providerRef: string
  readonly checks: readonly ReleaseProductionCheckDto[]
  readonly decision: 'passed' | 'failed'
  readonly payloadHash: string
  readonly createdBy: string
  readonly createdAt: string
}

/**
 * Legacy receipts have no v3 Controller result authority. The explicit null
 * keeps callers from confusing a missing historical reference with a v2
 * receipt whose mandatory authority was lost in transport.
 */
export type ReleasePreviewReceiptDto =
  | ReleasePreviewReceiptBaseDto & {
      readonly schemaVersion: 'release-preview-receipt/v1'
      readonly controllerOperation: null
    }
  | ReleasePreviewReceiptBaseDto & {
      readonly schemaVersion: 'release-preview-receipt/v2'
      readonly controllerOperation: ReleaseControllerOperationReferenceDto
    }

interface ReleaseProductionReceiptBaseDto {
  readonly id: string
  readonly runId: string
  readonly projectId: string
  readonly operation: 'promote' | 'rollback'
  readonly releaseBundle: ExactRepositoryRefDto
  readonly previewReceipt: ExactRepositoryRefDto
  readonly promotionApproval: ExactRepositoryRefDto
  readonly sourceRevision?: ExactRepositoryRefDto
  readonly provider: string
  readonly providerRef: string
  readonly publicUrl: string
  readonly checks: readonly ReleaseProductionCheckDto[]
  readonly decision: 'passed' | 'failed'
  readonly payloadHash: string
  readonly createdBy: string
  readonly createdAt: string
}

export type ReleaseProductionReceiptDto =
  | ReleaseProductionReceiptBaseDto & {
      readonly schemaVersion: 'release-production-receipt/v1'
      readonly controllerOperation: null
    }
  | ReleaseProductionReceiptBaseDto & {
      readonly schemaVersion: 'release-production-receipt/v2'
      readonly controllerOperation: ReleaseControllerOperationReferenceDto
    }

interface ReleaseDeploymentRevisionBaseDto {
  readonly id: string
  readonly runId: string
  readonly projectId: string
  readonly releaseBundle: ExactRepositoryRefDto
  readonly previewReceipt: ExactRepositoryRefDto
  readonly promotionApproval: ExactRepositoryRefDto
  readonly productionReceipt: ExactRepositoryRefDto
  readonly operation: 'promote' | 'rollback'
  readonly sourceRevision?: ExactRepositoryRefDto
  readonly provider: string
  readonly providerRef: string
  readonly publicUrl: string
  readonly checks: readonly ReleaseProductionCheckDto[]
  readonly payloadHash: string
  readonly createdBy: string
  readonly createdAt: string
}

export type ReleaseDeploymentRevisionDto =
  | ReleaseDeploymentRevisionBaseDto & {
      readonly schemaVersion: 'release-deployment-revision/v1'
      readonly controllerOperation: null
    }
  | ReleaseDeploymentRevisionBaseDto & {
      readonly schemaVersion: 'release-deployment-revision/v2'
      readonly controllerOperation: ReleaseControllerOperationReferenceDto
    }

export interface ReleasePreviewRunViewDto {
  readonly run: ReleasePreviewRunDto
  readonly replayed: boolean
}

export interface ReleasePromotionApprovalViewDto {
  readonly approval: ReleasePromotionApprovalDto
  readonly replayed: boolean
}

export interface ReleaseProductionRunViewDto {
  readonly run: ReleaseProductionRunDto
  readonly replayed: boolean
}

export type ReleaseDeliveryOperationKind = 'preview' | 'production'

export interface ReleaseDeliveryControllerIdentityDto {
  readonly schemaVersion: 'release-delivery-controller-identity/v1'
  readonly id: string
  readonly version: string
  readonly protocol: 'worksflow.release-delivery/v3'
  readonly trustKeyDigest: string
}

export interface ReleaseDeliveryReconciliationAttemptDto {
  readonly ordinal: number
  readonly kind: 'submit' | 'reconcile' | 'resubmit'
  readonly workerId: string
  readonly fenceEpoch: number
  readonly startedAt: string
  readonly completedAt: string
  readonly outcome: 'quarantined'
  readonly errorCode: string
  readonly errorDetail: string
}

export interface ReleaseDeliveryReconciliationObservationDto {
  readonly sequence: number
  readonly observedAt: string
}

export interface ReleaseDeliveryReconciliationErrorDto {
  readonly code: string
  readonly detail: string
}

/** Side-effect-free server snapshot used as the exact CAS precondition. */
export interface ReleaseDeliveryReconciliationBlockDto {
  readonly schemaVersion: 'release-delivery-reconciliation-block/v1'
  readonly projectId: string
  readonly runKind: ReleaseDeliveryOperationKind
  readonly runId: string
  readonly runSchemaVersion: 'release-preview-run/v2' | 'release-deployment-run/v2'
  readonly expectedRunVersion: number
  readonly operationId: string
  readonly operationRequestHash: string
  readonly controller: ReleaseDeliveryControllerIdentityDto
  readonly lastError: ReleaseDeliveryReconciliationErrorDto
}

/**
 * Immutable evidence for one governed edge from an exact reconcile_blocked
 * Run version to reconcile_wait. This authorizes only exact GET
 * reconciliation; it is not a controller result and does not assert or
 * replace a production head.
 */
export interface ReleaseDeliveryReconciliationCaseDto {
  readonly schemaVersion: 'release-delivery-reconciliation-case/v1'
  readonly id: string
  readonly projectId: string
  readonly runKind: ReleaseDeliveryOperationKind
  readonly runId: string
  readonly runSchemaVersion: 'release-preview-run/v2' | 'release-deployment-run/v2'
  readonly expectedRunVersion: number
  readonly operationId: string
  readonly operationRequestHash: string
  readonly controller: ReleaseDeliveryControllerIdentityDto
  readonly previousRemoteState: 'quarantined'
  readonly resumeRemoteState: 'submit_unknown' | 'accepted' | 'running'
  readonly submitAttemptCount: number
  readonly reconcileAttemptCount: number
  readonly lastAttempt: ReleaseDeliveryReconciliationAttemptDto
  readonly lastObservation?: ReleaseDeliveryReconciliationObservationDto
  readonly quarantineError: ReleaseDeliveryReconciliationErrorDto
  readonly actorId: string
  readonly reason: string
  readonly idempotencyKey: string
  readonly requestHash: string
  readonly caseHash: string
  readonly createdAt: string
}

export interface ReleaseDeliveryReconciliationCaseViewDto {
  readonly case: ReleaseDeliveryReconciliationCaseDto
  readonly replayed: boolean
}

export interface ResumeBlockedReleaseDeliveryInput {
  readonly runKind: ReleaseDeliveryOperationKind
  readonly runId: string
  readonly expectedVersion: number
  readonly expectedErrorCode: string
  readonly reason: string
}

export function selectReleaseDeliveryReconciliationCaseForRun(
  cases: readonly ReleaseDeliveryReconciliationCaseDto[],
  runKind: ReleaseDeliveryOperationKind,
  runId: string,
  expectedRunVersion: number,
): ReleaseDeliveryReconciliationCaseDto | undefined {
  return cases.find((item) => (
    item.runKind === runKind
    && item.runId === runId
    && item.expectedRunVersion === expectedRunVersion
  ))
}

export class ReleaseContractError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ReleaseContractError'
  }
}

function record(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {}
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function number(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function reconciliationRecord(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new ReleaseContractError(`The release service returned a malformed ${label}.`)
  }
  return value as Record<string, unknown>
}

function reconciliationText(value: unknown, maximum: number): value is string {
  return typeof value === 'string'
    && value.trim().length > 0
    && value.length <= maximum
    && !value.includes('\0')
}

function reconciliationCanonicalText(value: unknown, maximum: number): value is string {
  return reconciliationText(value, maximum) && value === value.trim()
}

function reconciliationInteger(value: unknown, positive = false): value is number {
  return typeof value === 'number'
    && Number.isSafeInteger(value)
    && value >= (positive ? 1 : 0)
}

function reconciliationUUID(value: unknown): value is string {
  return typeof value === 'string'
    && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value)
}

function reconciliationHash(value: unknown): value is string {
  return typeof value === 'string' && /^sha256:[0-9a-f]{64}$/.test(value)
}

function reconciliationTimestamp(value: unknown): value is string {
  return typeof value === 'string'
    && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(value)
    && !Number.isNaN(Date.parse(value))
}

function exactReference(value: unknown): ExactRepositoryRefDto {
  const source = record(value)
  return { id: text(source.id), contentHash: text(source.contentHash) }
}

function optionalExactReference(value: unknown): ExactRepositoryRefDto | undefined {
  const parsed = exactReference(value)
  return parsed.id && parsed.contentHash ? parsed : undefined
}

function releaseAuthorityRecord(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new ReleaseContractError(`The release service returned a malformed ${label}.`)
  }
  return value as Record<string, unknown>
}

function releaseExactAuthorityRecord(
  value: unknown,
  required: readonly string[],
  optional: readonly string[],
  label: string,
): Record<string, unknown> {
  const source = releaseAuthorityRecord(value, label)
  const allowed = new Set([...required, ...optional])
  const keys = Object.keys(source)
  if (required.some((key) => !Object.hasOwn(source, key)) ||
    keys.some((key) => !allowed.has(key))) {
    throw new ReleaseContractError(`The release service returned a malformed ${label}.`)
  }
  return source
}

function releaseUUID(value: unknown): value is string {
  return typeof value === 'string' &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value)
}

function releaseHash(value: unknown): value is string {
  return typeof value === 'string' && /^sha256:[0-9a-f]{64}$/.test(value)
}

const releaseTextEncoder = new TextEncoder()

function releaseStableID(value: unknown): value is string {
  return typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$/.test(value)
}

function releaseCanonicalString(value: unknown, maximum: number, allowEmpty = false): value is string {
  return typeof value === 'string'
    && value === value.trim()
    && releaseTextEncoder.encode(value).byteLength <= maximum
    && (allowEmpty || value.length > 0)
    && !value.includes('\0')
}

function releaseTimestamp(value: unknown): value is string {
  if (typeof value !== 'string') return false
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(value)
  if (!match || (match[7]?.endsWith('0') ?? false)) return false
  const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number)
  const calendar = new Date(0)
  calendar.setUTCFullYear(year!, month! - 1, day!)
  calendar.setUTCHours(hour!, minute!, second!, 0)
  return calendar.getUTCFullYear() === year
    && calendar.getUTCMonth() === month! - 1
    && calendar.getUTCDate() === day
    && calendar.getUTCHours() === hour
    && calendar.getUTCMinutes() === minute
    && calendar.getUTCSeconds() === second
}

function strictReleaseExactReference(value: unknown, label: string): ExactRepositoryRefDto {
  const source = releaseExactAuthorityRecord(value, ['id', 'contentHash'], [], label)
  if (!releaseUUID(source.id) || !releaseHash(source.contentHash)) {
    throw new ReleaseContractError(`The release service returned a malformed ${label}.`)
  }
  return { id: source.id, contentHash: source.contentHash }
}

function optionalStrictReleaseExactReference(
  source: Record<string, unknown>,
  field: string,
  label: string,
): ExactRepositoryRefDto | undefined {
  if (!Object.hasOwn(source, field)) return undefined
  return strictReleaseExactReference(source[field], label)
}

function strictReleaseWorkspace(
  value: unknown,
  label: string,
): CanonicalVerificationSubjectDto {
  const source = releaseExactAuthorityRecord(
    value,
    ['workspaceArtifactId', 'workspaceRevisionId', 'workspaceContentHash'],
    [],
    label,
  )
  if (!releaseUUID(source.workspaceArtifactId) || !releaseUUID(source.workspaceRevisionId) ||
    !releaseHash(source.workspaceContentHash)) {
    throw new ReleaseContractError(`The release service returned a malformed ${label}.`)
  }
  return {
    workspaceArtifactId: source.workspaceArtifactId,
    workspaceRevisionId: source.workspaceRevisionId,
    workspaceContentHash: source.workspaceContentHash,
  }
}

function strictReleaseArtifacts(
  value: unknown,
  label: string,
): readonly CanonicalReleaseArtifactDto[] {
  if (!Array.isArray(value) || value.length === 0 || value.length > 128) {
    throw new ReleaseContractError(`The release service returned malformed ${label}.`)
  }
  const artifacts = value.map((entry, index): CanonicalReleaseArtifactDto => {
    const source = releaseExactAuthorityRecord(
      entry,
      ['id', 'kind', 'store', 'ref', 'contentHash', 'mediaType', 'byteSize'],
      [],
      `${label} entry`,
    )
    const previous = index > 0
      ? releaseAuthorityRecord(value[index - 1], `${label} entry`).id
      : undefined
    if (!releaseStableID(source.id) || !releaseStableID(source.kind) ||
      !releaseCanonicalString(source.store, 80) || !releaseCanonicalString(source.ref, 2_000) ||
      !releaseHash(source.contentHash) || !releaseCanonicalString(source.mediaType, 256) ||
      typeof source.byteSize !== 'number' || !Number.isSafeInteger(source.byteSize) ||
      source.byteSize < 0 || source.byteSize > 10 * 2 ** 30 ||
      (typeof previous === 'string' && previous >= source.id)) {
      throw new ReleaseContractError(`The release service returned a malformed ${label} entry.`)
    }
    return {
      id: source.id,
      kind: source.kind,
      store: source.store,
      ref: source.ref,
      contentHash: source.contentHash,
      mediaType: source.mediaType,
      byteSize: source.byteSize,
    }
  })
  const requiredKinds = new Set([
    'migration',
    'runtime-config-schema',
    'health-readiness-contract',
    'sbom',
    'vulnerability-report',
    'provenance',
    'signature',
  ])
  const deployableKinds = new Set(['web-static', 'oci-image', 'service-image'])
  if ([...requiredKinds].some((kind) => !artifacts.some((artifact) => artifact.kind === kind)) ||
    !artifacts.some((artifact) => deployableKinds.has(artifact.kind))) {
    throw new ReleaseContractError(`The release service returned incomplete ${label}.`)
  }
  return artifacts
}

function strictControllerOperation(
  source: Record<string, unknown>,
  schemaVersion: string,
  v1: string,
  v2: string,
  label: string,
): ReleaseControllerOperationReferenceDto | null {
  const raw = source.controllerOperation
  if (schemaVersion === v1) {
    if (raw !== undefined && raw !== null) {
      throw new ReleaseContractError(`The release service returned a malformed ${label}.`)
    }
    return null
  }
  if (schemaVersion !== v2 || raw === undefined || raw === null) {
    throw new ReleaseContractError(`The release service returned a malformed ${label}.`)
  }
  const operation = releaseExactAuthorityRecord(
    raw,
    ['operationId', 'resultHash'],
    [],
    `${label} Controller operation`,
  )
  if (!releaseUUID(operation.operationId) || !releaseHash(operation.resultHash)) {
    throw new ReleaseContractError(
      `The release service returned a malformed ${label} Controller operation.`,
    )
  }
  return { operationId: operation.operationId, resultHash: operation.resultHash }
}

function strictReleaseChecks(value: unknown, label: string): readonly ReleaseProductionCheckDto[] {
  if (!Array.isArray(value) || value.length === 0 || value.length > 128) {
    throw new ReleaseContractError(`The release service returned malformed ${label} checks.`)
  }
  return value.map((entry, index) => {
    const source = releaseExactAuthorityRecord(entry, ['id', 'kind', 'status'], ['detail'], `${label} check`)
    const previous = index > 0
      ? releaseAuthorityRecord(value[index - 1], `${label} check`).id
      : undefined
    if (!releaseCanonicalString(source.id, 128) || !releaseCanonicalString(source.kind, 128) ||
      (source.status !== 'passed' && source.status !== 'failed') ||
      (typeof previous === 'string' && previous >= source.id) ||
      (Object.hasOwn(source, 'detail') &&
        !releaseCanonicalString(source.detail, 2_000))) {
      throw new ReleaseContractError(`The release service returned a malformed ${label} check.`)
    }
    const detail = source.detail as string | undefined
    return {
      id: source.id,
      kind: source.kind,
      status: source.status,
      ...(detail ? { detail } : {}),
    }
  })
}

function strictReleaseDecision(
  value: unknown,
  checks: readonly ReleaseProductionCheckDto[],
  label: string,
): 'passed' | 'failed' {
  if (value !== 'passed' && value !== 'failed') {
    throw new ReleaseContractError(`The release service returned a malformed ${label} decision.`)
  }
  const passed = checks.every((check) => check.status === 'passed')
  if ((value === 'passed') !== passed) {
    throw new ReleaseContractError(`The release service returned an inconsistent ${label} decision.`)
  }
  return value
}

function deliveryState(value: unknown): ReleaseDeliveryRunState {
  switch (value) {
    case 'queued':
    case 'claimed':
    case 'submitting':
    case 'reconcile_wait':
    case 'reconciling':
    case 'deploying':
    case 'verifying':
    case 'reconcile_blocked':
    case 'passed':
    case 'healthy':
    case 'failed':
    case 'error':
    case 'cancelled':
      return value
    default:
      // An unrecognized server state may still represent an in-flight remote
      // mutation. Project it to the operator-only quarantine state so an older
      // UI never reopens release controls after a protocol upgrade.
      return 'reconcile_blocked'
  }
}

function bundle(value: unknown): ReleaseBundleDto {
  const source = releaseExactAuthorityRecord(
    value,
    [
      'schemaVersion', 'id', 'projectId', 'workspace', 'canonicalReceipt', 'buildManifest',
      'buildContract', 'fullStackTemplate', 'verificationProfile', 'releaseArtifacts',
      'bundleHash', 'createdBy', 'createdAt',
    ],
    [],
    'immutable release Bundle',
  )
  const profile = releaseExactAuthorityRecord(
    source.verificationProfile,
    ['id', 'version', 'contentHash'],
    [],
    'immutable release Bundle VerificationProfile reference',
  )
  if (source.schemaVersion !== 'release-bundle/v1' || !releaseUUID(source.id) ||
    !releaseUUID(source.projectId) || !releaseUUID(source.createdBy) ||
    !releaseHash(source.bundleHash) || !releaseTimestamp(source.createdAt) ||
    !releaseStableID(profile.id) || typeof profile.version !== 'number' ||
    !Number.isSafeInteger(profile.version) || profile.version < 1 || !releaseHash(profile.contentHash)) {
    throw new ReleaseContractError('The release service returned a malformed immutable release Bundle.')
  }
  return {
    schemaVersion: source.schemaVersion,
    id: source.id,
    projectId: source.projectId,
    workspace: strictReleaseWorkspace(source.workspace, 'immutable release Bundle workspace'),
    canonicalReceipt: strictReleaseExactReference(
      source.canonicalReceipt,
      'immutable release Bundle CanonicalReceipt reference',
    ),
    buildManifest: strictReleaseExactReference(
      source.buildManifest,
      'immutable release Bundle BuildManifest reference',
    ),
    buildContract: strictReleaseExactReference(
      source.buildContract,
      'immutable release Bundle BuildContract reference',
    ),
    fullStackTemplate: strictReleaseExactReference(
      source.fullStackTemplate,
      'immutable release Bundle FullStackTemplate reference',
    ),
    verificationProfile: {
      id: profile.id,
      version: profile.version,
      contentHash: profile.contentHash,
    },
    releaseArtifacts: strictReleaseArtifacts(source.releaseArtifacts, 'immutable release Bundle artifacts'),
    bundleHash: source.bundleHash,
    createdBy: source.createdBy,
    createdAt: source.createdAt,
  }
}

export function normalizeReleaseBundle(value: unknown): ReleaseBundleDto {
  return bundle(value)
}

export function normalizeReleaseBundleView(value: unknown): ReleaseBundleViewDto {
  const source = releaseExactAuthorityRecord(
    value,
    ['bundle', 'replayed'],
    [],
    'immutable release Bundle response',
  )
  if (typeof source.replayed !== 'boolean') {
    throw new ReleaseContractError('The release service returned a malformed immutable release Bundle response.')
  }
  return { bundle: bundle(source.bundle), replayed: source.replayed }
}

export function normalizeReleaseCapabilities(value: unknown): ReleaseCapabilitiesDto {
  const source = record(value)
  return {
    schemaVersion: text(source.schemaVersion),
    deliveryEnabled: source.deliveryEnabled === true,
  }
}

export function normalizeReleasePreviewRun(value: unknown): ReleasePreviewRunDto {
  const source = record(value)
  const receipt = optionalExactReference(source.receipt)
  return {
    id: text(source.id),
    projectId: text(source.projectId),
    releaseBundle: exactReference(source.releaseBundle),
    reason: text(source.reason),
    state: deliveryState(source.state),
    version: number(source.version),
    createdBy: text(source.createdBy),
    createdAt: text(source.createdAt),
    updatedAt: text(source.updatedAt),
    ...(receipt ? { receipt } : {}),
  }
}

export function normalizeReleasePromotionApproval(value: unknown): ReleasePromotionApprovalDto {
  const source = releaseExactAuthorityRecord(
    value,
    [
      'schemaVersion', 'id', 'projectId', 'releaseBundle', 'previewReceipt', 'reason',
      'payloadHash', 'createdBy', 'createdAt',
    ],
    [],
    'immutable release PromotionApproval',
  )
  if (source.schemaVersion !== 'release-promotion-approval/v1' || !releaseUUID(source.id) ||
    !releaseUUID(source.projectId) || !releaseUUID(source.createdBy) ||
    !releaseCanonicalString(source.reason, 1_000) || !releaseHash(source.payloadHash) ||
    !releaseTimestamp(source.createdAt)) {
    throw new ReleaseContractError('The release service returned a malformed immutable release PromotionApproval.')
  }
  return {
    schemaVersion: source.schemaVersion,
    id: source.id,
    projectId: source.projectId,
    releaseBundle: strictReleaseExactReference(
      source.releaseBundle,
      'immutable release PromotionApproval Bundle reference',
    ),
    previewReceipt: strictReleaseExactReference(
      source.previewReceipt,
      'immutable release PromotionApproval PreviewReceipt reference',
    ),
    reason: source.reason,
    payloadHash: source.payloadHash,
    createdBy: source.createdBy,
    createdAt: source.createdAt,
  }
}

export function normalizeReleaseProductionRun(value: unknown): ReleaseProductionRunDto {
  const source = record(value)
  const sourceRevision = optionalExactReference(source.sourceRevision)
  const receipt = optionalExactReference(source.receipt)
  const revision = optionalExactReference(source.revision)
  return {
    id: text(source.id),
    projectId: text(source.projectId),
    operation: source.operation === 'rollback' ? 'rollback' : 'promote',
    releaseBundle: exactReference(source.releaseBundle),
    previewReceipt: exactReference(source.previewReceipt),
    promotionApproval: exactReference(source.promotionApproval),
    ...(sourceRevision ? { sourceRevision } : {}),
    reason: text(source.reason),
    state: deliveryState(source.state),
    version: number(source.version),
    createdBy: text(source.createdBy),
    createdAt: text(source.createdAt),
    updatedAt: text(source.updatedAt),
    ...(receipt ? { receipt } : {}),
    ...(revision ? { revision } : {}),
  }
}

export function normalizeReleaseProductionReceipt(value: unknown): ReleaseProductionReceiptDto {
  const envelope = releaseAuthorityRecord(value, 'release production receipt')
  const schemaVersion = envelope.schemaVersion
  const required = [
    'schemaVersion', 'id', 'runId', 'projectId', 'operation', 'releaseBundle',
    'previewReceipt', 'promotionApproval', 'provider', 'providerRef', 'publicUrl',
    'checks', 'decision', 'payloadHash', 'createdBy', 'createdAt',
  ] as const
  const source = schemaVersion === 'release-production-receipt/v1'
    ? releaseExactAuthorityRecord(value, required, ['sourceRevision'], 'release production receipt')
    : schemaVersion === 'release-production-receipt/v2'
      ? releaseExactAuthorityRecord(
        value,
        [...required, 'controllerOperation'],
        ['sourceRevision'],
        'release production receipt',
      )
      : (() => {
        throw new ReleaseContractError('The release service returned a malformed release production receipt.')
      })()
  const controllerOperation = strictControllerOperation(
    source,
    typeof schemaVersion === 'string' ? schemaVersion : '',
    'release-production-receipt/v1',
    'release-production-receipt/v2',
    'release production receipt',
  )
  const sourceRevision = optionalStrictReleaseExactReference(
    source,
    'sourceRevision',
    'release production receipt source revision',
  )
  if (!releaseUUID(source.id) || !releaseUUID(source.runId) || !releaseUUID(source.projectId) ||
    !releaseUUID(source.createdBy) || !releaseHash(source.payloadHash) ||
    !releaseTimestamp(source.createdAt) ||
    (source.operation !== 'promote' && source.operation !== 'rollback') ||
    !releaseCanonicalString(source.provider, 128) ||
    !releaseCanonicalString(source.providerRef, 1_000) ||
    !releaseCanonicalString(source.publicUrl, 2_000, true) ||
    (source.operation === 'promote' && sourceRevision !== undefined) ||
    (source.operation === 'rollback' && sourceRevision === undefined)) {
    throw new ReleaseContractError('The release service returned a malformed release production receipt.')
  }
  const checks = strictReleaseChecks(source.checks, 'release production receipt')
  const decision = strictReleaseDecision(source.decision, checks, 'release production receipt')
  const common: ReleaseProductionReceiptBaseDto = {
    id: source.id,
    runId: source.runId,
    projectId: source.projectId,
    operation: source.operation,
    releaseBundle: strictReleaseExactReference(
      source.releaseBundle,
      'release production receipt Bundle reference',
    ),
    previewReceipt: strictReleaseExactReference(
      source.previewReceipt,
      'release production receipt PreviewReceipt reference',
    ),
    promotionApproval: strictReleaseExactReference(
      source.promotionApproval,
      'release production receipt PromotionApproval reference',
    ),
    ...(sourceRevision ? { sourceRevision } : {}),
    provider: source.provider,
    providerRef: source.providerRef,
    publicUrl: source.publicUrl,
    checks,
    decision,
    payloadHash: source.payloadHash,
    createdBy: source.createdBy,
    createdAt: source.createdAt,
  }
  return schemaVersion === 'release-production-receipt/v1'
    ? { ...common, schemaVersion, controllerOperation: null }
    : { ...common, schemaVersion: 'release-production-receipt/v2', controllerOperation: controllerOperation! }
}

export function normalizeReleasePreviewReceipt(value: unknown): ReleasePreviewReceiptDto {
  const envelope = releaseAuthorityRecord(value, 'release preview receipt')
  const schemaVersion = envelope.schemaVersion
  const required = [
    'schemaVersion', 'id', 'runId', 'projectId', 'releaseBundle', 'canonicalReceipt',
    'workspace', 'releaseArtifacts', 'namespace', 'provider', 'providerRef', 'checks',
    'decision', 'payloadHash', 'createdBy', 'createdAt',
  ] as const
  const source = schemaVersion === 'release-preview-receipt/v1'
    ? releaseExactAuthorityRecord(value, required, [], 'release preview receipt')
    : schemaVersion === 'release-preview-receipt/v2'
      ? releaseExactAuthorityRecord(
        value,
        [...required, 'controllerOperation'],
        [],
        'release preview receipt',
      )
      : (() => {
        throw new ReleaseContractError('The release service returned a malformed release preview receipt.')
      })()
  const controllerOperation = strictControllerOperation(
    source,
    typeof schemaVersion === 'string' ? schemaVersion : '',
    'release-preview-receipt/v1',
    'release-preview-receipt/v2',
    'release preview receipt',
  )
  if (!releaseUUID(source.id) || !releaseUUID(source.runId) || !releaseUUID(source.projectId) ||
    !releaseUUID(source.createdBy) || !releaseHash(source.payloadHash) ||
    !releaseTimestamp(source.createdAt) || !releaseCanonicalString(source.namespace, 200) ||
    !releaseCanonicalString(source.provider, 128) ||
    !releaseCanonicalString(source.providerRef, 1_000)) {
    throw new ReleaseContractError('The release service returned a malformed release preview receipt.')
  }
  const checks = strictReleaseChecks(source.checks, 'release preview receipt')
  const decision = strictReleaseDecision(source.decision, checks, 'release preview receipt')
  const common: ReleasePreviewReceiptBaseDto = {
    id: source.id,
    runId: source.runId,
    projectId: source.projectId,
    releaseBundle: strictReleaseExactReference(
      source.releaseBundle,
      'release preview receipt Bundle reference',
    ),
    canonicalReceipt: strictReleaseExactReference(
      source.canonicalReceipt,
      'release preview receipt CanonicalReceipt reference',
    ),
    workspace: strictReleaseWorkspace(source.workspace, 'release preview receipt workspace'),
    releaseArtifacts: strictReleaseArtifacts(
      source.releaseArtifacts,
      'release preview receipt artifacts',
    ),
    namespace: source.namespace,
    provider: source.provider,
    providerRef: source.providerRef,
    checks,
    decision,
    payloadHash: source.payloadHash,
    createdBy: source.createdBy,
    createdAt: source.createdAt,
  }
  return schemaVersion === 'release-preview-receipt/v1'
    ? { ...common, schemaVersion, controllerOperation: null }
    : { ...common, schemaVersion: 'release-preview-receipt/v2', controllerOperation: controllerOperation! }
}

export function normalizeReleaseDeploymentRevision(value: unknown): ReleaseDeploymentRevisionDto {
  const envelope = releaseAuthorityRecord(value, 'release deployment revision')
  const schemaVersion = envelope.schemaVersion
  const required = [
    'schemaVersion', 'id', 'runId', 'projectId', 'releaseBundle', 'previewReceipt',
    'promotionApproval', 'productionReceipt', 'operation', 'provider', 'providerRef',
    'publicUrl', 'checks', 'payloadHash', 'createdBy', 'createdAt',
  ] as const
  const source = schemaVersion === 'release-deployment-revision/v1'
    ? releaseExactAuthorityRecord(value, required, ['sourceRevision'], 'release deployment revision')
    : schemaVersion === 'release-deployment-revision/v2'
      ? releaseExactAuthorityRecord(
        value,
        [...required, 'controllerOperation'],
        ['sourceRevision'],
        'release deployment revision',
      )
      : (() => {
        throw new ReleaseContractError('The release service returned a malformed release deployment revision.')
      })()
  const controllerOperation = strictControllerOperation(
    source,
    typeof schemaVersion === 'string' ? schemaVersion : '',
    'release-deployment-revision/v1',
    'release-deployment-revision/v2',
    'release deployment revision',
  )
  const sourceRevision = optionalStrictReleaseExactReference(
    source,
    'sourceRevision',
    'release deployment source revision',
  )
  if (!releaseUUID(source.id) || !releaseUUID(source.runId) || !releaseUUID(source.projectId) ||
    !releaseUUID(source.createdBy) || !releaseHash(source.payloadHash) ||
    !releaseTimestamp(source.createdAt) ||
    (source.operation !== 'promote' && source.operation !== 'rollback') ||
    !releaseCanonicalString(source.provider, 128) ||
    !releaseCanonicalString(source.providerRef, 1_000) ||
    !releaseCanonicalString(source.publicUrl, 2_000) ||
    (source.operation === 'promote' && sourceRevision !== undefined) ||
    (source.operation === 'rollback' && sourceRevision === undefined)) {
    throw new ReleaseContractError('The release service returned a malformed release deployment revision.')
  }
  const checks = strictReleaseChecks(source.checks, 'release deployment revision')
  if (checks.some((check) => check.status !== 'passed')) {
    throw new ReleaseContractError('The release service returned a failed release deployment revision check.')
  }
  const common: ReleaseDeploymentRevisionBaseDto = {
    id: source.id,
    runId: source.runId,
    projectId: source.projectId,
    releaseBundle: strictReleaseExactReference(
      source.releaseBundle,
      'release deployment Bundle reference',
    ),
    previewReceipt: strictReleaseExactReference(
      source.previewReceipt,
      'release deployment PreviewReceipt reference',
    ),
    promotionApproval: strictReleaseExactReference(
      source.promotionApproval,
      'release deployment PromotionApproval reference',
    ),
    productionReceipt: strictReleaseExactReference(
      source.productionReceipt,
      'release deployment ProductionReceipt reference',
    ),
    operation: source.operation,
    ...(sourceRevision ? { sourceRevision } : {}),
    provider: source.provider,
    providerRef: source.providerRef,
    publicUrl: source.publicUrl,
    checks,
    payloadHash: source.payloadHash,
    createdBy: source.createdBy,
    createdAt: source.createdAt,
  }
  return schemaVersion === 'release-deployment-revision/v1'
    ? { ...common, schemaVersion, controllerOperation: null }
    : { ...common, schemaVersion: 'release-deployment-revision/v2', controllerOperation: controllerOperation! }
}

export function normalizeReleasePreviewRunView(value: unknown): ReleasePreviewRunViewDto {
  const source = record(value)
  return { run: normalizeReleasePreviewRun(source.run), replayed: source.replayed === true }
}

export function normalizeReleasePromotionApprovalView(value: unknown): ReleasePromotionApprovalViewDto {
  const source = releaseExactAuthorityRecord(
    value,
    ['approval', 'replayed'],
    [],
    'immutable release PromotionApproval response',
  )
  if (typeof source.replayed !== 'boolean') {
    throw new ReleaseContractError(
      'The release service returned a malformed immutable release PromotionApproval response.',
    )
  }
  return { approval: normalizeReleasePromotionApproval(source.approval), replayed: source.replayed }
}

export function normalizeReleaseProductionRunView(value: unknown): ReleaseProductionRunViewDto {
  const source = record(value)
  return { run: normalizeReleaseProductionRun(source.run), replayed: source.replayed === true }
}

export function normalizeReleasePreviewRunList(value: unknown): readonly ReleasePreviewRunDto[] {
  const source = record(value)
  return (Array.isArray(source.runs) ? source.runs : []).map(normalizeReleasePreviewRun)
}

export function normalizeReleaseProductionRunList(value: unknown): readonly ReleaseProductionRunDto[] {
  const source = record(value)
  return (Array.isArray(source.runs) ? source.runs : []).map(normalizeReleaseProductionRun)
}

function malformedReconciliationCase(): never {
  throw new ReleaseContractError('The release service returned a malformed immutable delivery reconciliation case.')
}

function malformedReconciliationBlock(): never {
  throw new ReleaseContractError('The release service returned a malformed blocked delivery reconciliation snapshot.')
}

export function normalizeReleaseDeliveryReconciliationCase(
  value: unknown,
): ReleaseDeliveryReconciliationCaseDto {
  const source = reconciliationRecord(value, 'immutable delivery reconciliation case')
  const controller = reconciliationRecord(source.controller, 'delivery controller identity')
  const lastAttempt = reconciliationRecord(source.lastAttempt, 'delivery reconciliation attempt')
  const quarantineError = reconciliationRecord(source.quarantineError, 'delivery quarantine error')
  const runKind = source.runKind === 'preview' || source.runKind === 'production'
    ? source.runKind
    : malformedReconciliationCase()
  const runSchemaVersion = runKind === 'preview'
    ? 'release-preview-run/v2'
    : 'release-deployment-run/v2'
  const resumeRemoteState = source.resumeRemoteState === 'submit_unknown'
    || source.resumeRemoteState === 'accepted'
    || source.resumeRemoteState === 'running'
    ? source.resumeRemoteState
    : malformedReconciliationCase()
  const attemptKind = lastAttempt.kind === 'submit'
    || lastAttempt.kind === 'reconcile'
    || lastAttempt.kind === 'resubmit'
    ? lastAttempt.kind
    : malformedReconciliationCase()

  let lastObservation: ReleaseDeliveryReconciliationObservationDto | undefined
  if (source.lastObservation !== null && source.lastObservation !== undefined) {
    const observation = reconciliationRecord(source.lastObservation, 'delivery reconciliation observation')
    if (!reconciliationInteger(observation.sequence, true) || !reconciliationTimestamp(observation.observedAt)) {
      malformedReconciliationCase()
    }
    lastObservation = {
      sequence: observation.sequence,
      observedAt: observation.observedAt,
    }
  }

  if (
    source.schemaVersion !== 'release-delivery-reconciliation-case/v1'
    || !reconciliationUUID(source.id)
    || !reconciliationUUID(source.projectId)
    || !reconciliationUUID(source.runId)
    || source.runSchemaVersion !== runSchemaVersion
    || !reconciliationInteger(source.expectedRunVersion, true)
    || !reconciliationUUID(source.operationId)
    || !reconciliationHash(source.operationRequestHash)
    || controller.schemaVersion !== 'release-delivery-controller-identity/v1'
    || !reconciliationCanonicalText(controller.id, 200)
    || !reconciliationCanonicalText(controller.version, 120)
    || controller.protocol !== 'worksflow.release-delivery/v3'
    || !reconciliationHash(controller.trustKeyDigest)
    || source.previousRemoteState !== 'quarantined'
    || !reconciliationInteger(source.submitAttemptCount)
    || !reconciliationInteger(source.reconcileAttemptCount)
    || !reconciliationInteger(lastAttempt.ordinal, true)
    || !reconciliationCanonicalText(lastAttempt.workerId, 200)
    || !reconciliationInteger(lastAttempt.fenceEpoch, true)
    || !reconciliationTimestamp(lastAttempt.startedAt)
    || !reconciliationTimestamp(lastAttempt.completedAt)
    || lastAttempt.outcome !== 'quarantined'
    || !reconciliationCanonicalText(lastAttempt.errorCode, 128)
    || !reconciliationText(lastAttempt.errorDetail, 4000)
    || !reconciliationCanonicalText(quarantineError.code, 128)
    || !reconciliationText(quarantineError.detail, 4000)
    || quarantineError.code !== lastAttempt.errorCode
    || quarantineError.detail !== lastAttempt.errorDetail
    || !reconciliationUUID(source.actorId)
    || !reconciliationCanonicalText(source.reason, 1000)
    || !reconciliationCanonicalText(source.idempotencyKey, 128)
    || !reconciliationHash(source.requestHash)
    || !reconciliationHash(source.caseHash)
    || !reconciliationTimestamp(source.createdAt)
    || ((resumeRemoteState === 'accepted' || resumeRemoteState === 'running') && !lastObservation)
  ) {
    malformedReconciliationCase()
  }

  return {
    schemaVersion: 'release-delivery-reconciliation-case/v1',
    id: source.id,
    projectId: source.projectId,
    runKind,
    runId: source.runId,
    runSchemaVersion,
    expectedRunVersion: source.expectedRunVersion,
    operationId: source.operationId,
    operationRequestHash: source.operationRequestHash,
    controller: {
      schemaVersion: 'release-delivery-controller-identity/v1',
      id: controller.id,
      version: controller.version,
      protocol: 'worksflow.release-delivery/v3',
      trustKeyDigest: controller.trustKeyDigest,
    },
    previousRemoteState: 'quarantined',
    resumeRemoteState,
    submitAttemptCount: source.submitAttemptCount,
    reconcileAttemptCount: source.reconcileAttemptCount,
    lastAttempt: {
      ordinal: lastAttempt.ordinal,
      kind: attemptKind,
      workerId: lastAttempt.workerId,
      fenceEpoch: lastAttempt.fenceEpoch,
      startedAt: lastAttempt.startedAt,
      completedAt: lastAttempt.completedAt,
      outcome: 'quarantined',
      errorCode: lastAttempt.errorCode,
      errorDetail: lastAttempt.errorDetail,
    },
    ...(lastObservation ? { lastObservation } : {}),
    quarantineError: {
      code: quarantineError.code,
      detail: quarantineError.detail,
    },
    actorId: source.actorId,
    reason: source.reason,
    idempotencyKey: source.idempotencyKey,
    requestHash: source.requestHash,
    caseHash: source.caseHash,
    createdAt: source.createdAt,
  }
}

export function normalizeReleaseDeliveryReconciliationBlock(
  value: unknown,
): ReleaseDeliveryReconciliationBlockDto {
  const source = reconciliationRecord(value, 'blocked delivery reconciliation snapshot')
  const controller = reconciliationRecord(source.controller, 'delivery controller identity')
  const lastError = reconciliationRecord(source.lastError, 'delivery quarantine error')
  const runKind = source.runKind === 'preview' || source.runKind === 'production'
    ? source.runKind
    : malformedReconciliationBlock()
  const runSchemaVersion = runKind === 'preview'
    ? 'release-preview-run/v2'
    : 'release-deployment-run/v2'

  if (
    source.schemaVersion !== 'release-delivery-reconciliation-block/v1'
    || !reconciliationUUID(source.projectId)
    || !reconciliationUUID(source.runId)
    || source.runSchemaVersion !== runSchemaVersion
    || !reconciliationInteger(source.expectedRunVersion, true)
    || !reconciliationUUID(source.operationId)
    || !reconciliationHash(source.operationRequestHash)
    || controller.schemaVersion !== 'release-delivery-controller-identity/v1'
    || !reconciliationCanonicalText(controller.id, 200)
    || !reconciliationCanonicalText(controller.version, 120)
    || controller.protocol !== 'worksflow.release-delivery/v3'
    || !reconciliationHash(controller.trustKeyDigest)
    || !reconciliationCanonicalText(lastError.code, 128)
    || !reconciliationText(lastError.detail, 4000)
  ) {
    malformedReconciliationBlock()
  }

  return {
    schemaVersion: 'release-delivery-reconciliation-block/v1',
    projectId: source.projectId,
    runKind,
    runId: source.runId,
    runSchemaVersion,
    expectedRunVersion: source.expectedRunVersion,
    operationId: source.operationId,
    operationRequestHash: source.operationRequestHash,
    controller: {
      schemaVersion: 'release-delivery-controller-identity/v1',
      id: controller.id,
      version: controller.version,
      protocol: 'worksflow.release-delivery/v3',
      trustKeyDigest: controller.trustKeyDigest,
    },
    lastError: {
      code: lastError.code,
      detail: lastError.detail,
    },
  }
}

export function normalizeReleaseDeliveryReconciliationCaseList(
  value: unknown,
): readonly ReleaseDeliveryReconciliationCaseDto[] {
  const source = reconciliationRecord(value, 'delivery reconciliation case list')
  if (!Array.isArray(source.cases)) {
    throw new ReleaseContractError('The release service returned a malformed delivery reconciliation case list.')
  }
  return source.cases.map(normalizeReleaseDeliveryReconciliationCase)
}

export function normalizeReleaseDeliveryReconciliationCaseView(
  value: unknown,
): ReleaseDeliveryReconciliationCaseViewDto {
  const source = reconciliationRecord(value, 'delivery reconciliation case response')
  if (typeof source.replayed !== 'boolean') {
    throw new ReleaseContractError('The release service returned a malformed delivery reconciliation case response.')
  }
  return {
    case: normalizeReleaseDeliveryReconciliationCase(source.case),
    replayed: source.replayed,
  }
}
