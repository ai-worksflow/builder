import type { ImplementationProposalDto } from './flow-contract'
import { normalizeImplementationProposal } from './implementation-proposal'

export type SandboxState =
  | 'provisioning'
  | 'starting'
  | 'ready'
  | 'suspending'
  | 'suspended'
  | 'resuming'
  | 'terminating'
  | 'terminated'
  | 'failed'

export type SandboxAction =
  | 'view'
  | 'cancel'
  | 'edit'
  | 'pty'
  | 'process'
  | 'agent'
  | 'checkpoint'
  | 'verify'
  | 'freeze'
  | 'abandon'
  | 'suspend'
  | 'resume'
  | 'terminate'
  | 'view_logs'
  | 'restore_checkpoint'
  | 'new_session'
  | 'view_audit'
  | 'view_snapshots'

export type SandboxStreamChannel =
  | 'control'
  | 'fs'
  | 'pty'
  | 'process'
  | 'port'
  | 'preview-log'
  | 'agent'
  | 'resource'

export const SANDBOX_STREAM_CHANNELS: readonly SandboxStreamChannel[] = [
  'control', 'fs', 'pty', 'process', 'port', 'preview-log', 'agent', 'resource',
]

export const SANDBOX_CONNECTION_TICKET_SCHEMA_VERSION = 'sandbox-connection-ticket/v1'

export interface SandboxConnectionCursorDto {
  readonly channel: SandboxStreamChannel
  readonly lastAckedSeq: number
}

export interface SandboxConnectionTicketDto {
  readonly schemaVersion: typeof SANDBOX_CONNECTION_TICKET_SCHEMA_VERSION
  readonly id: string
  readonly ticket: string
  readonly sessionId: string
  readonly sessionEpoch: number
  readonly channels: readonly SandboxStreamChannel[]
  readonly cursors: readonly SandboxConnectionCursorDto[]
  readonly webSocketPath: string
  readonly expiresAt: string
}

export interface ExactRepositoryRefDto {
  readonly id: string
  readonly contentHash: string
}

export interface ExactWorkspaceRevisionRefDto {
  readonly artifactId: string
  readonly revisionId: string
  readonly contentHash: string
}

export interface CandidateStateDto {
  readonly id: string
  readonly repositorySnapshotId: string
  readonly status: 'active' | 'frozen' | 'abandoned'
  readonly baseTreeHash: string
  readonly treeHash: string
  readonly version: number
  readonly journalSequence: number
  readonly sessionEpoch: number
  readonly writerLeaseEpoch: number
  readonly dirty: boolean
  readonly conflicted: boolean
  readonly stale: boolean
  readonly rebaseRequired: boolean
  readonly updatedAt: string
}

export interface CandidateWriterLeaseDto {
  readonly ownerId: string
  readonly epoch: number
  readonly expiresAt: string
}

export interface CandidateCheckpointRefDto {
  readonly id: string
  readonly contentHash: string
  readonly candidateId: string
  readonly candidateVersion: number
  readonly journalSequence: number
  readonly sessionEpoch: number
  readonly writerLeaseEpoch: number
  readonly treeHash: string
}

export interface SandboxBlockingReasonDto {
  readonly code: string
  readonly actions: readonly SandboxAction[]
  readonly detail: string
}

export interface SandboxServiceDto {
  readonly id: string
  readonly kind: string
  readonly profiles: readonly string[]
  readonly templateRelease: ExactRepositoryRefDto
}

export interface SandboxPortDto {
  readonly name: string
  readonly serviceId: string
  readonly number: number
  readonly protocol: string
}

export interface SandboxSessionDto {
  readonly schemaVersion: string
  readonly id: string
  readonly projectId: string
  readonly actorId: string
  readonly buildManifest: ExactRepositoryRefDto
  readonly buildContract: ExactRepositoryRefDto
  readonly fullStackTemplate: ExactRepositoryRefDto
  readonly templateReleases: readonly ExactRepositoryRefDto[]
  readonly baseWorkspaceRevision?: ExactWorkspaceRevisionRefDto
  readonly runnerImageDigest: string
  readonly candidate: CandidateStateDto
  readonly latestCheckpoint?: CandidateCheckpointRefDto
  readonly sessionEpoch: number
  readonly state: SandboxState
  readonly version: number
  readonly ttl: {
    readonly policy: {
      readonly idleHibernateAfter: number
      readonly maxRuntime: number
    }
    readonly idleDeadline: string
    readonly expiresAt: string
  }
  readonly quota: {
    readonly cpuMillis: number
    readonly memoryBytes: number
    readonly workspaceBytes: number
    readonly pidLimit: number
    readonly previewPortLimit: number
  }
  readonly allowedServices: readonly SandboxServiceDto[]
  readonly allowedPorts: readonly SandboxPortDto[]
  readonly allowedActions: readonly SandboxAction[]
  readonly blockingReasons: readonly SandboxBlockingReasonDto[]
  readonly lastTransition: {
    readonly from?: SandboxState
    readonly to: SandboxState
    readonly reason: string
    readonly at: string
  }
  readonly failureReason?: string
  readonly createdAt: string
  readonly updatedAt: string
}

export interface RepositoryTreeFileDto {
  readonly path: string
  readonly mode: string
  readonly contentHash: string
  readonly byteSize: number
}

export interface RepositoryTreeDto {
  readonly schemaVersion: string
  readonly files: readonly RepositoryTreeFileDto[]
  readonly treeHash: string
}

export interface CandidateWorkspaceDto extends CandidateStateDto {
  readonly schemaVersion: string
  readonly projectId: string
  readonly buildManifest: ExactRepositoryRefDto
  readonly buildContract: ExactRepositoryRefDto
  readonly fullStackTemplate: ExactRepositoryRefDto
  readonly baseWorkspaceRevision?: ExactWorkspaceRevisionRefDto
  readonly currentTree: RepositoryTreeDto
  readonly lease?: CandidateWriterLeaseDto
  readonly createdBy: string
  readonly createdAt: string
}

export interface SandboxRepositoryViewDto {
  readonly session: SandboxSessionDto
  readonly candidate: CandidateWorkspaceDto
  readonly tree: RepositoryTreeDto
}

export interface SandboxFences {
  readonly etag: string
  readonly sessionEpoch: number
  readonly candidateVersion: number
  readonly writerLeaseEpoch: number
  readonly treeHash: string
}

export interface SandboxSessionResultDto {
  readonly session: SandboxSessionDto
  readonly candidate: CandidateWorkspaceDto
}

export interface SandboxFileMutationResultDto {
  readonly session: SandboxSessionDto
  readonly mutation: {
    readonly recovered: boolean
    readonly finalizationPending: boolean
    readonly entry: {
      readonly candidateId: string
      readonly sequence: number
      readonly candidateVersionFrom: number
      readonly candidateVersionTo: number
      readonly operation: {
        readonly id: string
        readonly kind: string
        readonly path: string
        readonly fromPath?: string
        readonly expectedHash?: string
        readonly contentHash?: string
        readonly byteSize?: number
        readonly mode?: string
      }
    }
  }
}

export interface CandidateSnapshotDto {
  readonly schemaVersion: string
  readonly id: string
  readonly projectId: string
  readonly candidateId: string
  readonly candidateVersion: number
  readonly journalSequence: number
  readonly sessionEpoch: number
  readonly writerLeaseEpoch: number
  readonly tree: RepositoryTreeDto
  readonly reason: string
  readonly createdBy: string
  readonly createdAt: string
}

export interface SandboxCheckpointResultDto {
  readonly session: SandboxSessionDto
  readonly checkpoint: CandidateSnapshotDto
}

