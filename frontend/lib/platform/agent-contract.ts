import {
  normalizeSandboxSession,
  type SandboxSessionDto,
} from './sandbox-contract'

export type AgentAttemptState =
  | 'pending'
  | 'ready'
  | 'queued'
  | 'claimed'
  | 'running'
  | 'patch_ready'
  | 'validating'
  | 'review_ready'
  | 'verification_failed'
  | 'failed'
  | 'timed_out'
  | 'cancelled'
  | 'stale'

export type AgentEvidenceKind =
  | 'patch'
  | 'structured_result'
  | 'stdout'
  | 'stderr'
  | 'validation'

export interface AgentExactReferenceDto {
  readonly id: string
  readonly contentHash: string
}

export interface AgentBlobReferenceDto {
  readonly store: string
  readonly ownerId: string
  readonly ref: string
  readonly contentHash: string
  readonly byteSize: number
}

export interface AgentAttemptEvidenceDto {
  readonly patch?: AgentBlobReferenceDto
  readonly structuredResult?: AgentBlobReferenceDto
  readonly stdout?: AgentBlobReferenceDto
  readonly stderr?: AgentBlobReferenceDto
  readonly validation?: AgentBlobReferenceDto
}

export interface AgentAttemptLeaseDto {
  readonly workerId: string
  readonly epoch: number
  readonly expiresAt: string
}

export interface AgentAttemptDto {
  readonly schemaVersion: string
  readonly id: string
  readonly projectId: string
  readonly sandboxSessionId: string
  readonly candidateId: string
  readonly taskCapsule: AgentExactReferenceDto
  readonly contextPack: AgentExactReferenceDto
  readonly baseCandidateTreeHash: string
  readonly buildContractHash: string
  readonly templateReleaseHashes: readonly string[]
  readonly executor: {
    readonly adapter: string
    readonly provider: string
    readonly model: string
    readonly runnerImageDigest: string
    readonly modelPolicyHash: string
    readonly parametersHash: string
    readonly promptHash: string
    readonly outputSchemaHash: string
    readonly toolchainHash: string
  }
  readonly requestKeyHash: string
  readonly configurationHash: string
  readonly parentAttemptId?: string
  readonly retryReason?: string
  readonly state: AgentAttemptState
  readonly version: number
  readonly fenceEpoch: number
  readonly lease?: AgentAttemptLeaseDto
  readonly evidence: AgentAttemptEvidenceDto
  readonly exitReason?: string
  readonly createdBy: string
  readonly createdAt: string
  readonly startedAt?: string
  readonly finishedAt?: string
  readonly updatedAt: string
}

export interface AgentTaskCapsuleDto {
  readonly schemaVersion: string
  readonly taskId: string
  readonly taskKey: string
  readonly projectId: string
  readonly sandboxSessionId: string
  readonly candidateId: string
  readonly candidateVersion: number
  readonly candidateSessionEpoch: number
  readonly candidateWriterLeaseEpoch: number
  readonly baseCandidateTreeHash: string
  readonly objective: string
  readonly obligationIds: readonly string[]
  readonly acceptanceCriterionIds: readonly string[]
  readonly readSet: readonly string[]
  readonly writeSet: readonly string[]
  readonly protectedPaths: readonly string[]
  readonly preconditions: readonly string[]
  readonly postconditions: readonly string[]
  readonly verificationCommandIds: readonly string[]
  readonly allowedTools: readonly string[]
  readonly contentHash: string
  readonly createdAt: string
}

export interface AgentTaskAttemptResultDto {
  readonly contextPack: {
    readonly id: string
    readonly contentHash: string
    readonly itemCount: number
  }
  readonly taskCapsule: AgentTaskCapsuleDto
  readonly attempt: AgentAttemptDto
  readonly replayed: boolean
}

export interface AgentAttemptEventDto {
  readonly schemaVersion: string
  readonly attemptId: string
  readonly sequence: number
  readonly versionFrom: number
  readonly versionTo: number
  readonly stateFrom: AgentAttemptState
  readonly stateTo: AgentAttemptState
  readonly fenceEpochFrom: number
  readonly fenceEpochTo: number
  readonly kind: string
  readonly actorId: string
  readonly workerId?: string
  readonly reason: string
  readonly lease?: AgentAttemptLeaseDto
  readonly evidence: AgentAttemptEvidenceDto
  readonly exitReason?: string
  readonly createdAt: string
}

export interface AgentFileOperationDto {
  readonly id: string
  readonly kind: 'file.upsert' | 'file.delete'
  readonly path: string
  readonly fromPath?: string
  readonly expectedHash?: string
  readonly contentHash?: string
  readonly byteSize?: number
  readonly mode?: string
}

export interface AgentPlatformPatchDto {
  readonly schemaVersion: string
  readonly attemptId: string
  readonly projectId: string
  readonly candidateId: string
  readonly taskCapsule: AgentExactReferenceDto
  readonly configurationHash: string
  readonly baseTreeHash: string
  readonly proposedTreeHash: string
  readonly operations: readonly AgentFileOperationDto[]
  readonly changedBytes: number
  readonly contentHash: string
}

export interface AgentStructuredResultDto {
  readonly summary: string
  readonly changedPaths: readonly string[]
  readonly verification: readonly {
    readonly commandId: string
    readonly status: 'not_run' | 'passed' | 'failed'
    readonly note: string
  }[]
  readonly blockers: readonly string[]
}

export interface AgentPatchValidationDto {
  readonly schemaVersion: string
  readonly scope: string
  readonly attemptId: string
  readonly projectId: string
  readonly taskCapsule: AgentExactReferenceDto
  readonly patch: AgentBlobReferenceDto
  readonly patchContentHash: string
  readonly baseTreeHash: string
  readonly proposedTreeHash: string
  readonly checks: readonly {
    readonly id: string
    readonly status: string
    readonly detail: string
  }[]
  readonly decision: string
  readonly independentQualityRequired: boolean
  readonly contentHash: string
}

export type AgentPatchDisposition = 'planned' | 'conflicted' | 'noop'

export interface AgentPatchFileStateDto {
  readonly exists: boolean
  readonly contentHash?: string
  readonly byteSize?: number
  readonly mode?: string
}

export interface AgentPatchConflictDto {
  readonly path: string
  readonly reason: string
  readonly base: AgentPatchFileStateDto
  readonly current: AgentPatchFileStateDto
  readonly proposed: AgentPatchFileStateDto
}

export interface AgentPatchMergePlanDto {
  readonly schemaVersion: string
  readonly id: string
  readonly operationId: string
  readonly projectId: string
  readonly sandboxSessionId: string
  readonly candidateId: string
  readonly attemptId: string
  readonly attemptVersion: number
  readonly patchReference: AgentBlobReferenceDto
  readonly patchRawHash: string
  readonly patchContentHash: string
  readonly baseTreeHash: string
  readonly currentTreeHash: string
  readonly proposedTreeHash: string
  readonly plannedTreeHash: string
  readonly expectedSessionVersion: number
  readonly expectedSessionEpoch: number
  readonly expectedCandidateVersion: number
  readonly expectedCandidateJournalSequence: number
  readonly expectedWriterLeaseEpoch: number
  readonly disposition: AgentPatchDisposition
  readonly operations: readonly AgentFileOperationDto[]
  readonly conflicts: readonly AgentPatchConflictDto[]
  readonly contentHash: string
  readonly createdBy: string
  readonly createdAt: string
}

export interface AgentPatchUndoPlanDto {
  readonly schemaVersion: string
  readonly id: string
  readonly operationId: string
  readonly projectId: string
  readonly sandboxSessionId: string
  readonly candidateId: string
  readonly mergeId: string
  readonly mergePlanContentHash: string
  readonly mergeApplicationContentHash: string
  readonly mergeBeforeTreeHash: string
  readonly mergedTreeHash: string
  readonly currentTreeHash: string
  readonly plannedTreeHash: string
  readonly expectedSessionVersion: number
  readonly expectedSessionEpoch: number
  readonly expectedCandidateVersion: number
  readonly expectedCandidateJournalSequence: number
  readonly expectedWriterLeaseEpoch: number
  readonly disposition: AgentPatchDisposition
  readonly operations: readonly AgentFileOperationDto[]
  readonly conflicts: readonly AgentPatchConflictDto[]
  readonly contentHash: string
  readonly createdBy: string
  readonly createdAt: string
}

export interface AgentPatchApplicationDto {
  readonly schemaVersion: string
  readonly operationId: string
  readonly planContentHash: string
  readonly projectId: string
  readonly candidateId: string
  readonly contentHash: string
  readonly journalSequenceFrom: number
  readonly journalSequenceTo: number
  readonly candidateVersionFrom: number
  readonly candidateVersionTo: number
  readonly beforeTreeHash: string
  readonly afterTreeHash: string
  readonly appliedBy: string
  readonly appliedAt: string
}

export interface AgentEventPageDto {
  readonly events: readonly AgentAttemptEventDto[]
  readonly afterSequence: number
  readonly lastSequence: number
}

export interface AgentPatchMergeResultDto {
  readonly plan: AgentPatchMergePlanDto
  readonly application?: AgentPatchApplicationDto
  readonly session?: SandboxSessionDto
  readonly replayed: boolean
}

export interface AgentPatchUndoResultDto {
  readonly plan: AgentPatchUndoPlanDto
  readonly application?: AgentPatchApplicationDto
  readonly session?: SandboxSessionDto
  readonly replayed: boolean
}

export interface AgentPatchUndoHistoryItemDto {
  readonly plan: AgentPatchUndoPlanDto
  readonly application?: AgentPatchApplicationDto
}

export interface AgentPatchMergeHistoryItemDto {
  readonly plan: AgentPatchMergePlanDto
  readonly application?: AgentPatchApplicationDto
  readonly undo?: AgentPatchUndoHistoryItemDto
}

type UnknownRecord = Record<string, unknown>

export class AgentContractError extends Error {
  constructor(detail: string) {
    super(`The Agent service returned a malformed protocol object: ${detail}.`)
    this.name = 'AgentContractError'
  }
}

const sha256Pattern = /^sha256:[0-9a-f]{64}$/
const attemptStates = new Set<AgentAttemptState>([
  'pending', 'ready', 'queued', 'claimed', 'running', 'patch_ready', 'validating',
  'review_ready', 'verification_failed', 'failed', 'timed_out', 'cancelled', 'stale',
])
const eventKinds = new Set([
  'lifecycle.advanced', 'lease.claimed', 'lease.reclaimed', 'lease.renewed',
  'control.cancelled', 'control.stale',
])

function invalid(detail: string): never {
  throw new AgentContractError(detail)
}

function has(source: UnknownRecord, key: string) {
  return Object.prototype.hasOwnProperty.call(source, key)
}

function exactRecord(
  value: unknown,
  detail: string,
  required: readonly string[],
  optional: readonly string[] = [],
): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return invalid(`${detail} must be an object`)
  }
  const source = value as UnknownRecord
  const allowed = new Set([...required, ...optional])
  for (const key of Object.keys(source)) {
    if (!allowed.has(key)) return invalid(`${detail}.${key} is unknown`)
  }
  for (const key of required) {
    if (!has(source, key)) return invalid(`${detail}.${key} is required`)
  }
  return source
}

function text(value: unknown, detail: string, nonEmpty = false) {
  if (typeof value !== 'string' || (nonEmpty && value.length === 0)) {
    return invalid(`${detail} must be ${nonEmpty ? 'a non-empty ' : ''}string`)
  }
  return value
}

function digest(value: unknown, detail: string) {
  const result = text(value, detail, true)
  if (!sha256Pattern.test(result)) return invalid(`${detail} must be a canonical sha256 digest`)
  return result
}

function integer(value: unknown, detail: string, minimum = 0) {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < minimum) {
    return invalid(`${detail} must be a safe integer greater than or equal to ${minimum}`)
  }
  return value
}

function truth(value: unknown, detail: string) {
  if (typeof value !== 'boolean') return invalid(`${detail} must be a boolean`)
  return value
}