export interface CandidateImplementationFreezeReceiptDto {
  readonly id: string
  readonly projectId: string
  readonly sessionId: string
  readonly candidateId: string
  readonly candidateSnapshotId: string
  readonly verificationReceipt: ExactRepositoryRefDto
  readonly implementationProposalId: string
  readonly requestKey: string
  readonly requestHash: string
  readonly sessionVersion: number
  readonly candidateVersion: number
  readonly journalSequence: number
  readonly sessionEpoch: number
  readonly writerLeaseEpoch: number
  readonly baseTreeHash: string
  readonly candidateTreeHash: string
  readonly buildManifest: ExactRepositoryRefDto
  readonly buildContract: ExactRepositoryRefDto
  readonly fullStackTemplate: ExactRepositoryRefDto
  readonly baseWorkspaceRevision?: ExactWorkspaceRevisionRefDto
  readonly proposalPayloadHash: string
  readonly operationCount: number
  readonly reason: string
  readonly createdBy: string
  readonly createdAt: string
}

export interface SandboxCandidateFreezeResultDto {
  readonly session: SandboxSessionDto
  readonly candidate: CandidateWorkspaceDto
  readonly proposal: ImplementationProposalDto
  readonly receipt: CandidateImplementationFreezeReceiptDto
  readonly replayed: boolean
}

export type SandboxProcessState = 'starting' | 'running' | 'exited' | 'failed' | 'orphaned'

export interface SandboxProcessDto {
  readonly schemaVersion: string
  readonly id: string
  readonly projectId: string
  readonly sessionId: string
  readonly sessionEpoch: number
  readonly sessionVersionAtCreation: number
  readonly actorId: string
  readonly serviceId: string
  readonly commandId: string
  readonly templateRelease: ExactRepositoryRefDto
  readonly workingDirectory: string
  readonly argv: readonly string[]
  readonly logLimitBytes: number
  readonly state: SandboxProcessState
  readonly version: number
  readonly pid?: number
  readonly exitCode?: number
  readonly failure?: string
  readonly logBytes: number
  readonly logTruncated: boolean
  readonly runtimeStartedAt?: string
  readonly finishedAt?: string
  readonly createdAt: string
  readonly updatedAt: string
}

export interface SandboxProcessResultDto {
  readonly session: SandboxSessionDto
  readonly process: SandboxProcessDto
}

export interface SandboxProcessListDto {
  readonly session: SandboxSessionDto
  readonly processes: readonly SandboxProcessDto[]
}

export interface SandboxProcessLogResultDto extends SandboxProcessResultDto {
  readonly log: {
    readonly schemaVersion: string
    readonly id: string
    readonly offset: number
    readonly nextOffset: number
    readonly valueBase64: string
    readonly eof: boolean
    readonly truncated: boolean
  }
}

export type SandboxTerminalState = 'opening' | 'running' | 'exited' | 'failed' | 'orphaned'

export interface SandboxTerminalDto {
  readonly schemaVersion: string
  readonly id: string
  readonly projectId: string
  readonly sessionId: string
  readonly sessionEpoch: number
  readonly sessionVersionAtCreation: number
  readonly actorId: string
  readonly workingDirectory: string
  readonly shellPath: '/bin/bash'
  readonly rows: number
  readonly columns: number
  readonly outputLimitBytes: number
  readonly state: SandboxTerminalState
  readonly version: number
  readonly exitCode?: number
  readonly failure?: string
  readonly outputBytes: number
  readonly outputTruncated: boolean
  readonly runtimeStartedAt?: string
  readonly finishedAt?: string
  readonly createdAt: string
  readonly updatedAt: string
}

export interface SandboxTerminalResultDto {
  readonly session: SandboxSessionDto
  readonly terminal: SandboxTerminalDto
}

export interface SandboxTerminalListDto {
  readonly session: SandboxSessionDto
  readonly terminals: readonly SandboxTerminalDto[]
}

export type SandboxPortState = 'unavailable' | 'starting' | 'listening'

export interface SandboxRuntimePortDto extends SandboxPortDto {
  readonly schemaVersion: string
  readonly state: SandboxPortState
  readonly healthy: boolean
  readonly previewable: boolean
}

export interface SandboxPortListDto {
  readonly session: SandboxSessionDto
  readonly ports: readonly SandboxRuntimePortDto[]
}

export interface SandboxPreviewLinkDto {
  readonly schemaVersion: string
  readonly id: string
  readonly sessionId: string
  readonly sessionEpoch: number
  readonly port: SandboxRuntimePortDto
  readonly url: string
  readonly expiresAt: string
}

type UnknownRecord = Record<string, unknown>

export class SandboxContractError extends Error {
  constructor(detail: string) {
    super(`The Sandbox service returned a malformed protocol object: ${detail}.`)
    this.name = 'SandboxContractError'
  }
}

const sandboxDigestPattern = /^sha256:[0-9a-f]{64}$/
const sandboxExternalDigestPattern = /^(?:sha256:)?[0-9a-f]{64}$/
const sandboxStates = new Set<SandboxState>([
  'provisioning', 'starting', 'ready', 'suspending', 'suspended', 'resuming',
  'terminating', 'terminated', 'failed',
])
const sandboxActions = new Set<SandboxAction>([
  'view', 'cancel', 'edit', 'pty', 'process', 'agent', 'checkpoint', 'verify', 'freeze',
  'abandon', 'suspend', 'resume', 'terminate', 'view_logs', 'restore_checkpoint',
  'new_session', 'view_audit', 'view_snapshots',
])

function invalidSandbox(detail: string): never {
  throw new SandboxContractError(detail)
}

function owns(source: UnknownRecord, key: string) {
  return Object.prototype.hasOwnProperty.call(source, key)
}

function record(
  value: unknown,
  detail = 'object',
  required?: readonly string[],
  optional: readonly string[] = [],
): UnknownRecord {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return invalidSandbox(`${detail} must be an object`)
  }
  const source = value as UnknownRecord
  if (required) {
    const allowed = new Set([...required, ...optional])
    for (const key of Object.keys(source)) {
      if (!allowed.has(key)) return invalidSandbox(`${detail}.${key} is unknown`)
    }
    for (const key of required) {
      if (!owns(source, key)) return invalidSandbox(`${detail}.${key} is required`)
    }
  }
  return source
}

function text(value: unknown, detail = 'value', nonEmpty = false) {
  if (typeof value !== 'string' || (nonEmpty && value.length === 0)) {
    return invalidSandbox(`${detail} must be ${nonEmpty ? 'a non-empty ' : ''}string`)
  }
  return value
}

function number(value: unknown, detail = 'value', minimum = 0) {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < minimum) {
    return invalidSandbox(`${detail} must be a safe integer greater than or equal to ${minimum}`)
  }
  return value
}

function integer(value: unknown, detail = 'value') {
  if (typeof value !== 'number' || !Number.isSafeInteger(value)) {
    return invalidSandbox(`${detail} must be a safe integer`)
  }
  return value
}

function boolean(value: unknown, detail = 'value') {
  if (typeof value !== 'boolean') return invalidSandbox(`${detail} must be a boolean`)
  return value
}

function list(value: unknown, detail = 'value') {
  if (!Array.isArray(value)) return invalidSandbox(`${detail} must be an array`)
  return value
}

function timestamp(value: unknown, detail: string) {
  const result = text(value, detail, true)
  if (!Number.isFinite(Date.parse(result))) return invalidSandbox(`${detail} must be a timestamp`)
  return result
}

function digest(value: unknown, detail: string, external = false) {
  const result = text(value, detail, true)
  if (!(external ? sandboxExternalDigestPattern : sandboxDigestPattern).test(result)) {
    return invalidSandbox(`${detail} must be a canonical sha256 digest`)
  }
  return result
}

function exactRef(value: unknown, detail = 'exactReference'): ExactRepositoryRefDto {
  const source = record(value, detail, ['id', 'contentHash'])
  return {
    id: text(source.id, `${detail}.id`, true),
    contentHash: digest(source.contentHash, `${detail}.contentHash`, true),
  }
}

function revisionRef(value: unknown, detail: string): ExactWorkspaceRevisionRefDto {
  const source = record(value, detail, ['artifactId', 'revisionId', 'contentHash'])
  return {
    artifactId: text(source.artifactId, `${detail}.artifactId`, true),
    revisionId: text(source.revisionId, `${detail}.revisionId`, true),
    contentHash: digest(source.contentHash, `${detail}.contentHash`, true),
  }
}