function array(value: unknown, detail: string) {
  if (!Array.isArray(value)) return invalid(`${detail} must be an array`)
  return value
}

function textList(value: unknown, detail: string) {
  return array(value, detail).map((entry, index) => text(entry, `${detail}[${index}]`))
}

function timestamp(value: unknown, detail: string) {
  const result = text(value, detail, true)
  if (!Number.isFinite(Date.parse(result))) return invalid(`${detail} must be a timestamp`)
  return result
}

function exactReference(value: unknown, detail: string): AgentExactReferenceDto {
  const source = exactRecord(value, detail, ['id', 'contentHash'])
  return { id: text(source.id, `${detail}.id`, true), contentHash: digest(source.contentHash, `${detail}.contentHash`) }
}

function blobReference(value: unknown, detail: string): AgentBlobReferenceDto {
  const source = exactRecord(value, detail, ['store', 'ownerId', 'ref', 'contentHash', 'byteSize'])
  return {
    store: text(source.store, `${detail}.store`, true),
    ownerId: text(source.ownerId, `${detail}.ownerId`, true),
    ref: text(source.ref, `${detail}.ref`, true),
    contentHash: digest(source.contentHash, `${detail}.contentHash`),
    byteSize: integer(source.byteSize, `${detail}.byteSize`),
  }
}

function optional<T>(source: UnknownRecord, key: string, parse: (value: unknown, detail: string) => T, detail: string) {
  return has(source, key) ? parse(source[key], `${detail}.${key}`) : undefined
}

function evidence(value: unknown, detail: string): AgentAttemptEvidenceDto {
  const source = exactRecord(value, detail, [], [
    'patch', 'structuredResult', 'stdout', 'stderr', 'validation',
  ])
  return {
    patch: optional(source, 'patch', blobReference, detail),
    structuredResult: optional(source, 'structuredResult', blobReference, detail),
    stdout: optional(source, 'stdout', blobReference, detail),
    stderr: optional(source, 'stderr', blobReference, detail),
    validation: optional(source, 'validation', blobReference, detail),
  }
}

function lease(value: unknown, detail: string): AgentAttemptLeaseDto {
  const source = exactRecord(value, detail, ['workerId', 'epoch', 'expiresAt'])
  return {
    workerId: text(source.workerId, `${detail}.workerId`, true),
    epoch: integer(source.epoch, `${detail}.epoch`, 1),
    expiresAt: timestamp(source.expiresAt, `${detail}.expiresAt`),
  }
}

function state(value: unknown, detail: string): AgentAttemptState {
  const result = text(value, detail, true) as AgentAttemptState
  if (!attemptStates.has(result)) return invalid(`${detail} is not a supported AgentAttempt state`)
  return result
}

export function normalizeAgentAttempt(value: unknown): AgentAttemptDto {
  const detail = 'attempt'
  const source = exactRecord(value, detail, [
    'schemaVersion', 'id', 'projectId', 'sandboxSessionId', 'candidateId', 'taskCapsule',
    'contextPack', 'baseCandidateTreeHash', 'buildContractHash', 'templateReleaseHashes',
    'executor', 'requestKeyHash', 'configurationHash', 'state', 'version', 'fenceEpoch',
    'evidence', 'createdBy', 'createdAt', 'updatedAt',
  ], ['parentAttemptId', 'retryReason', 'lease', 'exitReason', 'startedAt', 'finishedAt'])
  if (source.schemaVersion !== 'agent-attempt/v1') return invalid(`${detail}.schemaVersion is unsupported`)
  const executor = exactRecord(source.executor, `${detail}.executor`, [
    'adapter', 'provider', 'model', 'runnerImageDigest', 'modelPolicyHash', 'parametersHash',
    'promptHash', 'outputSchemaHash', 'toolchainHash',
  ])
  return {
    schemaVersion: 'agent-attempt/v1',
    id: text(source.id, `${detail}.id`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    sandboxSessionId: text(source.sandboxSessionId, `${detail}.sandboxSessionId`, true),
    candidateId: text(source.candidateId, `${detail}.candidateId`, true),
    taskCapsule: exactReference(source.taskCapsule, `${detail}.taskCapsule`),
    contextPack: exactReference(source.contextPack, `${detail}.contextPack`),
    baseCandidateTreeHash: digest(source.baseCandidateTreeHash, `${detail}.baseCandidateTreeHash`),
    buildContractHash: digest(source.buildContractHash, `${detail}.buildContractHash`),
    templateReleaseHashes: array(source.templateReleaseHashes, `${detail}.templateReleaseHashes`).map(
      (entry, index) => digest(entry, `${detail}.templateReleaseHashes[${index}]`),
    ),
    executor: {
      adapter: text(executor.adapter, `${detail}.executor.adapter`, true),
      provider: text(executor.provider, `${detail}.executor.provider`, true),
      model: text(executor.model, `${detail}.executor.model`, true),
      runnerImageDigest: digest(executor.runnerImageDigest, `${detail}.executor.runnerImageDigest`),
      modelPolicyHash: digest(executor.modelPolicyHash, `${detail}.executor.modelPolicyHash`),
      parametersHash: digest(executor.parametersHash, `${detail}.executor.parametersHash`),
      promptHash: digest(executor.promptHash, `${detail}.executor.promptHash`),
      outputSchemaHash: digest(executor.outputSchemaHash, `${detail}.executor.outputSchemaHash`),
      toolchainHash: digest(executor.toolchainHash, `${detail}.executor.toolchainHash`),
    },
    requestKeyHash: digest(source.requestKeyHash, `${detail}.requestKeyHash`),
    configurationHash: digest(source.configurationHash, `${detail}.configurationHash`),
    parentAttemptId: optional(source, 'parentAttemptId', (entry, name) => text(entry, name, true), detail),
    retryReason: optional(source, 'retryReason', (entry, name) => text(entry, name, true), detail),
    state: state(source.state, `${detail}.state`),
    version: integer(source.version, `${detail}.version`, 1),
    fenceEpoch: integer(source.fenceEpoch, `${detail}.fenceEpoch`),
    lease: optional(source, 'lease', lease, detail),
    evidence: evidence(source.evidence, `${detail}.evidence`),
    exitReason: optional(source, 'exitReason', (entry, name) => text(entry, name, true), detail),
    createdBy: text(source.createdBy, `${detail}.createdBy`, true),
    createdAt: timestamp(source.createdAt, `${detail}.createdAt`),
    startedAt: optional(source, 'startedAt', timestamp, detail),
    finishedAt: optional(source, 'finishedAt', timestamp, detail),
    updatedAt: timestamp(source.updatedAt, `${detail}.updatedAt`),
  }
}

function contextItem(value: unknown, detail: string) {
  const source = exactRecord(value, detail, ['key', 'kind', 'content', 'required'], ['source', 'path'])
  text(source.key, `${detail}.key`, true)
  text(source.kind, `${detail}.kind`, true)
  blobReference(source.content, `${detail}.content`)
  truth(source.required, `${detail}.required`)
  if (has(source, 'source')) exactReference(source.source, `${detail}.source`)
  if (has(source, 'path')) text(source.path, `${detail}.path`, true)
}

function normalizeContextPack(value: unknown) {
  const detail = 'contextPack'
  const source = exactRecord(value, detail, [
    'schemaVersion', 'id', 'projectId', 'candidateId', 'baseCandidateTreeHash',
    'buildContract', 'items', 'contentHash', 'createdBy', 'createdAt',
  ])
  if (source.schemaVersion !== 'agent-context-pack/v1') return invalid(`${detail}.schemaVersion is unsupported`)
  const items = array(source.items, `${detail}.items`)
  items.forEach((entry, index) => contextItem(entry, `${detail}.items[${index}]`))
  return {
    id: text(source.id, `${detail}.id`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    candidateId: text(source.candidateId, `${detail}.candidateId`, true),
    baseCandidateTreeHash: digest(source.baseCandidateTreeHash, `${detail}.baseCandidateTreeHash`),
    buildContract: exactReference(source.buildContract, `${detail}.buildContract`),
    contentHash: digest(source.contentHash, `${detail}.contentHash`),
    itemCount: items.length,
  }
}

function normalizeTaskCapsule(value: unknown): AgentTaskCapsuleDto & {
  readonly buildContract: AgentExactReferenceDto
  readonly templateReleases: readonly AgentExactReferenceDto[]
  readonly contextPack: AgentExactReferenceDto
  readonly outputSchemaHash: string
} {
  const detail = 'taskCapsule'
  const source = exactRecord(value, detail, [
    'schemaVersion', 'taskId', 'taskKey', 'projectId', 'sandboxSessionId', 'candidateId',
    'candidateVersion', 'candidateSessionEpoch', 'candidateWriterLeaseEpoch',
    'baseCandidateTreeHash', 'buildContract', 'templateReleases', 'contextPack', 'objective',
    'obligationIds', 'acceptanceCriterionIds', 'readSet', 'writeSet', 'protectedPaths',
    'preconditions', 'postconditions', 'verificationCommandIds', 'allowedTools',
    'networkPolicy', 'budgets', 'outputSchemaHash', 'contentHash', 'createdBy', 'createdAt',
  ])
  if (source.schemaVersion !== 'agent-task-capsule/v1') return invalid(`${detail}.schemaVersion is unsupported`)
  const network = exactRecord(source.networkPolicy, `${detail}.networkPolicy`, ['mode', 'allowedHosts'])
  text(network.mode, `${detail}.networkPolicy.mode`, true)
  textList(network.allowedHosts, `${detail}.networkPolicy.allowedHosts`)
  const budgets = exactRecord(source.budgets, `${detail}.budgets`, [
    'wallTimeSeconds', 'maxInputTokens', 'maxOutputTokens', 'maxCommands', 'maxLogBytes', 'maxPatchBytes',
  ])
  for (const key of Object.keys(budgets)) integer(budgets[key], `${detail}.budgets.${key}`, 1)
  return {
    schemaVersion: 'agent-task-capsule/v1',
    taskId: text(source.taskId, `${detail}.taskId`, true),
    taskKey: text(source.taskKey, `${detail}.taskKey`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    sandboxSessionId: text(source.sandboxSessionId, `${detail}.sandboxSessionId`, true),
    candidateId: text(source.candidateId, `${detail}.candidateId`, true),
    candidateVersion: integer(source.candidateVersion, `${detail}.candidateVersion`, 1),
    candidateSessionEpoch: integer(source.candidateSessionEpoch, `${detail}.candidateSessionEpoch`, 1),
    candidateWriterLeaseEpoch: integer(source.candidateWriterLeaseEpoch, `${detail}.candidateWriterLeaseEpoch`),
    baseCandidateTreeHash: digest(source.baseCandidateTreeHash, `${detail}.baseCandidateTreeHash`),
    buildContract: exactReference(source.buildContract, `${detail}.buildContract`),
    templateReleases: array(source.templateReleases, `${detail}.templateReleases`).map(
      (entry, index) => exactReference(entry, `${detail}.templateReleases[${index}]`),
    ),
    contextPack: exactReference(source.contextPack, `${detail}.contextPack`),
    objective: text(source.objective, `${detail}.objective`, true),
    obligationIds: textList(source.obligationIds, `${detail}.obligationIds`),
    acceptanceCriterionIds: textList(source.acceptanceCriterionIds, `${detail}.acceptanceCriterionIds`),
    readSet: textList(source.readSet, `${detail}.readSet`),
    writeSet: textList(source.writeSet, `${detail}.writeSet`),
    protectedPaths: textList(source.protectedPaths, `${detail}.protectedPaths`),
    preconditions: textList(source.preconditions, `${detail}.preconditions`),
    postconditions: textList(source.postconditions, `${detail}.postconditions`),
    verificationCommandIds: textList(source.verificationCommandIds, `${detail}.verificationCommandIds`),
    allowedTools: textList(source.allowedTools, `${detail}.allowedTools`),
    outputSchemaHash: digest(source.outputSchemaHash, `${detail}.outputSchemaHash`),
    contentHash: digest(source.contentHash, `${detail}.contentHash`),
    createdAt: timestamp(source.createdAt, `${detail}.createdAt`),
  }
}

export function normalizeAgentTaskAttemptResult(value: unknown): AgentTaskAttemptResultDto {
  const source = exactRecord(value, 'taskAttemptResult', [
    'contextPack', 'taskCapsule', 'attempt', 'replayed',
  ])
  const contextPack = normalizeContextPack(source.contextPack)
  const taskCapsule = normalizeTaskCapsule(source.taskCapsule)
  const attempt = normalizeAgentAttempt(source.attempt)
  if (
    attempt.taskCapsule.id !== taskCapsule.taskId ||
    attempt.taskCapsule.contentHash !== taskCapsule.contentHash ||
    attempt.contextPack.id !== contextPack.id || attempt.contextPack.contentHash !== contextPack.contentHash ||
    taskCapsule.contextPack.id !== contextPack.id || taskCapsule.contextPack.contentHash !== contextPack.contentHash ||
    attempt.projectId !== taskCapsule.projectId || attempt.projectId !== contextPack.projectId ||
    attempt.sandboxSessionId !== taskCapsule.sandboxSessionId ||
    attempt.candidateId !== taskCapsule.candidateId || attempt.candidateId !== contextPack.candidateId ||
    attempt.baseCandidateTreeHash !== taskCapsule.baseCandidateTreeHash ||
    attempt.baseCandidateTreeHash !== contextPack.baseCandidateTreeHash ||
    attempt.buildContractHash !== taskCapsule.buildContract.contentHash ||
    attempt.buildContractHash !== contextPack.buildContract.contentHash ||
    attempt.executor.outputSchemaHash !== taskCapsule.outputSchemaHash ||
    attempt.templateReleaseHashes.length !== taskCapsule.templateReleases.length ||
    attempt.templateReleaseHashes.some((hash, index) => hash !== taskCapsule.templateReleases[index]?.contentHash)
  ) {
    return invalid('taskAttemptResult exact lineage identities do not match')
  }
  return {
    contextPack: { id: contextPack.id, contentHash: contextPack.contentHash, itemCount: contextPack.itemCount },
    taskCapsule,
    attempt,
    replayed: truth(source.replayed, 'taskAttemptResult.replayed'),
  }
}

export function normalizeAgentAttemptList(value: unknown) {
  const source = exactRecord(value, 'attemptList', ['attempts'])
  return array(source.attempts, 'attemptList.attempts').map(normalizeAgentAttempt)
}

export function normalizeAgentAttemptEvent(value: unknown): AgentAttemptEventDto {
  const detail = 'attemptEvent'
  const source = exactRecord(value, detail, [
    'schemaVersion', 'attemptId', 'sequence', 'versionFrom', 'versionTo', 'stateFrom',
    'stateTo', 'fenceEpochFrom', 'fenceEpochTo', 'kind', 'actorId', 'reason',
    'evidence', 'createdAt',
  ], ['workerId', 'lease', 'exitReason'])
  if (source.schemaVersion !== 'agent-attempt-event/v1') return invalid(`${detail}.schemaVersion is unsupported`)
  const kind = text(source.kind, `${detail}.kind`, true)
  if (!eventKinds.has(kind)) return invalid(`${detail}.kind is unsupported`)
  const sequence = integer(source.sequence, `${detail}.sequence`, 1)
  const versionFrom = integer(source.versionFrom, `${detail}.versionFrom`, 1)
  const versionTo = integer(source.versionTo, `${detail}.versionTo`, 1)
  if (sequence !== versionFrom || versionTo !== versionFrom + 1) {
    return invalid(`${detail} version chain is not contiguous`)
  }
  return {
    schemaVersion: 'agent-attempt-event/v1',
    attemptId: text(source.attemptId, `${detail}.attemptId`, true),
    sequence, versionFrom, versionTo,
    stateFrom: state(source.stateFrom, `${detail}.stateFrom`),
    stateTo: state(source.stateTo, `${detail}.stateTo`),
    fenceEpochFrom: integer(source.fenceEpochFrom, `${detail}.fenceEpochFrom`),
    fenceEpochTo: integer(source.fenceEpochTo, `${detail}.fenceEpochTo`),
    kind,
    actorId: text(source.actorId, `${detail}.actorId`, true),
    workerId: optional(source, 'workerId', (entry, name) => text(entry, name, true), detail),
    reason: text(source.reason, `${detail}.reason`),
    lease: optional(source, 'lease', lease, detail),
    evidence: evidence(source.evidence, `${detail}.evidence`),
    exitReason: optional(source, 'exitReason', (entry, name) => text(entry, name, true), detail),
    createdAt: timestamp(source.createdAt, `${detail}.createdAt`),
  }
}

export function normalizeAgentEventList(value: unknown): AgentEventPageDto {
  const source = exactRecord(value, 'eventPage', ['events', 'afterSequence', 'lastSequence'])
  const afterSequence = integer(source.afterSequence, 'eventPage.afterSequence')
  const lastSequence = integer(source.lastSequence, 'eventPage.lastSequence')
  const events = array(source.events, 'eventPage.events').map(normalizeAgentAttemptEvent)
  let expected = afterSequence + 1
  for (const event of events) {
    if (event.sequence !== expected) return invalid('eventPage.events are not a contiguous suffix')
    expected += 1
  }
  if (lastSequence !== (events.at(-1)?.sequence ?? afterSequence)) {
    return invalid('eventPage.lastSequence does not identify the returned suffix')
  }
  return { events, afterSequence, lastSequence }
}

function fileOperation(value: unknown, detail: string): AgentFileOperationDto {
  const source = exactRecord(value, detail, ['id', 'kind', 'path'], [
    'fromPath', 'expectedHash', 'contentHash', 'byteSize', 'mode',
  ])
  const kind = text(source.kind, `${detail}.kind`, true)
  if (kind !== 'file.upsert' && kind !== 'file.delete') return invalid(`${detail}.kind is unsupported`)
  const operation: AgentFileOperationDto = {
    id: text(source.id, `${detail}.id`, true),
    kind,
    path: text(source.path, `${detail}.path`, true),
    fromPath: optional(source, 'fromPath', (entry, name) => text(entry, name, true), detail),
    expectedHash: optional(source, 'expectedHash', digest, detail),
    contentHash: optional(source, 'contentHash', digest, detail),
    byteSize: optional(source, 'byteSize', integer, detail),
    mode: optional(source, 'mode', (entry, name) => text(entry, name, true), detail),
  }
  if (operation.fromPath || (kind === 'file.upsert'
    ? (!operation.contentHash || operation.byteSize === undefined || !operation.mode)
    : (!operation.expectedHash || operation.contentHash !== undefined || operation.byteSize !== undefined || operation.mode !== undefined))) {
    return invalid(`${detail} does not have the exact ${kind} shape`)
  }
  return operation
}

export function normalizeAgentPlatformPatch(value: unknown): AgentPlatformPatchDto {
  const detail = 'platformPatch'
  const source = exactRecord(value, detail, [
    'schemaVersion', 'attemptId', 'projectId', 'candidateId', 'taskCapsule',
    'configurationHash', 'baseTreeHash', 'proposedTreeHash', 'operations', 'changedBytes', 'contentHash',
  ])
  if (source.schemaVersion !== 'agent-platform-patch/v1') return invalid(`${detail}.schemaVersion is unsupported`)
  const operations = array(source.operations, `${detail}.operations`).map(
    (entry, index) => fileOperation(entry, `${detail}.operations[${index}]`),
  )
  const changedBytes = integer(source.changedBytes, `${detail}.changedBytes`)
  if (operations.length === 0 || operations.reduce((sum, entry) => sum + (entry.byteSize ?? 0), 0) !== changedBytes) {
    return invalid(`${detail}.changedBytes does not bind the non-empty operation list`)
  }
  return {
    schemaVersion: 'agent-platform-patch/v1',
    attemptId: text(source.attemptId, `${detail}.attemptId`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    candidateId: text(source.candidateId, `${detail}.candidateId`, true),
    taskCapsule: exactReference(source.taskCapsule, `${detail}.taskCapsule`),
    configurationHash: digest(source.configurationHash, `${detail}.configurationHash`),
    baseTreeHash: digest(source.baseTreeHash, `${detail}.baseTreeHash`),
    proposedTreeHash: digest(source.proposedTreeHash, `${detail}.proposedTreeHash`),
    operations,
    changedBytes,
    contentHash: digest(source.contentHash, `${detail}.contentHash`),
  }
}

export function normalizeAgentStructuredResult(value: unknown): AgentStructuredResultDto {
  const detail = 'structuredResult'
  const source = exactRecord(value, detail, ['summary', 'changedPaths', 'verification', 'blockers'])
  return {
    summary: text(source.summary, `${detail}.summary`),
    changedPaths: textList(source.changedPaths, `${detail}.changedPaths`),
    verification: array(source.verification, `${detail}.verification`).map((entry, index) => {
      const checkDetail = `${detail}.verification[${index}]`
      const check = exactRecord(entry, checkDetail, ['commandId', 'status', 'note'])
      const status = text(check.status, `${checkDetail}.status`, true)
      if (status !== 'not_run' && status !== 'passed' && status !== 'failed') {
        return invalid(`${checkDetail}.status is unsupported`)
      }
      return {
        commandId: text(check.commandId, `${checkDetail}.commandId`, true), status,
        note: text(check.note, `${checkDetail}.note`),
      }
    }),
    blockers: textList(source.blockers, `${detail}.blockers`),
  }
}

export function normalizeAgentPatchValidation(value: unknown): AgentPatchValidationDto {
  const detail = 'patchValidation'
  const source = exactRecord(value, detail, [
    'schemaVersion', 'scope', 'attemptId', 'projectId', 'taskCapsule', 'patch',
    'patchContentHash', 'baseTreeHash', 'proposedTreeHash', 'checks', 'decision',
    'independentQualityRequired', 'contentHash',
  ])
  if (source.schemaVersion !== 'agent-patch-validation/v1') return invalid(`${detail}.schemaVersion is unsupported`)
  return {
    schemaVersion: 'agent-patch-validation/v1',
    scope: text(source.scope, `${detail}.scope`, true),
    attemptId: text(source.attemptId, `${detail}.attemptId`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    taskCapsule: exactReference(source.taskCapsule, `${detail}.taskCapsule`),
    patch: blobReference(source.patch, `${detail}.patch`),
    patchContentHash: digest(source.patchContentHash, `${detail}.patchContentHash`),
    baseTreeHash: digest(source.baseTreeHash, `${detail}.baseTreeHash`),
    proposedTreeHash: digest(source.proposedTreeHash, `${detail}.proposedTreeHash`),
    checks: array(source.checks, `${detail}.checks`).map((entry, index) => {
      const checkDetail = `${detail}.checks[${index}]`
      const check = exactRecord(entry, checkDetail, ['id', 'status', 'detail'])
      return {
        id: text(check.id, `${checkDetail}.id`, true),
        status: text(check.status, `${checkDetail}.status`, true),
        detail: text(check.detail, `${checkDetail}.detail`, true),
      }
    }),
    decision: text(source.decision, `${detail}.decision`, true),
    independentQualityRequired: truth(source.independentQualityRequired, `${detail}.independentQualityRequired`),
    contentHash: digest(source.contentHash, `${detail}.contentHash`),
  }
}

function fileState(value: unknown, detail: string): AgentPatchFileStateDto {
  const source = exactRecord(value, detail, ['exists'], ['contentHash', 'byteSize', 'mode'])
  const exists = truth(source.exists, `${detail}.exists`)
  if (!exists) {
    if (has(source, 'contentHash') || has(source, 'byteSize') || has(source, 'mode')) {
      return invalid(`${detail} fabricates identity for an absent file`)
    }
    return { exists }
  }
  if (!has(source, 'contentHash') || !has(source, 'byteSize') || !has(source, 'mode')) {
    return invalid(`${detail} omits identity for an existing file`)
  }
  return {
    exists,
    contentHash: digest(source.contentHash, `${detail}.contentHash`),
    byteSize: integer(source.byteSize, `${detail}.byteSize`),
    mode: text(source.mode, `${detail}.mode`, true),
  }
}

function conflict(value: unknown, detail: string): AgentPatchConflictDto {
  const source = exactRecord(value, detail, ['path', 'reason', 'base', 'current', 'proposed'])
  return {
    path: text(source.path, `${detail}.path`, true),
    reason: text(source.reason, `${detail}.reason`, true),
    base: fileState(source.base, `${detail}.base`),
    current: fileState(source.current, `${detail}.current`),
    proposed: fileState(source.proposed, `${detail}.proposed`),
  }
}

function treePointer(value: unknown, detail: string) {
  const source = exactRecord(value, detail, [
    'store', 'ref', 'ownerId', 'treeHash', 'fileCount', 'byteSize', 'contentObjectHash',
  ])
  text(source.store, `${detail}.store`, true)
  text(source.ref, `${detail}.ref`, true)
  text(source.ownerId, `${detail}.ownerId`, true)
  const treeHash = digest(source.treeHash, `${detail}.treeHash`)
  integer(source.fileCount, `${detail}.fileCount`)
  integer(source.byteSize, `${detail}.byteSize`)
  digest(source.contentObjectHash, `${detail}.contentObjectHash`)
  return treeHash
}

function application(
  value: unknown,
  kind: 'merge' | 'undo',
  detail: string,
): AgentPatchApplicationDto {
  const operationKey = kind === 'merge' ? 'mergeId' : 'undoId'
  const schemaVersion = kind === 'merge'
    ? 'agent-patch-merge-application/v1'
    : 'agent-patch-undo-application/v1'
  const source = exactRecord(value, detail, [
    'schemaVersion', operationKey, 'planContentHash', 'projectId', 'candidateId',
    'journalSequenceFrom', 'journalSequenceTo', 'candidateVersionFrom', 'candidateVersionTo',
    'beforeTree', 'afterTree', 'contentHash', 'appliedBy', 'appliedAt',
  ])
  if (source.schemaVersion !== schemaVersion) return invalid(`${detail}.schemaVersion is unsupported`)
  return {
    schemaVersion,
    operationId: text(source[operationKey], `${detail}.${operationKey}`, true),
    planContentHash: digest(source.planContentHash, `${detail}.planContentHash`),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    candidateId: text(source.candidateId, `${detail}.candidateId`, true),
    contentHash: digest(source.contentHash, `${detail}.contentHash`),
    journalSequenceFrom: integer(source.journalSequenceFrom, `${detail}.journalSequenceFrom`, 1),
    journalSequenceTo: integer(source.journalSequenceTo, `${detail}.journalSequenceTo`, 1),
    candidateVersionFrom: integer(source.candidateVersionFrom, `${detail}.candidateVersionFrom`, 1),
    candidateVersionTo: integer(source.candidateVersionTo, `${detail}.candidateVersionTo`, 1),
    beforeTreeHash: treePointer(source.beforeTree, `${detail}.beforeTree`),
    afterTreeHash: treePointer(source.afterTree, `${detail}.afterTree`),
    appliedBy: text(source.appliedBy, `${detail}.appliedBy`, true),
    appliedAt: timestamp(source.appliedAt, `${detail}.appliedAt`),
  }
}

function planCommon(source: UnknownRecord, detail: string) {
  const disposition = text(source.disposition, `${detail}.disposition`, true) as AgentPatchDisposition
  if (disposition !== 'planned' && disposition !== 'conflicted' && disposition !== 'noop') {
    return invalid(`${detail}.disposition is unsupported`)
  }
  return {
    expectedSessionVersion: integer(source.expectedSessionVersion, `${detail}.expectedSessionVersion`, 1),
    expectedSessionEpoch: integer(source.expectedSessionEpoch, `${detail}.expectedSessionEpoch`, 1),
    expectedCandidateVersion: integer(source.expectedCandidateVersion, `${detail}.expectedCandidateVersion`, 1),
    expectedCandidateJournalSequence: integer(
      source.expectedCandidateJournalSequence, `${detail}.expectedCandidateJournalSequence`,
    ),
    expectedWriterLeaseEpoch: integer(source.expectedWriterLeaseEpoch, `${detail}.expectedWriterLeaseEpoch`, 1),
    disposition,
    operations: array(source.operations, `${detail}.operations`).map(
      (entry, index) => fileOperation(entry, `${detail}.operations[${index}]`),
    ),
    conflicts: array(source.conflicts, `${detail}.conflicts`).map(
      (entry, index) => conflict(entry, `${detail}.conflicts[${index}]`),
    ),
    contentHash: digest(source.contentHash, `${detail}.contentHash`),
    createdBy: text(source.createdBy, `${detail}.createdBy`, true),
    createdAt: timestamp(source.createdAt, `${detail}.createdAt`),
  }
}

function mergePlan(value: unknown): AgentPatchMergePlanDto {
  const detail = 'mergePlan'
  const source = exactRecord(value, detail, [
    'schemaVersion', 'id', 'operationId', 'projectId', 'sandboxSessionId', 'candidateId',
    'attemptId', 'attemptVersion', 'patchReference', 'patchRawHash', 'patchContentHash',
    'baseTreeHash', 'currentTreeHash', 'proposedTreeHash', 'plannedTreeHash',
    'expectedSessionVersion', 'expectedSessionEpoch', 'expectedCandidateVersion',
    'expectedCandidateJournalSequence', 'expectedWriterLeaseEpoch', 'disposition',
    'operations', 'conflicts', 'contentHash', 'createdBy', 'createdAt',
  ])
  if (source.schemaVersion !== 'agent-patch-merge-plan/v1') return invalid(`${detail}.schemaVersion is unsupported`)
  return {
    schemaVersion: 'agent-patch-merge-plan/v1',
    id: text(source.id, `${detail}.id`, true), operationId: text(source.operationId, `${detail}.operationId`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    sandboxSessionId: text(source.sandboxSessionId, `${detail}.sandboxSessionId`, true),
    candidateId: text(source.candidateId, `${detail}.candidateId`, true),
    attemptId: text(source.attemptId, `${detail}.attemptId`, true),
    attemptVersion: integer(source.attemptVersion, `${detail}.attemptVersion`, 1),
    patchReference: blobReference(source.patchReference, `${detail}.patchReference`),
    patchRawHash: digest(source.patchRawHash, `${detail}.patchRawHash`),
    patchContentHash: digest(source.patchContentHash, `${detail}.patchContentHash`),
    baseTreeHash: digest(source.baseTreeHash, `${detail}.baseTreeHash`),
    currentTreeHash: digest(source.currentTreeHash, `${detail}.currentTreeHash`),
    proposedTreeHash: digest(source.proposedTreeHash, `${detail}.proposedTreeHash`),
    plannedTreeHash: digest(source.plannedTreeHash, `${detail}.plannedTreeHash`),
    ...planCommon(source, detail),
  }
}

function undoPlan(value: unknown): AgentPatchUndoPlanDto {
  const detail = 'undoPlan'
  const source = exactRecord(value, detail, [
    'schemaVersion', 'id', 'operationId', 'projectId', 'sandboxSessionId', 'candidateId',
    'mergeId', 'mergePlanContentHash', 'mergeApplicationContentHash', 'mergeBeforeTreeHash',
    'mergedTreeHash', 'currentTreeHash', 'plannedTreeHash', 'expectedSessionVersion',
    'expectedSessionEpoch', 'expectedCandidateVersion', 'expectedCandidateJournalSequence',
    'expectedWriterLeaseEpoch', 'disposition', 'operations', 'conflicts', 'contentHash',
    'createdBy', 'createdAt',
  ])
  if (source.schemaVersion !== 'agent-patch-undo-plan/v1') return invalid(`${detail}.schemaVersion is unsupported`)
  return {
    schemaVersion: 'agent-patch-undo-plan/v1',
    id: text(source.id, `${detail}.id`, true), operationId: text(source.operationId, `${detail}.operationId`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    sandboxSessionId: text(source.sandboxSessionId, `${detail}.sandboxSessionId`, true),
    candidateId: text(source.candidateId, `${detail}.candidateId`, true),
    mergeId: text(source.mergeId, `${detail}.mergeId`, true),
    mergePlanContentHash: digest(source.mergePlanContentHash, `${detail}.mergePlanContentHash`),
    mergeApplicationContentHash: digest(source.mergeApplicationContentHash, `${detail}.mergeApplicationContentHash`),
    mergeBeforeTreeHash: digest(source.mergeBeforeTreeHash, `${detail}.mergeBeforeTreeHash`),
    mergedTreeHash: digest(source.mergedTreeHash, `${detail}.mergedTreeHash`),
    currentTreeHash: digest(source.currentTreeHash, `${detail}.currentTreeHash`),
    plannedTreeHash: digest(source.plannedTreeHash, `${detail}.plannedTreeHash`),
    ...planCommon(source, detail),
  }
}

export function normalizeAgentPatchMergeResult(value: unknown): AgentPatchMergeResultDto {
  const source = exactRecord(value, 'mergeResult', ['plan', 'replayed'], ['application', 'session'])
  const plan = mergePlan(source.plan)
  const parsedApplication = optional(source, 'application', (entry, detail) => application(entry, 'merge', detail), 'mergeResult')
  if (parsedApplication && (parsedApplication.operationId !== plan.id || parsedApplication.planContentHash !== plan.contentHash)) {
    return invalid('mergeResult application does not bind its immutable plan')
  }
  return {
    plan,
    application: parsedApplication,
    session: optional(source, 'session', normalizeSandboxSession, 'mergeResult'),
    replayed: truth(source.replayed, 'mergeResult.replayed'),
  }
}

export function normalizeAgentPatchUndoResult(value: unknown): AgentPatchUndoResultDto {
  const source = exactRecord(value, 'undoResult', ['plan', 'replayed'], ['application', 'session'])
  const plan = undoPlan(source.plan)
  const parsedApplication = optional(source, 'application', (entry, detail) => application(entry, 'undo', detail), 'undoResult')
  if (parsedApplication && (parsedApplication.operationId !== plan.id || parsedApplication.planContentHash !== plan.contentHash)) {
    return invalid('undoResult application does not bind its immutable plan')
  }
  return {
    plan,
    application: parsedApplication,
    session: optional(source, 'session', normalizeSandboxSession, 'undoResult'),
    replayed: truth(source.replayed, 'undoResult.replayed'),
  }
}

export function normalizeAgentPatchMergeHistory(value: unknown) {
  const source = exactRecord(value, 'mergeHistory', ['merges'])
  return array(source.merges, 'mergeHistory.merges').map((entry, index): AgentPatchMergeHistoryItemDto => {
    const detail = `mergeHistory.merges[${index}]`
    const item = exactRecord(entry, detail, ['plan'], ['application', 'undo'])
    const plan = mergePlan(item.plan)
    const parsedApplication = optional(item, 'application', (value, name) => application(value, 'merge', name), detail)
    if (parsedApplication && (parsedApplication.operationId !== plan.id || parsedApplication.planContentHash !== plan.contentHash)) {
      return invalid(`${detail}.application does not bind its plan`)
    }
    const parsedUndo = optional(item, 'undo', (value, name) => {
      const undo = exactRecord(value, name, ['plan'], ['application'])
      const parsedPlan = undoPlan(undo.plan)
      const undoApplication = optional(undo, 'application', (entry, nested) => application(entry, 'undo', nested), name)
      if (parsedPlan.mergeId !== plan.id || (undoApplication && (
        undoApplication.operationId !== parsedPlan.id || undoApplication.planContentHash !== parsedPlan.contentHash
      ))) return invalid(`${name} does not bind its merge and undo plans`)
      return { plan: parsedPlan, application: undoApplication }
    }, detail)
    return { plan, application: parsedApplication, undo: parsedUndo }
  })
}