function sandboxState(value: unknown, detail: string) {
  const result = text(value, detail, true) as SandboxState
  if (!sandboxStates.has(result)) return invalidSandbox(`${detail} is unsupported`)
  return result
}

function sandboxAction(value: unknown, detail: string) {
  const result = text(value, detail, true) as SandboxAction
  if (!sandboxActions.has(result)) return invalidSandbox(`${detail} is unsupported`)
  return result
}

function treeFile(value: unknown, detail = 'treeFile'): RepositoryTreeFileDto {
  const source = record(value, detail, ['path', 'mode', 'contentHash', 'byteSize'])
  const mode = text(source.mode, `${detail}.mode`, true)
  if (mode !== '100644' && mode !== '100755') return invalidSandbox(`${detail}.mode is unsupported`)
  return {
    path: text(source.path, `${detail}.path`, true),
    mode,
    contentHash: digest(source.contentHash, `${detail}.contentHash`),
    byteSize: number(source.byteSize, `${detail}.byteSize`),
  }
}

export function normalizeRepositoryTree(value: unknown): RepositoryTreeDto {
  const source = record(value, 'repositoryTree', ['schemaVersion', 'files', 'treeHash'])
  if (source.schemaVersion !== 'repository-tree/v1') return invalidSandbox('repositoryTree.schemaVersion is unsupported')
  const files = list(source.files, 'repositoryTree.files').map(
    (entry, index) => treeFile(entry, `repositoryTree.files[${index}]`),
  )
  const seen = new Set<string>()
  for (const file of files) {
    if (seen.has(file.path)) return invalidSandbox('repositoryTree.files contain a duplicate path')
    seen.add(file.path)
  }
  return {
    schemaVersion: 'repository-tree/v1',
    files,
    treeHash: digest(source.treeHash, 'repositoryTree.treeHash'),
  }
}

function candidateState(value: unknown): CandidateStateDto {
  const detail = 'candidateState'
  const source = record(value, detail, [
    'id', 'repositorySnapshotId', 'status', 'baseTreeHash', 'treeHash', 'version',
    'journalSequence', 'sessionEpoch', 'writerLeaseEpoch', 'dirty', 'conflicted',
    'stale', 'rebaseRequired', 'updatedAt',
  ])
  const status = text(source.status, `${detail}.status`, true)
  if (status !== 'active' && status !== 'frozen' && status !== 'abandoned') {
    return invalidSandbox(`${detail}.status is unsupported`)
  }
  return {
    id: text(source.id, `${detail}.id`, true),
    repositorySnapshotId: text(source.repositorySnapshotId, `${detail}.repositorySnapshotId`, true),
    status,
    baseTreeHash: digest(source.baseTreeHash, `${detail}.baseTreeHash`),
    treeHash: digest(source.treeHash, `${detail}.treeHash`),
    version: number(source.version, `${detail}.version`, 1),
    journalSequence: number(source.journalSequence, `${detail}.journalSequence`),
    sessionEpoch: number(source.sessionEpoch, `${detail}.sessionEpoch`, 1),
    writerLeaseEpoch: number(source.writerLeaseEpoch, `${detail}.writerLeaseEpoch`),
    dirty: boolean(source.dirty, `${detail}.dirty`),
    conflicted: boolean(source.conflicted, `${detail}.conflicted`),
    stale: boolean(source.stale, `${detail}.stale`),
    rebaseRequired: boolean(source.rebaseRequired, `${detail}.rebaseRequired`),
    updatedAt: timestamp(source.updatedAt, `${detail}.updatedAt`),
  }
}

function checkpointRef(value: unknown): CandidateCheckpointRefDto | undefined {
  if (value === undefined) return undefined
  const detail = 'latestCheckpoint'
  const source = record(value, detail, [
    'id', 'contentHash', 'candidateId', 'candidateVersion', 'journalSequence',
    'sessionEpoch', 'writerLeaseEpoch', 'treeHash',
  ])
  return {
    id: text(source.id, `${detail}.id`, true), contentHash: digest(source.contentHash, `${detail}.contentHash`, true),
    candidateId: text(source.candidateId, `${detail}.candidateId`, true),
    candidateVersion: number(source.candidateVersion, `${detail}.candidateVersion`, 1),
    journalSequence: number(source.journalSequence, `${detail}.journalSequence`),
    sessionEpoch: number(source.sessionEpoch, `${detail}.sessionEpoch`, 1),
    writerLeaseEpoch: number(source.writerLeaseEpoch, `${detail}.writerLeaseEpoch`),
    treeHash: digest(source.treeHash, `${detail}.treeHash`),
  }
}

export function normalizeSandboxSession(value: unknown): SandboxSessionDto {
  const detail = 'sandboxSession'
  const source = record(value, detail, [
    'schemaVersion', 'id', 'projectId', 'actorId', 'buildManifest', 'buildContract',
    'fullStackTemplate', 'templateReleases', 'runnerImageDigest', 'candidate', 'sessionEpoch',
    'state', 'version', 'ttl', 'quota', 'allowedServices', 'allowedPorts', 'allowedActions',
    'blockingReasons', 'lastTransition', 'createdAt', 'updatedAt',
  ], ['baseWorkspaceRevision', 'latestCheckpoint', 'failureReason'])
  if (source.schemaVersion !== 'sandbox-session/v1') return invalidSandbox(`${detail}.schemaVersion is unsupported`)
  const ttl = record(source.ttl, `${detail}.ttl`, ['policy', 'idleDeadline', 'expiresAt'])
  const policy = record(ttl.policy, `${detail}.ttl.policy`, ['idleHibernateAfter', 'maxRuntime'])
  const quota = record(source.quota, `${detail}.quota`, [
    'cpuMillis', 'memoryBytes', 'workspaceBytes', 'pidLimit', 'previewPortLimit',
  ])
  const transition = record(source.lastTransition, `${detail}.lastTransition`, [
    'to', 'reason', 'at',
  ], ['from'])
  const candidate = candidateState(source.candidate)
  const sessionEpoch = number(source.sessionEpoch, `${detail}.sessionEpoch`, 1)
  if (candidate.sessionEpoch !== sessionEpoch) return invalidSandbox(`${detail}.candidate belongs to a different epoch`)
  return {
    schemaVersion: 'sandbox-session/v1',
    id: text(source.id, `${detail}.id`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    actorId: text(source.actorId, `${detail}.actorId`, true),
    buildManifest: exactRef(source.buildManifest, `${detail}.buildManifest`),
    buildContract: exactRef(source.buildContract, `${detail}.buildContract`),
    fullStackTemplate: exactRef(source.fullStackTemplate, `${detail}.fullStackTemplate`),
    templateReleases: list(source.templateReleases, `${detail}.templateReleases`).map(
      (entry, index) => exactRef(entry, `${detail}.templateReleases[${index}]`),
    ),
    ...(owns(source, 'baseWorkspaceRevision') ? {
      baseWorkspaceRevision: revisionRef(source.baseWorkspaceRevision, `${detail}.baseWorkspaceRevision`),
    } : {}),
    runnerImageDigest: digest(source.runnerImageDigest, `${detail}.runnerImageDigest`),
    candidate,
    latestCheckpoint: owns(source, 'latestCheckpoint') ? checkpointRef(source.latestCheckpoint) : undefined,
    sessionEpoch,
    state: sandboxState(source.state, `${detail}.state`),
    version: number(source.version, `${detail}.version`, 1),
    ttl: {
      policy: {
        idleHibernateAfter: number(policy.idleHibernateAfter, `${detail}.ttl.policy.idleHibernateAfter`, 1),
        maxRuntime: number(policy.maxRuntime, `${detail}.ttl.policy.maxRuntime`, 1),
      },
      idleDeadline: timestamp(ttl.idleDeadline, `${detail}.ttl.idleDeadline`),
      expiresAt: timestamp(ttl.expiresAt, `${detail}.ttl.expiresAt`),
    },
    quota: {
      cpuMillis: number(quota.cpuMillis, `${detail}.quota.cpuMillis`, 1),
      memoryBytes: number(quota.memoryBytes, `${detail}.quota.memoryBytes`, 1),
      workspaceBytes: number(quota.workspaceBytes, `${detail}.quota.workspaceBytes`, 1),
      pidLimit: number(quota.pidLimit, `${detail}.quota.pidLimit`, 1),
      previewPortLimit: number(quota.previewPortLimit, `${detail}.quota.previewPortLimit`),
    },
    allowedServices: list(source.allowedServices, `${detail}.allowedServices`).map((entry, index) => {
      const item = `${detail}.allowedServices[${index}]`
      const service = record(entry, item, ['id', 'kind', 'profiles', 'templateRelease'])
      return {
        id: text(service.id, `${item}.id`, true), kind: text(service.kind, `${item}.kind`, true),
        profiles: list(service.profiles, `${item}.profiles`).map(
          (profile, profileIndex) => text(profile, `${item}.profiles[${profileIndex}]`, true),
        ),
        templateRelease: exactRef(service.templateRelease, `${item}.templateRelease`),
      }
    }),
    allowedPorts: list(source.allowedPorts, `${detail}.allowedPorts`).map((entry, index) => {
      const item = `${detail}.allowedPorts[${index}]`
      const port = record(entry, item, ['name', 'serviceId', 'number', 'protocol'])
      return {
        name: text(port.name, `${item}.name`, true),
        serviceId: text(port.serviceId, `${item}.serviceId`, true),
        number: number(port.number, `${item}.number`, 1),
        protocol: text(port.protocol, `${item}.protocol`, true),
      }
    }),
    allowedActions: list(source.allowedActions, `${detail}.allowedActions`).map(
      (entry, index) => sandboxAction(entry, `${detail}.allowedActions[${index}]`),
    ),
    blockingReasons: list(source.blockingReasons, `${detail}.blockingReasons`).map((entry, index) => {
      const item = `${detail}.blockingReasons[${index}]`
      const reason = record(entry, item, ['code', 'actions', 'detail'])
      return {
        code: text(reason.code, `${item}.code`, true),
        actions: list(reason.actions, `${item}.actions`).map(
          (action, actionIndex) => sandboxAction(action, `${item}.actions[${actionIndex}]`),
        ),
        detail: text(reason.detail, `${item}.detail`, true),
      }
    }),
    lastTransition: {
      ...(owns(transition, 'from') ? { from: sandboxState(transition.from, `${detail}.lastTransition.from`) } : {}),
      to: sandboxState(transition.to, `${detail}.lastTransition.to`),
      reason: text(transition.reason, `${detail}.lastTransition.reason`, true),
      at: timestamp(transition.at, `${detail}.lastTransition.at`),
    },
    failureReason: owns(source, 'failureReason')
      ? text(source.failureReason, `${detail}.failureReason`, true)
      : undefined,
    createdAt: timestamp(source.createdAt, `${detail}.createdAt`),
    updatedAt: timestamp(source.updatedAt, `${detail}.updatedAt`),
  }
}

export function normalizeCandidateWorkspace(value: unknown): CandidateWorkspaceDto {
  const detail = 'candidateWorkspace'
  const source = record(value, detail, [
    'schemaVersion', 'id', 'projectId', 'repositorySnapshotId', 'buildManifest',
    'buildContract', 'fullStackTemplate', 'baseTreeHash', 'currentTree', 'status', 'dirty',
    'conflicted', 'stale', 'rebaseRequired', 'sessionEpoch', 'version', 'journalSequence',
    'writerLeaseEpoch', 'createdBy', 'createdAt', 'updatedAt',
  ], ['baseWorkspaceRevision', 'lease'])
  if (source.schemaVersion !== 'candidate-workspace/v1') {
    return invalidSandbox(`${detail}.schemaVersion is unsupported`)
  }
  const currentTree = normalizeRepositoryTree(source.currentTree)
  const status = text(source.status, `${detail}.status`, true)
  if (status !== 'active' && status !== 'frozen' && status !== 'abandoned') {
    return invalidSandbox(`${detail}.status is unsupported`)
  }
  let parsedLease: CandidateWriterLeaseDto | undefined
  if (owns(source, 'lease')) {
    const lease = record(source.lease, `${detail}.lease`, ['ownerId', 'epoch', 'expiresAt'])
    parsedLease = {
      ownerId: text(lease.ownerId, `${detail}.lease.ownerId`, true),
      epoch: number(lease.epoch, `${detail}.lease.epoch`, 1),
      expiresAt: timestamp(lease.expiresAt, `${detail}.lease.expiresAt`),
    }
  }
  return {
    schemaVersion: 'candidate-workspace/v1',
    id: text(source.id, `${detail}.id`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    repositorySnapshotId: text(source.repositorySnapshotId, `${detail}.repositorySnapshotId`, true),
    buildManifest: exactRef(source.buildManifest, `${detail}.buildManifest`),
    buildContract: exactRef(source.buildContract, `${detail}.buildContract`),
    fullStackTemplate: exactRef(source.fullStackTemplate, `${detail}.fullStackTemplate`),
    baseWorkspaceRevision: owns(source, 'baseWorkspaceRevision')
      ? revisionRef(source.baseWorkspaceRevision, `${detail}.baseWorkspaceRevision`)
      : undefined,
    baseTreeHash: digest(source.baseTreeHash, `${detail}.baseTreeHash`),
    treeHash: currentTree.treeHash,
    currentTree,
    status,
    version: number(source.version, `${detail}.version`, 1),
    journalSequence: number(source.journalSequence, `${detail}.journalSequence`),
    sessionEpoch: number(source.sessionEpoch, `${detail}.sessionEpoch`, 1),
    writerLeaseEpoch: number(source.writerLeaseEpoch, `${detail}.writerLeaseEpoch`),
    dirty: boolean(source.dirty, `${detail}.dirty`),
    conflicted: boolean(source.conflicted, `${detail}.conflicted`),
    stale: boolean(source.stale, `${detail}.stale`),
    rebaseRequired: boolean(source.rebaseRequired, `${detail}.rebaseRequired`),
    updatedAt: timestamp(source.updatedAt, `${detail}.updatedAt`),
    lease: parsedLease,
    createdBy: text(source.createdBy, `${detail}.createdBy`, true),
    createdAt: timestamp(source.createdAt, `${detail}.createdAt`),
  }
}

export function normalizeSandboxRepositoryView(value: unknown): SandboxRepositoryViewDto {
  const source = record(value, 'repositoryView', ['session', 'candidate', 'tree'])
  const session = normalizeSandboxSession(source.session)
  const candidate = normalizeCandidateWorkspace(source.candidate)
  const tree = normalizeRepositoryTree(source.tree)
  if (session.projectId !== candidate.projectId || session.candidate.id !== candidate.id ||
    session.candidate.version !== candidate.version || session.candidate.treeHash !== candidate.treeHash ||
    candidate.currentTree.treeHash !== tree.treeHash ||
    JSON.stringify(candidate.currentTree.files) !== JSON.stringify(tree.files)) {
    return invalidSandbox('repositoryView exact Session, Candidate, and tree identities do not match')
  }
  return { session, candidate, tree }
}

export function normalizeSandboxConnectionTicket(value: unknown): SandboxConnectionTicketDto {
  return parseSandboxConnectionTicket(value)
}

export class SandboxConnectionTicketContractError extends Error {
  constructor(detail: string) {
    super(`The sandbox service returned a malformed connection ticket: ${detail}.`)
    this.name = 'SandboxConnectionTicketContractError'
  }
}

const connectionTicketUUIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const connectionTicketExpiryPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/
const connectionTicketSecretAlphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'
const connectionTicketMaximumFutureMs = 2 * 60_000 + 10_000

function isCanonicalConnectionTicketSecret(value: string) {
  if (value.length !== 43) return false
  let buffered = 0
  let bufferedBits = 0
  let byteLength = 0
  for (const character of value) {
    const sextet = connectionTicketSecretAlphabet.indexOf(character)
    if (sextet < 0) return false
    buffered = (buffered << 6) | sextet
    bufferedBits += 6
    while (bufferedBits >= 8) {
      bufferedBits -= 8
      byteLength += 1
      buffered &= (1 << bufferedBits) - 1
    }
  }
  // 32 bytes encode to 43 unpadded characters with two zero padding bits.
  return byteLength === 32 && bufferedBits === 2 && buffered === 0
}

function invalidConnectionTicket(detail: string): never {
  throw new SandboxConnectionTicketContractError(detail)
}

function connectionTicketRecord(value: unknown, detail: string): UnknownRecord {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return invalidConnectionTicket(detail)
  }
  return value as UnknownRecord
}

function connectionTicketExactKeys(
  source: UnknownRecord,
  detail: string,
  keys: readonly string[],
) {
  const expected = new Set(keys)
  if (Object.keys(source).length !== expected.size ||
    Object.keys(source).some((key) => !expected.has(key)) ||
    keys.some((key) => !Object.prototype.hasOwnProperty.call(source, key))) {
    return invalidConnectionTicket(`${detail} does not have the exact schema shape`)
  }
}

function connectionTicketText(value: unknown, detail: string, maximum: number) {
  if (typeof value !== 'string' || value.length === 0 || value !== value.trim() ||
    value.length > maximum || [...value].some((character) => {
      const code = character.codePointAt(0) ?? 0
      return code < 0x20 || code === 0x7f
    })) {
    return invalidConnectionTicket(detail)
  }
  return value
}

/**
 * Parses a single-use sandbox stream ticket without manufacturing defaults.
 * Every channel must have one same-order cursor so a response cannot silently
 * widen, reorder, or replace the scope requested by the client.
 */
export function parseSandboxConnectionTicket(value: unknown): SandboxConnectionTicketDto {
  const source = connectionTicketRecord(value, 'response must be an object')
  connectionTicketExactKeys(source, 'response', [
    'schemaVersion', 'id', 'ticket', 'sessionId', 'sessionEpoch', 'channels',
    'cursors', 'webSocketPath', 'expiresAt',
  ])
  if (source.schemaVersion !== SANDBOX_CONNECTION_TICKET_SCHEMA_VERSION) {
    return invalidConnectionTicket('schemaVersion is unsupported')
  }
  const id = connectionTicketText(source.id, 'id is invalid', 36)
  if (!connectionTicketUUIDPattern.test(id)) {
    return invalidConnectionTicket('id is not a canonical UUID')
  }
  const ticket = connectionTicketText(source.ticket, 'secret is invalid', 512)
  if (!isCanonicalConnectionTicketSecret(ticket)) {
    return invalidConnectionTicket('secret is not a 256-bit base64url capability')
  }
  const sessionId = connectionTicketText(source.sessionId, 'sessionId is invalid', 256)
  if (!connectionTicketUUIDPattern.test(sessionId)) {
    return invalidConnectionTicket('sessionId is not a canonical UUID')
  }
  if (typeof source.sessionEpoch !== 'number' || !Number.isSafeInteger(source.sessionEpoch) ||
    source.sessionEpoch < 1) {
    return invalidConnectionTicket('sessionEpoch must be a positive safe integer')
  }
  if (!Array.isArray(source.channels) || source.channels.length < 1 ||
    source.channels.length > SANDBOX_STREAM_CHANNELS.length) {
    return invalidConnectionTicket('channels must be a non-empty bounded array')
  }
  const requested = new Set<string>()
  const channels: SandboxStreamChannel[] = []
  for (const value of source.channels) {
    if (typeof value !== 'string' || !SANDBOX_STREAM_CHANNELS.includes(value as SandboxStreamChannel) ||
      requested.has(value)) {
      return invalidConnectionTicket('channels contain an unknown or duplicate value')
    }
    requested.add(value)
    channels.push(value as SandboxStreamChannel)
  }
  const canonicalChannels = SANDBOX_STREAM_CHANNELS.filter((channel) => requested.has(channel))
  if (channels.some((channel, index) => channel !== canonicalChannels[index])) {
    return invalidConnectionTicket('channels are not in canonical order')
  }
  if (!Array.isArray(source.cursors) || source.cursors.length !== channels.length) {
    return invalidConnectionTicket('cursors must cover every channel exactly once')
  }
  const cursors: SandboxConnectionCursorDto[] = source.cursors.map((value, index) => {
    const cursor = connectionTicketRecord(value, 'cursor must be an object')
    connectionTicketExactKeys(cursor, 'cursor', ['channel', 'lastAckedSeq'])
    if (cursor.channel !== channels[index] || typeof cursor.lastAckedSeq !== 'number' ||
      !Number.isSafeInteger(cursor.lastAckedSeq) || cursor.lastAckedSeq < 0) {
      return invalidConnectionTicket('cursor identity or sequence is invalid')
    }
    return { channel: channels[index]!, lastAckedSeq: cursor.lastAckedSeq }
  })
  if (source.webSocketPath !== '/v1/sandbox-stream') {
    return invalidConnectionTicket('webSocketPath is not the fixed sandbox stream route')
  }
  const expiresAt = connectionTicketText(source.expiresAt, 'expiresAt is invalid', 64)
  const expiresAtMillis = Date.parse(expiresAt)
  if (!connectionTicketExpiryPattern.test(expiresAt) || !Number.isFinite(expiresAtMillis)) {
    return invalidConnectionTicket('expiresAt is not a canonical UTC timestamp')
  }
  const observedAtMillis = Date.now()
  if (expiresAtMillis <= observedAtMillis) {
    return invalidConnectionTicket('expiresAt is not in the future')
  }
  if (expiresAtMillis - observedAtMillis > connectionTicketMaximumFutureMs) {
    return invalidConnectionTicket('expiresAt exceeds the authority TTL and clock-skew allowance')
  }
  return {
    schemaVersion: SANDBOX_CONNECTION_TICKET_SCHEMA_VERSION,
    id, ticket, sessionId, sessionEpoch: source.sessionEpoch,
    channels, cursors, webSocketPath: source.webSocketPath, expiresAt,
  }
}

export function sandboxFences(headers: Headers, session: SandboxSessionDto): SandboxFences {
  const integerHeader = (name: string, minimum: number) => {
    const raw = headers.get(name)
    const value = raw === null ? Number.NaN : Number(raw)
    if (raw === null || !/^\d+$/.test(raw) || !Number.isSafeInteger(value) || value < minimum) {
      return invalidSandbox(`response header ${name} is required and must be canonical`)
    }
    return value
  }
  const result = {
    etag: headers.get('x-sandbox-session-etag') ?? '',
    sessionEpoch: integerHeader('x-sandbox-session-epoch', 1),
    candidateVersion: integerHeader('x-candidate-version', 1),
    writerLeaseEpoch: integerHeader('x-writer-lease-epoch', 0),
    treeHash: headers.get('x-candidate-tree-hash') ?? '',
  }
  if (result.etag !== `"sandbox:${session.id}:${session.version}"` ||
    headers.get('x-candidate-id') !== session.candidate.id ||
    integerHeader('x-candidate-journal-sequence', 0) !== session.candidate.journalSequence ||
    result.sessionEpoch !== session.sessionEpoch ||
    result.candidateVersion !== session.candidate.version ||
    result.writerLeaseEpoch !== session.candidate.writerLeaseEpoch ||
    result.treeHash !== session.candidate.treeHash) {
    return invalidSandbox('response fences do not match the exact SandboxSession snapshot')
  }
  return result
}

export function normalizeSandboxSessionResult(value: unknown): SandboxSessionResultDto {
  const source = record(value, 'sandboxSessionResult', ['session', 'candidate'])
  const session = normalizeSandboxSession(source.session)
  const candidate = normalizeCandidateWorkspace(source.candidate)
  if (session.projectId !== candidate.projectId || session.candidate.id !== candidate.id ||
    session.candidate.version !== candidate.version || session.candidate.treeHash !== candidate.treeHash) {
    return invalidSandbox('sandboxSessionResult exact Candidate identity changed')
  }
  return { session, candidate }
}

function validateTreePointer(value: unknown, detail: string) {
  const pointer = record(value, detail, [
    'store', 'ref', 'ownerId', 'treeHash', 'fileCount', 'byteSize', 'contentObjectHash',
  ])
  text(pointer.store, `${detail}.store`, true)
  text(pointer.ref, `${detail}.ref`, true)
  text(pointer.ownerId, `${detail}.ownerId`, true)
  digest(pointer.treeHash, `${detail}.treeHash`)
  number(pointer.fileCount, `${detail}.fileCount`)
  number(pointer.byteSize, `${detail}.byteSize`)
  digest(pointer.contentObjectHash, `${detail}.contentObjectHash`)
}

function normalizeFileOperation(value: unknown, detail: string) {
  const operation = record(value, detail, ['id', 'kind', 'path'], [
    'fromPath', 'expectedHash', 'contentHash', 'byteSize', 'mode',
  ])
  const kind = text(operation.kind, `${detail}.kind`, true)
  if (!['file.upsert', 'file.delete', 'file.rename'].includes(kind)) {
    return invalidSandbox(`${detail}.kind is unsupported`)
  }
  const parsed = {
    id: text(operation.id, `${detail}.id`, true),
    kind,
    path: text(operation.path, `${detail}.path`, true),
    fromPath: owns(operation, 'fromPath') ? text(operation.fromPath, `${detail}.fromPath`, true) : undefined,
    expectedHash: owns(operation, 'expectedHash')
      ? digest(operation.expectedHash, `${detail}.expectedHash`) : undefined,
    contentHash: owns(operation, 'contentHash')
      ? digest(operation.contentHash, `${detail}.contentHash`) : undefined,
    byteSize: owns(operation, 'byteSize') ? number(operation.byteSize, `${detail}.byteSize`) : undefined,
    mode: owns(operation, 'mode') ? text(operation.mode, `${detail}.mode`, true) : undefined,
  }
  if ((kind === 'file.upsert' && (!parsed.contentHash || parsed.byteSize === undefined || !parsed.mode || parsed.fromPath)) ||
    (kind === 'file.delete' && (!parsed.expectedHash || parsed.fromPath || parsed.contentHash ||
      parsed.byteSize !== undefined || parsed.mode)) ||
    (kind === 'file.rename' && (!parsed.expectedHash || !parsed.fromPath || parsed.contentHash ||
      parsed.byteSize !== undefined || parsed.mode))) {
    return invalidSandbox(`${detail} does not have the exact ${kind} shape`)
  }
  return parsed
}

export function normalizeSandboxFileMutationResult(value: unknown): SandboxFileMutationResultDto {
  const source = record(value, 'fileMutationResult', ['session', 'mutation'], ['fileBlob'])
  const mutation = record(source.mutation, 'fileMutationResult.mutation', [
    'entry', 'beforeTree', 'afterTree', 'recovered', 'finalizationPending',
  ])
  const entry = record(mutation.entry, 'fileMutationResult.mutation.entry', [
    'candidateId', 'sequence', 'candidateVersionFrom', 'candidateVersionTo', 'sessionEpoch',
    'leaseEpoch', 'actorId', 'attribution', 'operation', 'beforeTreeHash', 'afterTreeHash', 'createdAt',
  ])
  validateTreePointer(mutation.beforeTree, 'fileMutationResult.mutation.beforeTree')
  validateTreePointer(mutation.afterTree, 'fileMutationResult.mutation.afterTree')
  if (owns(source, 'fileBlob')) {
    const blob = record(source.fileBlob, 'fileMutationResult.fileBlob', [
      'store', 'ref', 'ownerId', 'contentHash', 'byteSize', 'contentObjectHash',
    ])
    text(blob.store, 'fileMutationResult.fileBlob.store', true)
    text(blob.ref, 'fileMutationResult.fileBlob.ref', true)
    text(blob.ownerId, 'fileMutationResult.fileBlob.ownerId', true)
    digest(blob.contentHash, 'fileMutationResult.fileBlob.contentHash')
    number(blob.byteSize, 'fileMutationResult.fileBlob.byteSize')
    digest(blob.contentObjectHash, 'fileMutationResult.fileBlob.contentObjectHash')
  }
  const session = normalizeSandboxSession(source.session)
  const parsedOperation = normalizeFileOperation(entry.operation, 'fileMutationResult.mutation.entry.operation')
  const candidateId = text(entry.candidateId, 'fileMutationResult.mutation.entry.candidateId', true)
  const candidateVersionTo = number(entry.candidateVersionTo, 'fileMutationResult.mutation.entry.candidateVersionTo', 1)
  if (candidateId !== session.candidate.id || candidateVersionTo !== session.candidate.version) {
    return invalidSandbox('fileMutationResult does not bind the returned Candidate head')
  }
  number(entry.sessionEpoch, 'fileMutationResult.mutation.entry.sessionEpoch', 1)
  number(entry.leaseEpoch, 'fileMutationResult.mutation.entry.leaseEpoch')
  text(entry.actorId, 'fileMutationResult.mutation.entry.actorId', true)
  text(entry.attribution, 'fileMutationResult.mutation.entry.attribution', true)
  digest(entry.beforeTreeHash, 'fileMutationResult.mutation.entry.beforeTreeHash')
  digest(entry.afterTreeHash, 'fileMutationResult.mutation.entry.afterTreeHash')
  timestamp(entry.createdAt, 'fileMutationResult.mutation.entry.createdAt')
  return {
    session,
    mutation: {
      recovered: boolean(mutation.recovered, 'fileMutationResult.mutation.recovered'),
      finalizationPending: boolean(mutation.finalizationPending, 'fileMutationResult.mutation.finalizationPending'),
      entry: {
        candidateId,
        sequence: number(entry.sequence, 'fileMutationResult.mutation.entry.sequence', 1),
        candidateVersionFrom: number(entry.candidateVersionFrom, 'fileMutationResult.mutation.entry.candidateVersionFrom', 1),
        candidateVersionTo,
        operation: parsedOperation,
      },
    },
  }
}

export function normalizeSandboxCheckpointResult(value: unknown): SandboxCheckpointResultDto {
  const source = record(value, 'checkpointResult', ['session', 'checkpoint'])
  const checkpoint = record(source.checkpoint, 'checkpointResult.checkpoint', [
    'schemaVersion', 'id', 'projectId', 'candidateId', 'candidateVersion', 'journalSequence',
    'sessionEpoch', 'writerLeaseEpoch', 'tree', 'reason', 'createdBy', 'createdAt',
  ])
  if (checkpoint.schemaVersion !== 'candidate-snapshot/v1') {
    return invalidSandbox('checkpointResult.checkpoint.schemaVersion is unsupported')
  }
  const session = normalizeSandboxSession(source.session)
  if (checkpoint.projectId !== session.projectId || checkpoint.candidateId !== session.candidate.id ||
    checkpoint.candidateVersion !== session.candidate.version || checkpoint.sessionEpoch !== session.sessionEpoch) {
    return invalidSandbox('checkpointResult exact Candidate identity changed')
  }
  return {
    session,
    checkpoint: {
      schemaVersion: 'candidate-snapshot/v1', id: text(checkpoint.id, 'checkpointResult.checkpoint.id', true),
      projectId: text(checkpoint.projectId, 'checkpointResult.checkpoint.projectId', true),
      candidateId: text(checkpoint.candidateId, 'checkpointResult.checkpoint.candidateId', true),
      candidateVersion: number(checkpoint.candidateVersion, 'checkpointResult.checkpoint.candidateVersion', 1),
      journalSequence: number(checkpoint.journalSequence, 'checkpointResult.checkpoint.journalSequence'),
      sessionEpoch: number(checkpoint.sessionEpoch, 'checkpointResult.checkpoint.sessionEpoch', 1),
      writerLeaseEpoch: number(checkpoint.writerLeaseEpoch, 'checkpointResult.checkpoint.writerLeaseEpoch'),
      tree: normalizeRepositoryTree(checkpoint.tree),
      reason: text(checkpoint.reason, 'checkpointResult.checkpoint.reason', true),
      createdBy: text(checkpoint.createdBy, 'checkpointResult.checkpoint.createdBy', true),
      createdAt: timestamp(checkpoint.createdAt, 'checkpointResult.checkpoint.createdAt'),
    },
  }
}

export function normalizeSandboxCandidateFreezeResult(value: unknown): SandboxCandidateFreezeResultDto {
  const source = record(value, 'candidateFreezeResult', [
    'session', 'candidate', 'proposal', 'receipt', 'replayed',
  ])
  const receipt = record(source.receipt, 'candidateFreezeResult.receipt', [
    'id', 'projectId', 'sessionId', 'candidateId', 'candidateSnapshotId',
    'verificationReceipt', 'implementationProposalId', 'requestKey', 'requestHash',
    'sessionVersion', 'candidateVersion', 'journalSequence', 'sessionEpoch',
    'writerLeaseEpoch', 'baseTreeHash', 'candidateTreeHash', 'buildManifest',
    'buildContract', 'fullStackTemplate', 'proposalPayloadHash', 'operationCount',
    'reason', 'createdBy', 'createdAt',
  ], ['baseWorkspaceRevision'])
  const session = normalizeSandboxSession(source.session)
  const candidate = normalizeCandidateWorkspace(source.candidate)
  return {
    session,
    candidate,
    proposal: normalizeImplementationProposal(source.proposal),
    receipt: {
      id: text(receipt.id, 'candidateFreezeResult.receipt.id', true),
      projectId: text(receipt.projectId, 'candidateFreezeResult.receipt.projectId', true),
      sessionId: text(receipt.sessionId, 'candidateFreezeResult.receipt.sessionId', true),
      candidateId: text(receipt.candidateId, 'candidateFreezeResult.receipt.candidateId', true),
      candidateSnapshotId: text(receipt.candidateSnapshotId, 'candidateFreezeResult.receipt.candidateSnapshotId', true),
      verificationReceipt: exactRef(receipt.verificationReceipt, 'candidateFreezeResult.receipt.verificationReceipt'),
      implementationProposalId: text(receipt.implementationProposalId, 'candidateFreezeResult.receipt.implementationProposalId', true),
      requestKey: text(receipt.requestKey, 'candidateFreezeResult.receipt.requestKey', true),
      requestHash: digest(receipt.requestHash, 'candidateFreezeResult.receipt.requestHash'),
      sessionVersion: number(receipt.sessionVersion, 'candidateFreezeResult.receipt.sessionVersion', 1),
      candidateVersion: number(receipt.candidateVersion, 'candidateFreezeResult.receipt.candidateVersion', 1),
      journalSequence: number(receipt.journalSequence, 'candidateFreezeResult.receipt.journalSequence'),
      sessionEpoch: number(receipt.sessionEpoch, 'candidateFreezeResult.receipt.sessionEpoch', 1),
      writerLeaseEpoch: number(receipt.writerLeaseEpoch, 'candidateFreezeResult.receipt.writerLeaseEpoch'),
      baseTreeHash: digest(receipt.baseTreeHash, 'candidateFreezeResult.receipt.baseTreeHash'),
      candidateTreeHash: digest(receipt.candidateTreeHash, 'candidateFreezeResult.receipt.candidateTreeHash'),
      buildManifest: exactRef(receipt.buildManifest, 'candidateFreezeResult.receipt.buildManifest'),
      buildContract: exactRef(receipt.buildContract, 'candidateFreezeResult.receipt.buildContract'),
      fullStackTemplate: exactRef(receipt.fullStackTemplate, 'candidateFreezeResult.receipt.fullStackTemplate'),
      ...(owns(receipt, 'baseWorkspaceRevision') ? {
        baseWorkspaceRevision: revisionRef(
          receipt.baseWorkspaceRevision, 'candidateFreezeResult.receipt.baseWorkspaceRevision',
        ),
      } : {}),
      proposalPayloadHash: digest(receipt.proposalPayloadHash, 'candidateFreezeResult.receipt.proposalPayloadHash'),
      operationCount: number(receipt.operationCount, 'candidateFreezeResult.receipt.operationCount', 1),
      reason: text(receipt.reason, 'candidateFreezeResult.receipt.reason', true),
      createdBy: text(receipt.createdBy, 'candidateFreezeResult.receipt.createdBy', true),
      createdAt: timestamp(receipt.createdAt, 'candidateFreezeResult.receipt.createdAt'),
    },
    replayed: boolean(source.replayed, 'candidateFreezeResult.replayed'),
  }
}

export function normalizeSandboxProcess(value: unknown): SandboxProcessDto {
  const detail = 'sandboxProcess'
  const source = record(value, detail, [
    'schemaVersion', 'id', 'projectId', 'sessionId', 'sessionEpoch',
    'sessionVersionAtCreation', 'actorId', 'serviceId', 'commandId', 'templateRelease',
    'workingDirectory', 'argv', 'logLimitBytes', 'state', 'version', 'logBytes',
    'logTruncated', 'createdAt', 'updatedAt',
  ], ['pid', 'exitCode', 'failure', 'runtimeStartedAt', 'finishedAt'])
  if (source.schemaVersion !== 'sandbox-process/v1') return invalidSandbox(`${detail}.schemaVersion is unsupported`)
  const state = text(source.state, `${detail}.state`, true)
  if (!['starting', 'running', 'exited', 'failed', 'orphaned'].includes(state)) {
    return invalidSandbox(`${detail}.state is unsupported`)
  }
  return {
    schemaVersion: 'sandbox-process/v1', id: text(source.id, `${detail}.id`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    sessionId: text(source.sessionId, `${detail}.sessionId`, true),
    sessionEpoch: number(source.sessionEpoch, `${detail}.sessionEpoch`, 1),
    sessionVersionAtCreation: number(source.sessionVersionAtCreation, `${detail}.sessionVersionAtCreation`, 1),
    actorId: text(source.actorId, `${detail}.actorId`, true),
    serviceId: text(source.serviceId, `${detail}.serviceId`, true),
    commandId: text(source.commandId, `${detail}.commandId`, true),
    templateRelease: exactRef(source.templateRelease, `${detail}.templateRelease`),
    workingDirectory: text(source.workingDirectory, `${detail}.workingDirectory`, true),
    argv: list(source.argv, `${detail}.argv`).map(
      (entry, index) => text(entry, `${detail}.argv[${index}]`, true),
    ),
    logLimitBytes: number(source.logLimitBytes, `${detail}.logLimitBytes`, 1),
    state: state as SandboxProcessState,
    version: number(source.version, `${detail}.version`, 1),
    pid: owns(source, 'pid') ? integer(source.pid, `${detail}.pid`) : undefined,
    exitCode: owns(source, 'exitCode') ? integer(source.exitCode, `${detail}.exitCode`) : undefined,
    failure: owns(source, 'failure') ? text(source.failure, `${detail}.failure`, true) : undefined,
    logBytes: number(source.logBytes, `${detail}.logBytes`),
    logTruncated: boolean(source.logTruncated, `${detail}.logTruncated`),
    runtimeStartedAt: owns(source, 'runtimeStartedAt')
      ? timestamp(source.runtimeStartedAt, `${detail}.runtimeStartedAt`) : undefined,
    finishedAt: owns(source, 'finishedAt') ? timestamp(source.finishedAt, `${detail}.finishedAt`) : undefined,
    createdAt: timestamp(source.createdAt, `${detail}.createdAt`),
    updatedAt: timestamp(source.updatedAt, `${detail}.updatedAt`),
  }
}

export function normalizeSandboxProcessResult(value: unknown): SandboxProcessResultDto {
  const source = record(value, 'sandboxProcessResult', ['session', 'process'])
  const session = normalizeSandboxSession(source.session)
  const process = normalizeSandboxProcess(source.process)
  if (session.id !== process.sessionId || session.projectId !== process.projectId ||
    session.sessionEpoch !== process.sessionEpoch) return invalidSandbox('sandboxProcessResult identity changed')
  return { session, process }
}

export function normalizeSandboxProcessList(value: unknown): SandboxProcessListDto {
  const source = record(value, 'sandboxProcessList', ['session', 'processes'])
  const session = normalizeSandboxSession(source.session)
  const processes = list(source.processes, 'sandboxProcessList.processes').map(normalizeSandboxProcess)
  if (processes.some((process) => process.sessionId !== session.id || process.projectId !== session.projectId ||
    process.sessionEpoch !== session.sessionEpoch)) return invalidSandbox('sandboxProcessList identity widened')
  return { session, processes }
}

export function normalizeSandboxProcessLogResult(value: unknown): SandboxProcessLogResultDto {
  const source = record(value, 'sandboxProcessLogResult', ['session', 'process', 'log'])
  const normalized = normalizeSandboxProcessResult({ session: source.session, process: source.process })
  const log = record(source.log, 'sandboxProcessLogResult.log', [
    'schemaVersion', 'id', 'offset', 'nextOffset', 'value', 'eof', 'truncated',
  ])
  if (log.schemaVersion !== 'sandbox-process/v1' || log.id !== normalized.process.id) {
    return invalidSandbox('sandboxProcessLogResult.log identity changed')
  }
  return {
    ...normalized,
    log: {
      schemaVersion: 'sandbox-process/v1', id: text(log.id, 'sandboxProcessLogResult.log.id', true),
      offset: number(log.offset, 'sandboxProcessLogResult.log.offset'),
      nextOffset: number(log.nextOffset, 'sandboxProcessLogResult.log.nextOffset'),
      valueBase64: text(log.value, 'sandboxProcessLogResult.log.value'),
      eof: boolean(log.eof, 'sandboxProcessLogResult.log.eof'),
      truncated: boolean(log.truncated, 'sandboxProcessLogResult.log.truncated'),
    },
  }
}

export function normalizeSandboxTerminal(value: unknown): SandboxTerminalDto {
  const detail = 'sandboxTerminal'
  const source = record(value, detail, [
    'schemaVersion', 'id', 'projectId', 'sessionId', 'sessionEpoch', 'sessionVersionAtCreation',
    'actorId', 'workingDirectory', 'shellPath', 'rows', 'columns', 'outputLimitBytes',
    'state', 'version', 'outputBytes', 'outputTruncated', 'createdAt', 'updatedAt',
  ], ['exitCode', 'failure', 'runtimeStartedAt', 'finishedAt'])
  if (source.schemaVersion !== 'sandbox-terminal/v1' || source.shellPath !== '/bin/bash') {
    return invalidSandbox(`${detail} schema or shell is unsupported`)
  }
  const state = text(source.state, `${detail}.state`, true)
  if (!['opening', 'running', 'exited', 'failed', 'orphaned'].includes(state)) {
    return invalidSandbox(`${detail}.state is unsupported`)
  }
  return {
    schemaVersion: 'sandbox-terminal/v1', id: text(source.id, `${detail}.id`, true),
    projectId: text(source.projectId, `${detail}.projectId`, true),
    sessionId: text(source.sessionId, `${detail}.sessionId`, true),
    sessionEpoch: number(source.sessionEpoch, `${detail}.sessionEpoch`, 1),
    sessionVersionAtCreation: number(source.sessionVersionAtCreation, `${detail}.sessionVersionAtCreation`, 1),
    actorId: text(source.actorId, `${detail}.actorId`, true),
    workingDirectory: text(source.workingDirectory, `${detail}.workingDirectory`, true),
    shellPath: '/bin/bash', rows: number(source.rows, `${detail}.rows`, 2),
    columns: number(source.columns, `${detail}.columns`, 2),
    outputLimitBytes: number(source.outputLimitBytes, `${detail}.outputLimitBytes`, 1),
    state: state as SandboxTerminalState, version: number(source.version, `${detail}.version`, 1),
    exitCode: owns(source, 'exitCode') ? integer(source.exitCode, `${detail}.exitCode`) : undefined,
    failure: owns(source, 'failure') ? text(source.failure, `${detail}.failure`, true) : undefined,
    outputBytes: number(source.outputBytes, `${detail}.outputBytes`),
    outputTruncated: boolean(source.outputTruncated, `${detail}.outputTruncated`),
    runtimeStartedAt: owns(source, 'runtimeStartedAt')
      ? timestamp(source.runtimeStartedAt, `${detail}.runtimeStartedAt`) : undefined,
    finishedAt: owns(source, 'finishedAt') ? timestamp(source.finishedAt, `${detail}.finishedAt`) : undefined,
    createdAt: timestamp(source.createdAt, `${detail}.createdAt`),
    updatedAt: timestamp(source.updatedAt, `${detail}.updatedAt`),
  }
}

export function normalizeSandboxTerminalResult(value: unknown): SandboxTerminalResultDto {
  const source = record(value, 'sandboxTerminalResult', ['session', 'terminal'])
  const session = normalizeSandboxSession(source.session)
  const terminal = normalizeSandboxTerminal(source.terminal)
  if (terminal.sessionId !== session.id || terminal.projectId !== session.projectId ||
    terminal.sessionEpoch !== session.sessionEpoch) return invalidSandbox('sandboxTerminalResult identity changed')
  return { session, terminal }
}

export function normalizeSandboxTerminalList(value: unknown): SandboxTerminalListDto {
  const source = record(value, 'sandboxTerminalList', ['session', 'terminals'])
  const session = normalizeSandboxSession(source.session)
  const terminals = list(source.terminals, 'sandboxTerminalList.terminals').map(normalizeSandboxTerminal)
  if (terminals.some((terminal) => terminal.sessionId !== session.id || terminal.projectId !== session.projectId ||
    terminal.sessionEpoch !== session.sessionEpoch)) return invalidSandbox('sandboxTerminalList identity widened')
  return { session, terminals }
}

export function normalizeSandboxRuntimePort(value: unknown): SandboxRuntimePortDto {
  const detail = 'sandboxPort'
  const source = record(value, detail, [
    'schemaVersion', 'name', 'serviceId', 'number', 'protocol', 'state', 'healthy', 'previewable',
  ])
  if (source.schemaVersion !== 'sandbox-port/v1') return invalidSandbox(`${detail}.schemaVersion is unsupported`)
  const state = text(source.state, `${detail}.state`, true)
  if (!['unavailable', 'starting', 'listening'].includes(state)) return invalidSandbox(`${detail}.state is unsupported`)
  return {
    schemaVersion: 'sandbox-port/v1', name: text(source.name, `${detail}.name`, true),
    serviceId: text(source.serviceId, `${detail}.serviceId`, true),
    number: number(source.number, `${detail}.number`, 1),
    protocol: text(source.protocol, `${detail}.protocol`, true),
    state: state as SandboxPortState,
    healthy: boolean(source.healthy, `${detail}.healthy`),
    previewable: boolean(source.previewable, `${detail}.previewable`),
  }
}

export function normalizeSandboxPortList(value: unknown): SandboxPortListDto {
  const source = record(value, 'sandboxPortList', ['session', 'ports'])
  return {
    session: normalizeSandboxSession(source.session),
    ports: list(source.ports, 'sandboxPortList.ports').map(normalizeSandboxRuntimePort),
  }
}

export function normalizeSandboxPreviewLink(value: unknown): SandboxPreviewLinkDto {
  const detail = 'sandboxPreviewLink'
  const source = record(value, detail, [
    'schemaVersion', 'id', 'sessionId', 'sessionEpoch', 'port', 'url', 'expiresAt',
  ])
  if (source.schemaVersion !== 'sandbox-preview-grant/v1') return invalidSandbox(`${detail}.schemaVersion is unsupported`)
  return {
    schemaVersion: 'sandbox-preview-grant/v1', id: text(source.id, `${detail}.id`, true),
    sessionId: text(source.sessionId, `${detail}.sessionId`, true),
    sessionEpoch: number(source.sessionEpoch, `${detail}.sessionEpoch`, 1),
    port: normalizeSandboxRuntimePort(source.port),
    url: text(source.url, `${detail}.url`, true),
    expiresAt: timestamp(source.expiresAt, `${detail}.expiresAt`),
  }
}
